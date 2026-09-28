// Package jobs runs jobs: scan -> local -> vlm for a set of paths, with progress in the jobs table.
//
// One worker runs jobs one after another (the GPU and the local model server are shared). While that job waits on the
// vision model, a second lane runs the scan and local stages of the jobs queued behind it, so the GPU isn't idle; they
// go back in the queue with their local work done and the main lane only has the model left to do. An override puts a
// queued job first and pauses the running one, which goes back in the queue and resumes after it.
//
// Rolling restarts: for a while the old and new server share the database, each with a runner. A runner claims a job
// and holds it with a heartbeat; the other leaves it alone until it is handed back (the old server stopping cleanly)
// or the heartbeat goes stale (killed), and starts nothing until the old one holds no jobs, so they resume in queue
// order, each in its old lane. Every write to the job row is fenced on the claim, so a runner that lost its job can't
// overwrite the new one's progress. A job picked up again resumes: images it already finished count towards
// done/total and aren't redone.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/scan"
)

const (
	HeartbeatEvery = 5 * time.Second  // how often the runner renews its claim on the jobs it holds
	LeaseTTL       = 45.0             // seconds: a claim this old belongs to a dead worker; its job goes back in the queue
	Drain          = 20 * time.Second // on shutdown, how long in-flight images get to finish (k8s grace period is 30 s)
	idle           = 2 * time.Second
)

// Result is a vision model's answer for one image.
type Result struct {
	Key   string
	Data  pj.Obj
	Usage pj.Obj
	Error string
}

// VLM is a vision backend as the runner uses it.
type VLM interface {
	Name() string
	DefaultModel() string
	Sync() bool
	BaseURL() string
	// Classify tags one image (sync backends only). An error fails the job; a model-side failure comes back in Result.
	Classify(ctx context.Context, img db.Image, model string, cfg pj.Obj) (Result, error)
}

// Deps are what the runner calls out to.
type Deps struct {
	DB         *db.DB
	Config     func() pj.Obj // a private copy of the current config
	PhotosRoot string
	CacheDir   string
	// Local runs the local stage on one image and returns its local_json.
	Local func(ctx context.Context, cfg pj.Obj, id int64, path string) (pj.Obj, error)
	// Slots, when set, paces the local stage by analyzer capacity instead of the config's workers.
	Slots Slots
	// Backend resolves a vision backend by name.
	Backend func(name, baseURL string) (VLM, error)
	// Ingest reads Lightroom sidecars for images that have none recorded yet.
	Ingest func(rows []db.Image) error
	Log    *slog.Logger
}

// Slots is analyzer capacity that grows and shrinks (a pool of pods under an HPA). Each image waits for a slot before
// it starts; the slot travels to Local in the context.
type Slots interface {
	Slot(ctx context.Context) (context.Context, func(), error)
	Max() int // how many images may wait for a slot at once
}

// Runner is one worker. Start it with Run; stop it with Shutdown.
type Runner struct {
	Deps
	Owner string

	mu     sync.Mutex
	held   map[int64]bool
	stages map[int64]pj.Obj

	stop      chan struct{} // closed: take no new images (shutdown)
	stopOnce  sync.Once
	done      chan struct{} // closed when Run returns
	aheadStop atomic.Pointer[chan struct{}]
}

// New makes a runner with a unique owner id (host/pid/random).
func New(d Deps) *Runner {
	host, _ := os.Hostname()
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Runner{Deps: d, Owner: fmt.Sprintf("%s/%d/%06x", host, os.Getpid(), rand.Uint32()&0xffffff),
		held: map[int64]bool{}, stages: map[int64]pj.Obj{}, stop: make(chan struct{}), done: make(chan struct{})}
}

func (r *Runner) stopped() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

func (r *Runner) wait(d time.Duration) bool {
	select {
	case <-r.stop:
		return true
	case <-time.After(d):
		return false
	}
}

// ---- helpers ------------------------------------------------------------------------------------------------------

func (r *Runner) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(r.PhotosRoot, p)
}

// job writes to the job row only while this runner still holds it.
func (r *Runner) job(jid int64, f db.JobFields) bool {
	ok, err := r.DB.UpdateJob(jid, r.Owner, f)
	if err != nil {
		r.Log.Error("job update", "job", jid, "err", err)
	}
	return ok
}

// cancelled: stop early; the user cancelled, this server is shutting down, or another runner took the job over.
func (r *Runner) cancelled(jid int64) bool {
	if r.stopped() {
		return true
	}
	state, owner, err := r.DB.JobLease(jid)
	return err != nil || state != "running" || owner != r.Owner
}

// stage records a stage starting (first call) or its settings/outcome (later calls) in stages_json. Starting a stage
// closes the one before it.
func (r *Runner) stage(jid int64, name string, info pj.Obj) {
	r.mu.Lock()
	now, stages := db.Now(), r.stages[jid]
	if stages == nil {
		stages = pj.Obj{}
		r.stages[jid] = stages
	}
	st, ok := stages[name].(pj.Obj)
	if !ok {
		for _, v := range stages {
			if o, ok := v.(pj.Obj); ok {
				if _, has := o["finished"]; !has {
					o["finished"] = now
				}
			}
		}
		st = pj.Obj{"started": now}
		stages[name] = st
	}
	for k, v := range info {
		st[k] = v
	}
	b := pj.Dumps(stages)
	r.mu.Unlock()
	r.job(jid, db.JobFields{"stages_json": b})
}

func (r *Runner) stagesJSON(jid int64) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.stages[jid]
	if !ok {
		return "{}", false
	}
	return pj.Dumps(s), true
}

// ---- main loop ----------------------------------------------------------------------------------------------------

// Run works through the queue until Shutdown.
func (r *Runner) Run() {
	defer close(r.done)
	go r.heartbeat()
	for !r.stopped() {
		stale, err := r.DB.RequeueStale(LeaseTTL)
		if err != nil {
			r.Log.Error("requeue", "err", err)
		}
		for _, jid := range stale {
			r.Log.Warn("its worker stopped responding; requeued", "job", jid)
		}
		// Until the old server has handed back all its jobs, wait: they come back one lane at a time, and picking
		// whichever is first (the local-ahead lane's, usually) would run it in the main lane and leave the job that
		// was with the vision model queued behind it.
		var j *db.Job
		if held, err := r.DB.HeldElsewhere(r.Owner); err == nil && !held {
			j, _ = r.DB.NextQueuedJob()
		}
		if j == nil {
			r.wait(idle)
			continue
		}
		r.guarded(r.RunJob, j)
	}
}

func (r *Runner) guarded(fn func(*db.Job) error, j *db.Job) {
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		return fn(j)
	}()
	if err != nil {
		r.Log.Error("job failed", "job", j.ID, "err", err)
		r.job(j.ID, db.JobFields{"state": "failed", "finished": db.Now(), "owner": nil, "heartbeat": nil, "lane": nil,
			"message": err.Error()})
	}
}

func (r *Runner) heartbeat() {
	t := time.NewTicker(HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.mu.Lock()
			ids := make([]int64, 0, len(r.held))
			for id := range r.held {
				ids = append(ids, id)
			}
			r.mu.Unlock()
			for _, id := range ids {
				r.DB.Heartbeat(id, r.Owner)
			}
		}
	}
}

// Shutdown: the server is stopping (e.g. SIGTERM in a rolling restart): take no new images, give the in-flight ones
// up to timeout to finish, and put the jobs back in the queue so the next server resumes them now rather than after
// the claim goes stale. Anything still running after that is fenced out.
func (r *Runner) Shutdown(timeout time.Duration) {
	r.stopOnce.Do(func() { close(r.stop) })
	select {
	case <-r.done:
	case <-time.After(timeout):
	}
	r.mu.Lock()
	ids := make([]int64, 0, len(r.held))
	for id := range r.held {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, jid := range ids {
		if r.release(jid, false, "handed back to the queue: server restarting") {
			r.Log.Info("still busy at shutdown; handed back anyway", "job", jid)
		}
	}
}

func (r *Runner) stoppedJob(jid int64, message string) {
	state, owner, _ := r.DB.JobLease(jid)
	if owner != r.Owner {
		return // another runner has it now; leave its progress alone
	}
	if state == "cancelling" {
		r.finish(jid, "cancelled")
		return
	}
	if state == "preempting" {
		message = "paused: another job overrode it; resumes after that one"
	}
	if message == "" {
		message = "handed back to the queue: server restarting"
	}
	r.release(jid, true, message)
}

func (r *Runner) release(jid int64, saveStages bool, message string) bool {
	f := db.JobFields{"state": "queued", "stage": "queued", "owner": nil, "heartbeat": nil, "lane": nil, "message": message}
	if saveStages {
		if s, ok := r.stagesJSON(jid); ok {
			f["stages_json"] = s
		}
	}
	return r.job(jid, f)
}

// claim takes a queued job and picks up where it left off: a job that was handed back, requeued, paused by an
// override or run ahead keeps its start time, stage history and finished images.
func (r *Runner) claim(j *db.Job, lane string) (bool, error) {
	ok, err := r.DB.ClaimJob(j.ID, r.Owner, lane)
	if err != nil || !ok {
		return false, err // another runner got it first
	}
	resumed := j.Started != nil
	stages := pj.Obj{}
	if resumed && j.StagesJSON != nil {
		if s := pj.Parse(j.StagesJSON); s != nil {
			stages = s
		}
	}
	r.mu.Lock()
	r.held[j.ID] = true
	r.stages[j.ID] = stages
	r.mu.Unlock()
	if resumed {
		r.job(j.ID, db.JobFields{"stage": "scan", "message": "resumed"})
	} else {
		r.job(j.ID, db.JobFields{"stage": "scan", "started": db.Now(), "message": nil, "done": 0, "errors": 0})
	}
	return true, nil
}

func (r *Runner) unhold(jid int64) {
	r.mu.Lock()
	delete(r.held, jid)
	delete(r.stages, jid)
	r.mu.Unlock()
}

// options are a job's options; a pose model override among them is folded into cfg for its local stage.
func options(j *db.Job, cfg pj.Obj) pj.Obj {
	o := pj.Parse(j.OptionsJSON)
	if o == nil {
		o = pj.Obj{}
	}
	if det := pj.O(o, "detector"); len(det) > 0 {
		merged := pj.Clone(pj.O(cfg, "detector"))
		if merged == nil {
			merged = pj.Obj{}
		}
		for k, v := range det {
			merged[k] = v
		}
		cfg["detector"] = merged
	}
	return o
}

func (r *Runner) paths(j *db.Job) []string {
	var ps []string
	if j.PathsJSON != nil {
		json.Unmarshal([]byte(*j.PathsJSON), &ps)
	}
	for i, p := range ps {
		ps[i] = r.resolve(p)
	}
	return ps
}

func opt(o pj.Obj, k string, def bool) bool {
	v, ok := o[k]
	if !ok || v == nil {
		return def
	}
	return pj.Truthy(v)
}

// RunJob runs one queued job through all its stages in the main lane.
func (r *Runner) RunJob(j *db.Job) error {
	jid := j.ID
	cfg := r.Config()
	ok, err := r.claim(j, "main")
	if err != nil || !ok {
		return err
	}
	defer r.unhold(jid)
	opts := options(j, cfg)
	paths := r.paths(j)
	res, err := r.scanLocal(jid, cfg, paths, opts, func() bool { return r.cancelled(jid) })
	if err != nil {
		return err
	}
	if res == nil {
		r.stoppedJob(jid, "")
		return nil
	}
	if opt(opts, "vlm", true) {
		if err := r.runVLM(jid, cfg, paths, opts, res.errors, res.note); err != nil {
			return err
		}
		if r.cancelled(jid) {
			r.stoppedJob(jid, "")
			return nil
		}
	}
	r.finish(jid, "done")
	return nil
}

// runAhead is the local-ahead lane's share of a job: scan and local, then back in the queue for the vision model (or
// done, for a local-only job). Stopped early, it goes back with what it finished.
func (r *Runner) runAhead(j *db.Job, aheadStop chan struct{}) error {
	jid := j.ID
	cfg := r.Config()
	ok, err := r.claim(j, "ahead")
	if err != nil || !ok {
		return err
	}
	defer r.unhold(jid)
	opts := options(j, cfg)
	paths := r.paths(j)
	stop := func() bool {
		select {
		case <-aheadStop:
			return true
		default:
		}
		return r.cancelled(jid)
	}
	res, err := r.scanLocal(jid, cfg, paths, opts, stop)
	if err != nil {
		return err
	}
	if res == nil {
		msg := "local stage started ahead; waiting its turn"
		if r.stopped() {
			msg = ""
		}
		r.stoppedJob(jid, msg)
		return nil
	}
	r.stage(jid, "local", pj.Obj{"finished": db.Now()})
	if !opt(opts, "vlm", true) {
		r.finish(jid, "done")
		return nil
	}
	r.release(jid, true, res.note+" · waiting for the vision model")
	return nil
}

// localAhead: while the main lane is in the vlm stage, work through the queue's local stages in queue order.
func (r *Runner) localAhead(aheadStop chan struct{}) {
	tried := map[int64]bool{}
	for {
		select {
		case <-aheadStop:
			return
		case <-r.stop:
			return
		default:
		}
		j, err := r.DB.NextLocalAheadJob(tried)
		if err != nil || j == nil {
			select {
			case <-aheadStop:
				return
			case <-r.stop:
				return
			case <-time.After(idle):
			}
			continue
		}
		tried[j.ID] = true
		r.guarded(func(j *db.Job) error { return r.runAhead(j, aheadStop) }, j)
	}
}

func (r *Runner) startAhead(cfg pj.Obj) func() {
	if !opt(cfg, "local_ahead", true) {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.localAhead(stop)
	}()
	// Before the main lane moves on: it may need the local stage (and the job the lane holds) itself.
	return func() {
		close(stop)
		<-done
	}
}

type localResult struct {
	errors int
	note   string
}

// scanLocal runs the scan and local stages; nil when shouldStop says so.
func (r *Runner) scanLocal(jid int64, cfg pj.Obj, paths []string, opts pj.Obj, shouldStop func() bool) (*localResult, error) {
	r.stage(jid, "scan", nil)

	// 1) scan
	files := scan.FindImages(paths, opt(opts, "skip_raw_dupes", true))
	if _, err := r.DB.AddPaths(files); err != nil {
		return nil, err
	}
	if len(paths) > 0 && r.Ingest != nil {
		rows, err := r.DB.RowsUnder(paths, "lr_json IS NULL", nil, "")
		if err != nil {
			return nil, err
		}
		if err := r.Ingest(rows); err != nil {
			return nil, err
		}
	}
	r.stage(jid, "scan", pj.Obj{"files": len(files)})
	if shouldStop() {
		return nil, nil
	}

	// 2) local stage
	cond := "local_json IS NULL"
	if opt(opts, "rescan", false) {
		cond = "1=1"
	}
	prior, localErr, err := r.DB.JobFinishedImages(jid, "local")
	if err != nil {
		return nil, err
	}
	var rows []db.Image
	if len(paths) > 0 {
		all, err := r.DB.RowsUnder(paths, cond, nil, "id, path")
		if err != nil {
			return nil, err
		}
		for _, row := range all {
			if !prior[row.ID] {
				rows = append(rows, row)
			}
		}
	}
	total := len(prior) + len(rows)
	r.job(jid, db.JobFields{"stage": "local", "total": total, "done": len(prior), "errors": localErr})
	r.stage(jid, "local", pj.Obj{"total": total, "workers": cfg["workers"]})
	note := "local: nothing new"
	if len(prior) > 0 {
		note = fmt.Sprintf("local %d/%d", len(prior), total)
	}
	if len(rows) > 0 {
		prog := newProgress(r, jid, "local", len(prior))
		nErr := r.runLocal(cfg, rows, prog, shouldStop)
		localErr += nErr
		r.job(jid, db.JobFields{"done": prog.n(), "errors": localErr})
		note = fmt.Sprintf("local %d/%d", prog.n(), total)
		r.stage(jid, "local", pj.Obj{"done": prog.n(), "errors": localErr, "device": r.device()})
	}
	r.job(jid, db.JobFields{"message": note})
	if shouldStop() {
		return nil, nil
	}
	return &localResult{localErr, note}, nil
}

// Device reports where detection runs, when the local stage knows; set by the server from the analyzer's health.
var Device func() any

func (r *Runner) device() any {
	if Device != nil {
		return Device()
	}
	return nil
}

// runLocal analyzes images concurrently (cfg workers, or as many as there are analyzer slots) and stores the results;
// returns how many failed.
func (r *Runner) runLocal(cfg pj.Obj, rows []db.Image, prog *progress, shouldStop func() bool) int {
	workers := max(1, pj.Int(pj.Or(cfg["workers"], 4.0)))
	if r.Slots != nil {
		workers = max(1, r.Slots.Max())
	}
	stopCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-r.stop:
			cancel()
		case <-stopCtx.Done():
		}
	}()
	work := make(chan db.Image)
	var nErr atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range work {
				if shouldStop() {
					continue // stopped: left for whoever resumes the job
				}
				ctx, release := context.Background(), func() {}
				if r.Slots != nil {
					var err error
					if ctx, release, err = r.Slots.Slot(stopCtx); err != nil {
						continue // shutting down
					}
					if shouldStop() {
						release()
						continue
					}
				}
				prog.start(row.ID)
				data, err := r.Local(ctx, cfg, row.ID, row.Path)
				release()
				var msg *string
				if err == nil {
					s := pj.Dumps(data)
					err = r.DB.SetLocal(row.ID, &s, nil)
				}
				if err != nil { // keep going; record the failure
					m := "local: " + err.Error()
					msg = &m
					r.DB.SetLocal(row.ID, nil, msg)
					nErr.Add(1)
				}
				prog.finish(row.ID, msg, nil)
				prog.update(1)
			}
		}()
	}
	for _, row := range rows {
		work <- row
	}
	close(work)
	wg.Wait()
	return int(nErr.Load())
}

// runVLM: counters switch to the vlm stage only when it has work, so a local-only re-analysis keeps its own
// done/total; errors from both stages add up. The message keeps the local stage's summary.
func (r *Runner) runVLM(jid int64, cfg pj.Obj, paths []string, opts pj.Obj, localErr int, localNote string) error {
	bname := pj.Str(pj.Or(opts["backend"], pj.Or(cfg["backend"], "ollama")))
	backend, err := r.Backend(bname, pj.Str(pj.Or(opts["base_url"], cfg["base_url"])))
	if err != nil {
		return err
	}
	if !backend.Sync() {
		r.job(jid, db.JobFields{"message": fmt.Sprintf("backend %s is batch-only; use the CLI submit/poll for it", bname)})
		return nil
	}
	model := pj.Str(pj.Or(opts["model"], pj.Or(cfg["model"], backend.DefaultModel())))
	conc := pj.Int(pj.Or(opts["concurrency"], pj.Or(cfg["vlm_concurrency"], 1.0)))
	conc = max(conc, 1)
	cond := "local_json IS NOT NULL"
	if !opt(opts, "revlm", false) {
		cond += " AND " + r.DB.D.VLMTodo()
	}
	if !opt(opts, "retry_errors", false) {
		cond += " AND error IS NULL"
	}
	prior, errs, err := r.DB.JobFinishedImages(jid, "vlm")
	if err != nil {
		return err
	}
	var rows []db.Image
	if len(paths) > 0 {
		all, err := r.DB.RowsUnder(paths, cond, nil, "")
		if err != nil {
			return err
		}
		for _, row := range all {
			if !prior[row.ID] {
				rows = append(rows, row)
			}
		}
	}
	skipped := 0
	if opt(opts, "skip_tier0", false) {
		var keep []db.Image
		var skipIDs []int64
		for _, row := range rows {
			if pj.F(pj.Parse(row.LocalJSON)["local_tier"]) == 0 {
				skipped++
				// only untagged ones change state; a tagged image keeps its old tags and stays "tagged"
				if row.VLMJSON == nil {
					skipIDs = append(skipIDs, row.ID)
				}
			} else {
				keep = append(keep, row)
			}
		}
		if err := r.DB.SetVLMSkip(skipIDs, "local tier 0"); err != nil {
			return err
		}
		rows = keep
	}
	prefix, skipNote := "", ""
	if localNote != "" {
		prefix = localNote + " · "
	}
	if skipped > 0 {
		skipNote = fmt.Sprintf(" · skipped %d at local tier 0", skipped)
	}
	if len(rows) == 0 && len(prior) == 0 {
		r.job(jid, db.JobFields{"message": fmt.Sprintf("%s%s/%s: nothing new to tag%s", prefix, bname, model, skipNote)})
		if skipped > 0 { // the stage card then says why nothing went to the model
			r.stage(jid, "vlm", pj.Obj{"total": 0, "skipped": skipped, "backend": bname, "model": model, "concurrency": conc})
		}
		return nil
	}
	total := len(prior) + len(rows)
	r.job(jid, db.JobFields{"stage": "vlm", "total": total, "done": len(prior), "errors": localErr + errs,
		"message": fmt.Sprintf("%s%s/%s%s", prefix, bname, model, skipNote)})
	info := pj.Obj{"total": total, "backend": bname, "model": model, "concurrency": conc, "base_url": nilIfEmpty(backend.BaseURL())}
	if skipped > 0 {
		info["skipped"] = skipped
	}
	r.stage(jid, "vlm", info)
	prog := newProgress(r, jid, "vlm", len(prior))

	stopAhead := r.startAhead(cfg)
	defer stopAhead()
	work := make(chan db.Image)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for range conc {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range work {
				mu.Lock()
				failed := firstErr != nil
				mu.Unlock()
				if failed || r.cancelled(jid) {
					continue
				}
				prog.start(row.ID)
				res, err := backend.Classify(context.Background(), row, model, cfg)
				if err != nil {
					m := err.Error()
					prog.finish(row.ID, &m, nil)
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				if !r.storeVLM(row.ID, res) {
					mu.Lock()
					errs++
					mu.Unlock()
				}
				var em *string
				if res.Error != "" {
					em = &res.Error
				}
				prog.finish(row.ID, em, res.Usage)
				prog.update(1)
			}
		}()
	}
	for _, row := range rows {
		work <- row
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	r.job(jid, db.JobFields{"done": prog.n(), "errors": localErr + errs})
	r.stage(jid, "vlm", pj.Obj{"done": prog.n(), "errors": errs})
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// storeVLM stores a backend result for its image; says whether it carried data.
func (r *Runner) storeVLM(id int64, res Result) bool {
	ok := pj.Truthy(res.Data)
	var data, usage, em *string
	if ok {
		s := pj.Dumps(res.Data)
		data = &s
	} else if res.Error != "" {
		em = &res.Error
	}
	usage = pj.DumpsPtr(res.Usage)
	if err := r.DB.SetVLM(id, data, usage, em); err != nil {
		r.Log.Error("store vlm", "image", id, "err", err)
		return false
	}
	return ok
}

func (r *Runner) finish(jid int64, state string) {
	now := db.Now()
	r.mu.Lock()
	stages := r.stages[jid]
	if stages == nil {
		stages = pj.Obj{}
	}
	for _, v := range stages {
		if o, ok := v.(pj.Obj); ok {
			if _, has := o["finished"]; !has {
				o["finished"] = now
			}
		}
	}
	s := pj.Dumps(stages)
	r.mu.Unlock()
	r.job(jid, db.JobFields{"state": state, "stage": "done", "finished": now, "owner": nil, "heartbeat": nil,
		"lane": nil, "stages_json": s})
}
