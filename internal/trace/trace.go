// Package trace explains why a photo ended up where it did: every rule the pipeline applies to one image, in order,
// with the values it read, whether it fired and what it did. It feeds the Trace page.
//
// The walk reads the stored results against the current config and calls the pipeline's own functions
// (rules.Grade, SoftInFront, SharpOther, MetricSplit, af.PersonScore, export.FinalRecord), so a rule can't read
// differently here than it does there. The branching of rules.LocalTier is spelled out step by step; LocalTier
// itself runs too, and the two answers are returned side by side ("check") so the page can say if they ever part.
//
// The texts (summaries, rules, notes, effects) are the Python version's byte for byte, Python number formatting
// included: stored JSON is decoded with py.Loads so an int prints as "3" and a float as "3.0", as it did there.
package trace

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mononendev/photosort/internal/af"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/exif"
	"github.com/mononendev/photosort/internal/export"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
	"github.com/mononendev/photosort/internal/rules"
	"github.com/mononendev/photosort/internal/scan"
)

// ---- values and formatting ----------------------------------------------------------------------------------

// decode parses a JSON column as jcol did (nil for NULL/empty text) into pj values that keep Python's int/float
// distinction: objects become pj.Obj, integer literals int64, other numbers float64.
func decode(name string, s *string) (any, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	v, err := py.Loads(*s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return py.Plain(v), nil
}

// decodeObj is decode for a column that must hold an object (or null).
func decodeObj(name string, s *string) (pj.Obj, error) {
	v, err := decode(name, s)
	if err != nil || v == nil {
		return nil, err
	}
	o, ok := v.(pj.Obj)
	if !ok {
		return nil, fmt.Errorf("%s is not a JSON object", name)
	}
	return o, nil
}

// deepCopy is copy.deepcopy of a decoded value (pj.Clone would turn int64 into float64).
func deepCopy(v any) any {
	switch x := v.(type) {
	case pj.Obj:
		o := make(pj.Obj, len(x))
		for k, e := range x {
			o[k] = deepCopy(e)
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopy(e)
		}
		return out
	}
	return v
}

// floatKeys are the config keys whose Python default is a float with an integral value (3.0 prints "3.0"). The
// config reaches Go as float64 throughout, so every other integral value is taken to be the int Python had.
var floatKeys = map[string]bool{
	"exposure.raw_max_ev": true, "focus.eyewear_ratio": true, "focus.front_min_height": true,
	"exif.crop_factor": true, "exif.wide_open_f": true, "af.near": true,
}

// cv is a config value as Python held it: an integral float64 becomes int64 unless the key's default is a float.
func cv(path string, v any) any {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 && !floatKeys[path] {
		return int64(f)
	}
	return v
}

// cget is sect.get(key) on a config section; path prefixes the key for floatKeys ("focus." or "").
func cget(o pj.Obj, path, key string) any { return cv(path+key, o[key]) }

// cgetd is sect.get(key, def).
func cgetd(o pj.Obj, path, key string, def any) any {
	if v, ok := o[key]; ok {
		return cv(path+key, v)
	}
	return def
}

// getd is d.get(key, def) on stored data (no config typing).
func getd(o pj.Obj, key string, def any) any {
	if v, ok := o[key]; ok {
		return v
	}
	return def
}

// s is f"{v}".
func s(v any) string { return py.Str(v) }

// fmtG is f"{x:g}".
func fmtG(v any) string {
	x := pj.F(v)
	switch {
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	case math.IsNaN(x):
		return "nan"
	}
	return strconv.FormatFloat(x, 'g', 6, 64)
}

// fmtPlus is f"{x:+}" for a float.
func fmtPlus(x float64) string {
	if math.Signbit(x) || math.IsNaN(x) {
		return py.FloatRepr(x)
	}
	return "+" + py.FloatRepr(x)
}

// rnd is _r: None for None, else round(float(v), n).
func rnd(v any, n int) any {
	f, ok := pj.Float(v)
	if v == nil || !ok {
		return nil
	}
	return pj.Round(f, n)
}

// or is Python's `a or b`.
func or(a, b any) any { return pj.Or(a, b) }

// eq is Python's ==.
func eq(a, b any) bool { return py.Eq(a, b) }

// ge is a >= b on numbers.
func ge(a, b any) bool { return pj.F(a) >= pj.F(b) }

// grade is local._grade: 3/2/1/0, or nil when nothing is measurable.
func grade(p, thr pj.Obj, k float64) any {
	g, ok := rules.Grade(p, thr, k)
	if !ok {
		return nil
	}
	return g
}

// ---- nodes, chains, stages ----------------------------------------------------------------------------------

// opts are _node's keyword arguments, plus the chain's (decides, value, gate).
type opts struct {
	rule, effect, note any // string or nil
	inputs             []any
	people             any // nil, or a list (an empty list stays [])
	decides            bool
	value              []any
	gate               any // string or nil
}

// node is one rule the pipeline evaluates. result: true it holds (fires), false it doesn't, nil it's off or has
// nothing to read. Every rule is evaluated, even where it can't matter: reached is false when an earlier rule
// already decided or its branch wasn't taken, and effect is then what it would have done. decided marks the one
// rule that settled the stage.
func node(q string, result any, o opts) *py.Object {
	inputs := o.inputs
	if inputs == nil {
		inputs = []any{}
	}
	return py.NewObject("q", q, "result", result, "rule", o.rule, "inputs", inputs, "effect", o.effect,
		"note", o.note, "reached", true, "people", o.people, "decided", false)
}

// kv is one value a rule read (or a fact), with an optional note.
func kv(k string, v any, note ...any) *py.Object {
	o := py.NewObject("k", k, "v", v)
	if len(note) > 0 && pj.Truthy(note[0]) {
		o.Set("note", note[0])
	}
	return o
}

// chain is rules asked in order until one decides. Later rules are still evaluated and listed, marked not reached.
type chain struct {
	nodes   []any
	decided bool
	value   []any
}

// ask adds a rule. o.gate says why this rule's branch wasn't taken (it's evaluated anyway); o.value is what
// deciding sets.
func (c *chain) ask(q string, result any, o opts) *py.Object {
	reached := !c.decided && o.gate == nil
	n := node(q, result, o)
	n.Set("reached", reached)
	if !reached {
		why := "not reached: decided above"
		if !c.decided {
			why = s(o.gate)
		}
		if note := n.Get("note"); pj.Truthy(note) {
			why += " · " + s(note)
		}
		n.Set("note", why)
	} else if result == true && o.decides {
		c.decided, c.value = true, o.value
		n.Set("decided", true)
	}
	c.nodes = append(c.nodes, n)
	return n
}

// stageOpts are _stage's keyword arguments. table and outcome are nil or a value.
type stageOpts struct {
	facts, nodes   []any
	table, outcome any
}

// stage is one step of the pipeline. state: done (ran and decided), skipped (a rule left it out), pending (hasn't
// run), error, off.
func stage(key, title, state, summary string, o stageOpts) *py.Object {
	if o.facts == nil {
		o.facts = []any{}
	}
	if o.nodes == nil {
		o.nodes = []any{}
	}
	return py.NewObject("key", key, "title", title, "state", state, "summary", summary, "facts", o.facts,
		"nodes", o.nodes, "table", o.table, "outcome", o.outcome)
}

func label(l string) *py.Object { return py.NewObject("label", l) }

// pid is a person's number as the detail view shows it (1-based, in stored order), 0 when not found.
func pid(people []pj.Obj, p pj.Obj) int {
	for i, q := range people {
		if eq(q["box"], p["box"]) {
			return i + 1
		}
	}
	return 0
}

func objs(v any) []pj.Obj {
	a, _ := v.([]any)
	out := make([]pj.Obj, 0, len(a))
	for _, e := range a {
		o, _ := e.(pj.Obj)
		out = append(out, o)
	}
	return out
}

func ternary[T any](c bool, a, b T) T {
	if c {
		return a
	}
	return b
}

// ---- stages -------------------------------------------------------------------------------------------------

func scanStage(row db.Image, local pj.Obj, rel string) (*py.Object, error) {
	ext := py.Lower(py.Suffix(row.Path))
	lr, err := decodeObj("lr_json", row.LRJSON)
	if err != nil {
		return nil, err
	}
	var size, typNote any
	if row.Size != nil && *row.Size != 0 {
		size = fmt.Sprintf("%.1f MB", float64(*row.Size)/1e6)
	}
	typ := strings.ToUpper(strings.TrimLeft(ext, "."))
	if scan.RawExt[ext] {
		typ, typNote = "RAW", "a RAW is only scanned when no JPEG/HEIC shares its name (skip_raw_dupes)"
	}
	facts := []any{kv("file", rel), kv("size", size), kv("type", typ, typNote)}
	if lr["rating"] != nil || pj.Truthy(lr["label"]) {
		rating := any("–")
		if v, ok := lr["rating"]; ok {
			rating = v
		}
		facts = append(facts, kv("sidecar", py.Strip(s(rating)+"★ "+s(or(lr["label"], ""))),
			"read from an existing .xmp next to the file; informational only"))
	}
	state, summary := "done", "registered"
	if row.Error != nil && *row.Error != "" && !pj.Truthy(local) {
		state, summary = "error", "registered · "+*row.Error
	}
	return stage("scan", "Scan", state, summary, stageOpts{facts: facts}), nil
}

func exposureStage(local pj.Obj, cfg pj.Obj) *py.Object {
	ex := pj.O(cfg, "exposure")
	if local == nil {
		return stage("exposure", "Exposure lift", "pending", "runs with the local stage", stageOpts{})
	}
	const p = "exposure."
	e := pj.O(local, "exposure")
	var ch chain
	ch.ask("Lift dark frames?", pj.Truthy(ex["recover"]), opts{rule: "exposure.recover = " + s(cget(ex, p, "recover")),
		note: ternary[any](pj.Truthy(ex["recover"]), nil, "off: frames are measured and sent as shot")})
	if pj.Truthy(e) {
		kind := s(getd(e, "source", "jpeg"))
		dark, capEV := cget(ex, p, kind+"_dark_key"), cget(ex, p, kind+"_max_ev")
		var want, hl any
		if pj.Truthy(e["key"]) && pj.Truthy(ex["target_key"]) {
			want = math.Log2(pj.F(ex["target_key"]) / pj.F(e["key"]))
		}
		if pj.Truthy(ex["highlight_cap"]) {
			p99 := math.Max(pj.F(or(e["p99"], 1e-6)), 1e-6)
			hl = math.Log2(pj.F(ex["highlight_cap"]) / p99)
		}
		ch.ask(fmt.Sprintf("Scene key under the %s dark cut?", strings.ToUpper(kind)), true,
			opts{rule: fmt.Sprintf("exposure.%s_dark_key = %s", kind, s(dark)),
				inputs: []any{kv("key", e["key"], "log-average luminance, linear light")}})
		ev := e["ev"]
		from := "JPEG"
		if kind == "raw" {
			from = "embedded camera JPEG"
		}
		ch.ask("Lift at least min_ev?", true, opts{decides: true, rule: "exposure.min_ev = " + s(cget(ex, p, "min_ev")),
			inputs: []any{
				kv("to target", rnd(want, 2), fmt.Sprintf("log2(target_key %s / key)", s(cget(ex, p, "target_key")))),
				kv("cap", capEV, fmt.Sprintf("exposure.%s_max_ev", kind)),
				kv("highlight room", rnd(hl, 2), fmt.Sprintf("log2(highlight_cap %s / p99 %s)",
					s(cget(ex, p, "highlight_cap")), s(e["p99"]))),
				kv("lift", ev, "the smallest of the three"),
			},
			effect: fmt.Sprintf("+%s EV, from the %s", s(ev), from)})
		return stage("exposure", "Exposure lift", "done", "+"+s(ev)+" EV",
			stageOpts{nodes: ch.nodes, outcome: label("lifted +" + s(ev) + " EV")})
	}
	ch.ask("Dark enough to lift, by at least min_ev?", false, opts{
		rule: fmt.Sprintf("raw_dark_key %s · jpeg_dark_key %s · min_ev %s", s(cget(ex, p, "raw_dark_key")),
			s(cget(ex, p, "jpeg_dark_key")), s(cget(ex, p, "min_ev"))),
		note: "no lift recorded at the last local pass (the key of an unlifted frame isn't stored)"})
	return stage("exposure", "Exposure lift", "skipped", "not lifted", stageOpts{nodes: ch.nodes, outcome: label("as shot")})
}

// noiseNow is exif.NoisePrior on the stored measurement under the current config, as a rescore computes it,
// with the displayed inputs kept as stored (an int ISO or lift prints as one).
type noiseNow struct {
	prior                        pj.Obj // NoisePrior's result, for rules.LocalTier
	iso, ev, effISO, sigma, risk any
	by                           any
}

func computeNoise(local, cfg pj.Obj) noiseNow {
	ex := pj.O(local, "exif")
	evRaw := pj.Get(local, "exposure", "ev")
	sigRaw := pj.Get(local, "noise", "sigma")
	var sigma *float64
	if f, ok := pj.Float(sigRaw); ok && sigRaw != nil {
		sigma = &f
	}
	pr := exif.NoisePrior(ex, pj.F(evRaw), sigma, pj.O(cfg, "noise"))
	n := noiseNow{prior: pr, iso: ex["iso"], effISO: pr["eff_iso"], sigma: sigRaw, risk: pr["risk"], by: pr["by"]}
	if pj.Truthy(evRaw) {
		n.ev = evRaw
	}
	return n
}

// noiseStage is exif.noise_prior: measured noise decides, effective ISO stands in for rows analyzed before it was
// measured. High risk makes a tier 3 clear the cuts by noise.margin (the noisy rule in the local tier).
func noiseStage(local, cfg pj.Obj) *py.Object {
	if local == nil {
		return stage("noise", "Noise", "pending", "measured with the local stage", stageOpts{})
	}
	nc := pj.O(cfg, "noise")
	const p = "noise."
	nz := computeNoise(local, cfg)
	if nz.by == nil {
		return stage("noise", "Noise", "skipped", "no measurement and no ISO", stageOpts{outcome: label("unknown")})
	}
	measured := nz.by == "measured"
	var lift any
	if pj.Truthy(nz.ev) {
		lift = "+" + s(nz.ev) + " EV"
	}
	isoIn := []any{kv("ISO", nz.iso), kv("lift", lift), kv("effective ISO", nz.effISO, "ISO × 2^lift")}
	var ch chain
	if measured {
		ch.ask("Measured noise high?", nz.risk == "high", opts{decides: true, value: []any{"high"},
			rule:   fmt.Sprintf("noise.high_sigma = %s levels", s(cget(nc, p, "high_sigma"))),
			inputs: []any{kv("noise sigma", nz.sigma, "8-bit levels, flattest half of the frame, after any lift")},
			effect: "a tier 3 must clear the cuts by noise.margin"})
		ch.ask("Measured noise medium?", nz.risk == "medium", opts{decides: true, value: []any{"medium"},
			rule:   fmt.Sprintf("noise.medium_sigma = %s levels", s(cget(nc, p, "medium_sigma"))),
			effect: "noted for the model; no tier change"})
	} else {
		ch.ask("Effective ISO high?", nz.risk == "high", opts{decides: true, value: []any{"high"},
			rule: "noise.high_iso = " + s(cget(nc, p, "high_iso")), inputs: isoIn,
			effect: "a tier 3 must clear the cuts by noise.margin"})
		ch.ask("Effective ISO medium?", nz.risk == "medium", opts{decides: true, value: []any{"medium"},
			rule: "noise.noisy_iso = " + s(cget(nc, p, "noisy_iso")), effect: "noted for the model; no tier change"})
	}
	facts := isoIn
	if !measured {
		note := "not measured: analyzed before noise was; re-run the local stage"
		if nz.sigma != nil {
			note = "8-bit levels; set noise.medium_sigma and high_sigma to judge on it"
		}
		facts = append(append([]any{}, isoIn...), kv("noise sigma", nz.sigma, note))
	}
	how := ternary(measured, "measured", "from ISO")
	return stage("noise", "Noise", "done", fmt.Sprintf("%s (%s)", s(nz.risk), how),
		stageOpts{facts: facts, nodes: ch.nodes, outcome: label("noise " + s(or(nz.risk, "–")))})
}

// exifStage is exif.prior: motion-blur and depth-of-field risk from the exposure settings. Only high motion risk
// can move a tier (the slow-shutter rule in the local tier); the rest is context for the notes and the vision
// prompt.
func exifStage(local, cfg pj.Obj) *py.Object {
	if local == nil {
		return stage("exif", "Camera settings", "pending", "read with the local stage", stageOpts{})
	}
	ex := pj.O(local, "exif")
	if len(ex) == 0 {
		return stage("exif", "Camera settings", "skipped", "no EXIF", stageOpts{outcome: label("no EXIF")})
	}
	ec := pj.O(cfg, "exif")
	const p = "exif."
	pr := exif.Prior(ex, ec)
	sh, f := ex["shutter_s"], ex["f_number"]
	act := cgetd(ec, p, "action_shutter", 1.0/500)
	var shutter, stops any
	if pj.Truthy(sh) {
		shutter = exif.FmtShutter(pj.F(sh))
	}
	if ss, ok := pr["shake_stops"].(float64); ok {
		stops = fmtPlus(ss) + " stops"
	}
	var ch chain
	riskIs := func(key, want string, on any) any {
		if !pj.Truthy(on) {
			return nil
		}
		return pr[key] == want
	}
	ch.ask("Motion risk high?", riskIs("motion_risk", "high", sh), opts{decides: true, value: []any{"high"},
		rule: "a stop or more past 1/focal length, or 1/60 s and slower",
		inputs: []any{kv("shutter", shutter), kv("vs 1/focal", stops,
			fmt.Sprintf("crop_factor %s when EXIF lacks the 35mm focal", s(cgetd(ec, p, "crop_factor", 1.0))))},
		effect: "a tier 3 must clear the cuts by exif.shake_margin"})
	ch.ask("Motion risk medium?", riskIs("motion_risk", "medium", sh), opts{decides: true, value: []any{"medium"},
		rule:   fmt.Sprintf("within a stop of 1/focal length, or slower than exif.action_shutter (%s)", exif.FmtShutter(pj.F(act))),
		effect: "noted for the model; no tier change"})
	var aperture any
	if pj.Truthy(f) {
		aperture = "f/" + fmtG(f)
	}
	ch.nodes = append(ch.nodes, node("Very shallow depth of field?", riskIs("dof_risk", "high", f), opts{
		rule:   fmt.Sprintf("f ≤ %s (exif.wide_open_f) or entrance pupil ≥ 40 mm", s(cgetd(ec, p, "wide_open_f", 2.0))),
		inputs: []any{kv("aperture", aperture), kv("pupil", pr["pupil_mm"], "mm, focal ÷ f-number")},
		effect: "noted: judge the eyes; no tier change"}))
	summary := pr["summary"]
	return stage("exif", "Camera settings", "done", s(or(summary, "read")), stageOpts{
		facts: []any{kv("camera", summary)}, nodes: ch.nodes,
		outcome: label(fmt.Sprintf("motion %s · DOF %s", s(or(pr["motion_risk"], "–")), s(or(pr["dof_risk"], "–"))))})
}

func detectStage(local, cfg pj.Obj) *py.Object {
	if local == nil {
		return stage("detect", "People", "pending", "local stage hasn't run", stageOpts{})
	}
	people := objs(local["people"])
	n := getd(local, "n_people", int64(0))
	var stored any
	if pj.F(n) > float64(len(people)) {
		stored = fmt.Sprintf("%d stored", len(people))
	}
	facts := []any{kv("found", n, stored),
		kv("pose model", cfg["detect_model"]),
		kv("confidence ≥", cfg["detect_conf"], "detect_conf"),
		kv("smallest person", cfg["min_person_frac"], "min_person_frac × frame area"),
		kv("duplicates", fmt.Sprintf("IoU ≥ %s, or ≥ %s with the same head", s(cget(cfg, "", "dedup_iou")),
			s(cget(cfg, "", "dedup_head_iou"))), "dedup_iou / dedup_head_iou / dedup_head_tol")}
	rows := make([]any, 0, len(people))
	for i, p := range people {
		rows = append(rows, py.NewObject("n", i+1, "conf", rnd(p["conf"], 3), "area", rnd(p["area_frac"], 4),
			"center", rnd(p["center_dist"], 3), "priority", p["priority"], "head_src", p["head_src"]))
	}
	state := ternary(pj.Truthy(n), "done", "skipped")
	word := ternary(eq(n, int64(1)), "person", "people")
	return stage("detect", "People", state, fmt.Sprintf("%s %s", s(n), word), stageOpts{facts: facts,
		table: py.NewObject("kind", "people", "rows", rows), outcome: label(s(n) + " found")})
}

// maxBy is Python's max(xs, key=...) for a two-part key: the first of equals wins.
func maxBy(xs []pj.Obj, key func(pj.Obj) [2]float64) pj.Obj {
	var best pj.Obj
	var bk [2]float64
	for i, x := range xs {
		k := key(x)
		if i == 0 || k[0] > bk[0] || (k[0] == bk[0] && k[1] > bk[1]) {
			best, bk = x, k
		}
	}
	return best
}

func primaryStage(local, cfg pj.Obj, people, picked []pj.Obj, by string) (*py.Object, error) {
	if local == nil {
		return stage("primary", "Primary subject", "pending", "local stage hasn't run", stageOpts{}), nil
	}
	if len(people) == 0 {
		return stage("primary", "Primary subject", "skipped", "nobody to pick", stageOpts{outcome: label("none")}), nil
	}
	acfg := pj.O(cfg, "af")
	const p = "af."
	afv := local["af"]
	scores := make([]any, 0, len(picked))
	for _, q := range picked {
		scores = append(scores, py.NewObject("n", pid(people, q), "af_score", q["af_score"], "priority", q["priority"]))
	}
	var best pj.Obj
	if pj.Truthy(afv) {
		best = maxBy(picked, func(q pj.Obj) [2]float64 {
			return [2]float64{pj.F(or(q["af_score"], 0.0)), pj.F(or(q["priority"], 0.0))}
		})
	}
	var ch chain
	ch.ask("AF points in the file?", afv != nil, opts{inputs: []any{kv("read", local["af_note"])}})
	if afv != nil {
		a, ok := afv.(pj.Obj)
		if !ok || best == nil {
			return nil, errors.New("local_json: af is not a non-empty object")
		}
		use := cgetd(acfg, p, "use", true)
		ch.ask("Let AF points pick?", pj.Truthy(use), opts{rule: "af.use = " + s(use)})
		mode, ok := a["mode_name"].(string)
		if !ok {
			return nil, errors.New("local_json: af has no mode_name")
		}
		if !pj.Truthy(a["user_placed"]) {
			mode += " (camera-chosen)"
		}
		active, _ := a["active"].([]any)
		ch.ask("Any active points?", pj.Truthy(a["active"]), opts{inputs: []any{kv("mode", mode),
			kv("active", fmt.Sprintf("%d of %s", len(active), s(a["n_points"])), "points that reported focus, else the selected ones")}})
		var gate any = "AF can't pick: off, or no active points"
		if pj.Truthy(use) && pj.Truthy(a["active"]) {
			gate = nil
		}
		minScore := cgetd(acfg, p, "min_score", 0.5)
		bn := pid(people, best)
		ch.ask("Best AF hit score reaches min_score?", ge(or(best["af_score"], 0.0), minScore), opts{decides: true, gate: gate,
			rule: fmt.Sprintf("af.min_score = %s · af.near = %s", s(minScore), s(cgetd(acfg, p, "near", int64(0)))),
			inputs: []any{kv(fmt.Sprintf("person #%d", bn), best["af_score"],
				"head hit 2, torso 1.5, body 1 per point; up to half that just beside them")},
			effect: fmt.Sprintf("person #%d is the primary (AF)", bn)})
	}
	prom := maxBy(picked, func(q pj.Obj) [2]float64 { return [2]float64{pj.F(or(q["priority"], 0.0)), 0} })
	pn := pid(people, prom)
	ch.ask("Most prominent person", true, opts{decides: true,
		inputs: []any{kv(fmt.Sprintf("person #%d", pn), prom["priority"],
			"area × (1 − 0.5 × distance from center) × (0.5 + 0.5 × confidence)")},
		effect: fmt.Sprintf("person #%d is the primary (prominence)", pn)})
	top := picked[0]
	tn := pid(people, top)
	storedBy := local["primary_by"]
	var facts []any
	if !eq(people[0]["box"], top["box"]) || (pj.Truthy(storedBy) && !eq(storedBy, by)) {
		facts = []any{kv("stale", fmt.Sprintf("The stored results still have person #1 (%s) as primary; saving the config re-scores and picks #%d.", s(storedBy), tn))}
	}
	how := ternary(by == "af", "AF", "prominence")
	return stage("primary", "Primary subject", "done", fmt.Sprintf("person #%d by %s", tn, how), stageOpts{
		nodes: ch.nodes, table: py.NewObject("kind", "scores", "rows", scores), facts: facts,
		outcome: py.NewObject("label", fmt.Sprintf("#%d (%s)", tn, how), "person", tn)}), nil
}

// onEyes is `thr.get("use_eyes", True) and p.get("sharp_eye") is not None and "eye_tier3_min" in thr`, with
// Python's `and` returning a falsy use_eyes itself.
func onEyes(p, thr pj.Obj) any {
	if use := cgetd(thr, "focus.", "use_eyes", true); !pj.Truthy(use) {
		return use
	}
	_, has := thr["eye_tier3_min"]
	return p["sharp_eye"] != nil && has
}

func eyesStage(local, cfg pj.Obj, people []pj.Obj, primary pj.Obj) *py.Object {
	if local == nil {
		return stage("eyes", "Where it's judged", "pending", "local stage hasn't run", stageOpts{})
	}
	if primary == nil {
		return stage("eyes", "Where it's judged", "skipped", "no primary subject", stageOpts{})
	}
	thr := pj.O(cfg, "focus")
	const p = "focus."
	face, src := primary["face"], primary["eye_src"]
	on := onEyes(primary, thr)
	var ch chain
	var faceIn []any
	var faceNote any = "falls back to the pose model's eye keypoints (confidence ≥ 0.5)"
	if pj.Truthy(face) {
		faceIn = []any{kv("score", pj.Get(face, "score"))}
		faceNote = nil
		switch pj.Get(face, "rejected") {
		case "profile":
			faceNote = "not used: the head is side-on (pose keypoints), and there the face model's landmarks follow a frontal template; judged on the one visible eye"
		case "off_pose":
			faceNote = "not used: its eyes aren't where the pose model sees eyes; falls back to the pose eye keypoints"
		}
	}
	ch.ask("Face model found this head's face?", face != nil, opts{rule: "face_conf = " + s(cget(cfg, "", "face_conf")),
		inputs: faceIn, note: faceNote})
	from := "—"
	switch s(or(src, "")) {
	case "face":
		from = "face landmarks"
	case "pose":
		from = "pose keypoints"
	}
	var eyeNote any
	if primary["sharp_eye"] == nil {
		eyeNote = "helmet, visor, turned away, or inter-eye distance under 8 px / band under 24 px"
		if float64(pid(people, primary)) > pj.F(cgetd(cfg, "", "eye_max_people", int64(4))) {
			eyeNote = "only the top eye_max_people people get eye bands"
		}
	}
	ch.ask("Eye band located and measurable?", primary["sharp_eye"] != nil, opts{inputs: []any{kv("from", from)}, note: eyeNote})
	effect := "eye band Laplacian"
	if pj.Truthy(cgetd(thr, p, "use_hf", true)) && primary["hf_eye"] != nil {
		effect += " + FFT ratio"
	}
	ch.ask("Judge on the eye band?", on, opts{decides: true, rule: "focus.use_eyes = " + s(cgetd(thr, p, "use_eyes", true)), effect: effect})
	if !pj.Truthy(on) {
		eff := "nothing measurable"
		if primary["sharp_head"] != nil {
			eff = "head box Laplacian"
		} else if primary["sharp_body"] != nil {
			eff = "body box Laplacian"
		}
		ch.nodes = append(ch.nodes, node("Head box measurable?", primary["sharp_head"] != nil, opts{
			inputs: []any{kv("head from", primary["head_src"], "keypoints, or guessed from the box top")}, effect: eff}))
	} else {
		ew := rules.Eyewear(primary, thr)
		var res, ratio, eff any
		if pj.Truthy(thr["eyewear_ratio"]) {
			res = ew
		}
		if pj.Truthy(primary["sharp_head"]) {
			ratio = pj.Round(pj.F(primary["sharp_eye"])/pj.F(primary["sharp_head"]), 2)
		}
		if ew {
			eff = "the head box must clear each tier too"
		}
		ch.nodes = append(ch.nodes, node("Eye band looks like eyewear?", res, opts{
			rule: "focus.eyewear_ratio = " + s(cget(thr, p, "eyewear_ratio")), inputs: []any{kv("eye / head", ratio)}, effect: eff}))
	}
	basis := "body box"
	if pj.Truthy(on) {
		basis = "eye band"
	} else if primary["sharp_head"] != nil {
		basis = "head box"
	}
	return stage("eyes", "Where it's judged", "done", basis, stageOpts{nodes: ch.nodes, outcome: label(basis)})
}

// gradeTable is the grade (rules.Grade) and the per-tier pass/fail matrix behind it.
func gradeTable(p, thr pj.Obj) (any, *py.Object) {
	type row struct {
		label string
		v     any
		pre   string
	}
	var rows []row
	if pj.Truthy(onEyes(p, thr)) {
		rows = append(rows, row{"eye band Laplacian", p["sharp_eye"], "eye_"})
		_, hasHF := thr["hf_tier3_min"]
		if pj.Truthy(cgetd(thr, "focus.", "use_hf", true)) && p["hf_eye"] != nil && hasHF {
			rows = append(rows, row{"eye band FFT ratio", p["hf_eye"], "hf_"})
		}
		if rules.Eyewear(p, thr) {
			rows = append(rows, row{"head box Laplacian (eyewear)", p["sharp_head"], ""})
		}
	} else {
		lab := ternary(pj.Truthy(p["sharp_head"]), "head box Laplacian", "body box Laplacian")
		rows = append(rows, row{lab, or(p["sharp_head"], p["sharp_body"]), ""})
	}
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		cuts, ok := make([]any, 0, 3), make([]any, 0, 3)
		for _, n := range []int{3, 2, 1} {
			k := fmt.Sprintf("%stier%d_min", r.pre, n)
			cuts = append(cuts, thr[k])
			ok = append(ok, r.v != nil && ge(r.v, thr[k]))
		}
		out = append(out, py.NewObject("label", r.label, "value", rnd(r.v, 4), "cuts", cuts, "ok", ok))
	}
	return grade(p, thr, 1), py.NewObject("kind", "grade", "tiers", []any{3, 2, 1}, "rows", out)
}

func gradeStage(local, cfg, primary pj.Obj) *py.Object {
	if local == nil {
		return stage("grade", "Grade", "pending", "local stage hasn't run", stageOpts{})
	}
	if primary == nil {
		return stage("grade", "Grade", "skipped", "no primary subject", stageOpts{})
	}
	g, table := gradeTable(primary, pj.O(cfg, "focus"))
	summary, lab := "nothing measurable", "—"
	if g != nil {
		summary, lab = fmt.Sprintf("grade %d", g), fmt.Sprintf("grade %d", g)
	}
	return stage("grade", "Grade", "done", summary, stageOpts{table: table,
		outcome: py.NewObject("label", lab, "tier", g),
		facts:   []any{kv("rule", "the highest tier every row clears")}})
}

func tag(t, r any) string { return fmt.Sprintf("tier %s · %s", s(t), s(r)) }

// localTierStage is rules.LocalTier, one rule at a time and in its order. Every rule is evaluated for this photo;
// the ones its grade or an earlier rule made moot are marked not reached, with what they would have done. The
// second result is the check: the walk's answer, LocalTier's and the stored one (empty when there's no local
// result).
func localTierStage(local, cfg pj.Obj, people []pj.Obj, primary pj.Obj, others []pj.Obj) (*py.Object, *py.Object) {
	if local == nil {
		return stage("local", "Local tier", "pending", "local stage hasn't run", stageOpts{}), py.NewObject()
	}
	thr := pj.O(cfg, "focus")
	const p = "focus."
	prior := pj.O(local, "exif_prior")
	if !pj.Truthy(prior) {
		prior = pj.Obj{}
	}
	margin := cgetd(pj.O(cfg, "exif"), "exif.", "shake_margin", 1.5)
	noise := computeNoise(local, cfg)
	var size *rules.Size
	if pj.Truthy(local["width"]) {
		size = &rules.Size{W: pj.F(local["width"]), H: pj.F(local["height"])}
	}
	var g any
	if primary != nil {
		g = grade(primary, thr, 1)
	}
	eyes := primary != nil && pj.Truthy(onEyes(primary, thr))
	var ch chain

	ch.ask("Nobody found?", primary == nil, opts{decides: true, value: []any{0, "no_people"},
		effect: tag(0, "no_people"), inputs: []any{kv("people", getd(local, "n_people", int64(0)))}})

	// secondary_person_sharp as a floor: someone else confidently detected who grades floor_grade or better
	ft, fc, fg := cget(thr, p, "floor_tier"), cgetd(thr, p, "floor_conf", 0.5), cgetd(thr, p, "floor_grade", int64(3))
	rows := []any{}
	someone := false
	for _, o := range others {
		og := grade(o, thr, 1)
		tests := py.NewObject("conf ≥ "+s(fc), ge(or(o["conf"], 0.0), fc), "grade ≥ "+s(fg), ge(or(og, 0.0), fg))
		ok := ge(or(o["conf"], 0.0), fc) && ge(or(og, 0.0), fg)
		someone = someone || ok
		rows = append(rows, py.NewObject("n", pid(people, o), "conf", rnd(o["conf"], 3), "grade", og, "tests", tests, "ok", ok))
	}
	anyone := ft != nil && rules.SharpOther(others, thr)
	below := g == nil || pj.F(g) < pj.F(or(ft, 0.0))
	floorQ, floorRes, floorEff, floorNote := "Someone else sharp raises the floor?", any(nil), any(nil), any("off (floor_tier = null)")
	if ft != nil {
		floorQ = fmt.Sprintf("Primary below tier %s, and someone else sharp?", s(ft))
		floorRes, floorEff, floorNote = below && anyone, tag(ft, "secondary_person_sharp"), nil
	}
	ch.ask(floorQ, floorRes, opts{decides: true, value: []any{ft, "secondary_person_sharp"},
		rule:   fmt.Sprintf("focus.floor_tier = %s · floor_grade = %s · floor_conf = %s", s(ft), s(fg), s(fc)),
		inputs: []any{kv("primary grade", g), kv("below the floor", below), kv("someone else qualifies", someone)},
		people: rows, effect: floorEff, note: floorNote})
	unmeasurable := primary != nil && g == nil
	ch.ask("Primary unmeasurable?", unmeasurable, opts{decides: true, value: []any{0, "subject_too_small"},
		effect: tag(0, "subject_too_small"), note: ternary[any](unmeasurable, "every region under 40 px", nil)})

	// Grade 3 and the rules that can take it down to 2, in LocalTier's order
	g3 := eq(g, 3)
	var not3 any
	if !g3 {
		not3 = fmt.Sprintf("only checked on a grade-3 primary (this one grades %s)", s(g))
	}
	ch.ask("Grade 3?", g3, opts{inputs: []any{kv("grade", g)}})
	var gm, gn any
	if primary != nil {
		gm = grade(primary, thr, pj.F(margin))
	}
	risky := prior["motion_risk"] == "high"
	ch.ask("Slow shutter, and not sharp by the margin?", risky && gm != nil && pj.F(gm) < 3, opts{decides: true, gate: not3,
		value: []any{2, "borderline_sharp_slow_shutter"}, effect: tag(2, "borderline_sharp_slow_shutter"),
		rule: "exif.shake_margin = " + s(margin),
		inputs: []any{kv("motion risk", prior["motion_risk"], prior["summary"]),
			kv(fmt.Sprintf("grade at %s× the cuts", s(margin)), gm)}})
	nmargin := cgetd(pj.O(cfg, "noise"), "noise.", "margin", 1.5)
	if primary != nil {
		gn = grade(primary, thr, pj.F(nmargin))
	}
	noisy := noise.risk == "high"
	var by any
	if pj.Truthy(noise.by) {
		by = "by " + s(noise.by)
	}
	ch.ask("High noise, and not sharp by the margin?", noisy && gn != nil && pj.F(gn) < 3, opts{decides: true, gate: not3,
		value: []any{2, "borderline_sharp_noisy"}, effect: tag(2, "borderline_sharp_noisy"),
		rule:   "noise.margin = " + s(nmargin),
		inputs: []any{kv("noise risk", noise.risk, by), kv(fmt.Sprintf("grade at %s× the cuts", s(nmargin)), gn)}})

	pl := pj.O(primary, "plane")
	hasPl := pj.Truthy(pl)
	usePlane := cgetd(thr, p, "use_plane", true)
	cut, bodyCut := cget(thr, p, "plane_max_extra"), cget(thr, p, "plane_body_max_extra")
	var noPlane any
	if primary != nil && !hasPl {
		noPlane = "no plane measurement (head under 40 px, or analyzed before the check existed)"
	}
	var nearRes, torsoRes, nearNote any
	if pj.Truthy(usePlane) && pj.Truthy(cut) && hasPl {
		nearRes = ge(or(pl["head_vs_near"], 0.0), cut)
	}
	if hasPl && pl["near"] == nil {
		nearNote = "too few edges (bokeh)"
	}
	ch.ask("Surroundings sharper than the head?", nearRes, opts{decides: true, gate: not3,
		value: []any{2, "sharper_around_subject"}, effect: tag(2, "sharper_around_subject"),
		rule: fmt.Sprintf("focus.use_plane = %s · plane_max_extra = %s px", s(usePlane), s(cut)),
		inputs: []any{kv("head blur", pl["head"], "edge width, px"), kv("surroundings blur", pl["near"], nearNote),
			kv("head extra", pl["head_vs_near"], "px more than the surroundings, in quadrature")},
		note: or(noPlane, ternary[any](pj.Truthy(usePlane) && pj.Truthy(cut), nil, "off"))})
	if pj.Truthy(usePlane) && pj.Truthy(bodyCut) && hasPl {
		torsoRes = ge(or(pl["head_vs_torso"], 0.0), bodyCut)
	}
	ch.ask("Torso sharper than the head?", torsoRes, opts{decides: true, gate: not3,
		value: []any{2, "sharper_body_than_head"}, effect: tag(2, "sharper_body_than_head"),
		rule:   fmt.Sprintf("focus.plane_body_max_extra = %s px", s(bodyCut)),
		inputs: []any{kv("torso blur", pl["torso"]), kv("head extra", pl["head_vs_torso"], "px more than the torso")},
		note:   or(noPlane, ternary[any](pj.Truthy(bodyCut), nil, "off by default: clothing print reads sharper than a face"))})

	useFront := cgetd(thr, p, "use_front", true)
	frontRows := []any{}
	hit := -1
	if primary != nil && size != nil {
		frontRows = frontTests(primary, others, people, thr, *size)
		hit = rules.SoftInFront(primary, others, thr, size.W, size.H)
	}
	var frontRes, match any
	if pj.Truthy(useFront) {
		frontRes = hit >= 0
	}
	if hit >= 0 {
		match = fmt.Sprintf("person #%d", pid(people, others[hit]))
	}
	ch.ask("A soft person stands just in front?", frontRes, opts{decides: true, gate: not3,
		value: []any{2, "soft_person_in_front"}, effect: tag(2, "soft_person_in_front"),
		rule: fmt.Sprintf("focus.use_front = %s · front_min_height %s · front_min_drop %s · front_max_gap %s · front_max_grade %s · front_edge %s",
			s(useFront), s(cget(thr, p, "front_min_height")), s(cget(thr, p, "front_min_drop")), s(cget(thr, p, "front_max_gap")),
			s(cgetd(thr, p, "front_max_grade", int64(1))), s(cgetd(thr, p, "front_edge", 0.01))),
		inputs: []any{kv("match", match)}, people: frontRows, note: ternary[any](pj.Truthy(useFront), nil, "off")})
	r3 := ternary(eyes, "primary_eyes_sharp", "primary_head_sharp")
	ch.ask("Keeps tier 3", g3, opts{decides: true, gate: not3, value: []any{3, r3}, effect: tag(3, r3)})

	// Below 3: the grade is the tier
	for _, lv := range []struct {
		lvl    int
		reason string
	}{{2, ternary(eyes, "primary_eyes_slightly_soft", "primary_slightly_soft")}, {1, ternary(eyes, "primary_eyes_soft", "primary_soft")}} {
		ch.ask(fmt.Sprintf("Grade %d?", lv.lvl), eq(g, lv.lvl), opts{decides: true, value: []any{lv.lvl, lv.reason}, effect: tag(lv.lvl, lv.reason)})
	}
	bystander := false
	for _, o := range others {
		if eq(grade(o, thr, 1), 3) {
			bystander = true
			break
		}
	}
	zr := ternary(bystander, "secondary_person_sharp", "nothing_sharp")
	ch.ask("Grade 0", eq(g, 0), opts{decides: true, value: []any{0, zr}, effect: tag(0, zr),
		inputs: []any{kv("someone else grades 3", bystander)},
		note:   ternary[any](bystander, "the tier grades the primary, so a sharp bystander (with the floor off) keeps it a miss; the reason says so", nil)})

	traced := []any{nil, nil}
	if ch.value != nil {
		traced = ch.value
	}
	et, er := rules.LocalTier(primary, others, thr, prior, size, noise.prior, rules.MarginsFrom(cfg))
	engine := []any{et, er}
	stored := []any{local["local_tier"], local["local_reason"]}
	var facts []any
	if !eq(traced, engine) {
		facts = append(facts, kv("trace", fmt.Sprintf("this walk says %s, local_tier says %s", tag(traced[0], traced[1]), tag(et, er)),
			"the trace has fallen behind local.local_tier; the tier used everywhere is local_tier's"))
	}
	if !eq(engine, stored) {
		facts = append(facts, kv("stale", fmt.Sprintf("stored %s; the current config gives %s", tag(stored[0], stored[1]), tag(et, er)),
			"saving the config (or `photosort rescore`) updates the stored tier"))
	}
	return stage("local", "Local tier", "done", tag(et, er), stageOpts{nodes: ch.nodes, facts: facts,
			outcome: py.NewObject("label", fmt.Sprintf("local %d", et), "tier", et, "reason", er)}),
		py.NewObject("traced", traced, "engine", engine, "stored", stored)
}

// frontTests are rules.SoftInFront's tests on each other person, with the numbers, so the page shows which one
// failed.
func frontTests(primary pj.Obj, others []pj.Obj, people []pj.Obj, thr pj.Obj, size rules.Size) []any {
	const p = "focus."
	W, H := size.W, size.H
	bx, _ := primary["box"].([]any)
	e := cgetd(thr, p, "front_edge", 0.01)
	out := []any{}
	if len(bx) == 0 {
		return out
	}
	h := pj.F(bx[3]) - pj.F(bx[1])
	lim := func(k string) any { return or(cget(thr, p, k), int64(0)) }
	hmin, drop, gap := lim("front_min_height"), lim("front_min_drop"), lim("front_max_gap")
	mg := cgetd(thr, p, "front_max_grade", int64(1))
	var cutKey string
	if n, ok := mg.(int64); ok {
		cutKey = fmt.Sprintf("tier%d_min", n+1)
	} else {
		cutKey = "tier" + py.FloatRepr(pj.F(mg)+1) + "_min"
	}
	cut := cget(thr, p, cutKey)
	for _, o := range others {
		b, _ := o["box"].([]any)
		sh := o["sharp_head"]
		if len(b) == 0 || h == 0 {
			continue
		}
		hr := (pj.F(b[3]) - pj.F(b[1])) / h
		dr := (pj.F(b[3]) - pj.F(bx[3])) / h
		gp := math.Max(pj.F(b[0])-pj.F(bx[2]), pj.F(bx[0])-pj.F(b[2])) / h
		ef := pj.F(e)
		edge := pj.F(b[0]) <= ef*W || pj.F(b[2]) >= (1-ef)*W || pj.F(b[3]) >= (1-ef)*H
		shown := gp
		if 0 > gp {
			shown = 0
		}
		head := "–"
		if sh != nil {
			head = fmt.Sprintf("%.4f", pj.F(sh))
		}
		tests := py.NewObject(
			fmt.Sprintf("height %.2f× ≥ %s×", hr, s(hmin)), hr >= pj.F(hmin),
			fmt.Sprintf("feet %+.2f ≥ %s lower", dr, s(drop)), dr >= pj.F(drop),
			fmt.Sprintf("gap %.2f ≤ %s to the side", shown, s(gap)), gp <= pj.F(gap),
			fmt.Sprintf("clear of the frame edge (%s)", s(e)), !edge,
			fmt.Sprintf("head %s < %s (tier %s or worse)", head, s(cut), s(mg)), sh != nil && cut != nil && pj.F(sh) < pj.F(cut),
		)
		ok := true
		for _, k := range tests.Keys() {
			v := tests.Get(k)
			ok = ok && v.(bool)
		}
		out = append(out, py.NewObject("n", pid(people, o), "tests", tests, "ok", ok))
	}
	return out
}

func splitStage(local, cfg, primary pj.Obj) *py.Object {
	if local == nil {
		return stage("split", "Metrics agree?", "pending", "local stage hasn't run", stageOpts{})
	}
	thr := pj.O(cfg, "focus")
	steps := cget(thr, "focus.", "split_steps")
	sp := rules.MetricSplit(primary, thr)
	grades := py.NewObject()
	if primary != nil {
		for _, m := range []struct{ name, key, pre string }{{"eye", "sharp_eye", "eye_"}, {"fft", "hf_eye", "hf_"}, {"head", "sharp_head", ""}} {
			v := primary[m.key]
			if _, has := thr[m.pre+"tier3_min"]; v == nil || !has {
				continue
			}
			g := 0
			for _, lvl := range []int{3, 2, 1} {
				if ge(v, thr[fmt.Sprintf("%stier%d_min", m.pre, lvl)]) {
					g = lvl
					break
				}
			}
			grades.Set(m.name, g)
		}
	}
	inputs := make([]any, 0, grades.Len())
	for _, k := range grades.Keys() {
		g := grades.Get(k)
		inputs = append(inputs, kv(k, g))
	}
	on := pj.Truthy(steps)
	var res, effect, note any
	if on {
		res = sp != nil
		if grades.Len() < 2 {
			note = "fewer than two metrics measured"
		}
	} else {
		note = "off"
	}
	if sp != nil {
		effect = fmt.Sprintf("%s is %d tiers off (a filter in Review; the tier is left alone)", s(sp["odd"]), int(pj.F(sp["gap"])))
	}
	n := node("One metric far from the others?", res, opts{rule: "focus.split_steps = " + s(steps), inputs: inputs,
		effect: effect, note: note})
	summary := ternary(sp != nil, "split", ternary(on, "agree", "off"))
	return stage("split", "Metrics agree?", ternary(on, "done", "off"), summary, stageOpts{nodes: []any{n},
		outcome: label(ternary(sp != nil, "split", "agree"))})
}

func vlmStage(row db.Image, local, vlm, cfg pj.Obj) (*py.Object, error) {
	stale := export.VLMStale(nilIfNil(local), nilIfNil(vlm))
	usage, err := decodeObj("vlm_usage", row.VLMUsage)
	if err != nil {
		return nil, err
	}
	errText := ""
	if row.Error != nil {
		errText = *row.Error
	}
	if vlm == nil {
		if row.VLMSkip != nil && *row.VLMSkip != "" {
			n := node("Sent to the vision model?", false, opts{inputs: []any{kv("skipped", *row.VLMSkip)},
				note: "the job had “skip nobody-in-focus” on; re-tag with it off to have the model look"})
			return stage("vlm", "Vision model", "skipped", *row.VLMSkip, stageOpts{nodes: []any{n}, outcome: label("skipped")}), nil
		}
		state := ternary(errText != "" && pj.Truthy(local), "error", "pending")
		summary := errText
		if summary == "" {
			summary = ternary(pj.Truthy(local), "not tagged yet", "waits for the local stage")
		}
		return stage("vlm", "Vision model", state, summary, stageOpts{outcome: label("—")}), nil
	}
	keeper := ternary(pj.Truthy(vlm["keeper"]), "keeper", "cull")
	facts := []any{kv("focus notes", vlm["focus_notes"]),
		kv("subject", fmt.Sprintf("%s · %s · %s", s(vlm["primary_subject"]), s(vlm["composition"]), s(vlm["subject_placement"]))),
		kv("score", fmt.Sprintf("%s · %s", s(vlm["quality_score"]), keeper)),
		kv("model", or(or(usage["model"], cfg["model"]), cfg["backend"]))}
	evSeen := or(vlm["seen_ev"], int64(0))
	evNow := or(pj.Get(local, "exposure", "ev"), int64(0))
	var effect, note any
	if stale {
		effect, note = "stale: counts as not run until the model re-tags it", "the model judged a frame with a different exposure lift"
	}
	n := node("Verdict made on the frame the local stage has now?", !stale, opts{
		inputs: []any{kv("model saw", "+"+s(evSeen)+" EV"), kv("local has", "+"+s(evNow)+" EV")}, effect: effect, note: note})
	ft := vlm["focus_tier"]
	return stage("vlm", "Vision model", "done", "tier "+s(ft)+ternary(stale, " · stale", ""), stageOpts{facts: facts,
		nodes: []any{n}, outcome: py.NewObject("label", "model "+s(ft)+ternary(stale, " (stale)", ""), "tier", ft)}), nil
}

// nilIfNil keeps a nil pj.Obj from becoming a non-nil interface.
func nilIfNil(o pj.Obj) any {
	if o == nil {
		return nil
	}
	return o
}

// minPy is Python's min(a, b): the first unless the second is smaller.
func minPy(a, b any) any {
	if less, _ := py.Less(b, a); less {
		return b
	}
	return a
}

func finalStage(rec *export.Record, local, vlm, ov, cfg pj.Obj) *py.Object {
	src := cgetd(cfg, "", "focus_source", "vlm")
	var lt, vt any
	if pj.Truthy(local) {
		lt = local["local_tier"]
	}
	if vlm != nil && !export.VLMStale(nilIfNil(local), vlm) {
		vt = vlm["focus_tier"]
	}
	var ch chain
	var ratingIn []any
	if ov["rating"] != nil {
		ratingIn = []any{kv("your rating", ov["rating"])}
	}
	var ovEff any
	if ov["focus_tier"] != nil {
		ovEff = fmt.Sprintf("tier %s: your call beats everything", s(ov["focus_tier"]))
	}
	ch.ask("You rated it?", ov["focus_tier"] != nil, opts{decides: true, inputs: ratingIn, effect: ovEff})
	switch src {
	case "local":
		ch.ask("focus_source = local", true, opts{decides: true, rule: "focus_source = local", effect: fmt.Sprintf("tier %s (local)", s(lt))})
	case "strict":
		both := lt != nil && vt != nil
		var eff any
		if both {
			eff = fmt.Sprintf("tier %s: the lower of the two", s(minPy(lt, vt)))
		}
		ch.ask("Both tiers available?", both, opts{decides: both, rule: "focus_source = strict",
			inputs: []any{kv("local", lt), kv("model", vt)}, effect: eff})
		ch.ask("Fall back to whichever ran", true, opts{decides: true, effect: "tier " + s(ternary(vt == nil, lt, vt))})
	default:
		var model, stNote, eff any
		if pj.Truthy(vlm) {
			model = vlm["focus_tier"]
			if vt == nil {
				stNote = "stale"
			}
		}
		if vt != nil {
			eff = fmt.Sprintf("tier %s (model)", s(vt))
		}
		ch.ask("Usable model tier?", vt != nil, opts{decides: vt != nil, rule: "focus_source = " + s(src),
			inputs: []any{kv("model", model, stNote)}, effect: eff})
		ch.ask("Fall back to the local tier", true, opts{decides: true, effect: fmt.Sprintf("tier %s (local)", s(lt))})
	}
	t := rec.FocusTier
	state, summary, lab := "pending", "—", "—"
	if t != nil {
		state, summary, lab = "done", "tier "+s(t), "final "+s(t)
	}
	return stage("final", "Final tier", state, summary, stageOpts{nodes: ch.nodes, outcome: py.NewObject("label", lab, "tier", t)})
}

func reviewStage(rec *export.Record) *py.Object {
	lt, vt := rec.FocusTierLocal, rec.FocusTierVLM
	var res, eff, note any
	if vt != nil {
		res = rec.Disagree
	} else {
		note = "no model verdict (or a stale one) to compare"
	}
	if rec.Disagree {
		eff = "needs review"
	}
	var ratedEff any
	if rec.Reviewed {
		ratedEff = "off the Review queue (it shows unrated photos)"
	}
	nodes := []any{
		node("Local and model disagree?", res, opts{inputs: []any{kv("local", lt), kv("model", vt)}, effect: eff, note: note}),
		node("You've rated it?", rec.Reviewed, opts{effect: ratedEff}),
	}
	lab := ternary(rec.Review, "flagged", "not flagged") + ternary(rec.Reviewed, " · rated", "")
	return stage("review", "Review", "done", lab, stageOpts{nodes: nodes, outcome: label(lab)})
}

func exportStage(rec *export.Record, row db.Image) (*py.Object, error) {
	name := py.Name(row.Path)
	if rec.FocusTier == nil {
		return stage("export", "Export", "pending", "not exported until it has a tier", stageOpts{outcome: label("—")}), nil
	}
	ti, ok := py.IntKey(rec.FocusTier)
	tierDir, known := export.TierNames[ti]
	if !ok || !known {
		return nil, fmt.Errorf("no tier folder for focus tier %s", py.Repr(rec.FocusTier))
	}
	unknown := eq(rec.Subject, "unknown") && eq(rec.Composition, "unknown")
	first := fmt.Sprintf("%s/%s/%s/%s", tierDir, s(rec.Subject), s(rec.Composition), name)
	if unknown {
		first = tierDir + "/" + name
	}
	paths := []any{first}
	if rec.Review {
		paths = append(paths, fmt.Sprintf("review/local%s_vlm%s/%s", s(rec.FocusTierLocal), s(rec.FocusTierVLM), name))
	}
	if rec.Banger {
		paths = append(paths, "bangers/"+name)
	}
	var xmpLabel any
	if rec.Rating != nil {
		if n, ok := py.IntKey(rec.Rating); ok {
			if l, ok := export.RatingLabels[n]; ok {
				xmpLabel = l
			}
		}
	}
	kw := fmt.Sprintf("focus-%s · subject-%s · comp-%s", tierDir, s(rec.Subject), s(rec.Composition)) +
		ternary(rec.Review, " · photosort-review", "") + ternary(rec.Banger, " · photosort-banger", "")
	facts := []any{kv("XMP label", xmpLabel, "your rating's color; none until you rate it"),
		kv("XMP stars", rec.QualityScore, "quality score (yours, else the model's)"),
		kv("keywords", kw)}
	return stage("export", "Export", "done", first, stageOpts{facts: facts, table: py.NewObject("kind", "paths", "rows", paths),
		outcome: label(tierDir)}), nil
}

// ---- entry point --------------------------------------------------------------------------------------------

// Trace walks one image through every rule under cfg and returns what the Trace page shows: the image id and rel
// path, the focus source, one stage per pipeline step (scan, exposure, noise, exif, detect, primary, eyes, grade,
// local, split, vlm, final, review, export), the local-tier check (the walk's answer, LocalTier's and the stored
// one), the primary's person number and the final tiers. The primary is re-picked under the current config, as a
// rescore would; people keep their stored numbering. The value is an ordered object that encodes to the Python
// version's JSON. It fails where the Python raised: a JSON column that doesn't parse or isn't an object, a local
// result missing local_tier/n_people, a model result missing focus_tier.
func Trace(row db.Image, cfg pj.Obj, rel string) (any, error) {
	local, err := decodeObj("local_json", row.LocalJSON)
	if err != nil {
		return nil, err
	}
	vlm, err := decodeObj("vlm_json", row.VLMJSON)
	if err != nil {
		return nil, err
	}
	ov, err := decodeObj("override_json", row.OverrideJSON)
	if err != nil {
		return nil, err
	}
	if ov == nil {
		ov = pj.Obj{}
	}
	people := objs(local["people"])
	// Re-pick the primary under the current config, as a re-score would; the stored order is kept for numbering.
	picked := make([]pj.Obj, len(people))
	for i, p := range people {
		picked[i], _ = deepCopy(p).(pj.Obj)
	}
	by := "priority"
	if len(people) > 0 {
		afObj, _ := local["af"].(pj.Obj)
		by = rules.PickPrimary(picked, afObj, cfg, af.PersonScore)
	}
	var primary pj.Obj
	var others []pj.Obj
	if len(picked) > 0 {
		primary, others = picked[0], picked[1:]
	}
	source := cgetd(cfg, "", "focus_source", "vlm")
	rec, err := export.FinalRecord(export.Row{Path: row.Path, Error: row.Error, LocalJSON: row.LocalJSON,
		VLMJSON: row.VLMJSON, OverrideJSON: row.OverrideJSON}, pj.Str(source))
	if err != nil {
		return nil, err
	}
	localStage, check := localTierStage(local, cfg, people, primary, others)
	scanS, err := scanStage(row, local, rel)
	if err != nil {
		return nil, err
	}
	primaryS, err := primaryStage(local, cfg, people, picked, by)
	if err != nil {
		return nil, err
	}
	vlmS, err := vlmStage(row, local, vlm, cfg)
	if err != nil {
		return nil, err
	}
	exportS, err := exportStage(rec, row)
	if err != nil {
		return nil, err
	}
	stages := []any{
		scanS,
		exposureStage(local, cfg),
		noiseStage(local, cfg),
		exifStage(local, cfg),
		detectStage(local, cfg),
		primaryS,
		eyesStage(local, cfg, people, primary),
		gradeStage(local, cfg, primary),
		localStage,
		splitStage(local, cfg, primary),
		vlmS,
		finalStage(rec, local, vlm, ov, cfg),
		reviewStage(rec),
		exportS,
	}
	var primaryN any
	if pj.Truthy(primary) {
		primaryN = pid(people, primary)
	}
	return py.NewObject("id", row.ID, "rel", rel, "focus_source", source, "stages", stages, "check", check,
		"primary", primaryN,
		"final", py.NewObject("tier", rec.FocusTier, "local", rec.FocusTierLocal, "vlm", rec.FocusTierVLM,
			"review", rec.Review, "rating", rec.Rating)), nil
}
