// Package export is stage 3: merge each image's results into a final record, build the sorted link tree, and export
// results.csv / results.jsonl and XMP sidecars for Lightroom.
//
// Records carry the stored JSON values through untouched (ints stay ints, floats keep their repr, objects their key
// order), so the files written match the Python version byte for byte.
package export

import (
	"fmt"
	"math"

	"github.com/mononendev/photosort/internal/py"
)

// TierNames are the tree folder (and XMP keyword) names of the focus tiers.
var TierNames = map[int]string{0: "focus_0_miss", 1: "focus_1_soft", 2: "focus_2_slightly_soft", 3: "focus_3_sharp"}

// Banger is the cull rating above the focus tiers: sharp and a favorite (only you give it). Ratings 0-3 are the
// focus tiers.
const Banger = 4

// RatingLabels are the color labels your cull rating exports as. Lightroom's stock label set has no Orange; it
// shows as a custom label.
var RatingLabels = map[int]string{0: "Red", 1: "Orange", 2: "Yellow", 3: "Green", 4: "Blue"}

// Row is what FinalRecord reads of an images row: the path, the error text and the JSON columns (nil or "" when
// NULL). A caller without override_json leaves OverrideJSON nil.
type Row struct {
	Path         string
	Error        *string
	LocalJSON    *string
	VLMJSON      *string
	OverrideJSON *string
}

// Record is an image's final, merged result: one line of results.jsonl, one row of results.csv. Fields typed any
// hold the stored JSON value as decoded by py.Loads (nil for None); the keys serialize in the Python dict's order.
type Record struct {
	Path           string
	FocusTier      any // the final tier: the override's, else local/model per the focus source
	FocusTierLocal any
	FocusTierVLM   any
	Disagree       bool // local and (non-stale) model tiers both present and different
	Split          any
	Subject        any
	Composition    any
	Placement      any
	Action         any
	PeopleCount    any
	Keywords       any
	Adjectives     any
	Description    any
	FocusNotes     any
	QualityRemarks any
	QualityScore   any
	Keeper         any
	Note           any
	Rating         any // your cull rating 0-4
	Banger         bool
	Reviewed       bool
	Group          any // your sort group 1-4: its own folder in the tree, its keywords in the XMP
	Overridden     bool
	Local          *py.Object // n_people, primary_head_sharp, primary_body_sharp, bg_sharp, local_reason
	Error          *string
	Review         bool // as REVIEW_SQL: the same as Disagree
}

// PyObject is the record as the Python dict, keys in its order.
func (r *Record) PyObject() *py.Object {
	var local, errv any
	if r.Local != nil {
		local = r.Local
	}
	if r.Error != nil {
		errv = *r.Error
	}
	return py.NewObject(
		"path", r.Path,
		"focus_tier", r.FocusTier,
		"focus_tier_local", r.FocusTierLocal,
		"focus_tier_vlm", r.FocusTierVLM,
		"disagree", r.Disagree,
		"split", r.Split,
		"subject", r.Subject,
		"composition", r.Composition,
		"placement", r.Placement,
		"action", r.Action,
		"people_count", r.PeopleCount,
		"keywords", r.Keywords,
		"adjectives", r.Adjectives,
		"description", r.Description,
		"focus_notes", r.FocusNotes,
		"quality_remarks", r.QualityRemarks,
		"quality_score", r.QualityScore,
		"keeper", r.Keeper,
		"note", r.Note,
		"rating", r.Rating,
		"banger", r.Banger,
		"reviewed", r.Reviewed,
		"group", r.Group,
		"overridden", r.Overridden,
		"local", local,
		"error", errv,
		"review", r.Review,
	)
}

// MarshalJSON writes the record's keys in the Python order.
func (r *Record) MarshalJSON() ([]byte, error) { return r.PyObject().MarshalJSON() }

// column parses a JSON column as jcol did: nil for NULL/empty text, an error for invalid JSON.
func column(name string, s *string) (any, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	v, err := py.Loads(*s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

// asObject is a stage column as final_record reads it: `x if x else None`, so an empty object counts as absent.
func asObject(name string, v any) (*py.Object, error) {
	if !py.Truthy(v) {
		return nil, nil
	}
	o, ok := v.(*py.Object)
	if !ok {
		return nil, fmt.Errorf("%s is not a JSON object", name)
	}
	return o, nil
}

// VLMStale reports whether the model's verdict was made on a different frame than the local stage now has: the
// exposure lift it saw (stamped into vlm_json as seen_ev) no longer matches the local stage's exposure.ev. Results
// from before the lift carry no stamp, which reads as 0. local and vlm are parsed columns (*py.Object or pj.Obj,
// nil when NULL). The twin of VLM_STALE_SQL.
func VLMStale(local, vlm any) bool {
	if !py.Truthy(vlm) {
		return false
	}
	seen, _ := py.Lookup(vlm, "seen_ev")
	exp, _ := py.Lookup(local, "exposure")
	ev, _ := py.Lookup(exp, "ev")
	return !py.Eq(py.Or(seen, int64(0)), py.Or(ev, int64(0)))
}

func need(o *py.Object, name, key string) (any, error) {
	v, ok := o.Get(key)
	if !ok {
		return nil, fmt.Errorf("%s has no %q", name, key)
	}
	return v, nil
}

// FinalRecord merges an image's stages into its final record. source picks the focus tier: "local" (the local
// stage's), "strict" (the lower of local and model) or anything else (the model's, "vlm"); a model verdict made on
// the pre-lift frame (VLMStale) counts as not run yet, and an override's focus_tier beats both. It fails where the
// Python raised: a column that isn't valid JSON or an object, a local result without local_tier/n_people, a model
// result without focus_tier.
func FinalRecord(row Row, source string) (*Record, error) {
	lv, err := column("local_json", row.LocalJSON)
	if err != nil {
		return nil, err
	}
	vv, err := column("vlm_json", row.VLMJSON)
	if err != nil {
		return nil, err
	}
	local, err := asObject("local_json", lv)
	if err != nil {
		return nil, err
	}
	vlm, err := asObject("vlm_json", vv)
	if err != nil {
		return nil, err
	}
	ov := py.NewObject()
	if row.OverrideJSON != nil && *row.OverrideJSON != "" {
		v, err := column("override_json", row.OverrideJSON)
		if err != nil {
			return nil, err
		}
		o, ok := v.(*py.Object)
		if !ok {
			return nil, fmt.Errorf("override_json is not a JSON object")
		}
		ov = o
	}

	var lt, vt, nPeople any
	if local != nil {
		if lt, err = need(local, "local_json", "local_tier"); err != nil {
			return nil, err
		}
		if nPeople, err = need(local, "local_json", "n_people"); err != nil {
			return nil, err
		}
	}
	if vlm != nil {
		if vt, err = need(vlm, "vlm_json", "focus_tier"); err != nil {
			return nil, err
		}
	}
	useVT := vt
	if VLMStale(local, vlm) {
		useVT = nil
	}
	var tier any
	switch {
	case source == "local" || useVT == nil:
		tier = lt
	case source == "strict" && lt != nil:
		less, ok := py.Less(useVT, lt)
		if !ok {
			return nil, fmt.Errorf("can't compare tiers %s and %s", py.Repr(lt), py.Repr(useVT))
		}
		tier = lt // min(lt, use_vt): the first unless the second is smaller
		if less {
			tier = useVT
		}
	default:
		tier = useVT
	}
	if ot, _ := ov.Get("focus_tier"); ot != nil {
		if tier, err = pyInt(ot); err != nil {
			return nil, err
		}
	}

	subjectDefault := any("unknown")
	if local != nil && py.Eq(nPeople, int64(0)) {
		subjectDefault = "no_people"
	}
	rating, _ := ov.Get("rating")
	reviewed, _ := ov.Get("reviewed")
	note, _ := ov.Get("note")
	group, _ := ov.Get("group")
	rec := &Record{
		Path:           row.Path,
		FocusTier:      tier,
		FocusTierLocal: lt,
		FocusTierVLM:   vt,
		Disagree:       lt != nil && useVT != nil && !py.Eq(lt, useVT),
		Split:          local.GetOr("split", nil),
		Subject:        vlm.GetOr("primary_subject", subjectDefault),
		Composition:    vlm.GetOr("composition", "unknown"),
		Placement:      vlm.GetOr("subject_placement", nil),
		Action:         vlm.GetOr("action", nil),
		PeopleCount:    vlm.GetOr("people_count", nPeople),
		Keywords:       vlm.GetOr("keywords", []any{}),
		Adjectives:     vlm.GetOr("adjectives", []any{}),
		Description:    vlm.GetOr("description", nil),
		FocusNotes:     vlm.GetOr("focus_notes", nil),
		QualityRemarks: vlm.GetOr("quality_remarks", nil),
		QualityScore:   ov.GetOr("quality_score", vlm.GetOr("quality_score", nil)),
		Keeper:         ov.GetOr("keeper", vlm.GetOr("keeper", nil)),
		Note:           note,
		Rating:         rating,
		Banger:         py.Eq(rating, int64(Banger)),
		Reviewed:       py.Truthy(reviewed),
		Group:          group,
		Overridden:     ov.Len() > 0,
		Error:          row.Error,
	}
	if local != nil {
		rec.Local = py.NewObject()
		for _, k := range []string{"n_people", "primary_head_sharp", "primary_body_sharp", "bg_sharp", "local_reason"} {
			rec.Local.Set(k, local.GetOr(k, nil))
		}
	}
	rec.Review = rec.Disagree
	return rec, nil
}

// pyInt is int(v) for a JSON value: ints as is, floats truncated, bools 0/1, numeric strings parsed.
func pyInt(v any) (any, error) {
	switch x := v.(type) {
	case int64, int, py.BigInt:
		return x, nil
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) >= 1<<63 {
			return nil, fmt.Errorf("cannot convert float %s to integer", py.FloatRepr(x))
		}
		return int64(x), nil
	case string:
		if n, ok := py.Int(x); ok {
			return n, nil
		}
	}
	return nil, fmt.Errorf("invalid literal for int(): %s", py.Repr(v))
}
