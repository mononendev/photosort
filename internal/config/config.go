// Package config holds photosort's settings: the defaults, the workdir's config.json merged over them, and the
// change history every save appends to (config_history.jsonl) so any save can be rolled back.
//
// The config stays a dynamic JSON object (pj.Obj): the tier rules read thresholds by computed names
// ("eye_tier3_min"), users' files carry keys from older versions, and the UI edits it wholesale.
package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mononendev/photosort/internal/pj"
)

// Defaults returns a fresh copy of the default config.
func Defaults() pj.Obj {
	groups := pj.Obj{}
	for n := 1; n <= 4; n++ {
		groups[fmt.Sprint(n)] = pj.Obj{"folder": fmt.Sprintf("group_%d", n), "keywords": []any{}}
	}
	return pj.Obj{
		// What gets sent to the cloud model
		"frame_long_edge": 1568.0, // downscaled full frame (all providers cap image tokens near here)
		"frame_quality":   82.0,
		"crop_size":       768.0, // native-resolution crop around the primary subject (long edge)
		"crop_quality":    88.0,
		"crop_pad":        0.15, // padding around the detector box, fraction of box size
		// Underexposed frames are lifted before the local stage and the vision model see them. Brightness is judged
		// by the key (log-average luminance, linear light): below *_dark_key the frame is lifted toward target_key,
		// by at most *_max_ev stops and never so far that the 99th percentile passes highlight_cap; lifts under
		// min_ev are skipped. A RAW is lifted on its embedded camera JPEG, not re-demosaiced: that keeps the camera's
		// sharpening and denoise, which the focus thresholds are calibrated on (a plain demosaic moves the metrics
		// by 0.5-3.5x).
		"exposure": pj.Obj{"recover": true, "target_key": 0.08, "highlight_cap": 0.9, "min_ev": 0.5,
			"raw_dark_key": 0.03, "raw_max_ev": 4.0,
			"jpeg_dark_key": 0.015, "jpeg_max_ev": 1.5},
		// Local stage
		"detect_long_edge": 1280.0,
		"detect_conf":      0.25,
		"detect_model":     "yolo11n-pose.pt",
		"min_person_frac":  0.0015, // ignore boxes smaller than this fraction of the frame
		// Duplicate boxes on one person that survive the detector's NMS: drop the weaker one at IoU >= dedup_iou, or
		// at IoU >= dedup_head_iou when both put the head keypoints (shoulders + hips if one misses the head) within
		// dedup_head_tol * box size of each other. A box that sees the head is kept over one that doesn't.
		"dedup_iou":      0.6,
		"dedup_head_iou": 0.25,
		"dedup_head_tol": 0.1,
		"workers":        4.0,
		"local_ahead":    true,                                // while a job waits on the vision model, run the local stage of the jobs queued behind it
		"face_model":     "face_detection_yunet_2023mar.onnx", // OpenCV YuNet: locates the eyes inside the head box
		"face_conf":      0.6,
		"eye_max_people": 4.0, // eye bands for the N most prominent people
		// Local focus thresholds. When the eyes are found (face landmarks, else confident pose keypoints), the eye
		// band must clear both eye_* (contrast-normalized Laplacian) and hf_* (FFT upper-mid band energy ratio);
		// otherwise the head box Laplacian is judged against tier*_min. Tiers: 3 sharp, 2 slightly soft, 1 soft,
		// 0 miss. The eye/hf values are placeholders: upload exported verdicts on the Calibrate page (or run
		// `photosort calibrate`) to set them.
		"focus": pj.Obj{"tier3_min": 0.030, "tier2_min": 0.017, "tier1_min": 0.010,
			"eye_tier3_min": 0.060, "eye_tier2_min": 0.035, "eye_tier1_min": 0.020,
			"hf_tier3_min": 0.030, "hf_tier2_min": 0.017, "hf_tier1_min": 0.010,
			"use_eyes": true, "use_hf": true,
			// Eye band Laplacian above eyewear_ratio x the head's: sunglasses/goggles, so the head must clear too.
			"eyewear_ratio": 3.0,
			// A tier 3 whose head carries >= plane_max_extra px more edge blur than its surroundings drops to 2: focus
			// landed just in front or behind. plane_body_max_extra does the same against the torso; off by default,
			// since clothing print reads sharper than a face even when both are in focus.
			"use_plane": true, "plane_max_extra": 0.4, "plane_body_max_extra": nil,
			// A tier 3 drops to 2 when someone nearer (>= front_min_height x its box height, feet lower by
			// front_min_drop x that height) and beside it (<= front_max_gap heights apart) has a head at
			// front_max_grade or worse: focus went past them. People within front_edge of the frame edge don't count.
			"use_front": true, "front_min_height": 1.0, "front_min_drop": 0.25, "front_max_gap": 0.5,
			"front_max_grade": 1.0, "front_edge": 0.01,
			// Below floor_tier, a frame where someone besides the primary (detector conf >= floor_conf) grades
			// floor_grade or better is raised to floor_tier: focus landed on a person (nil = off).
			"floor_tier": 2.0, "floor_grade": 3.0, "floor_conf": 0.5,
			// Flags a metrics split when one of the primary's metrics (eye band Laplacian, eye band FFT, head box
			// Laplacian), each graded on its own thresholds, sits >= split_steps tiers from the nearest other (nil = off).
			"split_steps": 2.0},
		// Camera-metadata prior: crop_factor converts focal length to 35mm-equivalent when EXIF lacks it; f-number <=
		// wide_open_f or entrance pupil >= 40mm = "very shallow DOF"; a tier-3 sharpness below tier3_min*shake_margin
		// is demoted to tier 2 when the shutter was slow enough that motion blur is likely.
		"exif": pj.Obj{"crop_factor": 1.0, "wide_open_f": 2.0, "action_shutter": 1.0 / 500, "shake_margin": 1.5},
		// Noise: judged on effective ISO, ISO x 2^lift (noisy_iso / high_iso). The noise sigma measured on the frame
		// after any lift (8-bit levels) is always stored; set medium_sigma / high_sigma to judge on it instead. Its
		// level depends on the processing (Capture One exports read ~0.05 at ISO 100 and ~1 at ISO 1600), so
		// calibrate the cuts on your own camera files first. At high noise a tier-3 sharpness below tier3_min*margin
		// is demoted to tier 2 (1 = off).
		"noise": pj.Obj{"noisy_iso": 1600.0, "high_iso": 6400.0, "medium_sigma": nil, "high_sigma": nil, "margin": 1.5},
		// Camera AF points (Canon maker notes): the person the active points land on becomes the primary subject,
		// whatever their size or sharpness, when their score (head hit 2, torso 1.5, body 1 per point) >= min_score.
		// A point beside a region earns up to half its weight, falling to 0 at `near` point-widths away (0 = off).
		// y_up: AF y offsets count upward from center (flip if boxes draw mirrored top-to-bottom on your body).
		"af": pj.Obj{"use": true, "min_score": 0.5, "near": 2.0, "y_up": true},
		// Cloud stage
		"backend":               "ollama", // ollama (local, free) | gemini | anthropic
		"model":                 nil,      // nil = backend default
		"vlm_concurrency":       1.0,
		"batch_size":            1000.0, // requests per batch job (Anthropic backend clamps to its 256 MB limit)
		"gemini_thinking_level": "LOW",
		"anthropic_effort":      "low",
		"max_output_tokens":     1024.0,
		"ollama_num_ctx":        8192.0,
		"ollama_num_predict":    1536.0, // constrained JSON is pretty-printed; leave headroom so it never truncates
		// The local model gets a smaller frame; the crop carries fine focus.
		"ollama_frame_long_edge": 1024.0,
		// Hard string caps in the grammar (safer, slower decode); false = rely on sampling.
		"ollama_schema_max_lengths": true,
		// Ground truth (calibration): how your exported verdicts map to focus tiers when no explicit tier is given
		"truth": pj.Obj{"label_tiers": pj.Obj{"Blue": 3.0, "Green": 3.0, "Yellow": 2.0, "Orange": 1.0, "Red": 0.0},
			"rating_tiers": pj.Obj{"5": 3.0, "4": 3.0, "3": 2.0, "2": 1.0, "1": 0.0, "0": nil}},
		// Sorting
		"focus_source": "vlm", // vlm | local | strict (strict = min of both)
		// Your sort groups (keys a s d f in the photo view). In the sorted tree a grouped photo goes under its group's
		// folder (<folder>/focus_N/...) instead of the top level, and its XMP sidecar gets the group's keywords
		// ("Parent|Child" for a hierarchical one).
		"groups": groups,
	}
}

// migrateTiers brings a config.json from the three-tier scale (0 none, 1 partial, 2 sharp) to the four-tier one
// (0 miss, 1 soft, 2 slightly soft, 3 sharp). Old tier 2 becomes 3 and old tier 1 becomes 2, as in the database;
// each metric's new tier-2 cut starts at the geometric mean of its old two cuts. Reports whether anything changed.
func migrateTiers(user pj.Obj) bool {
	changed := false
	if f, ok := user["focus"].(pj.Obj); ok {
		for _, pre := range []string{"", "eye_", "hf_"} {
			t2v, t1v := f[pre+"tier2_min"], f[pre+"tier1_min"]
			_, has3 := f[pre+"tier3_min"]
			if t2v == nil || has3 {
				continue
			}
			f[pre+"tier3_min"] = t2v
			t2, t1 := pj.F(t2v), pj.F(t1v)
			if pj.Truthy(t1v) && t2 > 0 && t1 > 0 {
				f[pre+"tier2_min"] = pj.Round(math.Sqrt(t2*t1), 4)
			} else {
				f[pre+"tier2_min"] = t2v
			}
			changed = true
		}
	}
	t, ok := user["truth"].(pj.Obj)
	if changed && ok {
		up := map[float64]float64{1: 2, 2: 3}
		for _, k := range []string{"label_tiers", "rating_tiers"} {
			m, ok := t[k].(pj.Obj)
			if !ok {
				continue
			}
			for name, v := range m {
				if f, isNum := pj.Float(v); isNum {
					if u, hit := up[f]; hit {
						m[name] = u
					}
				}
			}
		}
		if lt, ok := t["label_tiers"].(pj.Obj); ok {
			if _, has := lt["Orange"]; !has {
				lt["Orange"] = 1.0
			}
		}
		oldDefault := pj.Obj{"5": 3.0, "4": 3.0, "3": 2.0, "2": 2.0, "1": 0.0, "0": nil}
		if rt, ok := t["rating_tiers"].(pj.Obj); ok && pj.Equal(rt, oldDefault) {
			t["rating_tiers"] = pj.Clone(pj.O(Defaults(), "truth", "rating_tiers"))
		}
	}
	return changed
}

// Merge lays user values over cfg one level deep: an object value updates the default object key by key, anything
// else replaces it. Keys the defaults don't know are kept.
func Merge(cfg, user pj.Obj) {
	for k, v := range user {
		uo, uok := v.(pj.Obj)
		co, cok := cfg[k].(pj.Obj)
		if uok && cok {
			maps.Copy(co, uo)
		} else {
			cfg[k] = v
		}
	}
}

// Load reads workdir/config.json over the defaults, writing the defaults there on first use.
func Load(workdir string) (pj.Obj, error) {
	cfg := Defaults()
	p := filepath.Join(workdir, "config.json")
	b, err := os.ReadFile(p)
	switch {
	case err == nil:
		var user pj.Obj
		if err := json.Unmarshal(b, &user); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if migrateTiers(user) {
			if err := writeJSON(p, user); err != nil {
				return nil, err
			}
		}
		Merge(cfg, user)
	case os.IsNotExist(err):
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			return nil, err
		}
		if err := writeJSON(p, cfg); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return cfg, nil
}

// Save writes the whole config to workdir/config.json.
func Save(workdir string, cfg pj.Obj) error {
	return writeJSON(filepath.Join(workdir, "config.json"), cfg)
}

func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Public is the config without internal keys (those starting with "_"), as the API shows it.
func Public(cfg pj.Obj) pj.Obj {
	out := pj.Obj{}
	for k, v := range cfg {
		if !strings.HasPrefix(k, "_") {
			out[k] = v
		}
	}
	return out
}

// ---- change history ----------------------------------------------------------------------------------------------
// Every save appends one line to config_history.jsonl: what changed, where from, and the whole config as it was just
// before, so any save (an auto-calibrate included) can be rolled back exactly.

// History is the history file's name inside the workdir.
const History = "config_history.jsonl"

var historyMu sync.Mutex

// Flatten turns nested objects into dotted keys; an empty object stays a leaf.
func Flatten(d pj.Obj, prefix string) pj.Obj {
	out := pj.Obj{}
	for k, v := range d {
		if o, ok := v.(pj.Obj); ok && len(o) > 0 {
			maps.Copy(out, Flatten(o, prefix+k+"."))
		} else {
			out[prefix+k] = v
		}
	}
	return out
}

// Diff lists the flattened keys whose values differ, sorted by key.
func Diff(before, after pj.Obj) []pj.Obj {
	a, b := Flatten(before, ""), Flatten(after, "")
	keys := slices.Collect(maps.Keys(a))
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var out []pj.Obj
	for _, k := range keys {
		if !pj.Equal(a[k], b[k]) {
			out = append(out, pj.Obj{"key": k, "from": a[k], "to": b[k]})
		}
	}
	return out
}

// LogEvent appends an entry, stamped with an id (microseconds) and a local timestamp, and returns it.
func LogEvent(workdir string, entry pj.Obj) (pj.Obj, error) {
	now := time.Now()
	e := pj.Obj{"id": float64(now.UnixMicro()), "at": now.Format("2006-01-02T15:04:05-0700")}
	maps.Copy(e, entry)
	historyMu.Lock()
	defer historyMu.Unlock()
	f, err := os.OpenFile(filepath.Join(workdir, History), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(pj.Dumps(e) + "\n"); err != nil {
		return nil, err
	}
	return e, nil
}

// LogChange records a save; nothing (nil) when it changed nothing.
func LogChange(workdir string, before, after pj.Obj, source string) (pj.Obj, error) {
	changes := Diff(before, after)
	if len(changes) == 0 {
		return nil, nil
	}
	list := make([]any, len(changes))
	for i, c := range changes {
		list[i] = c
	}
	return LogEvent(workdir, pj.Obj{"kind": "change", "source": source, "changes": list, "before": before})
}

// ReadHistory returns every entry, oldest first; a torn last line (crash mid-write) is skipped.
func ReadHistory(workdir string) ([]pj.Obj, error) {
	f, err := os.Open(filepath.Join(workdir, History))
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []pj.Obj
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20) // each line carries a whole config
	for sc.Scan() {
		var e pj.Obj
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Store is the live config shared by the API and the job runner. Readers get a private copy, so a save mid-job
// never changes the settings an image is being judged with halfway.
type Store struct {
	mu      sync.RWMutex
	cfg     pj.Obj
	workdir string
}

// NewStore loads the workdir's config.
func NewStore(workdir string) (*Store, error) {
	cfg, err := Load(workdir)
	if err != nil {
		return nil, err
	}
	return &Store{cfg: cfg, workdir: workdir}, nil
}

// Get is a copy of the current config.
func (s *Store) Get() pj.Obj {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pj.Clone(s.cfg)
}

// Update applies fn to a copy of the config, saves it, logs the change under source, and returns the entry logged
// (nil when nothing changed). fn may return an error to abort.
func (s *Store) Update(source string, fn func(cfg pj.Obj) error) (pj.Obj, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := pj.Clone(s.cfg)
	after := pj.Clone(s.cfg)
	if err := fn(after); err != nil {
		return nil, err
	}
	if err := Save(s.workdir, Public(after)); err != nil {
		return nil, err
	}
	s.cfg = after
	return LogChange(s.workdir, Public(before), Public(after), source)
}

// Override sets values in memory only (deployment wiring such as OLLAMA_HOST); the next save writes them too.
func (s *Store) Override(values pj.Obj) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range values {
		s.cfg[k] = v
	}
}

// Workdir is where the config lives.
func (s *Store) Workdir() string { return s.workdir }
