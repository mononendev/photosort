package stats

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
)

// Bit-exact comparisons against numpy 2.2 outputs recorded in testdata/export_golden.json.

type goldenNumpy struct {
	Numpy struct {
		Linspace [][4]any `json:"linspace"`
		Arrays   []struct {
			X                []float64 `json:"x"`
			Mean             float64   `json:"mean"`
			Sum              float64   `json:"sum"`
			Quantiles        []float64 `json:"quantiles"`
			Percentiles      []float64 `json:"percentiles"`
			PercentilesNamed []float64 `json:"percentiles_named"`
			Unique           []float64 `json:"unique"`
		} `json:"arrays"`
	} `json:"numpy"`
}

func eqBits(t *testing.T, name string, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d values, want %d", name, len(got), len(want))
		return
	}
	for i := range got {
		if math.Float64bits(got[i]) != math.Float64bits(want[i]) {
			t.Errorf("%s[%d]: %v (%x), want %v (%x)", name, i, got[i], math.Float64bits(got[i]), want[i], math.Float64bits(want[i]))
		}
	}
}

func TestMatchesNumpy(t *testing.T) {
	b, err := os.ReadFile("../../testdata/export_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenNumpy
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Numpy.Arrays) == 0 || len(g.Numpy.Linspace) == 0 {
		t.Fatal("golden file is incomplete")
	}
	for _, c := range g.Numpy.Linspace {
		var want []float64
		for _, v := range c[3].([]any) {
			want = append(want, v.(float64))
		}
		eqBits(t, "linspace", Linspace(c[0].(float64), c[1].(float64), int(c[2].(float64))), want)
	}
	ps := make([]float64, 101)
	for i := range ps {
		ps[i] = float64(i)
	}
	for _, a := range g.Numpy.Arrays {
		name := func(s string) string { return fmt.Sprintf("%s/n=%d", s, len(a.X)) }
		eqBits(t, name("mean"), []float64{Mean(a.X)}, []float64{a.Mean})
		eqBits(t, name("sum"), []float64{Sum(a.X)}, []float64{a.Sum})
		eqBits(t, name("quantiles"), Quantiles(a.X, Linspace(0.02, 0.98, 97)), a.Quantiles)
		eqBits(t, name("percentiles"), Percentiles(a.X, ps), a.Percentiles)
		named := []float64{5, 10, 25, 50, 75, 90, 95, 99.5, 2}
		got := make([]float64, len(named))
		for i, p := range named {
			got[i] = Percentile(a.X, p)
		}
		eqBits(t, name("percentile"), got, a.PercentilesNamed)
		eqBits(t, name("unique"), Unique(a.X), a.Unique)
	}
}

func TestEdges(t *testing.T) {
	if !math.IsNaN(Quantile(nil, 0.5)) || !math.IsNaN(Quantile([]float64{1, math.NaN()}, 0.5)) {
		t.Error("NaN cases")
	}
	if Argmax([]float64{1, 3, 3, 2}) != 1 || Argmax(nil) != -1 || Argmax([]float64{1, math.NaN(), 5}) != 1 {
		t.Error("argmax")
	}
	if u := Unique([]float64{2, math.NaN(), 1, math.NaN(), 2}); len(u) != 3 || !math.IsNaN(u[2]) {
		t.Error(u)
	}
}
