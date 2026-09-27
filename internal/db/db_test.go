package db_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/db/dbtest"
	"github.com/mononendev/photosort/internal/pj"
)

func str(s string) *string { return &s }

func addImages(t *testing.T, d *db.DB, names ...string) (string, []int64) {
	t.Helper()
	root := t.TempDir()
	var paths []string
	for _, n := range names {
		p := filepath.Join(root, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		paths = append(paths, p)
	}
	if n, err := d.AddPaths(paths); err != nil || n != len(names) {
		t.Fatalf("AddPaths: %d %v", n, err)
	}
	if n, _ := d.AddPaths(paths); n != 0 {
		t.Fatal("known paths must be skipped")
	}
	rows, err := d.Rows("", nil, "", -1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return root, ids
}

func TestImagesRoundTrip(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		root, ids := addImages(t, d, "a/x_1.jpg", "a/b/y.jpg", "a_b/z.jpg", "c.jpg")
		if len(ids) != 4 {
			t.Fatal(ids)
		}
		// _ in a folder name is literal: a_b is not under a/
		under, err := d.RowsUnder([]string{filepath.Join(root, "a")}, "", nil, "")
		if err != nil || len(under) != 2 {
			t.Fatalf("under a: %d %v", len(under), err)
		}
		files, _ := d.RowsUnder([]string{filepath.Join(root, "c.jpg")}, "", nil, "id, path")
		if len(files) != 1 || files[0].LocalJSON != nil {
			t.Fatalf("files: %+v", files)
		}
		st, err := d.FolderStats(root)
		if err != nil || st[filepath.Join(root, "a")].N != 1 || len(st) != 4 {
			t.Fatalf("folder stats: %+v %v", st, err)
		}

		id := ids[0]
		local := `{"local_tier": 3, "exposure": {"ev": 1.5}, "people": [{"box": [1, 2, 3, 4]}]}`
		if err := d.SetLocal(id, str(local), nil); err != nil {
			t.Fatal(err)
		}
		if err := d.SetVLM(id, str(`{"focus_tier": 2, "keywords": ["<a>"]}`), str(`{"in": 5}`), nil); err != nil {
			t.Fatal(err)
		}
		r, err := d.Row(id)
		if err != nil {
			t.Fatal(err)
		}
		vlm := pj.Parse(r.VLMJSON)
		if vlm["seen_ev"] != 1.5 || pj.A(vlm, "keywords")[0] != "<a>" || r.VLMAt == nil {
			t.Errorf("vlm stamped with the lift: %v", vlm)
		}
		if pj.F(pj.Get(pj.Parse(r.LocalJSON), "exposure", "ev")) != 1.5 {
			t.Error(*r.LocalJSON)
		}
		if n, _ := d.Count(d.D.VLMStale()); n != 0 {
			t.Error("fresh verdict is not stale")
		}
		if n, _ := d.Count(d.D.Review()); n != 1 {
			t.Error("local 3 vs vlm 2 needs review")
		}
		// the local stage re-runs with another lift: the verdict goes stale and drops out of review and the final tier
		d.SetLocal(id, str(`{"local_tier": 1, "exposure": {"ev": 0.5}}`), nil)
		if n, _ := d.Count(d.D.VLMStale()); n != 1 {
			t.Error("stale")
		}
		var tier float64
		d.QueryRow("SELECT "+d.D.FinalTier("vlm")+" FROM images WHERE id=?", id).Scan(&tier)
		if tier != 1 {
			t.Errorf("stale vlm falls back to local: %v", tier)
		}
		d.SetOverride(id, str(`{"focus_tier": 0}`))
		d.QueryRow("SELECT "+d.D.FinalTier("strict")+" FROM images WHERE id=?", id).Scan(&tier)
		if tier != 0 {
			t.Errorf("override wins: %v", tier)
		}
		d.SetLRMany([]db.JSONUpdate{{ID: ids[1], JSON: str("{}")}})
		d.SetTruthMany([]db.JSONUpdate{{ID: ids[1], JSON: str(`{"focus_tier": 3}`)}})
		if n, _ := d.ClearTruth(); n != 1 {
			t.Error("clear truth")
		}
		if n, _ := d.Count(d.D.JNum("lr_json", "rating") + " IS NULL AND lr_json IS NOT NULL"); n != 1 {
			t.Error("lr {} stored")
		}
	})
}

func TestFinalTierStrictAndSources(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		_, ids := addImages(t, d, "1.jpg", "2.jpg", "3.jpg")
		d.SetLocal(ids[0], str(`{"local_tier": 3}`), nil)
		d.SetVLM(ids[0], str(`{"focus_tier": 1}`), nil, nil)
		d.SetLocal(ids[1], str(`{"local_tier": 2}`), nil)
		want := map[string][]any{
			"vlm":    {1.0, 2.0, nil},
			"local":  {3.0, 2.0, nil},
			"strict": {1.0, 2.0, nil},
			"junk":   {1.0, 2.0, nil},
		}
		for src, w := range want {
			rows, err := d.Query("SELECT " + d.D.FinalTier(src) + " FROM images ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			var got []any
			for rows.Next() {
				var v sql.NullFloat64
				rows.Scan(&v)
				if v.Valid {
					got = append(got, v.Float64)
				} else {
					got = append(got, nil)
				}
			}
			rows.Close()
			if !pj.Equal(got, w) {
				t.Errorf("%s: %v want %v", src, got, w)
			}
		}
	})
}

func TestJobsLeaseAndQueue(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		a, _ := d.AddJob([]string{"/p/a"}, pj.Obj{"vlm": true})
		b, _ := d.AddJob([]string{"/p/b"}, pj.Obj{})
		next, _ := d.NextQueuedJob()
		if next.ID != a || *next.PathsJSON != `["/p/a"]` {
			t.Fatalf("oldest first: %+v", next)
		}
		if ok, _ := d.ClaimJob(a, "w1", "main"); !ok {
			t.Fatal("claim")
		}
		if ok, _ := d.ClaimJob(a, "w2", "main"); ok {
			t.Fatal("a claimed job can't be claimed again")
		}
		if held, _ := d.HeldElsewhere("w2"); !held {
			t.Error("w1 holds a job")
		}
		if held, _ := d.HeldElsewhere("w1"); held {
			t.Error("not elsewhere for its owner")
		}
		if ok, _ := d.UpdateJob(a, "w2", db.JobFields{"done": 5}); ok {
			t.Error("fenced on the owner")
		}
		if ok, _ := d.UpdateJob(a, "w1", db.JobFields{"done": 5, "stage": "local"}); !ok {
			t.Error("owner writes")
		}
		item, _ := d.StartJobItem(a, 1, "local", db.Now()-1.2345)
		if fl, _ := d.InFlight(a); len(fl) != 1 {
			t.Error("in flight")
		}
		d.FinishJobItem(item, db.Now(), nil, str(`{"in": 1}`))
		d.AddJobItem(a, 2, "local", 10, 12.5, str("boom"), nil)
		done, failed, _ := d.JobFinishedImages(a, "local")
		if len(done) != 2 || failed != 1 {
			t.Errorf("finished: %v %d", done, failed)
		}
		items, _ := d.JobItems(a, "local", false, 50, 0)
		if len(items) != 2 || *items[0].Seconds < 1.2 || *items[1].Seconds != 2.5 {
			t.Errorf("items: %+v", items)
		}
		if n, _ := d.JobItemCount(a, "", true); n != 1 {
			t.Error("error count")
		}
		// override: b jumps the queue and a is asked to yield
		if ok, _ := d.OverrideJob(b); !ok {
			t.Fatal("override")
		}
		if st, owner, _ := d.JobLease(a); st != "preempting" || owner != "w1" {
			t.Errorf("preempting: %s %s", st, owner)
		}
		if next, _ := d.NextQueuedJob(); next.ID != b {
			t.Error("b first")
		}
		// a's worker dies: after the TTL the job goes back in the queue and its in-flight items are dropped
		d.StartJobItem(a, 3, "vlm", db.Now())
		d.UpdateJob(a, "", db.JobFields{"heartbeat": db.Now() - 100})
		stale, _ := d.RequeueStale(45)
		if len(stale) != 1 || stale[0] != a {
			t.Fatalf("stale: %v", stale)
		}
		j, _ := d.Job(a)
		if j.State != "queued" || j.Owner != nil || *j.Message != "requeued: its worker stopped responding" {
			t.Errorf("requeued: %+v", j)
		}
		if fl, _ := d.InFlight(a); len(fl) != 0 {
			t.Error("in-flight dropped")
		}
		// local-ahead: the first queued job whose local stage hasn't finished
		d.UpdateJob(b, "", db.JobFields{"stages_json": `{"local": {"finished": 5}}`})
		ahead, _ := d.NextLocalAheadJob(map[int64]bool{})
		if ahead == nil || ahead.ID != a {
			t.Errorf("ahead: %+v", ahead)
		}
		if ahead, _ := d.NextLocalAheadJob(map[int64]bool{a: true}); ahead != nil {
			t.Error("excluded")
		}
		d.CancelJob(b)
		if j, _ := d.Job(b); j.State != "cancelled" || j.Finished == nil {
			t.Error("queued job cancels at once")
		}
		if id, _ := d.RunningJob(); id != 0 {
			t.Error("nothing running")
		}
		jobs, _ := d.Jobs(10)
		if len(jobs) != 2 || jobs[0].ID != b {
			t.Error("newest first")
		}
	})
}

func TestBatches(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		_, ids := addImages(t, d, "1.jpg", "2.jpg")
		d.AddBatch("b1", "anthropic", "m", 2, 100)
		d.AddBatch("b1", "anthropic", "m", 2, 101) // resubmitted under the same id
		d.SetBatch(ids, "b1")
		d.SetVLM(ids[0], str(`{"focus_tier": 1}`), nil, nil)
		d.SetVLM(ids[1], nil, nil, str("refused"))
		bs, _ := d.Batches(true)
		if len(bs) != 1 || bs[0].Created != 101 {
			t.Fatalf("%+v", bs)
		}
		d.ClearBatch("b1", false)
		if n, _ := d.Count("batch_id IS NOT NULL"); n != 2 {
			t.Error("errored stays, tagged stays")
		}
		d.ClearBatch("b1", true)
		if n, _ := d.Count("batch_id IS NOT NULL"); n != 1 {
			t.Error("errored released")
		}
		d.SetBatchState("b1", "ended", true)
		if bs, _ := d.Batches(true); len(bs) != 0 {
			t.Error("fetched")
		}
	})
}

// A database written by the Python version, before the four-tier scale: opens, gains the new columns and indexes,
// and its tiers move up.
func TestLegacySQLiteMigrates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "photosort.db")
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE images (id INTEGER PRIMARY KEY, path TEXT UNIQUE NOT NULL, size INTEGER, mtime REAL, local_json TEXT,
		  batch_id TEXT, vlm_json TEXT, vlm_usage TEXT, error TEXT)`,
		`CREATE TABLE batches (id TEXT PRIMARY KEY, backend TEXT, model TEXT, n INTEGER, created REAL, state TEXT, fetched INTEGER DEFAULT 0)`,
		`CREATE TABLE jobs (id INTEGER PRIMARY KEY, created REAL, started REAL, finished REAL, state TEXT, stage TEXT,
		  paths_json TEXT, options_json TEXT, total INTEGER DEFAULT 0, done INTEGER DEFAULT 0, errors INTEGER DEFAULT 0, message TEXT)`,
		`INSERT INTO images(path, local_json, vlm_json) VALUES ('/p/a/1.jpg', '{"local_tier": 2}', '{"focus_tier": 1}')`,
		`INSERT INTO images(path, local_json) VALUES ('/p/2.jpg', '{"local_tier": 0}')`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	d, err := db.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rows, _ := d.Rows("", nil, "id", -1, 0, "")
	if *rows[0].Folder != "/p/a" || pj.F(pj.Parse(rows[0].LocalJSON)["local_tier"]) != 3 ||
		pj.F(pj.Parse(rows[0].VLMJSON)["focus_tier"]) != 2 || pj.F(pj.Parse(rows[1].LocalJSON)["local_tier"]) != 0 {
		t.Errorf("migrated: %s %s %s", *rows[0].LocalJSON, *rows[0].VLMJSON, *rows[1].LocalJSON)
	}
	d.Close()
	// a second open must not raise the tiers again
	d, _ = db.Open(p)
	rows, _ = d.Rows("", nil, "id", -1, 0, "")
	if pj.F(pj.Parse(rows[0].LocalJSON)["local_tier"]) != 3 {
		t.Error("migrated twice")
	}
	if _, err := d.AddJob([]string{"/p"}, nil); err != nil {
		t.Error("jobs gained their columns:", err)
	}
	time.Sleep(0)
}
