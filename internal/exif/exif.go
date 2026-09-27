// Package exif reads camera metadata and turns it into a static prior on focus and motion blur.
//
// A wide-open lens (f/1.4) makes a missed focus plane unsurprising; a slow shutter (1/30 s) makes motion blur
// likely. Neither replaces measuring the pixels, but both tell the pipeline how much to trust a borderline sharpness
// score and tell the vision model what to expect.
package exif

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/mononendev/photosort/internal/pj"
)

// fields maps each output key to the exifread tag names tried in order. "MakerNote LensModel"/"MakerNote LensType"
// and "EXIF PhotographicSensitivity" were listed in Python too but never matched (maker notes aren't decoded with
// details=False, and exifread names 0x8827 ISOSpeedRatings); they are kept for parity.
var fields = []struct {
	key     string
	names   []string
	numeric bool
}{
	{"camera", []string{"Image Model"}, false},
	{"lens", []string{"EXIF LensModel", "MakerNote LensModel", "MakerNote LensType"}, false},
	{"f_number", []string{"EXIF FNumber"}, true},
	{"shutter_s", []string{"EXIF ExposureTime"}, true},
	{"iso", []string{"EXIF ISOSpeedRatings", "EXIF PhotographicSensitivity"}, true},
	{"focal_mm", []string{"EXIF FocalLength"}, true},
	{"focal_35mm", []string{"EXIF FocalLengthIn35mmFilm"}, true},
	{"taken", []string{"EXIF DateTimeOriginal", "Image DateTime"}, false},
}

// Read is a best-effort EXIF read for JPEG/TIFF/HEIC/PNG/WebP and TIFF-based raws (CR2, NEF, ARW, DNG...).
//
// Returns an empty map when the file carries no usable metadata (CR3 included). Never fails. Numbers are float64
// rounded to 6 places; "taken" is the EXIF text as stored ("2024:09:12 15:24:19").
func Read(path string) pj.Obj {
	out := pj.Obj{}
	fh, err := os.Open(path)
	if err != nil {
		return out
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil || st.IsDir() {
		return out
	}
	f := &file{r: fh, size: st.Size()}
	tags, err := exifreadTags(f)
	if err != nil {
		tags = pillowTags(f)
	}
	for _, fd := range fields {
		for _, name := range fd.names {
			t := tags[name]
			if t == nil {
				continue
			}
			if fd.numeric {
				if t.numOK && t.num != 0 {
					out[fd.key] = pj.Round(t.num, 6)
					break
				}
			} else if s := pyStrip(t.str); s != "" {
				out[fd.key] = s
				break
			}
		}
	}
	return out
}

// FmtShutter renders an exposure time: "1/500 s" below half a second, "2 s" above, "?" when unknown (0).
func FmtShutter(s float64) string {
	if s == 0 {
		return "?"
	}
	if s < 0.5 {
		return fmt.Sprintf("1/%d s", pj.RoundInt(1/s))
	}
	return pyG(s) + " s"
}

// cfgF is float(cfg.get(key, def)).
func cfgF(cfg pj.Obj, key string, def float64) float64 {
	if v, ok := cfg[key]; ok {
		return pj.F(v)
	}
	return def
}

// Prior derives depth-of-field and motion-blur risk from the exposure settings.
//
// shake_stops: log2(shutter * focal35) — 0 means exactly the 1/focal-length rule, +1 means one stop slower than
// that (handheld shake likely), -3 means 3 stops faster. Subject motion (riders) needs faster still;
// `action_shutter` marks that bar.
func Prior(exif pj.Obj, cfg pj.Obj) pj.Obj {
	crop := cfgF(cfg, "crop_factor", 1.0)
	wide := cfgF(cfg, "wide_open_f", 2.0)
	actionS := cfgF(cfg, "action_shutter", 1.0/500)
	f, s := pj.F(exif["f_number"]), pj.F(exif["shutter_s"])
	focal35 := pj.F(exif["focal_35mm"])
	if focal35 == 0 {
		if fm := pj.F(exif["focal_mm"]); fm != 0 {
			focal35 = fm * crop
		}
	}
	out := pj.Obj{"dof_risk": nil, "motion_risk": nil, "shake_stops": nil, "pupil_mm": nil, "summary": nil}
	if len(exif) == 0 {
		return out
	}
	if f != 0 {
		// Entrance pupil (focal / f-number) tracks background blur and focus-plane thinness far better than the
		// f-number alone: 135mm f/2.2 (61mm pupil) is as unforgiving as 85mm f/1.4 (61mm).
		var pupil float64
		if focal35 != 0 {
			pupil = focal35 / f
		}
		if pupil != 0 {
			out["pupil_mm"] = pj.Round(pupil, 1)
		}
		switch {
		case f <= wide || (pupil != 0 && pupil >= 40):
			out["dof_risk"] = "high"
		case f <= 4.0 || (pupil != 0 && pupil >= 15):
			out["dof_risk"] = "medium"
		default:
			out["dof_risk"] = "low"
		}
	}
	shakeStops := 0.0
	if s != 0 {
		shake := -99.0
		if focal35 != 0 {
			shakeStops = pj.Round(math.Log2(s*focal35), 2)
			out["shake_stops"] = shakeStops
			shake = shakeStops
		}
		switch {
		case shake >= 1.0 || s >= 1.0/60:
			out["motion_risk"] = "high"
		case shake >= -1.0 || s > actionS:
			out["motion_risk"] = "medium"
		default:
			out["motion_risk"] = "low"
		}
	}
	var parts []string
	if cam, ok := exif["camera"].(string); ok && cam != "" {
		parts = append(parts, cam)
	}
	if focal35 != 0 {
		parts = append(parts, pyG(focal35)+"mm")
	}
	if f != 0 {
		parts = append(parts, "f/"+pyG(f))
	}
	if s != 0 {
		parts = append(parts, FmtShutter(s))
	}
	if iso := pj.F(exif["iso"]); iso != 0 {
		parts = append(parts, "ISO "+pyG(iso))
	}
	var notes []string
	switch out["dof_risk"] {
	case "high":
		notes = append(notes, "very shallow depth of field: only the focus plane can be sharp, judge the head/eyes")
	case "medium":
		notes = append(notes, "moderate depth of field")
	}
	switch out["motion_risk"] {
	case "high":
		if shakeStops >= 1 {
			notes = append(notes, "slow shutter for this focal length: motion blur likely")
		} else {
			notes = append(notes, "slow shutter: motion blur likely")
		}
	case "medium":
		notes = append(notes, "shutter marginal for a moving subject")
	case "low":
		notes = append(notes, "fast shutter: motion blur unlikely")
	}
	summary := strings.Join(parts, " · ")
	if len(notes) > 0 {
		summary += ". " + strings.Join(notes, "; ") + "."
	}
	out["summary"] = summary
	return out
}

// NoisePrior is the noise risk from the noise measured on the frame, or from the ISO and the exposure lift when it
// wasn't measured.
//
// Noise cuts both ways on the focus metrics: grain that survives the Laplacian's pre-blur reads as detail, while the
// camera's noise reduction at high ISO smears real detail away. Either way a borderline sharpness score means less.
//
// eff_iso: ISO x 2^ev. Lifting a frame by ev stops amplifies its noise as shooting that much higher would have.
// sigma: noise measured on the (lifted) frame, in 8-bit levels (local.noise_sigma), nil when not measured. It
// decides when it was measured and medium_sigma/high_sigma are set; until they're calibrated, and for rows
// analyzed before it was measured, eff_iso decides.
func NoisePrior(exif pj.Obj, ev float64, sigma *float64, cfg pj.Obj) pj.Obj {
	isoV := exif["iso"]
	iso := pj.F(isoV)
	var eff float64
	if iso != 0 {
		eff = iso * math.Pow(2, ev)
	}
	out := pj.Obj{"iso": isoV, "ev": nil, "eff_iso": nil, "sigma": nil, "risk": nil, "by": nil, "summary": nil}
	if ev != 0 {
		out["ev"] = ev
	}
	if eff != 0 {
		out["eff_iso"] = pj.RoundInt(eff)
	}
	if sigma != nil {
		out["sigma"] = *sigma
	}
	rank := func(v float64, med, high any) any {
		if med == nil || high == nil {
			return nil
		}
		switch {
		case v >= pj.F(high):
			return "high"
		case v >= pj.F(med):
			return "medium"
		}
		return "low"
	}
	if sigma != nil && cfg["medium_sigma"] != nil && cfg["high_sigma"] != nil {
		out["risk"], out["by"] = rank(*sigma, cfg["medium_sigma"], cfg["high_sigma"]), "measured"
	} else if eff != 0 {
		out["risk"], out["by"] = rank(eff, cfg["noisy_iso"], cfg["high_iso"]), "iso"
	}
	if risk := out["risk"]; risk == "medium" || risk == "high" {
		var what []string
		if iso != 0 {
			w := "ISO " + pyG(iso)
			if ev != 0 {
				w += fmt.Sprintf(" lifted +%s EV, about ISO %.0f", pyG(ev), eff)
			}
			what = append(what, w)
		}
		if sigma != nil {
			what = append(what, fmt.Sprintf("measured noise %.1f levels", *sigma))
		}
		level := "Moderate"
		if risk == "high" {
			level = "High"
		}
		out["summary"] = fmt.Sprintf("%s noise (%s): don't read grain as detail, or noise-reduction smear as missed focus.",
			level, strings.Join(what, "; "))
	}
	return out
}
