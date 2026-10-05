package local

import (
	"fmt"

	"github.com/mononendev/photosort/internal/af"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/exif"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/rules"
)

// FileMeta reads real files: EXIF, Canon AF points, and the priors.
func FileMeta() Meta {
	return Meta{
		Exif:  exif.Read,
		AF:    af.ReadWithNote,
		Score: af.Scores,
		Prior: exif.Prior,
		NoisePrior: func(e pj.Obj, ev, sigma any, cfg pj.Obj) pj.Obj {
			var s *float64
			if f, ok := pj.Float(sigma); ok {
				s = &f
			}
			return exif.NoisePrior(e, pj.F(ev), s, cfg)
		},
	}
}

// RescoreResult counts what a rescore changed.
type RescoreResult struct {
	Changed        int     `json:"changed"`
	ExifBackfilled int     `json:"exif_backfilled"`
	AFBackfilled   int     `json:"af_backfilled"`
	PrimaryChanged int     `json:"primary_changed"`
	Errors         int     `json:"errors"`
	FirstError     *string `json:"first_error"`
}

// Rescore re-derives local tiers from stored metrics with the current thresholds (no re-detection).
//
// Rows analyzed before the EXIF prior or AF points existed get them read from the file (header only, fast), and the
// primary person is re-picked from the AF points.
//
// Rows go a page at a time, each page written before the next is read, so memory stays flat however large the
// library: holding every local_json at once ran the API container out of memory.
func Rescore(d *db.DB, meta Meta, cfg pj.Obj, backfillExif bool) (RescoreResult, error) {
	var res RescoreResult
	focus := pj.O(cfg, "focus")
	for after := int64(-1); ; {
		rows, err := d.Rows("local_json IS NOT NULL AND id > ?", []any{after}, "id", rescorePage, 0, "id, path, local_json")
		if err != nil {
			return res, err
		}
		if len(rows) == 0 {
			return res, nil
		}
		after = rows[len(rows)-1].ID
		if err := d.SetLocalMany(rescoreRows(rows, meta, cfg, focus, backfillExif, &res)); err != nil {
			return res, err
		}
	}
}

// rescorePage is how many rows a rescore holds at once.
var rescorePage = 500

// rescoreRows re-derives one page of rows, counting into res, and returns the rows that changed.
func rescoreRows(rows []db.Image, meta Meta, cfg, focus pj.Obj, backfillExif bool, res *RescoreResult) []db.LocalUpdate {
	var updates []db.LocalUpdate
	for _, r := range rows {
		func() {
			defer func() { // one malformed row must not abort the pass
				if p := recover(); p != nil {
					res.Errors++
					if res.FirstError == nil {
						s := fmt.Sprintf("%s: %v", r.Path, p)
						res.FirstError = &s
					}
				}
			}()
			dd := pj.Parse(r.LocalJSON)
			if dd == nil {
				panic("local_json is not an object")
			}
			dirty := false
			if _, has := dd["exif"]; backfillExif && !has {
				dd["exif"] = meta.Exif(r.Path)
				res.ExifBackfilled++
				dirty = true
			}
			if _, has := dd["exif"]; has {
				dd["exif_prior"] = meta.Prior(pj.O(dd, "exif"), pj.O(cfg, "exif"))
			}
			noise := meta.NoisePrior(pj.O(pj.Or(dd["exif"], pj.Obj{})), pj.Get(dd, "exposure", "ev"), pj.Get(dd, "noise", "sigma"),
				pj.O(cfg, "noise"))
			if !pj.Equal(noise, dd["noise"]) {
				dirty = true
			}
			dd["noise"] = noise
			var people []pj.Obj
			for _, p := range pj.A(dd, "people") {
				o, ok := p.(pj.Obj)
				if !ok {
					panic(fmt.Sprintf("people entry %v is not an object", p))
				}
				people = append(people, o)
			}
			_, hasAF := dd["af"]
			if backfillExif && dd["af"] == nil && pj.Truthy(dd["width"]) {
				// Also retries rows whose earlier read came back empty (the first reader missed Canon 0x0026).
				a, note := meta.AF(r.Path, pj.Int(dd["width"]), pj.Int(dd["height"]), on(pj.O(cfg, "af"), "y_up"))
				if a != nil || note != dd["af_note"] || !hasAF {
					dd["af"], dd["af_note"] = a, note
					if a != nil {
						res.AFBackfilled++
					}
					dirty = true
				}
			}
			var oldFirst any
			if len(people) > 0 {
				oldFirst = people[0]["box"]
			}
			oldBy := dd["primary_by"]
			dd["primary_by"] = rules.PickPrimary(people, pj.O(dd, "af"), cfg, meta.Score)
			if dd["primary_by"] != oldBy {
				dirty = true
			}
			stored := make([]any, len(people))
			for i, p := range people {
				stored[i] = p
			}
			if _, had := dd["people"]; had {
				dd["people"] = stored
			}
			if len(people) > 0 && !pj.Equal(people[0]["box"], oldFirst) {
				// Stored people carry their own metrics, so the new primary's numbers are already here; only the
				// head crop stays the old one until the next local pass.
				for k, v := range rules.PrimaryFields(people[0]) {
					dd[k] = v
				}
				res.PrimaryChanged++
				dirty = true
			}
			var primary pj.Obj
			var others []pj.Obj
			if len(people) > 0 {
				primary, others = people[0], people[1:]
			}
			var size *rules.Size
			if pj.Truthy(dd["width"]) {
				size = &rules.Size{W: pj.F(dd["width"]), H: pj.F(dd["height"])}
			}
			tier, reason := rules.LocalTier(primary, others, focus, pj.O(dd, "exif_prior"), size, noise, rules.MarginsFrom(cfg))
			split := rules.MetricSplit(primary, focus)
			oldTier, isNum := pj.Float(dd["local_tier"])
			tierChanged := !isNum || oldTier != float64(tier)
			if tierChanged || !pj.Equal(split, dd["split"]) || dirty {
				if tierChanged {
					res.Changed++
				}
				dd["local_tier"], dd["local_reason"], dd["split"] = tier, reason, split
				updates = append(updates, db.LocalUpdate{ID: r.ID, JSON: pj.Dumps(dd)})
			}
		}()
	}
	return updates
}
