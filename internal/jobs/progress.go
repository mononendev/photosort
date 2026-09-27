package jobs

import (
	"sync"
	"time"

	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/pj"
)

// progress counts finished images for the job row, and keeps the per-image record behind the job detail view:
// start adds an in-flight job item, finish stamps it with timing, error and token usage. Both live in the database,
// so whichever server answers the UI sees the same in-flight set.
type progress struct {
	r     *Runner
	jid   int64
	stage string

	mu    sync.Mutex
	count int
	last  time.Time
	items map[int64]int64 // image id -> its in-flight job item
}

func newProgress(r *Runner, jid int64, stage string, done int) *progress {
	return &progress{r: r, jid: jid, stage: stage, count: done, last: time.Now(), items: map[int64]int64{}}
}

func (p *progress) n() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

func (p *progress) update(k int) {
	p.mu.Lock()
	p.count += k
	write := p.count%5 == 0 || time.Since(p.last) > 2*time.Second
	n := p.count
	if write {
		p.last = time.Now()
	}
	p.mu.Unlock()
	if write {
		p.r.job(p.jid, db.JobFields{"done": n})
	}
}

func (p *progress) start(img int64) {
	item, err := p.r.DB.StartJobItem(p.jid, img, p.stage, db.Now())
	if err != nil {
		return
	}
	p.mu.Lock()
	p.items[img] = item
	p.mu.Unlock()
}

func (p *progress) finish(img int64, errMsg *string, usage pj.Obj) {
	p.mu.Lock()
	item, ok := p.items[img]
	delete(p.items, img)
	p.mu.Unlock()
	now := db.Now()
	u := pj.DumpsPtr(usage)
	if !ok {
		p.r.DB.AddJobItem(p.jid, img, p.stage, now, now, errMsg, u)
	} else {
		p.r.DB.FinishJobItem(item, now, errMsg, u)
	}
}
