package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
)

func TestDefaultsMatchPython(t *testing.T) {
	b, err := os.ReadFile("../../testdata/config_defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var py pj.Obj
	if err := json.Unmarshal(b, &py); err != nil {
		t.Fatal(err)
	}
	// Changed on purpose with the move to ONNX pose models, and added for the analyzer pool; everything else must match.
	want := map[string]bool{"detect_model": true, "detect_iou": true, "analyzer_slots": true}
	for _, d := range Diff(py, Defaults()) {
		if !want[pj.Str(d["key"])] {
			t.Errorf("default differs from Python's: %v", pj.Dumps(d))
		}
	}
}

func TestLoadWritesDefaultsThenMergesOneLevel(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil || cfg["backend"] != "ollama" {
		t.Fatal(err, cfg["backend"])
	}
	user := `{"focus": {"tier3_min": 0.05}, "backend": "gemini", "extra": 1}`
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(user), 0o644)
	cfg, _ = Load(dir)
	if pj.F(pj.Get(cfg, "focus", "tier3_min")) != 0.05 || pj.F(pj.Get(cfg, "focus", "tier2_min")) != 0.017 {
		t.Error("focus not merged key by key")
	}
	if cfg["backend"] != "gemini" || cfg["extra"] != 1.0 {
		t.Error("top-level values not replaced/kept")
	}
}

func TestMigrateThreeTierConfig(t *testing.T) {
	user := pj.Obj{
		"focus": pj.Obj{"tier2_min": 0.04, "tier1_min": 0.01, "eye_tier2_min": 0.06, "eye_tier1_min": 0.02},
		"truth": pj.Obj{"label_tiers": pj.Obj{"Green": 2.0, "Yellow": 1.0, "Red": 0.0},
			"rating_tiers": pj.Obj{"5": 3.0, "4": 3.0, "3": 2.0, "2": 2.0, "1": 0.0, "0": nil}},
	}
	if !migrateTiers(user) {
		t.Fatal("expected a migration")
	}
	f := pj.O(user, "focus")
	if f["tier3_min"] != 0.04 || f["tier2_min"] != 0.02 || f["eye_tier2_min"] != 0.0346 {
		t.Errorf("focus: %v", f)
	}
	lt := pj.O(user, "truth", "label_tiers")
	if lt["Green"] != 3.0 || lt["Yellow"] != 2.0 || lt["Orange"] != 1.0 {
		t.Errorf("labels: %v", lt)
	}
	// As in Python: tiers are raised first, so the map no longer reads as the old default afterwards.
	want := pj.Obj{"5": 3.0, "4": 3.0, "3": 3.0, "2": 3.0, "1": 0.0, "0": nil}
	if !pj.Equal(pj.O(user, "truth", "rating_tiers"), want) {
		t.Errorf("ratings: %v", pj.O(user, "truth", "rating_tiers"))
	}
	if migrateTiers(user) {
		t.Error("a migrated config must not migrate again")
	}
}

func TestHistoryLogsChangesAndSkipsTornLines(t *testing.T) {
	dir := t.TempDir()
	before := Defaults()
	after := Defaults()
	pj.O(after, "focus")["tier3_min"] = 0.05
	e, err := LogChange(dir, before, after, "test")
	if err != nil || e == nil {
		t.Fatal(err)
	}
	ch := e["changes"].([]any)[0].(pj.Obj)
	if ch["key"] != "focus.tier3_min" || ch["from"] != 0.03 || ch["to"] != 0.05 {
		t.Errorf("change: %v", ch)
	}
	if e, _ := LogChange(dir, before, pj.Clone(before), "noop"); e != nil {
		t.Error("an unchanged save logs nothing")
	}
	f, _ := os.OpenFile(filepath.Join(dir, History), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"torn": `)
	f.Close()
	h, err := ReadHistory(dir)
	if err != nil || len(h) != 1 || h[0]["source"] != "test" {
		t.Fatalf("history: %v %v", h, err)
	}
}
