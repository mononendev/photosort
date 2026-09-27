package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mononendev/photosort/internal/config"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/db/dbtest"
	"github.com/mononendev/photosort/internal/pj"
)

// Rows analyzed before `priority` existed must re-score, the AF points must land, and a malformed row is reported
// rather than fatal.
func TestRescoreOldRowsWithoutPriority(t *testing.T) {
	dbtest.Backends(t, func(t *testing.T, d *db.DB) {
		dir := t.TempDir()
		raw, bad := filepath.Join(dir, "a.cr2"), filepath.Join(dir, "b.jpg")
		os.WriteFile(raw, []byte("x"), 0o644)
		os.WriteFile(bad, nil, 0o644)
		d.AddPaths([]string{raw, bad})
		near := pj.Obj{"box": []any{1400.0, 700.0, 2100.0, 3000.0}, "head": []any{1500.0, 700.0, 1950.0, 1150.0},
			"torso": []any{1400.0, 1150.0, 2100.0, 2000.0}, "sharp_head": 0.1, "sharp_body": 0.1}
		far := pj.Obj{"box": []any{100.0, 2000.0, 1000.0, 5000.0}, "head": []any{300.0, 2000.0, 800.0, 2500.0},
			"torso": []any{100.0, 2500.0, 1000.0, 3500.0}, "sharp_head": 0.2, "sharp_body": 0.2}
		ids := map[string]int64{}
		rows, _ := d.Rows("", nil, "", -1, 0, "id, path")
		for _, r := range rows {
			ids[r.Path] = r.ID
		}
		set := func(p string, v pj.Obj) {
			s := pj.Dumps(v)
			d.SetLocal(ids[p], &s, nil)
		}
		set(raw, pj.Obj{"width": 3456, "height": 5184, "people": []any{far, near}, "exif": pj.Obj{}, "local_tier": 0})
		set(bad, pj.Obj{"width": 10, "height": 10, "people": []any{nil}, "exif": pj.Obj{}, "local_tier": 0})
		meta := FileMeta()
		meta.AF = func(path string, W, H int, yUp bool) (pj.Obj, string) {
			if path == raw {
				return pj.Obj{"active": []any{1.0}}, "ok"
			}
			return nil, "no AF info in file"
		}
		meta.Score = func(af, p pj.Obj, near float64) float64 {
			if pj.Equal(p["head"], []any{1500.0, 700.0, 1950.0, 1150.0}) {
				return 2
			}
			return 0
		}
		res, err := Rescore(d, meta, config.Defaults(), true)
		if err != nil {
			t.Fatal(err)
		}
		if res.Errors != 1 || res.FirstError == nil || !strings.Contains(*res.FirstError, "b.jpg") {
			t.Errorf("errors: %+v", res)
		}
		r, _ := d.Row(ids[raw])
		got := pj.Parse(r.LocalJSON)
		if got["primary_by"] != "af" || !pj.Equal(pj.Get(pj.A(got, "people")[0], "head"), near["head"]) {
			t.Errorf("primary: %v", got)
		}
		if res.AFBackfilled != 1 || res.PrimaryChanged != 1 {
			t.Errorf("%+v", res)
		}
		if got["primary_head_sharp"] != 0.1 || got["local_tier"] != 3.0 {
			t.Errorf("primary fields: %v %v", got["primary_head_sharp"], got["local_tier"])
		}
	})
}

// A stored row rescored under the config it was made with changes nothing: the rules agree with themselves, and
// the EXIF/AF reads agree with what the Python stage stored.
func TestRescoreOfGoldenRowsIsANoOp(t *testing.T) {
	d := dbtest.SQLite(t)
	var paths []string
	docs := map[string]pj.Obj{}
	for name, doc := range fixtures(t) {
		p := pj.Str(doc["path"])
		if !filepath.IsAbs(p) {
			p = filepath.Join("../..", p)
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		abs, _ := filepath.Abs(p)
		paths = append(paths, abs)
		docs[abs] = doc
		_ = name
	}
	d.AddPaths(paths)
	rows, _ := d.Rows("", nil, "", -1, 0, "id, path")
	for _, r := range rows {
		s := pj.Dumps(pj.O(docs[r.Path], "local"))
		d.SetLocal(r.ID, &s, nil)
	}
	cfg := pj.O(docs[paths[0]], "config")
	res, err := Rescore(d, FileMeta(), cfg, true)
	if err != nil || res.Changed != 0 || res.Errors != 0 || res.PrimaryChanged != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	after, _ := d.Rows("", nil, "", -1, 0, "path, local_json")
	for _, r := range after {
		if d := diff(pj.O(docs[r.Path], "local"), pj.Parse(r.LocalJSON), ""); d != "" {
			t.Errorf("%s: %s", filepath.Base(r.Path), d)
		}
	}
}
