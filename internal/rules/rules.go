// Package rules turns measurements into decisions: who the primary subject is, the local focus tier and why, and
// whether the primary's metrics disagree. It is the only place those rules live; the analyzer only measures, and
// rescore, the trace view and a fresh local pass all come through here.
//
// People and thresholds are the stored JSON objects (see package pj), so rows written by any earlier version keep
// every key they have.
package rules

import (
	"fmt"
	"math"
	"sort"

	"github.com/mononendev/photosort/internal/pj"
)

// Eyewear reports an eye band that reads far sharper than the head around it: sunglasses or goggles, whose hard
// frame edges pass the contrast-normalized Laplacian even when a little soft. The head then has to clear its tier too.
func Eyewear(p, thr pj.Obj) bool {
	r := thr["eyewear_ratio"]
	eye, eok := pj.Float(p["sharp_eye"])
	head, hok := pj.Float(p["sharp_head"])
	return pj.Truthy(r) && eok && hok && eye > pj.F(r)*head
}

func has(o pj.Obj, k string) bool {
	_, ok := o[k]
	return ok
}

func on(thr pj.Obj, k string) bool {
	v, ok := thr[k]
	return !ok || pj.Truthy(v) // thr.get(k, True)
}

func cut(thr pj.Obj, pre string, lvl int) float64 {
	return pj.F(thr[fmt.Sprintf("%stier%d_min", pre, lvl)])
}

// Grade is 3/2/1/0 for one person with every threshold multiplied by k; ok is false when nothing is measurable.
//
// With an eye band, both the Laplacian and the FFT ratio on it must clear their thresholds (the FFT check is skipped
// when use_hf is off or the band had no FFT value), and so must the head box when the band looks like eyewear.
// Without one, the head box Laplacian is judged against the head thresholds.
func Grade(p, thr pj.Obj, k float64) (int, bool) {
	var ok func(lvl int) bool
	if eye, isNum := pj.Float(p["sharp_eye"]); on(thr, "use_eyes") && isNum && has(thr, "eye_tier3_min") {
		hf, hfNum := pj.Float(p["hf_eye"])
		useHF := on(thr, "use_hf") && hfNum && has(thr, "hf_tier3_min")
		head, headNum := 0.0, false
		if Eyewear(p, thr) {
			head, headNum = pj.Float(p["sharp_head"])
		}
		ok = func(lvl int) bool {
			return eye >= k*cut(thr, "eye_", lvl) && (!useHF || hf >= k*cut(thr, "hf_", lvl)) &&
				(!headNum || head >= k*cut(thr, "", lvl))
		}
	} else {
		s := pj.Or(p["sharp_head"], p["sharp_body"])
		sv, isNum := pj.Float(s)
		if s == nil || !isNum {
			return 0, false
		}
		ok = func(lvl int) bool { return sv >= k*cut(thr, "", lvl) }
	}
	for _, lvl := range []int{3, 2, 1} {
		if ok(lvl) {
			return lvl, true
		}
	}
	return 0, true
}

func box(v any) ([4]float64, bool) {
	a, ok := v.([]any)
	if !ok || len(a) < 4 {
		return [4]float64{}, false
	}
	return [4]float64{pj.F(a[0]), pj.F(a[1]), pj.F(a[2]), pj.F(a[3])}, true
}

// SoftInFront is the index into others of a person standing clearly nearer than, and beside, the primary whose head
// is soft, or -1.
//
// Nearer means at least front_min_height x the primary's box height with feet (box bottom) lower in the frame by
// front_min_drop x that height: on a ground plane both grow toward the camera. Beside means at most front_max_gap x
// that height apart sideways, where a slipped AF point lands. When the point slips off a rider onto a spectator
// behind them, the spectator is sharp and the rider in front is not: focus landed behind the subject.
//
// The head box Laplacian decides, not Grade: an eye band can grade low on a plainly sharp face. People cut by the
// frame edge (within front_edge of it) are passers-by in the foreground, not the subject, so they don't count.
func SoftInFront(primary pj.Obj, others []pj.Obj, thr pj.Obj, W, H float64) int {
	hmin, drop, gap := thr["front_min_height"], thr["front_min_drop"], thr["front_max_gap"]
	e := 0.01
	if v, ok := thr["front_edge"]; ok {
		e = pj.F(v)
	}
	b0, ok := box(primary["box"])
	if !on(thr, "use_front") || hmin == nil || drop == nil || gap == nil || !ok {
		return -1
	}
	h := b0[3] - b0[1]
	grade := 1.0
	if v, ok := thr["front_max_grade"]; ok {
		grade = pj.F(v)
	}
	limit := pj.F(thr[fmt.Sprintf("tier%d_min", int(grade)+1)])
	for i, o := range others {
		b, ok := box(o["box"])
		s, sok := pj.Float(o["sharp_head"])
		if !ok || !sok || b[3]-b[1] < pj.F(hmin)*h || b[3]-b0[3] < pj.F(drop)*h ||
			math.Max(b[0]-b0[2], b0[0]-b[2]) > pj.F(gap)*h {
			continue
		}
		if b[0] <= e*W || b[2] >= (1-e)*W || b[3] >= (1-e)*H {
			continue
		}
		if s < limit {
			return i
		}
	}
	return -1
}

// SharpOther reports someone besides the primary confidently detected (conf >= floor_conf) who grades floor_grade
// or better. Then focus plainly landed on a person in the frame, so the frame is worth a look even when the primary
// missed: LocalTier raises it to floor_tier.
func SharpOther(others []pj.Obj, thr pj.Obj) bool {
	if thr["floor_tier"] == nil {
		return false
	}
	c, g := 0.5, 3
	if v, ok := thr["floor_conf"]; ok {
		c = pj.F(v)
	}
	if v, ok := thr["floor_grade"]; ok {
		g = pj.Int(v)
	}
	for _, o := range others {
		if pj.F(pj.Or(o["conf"], 0.0)) >= c {
			if gr, _ := Grade(o, thr, 1); gr >= g {
				return true
			}
		}
	}
	return false
}

// MetricSplit reports the primary's metrics disagreeing: one grades split_steps or more tiers from the nearest of
// the others. Each metric (eye band Laplacian, eye band FFT ratio, head box Laplacian) is graded on its own
// thresholds; when one stands well apart, a region landed wrong and the tier can't be trusted either way. nil when
// they agree, split_steps is off, or fewer than two metrics were measured.
func MetricSplit(p, thr pj.Obj) pj.Obj {
	steps := thr["split_steps"]
	if !pj.Truthy(steps) || p == nil {
		return nil
	}
	eyes := on(thr, "use_eyes")
	hf := eyes && on(thr, "use_hf")
	type metric struct {
		name, key, pre string
		on             bool
	}
	grades := pj.Obj{}
	var names []string
	for _, m := range []metric{{"eye", "sharp_eye", "eye_", eyes}, {"fft", "hf_eye", "hf_", hf}, {"head", "sharp_head", "", true}} {
		v, isNum := pj.Float(p[m.key])
		if !m.on || !isNum || !has(thr, m.pre+"tier3_min") {
			continue
		}
		g := 0
		for _, lvl := range []int{3, 2, 1} {
			if v >= cut(thr, m.pre, lvl) {
				g = lvl
				break
			}
		}
		grades[m.name] = float64(g)
		names = append(names, m.name)
	}
	if len(names) < 2 {
		return nil
	}
	gaps := map[string]float64{}
	for _, k := range names {
		best := math.Inf(1)
		for _, o := range names {
			if o != k {
				best = math.Min(best, math.Abs(pj.F(grades[k])-pj.F(grades[o])))
			}
		}
		gaps[k] = best
	}
	odd := names[0] // max() keeps the first of equals, in insertion order
	for _, k := range names[1:] {
		if gaps[k] > gaps[odd] {
			odd = k
		}
	}
	if gaps[odd] < pj.F(steps) {
		return nil
	}
	return pj.Obj{"grades": grades, "odd": odd, "gap": gaps[odd]}
}

// Margins are LocalTier's shake and noise margins from the config.
type Margins struct{ Shake, Noise float64 }

// MarginsFrom reads them from the config (exif.shake_margin, noise.margin; 1.5 each by default).
func MarginsFrom(cfg pj.Obj) Margins {
	m := Margins{1.5, 1.5}
	if v, ok := pj.O(cfg, "exif")["shake_margin"]; ok {
		m.Shake = pj.F(v)
	}
	if v, ok := pj.O(cfg, "noise")["margin"]; ok {
		m.Noise = pj.F(v)
	}
	return m
}

// Size is a frame's width and height, when known.
type Size struct{ W, H float64 }

// LocalTier is the tier from measured sharpness (3 sharp, 2 slightly soft, 1 soft, 0 miss) and the reason. The EXIF
// prior only demotes a *borderline* tier 3 shot at a slow shutter (a clearly sharp subject wins, e.g. a well-panned
// rider), and the noise prior one at high noise, where grain or noise reduction make a near pass unreliable. A tier 3
// whose surroundings (or, when plane_body_max_extra is set, torso) are clearly sharper than the head is only slightly
// soft: focus landed just off the face. So is one with a soft person standing clearly in front of it. Below
// floor_tier, someone else graded sharp raises the frame to floor_tier.
func LocalTier(primary pj.Obj, others []pj.Obj, thr, prior pj.Obj, size *Size, noise pj.Obj, m Margins) (int, string) {
	if primary == nil {
		return 0, "no_people"
	}
	g, measurable := Grade(primary, thr, 1)
	floor := pj.F(pj.Or(thr["floor_tier"], 0.0))
	if (!measurable || float64(g) < floor) && SharpOther(others, thr) {
		return pj.Int(thr["floor_tier"]), "secondary_person_sharp"
	}
	if !measurable {
		return 0, "subject_too_small"
	}
	_, eyeNum := pj.Float(primary["sharp_eye"])
	onEyes := on(thr, "use_eyes") && eyeNum && has(thr, "eye_tier3_min")
	switch g {
	case 3:
		if prior != nil && prior["motion_risk"] == "high" {
			if g2, _ := Grade(primary, thr, m.Shake); g2 < 3 {
				return 2, "borderline_sharp_slow_shutter"
			}
		}
		if noise != nil && noise["risk"] == "high" {
			if g2, _ := Grade(primary, thr, m.Noise); g2 < 3 {
				return 2, "borderline_sharp_noisy"
			}
		}
		pl := pj.O(primary, "plane")
		if on(thr, "use_plane") {
			c, bodyCut := thr["plane_max_extra"], thr["plane_body_max_extra"]
			if pj.Truthy(c) && pj.F(pj.Or(pl["head_vs_near"], 0.0)) >= pj.F(c) {
				return 2, "sharper_around_subject"
			}
			if pj.Truthy(bodyCut) && pj.F(pj.Or(pl["head_vs_torso"], 0.0)) >= pj.F(bodyCut) {
				return 2, "sharper_body_than_head"
			}
		}
		if size != nil && SoftInFront(primary, others, thr, size.W, size.H) >= 0 {
			return 2, "soft_person_in_front"
		}
		if onEyes {
			return 3, "primary_eyes_sharp"
		}
		return 3, "primary_head_sharp"
	case 2:
		if onEyes {
			return 2, "primary_eyes_slightly_soft"
		}
		return 2, "primary_slightly_soft"
	case 1:
		if onEyes {
			return 1, "primary_eyes_soft"
		}
		return 1, "primary_soft"
	}
	// With the floor off, the tiers grade the primary subject only, so a sharp bystander is still a miss; the reason
	// keeps it findable.
	for _, o := range others {
		if gr, ok := Grade(o, thr, 1); ok && gr == 3 {
			return 0, "secondary_person_sharp"
		}
	}
	return 0, "nothing_sharp"
}

// Scorer scores how well a camera's AF points land on each person, nearer people occluding farther ones (see
// package af).
type Scorer func(af pj.Obj, people []pj.Obj, near, occlude float64) []float64

// PickPrimary orders people in place, primary first, and says what chose the primary ("af" or "priority").
//
// People are ranked by prominence (size, centering, confidence). When the camera's active AF points land on someone,
// that person is who the photographer meant, so they lead even if smaller or softer than a bystander. A point just
// beside someone counts too, for less (af.near). A point inside a much bigger person's box goes to them, not to a
// small figure behind (af.occlude).
func PickPrimary(people []pj.Obj, af, cfg pj.Obj, score Scorer) string {
	acfg := pj.O(cfg, "af")
	near := pj.F(pj.Or(acfg["near"], 0.0))
	var scores []float64
	if pj.Truthy(af) {
		scores = score(af, people, near, pj.F(pj.Or(acfg["occlude"], 0.0)))
	}
	for i, p := range people {
		if scores != nil {
			p["af_score"] = scores[i]
		} else {
			p["af_score"] = nil
		}
	}
	// Rows analyzed before priority existed don't store it; the sort is stable, so they keep their stored order.
	sort.SliceStable(people, func(i, j int) bool {
		return -pj.F(pj.Or(people[i]["priority"], 0.0)) < -pj.F(pj.Or(people[j]["priority"], 0.0))
	})
	if len(people) == 0 || !pj.Truthy(af) || !on(acfg, "use") {
		return "priority"
	}
	best := 0 // max() keeps the first of equals
	for i := 1; i < len(people); i++ {
		a, b := people[i], people[best]
		sa, sb := pj.F(pj.Or(a["af_score"], 0.0)), pj.F(pj.Or(b["af_score"], 0.0))
		if sa > sb || (sa == sb && pj.F(pj.Or(a["priority"], 0.0)) > pj.F(pj.Or(b["priority"], 0.0))) {
			best = i
		}
	}
	minScore := 0.5
	if v, ok := acfg["min_score"]; ok {
		minScore = pj.F(v)
	}
	if pj.F(pj.Or(people[best]["af_score"], 0.0)) < minScore {
		return "priority"
	}
	p := people[best]
	copy(people[1:best+1], people[:best])
	people[0] = p
	return "af"
}

// PrimaryFields are the primary's headline numbers stored at the top of local_json.
func PrimaryFields(primary pj.Obj) pj.Obj {
	if primary == nil {
		return pj.Obj{"primary_head_sharp": nil, "primary_body_sharp": nil, "primary_eye_sharp": nil,
			"primary_eye_hf": nil, "primary_eye_src": nil}
	}
	return pj.Obj{
		"primary_head_sharp": pj.RoundPtr(primary["sharp_head"], 4),
		"primary_body_sharp": pj.RoundPtr(primary["sharp_body"], 4),
		"primary_eye_sharp":  pj.RoundPtr(primary["sharp_eye"], 4),
		"primary_eye_hf":     pj.RoundPtr(primary["hf_eye"], 4),
		"primary_eye_src":    primary["eye_src"],
	}
}
