package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Job is a row of the jobs table.
type Job struct {
	ID                     int64
	Created                float64
	Started, Finished      *float64
	State, Stage           string
	PathsJSON, OptionsJSON *string
	Total, Done, Errors    int
	Message                *string
	Owner                  *string
	Heartbeat              *float64
	Lane                   *string
	Priority               int
	StagesJSON             *string
}

const jobCols = "id, created, started, finished, state, stage, paths_json, options_json, total, done, errors, message, " +
	"owner, heartbeat, lane, priority, stages_json"

func scanJobs(rows *sql.Rows, err error) ([]Job, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		var created sql.NullFloat64
		var state, stage sql.NullString
		var total, done, errs, prio sql.NullInt64
		if err := rows.Scan(&j.ID, &created, &j.Started, &j.Finished, &state, &stage, jsonText{&j.PathsJSON},
			jsonText{&j.OptionsJSON}, &total, &done, &errs, &j.Message, &j.Owner, &j.Heartbeat, &j.Lane, &prio,
			jsonText{&j.StagesJSON}); err != nil {
			return nil, err
		}
		j.Created, j.State, j.Stage = created.Float64, state.String, stage.String
		j.Total, j.Done, j.Errors, j.Priority = int(total.Int64), int(done.Int64), int(errs.Int64), int(prio.Int64)
		out = append(out, j)
	}
	return out, rows.Err()
}

func one(jobs []Job, err error) (*Job, error) {
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return &jobs[0], nil
}

// AddJob queues a job over paths.
func (d *DB) AddJob(paths []string, options any) (int64, error) {
	p, _ := json.Marshal(paths)
	o, _ := json.Marshal(options)
	var id int64
	err := d.Write(func(tx *Tx) error {
		return tx.QueryRow("INSERT INTO jobs(created, state, stage, paths_json, options_json) VALUES(?,?,?,?,?) RETURNING id",
			Now(), "queued", "queued", string(p), string(o)).Scan(&id)
	})
	return id, err
}

// Jobs lists the newest jobs first.
func (d *DB) Jobs(limit int) ([]Job, error) {
	return scanJobs(d.Query("SELECT "+jobCols+" FROM jobs ORDER BY id DESC LIMIT ?", limit))
}

// Job is one job, or nil.
func (d *DB) Job(id int64) (*Job, error) {
	return one(scanJobs(d.Query("SELECT "+jobCols+" FROM jobs WHERE id=?", id)))
}

// NextQueuedJob is the job to run next: highest priority, then oldest.
func (d *DB) NextQueuedJob() (*Job, error) {
	return one(scanJobs(d.Query("SELECT " + jobCols + " FROM jobs WHERE state='queued' ORDER BY priority DESC, id LIMIT 1")))
}

// NextLocalAheadJob is the first queued job whose local stage hasn't finished yet, other than those in exclude.
func (d *DB) NextLocalAheadJob(exclude map[int64]bool) (*Job, error) {
	jobs, err := scanJobs(d.Query("SELECT " + jobCols + " FROM jobs WHERE state='queued' AND " +
		d.D.J("stages_json", "local", "finished") + " IS NULL ORDER BY priority DESC, id"))
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		if !exclude[jobs[i].ID] {
			return &jobs[i], nil
		}
	}
	return nil, nil
}

// OverrideJob puts a queued (or running-ahead) job at the front of the queue and pauses every other running job:
// their runners see 'preempting', hand them back to the queue and pick this one next. False if it isn't waiting.
func (d *DB) OverrideJob(id int64) (bool, error) {
	ok := false
	err := d.Write(func(tx *Tx) error {
		var x int
		if err := tx.QueryRow("SELECT 1 FROM jobs WHERE id=? AND state IN ('queued','running')", id).Scan(&x); err == sql.ErrNoRows {
			return nil
		} else if err != nil {
			return err
		}
		ok = true
		if _, err := tx.Exec("UPDATE jobs SET priority = (SELECT COALESCE(MAX(priority), 0) + 1 FROM jobs) WHERE id=?", id); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE jobs SET state='preempting' WHERE state='running' AND id != ?", id)
		return err
	})
	return ok, err
}

// JobFields are columns to set on a job row.
type JobFields map[string]any

// UpdateJob sets fields. With owner != "", it only writes while that worker still holds the job, and says whether it
// did.
func (d *DB) UpdateJob(id int64, owner string, f JobFields) (bool, error) {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	sets := make([]string, len(keys))
	args := make([]any, 0, len(keys)+2)
	for i, k := range keys {
		sets[i] = k + "=?"
		args = append(args, f[k])
	}
	q := fmt.Sprintf("UPDATE jobs SET %s WHERE id=?", strings.Join(sets, ", "))
	args = append(args, id)
	if owner != "" {
		q += " AND owner=?"
		args = append(args, owner)
	}
	n, err := affected(d.Exec(q, args...))
	return n > 0, err
}

// A worker holds a job while it keeps the heartbeat fresh. During a rolling restart two workers share this database:
// the new one leaves a job alone while the old one is still beating, and picks it up once the old one hands it back
// (clean shutdown) or its heartbeat goes stale (killed).

// ClaimJob takes a queued job for owner in a lane ("main" or "ahead").
func (d *DB) ClaimJob(id int64, owner, lane string) (bool, error) {
	ok := false
	err := d.Write(func(tx *Tx) error {
		n, err := affected(tx.Exec("UPDATE jobs SET state='running', owner=?, heartbeat=?, lane=? WHERE id=? AND state='queued'",
			owner, Now(), lane, id))
		if err != nil || n == 0 {
			return err
		}
		ok = true
		// images the previous worker had in flight never finished
		_, err = tx.Exec("DELETE FROM job_items WHERE job_id=? AND finished IS NULL", id)
		return err
	})
	return ok, err
}

// Heartbeat renews owner's claim; false when it no longer holds the job.
func (d *DB) Heartbeat(id int64, owner string) (bool, error) {
	return d.UpdateJob(id, owner, JobFields{"heartbeat": Now()})
}

// JobLease is a job's state and owner ("" when unowned or missing).
func (d *DB) JobLease(id int64) (state, owner string, err error) {
	var s, o sql.NullString
	err = d.QueryRow("SELECT state, owner FROM jobs WHERE id=?", id).Scan(&s, &o)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return s.String, o.String, err
}

// RequeueStale: running (or preempting) jobs whose worker went quiet go back in the queue; ones being cancelled end
// as cancelled. Returns their ids.
func (d *DB) RequeueStale(ttl float64) ([]int64, error) {
	now := Now()
	cutoff := now - ttl
	var stale []int64
	err := d.Write(func(tx *Tx) error {
		rows, err := tx.Query("SELECT id FROM jobs WHERE state IN ('running','preempting','cancelling') AND (heartbeat IS NULL OR heartbeat < ?)", cutoff)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			stale = append(stale, id)
		}
		rows.Close()
		for _, id := range stale {
			if _, err := tx.Exec("UPDATE jobs SET state = CASE state WHEN 'cancelling' THEN 'cancelled' ELSE 'queued' END, "+
				"stage = CASE state WHEN 'cancelling' THEN 'done' ELSE 'queued' END, "+
				"finished = CASE state WHEN 'cancelling' THEN ? ELSE finished END, owner=NULL, heartbeat=NULL, lane=NULL, "+
				"message = CASE state WHEN 'cancelling' THEN message ELSE 'requeued: its worker stopped responding' END "+
				"WHERE id=?", now, id); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM job_items WHERE job_id=? AND finished IS NULL", id); err != nil {
				return err
			}
		}
		return nil
	})
	return stale, err
}

// CancelJob cancels a queued job at once, and asks a running one's worker to stop.
func (d *DB) CancelJob(id int64) error {
	_, err := d.Exec("UPDATE jobs SET state = CASE state WHEN 'queued' THEN 'cancelled' ELSE 'cancelling' END, "+
		"finished = CASE state WHEN 'queued' THEN ? ELSE finished END "+
		"WHERE id=? AND state IN ('queued','running','preempting')", Now(), id)
	return err
}

// HeldElsewhere: another worker still holds a job (e.g. the old server in a rolling restart, handing its jobs back).
func (d *DB) HeldElsewhere(owner string) (bool, error) {
	var x int
	err := d.QueryRow("SELECT 1 FROM jobs WHERE state IN ('running','preempting','cancelling') AND owner IS DISTINCT FROM ? LIMIT 1", owner).Scan(&x)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// RunningJob is the id of the job being worked on (the main lane's first), or 0.
func (d *DB) RunningJob() (int64, error) {
	var id int64
	err := d.QueryRow("SELECT id FROM jobs WHERE state IN ('running','preempting','cancelling') ORDER BY COALESCE(lane, '') = 'ahead', id LIMIT 1").Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// ---- job items ------------------------------------------------------------------------------------------------------

// StartJobItem records an image going in flight and returns the item's id.
func (d *DB) StartJobItem(jobID, imageID int64, stage string, started float64) (int64, error) {
	var id int64
	err := d.Write(func(tx *Tx) error {
		return tx.QueryRow("INSERT INTO job_items(job_id, image_id, stage, started) VALUES(?,?,?,?) RETURNING id",
			jobID, imageID, stage, started).Scan(&id)
	})
	return id, err
}

// FinishJobItem stamps an in-flight item with its end, error and usage.
func (d *DB) FinishJobItem(itemID int64, finished float64, errMsg, usage *string) error {
	_, err := d.Exec("UPDATE job_items SET finished=?, seconds="+d.D.Round("? - started", 3)+", error=?, usage_json=? WHERE id=?",
		finished, finished, errMsg, usage, itemID)
	return err
}

// AddJobItem records a finished image that never went through StartJobItem.
func (d *DB) AddJobItem(jobID, imageID int64, stage string, started, finished float64, errMsg, usage *string) error {
	_, err := d.Exec("INSERT INTO job_items(job_id, image_id, stage, started, finished, seconds, error, usage_json) VALUES(?,?,?,?,?,"+
		d.D.Round("CAST(? AS DOUBLE PRECISION) - CAST(? AS DOUBLE PRECISION)", 3)+",?,?)",
		jobID, imageID, stage, started, finished, finished, started, errMsg, usage)
	return err
}

// InFlight is an image a job is working on right now.
type InFlight struct {
	ID      int64
	Stage   string
	Started float64
	Path    *string
}

// InFlight lists a job's unfinished items, oldest first.
func (d *DB) InFlight(jobID int64) ([]InFlight, error) {
	rows, err := d.Query("SELECT ji.image_id, ji.stage, ji.started, i.path FROM job_items ji LEFT JOIN images i ON i.id = ji.image_id "+
		"WHERE ji.job_id = ? AND ji.finished IS NULL ORDER BY ji.started", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InFlight
	for rows.Next() {
		var f InFlight
		var stage sql.NullString
		var started sql.NullFloat64
		if err := rows.Scan(&f.ID, &stage, &started, &f.Path); err != nil {
			return nil, err
		}
		f.Stage, f.Started = stage.String, started.Float64
		out = append(out, f)
	}
	return out, rows.Err()
}

// JobFinishedImages are the images this job already got through in a stage (by an earlier worker, when resuming),
// and how many of them failed.
func (d *DB) JobFinishedImages(jobID int64, stage string) (map[int64]bool, int, error) {
	rows, err := d.Query("SELECT image_id, error IS NOT NULL FROM job_items WHERE job_id = ? AND stage = ? AND finished IS NOT NULL", jobID, stage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	done, failed := map[int64]bool{}, 0
	for rows.Next() {
		var id int64
		var f bool
		if err := rows.Scan(&id, &f); err != nil {
			return nil, 0, err
		}
		done[id] = true
		if f {
			failed++
		}
	}
	return done, failed, rows.Err()
}

// JobItem is a finished item joined with its image's path and current results.
type JobItem struct {
	ID, JobID, ImageID int64
	Stage              string
	Started            float64
	Finished, Seconds  *float64
	Error, UsageJSON   *string
	Path               *string
	LocalJSON, VLMJSON *string
	OverrideJSON       *string
}

// JobItems lists a job's finished items, newest first.
func (d *DB) JobItems(jobID int64, stage string, errorsOnly bool, limit, offset int) ([]JobItem, error) {
	where, args := []string{"ji.job_id = ?", "ji.finished IS NOT NULL"}, []any{jobID}
	if stage != "" {
		where = append(where, "ji.stage = ?")
		args = append(args, stage)
	}
	if errorsOnly {
		where = append(where, "ji.error IS NOT NULL")
	}
	rows, err := d.Query("SELECT ji.id, ji.job_id, ji.image_id, ji.stage, ji.started, ji.finished, ji.seconds, ji.error, ji.usage_json, "+
		"i.path, i.local_json, i.vlm_json, i.override_json FROM job_items ji LEFT JOIN images i ON i.id = ji.image_id WHERE "+
		strings.Join(where, " AND ")+" ORDER BY ji.finished DESC LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobItem
	for rows.Next() {
		var it JobItem
		var stage sql.NullString
		var started sql.NullFloat64
		if err := rows.Scan(&it.ID, &it.JobID, &it.ImageID, &stage, &started, &it.Finished, &it.Seconds, &it.Error,
			jsonText{&it.UsageJSON}, &it.Path, jsonText{&it.LocalJSON}, jsonText{&it.VLMJSON}, jsonText{&it.OverrideJSON}); err != nil {
			return nil, err
		}
		it.Stage, it.Started = stage.String, started.Float64
		out = append(out, it)
	}
	return out, rows.Err()
}

// JobItemCount counts a job's finished items.
func (d *DB) JobItemCount(jobID int64, stage string, errorsOnly bool) (int, error) {
	q, args := "SELECT COUNT(*) FROM job_items WHERE job_id = ? AND finished IS NOT NULL", []any{jobID}
	if stage != "" {
		q += " AND stage = ?"
		args = append(args, stage)
	}
	if errorsOnly {
		q += " AND error IS NOT NULL"
	}
	var n int
	err := d.QueryRow(q, args...).Scan(&n)
	return n, err
}

// Timing is one finished item's numbers, for stats and charts.
type Timing struct {
	Stage             string
	Started, Finished float64
	Seconds           *float64
	Failed            bool
	UsageJSON         *string
}

// JobItemTimings lists every finished item of a job, oldest first.
func (d *DB) JobItemTimings(jobID int64) ([]Timing, error) {
	rows, err := d.Query("SELECT stage, started, finished, seconds, error IS NOT NULL, usage_json FROM job_items "+
		"WHERE job_id = ? AND finished IS NOT NULL ORDER BY finished", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Timing
	for rows.Next() {
		var t Timing
		var stage sql.NullString
		var started, finished sql.NullFloat64
		if err := rows.Scan(&stage, &started, &finished, &t.Seconds, &t.Failed, jsonText{&t.UsageJSON}); err != nil {
			return nil, err
		}
		t.Stage, t.Started, t.Finished = stage.String, started.Float64, finished.Float64
		out = append(out, t)
	}
	return out, rows.Err()
}
