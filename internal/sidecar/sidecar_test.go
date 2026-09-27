package sidecar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mononendev/photosort/internal/py"
)

// Ports of tests/test_sidecar.py (the DB half of the ingest test becomes the set callback), plus the rating/label
// cases of testdata/export_golden.json (written by the Python).

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o666); err != nil {
		t.Fatal(err)
	}
}

func TestReadsAttributeAndElementForms(t *testing.T) {
	d := t.TempDir()
	img := filepath.Join(d, "IMG_1.CR2")
	write(t, img, "x")
	write(t, filepath.Join(d, "IMG_1.xmp"), `<x:xmpmeta><rdf:Description xmp:Rating="3" xmp:Label="Yellow"/></x:xmpmeta>`)
	if got := py.Dumps(ReadSidecar(img, nil)); got != `{"rating": 3, "label": "Yellow", "sidecar": "IMG_1.xmp"}` {
		t.Fatal(got)
	}
	img2 := filepath.Join(d, "IMG_2.CR2")
	write(t, img2, "x")
	write(t, filepath.Join(d, "IMG_2.XMP"), `<xmp:Rating>5</xmp:Rating><xmp:Label></xmp:Label>`)
	if lr := ReadSidecar(img2, nil); *lr.Rating != 5 || lr.Label != nil {
		t.Fatalf("%+v", lr)
	}
	if lr := ReadSidecar(filepath.Join(d, "nope.CR2"), nil); lr != nil || lr.JSON() != "{}" {
		t.Fatal(lr)
	}
}

func TestIngestListsEachFolderOnce(t *testing.T) {
	d := t.TempDir()
	var rows []Row
	for i := range 5 {
		p := filepath.Join(d, "IMG_"+string(rune('0'+i))+".jpg")
		write(t, p, "x")
		rows = append(rows, Row{ID: int64(i + 1), Path: p})
	}
	write(t, filepath.Join(d, "IMG_3.xmp"), `<x:xmpmeta xmp:Rating="4"/>`)
	var stored []Item
	n, err := Ingest(rows, func(items []Item) error { stored = items; return nil })
	if err != nil || n != 1 || len(stored) != 5 {
		t.Fatal(n, err, stored)
	}
	for i, it := range stored {
		want := "{}"
		if i == 3 {
			want = `{"rating": 4, "label": null, "sidecar": "IMG_3.xmp"}`
		}
		if it.ID != int64(i+1) || it.LR.JSON() != want {
			t.Errorf("item %d: %d %s", i, it.ID, it.LR.JSON())
		}
	}
	// The listing, not a stat, decides: a sidecar created after the folder was listed isn't seen in the same run,
	// and an unreadable folder has none.
	if lr := ReadSidecar(filepath.Join(d, "IMG_0.jpg"), map[string]bool{}); lr != nil {
		t.Fatal(lr)
	}
}

func TestRatingLabelMatchesPython(t *testing.T) {
	b, err := os.ReadFile("../../testdata/export_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		RatingLabel [][2]any `json:"rating_label"`
		LRJSON      []string `json:"lr_json"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.RatingLabel) == 0 {
		t.Fatal("golden file is incomplete")
	}
	for _, c := range g.RatingLabel {
		r, l := RatingLabel(c[0].(string))
		var got [2]any
		if r != nil {
			got[0] = float64(*r)
		}
		if l != nil {
			got[1] = *l
		}
		want := c[1].([]any)
		if got[0] != want[0] || got[1] != want[1] {
			t.Errorf("%q: got %v, want %v", c[0], got, want)
		}
	}
	four, green := 4, "Grün \"x\""
	if got := (&LR{Rating: &four, Sidecar: "IMG_3.xmp"}).JSON(); got != g.LRJSON[0] {
		t.Error(got)
	}
	if got := (&LR{Label: &green, Sidecar: "Ä.XMP"}).JSON(); got != g.LRJSON[1] {
		t.Error(got)
	}
}
