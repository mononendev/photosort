package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/stats"
)

// jobIn is a new job's request.
type jobIn struct {
	Paths       []string `json:"paths"`
	VLM         *bool    `json:"vlm"`
	SkipTier0   bool     `json:"skip_tier0"`
	Rescan      bool     `json:"rescan"`       // redo the local stage on images that already have it
	Revlm       bool     `json:"revlm"`        // re-tag images that already have vision-model tags
	RetryErrors bool     `json:"retry_errors"`
	Concurrency *int     `json:"concurrency"`
	Model       *string  `json:"model"`
	Detector    pj.Obj   `json:"detector"` // pose model override for this job's local stage
}

func nullable[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func (s *Server) jobOut(j *db.Job) pj.Obj {
	d := pj.Obj{"id": j.ID, "created": j.Created, "started": nullable(j.Started), "finished": nullable(j.Finished),
		"state": j.State, "stage": j.Stage, "total": j.Total, "done": j.Done, "errors": j.Errors,
		"message": nullable(j.Message), "owner": nullable(j.Owner), "heartbeat": nullable(j.Heartbeat),
		"lane": nullable(j.Lane), "priority": j.Priority}
	var paths []any
	if j.PathsJSON != nil {
		json.Unmarshal([]byte(*j.PathsJSON), &paths)
	}
	if paths == nil {
		paths = []any{}
	}
	d["paths"] = paths
	opts := pj.Parse(j.OptionsJSON)
	if opts == nil {
		opts = pj.Obj{}
	}
	d["options"] = opts
	stages := pj.Parse(j.StagesJSON)
	if stages == nil {
		stages = pj.Obj{}
	}
	d["stages"] = stages
	if j.Started != nil && j.Done != 0 && j.State == "running" {
		// done/total count the current stage, so time it from that stage's start, not the job's
		start := *j.Started
		if st, ok := pj.Num(stages, j.Stage, "started"); ok && st != 0 {
			start = st
		}
		el := db.Now() - start
		var rate any
		if el != 0 {
			rate = pj.Round(float64(j.Done)/el, 2)
		}
		d["rate"] = rate
		if pj.Truthy(rate) {
			d["eta_s"] = pj.RoundInt(float64(j.Total-j.Done) / rate.(float64))
		} else {
			d["eta_s"] = nil
		}
	}
	return d
}

func (s *Server) createJob(r *http.Request) (any, error) {
	var in jobIn
	if err := decodeBody(r, &in); err != nil {
		return nil, err
	}
	if len(in.Paths) == 0 {
		return nil, errf(400, "no paths")
	}
	paths := make([]string, len(in.Paths))
	for i, p := range in.Paths {
		sp, err := s.safePath(p)
		if err != nil {
			return nil, err
		}
		paths[i] = sp
	}
	vlm, conc := true, 1
	if in.VLM != nil {
		vlm = *in.VLM
	}
	if in.Concurrency != nil {
		conc = *in.Concurrency
	}
	opts := pj.Obj{"vlm": vlm, "skip_tier0": in.SkipTier0, "rescan": in.Rescan, "revlm": in.Revlm,
		"retry_errors": in.RetryErrors, "concurrency": conc, "model": nullable(in.Model)}
	if len(in.Detector) > 0 {
		opts["detector"] = in.Detector
	}
	id, err := s.DB.AddJob(paths, opts)
	if err != nil {
		return nil, err
	}
	j, err := s.DB.Job(id)
	if err != nil {
		return nil, err
	}
	return s.jobOut(j), nil
}

func (s *Server) listJobs(r *http.Request) (any, error) {
	limit, err := qIntDefault(r, "limit", 50, 0)
	if err != nil {
		return nil, err
	}
	jobs, err := s.DB.Jobs(limit)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(jobs))
	for i := range jobs {
		out[i] = s.jobOut(&jobs[i])
	}
	return out, nil
}

func (s *Server) job(r *http.Request) (*db.Job, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	j, err := s.DB.Job(id)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, errNotFound
	}
	return j, nil
}

func (s *Server) getJob(r *http.Request) (any, error) {
	j, err := s.job(r)
	if err != nil {
		return nil, err
	}
	return s.jobOut(j), nil
}

// stageStats are timing and token figures for one stage's finished items (oldest first).
func stageStats(rows []db.Timing) pj.Obj {
	var secs []float64
	var us []pj.Obj
	errs := 0
	for _, r := range rows {
		if r.Seconds != nil && !r.Failed {
			secs = append(secs, *r.Seconds)
		}
		if r.Failed {
			errs++
		}
		if u := pj.Parse(r.UsageJSON); r.UsageJSON != nil && *r.UsageJSON != "" && u != nil {
			us = append(us, u)
		}
	}
	if len(secs) == 0 {
		secs = []float64{0}
	}
	sum := func(xs []pj.Obj, k string) float64 {
		t := 0.0
		for _, u := range xs {
			t += pj.F(pj.Or(u[k], 0.0))
		}
		return t
	}
	tin, tout, decode := sum(us, "in"), sum(us, "out"), sum(us, "decode_s")
	recent := us[max(0, len(us)-10):]
	rdec := sum(recent, "decode_s")
	span := 0.0
	if len(rows) > 0 {
		span = rows[len(rows)-1].Finished - rows[0].Started
	}
	last := rows[max(0, len(rows)-20):]
	lspan := 0.0
	if len(last) > 1 {
		lspan = last[len(last)-1].Finished - last[0].Finished
	}
	var prefill []float64
	for _, u := range us {
		if v, ok := pj.Float(u["prefill_s"]); ok {
			prefill = append(prefill, v)
		}
	}
	maxS := secs[0]
	for _, v := range secs {
		maxS = max(maxS, v)
	}
	out := pj.Obj{
		"n": len(rows), "errors": errs,
		"avg_s": pj.Round(stats.Mean(secs), 2), "p50_s": pj.Round(stats.Percentile(secs, 50), 2),
		"p95_s": pj.Round(stats.Percentile(secs, 95), 2), "max_s": pj.Round(maxS, 2),
		"rate": nil, "recent_rate": nil, "tokens_in": tin, "tokens_out": tout,
		"tok_s": nil, "recent_tok_s": nil, "avg_in": nil, "avg_out": nil, "avg_prefill_s": nil, "avg_decode_s": nil,
	}
	if span > 0 {
		out["rate"] = pj.Round(float64(len(rows))/span, 3) // images/s over the stage
	}
	if lspan > 0 {
		out["recent_rate"] = pj.Round(float64(len(last)-1)/lspan, 3) // images/s over the last 20
	}
	if decode != 0 {
		out["tok_s"] = pj.Round(tout/decode, 1) // decode tokens/s, whole stage
	}
	if rdec != 0 {
		out["recent_tok_s"] = pj.Round(sum(recent, "out")/rdec, 1)
	}
	if len(us) > 0 {
		out["avg_in"] = pj.RoundInt(tin / float64(len(us)))
		out["avg_out"] = pj.RoundInt(tout / float64(len(us)))
		out["avg_decode_s"] = pj.Round(decode/float64(len(us)), 2)
	}
	if len(prefill) > 0 {
		t := 0.0
		for _, v := range prefill {
			t += v
		}
		out["avg_prefill_s"] = pj.Round(t/float64(len(prefill)), 2)
	}
	return out
}

// jobDetail is everything the job page shows except the item list: stage timings, per-stage throughput and tokens,
// the images in flight right now, and a per-image series for the charts.
func (s *Server) jobDetail(r *http.Request) (any, error) {
	j, err := s.job(r)
	if err != nil {
		return nil, err
	}
	points, err := qIntDefault(r, "points", 300, 0)
	if err != nil {
		return nil, err
	}
	out := s.jobOut(j)
	rows, err := s.DB.JobItemTimings(j.ID)
	if err != nil {
		return nil, err
	}
	var order []string
	byStage := map[string][]db.Timing{}
	for _, t := range rows {
		if _, ok := byStage[t.Stage]; !ok {
			order = append(order, t.Stage)
		}
		byStage[t.Stage] = append(byStage[t.Stage], t)
	}
	st := pj.Obj{}
	series := []any{} // the last `points` images of each stage, so a finished stage keeps its charts
	for _, name := range order {
		rs := byStage[name]
		st[name] = stageStats(rs)
		for _, t := range rs[max(0, len(rs)-points):] {
			u := pj.Parse(t.UsageJSON)
			series = append(series, pj.Obj{"t": t.Finished, "stage": t.Stage, "s": nullable(t.Seconds), "err": t.Failed,
				"tok_s": u["tok_s"], "out": u["out"]})
		}
	}
	out["stats"], out["series"] = st, series
	now := db.Now()
	out["now"] = now
	active := []any{}
	if j.State == "running" || j.State == "cancelling" {
		fl, err := s.DB.InFlight(j.ID)
		if err != nil {
			return nil, err
		}
		for _, a := range fl {
			path := ""
			if a.Path != nil {
				path = *a.Path
			}
			_, thumbErr := os.Stat(filepath.Join(s.CacheDir, itoa(a.ID)+"_thumb.jpg"))
			active = append(active, pj.Obj{"id": a.ID, "stage": a.Stage, "started": a.Started, "path": nullable(a.Path),
				"name": filepath.Base(path), "rel": s.rel(path), "elapsed": pj.Round(now-a.Started, 1),
				"has_thumb": thumbErr == nil})
		}
	}
	out["active"] = active
	cfg := s.cfg()
	out["runner"] = pj.Obj{"device": pj.Get(s.AnalyzerHealth(), "device"), "backend": cfg["backend"], "model": cfg["model"],
		"base_url": cfg["base_url"], "workers": cfg["workers"], "vlm_concurrency": cfg["vlm_concurrency"]}
	return out, nil
}

func pick(o pj.Obj, keys ...string) pj.Obj {
	out := pj.Obj{}
	for _, k := range keys {
		out[k] = o[k]
	}
	return out
}

func (s *Server) itemOut(it db.JobItem) pj.Obj {
	local, vlm := pj.Parse(it.LocalJSON), pj.Parse(it.VLMJSON)
	var name, rel any
	if it.Path != nil {
		name, rel = filepath.Base(*it.Path), s.rel(*it.Path)
	}
	var usage any
	if u := pj.Parse(it.UsageJSON); u != nil {
		usage = u
	}
	d := pj.Obj{"id": it.ID, "image_id": it.ImageID, "stage": it.Stage, "started": it.Started,
		"finished": nullable(it.Finished), "seconds": nullable(it.Seconds), "error": nullable(it.Error),
		"usage": usage, "name": name, "rel": rel, "has_crop": local != nil && pj.Truthy(local["n_people"])}
	if local != nil {
		d["local"] = pick(local, "local_tier", "local_reason", "n_people", "primary_eye_sharp", "primary_eye_hf",
			"primary_head_sharp", "primary_by")
	}
	if vlm != nil {
		d["vlm"] = pick(vlm, "focus_tier", "primary_subject", "composition", "quality_score", "keeper", "description", "keywords")
	}
	return d
}

func (s *Server) jobItems(r *http.Request) (any, error) {
	j, err := s.job(r)
	if err != nil {
		return nil, err
	}
	stage := r.URL.Query().Get("stage")
	errs, err := qBool(r, "errors")
	if err != nil {
		return nil, err
	}
	offset, err := qIntDefault(r, "offset", 0, 0)
	if err != nil {
		return nil, err
	}
	limit, err := qIntDefault(r, "limit", 50, 500)
	if err != nil {
		return nil, err
	}
	onlyErr := errs != nil && *errs
	total, err := s.DB.JobItemCount(j.ID, stage, onlyErr)
	if err != nil {
		return nil, err
	}
	items, err := s.DB.JobItems(j.ID, stage, onlyErr, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = s.itemOut(it)
	}
	return pj.Obj{"total": total, "offset": offset, "items": out}, nil
}

func (s *Server) cancelJob(r *http.Request) (any, error) {
	j, err := s.job(r)
	if err != nil {
		return nil, err
	}
	if err := s.DB.CancelJob(j.ID); err != nil {
		return nil, err
	}
	j, err = s.DB.Job(j.ID)
	if err != nil {
		return nil, err
	}
	return s.jobOut(j), nil
}

// overrideJob: run this job next; the running job pauses, goes back in the queue and resumes after it.
func (s *Server) overrideJob(r *http.Request) (any, error) {
	j, err := s.job(r)
	if err != nil {
		return nil, err
	}
	ok, err := s.DB.OverrideJob(j.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errf(409, "only a queued job can override the running one")
	}
	j, err = s.DB.Job(j.ID)
	if err != nil {
		return nil, err
	}
	return s.jobOut(j), nil
}
