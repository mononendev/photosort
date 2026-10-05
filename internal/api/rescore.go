package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mononendev/photosort/internal/config"
	"github.com/mononendev/photosort/internal/local"
	"github.com/mononendev/photosort/internal/pj"
)

// A full rescore outlasts the ingress's 60 s proxy timeout on a large library, so it runs in the background and
// requests long-poll for it, each answering well inside that window.
//
// Requests are numbered. A run covers every request made before it started (it reads the config then, after any save
// that asked for it), so requests that arrive mid-run are served together by one more run.
type rescorer struct {
	mu      sync.Mutex
	wake    *sync.Cond
	asked   int // requests so far
	covered int // requests covered by the last finished run
	running bool
	source  string // the latest request's, for the change history
	result  *local.RescoreResult
	err     string
}

// rescoreWait is how long one request waits for a run to finish before answering that it's still going.
var rescoreWait = 20 * time.Second

func (s *Server) rescoreStatus(run int) pj.Obj {
	rs := &s.rescores
	out := pj.Obj{"run": run, "running": rs.running, "done": rs.covered >= run}
	if rs.covered >= run && run > 0 {
		if rs.err != "" {
			out["error"] = rs.err
		} else {
			out["result"] = rs.result
		}
	}
	return out
}

// await blocks until run is covered or the wait runs out, holding rs.mu on return.
func (rs *rescorer) await(run int) {
	if rs.wake == nil {
		rs.wake = sync.NewCond(&rs.mu)
	}
	deadline := time.Now().Add(rescoreWait)
	t := time.AfterFunc(rescoreWait, func() { rs.mu.Lock(); rs.wake.Broadcast(); rs.mu.Unlock() })
	defer t.Stop()
	for rs.covered < run && time.Now().Before(deadline) {
		rs.wake.Wait()
	}
}

func (s *Server) rescore(r *http.Request) (any, error) {
	var in struct {
		Source string `json:"source"`
	}
	if r.ContentLength > 0 {
		if err := decodeBody(r, &in); err != nil {
			return nil, err
		}
	}
	rs := &s.rescores
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.asked++
	run := rs.asked
	rs.source = in.Source
	if !rs.running {
		rs.running = true
		go s.runRescores()
	}
	rs.await(run)
	return s.rescoreStatus(run), nil
}

// rescoreState answers ?run=N once that request is covered (or the wait runs out); without run, right away.
func (s *Server) rescoreState(r *http.Request) (any, error) {
	run := 0
	if v := r.URL.Query().Get("run"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, errf(422, "run: expected a positive integer")
		}
		run = n
	}
	rs := &s.rescores
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if run > rs.asked {
		return nil, errf(404, "no rescore run %d (this server has had %d; it may have restarted)", run, rs.asked)
	}
	if run > 0 {
		rs.await(run)
	}
	return s.rescoreStatus(run), nil
}

// doRescore is one full pass, logged to the change history (a variable so tests can stand in for it).
var doRescore = func(s *Server, source string) (local.RescoreResult, error) {
	res, err := local.Rescore(s.DB, local.FileMeta(), s.cfg(), true)
	if err == nil {
		_, err = config.LogEvent(s.Workdir, pj.Obj{"kind": "rescore", "source": source, "changed": res.Changed})
	}
	return res, err
}

func (s *Server) runRescores() {
	rs := &s.rescores
	for {
		rs.mu.Lock()
		target, source := rs.asked, rs.source
		rs.mu.Unlock()

		res, err := doRescore(s, source)
		if err != nil && s.Log != nil {
			s.Log.Error("rescore failed", "err", err)
		}

		rs.mu.Lock()
		rs.covered, rs.result, rs.err = target, &res, ""
		if err != nil {
			rs.err = err.Error()
		}
		if rs.wake != nil {
			rs.wake.Broadcast()
		}
		if rs.asked == target {
			rs.running = false
			rs.mu.Unlock()
			return
		}
		rs.mu.Unlock()
	}
}
