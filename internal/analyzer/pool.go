package analyzer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mononendev/photosort/internal/pj"
)

// A Pool is where the pixel stage runs: one analyzer (the sidecar, or the CLI's child process), or a set of analyzer
// pods found through a headless Service and scaled by an HPA. In a pool, each image holds a slot on one pod for its
// measure and finalize (the decoded image stays in that pod's memory between them), a pod takes as many images at
// once as its /health "slots" says, and the JPEGs come back in the answers, since the pods share no disk with the
// backend. More pods mean more slots, so a job speeds up as the HPA scales out.
type Pool struct {
	remote bool
	max    int
	log    *slog.Logger

	mu      sync.Mutex
	eps     map[string]*endpoint
	changed chan struct{} // closed and replaced when a slot frees up or the pods change
}

type endpoint struct {
	c       *Client
	slots   int // 0: no limit (a single analyzer; the caller's worker count bounds it)
	busy    int
	badTill time.Time
}

// Single is a pool of one analyzer that shares the backend's disk.
func Single(c *Client) *Pool {
	p := &Pool{eps: map[string]*endpoint{c.Base: {c: c}}, changed: make(chan struct{}), log: slog.Default()}
	return p
}

// DNSScheme prefixes a pool's address: dns+http://analyzer-headless.ns.svc.cluster.local:8090.
const DNSScheme = "dns+"

// transport keeps enough idle connections per pod for all of its slots.
var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 64
	return t
}()

// Discover is a pool of the analyzers behind a DNS name (dns+http://host:port), re-resolved every few seconds until
// ctx is done. Pods that stop resolving leave the pool; images already on them finish or are retried elsewhere.
func Discover(ctx context.Context, raw string) (*Pool, error) {
	u, err := url.Parse(strings.TrimPrefix(raw, DNSScheme))
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return nil, fmt.Errorf("analyzer pool address %q: want %shttp://host:port", raw, DNSScheme)
	}
	p := &Pool{remote: true, max: 256, eps: map[string]*endpoint{}, changed: make(chan struct{}), log: slog.Default()}
	if n, err := strconv.Atoi(os.Getenv("PHOTOSORT_ANALYZER_MAX_INFLIGHT")); err == nil && n > 0 {
		p.max = n
	}
	p.refresh(ctx, u.Scheme, u.Hostname(), u.Port())
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.refresh(ctx, u.Scheme, u.Hostname(), u.Port())
			}
		}
	}()
	return p, nil
}

func (p *Pool) refresh(ctx context.Context, scheme, host, port string) {
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	addrs, err := net.DefaultResolver.LookupHost(rctx, host)
	cancel()
	if err != nil {
		p.log.Debug("analyzer pool: lookup failed; keeping the pods it has", "host", host, "err", err)
		return
	}
	want := map[string]bool{}
	for _, a := range addrs {
		want[scheme+"://"+net.JoinHostPort(a, port)] = true
	}
	p.mu.Lock()
	var add []string
	for base := range want {
		if p.eps[base] == nil {
			add = append(add, base)
		}
	}
	gone := 0
	for base := range p.eps {
		if !want[base] {
			delete(p.eps, base) // leases on it keep their pointer and release into nothing
			gone++
		}
	}
	p.mu.Unlock()
	joined := 0
	for _, base := range add {
		c := New(base)
		c.HTTP.Transport = transport
		hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		h, err := c.Health(hctx)
		cancel()
		if err != nil {
			continue // not answering yet; the next refresh tries again
		}
		p.mu.Lock()
		p.eps[base] = &endpoint{c: c, slots: max(1, pj.Int(pj.Or(h["slots"], 1.0)))}
		p.mu.Unlock()
		joined++
	}
	if joined+gone > 0 {
		p.mu.Lock()
		n, slots := len(p.eps), p.slotsLocked()
		p.broadcastLocked()
		p.mu.Unlock()
		p.log.Info("analyzer pool", "pods", n, "slots", slots, "joined", joined, "left", gone)
	}
}

func (p *Pool) slotsLocked() int {
	n := 0
	for _, e := range p.eps {
		n += e.slots
	}
	return n
}

func (p *Pool) broadcastLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// Remote says the analyzers share no disk with the backend (a pool of pods).
func (p *Pool) Remote() bool { return p.remote }

// Max is how many images may usefully wait for a slot at once: the ceiling on the pool's capacity.
func (p *Pool) Max() int { return p.max }

// pickLocked is the least busy healthy endpoint; with free, only one with a free slot.
func (p *Pool) pickLocked(free bool) *endpoint {
	now := time.Now()
	var best *endpoint
	load := func(e *endpoint) float64 {
		if e.slots == 0 {
			return float64(e.busy) / 1e6
		}
		return float64(e.busy) / float64(e.slots)
	}
	for _, e := range p.eps {
		if now.Before(e.badTill) || (free && e.slots > 0 && e.busy >= e.slots) {
			continue
		}
		if best == nil || load(e) < load(best) {
			best = e
		}
	}
	return best
}

func (p *Pool) any() (*Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.pickLocked(false); e != nil {
		return e.c, nil
	}
	return nil, errors.New("analyzer unreachable: no analyzer pods are answering")
}

// A Lease is a slot on one analyzer, for one image's measure and finalize. It is a local.Pixels.
type Lease struct {
	p        *Pool
	ep       *endpoint
	cacheDir string // from the measure request, for the JPEGs a remote analyzer sends back
}

// Acquire waits for a free slot and holds it until Release.
func (p *Pool) Acquire(ctx context.Context) (*Lease, error) {
	for {
		p.mu.Lock()
		if e := p.pickLocked(true); e != nil {
			e.busy++
			p.mu.Unlock()
			return &Lease{p: p, ep: e}, nil
		}
		ch := p.changed
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ch:
		case <-time.After(2 * time.Second): // a pod marked bad may be back
		}
	}
}

// Release gives the slot back.
func (l *Lease) Release() {
	if l.ep == nil {
		return
	}
	l.p.mu.Lock()
	l.ep.busy--
	l.ep = nil
	l.p.broadcastLocked()
	l.p.mu.Unlock()
}

// Move gives up this lease's pod after err (the pod left, or forgot the measurement) and takes a slot elsewhere.
// A pod that couldn't be reached is avoided for a while.
func (l *Lease) Move(ctx context.Context, err error) error {
	if l.ep != nil && !errors.Is(err, ErrGone) {
		l.p.mu.Lock()
		l.ep.badTill = time.Now().Add(15 * time.Second)
		l.p.mu.Unlock()
	}
	l.Release()
	n, aerr := l.p.Acquire(ctx)
	if aerr != nil {
		return aerr
	}
	l.ep = n.ep
	return nil
}

// Retryable: the image may succeed on another pod (or on this one, measured again).
func Retryable(err error) bool {
	var ne net.Error
	var ae *Error
	return errors.Is(err, ErrGone) || errors.As(err, &ne) ||
		(errors.As(err, &ae) && (ae.Status == http.StatusBadGateway || ae.Status == http.StatusServiceUnavailable))
}

// Measure runs the first half of the local stage on the leased analyzer.
func (l *Lease) Measure(ctx context.Context, req pj.Obj) (pj.Obj, error) {
	if l.p.remote {
		req = pj.Clone(req)
		l.cacheDir = pj.Str(req["cache_dir"])
		delete(req, "cache_dir")
	}
	m, err := l.ep.c.Measure(ctx, req)
	if err == nil && l.p.remote {
		err = l.store(m)
	}
	return m, err
}

// Finalize runs the second half on the same analyzer.
func (l *Lease) Finalize(ctx context.Context, req pj.Obj) (pj.Obj, error) {
	f, err := l.ep.c.Finalize(ctx, req)
	if err == nil && l.p.remote {
		err = l.store(f)
	}
	return f, err
}

// store writes (or removes, for null) the JPEGs a remote analyzer sent back into the cache dir.
func (l *Lease) store(o pj.Obj) error {
	files := pj.O(o, "files")
	delete(o, "files")
	if len(files) == 0 {
		return nil
	}
	if l.cacheDir == "" {
		return errors.New("analyzer sent cache files but the request named no cache dir")
	}
	if err := os.MkdirAll(l.cacheDir, 0o755); err != nil {
		return err
	}
	for name, v := range files {
		if name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			return fmt.Errorf("analyzer sent a bad cache file name %q", name)
		}
		dst := filepath.Join(l.cacheDir, name)
		if v == nil {
			if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		b, err := base64.StdEncoding.DecodeString(pj.Str(v))
		if err != nil {
			return fmt.Errorf("cache file %s: %w", name, err)
		}
		tmp := dst + ".part"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil { // readers never see half a JPEG
			return err
		}
	}
	return nil
}

type leaseKey struct{}

// WithLease carries a lease the caller already holds down to whoever runs the image.
func WithLease(ctx context.Context, l *Lease) context.Context {
	return context.WithValue(ctx, leaseKey{}, l)
}

// Slot takes a slot for one image and returns ctx carrying it, for jobs.Slots (see Pool.Lease).
func (p *Pool) Slot(ctx context.Context) (context.Context, func(), error) {
	l, err := p.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	return WithLease(ctx, l), l.Release, nil
}

// Lease is the lease ctx carries, or a new one (own: the caller releases it).
func (p *Pool) Lease(ctx context.Context) (l *Lease, own bool, err error) {
	if l, ok := ctx.Value(leaseKey{}).(*Lease); ok && l.p == p {
		return l, false, nil
	}
	l, err = p.Acquire(ctx)
	return l, err == nil, err
}

// ---- one-off calls: the UI's, on the least busy analyzer, outside the slots -----------------------------------------

// Detect runs one pose model on one image without storing anything.
func (p *Pool) Detect(ctx context.Context, req pj.Obj) (pj.Obj, error) {
	c, err := p.any()
	if err != nil {
		return nil, err
	}
	return c.Detect(ctx, req)
}

// FocusDebug recomputes the focus math's visual intermediates for the detail view.
func (p *Pool) FocusDebug(ctx context.Context, req pj.Obj) (pj.Obj, error) {
	c, err := p.any()
	if err != nil {
		return nil, err
	}
	return c.FocusDebug(ctx, req)
}

// RenderFull is the full-resolution JPEG of an image, decoded and lifted as the metrics saw it.
func (p *Pool) RenderFull(ctx context.Context, path string, exposure any, quality int) ([]byte, error) {
	c, err := p.any()
	if err != nil {
		return nil, err
	}
	return c.RenderFull(ctx, path, exposure, quality)
}

// Health is one analyzer's /health answer, with the pool's size in "pods" and "slots".
func (p *Pool) Health(ctx context.Context) (pj.Obj, error) {
	c, err := p.any()
	if err != nil {
		return nil, err
	}
	h, err := c.Health(ctx)
	if err != nil {
		return nil, err
	}
	if p.remote {
		p.mu.Lock()
		h["pods"], h["slots"] = len(p.eps), p.slotsLocked()
		p.mu.Unlock()
	}
	return h, nil
}
