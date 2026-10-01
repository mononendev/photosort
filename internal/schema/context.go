package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

// intFields are the local_json fields Python always has as ints. They print as ints even when decoded to float64
// (pj.Parse), so the text is the same whichever way the caller decoded local_json.
var intFields = map[string]bool{"width": true, "height": true, "n_people": true, "local_tier": true}

// ContextText is the per-image detector summary passed alongside the images (Python's context_text), byte for
// byte: it is part of the prompt.
//
// local is a decoded local_json. Numbers may be float64 (pj.Parse) or json.Number (py.LoadsNumber, or a json.Decoder
// with UseNumber). Values are printed with Python's str(): a float64 always as a float ("0.0123", "1e-05", "3.0"),
// except the int fields width/height/n_people/local_tier, which print as ints; a json.Number keeps its literal's
// int/float kind. Local_json written by Python has floats in every other printed field, so decoding with pj.Parse
// reproduces Python exactly, even after a Go round trip that dropped a ".0".
//
// A missing required key fails like Python's KeyError ("KeyError: 'width'").
func ContextText(local pj.Obj) (string, error) {
	if len(local) == 0 {
		return "Detector data unavailable.", nil
	}
	need := func(o pj.Obj, k string) (any, error) {
		v, ok := o[k]
		if !ok {
			return nil, py.KeyError(k)
		}
		return v, nil
	}
	str := func(k string, v any) string {
		if intFields[k] {
			if f, ok := v.(float64); ok && f == math.Trunc(f) && !math.IsInf(f, 0) && math.Abs(f) < 1e18 {
				return strconv.FormatInt(int64(f), 10)
			}
		}
		return py.Str(v)
	}
	var err error
	get := func(o pj.Obj, k string) string {
		if err != nil {
			return ""
		}
		var v any
		v, err = need(o, k)
		return str(k, v)
	}

	n, ok := local["n_people"]
	if !ok {
		n = 0
	}
	lines := []string{fmt.Sprintf("Frame: %sx%s %s. People detected: %s.",
		get(local, "width"), get(local, "height"), get(local, "orientation"), str("n_people", n))}
	if err != nil {
		return "", err
	}
	if s := pj.Get(local, "exif_prior", "summary"); pj.Truthy(s) {
		lines = append(lines, "Camera: "+py.Str(s))
	}
	if ev := pj.Get(local, "exposure", "ev"); pj.Truthy(ev) {
		lines = append(lines, fmt.Sprintf("The original was underexposed; these images were brightened by %s stops "+
			"for review, so judge exposure as dark and expect lifted shadow noise.", fixed(ev, 1)))
	}
	if s := pj.Get(local, "noise", "summary"); pj.Truthy(s) {
		lines = append(lines, py.Str(s))
	}
	people := pj.A(local, "people")
	if pj.Truthy(n) && pj.Truthy(local["people"]) {
		p, _ := people[0].(pj.Obj)
		af := pj.O(local, "af")
		if local["primary_by"] == "af" {
			lines = append(lines, fmt.Sprintf("The camera's AF points (%s) were on this person: they are the intended "+
				"subject even if someone else is larger or sharper. Grade focus on them.", py.Str(af["mode_name"])))
		} else if pj.Truthy(af["active"]) {
			lines = append(lines, fmt.Sprintf("The camera's AF points (%s) were not on any detected person.", py.Str(af["mode_name"])))
		}
		area, e1 := need(p, "area_frac")
		center, e2 := need(p, "center")
		if e := firstErr(e1, e2); e != nil {
			return "", e
		}
		c, _ := center.([]any)
		if len(c) < 2 {
			return "", py.Errorf("IndexError", "list index out of range")
		}
		af100 := pj.F(area) * 100
		if nb, ok := area.(json.Number); ok && py.IntLiteral(nb) {
			af100 = float64(pj.Int(area) * 100)
		}
		lines = append(lines, fmt.Sprintf("Primary subject: %s%% of frame, center at (%s, %s) (0,0 = top-left). Head located by %s.",
			strconv.FormatFloat(af100, 'f', 1, 64), fixed(c[0], 2), fixed(c[1], 2), get(p, "head_src")))
		lines = append(lines, fmt.Sprintf("Sharpness (higher = sharper): head %s, torso %s, body %s, background %s, whole frame %s.",
			get(p, "sharp_head"), get(p, "sharp_torso"), get(p, "sharp_body"), py.Str(local["bg_sharp"]), py.Str(local["global_sharp"])))
		if err != nil {
			return "", err
		}
		if p["sharp_eye"] != nil {
			src := "pose keypoints"
			if p["eye_src"] == "face" {
				src = "face landmarks"
			}
			what := "Eye band (both eyes, located by %s)"
			if eyes, _ := p["eyes"].([]any); len(eyes) == 1 {
				what = "Eye box (one eye, the head is side-on; located by %s)"
			}
			lines = append(lines, fmt.Sprintf(what+": sharpness %s, fine-detail energy ratio %s. This is what decides focus.",
				src, py.Str(p["sharp_eye"]), py.Str(p["hf_eye"])))
		} else {
			lines = append(lines, "Eyes not located (helmet, visor, turned away, or too small); judge the head.")
		}
		if pj.F(n) > 1 {
			var others []string
			for _, q := range people[1:min(4, len(people))] {
				qo, _ := q.(pj.Obj)
				others = append(others, py.Str(pj.Or(qo["sharp_head"], qo["sharp_body"])))
			}
			lines = append(lines, fmt.Sprintf("Other people head sharpness: %s.", strings.Join(others, ", ")))
		}
		lines = append(lines, fmt.Sprintf("Local focus guess: tier %s (%s).", get(local, "local_tier"), get(local, "local_reason")))
		if err != nil {
			return "", err
		}
	} else {
		lines = append(lines, fmt.Sprintf("Whole-frame sharpness %s. Local focus guess: tier 0 (no people).", py.Str(local["global_sharp"])))
	}
	return strings.Join(lines, "\n"), nil
}

// fixed is Python's f"{v:.{n}f}" for a number (Python and strconv both round the exact binary value half-even).
func fixed(v any, n int) string {
	return strconv.FormatFloat(pj.F(v), 'f', n, 64)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
