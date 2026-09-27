package api

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mononendev/photosort/internal/backends"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/export"
	"github.com/mononendev/photosort/internal/local"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/scan"
	"github.com/mononendev/photosort/internal/schema"
	"github.com/mononendev/photosort/internal/trace"
)

func itoa(i int64) string { return fmt.Sprint(i) }

func exportRow(r db.Image) export.Row {
	return export.Row{Path: r.Path, Error: r.Error, LocalJSON: r.LocalJSON, VLMJSON: r.VLMJSON, OverrideJSON: r.OverrideJSON}
}

// summary is an image as the lists show it.
func (s *Server) summary(r db.Image, source string) (pj.Obj, error) {
	rec, err := export.FinalRecord(exportRow(r), source)
	if err != nil {
		return nil, err
	}
	loc := pj.Parse(r.LocalJSON)
	lr, tr := pj.Parse(r.LRJSON), pj.Parse(r.TruthJSON)
	folder := filepath.Dir(r.Path)
	if r.Folder != nil && *r.Folder != "" {
		folder = *r.Folder
	}
	status := "pending"
	switch {
	case r.VLMJSON != nil && *r.VLMJSON != "":
		status = "tagged"
	case r.LocalJSON != nil && *r.LocalJSON != "" && r.VLMSkip != nil && *r.VLMSkip != "":
		status = "skipped"
	case r.LocalJSON != nil && *r.LocalJSON != "":
		status = "analyzed"
	case r.Error != nil && *r.Error != "":
		status = "error"
	}
	return pj.Obj{
		"id": r.ID, "path": r.Path, "rel": s.rel(r.Path), "name": filepath.Base(r.Path), "folder": s.rel(folder),
		"status": status, "vlm_skip": nullable(r.VLMSkip), "has_crop": loc != nil && pj.Truthy(loc["n_people"]),
		"focus_tier": rec.FocusTier, "focus_tier_local": rec.FocusTierLocal, "focus_tier_vlm": rec.FocusTierVLM,
		"review": rec.Review, "split": rec.Split, "subject": rec.Subject, "composition": rec.Composition,
		"quality_score": rec.QualityScore, "keeper": rec.Keeper, "overridden": rec.Overridden,
		"rating": rec.Rating, "reviewed": rec.Reviewed, "group": rec.Group,
		"people_count": rec.PeopleCount, "description": rec.Description, "error": nullable(r.Error),
		"lr_rating": lr["rating"], "lr_label": lr["label"],
		"truth_tier": tr["focus_tier"], "truth_rating": tr["rating"], "truth_label": tr["label"],
	}, nil
}

// The fields of export.FinalRecord the image list filters and sorts on, as SQL.
type exprs struct{ keeper, loc, score, people, taken string }

func (s *Server) exprs() exprs {
	d := s.DB.D
	return exprs{
		keeper: fmt.Sprintf("COALESCE(%s, %s)", d.JBool("override_json", "keeper"), d.JBool("vlm_json", "keeper")),
		loc:    d.JNum("local_json", "local_tier"),
		score:  fmt.Sprintf("COALESCE(%s, %s)", d.JNum("override_json", "quality_score"), d.JNum("vlm_json", "quality_score")),
		people: fmt.Sprintf("COALESCE(%s, %s)", d.JNum("vlm_json", "people_count"), d.JNum("local_json", "n_people")),
		taken:  d.JText("local_json", "exif", "taken"),
	}
}

func scanCount(row *sql.Row) (int, error) {
	var n int
	err := row.Scan(&n)
	return n, err
}

func (s *Server) stats(*http.Request) (any, error) {
	d, x := s.DB.D, s.exprs()
	cfg := s.cfg()
	var tracked, analyzed, tagged, errs, review, keepers, lrRated int
	err := s.DB.QueryRow("SELECT COUNT(*), COUNT(*) FILTER (WHERE local_json IS NOT NULL), "+
		"COUNT(*) FILTER (WHERE vlm_json IS NOT NULL), COUNT(*) FILTER (WHERE error IS NOT NULL), "+
		"COUNT(*) FILTER (WHERE "+d.Review()+"), COUNT(*) FILTER (WHERE "+x.keeper+" = TRUE), "+
		"COUNT(*) FILTER (WHERE "+d.JNum("lr_json", "rating")+" > 0) FROM images").
		Scan(&tracked, &analyzed, &tagged, &errs, &review, &keepers, &lrRated)
	if err != nil {
		return nil, err
	}
	tierSQL := d.FinalTier(s.focusSource(cfg))
	tiers := pj.Obj{"tier0": 0, "tier1": 0, "tier2": 0, "tier3": 0}
	rows, err := s.DB.Query("SELECT " + tierSQL + " t, COUNT(*) FROM images WHERE local_json IS NOT NULL GROUP BY t")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t sql.NullFloat64
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if t.Valid {
			k := fmt.Sprintf("tier%d", int(t.Float64))
			tiers[k] = pj.Int(tiers[k]) + n
		}
	}
	rows.Close()
	lrRating := d.JNum("lr_json", "rating")
	rows, err = s.DB.Query("SELECT " + tierSQL + " tier, " + lrRating + " rating, COUNT(*) FROM images " +
		"WHERE local_json IS NOT NULL AND " + lrRating + " IS NOT NULL GROUP BY tier, rating ORDER BY tier, rating")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byTier := []any{}
	for rows.Next() {
		var t, rt sql.NullFloat64
		var n int
		if err := rows.Scan(&t, &rt, &n); err != nil {
			return nil, err
		}
		byTier = append(byTier, pj.Obj{"tier": nullF(t), "rating": nullF(rt), "n": n})
	}
	return pj.Obj{"tracked": tracked, "analyzed": analyzed, "tagged": tagged, "errors": errs, "review": review,
		"keepers": keepers, "lr_rated": lrRated, "tiers": tiers, "lr_by_tier": byTier}, rows.Err()
}

func nullF(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

// ---- browse -------------------------------------------------------------------------------------------------------

func (s *Server) tree(r *http.Request) (any, error) {
	base, err := s.safePath(r.URL.Query().Get("path"))
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		return nil, errf(404, "not a directory")
	}
	// Every tracked folder under base, rolled up into the child of base it sits in.
	fs, err := s.DB.FolderStats(base)
	if err != nil {
		return nil, err
	}
	agg := map[string]pj.Obj{}
	zero := func() pj.Obj { return pj.Obj{"tracked": 0, "local_done": 0, "vlm_done": 0, "errors": 0} }
	for folder, st := range fs {
		rel, err := filepath.Rel(base, folder)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		child := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		a, ok := agg[child]
		if !ok {
			a = zero()
			agg[child] = a
		}
		a["tracked"] = pj.Int(a["tracked"]) + st.N
		a["local_done"] = pj.Int(a["local_done"]) + st.LocalDone
		a["vlm_done"] = pj.Int(a["vlm_done"]) + st.VLMDone
		a["errors"] = pj.Int(a["errors"]) + st.Err
	}
	hereRows, err := s.DB.Rows("folder = ?", []any{base}, "", -1, 0, "")
	if err != nil {
		return nil, err
	}
	here := map[string]db.Image{}
	for _, row := range hereRows {
		here[row.Path] = row
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsPermission(err) {
			return nil, errf(403, "permission denied")
		}
		return nil, err
	}
	slices.SortStableFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(strings.ToLower(a.Name()), strings.ToLower(b.Name()))
	})
	source := s.focusSource(s.cfg())
	dirs, files := []any{}, []any{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(base, e.Name())
		if e.IsDir() { // is_dir(follow_symlinks=False): a DirEntry reports the link itself
			n := 0
			if sub, err := os.ReadDir(p); err == nil {
				for _, x := range sub {
					if isFile(p, x) && scan.IsImage(x.Name()) {
						n++
					}
				}
			}
			d := pj.Obj{"name": e.Name(), "path": s.rel(p), "images_direct": n}
			a := agg[e.Name()]
			if a == nil {
				a = zero()
			}
			for k, v := range a {
				d[k] = v
			}
			dirs = append(dirs, d)
		} else if isFile(base, e) && scan.IsImage(e.Name()) {
			if row, ok := here[p]; ok {
				sm, err := s.summary(row, source)
				if err != nil {
					return nil, err
				}
				files = append(files, sm)
			} else {
				files = append(files, pj.Obj{"id": nil, "name": e.Name(), "rel": s.rel(p), "path": p, "status": "untracked"})
			}
		}
	}
	path := ""
	if base != resolve(s.PhotosRoot) {
		path = s.rel(base)
	}
	return pj.Obj{"path": path, "dirs": dirs, "files": files}, nil
}

// isFile is DirEntry.is_file(): a regular file, following a symlink.
func isFile(dir string, e os.DirEntry) bool {
	if e.Type().IsRegular() {
		return true
	}
	if e.Type()&os.ModeSymlink != 0 {
		st, err := os.Stat(filepath.Join(dir, e.Name()))
		return err == nil && st.Mode().IsRegular()
	}
	return false
}

// ---- images -------------------------------------------------------------------------------------------------------

type filter struct {
	where []string
	args  []any
}

func (f *filter) add(cond string, args ...any) {
	f.where = append(f.where, cond)
	f.args = append(f.args, args...)
}

// between: sql within [lo, hi]; either end may be open. A photo without the value never matches a bound.
func between[T any](f *filter, sql string, lo, hi *T) {
	if lo != nil {
		f.add(sql+" >= ?", *lo)
	}
	if hi != nil {
		f.add(sql+" <= ?", *hi)
	}
}

func boolLit(b bool) string {
	if b {
		return "TRUE"
	}
	return "FALSE"
}

func (s *Server) listImages(r *http.Request) (any, error) {
	d, x, q := s.DB.D, s.exprs(), r.URL.Query()
	cfg := s.cfg()
	var f filter
	var err error
	ints := map[string]*int{}
	for _, k := range []string{"tier", "lr_rating", "truth_tier", "rating", "group", "local_tier", "vlm_tier", "people_min", "people_max"} {
		if ints[k], err = qInt(r, k); err != nil {
			return nil, err
		}
	}
	floats := map[string]*float64{}
	for _, k := range []string{"score_min", "score_max", "eye_min", "eye_max", "iso_min", "iso_max", "f_min", "f_max",
		"shutter_min", "shutter_max", "focal_min", "focal_max"} {
		if floats[k], err = qFloat(r, k); err != nil {
			return nil, err
		}
	}
	bools := map[string]*bool{}
	for _, k := range []string{"recursive", "keeper", "review", "split", "truth_mismatch", "reviewed", "stale", "lifted", "overridden", "noted"} {
		if bools[k], err = qBool(r, k); err != nil {
			return nil, err
		}
	}
	offset, err := qIntDefault(r, "offset", 0, 0)
	if err != nil {
		return nil, err
	}
	limit, err := qIntDefault(r, "limit", 60, 500)
	if err != nil {
		return nil, err
	}
	finalTier := d.FinalTier(s.focusSource(cfg))

	if folder := q.Get("folder"); folder != "" {
		base, err := s.safePath(folder)
		if err != nil {
			return nil, err
		}
		if b := bools["recursive"]; b == nil || *b {
			w, a := d.UnderFolder(base, "folder")
			f.add(w, a...)
		} else {
			f.add("folder = ?", base)
		}
	}
	switch q.Get("status") {
	case "pending":
		f.add("local_json IS NULL")
	case "analyzed":
		f.add("local_json IS NOT NULL AND vlm_json IS NULL AND vlm_skip IS NULL")
	case "skipped":
		f.add("local_json IS NOT NULL AND vlm_json IS NULL AND vlm_skip IS NOT NULL")
	case "tagged":
		f.add("vlm_json IS NOT NULL")
	case "error":
		f.add("error IS NOT NULL")
	}
	if v := ints["tier"]; v != nil {
		f.add(finalTier+" = ?", *v)
	}
	if v := bools["keeper"]; v != nil {
		f.add(x.keeper + " = " + boolLit(*v))
	}
	if v := q.Get("subject"); v != "" {
		f.add(d.JText("vlm_json", "primary_subject")+" = ?", v)
	}
	if v := bools["review"]; v != nil && *v {
		f.add(d.Review())
	}
	if v := bools["split"]; v != nil { // the local metrics disagree among themselves, or don't
		not := ""
		if *v {
			not = "NOT "
		}
		f.add(d.J("local_json", "split") + " IS " + not + "NULL" + nullJSON(d, "local_json", "split", *v))
	}
	if v := ints["lr_rating"]; v != nil {
		f.add(d.JNum("lr_json", "rating")+" = ?", *v)
	}
	if v := q.Get("lr_label"); v != "" {
		f.add(d.JText("lr_json", "label")+" = ?", v)
	}
	if v := ints["truth_tier"]; v != nil {
		f.add(d.JNum("truth_json", "focus_tier")+" = ?", *v)
	}
	if v := bools["truth_mismatch"]; v != nil && *v {
		tt := d.JNum("truth_json", "focus_tier")
		f.add(tt + " IS NOT NULL AND local_json IS NOT NULL AND " + tt + " != " + finalTier)
	}
	if v := ints["rating"]; v != nil {
		f.add(d.JNum("override_json", "rating")+" = ?", *v)
	}
	if v := bools["reviewed"]; v != nil {
		f.add("COALESCE(" + d.JBool("override_json", "reviewed") + ", FALSE) = " + boolLit(*v))
	}
	if v := ints["group"]; v != nil { // 0: in no group
		f.add("COALESCE("+d.JNum("override_json", "group")+", 0) = ?", *v)
	}
	if v := ints["local_tier"]; v != nil {
		f.add(x.loc+" = ?", *v)
	}
	if v := ints["vlm_tier"]; v != nil {
		f.add(d.JNum("vlm_json", "focus_tier")+" = ?", *v)
	}
	if st := q.Get("stages"); st == "agree" || st == "disagree" { // local vs a current (not stale) model verdict
		op := "="
		if st == "disagree" {
			op = "!="
		}
		f.add(x.loc + " IS NOT NULL AND vlm_json IS NOT NULL AND NOT " + d.VLMStale() + " AND " + x.loc + " " + op + " " +
			d.JNum("vlm_json", "focus_tier"))
	}
	if v := bools["stale"]; v != nil {
		if *v {
			f.add(d.VLMStale())
		} else {
			f.add("NOT " + d.VLMStale())
		}
	}
	if v := q.Get("composition"); v != "" {
		f.add(d.JText("vlm_json", "composition")+" = ?", v)
	}
	switch v := q.Get("eye_src"); {
	case v == "none":
		f.add("local_json IS NOT NULL AND " + d.JText("local_json", "primary_eye_src") + " IS NULL")
	case v != "":
		f.add(d.JText("local_json", "primary_eye_src")+" = ?", v)
	}
	if v := q.Get("primary_by"); v != "" {
		f.add(d.JText("local_json", "primary_by")+" = ?", v)
	}
	if v := q.Get("detector"); v != "" {
		f.add(d.JText("local_json", "detector")+" = ?", v)
	}
	if v := bools["lifted"]; v != nil {
		not := ""
		if *v {
			not = "NOT "
		}
		f.add(d.JNum("local_json", "exposure", "ev") + " IS " + not + "NULL")
	}
	if v := bools["overridden"]; v != nil {
		empty := d.EmptyObject("override_json")
		if *v {
			f.add("override_json IS NOT NULL AND NOT " + empty)
		} else {
			f.add("(override_json IS NULL OR " + empty + ")")
		}
	}
	if v := bools["noted"]; v != nil {
		op := "="
		if *v {
			op = "!="
		}
		f.add("COALESCE(" + d.JText("override_json", "note") + ", '') " + op + " ''")
	}
	if v := q.Get("camera"); v != "" {
		f.add(d.JText("local_json", "exif", "camera")+" = ?", v)
	}
	if v := q.Get("lens"); v != "" {
		f.add(d.JText("local_json", "exif", "lens")+" = ?", v)
	}
	between(&f, x.people, ints["people_min"], ints["people_max"])
	between(&f, x.score, floats["score_min"], floats["score_max"])
	between(&f, d.JNum("local_json", "primary_eye_sharp"), floats["eye_min"], floats["eye_max"])
	between(&f, d.JNum("local_json", "exif", "iso"), floats["iso_min"], floats["iso_max"])
	between(&f, d.JNum("local_json", "exif", "f_number"), floats["f_min"], floats["f_max"])
	between(&f, d.JNum("local_json", "exif", "shutter_s"), floats["shutter_min"], floats["shutter_max"])
	between(&f, fmt.Sprintf("COALESCE(%s, %s)", d.JNum("local_json", "exif", "focal_35mm"), d.JNum("local_json", "exif", "focal_mm")),
		floats["focal_min"], floats["focal_max"])
	// EXIF dates read "2024:05:01 12:00:00"; the bounds come in as ISO dates, and `to` takes the whole day
	day := func(k string) *string {
		v := q.Get(k)
		if v == "" {
			return nil
		}
		v = strings.ReplaceAll(v[:min(10, len(v))], "-", ":")
		return &v
	}
	between(&f, "substr("+x.taken+", 1, 10)", day("taken_from"), day("taken_to"))
	if v := q.Get("q"); v != "" {
		like := "%" + v + "%"
		lk := " " + d.Like() + " ? "
		f.add("(path"+lk+"OR "+d.AsText("vlm_json")+lk+"OR "+d.AsText("override_json")+lk+")", like, like, like)
	}
	order := s.order(q.Get("sort"))
	w := "1=1"
	if len(f.where) > 0 {
		w = strings.Join(f.where, " AND ")
	}
	total, err := s.DB.Count(w, f.args...)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Rows(w, f.args, order, limit, offset, "")
	if err != nil {
		return nil, err
	}
	source := s.focusSource(cfg)
	items := make([]any, len(rows))
	for i, row := range rows {
		if items[i], err = s.summary(row, source); err != nil {
			return nil, err
		}
	}
	return pj.Obj{"total": total, "offset": offset, "items": items}, nil
}

// nullJSON: on Postgres, a JSON null in the column reads as a value (not SQL NULL) through #>; json_extract gives SQL
// NULL for it. Match SQLite: a JSON null counts as NULL.
func nullJSON(d db.Dialect, col, key string, isNot bool) string {
	if !d.PG {
		return ""
	}
	t := fmt.Sprintf("jsonb_typeof(%s)", d.J(col, key))
	if isNot {
		return " AND " + t + " != 'null'"
	}
	return " OR " + t + " = 'null'"
}

// order is the ORDER BY for a sort key. NULLs sort as SQLite puts them (first ascending, last descending) on both.
func (s *Server) order(key string) string {
	d, x := s.DB.D, s.exprs()
	path := d.Collate("path")
	eye := d.JNum("local_json", "primary_eye_sharp")
	lr := d.JNum("lr_json", "rating")
	reviewedAt := d.JNum("override_json", "reviewed_at")
	iso := d.JNum("local_json", "exif", "iso")
	asc := func(e string) string { return e + " ASC NULLS FIRST" }
	desc := func(e string) string { return e + " DESC NULLS LAST" }
	switch key {
	case "newest":
		return "id DESC"
	case "score":
		return desc(x.score) + ", " + path
	case "score_low":
		return x.score + " IS NULL, " + asc(x.score) + ", " + path
	case "sharpness":
		return desc(d.JNum("local_json", "primary_head_sharp"))
	case "eye_sharpness":
		return desc(eye)
	case "eye_softest":
		return eye + " IS NULL, " + asc(eye)
	case "taken":
		return x.taken + " IS NULL, " + asc(d.Collate(x.taken)) + ", " + path
	case "taken_desc":
		return desc(d.Collate(x.taken)) + ", " + path
	case "people":
		return desc(x.people) + ", " + path
	case "iso":
		return desc(iso) + ", " + path
	case "name":
		return d.Collate("substr(path, length(rtrim(path, replace(path, '/', ''))) + 1)") + ", " + path
	case "shuffle":
		return "(id * 2654435761) % 4294967291" // a fixed scramble, so paging stays stable
	case "lr":
		return desc(lr) + ", " + path
	case "rated":
		return reviewedAt + " IS NULL, " + desc(reviewedAt) + ", id DESC"
	}
	return path
}

// facets are the values the image filters can take in this library, with counts: cameras, lenses, compositions,
// subjects and sidecar labels, plus the spread of the numeric EXIF fields and capture dates.
func (s *Server) facets(*http.Request) (any, error) {
	d, x := s.DB.D, s.exprs()
	counts := func(expr string) ([]any, error) {
		rows, err := s.DB.Query("SELECT " + expr + " v, COUNT(*) n FROM images WHERE " + expr + " IS NOT NULL AND " + expr +
			" != '' GROUP BY v ORDER BY n DESC, " + d.Collate(expr))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var v string
			var n int
			if err := rows.Scan(&v, &n); err != nil {
				return nil, err
			}
			out = append(out, pj.Obj{"value": v, "n": n})
		}
		return out, rows.Err()
	}
	ranges := pj.Obj{}
	for _, kv := range [][2]string{{"iso", d.JNum("local_json", "exif", "iso")}, {"f", d.JNum("local_json", "exif", "f_number")},
		{"shutter", d.JNum("local_json", "exif", "shutter_s")}, {"people", x.people},
		{"focal", fmt.Sprintf("COALESCE(%s, %s)", d.JNum("local_json", "exif", "focal_35mm"), d.JNum("local_json", "exif", "focal_mm"))},
		{"score", x.score}, {"eye", d.JNum("local_json", "primary_eye_sharp")}} {
		var lo, hi sql.NullFloat64
		if err := s.DB.QueryRow("SELECT MIN("+kv[1]+"), MAX("+kv[1]+") FROM images").Scan(&lo, &hi); err != nil {
			return nil, err
		}
		ranges[kv[0]] = []any{nullF(lo), nullF(hi)}
	}
	var lo, hi sql.NullString
	if err := s.DB.QueryRow("SELECT MIN("+d.Collate(x.taken)+"), MAX("+d.Collate(x.taken)+") FROM images").Scan(&lo, &hi); err != nil {
		return nil, err
	}
	ranges["taken"] = []any{nullable(nullStr(lo)), nullable(nullStr(hi))}
	out := pj.Obj{"ranges": ranges}
	for _, kv := range [][2]string{{"cameras", d.JText("local_json", "exif", "camera")}, {"lenses", d.JText("local_json", "exif", "lens")},
		{"compositions", d.JText("vlm_json", "composition")}, {"subjects", d.JText("vlm_json", "primary_subject")},
		{"lr_labels", d.JText("lr_json", "label")}} {
		c, err := counts(kv[1])
		if err != nil {
			return nil, err
		}
		out[kv[0]] = c
	}
	return out, nil
}

func nullStr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func (s *Server) image(r *http.Request) (db.Image, error) {
	id, err := pathID(r)
	if err != nil {
		return db.Image{}, err
	}
	row, err := s.DB.Row(id)
	if err == db.ErrNotFound {
		return row, errNotFound
	}
	return row, err
}

func (s *Server) imageOut(row db.Image) (any, error) {
	source := s.focusSource(s.cfg())
	out, err := s.summary(row, source)
	if err != nil {
		return nil, err
	}
	loc, vlm := pj.Parse(row.LocalJSON), pj.Parse(row.VLMJSON)
	rec, err := export.FinalRecord(exportRow(row), source)
	if err != nil {
		return nil, err
	}
	out["local"], out["vlm"] = orNil(loc), orNil(vlm)
	out["vlm_stale"] = export.VLMStale(loc, vlm)
	out["override"], out["usage"] = orNil(pj.Parse(row.OverrideJSON)), orNil(pj.Parse(row.VLMUsage))
	out["final"] = rec
	return out, nil
}

func orNil(o pj.Obj) any {
	if o == nil {
		return nil
	}
	return o
}

func (s *Server) getImage(r *http.Request) (any, error) {
	row, err := s.image(r)
	if err != nil {
		return nil, err
	}
	return s.imageOut(row)
}

// overrideIn is a manual correction from the UI.
type overrideIn struct {
	Rating       *int    `json:"rating"` // your cull: 0-3 focus tier, 4 banger; marks the photo reviewed
	FocusTier    *int    `json:"focus_tier"`
	QualityScore *int    `json:"quality_score"` // your stars; beat the model's score and export as xmp:Rating
	Group        *int    `json:"group"`         // your sort group (keys a s d f); see config "groups"
	Keeper       *bool   `json:"keeper"`
	Note         *string `json:"note"`
	Clear        bool    `json:"clear"`
	ClearRating  bool    `json:"clear_rating"` // drop just your rating (and the reviewed mark); keeps score, keeper and note
	ClearScore   bool    `json:"clear_score"`  // drop just your stars; the model's score shows through again
	ClearGroup   bool    `json:"clear_group"`  // take the photo back out of its group
}

func inRange(name string, v *int, lo, hi int) error {
	if v != nil && (*v < lo || *v > hi) {
		return &HTTPError{422, fmt.Sprintf("%s: must be between %d and %d", name, lo, hi)}
	}
	return nil
}

func (s *Server) patchImage(r *http.Request) (any, error) {
	row, err := s.image(r)
	if err != nil {
		return nil, err
	}
	var o overrideIn
	if err := decodeBody(r, &o); err != nil {
		return nil, err
	}
	for _, c := range []struct {
		n      string
		v      *int
		lo, hi int
	}{{"rating", o.Rating, 0, 4}, {"focus_tier", o.FocusTier, 0, 3}, {"quality_score", o.QualityScore, 1, 5}, {"group", o.Group, 1, 4}} {
		if err := inRange(c.n, c.v, c.lo, c.hi); err != nil {
			return nil, err
		}
	}
	cur := pj.Parse(row.OverrideJSON)
	if cur == nil {
		cur = pj.Obj{}
	}
	switch {
	case o.Clear:
		cur = pj.Obj{}
	case o.ClearRating:
		for _, k := range []string{"rating", "focus_tier", "reviewed", "reviewed_at"} {
			delete(cur, k)
		}
	case o.ClearScore:
		delete(cur, "quality_score")
	case o.ClearGroup:
		delete(cur, "group")
	default:
		set := func(k string, v any, present bool) {
			if present {
				cur[k] = v
			}
		}
		set("rating", nullable(o.Rating), o.Rating != nil)
		set("focus_tier", nullable(o.FocusTier), o.FocusTier != nil)
		set("quality_score", nullable(o.QualityScore), o.QualityScore != nil)
		set("group", nullable(o.Group), o.Group != nil)
		set("keeper", nullable(o.Keeper), o.Keeper != nil)
		set("note", nullable(o.Note), o.Note != nil)
		// A rating or a focus tier is your verdict on the photo: the two stay in step, and the photo counts as
		// reviewed. Only another rating or a reset changes it; jobs and rescans never write override_json.
		if o.Rating != nil || o.FocusTier != nil {
			rating := o.FocusTier
			if o.Rating != nil {
				rating = o.Rating
			}
			cur["rating"] = *rating
			cur["focus_tier"] = min(*rating, 3)
			cur["reviewed"], cur["reviewed_at"] = true, db.Now()
		}
	}
	if err := s.DB.SetOverride(row.ID, pj.DumpsPtr(cur)); err != nil {
		return nil, err
	}
	row, err = s.DB.Row(row.ID)
	if err != nil {
		return nil, err
	}
	return s.imageOut(row)
}

// trace: every rule the pipeline applies to this photo, in order, with its inputs and outcome (the Trace page).
func (s *Server) trace(r *http.Request) (any, error) {
	row, err := s.image(r)
	if err != nil {
		return nil, err
	}
	return trace.Trace(row, s.cfg(), s.rel(row.Path))
}

// vlmRequest is the request the vision model gets for this image, as the backend builds it, with image bytes replaced
// by their size. Built from the current cache and config, so it matches what was sent as long as neither changed.
func (s *Server) vlmRequest(r *http.Request) (any, error) {
	row, err := s.image(r)
	if err != nil && err != errNotFound {
		return nil, err
	}
	if err == errNotFound || row.LocalJSON == nil {
		return nil, errf(404, "not analyzed")
	}
	fp, cp := filepath.Join(s.CacheDir, itoa(row.ID)+".jpg"), filepath.Join(s.CacheDir, itoa(row.ID)+"_crop.jpg")
	fst, err := os.Stat(fp)
	if err != nil {
		return nil, errf(404, "frame not in cache")
	}
	cfg := s.cfg()
	bname := r.URL.Query().Get("backend")
	if bname == "" {
		bname = pj.Str(pj.Or(cfg["backend"], "ollama"))
	}
	be, err := backends.Get(bname, pj.Str(cfg["base_url"]))
	if err != nil {
		return nil, errf(400, "%s", err.Error())
	}
	model := r.URL.Query().Get("model")
	if model == "" {
		model = pj.Str(pj.Or(cfg["model"], be.DefaultModel()))
	}
	item, err := backends.LoadItem(s.CacheDir, row.ID, *row.LocalJSON)
	if err != nil {
		return nil, err
	}
	images := []any{pj.Obj{"label": "frame", "url": fmt.Sprintf("/media/frame/%d", row.ID), "bytes": fst.Size()}}
	if cst, err := os.Stat(cp); err == nil {
		images = append(images, pj.Obj{"label": "crop", "url": fmt.Sprintf("/media/crop/%d", row.ID), "bytes": cst.Size()})
	}
	var request, buildErr any
	if req, err := be.BuildRequest(item, model, cfg); err != nil {
		buildErr = err.Error()
	} else {
		request = backends.Redact(req)
	}
	return pj.Obj{"backend": bname, "model": model, "system": schema.SystemPrompt, "context": item.Context,
		"images": images, "request": request, "build_error": buildErr}, nil
}

// ---- pixels: served from the cache, or rendered by the analyzer -----------------------------------------------------

func (s *Server) media(suffix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeJSON(w, 422, map[string]any{"detail": err.Error()})
			return
		}
		s.serveCached(w, r, filepath.Join(s.CacheDir, itoa(id)+suffix+".jpg"))
	}
}

func (s *Server) serveCached(w http.ResponseWriter, r *http.Request, p string) {
	f, err := os.Open(p)
	if err != nil {
		writeJSON(w, 404, map[string]any{"detail": "Not Found"})
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeJSON(w, 500, map[string]any{"detail": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// full is the original at native resolution for the zoomable viewer; rendered once, then served from the cache.
func (s *Server) full(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, 422, map[string]any{"detail": err.Error()})
		return
	}
	p := filepath.Join(s.CacheDir, itoa(id)+"_full.jpg")
	if _, err := os.Stat(p); err != nil {
		row, err := s.DB.Row(id)
		if err != nil {
			writeJSON(w, 404, map[string]any{"detail": "Not Found"})
			return
		}
		if _, err := os.Stat(row.Path); err != nil {
			writeJSON(w, 404, map[string]any{"detail": "Not Found"})
			return
		}
		if s.Analyzer == nil {
			writeJSON(w, 503, map[string]any{"detail": "the analyzer is not running"})
			return
		}
		b, err := s.Analyzer.RenderFull(r.Context(), row.Path, s.cfg()["exposure"], 92)
		if err != nil {
			writeJSON(w, 502, map[string]any{"detail": err.Error()})
			return
		}
		os.MkdirAll(s.CacheDir, 0o755)
		tmp := fmt.Sprintf("%s.%d.%d.tmp", p, os.Getpid(), time.Now().UnixNano())
		if err := os.WriteFile(tmp, b, 0o644); err == nil {
			os.Rename(tmp, p)
		}
	}
	s.serveCached(w, r, p)
}

type debugEntry struct {
	key uint64
	id  int64
	val pj.Obj
}

// focusDebug: eye crops, Laplacian maps, FFT spectra and a sharpness heatmap, recomputed from the original file.
func (s *Server) focusDebug(r *http.Request) (any, error) {
	row, err := s.image(r)
	if err != nil && err != errNotFound {
		return nil, err
	}
	if err == errNotFound || row.LocalJSON == nil {
		return nil, errf(404, "not analyzed")
	}
	h := fnv.New64a()
	h.Write([]byte(*row.LocalJSON))
	key := h.Sum64()
	s.debugMu.Lock()
	for _, e := range s.debugCache {
		if e.id == row.ID && e.key == key {
			s.debugMu.Unlock()
			return e.val, nil
		}
	}
	s.debugMu.Unlock()
	if _, err := os.Stat(row.Path); err != nil {
		return nil, errf(404, "original file not found")
	}
	if s.Analyzer == nil {
		return nil, errf(503, "the analyzer is not running")
	}
	v, err := s.Analyzer.FocusDebug(r.Context(), pj.Obj{"path": row.Path, "local": pj.Parse(row.LocalJSON), "exposure": s.cfg()["exposure"]})
	if err != nil {
		return nil, errf(502, "%s", err.Error())
	}
	s.debugMu.Lock()
	if len(s.debugCache) >= 8 {
		s.debugCache = s.debugCache[1:]
	}
	s.debugCache = append(s.debugCache, debugEntry{key, row.ID, v})
	s.debugMu.Unlock()
	return v, nil
}

// detect runs a pose model on one image and returns its detections, storing nothing: the detail view's comparison of
// what another model would have found.
func (s *Server) detect(r *http.Request) (any, error) {
	row, err := s.image(r)
	if err != nil {
		return nil, err
	}
	if s.Analyzer == nil {
		return nil, errf(503, "the analyzer is not running")
	}
	cfg := s.cfg()
	det := local.Detector(cfg)
	if m := r.URL.Query().Get("model"); m != "" {
		det["model"] = m
	}
	req := local.MeasureRequest(cfg, row.ID, row.Path, s.CacheDir)
	req["detect"] = det
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	v, err := s.Analyzer.Detect(ctx, req)
	if err != nil {
		return nil, errf(502, "%s", err.Error())
	}
	return v, nil
}
