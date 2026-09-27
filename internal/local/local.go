// Package local runs the local stage for one image: the analyzer measures, the rules decide, and the result is the
// local_json document stored on the image. It also re-derives stored results under a new config (Rescore).
package local

import (
	"context"
	"errors"
	"strings"

	"github.com/mononendev/photosort/internal/analyzer"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/rules"
)

// Pixels is the analyzer's measure/finalize pair (an *analyzer.Client, or a fake in tests).
type Pixels interface {
	Measure(ctx context.Context, req pj.Obj) (pj.Obj, error)
	Finalize(ctx context.Context, req pj.Obj) (pj.Obj, error)
}

// Meta reads what the file itself says: EXIF and the camera's AF points, and turns EXIF into priors. The functions
// come from packages exif and af; tests can stand in for them.
type Meta struct {
	Exif       func(path string) pj.Obj
	AF         func(path string, W, H int, yUp bool) (pj.Obj, string)
	Score      rules.Scorer
	Prior      func(exif, cfg pj.Obj) pj.Obj
	NoisePrior func(exif pj.Obj, ev, sigma any, cfg pj.Obj) pj.Obj
}

// Detector is the pose model settings the analyzer should use, from the config (and a job's override).
func Detector(cfg pj.Obj) pj.Obj {
	d := pj.Obj{"model": cfg["detect_model"], "imgsz": cfg["detect_long_edge"], "conf": cfg["detect_conf"],
		"iou": pj.Or(cfg["detect_iou"], 0.7)}
	for k, v := range pj.O(cfg, "detector") {
		d[k] = v
	}
	return d
}

// ModelName is the pose model a row analyzed under cfg records (its "detector"): the configured name, with the
// ".pt" of pre-ONNX configs dropped, since those run as their ONNX export.
func ModelName(cfg pj.Obj) string {
	return strings.TrimSuffix(pj.Str(Detector(cfg)["model"]), ".pt")
}

// LegacyModel is the model behind rows analyzed before local_json recorded one.
const LegacyModel = "yolo11n-pose"

// MeasureRequest is the analyzer request for one image under cfg.
func MeasureRequest(cfg pj.Obj, id int64, path, cacheDir string) pj.Obj {
	dedup := pj.Obj{}
	for _, k := range []string{"dedup_iou", "dedup_head_iou", "dedup_head_tol"} {
		if v, ok := cfg[k]; ok {
			dedup[k] = v
		}
	}
	return pj.Obj{
		"id": id, "path": path, "cache_dir": cacheDir, "exposure": cfg["exposure"],
		"detect_long_edge": cfg["detect_long_edge"], "min_person_frac": cfg["min_person_frac"],
		"dedup": dedup, "detect": Detector(cfg),
		"frame": pj.Obj{"long_edge": cfg["frame_long_edge"], "quality": cfg["frame_quality"]},
	}
}

func on(o pj.Obj, k string) bool {
	v, ok := o[k]
	return !ok || pj.Truthy(v)
}

// Analyze runs the local stage on one image and returns its local_json. The analyzer writes the frame, thumbnail and
// crop JPEGs into cacheDir as a side effect.
func Analyze(ctx context.Context, px Pixels, meta Meta, cfg pj.Obj, id int64, path, cacheDir string) (pj.Obj, error) {
	m, err := px.Measure(ctx, MeasureRequest(cfg, id, path, cacheDir))
	if err != nil {
		return nil, err
	}
	W, H := pj.Int(m["width"]), pj.Int(m["height"])
	acfg := pj.O(cfg, "af")
	af, note := meta.AF(path, W, H, on(acfg, "y_up"))
	people := peopleOf(m)
	primaryBy := rules.PickPrimary(people, af, cfg, meta.Score)
	order := make([]any, len(people))
	for i, p := range people {
		order[i] = p["_i"]
	}
	focus := pj.O(cfg, "focus")
	eyeMax := 4.0
	if v, ok := cfg["eye_max_people"]; ok {
		eyeMax = pj.F(v)
	}
	fin, err := px.Finalize(ctx, pj.Obj{
		"token": m["token"], "order": order, "eye_max_people": eyeMax,
		"use_face": on(focus, "use_eyes"), "face_model": pj.Or(cfg["face_model"], "face_detection_yunet_2023mar.onnx"),
		"face_conf": pj.Or(cfg["face_conf"], 0.6), "plane": on(focus, "use_plane"),
		"crop": pj.Obj{"pad": cfg["crop_pad"], "size": cfg["crop_size"], "quality": cfg["crop_quality"]},
	})
	if errors.Is(err, analyzer.ErrGone) {
		return nil, err // the caller may retry the whole image
	} else if err != nil {
		return nil, err
	}
	return Assemble(m, fin, people, primaryBy, af, note, meta, cfg, path), nil
}

// peopleOf copies the measured people, tagging each with its index in the analyzer's list.
func peopleOf(m pj.Obj) []pj.Obj {
	var people []pj.Obj
	for i, v := range pj.A(m, "people") {
		p := pj.Clone(v.(pj.Obj))
		p["_i"] = float64(i)
		people = append(people, p)
	}
	return people
}

// Assemble builds local_json from the measurements, the finalize answer for the order the rules picked (people is
// already in that order, tagged with "_i"), and the file's metadata.
func Assemble(m, fin pj.Obj, people []pj.Obj, primaryBy string, af pj.Obj, afNote string, meta Meta, cfg pj.Obj, path string) pj.Obj {
	eyes := pj.O(fin, "eyes")
	for _, p := range people {
		idx := pj.Int(p["_i"])
		delete(p, "_i")
		e := pj.O(eyes, itoa(idx))
		if e == nil {
			continue
		}
		for _, k := range []string{"eyes", "eye_src", "eye", "sharp_eye", "hf_eye", "face"} {
			p[k] = e[k]
		}
		terms := pj.O(p, "terms")
		if terms == nil {
			terms = pj.Obj{}
			p["terms"] = terms
		}
		terms["eye"] = e["terms_eye"]
	}
	var primary pj.Obj
	if len(people) > 0 {
		primary = people[0]
	}
	focus := pj.O(cfg, "focus")
	if primary != nil && on(focus, "use_plane") {
		primary["plane"] = fin["plane"]
	}
	W, H := pj.F(m["width"]), pj.F(m["height"])
	exif := meta.Exif(path)
	prior := meta.Prior(exif, pj.O(cfg, "exif"))
	noise := meta.NoisePrior(exif, pj.Get(m, "exposure", "ev"), m["noise_sigma"], pj.O(cfg, "noise"))
	var others []pj.Obj
	if len(people) > 1 {
		others = people[1:]
	}
	tier, reason := rules.LocalTier(primary, others, focus, prior, &rules.Size{W: W, H: H}, noise, rules.MarginsFrom(cfg))

	stored := make([]any, 0, min(6, len(people)))
	for _, p := range people[:min(6, len(people))] {
		q := pj.Obj{}
		for k, v := range p {
			if strings.HasPrefix(k, "sharp") || strings.HasPrefix(k, "hf_") {
				v = pj.RoundPtr(v, 4)
			}
			q[k] = v
		}
		stored = append(stored, q)
	}
	masks := make([]any, len(people))
	for i, p := range people {
		masks[i] = p["box"] // every person, masked out of the background metric
	}
	orientation := "landscape"
	if H > W {
		orientation = "portrait"
	}
	data := pj.Obj{
		"width": W, "height": H, "orientation": orientation,
		"n_people": len(people), "people": stored, "mask_boxes": masks,
		"bg_sharp": m["bg_sharp"], "global_sharp": m["global_sharp"],
		"bg_terms": m["bg_terms"], "global_terms": m["global_terms"], "eps": m["eps"],
		"af": af, "af_note": afNote, "primary_by": primaryBy,
		"crop_box": fin["crop_box"],
		"exif":     exif, "exif_prior": prior, "exposure": m["exposure"], "noise": noise,
		"local_tier": tier, "local_reason": reason, "split": rules.MetricSplit(primary, focus),
	}
	if det := m["detector"]; det != nil {
		data["detector"] = det
	}
	for k, v := range rules.PrimaryFields(primary) {
		data[k] = v
	}
	return data
}

func itoa(i int) string {
	return strings.TrimSuffix(pj.Dumps(i), "\n")
}
