package api

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mononendev/photosort/internal/local"
	"github.com/mononendev/photosort/internal/pj"
)

// A rescore longer than the wait answers "still running"; asking again by run number gets the result, and requests
// made mid-run are served together by one more run.
func TestRescoreRunsInBackground(t *testing.T) {
	defer func(f func(*Server, string) (local.RescoreResult, error), w time.Duration) { doRescore, rescoreWait = f, w }(doRescore, rescoreWait)
	rescoreWait = 50 * time.Millisecond
	release := make(chan struct{})
	var mu sync.Mutex
	var sources []string
	doRescore = func(_ *Server, source string) (local.RescoreResult, error) {
		<-release
		mu.Lock()
		defer mu.Unlock()
		sources = append(sources, source)
		return local.RescoreResult{Changed: len(sources)}, nil
	}
	s := &Server{}
	post := func(src string) pj.Obj {
		v, err := s.rescore(httptest.NewRequest("POST", "/api/rescore", strings.NewReader(`{"source":"`+src+`"}`)))
		if err != nil {
			t.Fatal(err)
		}
		return v.(pj.Obj)
	}
	get := func(q string) pj.Obj {
		v, err := s.rescoreState(httptest.NewRequest("GET", "/api/rescore"+q, nil))
		if err != nil {
			t.Fatal(err)
		}
		return v.(pj.Obj)
	}

	first := post("a")
	if first["run"] != 1 || first["done"] != false || first["running"] != true {
		t.Fatalf("first: %v", first)
	}
	second, third := post("b"), post("c") // mid-run: one more run covers both
	if second["run"] != 2 || third["run"] != 3 || second["done"] != false {
		t.Fatalf("mid-run: %v %v", second, third)
	}
	if st := get(""); st["running"] != true {
		t.Fatalf("status: %v", st)
	}
	release <- struct{}{}
	if st := get("?run=1"); st["done"] != true || st["result"].(*local.RescoreResult).Changed != 1 {
		t.Fatalf("run 1: %v", st)
	}
	if st := get("?run=3"); st["done"] != false {
		t.Fatalf("run 3 before its pass: %v", st)
	}
	release <- struct{}{}
	if st := get("?run=3"); st["done"] != true || st["result"].(*local.RescoreResult).Changed != 2 {
		t.Fatalf("run 3: %v", st)
	}
	if st := get("?run=2"); st["done"] != true {
		t.Fatalf("run 2: %v", st)
	}
	for deadline := time.Now().Add(time.Second); get("")["running"] == true; {
		if time.Now().After(deadline) {
			t.Fatal("still running after the last pass")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(sources) != 2 || sources[0] != "a" || sources[1] != "c" {
		t.Fatalf("passes: %v", sources)
	}
	if _, err := s.rescoreState(httptest.NewRequest("GET", "/api/rescore?run=9", nil)); err == nil {
		t.Fatal("unknown run should 404")
	}
}
