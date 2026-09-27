package trace

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mononendev/photosort/internal/config"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

// testdata/trace_golden.json holds photosort.trace.trace()'s output (the Python implementation) for rows built from
// the golden local results and hand-made edge cases, under the default config and three variants. It was written
// by a one-off generator run against the old package (not kept in the repo).
type goldenFile struct {
	Cfgs  []pj.Obj         `json:"cfgs"`
	Rows  []map[string]any `json:"rows"`
	Cases []struct {
		Row   int             `json:"row"`
		Cfg   int             `json:"cfg"`
		Rel   string          `json:"rel"`
		Trace json.RawMessage `json:"trace"`
	} `json:"cases"`
}

func strPtr(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func imageRow(m map[string]any) db.Image {
	im := db.Image{ID: int64(pj.F(m["id"])), Path: m["path"].(string), LocalJSON: strPtr(m["local_json"]),
		VLMJSON: strPtr(m["vlm_json"]), VLMUsage: strPtr(m["vlm_usage"]), VLMSkip: strPtr(m["vlm_skip"]),
		OverrideJSON: strPtr(m["override_json"]), Error: strPtr(m["error"]), LRJSON: strPtr(m["lr_json"])}
	if f, ok := m["size"].(float64); ok {
		n := int64(f)
		im.Size = &n
	}
	return im
}

// diff reports where two decoded JSON values part: numbers by value, strings exactly, objects key by key.
func diff(path string, want, got any) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want object, got %#v", path, got)}
		}
		keys := map[string]bool{}
		for k := range w {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		var ks []string
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		var out []string
		for _, k := range ks {
			wv, wok := w[k]
			gv, gok := g[k]
			if wok != gok {
				out = append(out, fmt.Sprintf("%s.%s: present want %v, got %v", path, k, wok, gok))
				continue
			}
			out = append(out, diff(path+"."+k, wv, gv)...)
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return []string{fmt.Sprintf("%s: want %s, got %s", path, pj.Dumps(want), pj.Dumps(got))}
		}
		var out []string
		for i := range w {
			out = append(out, diff(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])...)
		}
		return out
	}
	if !reflect.DeepEqual(want, got) {
		return []string{fmt.Sprintf("%s: want %s, got %s", path, pj.Dumps(want), pj.Dumps(got))}
	}
	return nil
}

func TestTraceMatchesPython(t *testing.T) {
	b, err := os.ReadFile("../../testdata/trace_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Cases) < 100 {
		t.Fatalf("only %d cases", len(g.Cases))
	}
	for i, c := range g.Cases {
		row := imageRow(g.Rows[c.Row])
		t.Run(fmt.Sprintf("%d_%s_cfg%d", i, c.Rel, c.Cfg), func(t *testing.T) {
			out, err := Trace(row, pj.Clone(g.Cfgs[c.Cfg]), c.Rel)
			if err != nil {
				t.Fatal(err)
			}
			ob, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(ob, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(c.Trace, &want); err != nil {
				t.Fatal(err)
			}
			if d := diff("$", want, got); len(d) > 0 {
				if len(d) > 15 {
					d = d[:15]
				}
				t.Errorf("trace differs from Python:\n%s", strings.Join(d, "\n"))
			}
		})
	}
}

func TestTraceErrors(t *testing.T) {
	bad := "{not json"
	list := "[1, 2]"
	cfg := config.Defaults()
	for name, row := range map[string]db.Image{
		"local not JSON":    {Path: "/a.jpg", LocalJSON: &bad},
		"vlm not an object": {Path: "/a.jpg", VLMJSON: &list},
		"lr not JSON":       {Path: "/a.jpg", LRJSON: &bad},
	} {
		if _, err := Trace(row, cfg, "a.jpg"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestConfigNumbersPrintAsPython(t *testing.T) {
	for _, c := range []struct {
		path string
		v    any
		want string
	}{
		{"focus.floor_tier", 2.0, "2"},
		{"focus.eyewear_ratio", 3.0, "3.0"},
		{"focus.plane_max_extra", 0.4, "0.4"},
		{"noise.high_iso", 6400.0, "6400"},
		{"af.near", 2.0, "2.0"},
		{"focus.use_eyes", true, "True"},
		{"focus.plane_body_max_extra", nil, "None"},
	} {
		if got := s(cv(c.path, c.v)); got != c.want {
			t.Errorf("%s = %v prints %q, want %q", c.path, c.v, got, c.want)
		}
	}
	for x, want := range map[float64]string{1.5: "+1.5", 0: "+0.0", -3.89: "-3.89", 2: "+2.0"} {
		if got := fmtPlus(x); got != want {
			t.Errorf("fmtPlus(%v) = %q, want %q", x, got, want)
		}
	}
}

// randomPerson is tests/test_trace.py's _person.
func randomPerson(rng *rand.Rand, x float64) pj.Obj {
	uniform := func(a, b float64) float64 { return a + (b-a)*rng.Float64() }
	maybe := func(v float64) any {
		if rng.IntN(2) == 0 {
			return nil
		}
		return v
	}
	top, h := uniform(50, 400), uniform(150, 600)
	box := []any{x, top, x + h*0.4, top + h}
	p := pj.Obj{"box": box, "head": []any{x, top, x + h*0.15, top + h*0.15},
		"torso": []any{x, top + h*0.2, x + h*0.4, top + h*0.6}, "conf": uniform(0.2, 1), "priority": rng.Float64(),
		"sharp_head": maybe(uniform(0, 0.06)), "sharp_body": uniform(0, 0.05), "sharp_eye": maybe(uniform(0, 0.1)),
		"hf_eye": maybe(uniform(0, 0.05))}
	if rng.Float64() < 0.7 {
		p["plane"] = pj.Obj{"head": 1.0, "near": 0.8, "torso": 0.9, "head_vs_near": uniform(0, 1), "head_vs_torso": uniform(0, 1)}
	}
	return p
}

// TestWalkMatchesLocalTier is tests/test_trace.py: the Trace page's step-by-step walk of the local tier must land
// where rules.LocalTier does, with exactly one rule deciding and every rule listed whether or not it was reached.
func TestWalkMatchesLocalTier(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	pick := func(xs ...any) any { return xs[rng.IntN(len(xs))] }
	for i := 0; i < 3000; i++ {
		cfg := config.Defaults()
		f := pj.O(cfg, "focus")
		f["floor_tier"] = pick(nil, 1.0, 2.0, 3.0)
		f["floor_grade"] = pick(2.0, 3.0)
		f["plane_body_max_extra"] = pick(nil, 0.5)
		f["plane_max_extra"] = pick(nil, 0.4)
		f["use_plane"] = rng.Float64() < 0.8
		f["use_front"] = rng.Float64() < 0.8
		f["use_eyes"] = rng.Float64() < 0.8
		f["use_hf"] = rng.Float64() < 0.8
		f["front_min_height"] = pick(0.5, 1.0)
		people := make([]pj.Obj, rng.IntN(5))
		for j := range people {
			people[j] = randomPerson(rng, 1500*rng.Float64())
		}
		ppl := make([]any, len(people))
		for j, p := range people {
			ppl[j] = p
		}
		local := pj.Obj{"width": int64(2000), "height": int64(1300), "n_people": int64(len(people)), "people": ppl,
			"exif_prior": pj.Obj{"motion_risk": pick(nil, "low", "high")}, "local_tier": nil, "local_reason": nil,
			"exif": pj.Obj{"iso": pick(nil, int64(400), int64(6400))}, "exposure": pick(nil, pj.Obj{"ev": 2.0}),
			"noise": pick(nil, pj.Obj{"sigma": 6 * rng.Float64()})}
		var primary pj.Obj
		var others []pj.Obj
		if len(people) > 0 {
			primary, others = people[0], people[1:]
		}
		st, check := localTierStage(local, cfg, people, primary, others)
		traced, _ := check.Get("traced")
		engine, _ := check.Get("engine")
		if !py.Eq(traced, engine) {
			t.Fatalf("case %d: walk says %s, LocalTier %s (people %s)", i, py.Repr(traced), py.Repr(engine), pj.Dumps(people))
		}
		nodes, _ := st.Get("nodes")
		decided, soft := 0, false
		for _, n := range nodes.([]any) {
			o := n.(*py.Object)
			if d, _ := o.Get("decided"); d == true {
				decided++
			}
			if q, _ := o.Get("q"); strings.Contains(q.(string), "soft person") {
				soft = true
			}
		}
		if decided != 1 || !soft {
			t.Fatalf("case %d: %d rules decided, soft-person rule listed %v", i, decided, soft)
		}
	}
}
