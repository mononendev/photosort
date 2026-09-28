package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mononendev/photosort/internal/config"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/db/dbtest"
	"github.com/mononendev/photosort/internal/pj"
)

type fakeVLM struct {
	mu       sync.Mutex
	calls    []int64
	classify func(id int64) // hook, before answering
}

func (f *fakeVLM) Name() string         { return "fake" }
func (f *fakeVLM) DefaultModel() string { return "fake" }
func (f *fakeVLM) Sync() bool           { return true }
func (f *fakeVLM) BaseURL() string      { return "" }
func (f *fakeVLM) Classify(_ context.Context, img db.Image, _ string, _ pj.Obj) (Result, error) {
	f.mu.Lock()
	first := len(f.calls) == 0
	f.calls = append(f.calls, img.ID)
	hook := f.classify
	f.mu.Unlock()
	if hook != nil && first {
		hook(img.ID)
	}
	return Result{Key: fmt.Sprint(img.ID), Data: pj.Obj{"focus_tier": 2.0}, Usage: pj.Obj{}}, nil
}

type fixture struct {
	t      *testing.T
	d      *db.DB
	photos string
	cfg    pj.Obj
	vlm    *fakeVLM
	locals atomic.Int64
	fail   string // base name whose local stage fails
}

func (f *fixture) runner() *Runner {
	return New(Deps{
		DB: f.d, Config: func() pj.Obj { return pj.Clone(f.cfg) }, PhotosRoot: f.photos, CacheDir: f.t.TempDir(),
		Local: func(_ context.Context, _ pj.Obj, id int64, path string) (pj.Obj, error) {
			f.locals.Add(1)
			if filepath.Base(path) == f.fail {
				return nil, errors.New("boom")
			}
			return pj.Obj{"local_tier": 2.0, "people": []any{}}, nil
		},
		Backend: func(string, string) (VLM, error) { return f.vlm, nil },
	})
}

func write(t *testing.T, dir string, names ...string) {
	os.MkdirAll(dir, 0o755)
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte{0xff, 0xd8, 0xff}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func setup(t *testing.T, d *db.DB, names ...string) *fixture {
	photos := filepath.Join(t.TempDir(), "photos")
	write(t, photos, names...)
	return &fixture{t: t, d: d, photos: photos, cfg: config.Defaults(), vlm: &fakeVLM{}}
}

func (f *fixture) job(paths []string, opts pj.Obj) int64 {
	id, err := f.d.AddJob(paths, opts)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) get(id int64) *db.Job {
	j, err := f.d.Job(id)
	if err != nil || j == nil {
		f.t.Fatal(err)
	}
	return j
}

func run(t *testing.T, r *Runner, j *db.Job) {
	t.Helper()
	r.guarded(r.RunJob, j)
}

// tagAll gives every image a local result and a current verdict.
func (f *fixture) tagAll() {
	f.d.AddPaths([]string{filepath.Join(f.photos, "a.jpg"), filepath.Join(f.photos, "b.jpg")})
	rows, _ := f.d.Rows("", nil, "id", -1, 0, "")
	for _, r := range rows {
		f.d.SetLocal(r.ID, ptr(`{"local_tier": 2, "people": []}`), nil)
		f.d.SetVLM(r.ID, ptr(`{"focus_tier": 2}`), ptr("{}"), nil)
	}
}

func ptr(s string) *string { return &s }

func msg(j *db.Job) string {
	if j.Message == nil {
		return ""
	}
	return *j.Message
}

func TestRerunWithNothingForVLMKeepsLocalCounts(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg", "b.jpg", "c.jpg")
		f.fail = "c.jpg"
		f.tagAll()
		id := f.job([]string{f.photos}, pj.Obj{"vlm": true, "rescan": true})
		run(t, f.runner(), f.get(id))
		j := f.get(id)
		if j.State != "done" || j.Done != 3 || j.Total != 3 || j.Errors != 1 {
			t.Errorf("%s done=%d total=%d errors=%d", j.State, j.Done, j.Total, j.Errors)
		}
		if !strings.Contains(msg(j), "local 3/3") || !strings.Contains(msg(j), "nothing new") {
			t.Error(msg(j))
		}
	})
}

func TestAnalyzedOnlyRedoesTrackedImagesWithoutScanning(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg", "b.jpg", "c.jpg", "d.jpg")
		f.tagAll() // a and b are tracked and analyzed; c and d exist on disk only
		id := f.job([]string{f.photos}, pj.Obj{"vlm": false, "rescan": true, "analyzed_only": true})
		run(t, f.runner(), f.get(id))
		j := f.get(id)
		if j.State != "done" || j.Total != 2 || f.locals.Load() != 2 {
			t.Errorf("%s total=%d locals=%d", j.State, j.Total, f.locals.Load())
		}
		if n, _ := d.Count("1=1"); n != 2 {
			t.Errorf("registered files no job was pointed at: %d rows", n)
		}
	})
}

func TestRevlmRetagsAlreadyTaggedImages(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg", "b.jpg")
		f.tagAll()
		id := f.job([]string{f.photos}, pj.Obj{"vlm": true, "revlm": true})
		run(t, f.runner(), f.get(id))
		j := f.get(id)
		if j.State != "done" || j.Done != 2 || j.Total != 2 || strings.Contains(msg(j), "nothing new to tag") {
			t.Errorf("%+v %s", j, msg(j))
		}
	})
}

func TestVerdictOnAnUnliftedFrameIsStaleAndRetagged(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg", "b.jpg")
		f.tagAll()
		rows, _ := d.Rows("", nil, "id", -1, 0, "")
		a, b := rows[0].ID, rows[1].ID
		d.SetLocal(a, ptr(`{"local_tier": 3, "people": [], "exposure": {"ev": 4.0, "source": "raw"}}`), nil)
		d.SetLocal(b, ptr(`{"local_tier": 3, "people": []}`), nil) // disagrees with the model's 2, but on the same frame
		ids := func(where string) []int64 {
			rs, _ := d.Rows(where, nil, "id", -1, 0, "id")
			var out []int64
			for _, r := range rs {
				out = append(out, r.ID)
			}
			return out
		}
		if got := ids(d.D.VLMStale()); len(got) != 1 || got[0] != a {
			t.Errorf("stale: %v", got)
		}
		if got := ids(d.D.Review()); len(got) != 1 || got[0] != b {
			t.Errorf("review: %v", got)
		}
		id := f.job([]string{f.photos}, pj.Obj{"vlm": true})
		run(t, f.runner(), f.get(id))
		j := f.get(id)
		if j.State != "done" || j.Done != 1 || j.Total != 1 {
			t.Errorf("%+v", j)
		}
		row, _ := d.Row(a)
		if pj.Parse(row.VLMJSON)["seen_ev"] != 4.0 {
			t.Error(*row.VLMJSON)
		}
		if n, _ := d.Count(d.D.VLMStale()); n != 0 {
			t.Error("still stale")
		}
	})
}

func TestVLMStageTakesOverCountersAndAddsErrors(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg", "b.jpg", "c.jpg")
		f.fail = "c.jpg"
		id := f.job([]string{f.photos}, pj.Obj{"vlm": true})
		run(t, f.runner(), f.get(id))
		j := f.get(id)
		if j.State != "done" || j.Stage != "done" || j.Done != 2 || j.Total != 2 || j.Errors != 1 {
			t.Errorf("%s %s done=%d total=%d errors=%d", j.State, j.Stage, j.Done, j.Total, j.Errors)
		}
		if !strings.HasPrefix(msg(j), "local 3/3 · ") {
			t.Error(msg(j))
		}
		items, _ := d.JobItems(id, "vlm", false, 50, 0)
		if len(items) != 2 {
			t.Errorf("vlm items: %d", len(items))
		}
		fl, _ := d.InFlight(id)
		stages := pj.Parse(j.StagesJSON)
		if len(fl) != 0 || pj.Get(stages, "vlm", "model") != "fake" || pj.F(pj.Get(stages, "vlm", "done")) != 2 {
			t.Errorf("stages: %v", stages)
		}
		for _, k := range []string{"scan", "local", "vlm"} {
			if pj.Get(stages, k, "finished") == nil {
				t.Errorf("%s not closed", k)
			}
		}
		if row, _ := d.Rows("error IS NOT NULL", nil, "", -1, 0, ""); len(row) != 1 || !strings.HasPrefix(*row[0].Error, "local: boom") {
			t.Errorf("error stored: %+v", row)
		}
	})
}

func TestJobOver1500SelectedFilesRuns(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d)
		var paths []string
		for i := range 1500 {
			n := fmt.Sprintf("IMG_%04d.jpg", i)
			write(t, f.photos, n)
			paths = append(paths, filepath.Join(f.photos, n))
		}
		id := f.job(paths, pj.Obj{"vlm": false})
		run(t, f.runner(), f.get(id))
		if j := f.get(id); j.State != "done" || j.Done != 1500 || j.Total != 1500 {
			t.Errorf("%+v", j)
		}
	})
}

// ---- rolling restarts: two runners on one database -----------------------------------------------------------------

func TestSecondRunnerLeavesALiveJobAlone(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg")
		old := f.runner()
		id := f.job([]string{f.photos}, pj.Obj{"vlm": true})
		d.ClaimJob(id, old.Owner, "main")
		nw := f.runner()
		if stale, _ := d.RequeueStale(LeaseTTL); len(stale) != 0 {
			t.Error("live job requeued")
		}
		run(t, nw, f.get(id))
		if st, owner, _ := d.JobLease(id); st != "running" || owner != old.Owner {
			t.Error("taken over")
		}
		if nw.job(id, db.JobFields{"done": 99}) || f.get(id).Done != 0 {
			t.Error("fenced")
		}
	})
}

func TestShutdownMidVLMHandsBackAndNextRunnerResumes(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg", "b.jpg")
		f.fail = "none"
		old := f.runner()
		// the server gets SIGTERM while the first image is with the model
		f.vlm.classify = func(int64) { old.stopOnce.Do(func() { close(old.stop) }) }
		id := f.job([]string{f.photos}, pj.Obj{"vlm": true})
		run(t, old, f.get(id))
		j := f.get(id)
		if j.State != "queued" || j.Owner != nil || j.Done != 1 || j.Total != 2 {
			t.Fatalf("%s owner=%v done=%d total=%d", j.State, j.Owner, j.Done, j.Total) // the in-flight image finished
		}
		started := *j.Started
		f.vlm.classify = nil
		run(t, f.runner(), j)
		j = f.get(id)
		if j.State != "done" || j.Done != 2 || j.Total != 2 || *j.Started != started {
			t.Errorf("%+v", j)
		}
		if len(f.vlm.calls) != 2 || f.vlm.calls[0] == f.vlm.calls[1] {
			t.Errorf("tagged twice: %v", f.vlm.calls) // nothing tagged twice
		}
		if n, _ := d.JobItemCount(id, "vlm", false); n != 2 || pj.Get(pj.Parse(j.StagesJSON), "vlm", "finished") == nil {
			t.Error("items/stages")
		}
	})
}

func TestRunnerThatLostItsJobWritesNothing(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg")
		old := f.runner()
		id := f.job([]string{f.photos}, pj.Obj{"vlm": true})
		d.ClaimJob(id, old.Owner, "main")
		d.UpdateJob(id, "", db.JobFields{"heartbeat": 0.0})
		d.RequeueStale(LeaseTTL)
		d.ClaimJob(id, "new-server", "main")
		old.stoppedJob(id, "")
		old.finish(id, "done")
		if st, owner, _ := d.JobLease(id); st != "running" || owner != "new-server" {
			t.Errorf("%s %s", st, owner)
		}
	})
}

// The old server's local-ahead lane hands its job back first; the new server must not start it in its main lane
// while the job that was with the vision model is still on its way back.
func TestNewRunnerWaitsForEveryJobToBeHandedBack(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d)
		main := f.job(nil, pj.Obj{"vlm": true})
		ahead := f.job(nil, pj.Obj{"vlm": false})
		d.ClaimJob(main, "old", "main")
		d.ClaimJob(ahead, "old", "ahead")
		nw := f.runner()
		var picked []int64
		var mu sync.Mutex
		d.UpdateJob(ahead, "", db.JobFields{"state": "queued", "owner": nil, "lane": nil}) // the lane lets go first
		go func() {
			for !nw.stopped() {
				var j *db.Job
				if held, _ := d.HeldElsewhere(nw.Owner); !held {
					j, _ = d.NextQueuedJob()
				}
				if j != nil {
					mu.Lock()
					picked = append(picked, j.ID)
					mu.Unlock()
					nw.stopOnce.Do(func() { close(nw.stop) })
					return
				}
				nw.wait(20 * time.Millisecond)
			}
		}()
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		if len(picked) != 0 {
			t.Fatal("started before the old server let go of everything")
		}
		mu.Unlock()
		d.UpdateJob(main, "", db.JobFields{"state": "queued", "owner": nil, "lane": nil})
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(picked)
			mu.Unlock()
			if n > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(picked) != 1 || picked[0] != main {
			t.Errorf("picked %v", picked)
		}
	})
}

// ---- two lanes: local ahead while the model is busy; overrides -----------------------------------------------------

func TestLocalStageRunsAheadWhileTheModelIsBusy(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg", "b.jpg")
		other := filepath.Join(filepath.Dir(f.photos), "other")
		write(t, other, "c.jpg")
		a := f.job([]string{f.photos}, pj.Obj{"vlm": true})
		b := f.job([]string{other}, pj.Obj{"vlm": true})
		c := f.job([]string{other}, pj.Obj{"vlm": false})
		// the model is busy with a's first image until the lane has done b's and c's local stage
		f.vlm.classify = func(int64) {
			deadline := time.Now().Add(10 * time.Second)
			for f.get(c).State != "done" && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
		}
		r := f.runner()
		run(t, r, f.get(a))
		jb := f.get(b)
		if jb.State != "queued" || jb.Lane != nil || jb.Owner != nil || jb.Done != 1 || jb.Total != 1 ||
			!strings.HasSuffix(msg(jb), "waiting for the vision model") || pj.Get(pj.Parse(jb.StagesJSON), "local", "finished") == nil {
			t.Fatalf("%+v %s", jb, msg(jb))
		}
		if f.get(c).State != "done" {
			t.Error("local-only: the lane finishes it")
		}
		if j, _ := d.NextLocalAheadJob(map[int64]bool{}); j != nil {
			t.Error("nothing left to run ahead")
		}
		if j, _ := d.NextQueuedJob(); j.ID != b {
			t.Error("b next")
		}
		f.vlm.classify = nil
		nLocal := f.locals.Load()
		run(t, r, f.get(b))
		jb = f.get(b)
		if jb.State != "done" || jb.Done != 1 || jb.Total != 1 || f.locals.Load() != nLocal {
			t.Errorf("%+v, local redone: %v", jb, f.locals.Load() != nLocal)
		}
		if len(r.held) != 0 {
			t.Error("still holding")
		}
	})
}

func TestLocalAheadCanBeTurnedOff(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg", "b.jpg")
		f.cfg["local_ahead"] = false
		other := filepath.Join(filepath.Dir(f.photos), "other")
		write(t, other, "c.jpg")
		a := f.job([]string{f.photos}, pj.Obj{"vlm": true})
		b := f.job([]string{other}, pj.Obj{"vlm": false})
		run(t, f.runner(), f.get(a))
		if jb := f.get(b); jb.State != "queued" || jb.Started != nil {
			t.Errorf("%+v", jb)
		}
	})
}

func TestOverridePausesTheRunningJobAndItResumesAfter(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d, "a.jpg", "b.jpg")
		f.cfg["local_ahead"] = false
		other := filepath.Join(filepath.Dir(f.photos), "other")
		write(t, other, "c.jpg")
		a := f.job([]string{f.photos}, pj.Obj{"vlm": true})
		b := f.job([]string{other}, pj.Obj{"vlm": false})
		f.vlm.classify = func(int64) { d.OverrideJob(b) } // clicked while a's first image is out
		r := f.runner()
		run(t, r, f.get(a))
		ja := f.get(a)
		if ja.State != "queued" || ja.Owner != nil || ja.Done != 1 || ja.Total != 2 || !strings.Contains(msg(ja), "overrode") {
			t.Fatalf("%+v %s", ja, msg(ja))
		}
		if j, _ := d.NextQueuedJob(); j.ID != b {
			t.Error("b next")
		}
		f.vlm.classify = nil
		run(t, r, f.get(b))
		if f.get(b).State != "done" {
			t.Error("b done")
		}
		run(t, r, f.get(a))
		ja = f.get(a)
		if ja.State != "done" || ja.Done != 2 || ja.Total != 2 || len(f.vlm.calls) != 2 || f.vlm.calls[0] == f.vlm.calls[1] {
			t.Errorf("%+v %v", ja, f.vlm.calls)
		}
		if ok, _ := d.OverrideJob(a); ok {
			t.Error("finished jobs can't override")
		}
	})
}

func TestOverrideWhileTheLaneHoldsTheJob(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		main, _ := d.AddJob(nil, pj.Obj{})
		ahead, _ := d.AddJob(nil, pj.Obj{})
		other, _ := d.AddJob(nil, pj.Obj{})
		d.ClaimJob(main, "me", "main")
		d.ClaimJob(ahead, "me", "ahead")
		if ok, _ := d.OverrideJob(ahead); !ok {
			t.Fatal("override")
		}
		if st, _, _ := d.JobLease(main); st != "preempting" {
			t.Error(st)
		}
		if st, _, _ := d.JobLease(ahead); st != "running" {
			t.Error(st)
		}
		if id, _ := d.RunningJob(); id != main {
			t.Error("running job is the main lane's")
		}
		d.UpdateJob(main, "", db.JobFields{"heartbeat": 0.0})
		if stale, _ := d.RequeueStale(LeaseTTL); len(stale) != 1 || stale[0] != main {
			t.Error(stale)
		}
		d.UpdateJob(ahead, "", db.JobFields{"state": "queued", "owner": nil})
		if j, _ := d.NextQueuedJob(); j.ID != ahead {
			t.Error("ahead first")
		}
		if j, _ := d.Job(other); j.Priority != 0 {
			t.Error("priority")
		}
	})
}

func TestProgressTracksInFlight(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		f := setup(t, d)
		id := f.job(nil, pj.Obj{})
		p := newProgress(f.runner(), id, "local", 0)
		p.start(7)
		if fl, _ := d.InFlight(id); len(fl) != 1 || fl[0].ID != 7 || fl[0].Stage != "local" {
			t.Errorf("%+v", fl)
		}
		boom := "boom"
		p.finish(7, &boom, nil)
		items, _ := d.JobItems(id, "", true, 50, 0)
		if fl, _ := d.InFlight(id); len(fl) != 0 || len(items) != 1 || *items[0].Error != "boom" {
			t.Error("finished with error")
		}
	})
}

// fakeSlots is analyzer capacity of n slots; it records the most images ever in flight and hands Local its slot.
type fakeSlots struct {
	n        int
	sem      chan struct{}
	inflight atomic.Int64
	peak     atomic.Int64
}

type slotKey struct{}

func (s *fakeSlots) Max() int { return 32 }
func (s *fakeSlots) Slot(ctx context.Context) (context.Context, func(), error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	n := s.inflight.Add(1)
	for p := s.peak.Load(); n > p && !s.peak.CompareAndSwap(p, n); p = s.peak.Load() {
	}
	return context.WithValue(ctx, slotKey{}, true), func() { s.inflight.Add(-1); <-s.sem }, nil
}

func TestLocalStagePacedBySlots(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		names := make([]string, 12)
		for i := range names {
			names[i] = fmt.Sprintf("%02d.jpg", i)
		}
		f := setup(t, d, names...)
		f.cfg["workers"] = 1.0 // ignored: the slots decide
		slots := &fakeSlots{n: 3, sem: make(chan struct{}, 3)}
		r := f.runner()
		r.Slots = slots
		local := r.Local
		var noSlot atomic.Int64
		r.Local = func(ctx context.Context, cfg pj.Obj, id int64, path string) (pj.Obj, error) {
			if ctx.Value(slotKey{}) == nil {
				noSlot.Add(1)
			}
			time.Sleep(20 * time.Millisecond)
			return local(ctx, cfg, id, path)
		}
		id := f.job([]string{f.photos}, pj.Obj{"vlm": false})
		run(t, r, f.get(id))
		j := f.get(id)
		if j.State != "done" || j.Done != 12 {
			t.Fatalf("state %s done %d", j.State, j.Done)
		}
		if p := slots.peak.Load(); p != 3 {
			t.Errorf("peak in flight %d, want the 3 slots", p)
		}
		if noSlot.Load() != 0 {
			t.Errorf("%d images ran without their slot in the context", noSlot.Load())
		}
	})
}
