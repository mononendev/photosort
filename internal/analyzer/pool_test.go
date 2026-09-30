package analyzer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mononendev/photosort/internal/pj"
)

// fakeAnalyzer answers measure and finalize like a pooled analyzer (JPEGs in the answer) and remembers its tokens,
// so a finalize on the wrong pod is Gone.
type fakeAnalyzer struct {
	*httptest.Server
	measured atomic.Int64
	sawCache atomic.Bool
	slots    atomic.Int64 // the "slots" the last measure said this pod has
	tokens   map[string]bool
}

func newFake(t *testing.T) *fakeAnalyzer {
	f := &fakeAnalyzer{tokens: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req pj.Obj
		json.NewDecoder(r.Body).Decode(&req)
		jpeg := base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff, 0xd9})
		switch r.URL.Path {
		case "/measure":
			if _, ok := req["cache_dir"]; ok {
				f.sawCache.Store(true)
			}
			f.slots.Store(int64(pj.Int(req["slots"])))
			tok := f.URL + "/" + pj.Str(req["path"])
			f.tokens[tok] = true
			f.measured.Add(1)
			json.NewEncoder(w).Encode(pj.Obj{"token": tok, "width": 10.0, "height": 10.0,
				"files": pj.Obj{"7.jpg": jpeg, "7_thumb.jpg": jpeg}})
		case "/finalize":
			if !f.tokens[pj.Str(req["token"])] {
				w.WriteHeader(http.StatusGone)
				return
			}
			json.NewEncoder(w).Encode(pj.Obj{"eyes": pj.Obj{}, "files": pj.Obj{"7_crop.jpg": jpeg, "7_full.jpg": nil}})
		default:
			json.NewEncoder(w).Encode(pj.Obj{"ok": true, "slots": 2.0})
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func remotePool(slots map[string]int) *Pool {
	p := &Pool{remote: true, max: 8, eps: map[string]*endpoint{}, changed: make(chan struct{}), log: slog.Default()}
	for base, n := range slots {
		p.eps[base] = &endpoint{c: New(base), slots: n}
	}
	return p
}

func TestLeaseShipsCacheFilesAndFinalizesOnTheSamePod(t *testing.T) {
	a, b := newFake(t), newFake(t)
	p := remotePool(map[string]int{a.URL: 1, b.URL: 1})
	cache := t.TempDir()
	os.WriteFile(filepath.Join(cache, "7_full.jpg"), []byte("stale"), 0o644)
	ctx := context.Background()

	l1, _ := p.Acquire(ctx)
	l2, _ := p.Acquire(ctx) // the other pod: each has one slot
	if l1.ep == l2.ep {
		t.Fatal("two leases on a one-slot pod")
	}
	m, err := l1.Measure(ctx, pj.Obj{"path": "x.jpg", "cache_dir": cache})
	if err != nil {
		t.Fatal(err)
	}
	if a.sawCache.Load() || b.sawCache.Load() {
		t.Error("a pooled analyzer was sent the backend's cache dir")
	}
	if _, ok := m["files"]; ok {
		t.Error("files left in the measure answer")
	}
	if _, err := l1.Finalize(ctx, pj.Obj{"token": m["token"]}); err != nil {
		t.Fatalf("finalize went to another pod: %v", err)
	}
	for _, n := range []string{"7.jpg", "7_thumb.jpg", "7_crop.jpg"} {
		if b, err := os.ReadFile(filepath.Join(cache, n)); err != nil || len(b) != 4 {
			t.Errorf("%s: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cache, "7_full.jpg")); !os.IsNotExist(err) {
		t.Error("the stale full render was not removed")
	}
	l1.Release()
	l2.Release()
}

func TestAcquireWaitsForASlot(t *testing.T) {
	a := newFake(t)
	p := remotePool(map[string]int{a.URL: 2})
	ctx := context.Background()
	l1, _ := p.Acquire(ctx)
	p.Acquire(ctx)
	got := make(chan *Lease)
	go func() { l, _ := p.Acquire(ctx); got <- l }()
	select {
	case <-got:
		t.Fatal("a third lease on a two-slot pool")
	case <-time.After(50 * time.Millisecond):
	}
	l1.Release()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("a released slot was not handed on")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Acquire(cctx); !errors.Is(err, context.Canceled) {
		t.Errorf("full pool, cancelled: %v", err)
	}
}

func TestSetPerPodOverridesEachPodsSlots(t *testing.T) {
	a, b := newFake(t), newFake(t)
	p := remotePool(map[string]int{a.URL: 2, b.URL: 2})
	ctx := context.Background()
	if pods, slots := p.Capacity(); pods != 2 || slots != 4 {
		t.Fatalf("capacity %d pods %d slots, want 2 and 4", pods, slots)
	}
	var held []*Lease
	for range 4 {
		l, _ := p.Acquire(ctx)
		held = append(held, l)
	}
	got := make(chan *Lease)
	go func() { l, _ := p.Acquire(ctx); got <- l }()
	select {
	case <-got:
		t.Fatal("a fifth lease on a four-slot pool")
	case <-time.After(50 * time.Millisecond):
	}
	p.SetPerPod(3) // frees a slot on each pod: the waiter gets one
	var l *Lease
	select {
	case l = <-got:
	case <-time.After(time.Second):
		t.Fatal("raising the slots per pod did not hand on a slot")
	}
	if _, slots := p.Capacity(); slots != 6 {
		t.Errorf("capacity %d slots, want 6", slots)
	}
	if _, err := l.Measure(ctx, pj.Obj{"path": "x.jpg", "cache_dir": t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if n := max(a.slots.Load(), b.slots.Load()); n != 3 {
		t.Errorf("measure told the pod it has %d slots, want 3", n)
	}
	p.SetPerPod(0)
	if _, slots := p.Capacity(); slots != 4 {
		t.Errorf("back to the pods' own: %d slots, want 4", slots)
	}
	for _, l := range append(held, l) {
		l.Release()
	}
}

func TestMoveLeavesAnUnreachablePod(t *testing.T) {
	a := newFake(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	p := remotePool(map[string]int{dead.URL: 1})
	ctx := context.Background()
	l, _ := p.Acquire(ctx)
	_, err := l.Measure(ctx, pj.Obj{"path": "x.jpg", "cache_dir": t.TempDir()})
	if !Retryable(err) {
		t.Fatalf("unreachable pod not retryable: %v", err)
	}
	p.mu.Lock()
	p.eps[a.URL] = &endpoint{c: New(a.URL), slots: 1}
	p.mu.Unlock()
	if err := l.Move(ctx, err); err != nil {
		t.Fatal(err)
	}
	if l.ep.c.Base != a.URL {
		t.Errorf("moved to %s", l.ep.c.Base)
	}
	if Retryable(&Error{Status: 500}) || !Retryable(ErrGone) || !Retryable(&Error{Status: 503}) {
		t.Error("Retryable classification")
	}
}
