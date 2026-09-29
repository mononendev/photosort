package export

import (
	"archive/zip"
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
)

func TestXMPCaptureOne(t *testing.T) {
	rec := &Record{Path: "/p/a.CR2", FocusTier: 3, FocusTierLocal: 2, FocusTierVLM: 3, Subject: "rider_action",
		Composition: "full_body", Action: "carving", Keywords: []any{"onewheel", "a|b"}, Adjectives: []any{"dynamic"},
		Description: "rider & trail", QualityScore: 4, Rating: 0, Keeper: true, Group: 1}
	groups := pj.Obj{"1": map[string]any{"keywords": []any{"Events|Float Life"}}}
	doc := XMPCaptureOne(rec, groups)
	wellFormed(t, doc)
	for _, want := range []string{`<?xml version="1.0" encoding="utf-8"?><x:xmpmeta`, "<xmp:Rating>4</xmp:Rating>",
		"<xmp:Label>Red</xmp:Label>", "<rdf:li>PhotoSort|Focus|focus_3_sharp</rdf:li>", "<rdf:li>PhotoSort|Action|carving</rdf:li>",
		"<rdf:li>PhotoSort|Content|onewheel</rdf:li>", "<rdf:li>PhotoSort|Content|a/b</rdf:li>",
		"<rdf:li>PhotoSort|Adjectives|dynamic</rdf:li>", "<rdf:li>Events|Float Life</rdf:li>", "rider &amp; trail",
		"<photosort:Keeper>true</photosort:Keeper>"} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in\n%s", want, doc)
		}
	}
	rec.Rating = nil
	if doc := XMPCaptureOne(rec, groups); !strings.Contains(doc, "<xmp:Label>Green</xmp:Label>") {
		t.Errorf("unrated tier 3 wants the Green label:\n%s", doc)
	}
	rec.Rating, rec.FocusTier = nil, 1
	if doc := XMPCaptureOne(rec, groups); !strings.Contains(doc, "<xmp:Label>Orange</xmp:Label>") {
		t.Errorf("unrated tier 1 wants the Orange label:\n%s", doc)
	}
	if strings.Contains(doc, "dc:subject") || strings.Contains(doc, "\n") {
		t.Errorf("want one line, keywords only in lr:hierarchicalSubject:\n%s", doc)
	}
}

func TestXMPZipLayout(t *testing.T) {
	rec := func(p string) *Record {
		return &Record{Path: p, FocusTier: 2, Subject: "rider_action", Composition: "full_body"}
	}
	raw := rec("/photos/Fest/2024/September/12/IMG_1.CR2")
	raw.FocusTier = 3
	recs := []*Record{
		rec("/photos/Fest/2024/September/12/IMG_1.JPG"), raw, // a pair: one sidecar, the raw's
		rec("/photos/Fest/2024/September/13/IMG_2.CR2"),
		rec("/photos/Fest/2024/top.CR2"),
		rec("/photos/Other/IMG_3.CR2"),                                                        // outside base
		{Path: "/photos/Fest/2024/September/14/untiered.CR2", Subject: "x", Composition: "y"}, // no tier yet
	}
	b, n, err := XMPZip(recs, "/photos/Fest/2024", FormatCaptureOne, nil)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	want := []string{"September/12/IMG_1.xmp", "September/13/IMG_2.xmp", "top.xmp"}
	if n != len(want) || !slices.Equal(names, want) {
		t.Fatalf("got %d %v, want %v", n, names, want)
	}
	f, _ := zr.File[0].Open()
	body, _ := io.ReadAll(f)
	wellFormed(t, string(body))
	if !strings.HasPrefix(string(body), "<?xml") || !strings.Contains(string(body), "focus_3_sharp") {
		t.Fatalf("want the raw's sidecar in the Capture One format: %s", body)
	}
	if _, _, err := XMPZip(recs, "/photos", "darktable", nil); err == nil {
		t.Fatal("want an error for an unknown format")
	}
}
