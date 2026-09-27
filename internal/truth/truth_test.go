package truth

import (
	"encoding/base64"
	"encoding/json"
	"math/rand"
	"os"
	"sort"
	"testing"

	"github.com/mononendev/photosort/internal/config"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

// Ports of tests/test_truth.py, plus the truth sections of testdata/export_golden.json (written by the Python).

var cfg = pj.Obj{"truth": pj.Obj{
	"label_tiers":  pj.Obj{"Green": 3.0, "Yellow": 2.0, "Orange": 1.0, "Red": 0.0},
	"rating_tiers": pj.Obj{"5": 3.0, "4": 3.0, "3": 2.0, "2": 1.0, "1": 0.0, "0": nil}}}

func ip(n int) *int       { return &n }
func sp(s string) *string { return &s }
func tier(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestParseXMPWithKeywordsAndTierResolution(t *testing.T) {
	x := `<rdf:Description xmp:Rating="5" xmp:Label="Green"><dc:subject><rdf:Bag><rdf:li>onewheel</rdf:li><rdf:li>focus:1</rdf:li></rdf:Bag></dc:subject></rdf:Description>`
	v := ParseXMP(x)
	if *v.Rating != 5 || *v.Label != "Green" || len(v.Keywords) != 2 || v.Keywords[0] != "onewheel" || v.Keywords[1] != "focus:1" {
		t.Fatalf("%+v", v)
	}
	for i, c := range []struct {
		v    Verdict
		want any
	}{
		{v, 1}, // explicit keyword wins
		{Verdict{Label: sp("Red")}, 0},
		{Verdict{Label: sp("Orange")}, 1},
		{Verdict{Rating: ip(4)}, 3},
		{Verdict{Keywords: []string{"focus3"}}, 3},
		{Verdict{Rating: ip(0)}, nil},
	} {
		if got := tier(ResolveTier(c.v, cfg)); got != c.want {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
}

func TestParseCSVAndFiles(t *testing.T) {
	csv := "name,rating,focus_tier,keywords\nIMG_1.CR2,4,3,a;b\nIMG_2.cr2,,0,\n"
	v, err := ParseFiles([]File{{"truth.csv", []byte(csv)}, {"IMG_3.xmp", []byte(`<x xmp:Rating="2"/>`)}})
	if err != nil {
		t.Fatal(err)
	}
	if *v["img_1"].FocusTier != 3 || len(v["img_1"].Keywords) != 2 || v["img_1"].Keywords[1] != "b" {
		t.Fatalf("%+v", v["img_1"])
	}
	if *v["img_2"].FocusTier != 0 || *v["img_3"].Rating != 2 {
		t.Fatalf("%+v %+v", v["img_2"], v["img_3"])
	}
}

func TestSuggestThresholdsSeparatesClasses(t *testing.T) {
	var pairs []Pair
	for i := 1; i < 15; i++ {
		pairs = append(pairs, Pair{0.001 * float64(i), 0})
	}
	for i := 0; i < 15; i++ {
		pairs = append(pairs, Pair{0.02 + 0.0005*float64(i), 1}, Pair{0.035 + 0.0005*float64(i), 2}, Pair{0.06 + 0.002*float64(i), 3})
	}
	s := map[string]Suggestion{}
	for _, x := range SuggestThresholds(pairs) {
		s[x.Key()] = x
	}
	if v := s["tier3_min"]; v.Value < 0.043 || v.Value > 0.06 || v.BalancedAccuracy < 0.95 {
		t.Error(v)
	}
	if v := s["tier2_min"].Value; v < 0.028 || v > 0.035 {
		t.Error(v)
	}
	if v := s["tier1_min"].Value; v < 0.014 || v > 0.02 {
		t.Error(v)
	}
	few := []Pair{{0.1, 3}, {0.1, 3}, {0.1, 3}, {0.1, 3}, {0.1, 3}}
	if got := py.Dumps(SuggestThresholds(few)); got != "{}" {
		t.Error(got)
	}
}

func TestMetricsMapToConfigKeys(t *testing.T) {
	focus := pj.O(config.Defaults(), "focus")
	for name, m := range Metrics {
		if len(m.Path) < 10 || m.Path[:10] != "$.primary_" {
			t.Error(name, m.Path)
		}
		for _, k := range m.Keys {
			if _, ok := focus[k]; !ok {
				t.Error(name, k)
			}
		}
	}
	if names := MetricNames(); len(names) != 3 || names[0] != "eye" {
		t.Error(names)
	}
}

func TestSuggestedCutsStayOrdered(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for range 20 { // small, noisy sets: each tier's best cut on its own can cross the next one's
		pairs := make([]Pair, 40)
		for i := range pairs {
			pairs[i] = Pair{0.001 + rng.Float64()*0.099, rng.Intn(4)}
		}
		s := SuggestThresholds(pairs)
		for i := 1; i < len(s); i++ { // tier 3 first: each later (lower) tier's cut is at most the previous
			if s[i].Value > s[i-1].Value {
				t.Fatalf("cuts out of order: %v", s)
			}
		}
	}
}

type goldenTruth struct {
	Suggest []struct {
		Pairs [][2]float64 `json:"pairs"`
		Out   string       `json:"out"`
	} `json:"suggest"`
	Truth []struct {
		Files        [][2]string       `json:"files"`
		Verdicts     map[string]string `json:"verdicts"`
		Tiers        map[string]any    `json:"tiers"`
		DefaultTiers map[string]any    `json:"default_tiers"`
	} `json:"truth"`
	TruthBad []struct {
		Files [][2]string `json:"files"`
		Exc   string      `json:"exc"`
	} `json:"truth_bad"`
	CP437 string `json:"cp437"`
}

func loadGolden(t *testing.T) goldenTruth {
	t.Helper()
	b, err := os.ReadFile("../../testdata/export_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenTruth
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Suggest) == 0 || len(g.Truth) == 0 || len(g.TruthBad) == 0 {
		t.Fatal("golden file is incomplete")
	}
	return g
}

func files(t *testing.T, fs [][2]string) []File {
	out := make([]File, len(fs))
	for i, f := range fs {
		b, err := base64.StdEncoding.DecodeString(f[1])
		if err != nil {
			t.Fatal(err)
		}
		out[i] = File{f[0], b}
	}
	return out
}

func TestSuggestThresholdsMatchesPython(t *testing.T) {
	for i, c := range loadGolden(t).Suggest {
		pairs := make([]Pair, len(c.Pairs))
		for j, p := range c.Pairs {
			pairs[j] = Pair{p[0], int(p[1])}
		}
		if got := py.Dumps(SuggestThresholds(pairs)); got != c.Out {
			t.Errorf("set %d:\n got %s\nwant %s", i, got, c.Out)
		}
	}
}

func TestParseFilesMatchesPython(t *testing.T) {
	g := loadGolden(t)
	goldCfg := pj.Obj{"truth": pj.Obj{
		"label_tiers":  pj.Obj{"Green": 3.0, "Yellow": 2.0, "Orange": 1.0, "Red": 0.0, "Purple": nil},
		"rating_tiers": pj.Obj{"5": 3.0, "4": 3.0, "3": 2.0, "2": 1.0, "1": 0.0, "0": nil, "-1": 7.0}}}
	for i, c := range g.Truth {
		v, err := ParseFiles(files(t, c.Files))
		if err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		var got, want []string
		for k := range v {
			got = append(got, k)
		}
		for k := range c.Verdicts {
			want = append(want, k)
		}
		sort.Strings(got)
		sort.Strings(want)
		if py.Dumps(toAny(got)) != py.Dumps(toAny(want)) {
			t.Errorf("input %d: stems %q, want %q", i, got, want)
			continue
		}
		for k, w := range c.Verdicts {
			if j := v[k].JSON(); j != w {
				t.Errorf("input %d %s:\n got %s\nwant %s", i, k, j, w)
			}
			if !pj.Equal(tier(ResolveTier(v[k], goldCfg)), c.Tiers[k]) {
				t.Errorf("input %d %s: tier %v, want %v", i, k, tier(ResolveTier(v[k], goldCfg)), c.Tiers[k])
			}
			if !pj.Equal(tier(ResolveTier(v[k], pj.Obj{})), c.DefaultTiers[k]) {
				t.Errorf("input %d %s: default tier %v, want %v", i, k, tier(ResolveTier(v[k], pj.Obj{})), c.DefaultTiers[k])
			}
		}
	}
	for i, c := range g.TruthBad {
		if _, err := ParseFiles(files(t, c.Files)); err == nil {
			t.Errorf("bad input %d: no error, Python raised %s", i, c.Exc)
		}
	}
	if string(cp437Table) != g.CP437 {
		t.Errorf("cp437 table:\n got %q\nwant %q", string(cp437Table), g.CP437)
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func TestApplyMatchesByStem(t *testing.T) {
	verdicts, err := ParseFiles([]File{{"t.csv", []byte("name,label,keywords\nIMG_1.CR2,Red,x\nIMG_9.jpg,Green,\n")}})
	if err != nil {
		t.Fatal(err)
	}
	var stored []Item
	c, err := Apply([]Row{{1, "/p/img_1.jpg"}, {2, "/p/IMG_2.jpg"}, {3, "/q/IMG_1.JPG"}}, verdicts, cfg,
		func(items []Item) error { stored = items; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if c != (Counts{Verdicts: 2, Matched: 2, Unmatched: 1}) || len(stored) != 2 || stored[1].ID != 3 {
		t.Fatalf("%+v %+v", c, stored)
	}
	if j := stored[0].Verdict.JSON(); j != `{"rating": null, "label": "Red", "focus_tier": 0, "keywords": ["x"]}` {
		t.Fatal(j)
	}
}
