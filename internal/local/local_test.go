package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/rules"
)

// replay answers measure/finalize from a golden fixture, checking the order the rules picked is the one the old
// Python stage settled on.
type replay struct {
	t   *testing.T
	doc pj.Obj
}

func (r replay) Measure(context.Context, pj.Obj) (pj.Obj, error) {
	m := pj.Clone(pj.O(r.doc, "measure"))
	m["token"] = "t"
	return m, nil
}

func (r replay) Finalize(_ context.Context, req pj.Obj) (pj.Obj, error) {
	if !pj.Equal(req["order"], r.doc["order"]) {
		r.t.Errorf("order %v, the Python stage had %v", req["order"], r.doc["order"])
	}
	return pj.O(r.doc, "finalize"), nil
}

// storedMeta answers the file reads with what the Python stage stored, so this test pins the assembly and rules alone
// (packages exif and af have golden tests of their own).
func storedMeta(local pj.Obj, prior, noise func(pj.Obj, pj.Obj) pj.Obj) Meta {
	return Meta{
		Exif: func(string) pj.Obj { return pj.O(local, "exif") },
		AF:   func(string, int, int, bool) (pj.Obj, string) { return pj.O(local, "af"), pj.Str(local["af_note"]) },
		Score: func(pj.Obj, []pj.Obj, float64, float64) []float64 {
			panic("no AF points in the fixtures that reach here")
		},
		Prior:      func(pj.Obj, pj.Obj) pj.Obj { return pj.O(local, "exif_prior") },
		NoisePrior: func(pj.Obj, any, any, pj.Obj) pj.Obj { return pj.O(local, "noise") },
	}
}

func fixtures(t *testing.T) map[string]pj.Obj {
	out := map[string]pj.Obj{}
	for _, dir := range []string{"../../testdata/golden", "../../testdata/golden-local"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var doc pj.Obj
			if err := json.Unmarshal(b, &doc); err != nil {
				t.Fatal(err)
			}
			out[filepath.Base(f)] = doc
		}
	}
	if len(out) == 0 {
		t.Fatal("no golden fixtures")
	}
	return out
}

func TestAnalyzeReproducesThePythonStage(t *testing.T) {
	for name, doc := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			want := pj.O(doc, "local")
			meta := storedMeta(want, nil, nil)
			if pj.Truthy(want["af"]) {
				meta.Score = afScoreFromStored(want)
			}
			got, err := Analyze(context.Background(), replay{t, doc}, meta, pj.O(doc, "config"), 1, "x", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			delete(got, "detector") // new: which pose model produced the row
			if d := diff(want, got, ""); d != "" {
				t.Error(d)
			}
		})
	}
}

// afScoreFromStored replays the af_score Python stored for each person (matched by box).
func afScoreFromStored(local pj.Obj) rules.Scorer {
	return func(af pj.Obj, ps []pj.Obj, near, occlude float64) []float64 {
		out := make([]float64, len(ps)) // beyond the six stored: 0, never the AF pick in the fixtures
		for i, p := range ps {
			for _, q := range pj.A(local, "people") {
				if pj.Equal(pj.Get(q, "box"), p["box"]) {
					out[i] = pj.F(pj.Get(q, "af_score"))
					break
				}
			}
		}
		return out
	}
}

func diff(a, b any, path string) string {
	switch x := a.(type) {
	case pj.Obj:
		y, ok := b.(pj.Obj)
		if !ok {
			return fmt.Sprintf("%s: %v vs %v", path, a, b)
		}
		for k := range x {
			if _, ok := y[k]; !ok {
				return fmt.Sprintf("%s.%s missing", path, k)
			}
			if d := diff(x[k], y[k], path+"."+k); d != "" {
				return d
			}
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				return fmt.Sprintf("%s.%s extra: %v", path, k, y[k])
			}
		}
		return ""
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return fmt.Sprintf("%s: %v vs %v", path, a, b)
		}
		for i := range x {
			if d := diff(x[i], y[i], fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		return ""
	}
	if !pj.Equal(a, b) {
		return fmt.Sprintf("%s: %v vs %v", path, a, b)
	}
	return ""
}
