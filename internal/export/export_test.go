package export

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

// Ports of the pure parts of tests/test_local.py::test_final_record_and_xmp and the export tests in
// tests/test_rating.py. The HTTP ones (PATCH /api/images rating/focus_tier/group handling, SQL filters) belong to
// the web/db layers and are not ported here; the final-record halves of the stale-verdict tests are.

func sp(s string) *string { return &s }

func wellFormed(t *testing.T, doc string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(doc))
	for {
		_, err := d.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("XMP not well-formed: %v", err)
		}
	}
}

func TestFinalRecordAndXMP(t *testing.T) {
	row := Row{Path: filepath.Join(t.TempDir(), "a.jpg"),
		LocalJSON: sp(`{"local_tier": 2, "n_people": 1, "primary_head_sharp": 0.1, "primary_body_sharp": 0.1, "bg_sharp": 0.01, "local_reason": "primary_head_sharp"}`),
		VLMJSON: sp(`{"focus_tier": 1, "primary_subject": "rider_action", "composition": "full_body", "subject_placement": "center", "action": "carving",
			"people_count": 1, "keywords": ["onewheel"], "adjectives": ["dynamic"], "description": "d", "focus_notes": "f", "quality_remarks": "q", "quality_score": 4, "keeper": true}`),
		OverrideJSON: sp(`{"focus_tier": 3}`)}
	rec, err := FinalRecord(row, "vlm")
	if err != nil {
		t.Fatal(err)
	}
	if !py.Eq(rec.FocusTier, 3) || !rec.Review || !rec.Overridden {
		t.Fatalf("record %s", py.Dumps(rec))
	}
	doc := XMPFor(rec, nil)
	wellFormed(t, doc)
	if !strings.Contains(doc, "PhotoSort|Focus|focus_3_sharp") || !strings.Contains(doc, `xmp:Rating="4"`) {
		t.Fatal(doc)
	}
}

func TestExportCarriesLabelAndBanger(t *testing.T) {
	dir := t.TempDir()
	rec := &Record{Path: filepath.Join(dir, "x.jpg"), FocusTier: 3, FocusTierLocal: 2, FocusTierVLM: 3,
		Subject: "rider_action", Composition: "full_body", Keywords: []any{}, Adjectives: []any{}, Rating: 4, Banger: true}
	x := XMPFor(rec, nil)
	if !strings.Contains(x, `xmp:Label="Blue"`) || !strings.Contains(x, "PhotoSort|Banger") {
		t.Fatal(x)
	}
	if err := os.WriteFile(rec.Path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	counts, err := BuildTree([]*Record{rec}, filepath.Join(dir, "out"), "copy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Get("bangers") != 1 {
		t.Fatal(counts)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "bangers", "x.jpg")); err != nil {
		t.Fatal(err)
	}
}

func TestGroupExportsInItsOwnFolderWithItsKeywords(t *testing.T) {
	dir := t.TempDir()
	rec := &Record{Path: filepath.Join(dir, "x.jpg"), FocusTier: 3, FocusTierLocal: 3, Subject: "rider_action",
		Composition: "full_body", Keywords: []any{}, Adjectives: []any{}, Group: 1}
	groups := pj.Obj{"1": pj.Obj{"folder": "../clients", "keywords": []any{"Clients|Smith", "private"}}}
	x := XMPFor(rec, groups)
	for _, s := range []string{"<rdf:li>Smith</rdf:li>", "<rdf:li>Clients|Smith</rdf:li>", "<rdf:li>private</rdf:li>"} {
		if !strings.Contains(x, s) {
			t.Fatalf("missing %s in\n%s", s, x)
		}
	}
	ungrouped := *rec
	ungrouped.Group = nil
	if strings.Contains(XMPFor(&ungrouped, groups), "Smith") {
		t.Fatal("ungrouped record got the group's keywords")
	}
	if err := os.WriteFile(rec.Path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	counts, err := BuildTree([]*Record{rec}, filepath.Join(dir, "out"), "copy", groups)
	if err != nil {
		t.Fatal(err)
	}
	if counts.String() != "{'clients/focus_3_sharp': 1}" {
		t.Fatal(counts)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "clients", "focus_3_sharp", "rider_action", "full_body", "x.jpg")); err != nil {
		t.Fatal(err)
	}
	g3 := *rec
	g3.Group = 3 // no config entry: group_N
	if _, err := BuildTree([]*Record{&g3}, filepath.Join(dir, "out2"), "copy", groups); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out2", "group_3", "focus_3_sharp", "rider_action", "full_body", "x.jpg")); err != nil {
		t.Fatal(err)
	}
}

func TestRatingMakesABanger(t *testing.T) {
	for rating, banger := range map[string]bool{"4": true, "3": false, "1": false, "4.0": true, "true": false} {
		rec, err := FinalRecord(Row{Path: "/a.jpg", LocalJSON: sp(`{"local_tier": 0, "n_people": 0, "people": []}`),
			OverrideJSON: sp(`{"rating": ` + rating + `, "reviewed": true}`)}, "vlm")
		if err != nil {
			t.Fatal(err)
		}
		if rec.Banger != banger || !rec.Reviewed {
			t.Errorf("rating %s: banger %v reviewed %v", rating, rec.Banger, rec.Reviewed)
		}
	}
}

func TestStaleModelTierFallsBackToLocal(t *testing.T) {
	// The final tier skips a verdict made on the pre-lift frame; a re-tag (stamped with the lift it saw) counts.
	local := sp(`{"local_tier": 3, "n_people": 0, "people": [], "exposure": {"ev": 4.0}}`)
	rec, err := FinalRecord(Row{Path: "/a.jpg", LocalJSON: local, VLMJSON: sp(`{"focus_tier": 0, "keeper": false}`)}, "vlm")
	if err != nil {
		t.Fatal(err)
	}
	if !py.Eq(rec.FocusTier, 3) || !py.Eq(rec.FocusTierVLM, 0) || rec.Review {
		t.Fatalf("stale: %s", py.Dumps(rec))
	}
	rec, err = FinalRecord(Row{Path: "/a.jpg", LocalJSON: local, VLMJSON: sp(`{"focus_tier": 1, "keeper": false, "seen_ev": 4.0}`)}, "vlm")
	if err != nil {
		t.Fatal(err)
	}
	if !py.Eq(rec.FocusTier, 1) || !rec.Review {
		t.Fatalf("re-tagged: %s", py.Dumps(rec))
	}
}

func TestVLMStale(t *testing.T) {
	// as tests/test_rating.py::test_lift_after_tagging_marks_verdict_stale_and_keeps_rating, on parsed columns
	cases := []struct {
		local, vlm any
		want       bool
	}{
		{pj.Obj{"local_tier": 0.0}, pj.Obj{"focus_tier": 0.0}, false},
		{pj.Obj{"exposure": pj.Obj{"ev": 4.0}}, pj.Obj{"focus_tier": 0.0}, true},
		{pj.Obj{"exposure": pj.Obj{"ev": 4.0}}, pj.Obj{"focus_tier": 3.0, "seen_ev": 4.0}, false},
		{nil, pj.Obj{"seen_ev": 0.0}, false},
		{nil, pj.Obj{"seen_ev": 1.5}, true},
		{pj.Obj{"exposure": pj.Obj{"ev": 4.0}}, nil, false},
		{pj.Obj{"exposure": pj.Obj{"ev": 4.0}}, pj.Obj{}, false},
		{py.NewObject("exposure", py.NewObject("ev", int64(2))), py.NewObject("seen_ev", 2.0), false},
	}
	for i, c := range cases {
		if got := VLMStale(c.local, c.vlm); got != c.want {
			t.Errorf("case %d: %v", i, got)
		}
	}
}

func TestFocusSources(t *testing.T) {
	row := Row{Path: "/a.jpg", LocalJSON: sp(`{"local_tier": 2, "n_people": 1}`), VLMJSON: sp(`{"focus_tier": 3}`)}
	for source, want := range map[string]int{"vlm": 3, "local": 2, "strict": 2, "anything": 3} {
		rec, err := FinalRecord(row, source)
		if err != nil {
			t.Fatal(err)
		}
		if !py.Eq(rec.FocusTier, want) {
			t.Errorf("%s: %v", source, rec.FocusTier)
		}
	}
}

func TestCopyKeepsTimesAndMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.jpg")
	if err := os.WriteFile(src, []byte("abc"), 0o640); err != nil {
		t.Fatal(err)
	}
	mt := time.Date(2020, 1, 2, 3, 4, 5, 123456789, time.UTC)
	if err := os.Chtimes(src, mt, mt); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "a", "b", "d.jpg")
	if err := os.MkdirAll(filepath.Dir(dst), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, dst); err != nil { // an existing entry is replaced, not written through
		t.Fatal(err)
	}
	if err := Place(src, dst, "copy"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(mt) || fi.Mode() != 0o640 || fi.Size() != 3 {
		t.Fatalf("copy: mtime %v mode %v size %d", fi.ModTime(), fi.Mode(), fi.Size())
	}
}

func TestMovePlacesAndLinksAside(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "p.jpg")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &Record{Path: src, FocusTier: int64(1), FocusTierLocal: int64(1), FocusTierVLM: int64(3), Review: true,
		Subject: "unknown", Composition: "unknown"}
	counts, err := BuildTree([]*Record{rec}, filepath.Join(dir, "out"), "move", nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.String() != "{'focus_1_soft': 1, 'review': 1}" {
		t.Fatal(counts)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "out", "focus_1_soft", "p.jpg")); err != nil || string(b) != "x" {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source still there")
	}
	fi, err := os.Lstat(filepath.Join(dir, "out", "review", "local1_vlm3", "p.jpg"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("review entry is not a symlink", err)
	}
}
