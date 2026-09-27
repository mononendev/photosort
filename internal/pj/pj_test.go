package pj

import "testing"

func TestRoundMatchesPython(t *testing.T) {
	cases := []struct {
		x    float64
		n    int
		want float64
	}{
		{2.675, 2, 2.67}, // binary 2.67499999...: Python gives 2.67
		{0.125, 2, 0.12}, // exact tie: to even
		{0.375, 2, 0.38},
		{1.00005, 4, 1.0001}, // binary just over the tie
		{0.030049999, 4, 0.03},
		{-0.00001, 4, 0},
		{12345.6789, 0, 12346},
	}
	for _, c := range cases {
		if got := Round(c.x, c.n); got != c.want {
			t.Errorf("Round(%v, %d) = %v, want %v", c.x, c.n, got, c.want)
		}
	}
	if RoundInt(2.5) != 2 || RoundInt(3.5) != 4 || RoundInt(-2.5) != -2 {
		t.Error("RoundInt must round half to even")
	}
	if Sig(0.000123456, 4) != 0.0001235 || Sig(123456.0, 4) != 123500 {
		t.Error("Sig")
	}
}

func TestTruthy(t *testing.T) {
	for _, v := range []any{nil, false, 0.0, "", []any{}, Obj{}} {
		if Truthy(v) {
			t.Errorf("%#v should be falsy", v)
		}
	}
	for _, v := range []any{true, 1.0, "x", []any{1.0}, Obj{"a": 1.0}} {
		if !Truthy(v) {
			t.Errorf("%#v should be truthy", v)
		}
	}
}

func TestDumpsKeepsHTML(t *testing.T) {
	if Dumps(Obj{"a": "<b>&"}) != `{"a":"<b>&"}` {
		t.Error(Dumps(Obj{"a": "<b>&"}))
	}
}
