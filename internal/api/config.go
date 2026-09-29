package api

import (
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/mononendev/photosort/internal/config"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/export"
	"github.com/mononendev/photosort/internal/local"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/stats"
	"github.com/mononendev/photosort/internal/truth"
)

// ---- config / calibration -------------------------------------------------------------------------------------------

func (s *Server) getConfig(*http.Request) (any, error) { return config.Public(s.cfg()), nil }

func (s *Server) putConfig(r *http.Request) (any, error) {
	var in struct {
		Values pj.Obj `json:"values"`
		Source string `json:"source"` // where the save came from, for the change history (e.g. "auto-calibrate")
	}
	in.Source = "edit"
	if err := decodeBody(r, &in); err != nil {
		return nil, err
	}
	if in.Values == nil {
		return nil, &HTTPError{422, "values: field required"}
	}
	if _, err := s.Config.Update(in.Source, func(cfg pj.Obj) error {
		config.Merge(cfg, in.Values)
		return nil
	}); err != nil {
		return nil, err
	}
	return config.Public(s.cfg()), nil
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
	res, err := local.Rescore(s.DB, local.FileMeta(), s.cfg(), true)
	if err != nil {
		return nil, err
	}
	if _, err := config.LogEvent(s.Workdir, pj.Obj{"kind": "rescore", "source": in.Source, "changed": res.Changed}); err != nil {
		return nil, err
	}
	return res, nil
}

// history is newest first. Each change carries the whole config from just before it, to roll back to.
func (s *Server) history(r *http.Request) (any, error) {
	limit, err := qIntDefault(r, "limit", 100, 0)
	if err != nil {
		return nil, err
	}
	h, err := config.ReadHistory(s.Workdir)
	if err != nil {
		return nil, err
	}
	slices.Reverse(h)
	out := []any{}
	for _, e := range h[:min(max(limit, 0), len(h))] {
		out = append(out, e)
	}
	return out, nil
}

// restore puts every setting back to how it was just before that change (and logs the restore, so it can be undone).
func (s *Server) restore(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	h, err := config.ReadHistory(s.Workdir)
	if err != nil {
		return nil, err
	}
	var entry pj.Obj
	for _, e := range h {
		if int64(pj.F(e["id"])) == id && e["kind"] == "change" {
			entry = e
			break
		}
	}
	if entry == nil {
		return nil, errf(404, "no such change")
	}
	if _, err := s.Config.Update(fmt.Sprintf("restore to before %s", pj.Str(entry["at"])), func(cfg pj.Obj) error {
		for k := range cfg {
			if !strings.HasPrefix(k, "_") {
				delete(cfg, k)
			}
		}
		for k, v := range pj.Clone(pj.O(entry, "before")) {
			cfg[k] = v
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return config.Public(s.cfg()), nil
}

// metricExpr is a truth.Metric's stored value as SQL.
func (s *Server) metricExpr(m truth.Metric) string {
	return s.DB.D.JNum("local_json", strings.TrimPrefix(m.Path, "$."))
}

func (s *Server) calibration(r *http.Request) (any, error) {
	n, err := qIntDefault(r, "n", 48, 0)
	if err != nil {
		return nil, err
	}
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		metric = "eye"
	}
	m, ok := truth.Metrics[metric]
	if !ok {
		names := truth.MetricNames()
		sort.Strings(names)
		return nil, errf(400, "metric must be one of %s", pyList(names))
	}
	v := s.metricExpr(m)
	rows, err := s.DB.Query("SELECT " + v + ", id, " + s.DB.D.JNum("local_json", "local_tier") + " FROM images " +
		"WHERE local_json IS NOT NULL AND " + v + " IS NOT NULL ORDER BY 1, id")
	if err != nil {
		return nil, err
	}
	type sample struct {
		v    float64
		id   int64
		tier sql.NullFloat64
	}
	var vals []sample
	for rows.Next() {
		var x sample
		if err := rows.Scan(&x.v, &x.id, &x.tier); err != nil {
			rows.Close()
			return nil, err
		}
		vals = append(vals, x)
	}
	rows.Close()
	keys := []any{m.Keys[0], m.Keys[1], m.Keys[2]}
	out := pj.Obj{"metric": metric, "thresholds": pj.O(s.cfg(), "focus"), "keys": keys}
	if len(vals) == 0 {
		out["percentiles"], out["samples"] = pj.Obj{}, []any{}
		return out, nil
	}
	arr := make([]float64, len(vals))
	for i, x := range vals {
		arr[i] = x.v
	}
	pct := pj.Obj{}
	for _, q := range []int{5, 10, 25, 50, 75, 90, 95} {
		pct[fmt.Sprintf("p%d", q)] = pj.Round(stats.Percentile(arr, float64(q)), 4)
	}
	// quantiles[i] is the score i% of photos are at or below: the UI reads a cut's pass rate off it as you type.
	quant := make([]any, 101)
	qs := make([]float64, 101)
	for i := range qs {
		qs[i] = float64(i)
	}
	for i, q := range stats.Percentiles(arr, qs) {
		quant[i] = pj.Sig(q, 4)
	}
	samples := []any{}
	for _, f := range stats.Linspace(0, float64(len(vals)-1), min(n, len(vals))) {
		x := vals[int(f)]
		samples = append(samples, pj.Obj{"id": x.id, "sharp": x.v, "tier": nullF(x.tier)})
	}
	out["count"], out["percentiles"], out["quantiles"], out["samples"] = len(vals), pct, quant, samples
	return out, nil
}

func pyList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "'" + x + "'"
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// models lists the pose models the analyzer can run without a download.
func (s *Server) models(*http.Request) (any, error) {
	h := s.AnalyzerHealth()
	cfg := s.cfg()
	return pj.Obj{"installed": pj.Or(pj.Get(h, "models"), []any{}), "current": local.Detector(cfg), "device": pj.Get(h, "device")}, nil
}

// ---- ground truth ---------------------------------------------------------------------------------------------------

// truthSQL is where a photo's truth tier comes from: your in-app rating (q/w/e/r/t; a banger counts as sharp), the
// verdicts you imported, or both, with your rating winning where a photo has both.
func (s *Server) truthSQL() (rated, imported string, sources map[string]string) {
	d := s.DB.D
	rated = fmt.Sprintf("(CASE WHEN COALESCE(%s, FALSE) THEN %s END)", d.JBool("override_json", "reviewed"),
		d.Least(d.JNum("override_json", "rating"), "3"))
	imported = d.JNum("truth_json", "focus_tier")
	return rated, imported, map[string]string{"both": "COALESCE(" + rated + ", " + imported + ")", "ratings": rated, "imported": imported}
}

// truthSummary is the confusion matrices and suggested thresholds from the images that carry a verdict (usually a few
// hundred), read in one pass. source picks the verdicts: your in-app ratings, imported ones, or both (your rating
// wins).
func (s *Server) truthSummary(source string) (pj.Obj, error) {
	d := s.DB.D
	rated, imported, sources := s.truthSQL()
	has := map[string]string{"ratings": rated + " IS NOT NULL", "imported": "truth_json IS NOT NULL"}
	where := has["ratings"] + " OR " + has["imported"]
	if source != "both" {
		where = has[source]
	}
	names := truth.MetricNames()
	cols := []string{sources[source], d.JNum("local_json", "local_tier"), d.JNum("vlm_json", "focus_tier")}
	for _, m := range names {
		cols = append(cols, s.metricExpr(truth.Metrics[m]))
	}
	rows, err := s.DB.Query("SELECT " + strings.Join(cols, ", ") + " FROM images WHERE " + where)
	if err != nil {
		return nil, err
	}
	type rec struct {
		truth, local, vlm sql.NullFloat64
		metric            []sql.NullFloat64
	}
	var all []rec
	for rows.Next() {
		x := rec{metric: make([]sql.NullFloat64, len(names))}
		dest := []any{&x.truth, &x.local, &x.vlm}
		for i := range x.metric {
			dest = append(dest, &x.metric[i])
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, x)
	}
	rows.Close()
	var nRated, nImported int
	if err := s.DB.QueryRow("SELECT COUNT(*) FILTER (WHERE "+has["ratings"]+"), COUNT(*) FILTER (WHERE "+imported+
		" IS NOT NULL) FROM images").Scan(&nRated, &nImported); err != nil {
		return nil, err
	}
	var withTier []rec
	for _, x := range all {
		if x.truth.Valid {
			withTier = append(withTier, x)
		}
	}
	matrix := func(col func(rec) sql.NullFloat64) []any {
		type key struct{ t, p float64 }
		n := map[key]int{}
		for _, x := range withTier {
			if c := col(x); c.Valid {
				n[key{x.truth.Float64, c.Float64}]++
			}
		}
		keys := make([]key, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].t != keys[j].t {
				return keys[i].t < keys[j].t
			}
			return keys[i].p < keys[j].p
		})
		out := []any{}
		for _, k := range keys {
			out = append(out, pj.Obj{"truth": k.t, "pred": k.p, "n": n[k]})
		}
		return out
	}
	acc := func(m []any) any {
		tot, ok := 0, 0
		for _, e := range m {
			o := e.(pj.Obj)
			tot += o["n"].(int)
			if o["truth"] == o["pred"] {
				ok += o["n"].(int)
			}
		}
		if tot == 0 {
			return nil
		}
		return pj.Round(float64(ok)/float64(tot), 3)
	}
	localM := matrix(func(x rec) sql.NullFloat64 { return x.local })
	vlmM := matrix(func(x rec) sql.NullFloat64 { return x.vlm })
	suggested := pj.Obj{}
	for i, m := range names {
		var pairs []truth.Pair
		for _, x := range withTier {
			if x.metric[i].Valid {
				pairs = append(pairs, truth.Pair{Value: x.metric[i].Float64, Tier: int(x.truth.Float64)})
			}
		}
		sug := truth.SuggestThresholds(pairs)
		if len(sug) == 0 {
			continue
		}
		o := pj.Obj{"n": len(pairs)}
		keys := truth.Metrics[m].Keys
		for _, sg := range sug { // keys[i] is this metric's key for tier 3 - i
			o[keys[3-sg.Tier]] = sg.Value
		}
		suggested[m] = o
	}
	return pj.Obj{"source": source, "rated": nRated, "imported": nImported, "images_with_truth": len(all),
		"with_tier": len(withTier), "local": pj.Obj{"matrix": localM, "accuracy": acc(localM)},
		"vlm": pj.Obj{"matrix": vlmM, "accuracy": acc(vlmM)}, "suggested": suggested, "mapping": s.cfg()["truth"]}, nil
}

func (s *Server) getTruth(r *http.Request) (any, error) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "both"
	}
	if source != "both" && source != "ratings" && source != "imported" {
		return nil, errf(400, "source must be one of ['both', 'imported', 'ratings']")
	}
	return s.truthSummary(source)
}

// applyTruth attaches verdicts to tracked images under folder (all when "") and returns the counts plus a summary.
func (s *Server) applyTruth(verdicts map[string]truth.Verdict, folder string) (any, error) {
	where, args := "1=1", []any(nil)
	if folder != "" {
		base, err := s.safePath(folder)
		if err != nil {
			return nil, err
		}
		where, args = s.DB.D.UnderFolder(base, "folder")
	}
	rows, err := s.DB.Rows(where, args, "", -1, 0, "id, path")
	if err != nil {
		return nil, err
	}
	trs := make([]truth.Row, len(rows))
	for i, r := range rows {
		trs[i] = truth.Row{ID: r.ID, Path: r.Path}
	}
	counts, err := truth.Apply(trs, verdicts, s.cfg(), func(items []truth.Item) error {
		ups := make([]db.JSONUpdate, len(items))
		for i, it := range items {
			j := it.Verdict.JSON()
			ups[i] = db.JSONUpdate{ID: it.ID, JSON: &j}
		}
		return s.DB.SetTruthMany(ups)
	})
	if err != nil {
		return nil, err
	}
	sum, err := s.truthSummary("both")
	if err != nil {
		return nil, err
	}
	return pj.Obj{"verdicts": counts.Verdicts, "matched": counts.Matched, "unmatched": counts.Unmatched, "summary": sum}, nil
}

func (s *Server) truthUpload(r *http.Request) (any, error) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return nil, &HTTPError{422, "files: field required"}
	}
	var files []truth.File
	for _, fh := range r.MultipartForm.File["files"] {
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		name := fh.Filename
		if name == "" {
			name = "x"
		}
		files = append(files, truth.File{Name: name, Data: b})
	}
	if len(files) == 0 {
		return nil, &HTTPError{422, "files: field required"}
	}
	verdicts, err := truth.ParseFiles(files)
	if err != nil {
		return nil, errf(400, "%s", err.Error())
	}
	return s.applyTruth(verdicts, r.URL.Query().Get("folder"))
}

func (s *Server) truthImport(r *http.Request) (any, error) {
	var in struct {
		Dir    string `json:"dir"`    // folder on the photos or data volume containing .xmp/.xml/.csv/.zip
		Folder string `json:"folder"` // only match images under this photos subfolder
	}
	if err := decodeBody(r, &in); err != nil {
		return nil, err
	}
	d := in.Dir
	if !filepath.IsAbs(d) {
		if _, err := os.Stat(filepath.Join(s.PhotosRoot, in.Dir)); err == nil {
			d = filepath.Join(s.PhotosRoot, in.Dir)
		} else {
			d = filepath.Join(s.Workdir, in.Dir)
		}
	}
	if st, err := os.Stat(d); err != nil || !st.IsDir() {
		return nil, errf(404, "no such directory: %s", d)
	}
	var files []truth.File
	err := filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".xmp", ".xml", ".csv", ".zip":
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files = append(files, truth.File{Name: e.Name(), Data: b})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	verdicts, err := truth.ParseFiles(files)
	if err != nil {
		return nil, errf(400, "%s", err.Error())
	}
	return s.applyTruth(verdicts, in.Folder)
}

func (s *Server) truthClear(*http.Request) (any, error) {
	n, err := s.DB.ClearTruth()
	if err != nil {
		return nil, err
	}
	return pj.Obj{"cleared": n}, nil
}

// ---- export ---------------------------------------------------------------------------------------------------------

func (s *Server) export(r *http.Request) (any, error) {
	in := struct {
		Name        string  `json:"name"`
		Folder      string  `json:"folder"`
		Link        string  `json:"link"` // copy | symlink | hardlink (photos are read-only, so no move)
		XMP         *bool   `json:"xmp"`
		FocusSource *string `json:"focus_source"`
		Tree        *bool   `json:"tree"`
	}{Name: "export", Link: "copy"}
	if err := decodeBody(r, &in); err != nil {
		return nil, err
	}
	cfg := s.cfg()
	out := filepath.Join(s.Workdir, "exports", filepath.Base(in.Name))
	source := s.focusSource(cfg)
	if in.FocusSource != nil && *in.FocusSource != "" {
		source = *in.FocusSource
	}
	recs, _, err := s.exportRecords(in.Folder, source)
	if err != nil {
		return nil, err
	}
	if err := export.Export(recs, out); err != nil {
		return nil, err
	}
	groups := pj.O(cfg, "groups")
	var counts any = pj.Obj{}
	if in.Tree == nil || *in.Tree {
		c, err := export.BuildTree(recs, out, in.Link, groups)
		if err != nil {
			return nil, err
		}
		counts = c
	}
	written := 0
	if in.XMP == nil || *in.XMP {
		if written, _, err = export.WriteXMP(recs, filepath.Join(out, "xmp"), true, groups); err != nil {
			return nil, err
		}
	}
	return pj.Obj{"out": out, "images": len(recs), "tree": counts, "xmp_written": written}, nil
}

// exportRecords are the final records of the analyzed images under folder (relative to the photos root; "" for all)
// with the focus source, and folder's absolute path.
func (s *Server) exportRecords(folder, source string) ([]*export.Record, string, error) {
	base, err := s.safePath(folder)
	if err != nil {
		return nil, "", err
	}
	where, args := "local_json IS NOT NULL", []any(nil)
	if folder != "" {
		w, a := s.DB.D.UnderFolder(base, "folder")
		where += " AND " + w
		args = a
	}
	rows, err := s.DB.Rows(where, args, "", -1, 0, "")
	if err != nil {
		return nil, "", err
	}
	recs := make([]*export.Record, len(rows))
	for i, row := range rows {
		if recs[i], err = export.FinalRecord(exportRow(row), source); err != nil {
			return nil, "", err
		}
	}
	return recs, base, nil
}

// xmpZip is GET /api/export/xmp.zip?folder=&format=capture_one|lightroom&focus_source=: the sidecars of the images
// under folder, laid out as the photos are under it, to unzip over that folder.
func (s *Server) xmpZip(r *http.Request) (any, error) {
	q := r.URL.Query()
	folder, format := q.Get("folder"), q.Get("format")
	if format == "" {
		format = export.FormatCaptureOne
	}
	if format != export.FormatCaptureOne && format != export.FormatLightroom {
		return nil, errf(422, "format must be capture_one or lightroom")
	}
	cfg := s.cfg()
	source := s.focusSource(cfg)
	if v := q.Get("focus_source"); v != "" {
		source = v
	}
	recs, base, err := s.exportRecords(folder, source)
	if err != nil {
		return nil, err
	}
	b, n, err := export.XMPZip(recs, base, format, pj.O(cfg, "groups"))
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errf(404, "no analyzed photos under this folder")
	}
	name := "photos"
	if folder != "" {
		name = filepath.Base(base)
	}
	return download{Name: name + "-xmp-" + strings.ReplaceAll(format, "_", "") + ".zip", Type: "application/zip", Body: b}, nil
}

func (s *Server) exports(*http.Request) (any, error) {
	d := filepath.Join(s.Workdir, "exports")
	entries, err := os.ReadDir(d)
	if os.IsNotExist(err) {
		return []any{}, nil
	} else if err != nil {
		return nil, err
	}
	out := []any{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, pj.Obj{"name": e.Name(), "path": filepath.Join(d, e.Name()),
			"mtime": float64(st.ModTime().UnixNano()) / 1e9})
	}
	return out, nil
}
