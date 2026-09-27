package rules

import (
	"maps"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
)

var THR = pj.Obj{"tier3_min": 0.03, "tier2_min": 0.017, "tier1_min": 0.01,
	"eye_tier3_min": 0.06, "eye_tier2_min": 0.035, "eye_tier1_min": 0.02,
	"hf_tier3_min": 0.03, "hf_tier2_min": 0.017, "hf_tier1_min": 0.01}

// with is Python's {**a, **b}.
func with(a pj.Obj, kv ...any) pj.Obj {
	out := maps.Clone(a)
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func bx(a ...float64) []any {
	out := make([]any, len(a))
	for i, v := range a {
		out[i] = v
	}
	return out
}

type tierCase struct {
	name    string
	primary pj.Obj
	others  []pj.Obj
	thr     pj.Obj
	prior   pj.Obj
	size    *Size
	noise   pj.Obj
	m       *Margins
	tier    int
	reason  string // "" = don't check
}

func runTiers(t *testing.T, cases []tierCase) {
	t.Helper()
	for _, c := range cases {
		m := Margins{1.5, 1.5}
		if c.m != nil {
			m = *c.m
		}
		tier, reason := LocalTier(c.primary, c.others, c.thr, c.prior, c.size, c.noise, m)
		if tier != c.tier || (c.reason != "" && reason != c.reason) {
			t.Errorf("%s: got (%d, %s), want (%d, %s)", c.name, tier, reason, c.tier, c.reason)
		}
	}
}

func TestLocalTierRules(t *testing.T) {
	thr := pj.Obj{"tier3_min": 0.03, "tier2_min": 0.017, "tier1_min": 0.01}
	runTiers(t, []tierCase{
		{name: "nobody", thr: thr, tier: 0, reason: "no_people"},
		{name: "sharp", primary: pj.Obj{"sharp_head": 0.05}, thr: thr, tier: 3},
		{name: "slightly soft", primary: pj.Obj{"sharp_head": 0.02}, thr: thr, tier: 2, reason: "primary_slightly_soft"},
		{name: "soft", primary: pj.Obj{"sharp_head": 0.012}, thr: thr, tier: 1, reason: "primary_soft"},
		{name: "sharp bystander", primary: pj.Obj{"sharp_head": 0.002}, others: []pj.Obj{{"sharp_head": 0.06}}, thr: thr, tier: 0, reason: "secondary_person_sharp"},
		{name: "soft bystander", primary: pj.Obj{"sharp_head": 0.002}, others: []pj.Obj{{"sharp_head": 0.02}}, thr: thr, tier: 0},
		{name: "miss", primary: pj.Obj{"sharp_head": 0.002}, thr: thr, tier: 0},
	})
}

func TestLocalTierFloorForASharpBystander(t *testing.T) {
	thr := pj.Obj{"tier3_min": 0.03, "tier2_min": 0.017, "tier1_min": 0.01, "floor_tier": 2.0, "floor_grade": 3.0, "floor_conf": 0.5}
	sharp := pj.Obj{"sharp_head": 0.06, "conf": 0.9}
	runTiers(t, []tierCase{
		{name: "raised", primary: pj.Obj{"sharp_head": 0.002}, others: []pj.Obj{sharp}, thr: thr, tier: 2, reason: "secondary_person_sharp"},
		{name: "raised from 1", primary: pj.Obj{"sharp_head": 0.012}, others: []pj.Obj{sharp}, thr: thr, tier: 2, reason: "secondary_person_sharp"},
		{name: "primary unmeasurable", primary: pj.Obj{}, others: []pj.Obj{sharp}, thr: thr, tier: 2, reason: "secondary_person_sharp"},
		{name: "unsure detection", primary: pj.Obj{"sharp_head": 0.012}, others: []pj.Obj{with(sharp, "conf", 0.3)}, thr: thr, tier: 1},
		{name: "only slightly soft", primary: pj.Obj{"sharp_head": 0.012}, others: []pj.Obj{with(sharp, "sharp_head", 0.02)}, thr: thr, tier: 1},
		{name: "already there", primary: pj.Obj{"sharp_head": 0.02}, others: []pj.Obj{sharp}, thr: thr, tier: 2, reason: "primary_slightly_soft"},
		{name: "floor off", primary: pj.Obj{"sharp_head": 0.002}, others: []pj.Obj{sharp}, thr: with(thr, "floor_tier", nil), tier: 0, reason: "secondary_person_sharp"},
	})
}

func TestMetricSplitFlagsOneMetricFarFromTheOthers(t *testing.T) {
	thr := with(THR, "split_steps", 2.0)
	p := pj.Obj{"sharp_eye": 0.026, "hf_eye": 0.003, "sharp_head": 0.073}
	want := pj.Obj{"grades": pj.Obj{"eye": 1, "fft": 0, "head": 3}, "odd": "head", "gap": 2}
	if got := MetricSplit(p, thr); !pj.Equal(got, want) {
		t.Errorf("split: %v", got)
	}
	for _, c := range []struct {
		p   pj.Obj
		thr pj.Obj
	}{
		{p, with(thr, "split_steps", 3.0)},
		{p, with(thr, "split_steps", nil)},
		{pj.Obj{"sharp_eye": 0.07, "hf_eye": 0.02, "sharp_head": 0.02}, thr},
		{pj.Obj{"sharp_head": 0.073}, thr},
		{p, with(thr, "use_eyes", false)},
	} {
		if got := MetricSplit(c.p, c.thr); got != nil {
			t.Errorf("expected no split for %v: %v", c.p, got)
		}
	}
	if got := MetricSplit(p, with(thr, "use_hf", false)); !pj.Equal(got["grades"], pj.Obj{"eye": 1, "head": 3}) {
		t.Errorf("use_hf off: %v", got)
	}
}

func TestLocalTierEyeBandNeedsBothMetrics(t *testing.T) {
	sharp := pj.Obj{"sharp_eye": 0.08, "hf_eye": 0.05, "sharp_head": 0.001}
	runTiers(t, []tierCase{
		{name: "eyes beat a soft head box", primary: sharp, thr: THR, tier: 3, reason: "primary_eyes_sharp"},
		{name: "FFT vetoes", primary: with(sharp, "hf_eye", 0.02), thr: THR, tier: 2, reason: "primary_eyes_slightly_soft"},
		{name: "FFT soft", primary: with(sharp, "hf_eye", 0.012), thr: THR, tier: 1, reason: "primary_eyes_soft"},
		{name: "eyes missed", primary: with(sharp, "sharp_eye", 0.01), thr: THR, tier: 0, reason: "nothing_sharp"},
		{name: "use_hf off", primary: with(sharp, "hf_eye", 0.02), thr: with(THR, "use_hf", false), tier: 3},
		{name: "band too small for FFT", primary: with(sharp, "hf_eye", nil), thr: THR, tier: 3},
		{name: "no eyes: head box", primary: pj.Obj{"sharp_eye": nil, "sharp_head": 0.05}, thr: THR, tier: 3, reason: "primary_head_sharp"},
		{name: "use_eyes off", primary: sharp, thr: with(THR, "use_eyes", false), tier: 0},
		{name: "sharp secondary face", primary: pj.Obj{"sharp_eye": 0.001, "hf_eye": 0.001}, others: []pj.Obj{sharp}, thr: THR, tier: 0, reason: "secondary_person_sharp"},
	})
}

func TestLocalTierPriorsDemoteBorderlineOnly(t *testing.T) {
	slow, noisy := pj.Obj{"motion_risk": "high"}, pj.Obj{"risk": "high"}
	border, clear := pj.Obj{"sharp_eye": 0.07, "hf_eye": 0.05}, pj.Obj{"sharp_eye": 0.2, "hf_eye": 0.05}
	runTiers(t, []tierCase{
		{name: "slow", primary: border, thr: THR, prior: slow, tier: 2, reason: "borderline_sharp_slow_shutter"},
		{name: "slow but clear", primary: clear, thr: THR, prior: slow, tier: 3},
		{name: "noisy", primary: border, thr: THR, noise: noisy, tier: 2, reason: "borderline_sharp_noisy"},
		{name: "noisy but clear", primary: clear, thr: THR, noise: noisy, tier: 3},
		{name: "medium noise", primary: border, thr: THR, noise: pj.Obj{"risk": "medium"}, tier: 3},
		{name: "noise margin off", primary: border, thr: THR, noise: noisy, m: &Margins{1.5, 1.0}, tier: 3},
	})
}

func TestEyewearBandNeedsTheHeadToo(t *testing.T) {
	thr := with(THR, "eyewear_ratio", 3.0)
	shades := pj.Obj{"sharp_eye": 0.5, "hf_eye": 0.07, "sharp_head": 0.02}
	if !Eyewear(shades, thr) || Eyewear(shades, THR) {
		t.Error("eyewear")
	}
	runTiers(t, []tierCase{
		{name: "head clears only 2", primary: shades, thr: thr, tier: 2, reason: "primary_eyes_slightly_soft"},
		{name: "head sharp", primary: with(shades, "sharp_head", 0.05), thr: thr, tier: 3},
		{name: "ratio off", primary: pj.Obj{"sharp_eye": 0.08, "hf_eye": 0.05, "sharp_head": 0.001}, thr: with(thr, "eyewear_ratio", nil), tier: 3},
	})
}

func TestLocalTierFocusPlaneCapsSharpAtTwo(t *testing.T) {
	thr := with(THR, "plane_max_extra", 0.4)
	sharp := pj.Obj{"sharp_eye": 0.08, "hf_eye": 0.05}
	behind := with(sharp, "plane", pj.Obj{"head": 1.29, "torso": 1.02, "near": 0.92, "head_vs_near": 0.91, "head_vs_torso": 0.79})
	body := with(sharp, "plane", pj.Obj{"head_vs_near": nil, "head_vs_torso": 0.7})
	ok := with(sharp, "plane", pj.Obj{"head": 1.02, "torso": 1.09, "near": nil, "head_vs_near": nil, "head_vs_torso": 0.0})
	runTiers(t, []tierCase{
		{name: "behind", primary: behind, thr: thr, tier: 2, reason: "sharper_around_subject"},
		{name: "torso check off by default", primary: body, thr: thr, tier: 3},
		{name: "torso check on", primary: body, thr: with(thr, "plane_body_max_extra", 0.5), tier: 2, reason: "sharper_body_than_head"},
		{name: "in plane", primary: ok, thr: thr, tier: 3, reason: "primary_eyes_sharp"},
		{name: "plane off", primary: behind, thr: with(thr, "use_plane", false), tier: 3},
		{name: "only caps a 3", primary: with(behind, "sharp_eye", 0.04), thr: thr, tier: 2, reason: "primary_eyes_slightly_soft"},
	})
}

func TestLocalTierSoftPersonInFrontCapsSharpAtTwo(t *testing.T) {
	// AA4I6532: the AF point slipped past the rider onto a spectator behind, who is sharp; the rider is not.
	thr := with(THR, "use_front", true, "front_min_height", 1.0, "front_min_drop", 0.25, "front_max_gap", 0.5,
		"front_max_grade", 1.0, "front_edge", 0.01)
	size := &Size{3648, 5472}
	spectator := pj.Obj{"sharp_head": 0.28, "box": bx(1564, 2397, 2074, 3187)}
	rider := pj.Obj{"sharp_head": 0.0078, "box": bx(1189, 2566, 1666, 3684)}
	runTiers(t, []tierCase{
		{name: "slipped", primary: spectator, others: []pj.Obj{rider}, thr: thr, size: size, tier: 2, reason: "soft_person_in_front"},
		{name: "no frame size", primary: spectator, others: []pj.Obj{rider}, thr: thr, tier: 3},
		{name: "off", primary: spectator, others: []pj.Obj{rider}, thr: with(thr, "use_front", false), size: size, tier: 3},
		{name: "sharp in front", primary: spectator, others: []pj.Obj{with(rider, "sharp_head", 0.05)}, thr: thr, size: size, tier: 3},
		{name: "level", primary: spectator, others: []pj.Obj{with(rider, "box", bx(1189, 2566, 1666, 3200))}, thr: thr, size: size, tier: 3},
		{name: "shorter", primary: spectator, others: []pj.Obj{with(rider, "box", bx(1350, 2900, 1666, 3684))}, thr: thr, size: size, tier: 3},
		{name: "far off", primary: spectator, others: []pj.Obj{with(rider, "box", bx(100, 2566, 600, 3684))}, thr: thr, size: size, tier: 3},
		{name: "edge-cut", primary: spectator, others: []pj.Obj{with(rider, "box", bx(0, 2566, 1500, 3684))}, thr: thr, size: size, tier: 3},
	})
}

func TestPickPrimaryPrefersAFThenPriority(t *testing.T) {
	score := func(af, p pj.Obj, near float64) float64 { return pj.F(p["hit"]) }
	people := func() []pj.Obj {
		return []pj.Obj{{"id": "a", "priority": 0.1}, {"id": "b", "priority": 0.3, "hit": 0.4}, {"id": "c", "priority": 0.2, "hit": 1.0}}
	}
	cfg := pj.Obj{"af": pj.Obj{"use": true, "min_score": 0.5}}
	ps := people()
	if by := PickPrimary(ps, pj.Obj{"points": 1.0}, cfg, score); by != "af" || ps[0]["id"] != "c" || ps[1]["id"] != "b" || ps[2]["id"] != "a" {
		t.Errorf("af pick: %s %v", by, ps)
	}
	ps = people()
	if by := PickPrimary(ps, nil, cfg, score); by != "priority" || ps[0]["id"] != "b" || ps[0]["af_score"] != nil {
		t.Errorf("no af: %s %v", by, ps)
	}
	ps = people()
	if by := PickPrimary(ps, pj.Obj{"points": 1.0}, pj.Obj{"af": pj.Obj{"min_score": 1.5}}, score); by != "priority" || ps[0]["id"] != "b" {
		t.Errorf("below min_score: %s %v", by, ps)
	}
	legacy := []pj.Obj{{"id": "x"}, {"id": "y"}} // rows from before priority keep their order
	PickPrimary(legacy, nil, cfg, score)
	if legacy[0]["id"] != "x" {
		t.Error("stable")
	}
}
