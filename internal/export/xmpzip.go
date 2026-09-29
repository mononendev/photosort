package export

import (
	"archive/zip"
	"bytes"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

// XMP sidecar formats.
const (
	FormatLightroom  = "lightroom"
	FormatCaptureOne = "capture_one"
)

// XMPFormat renders a record's sidecar in the given format ("" is Lightroom).
func XMPFormat(r *Record, groups pj.Obj, format string) (string, error) {
	switch format {
	case "", FormatLightroom:
		return XMPFor(r, groups), nil
	case FormatCaptureOne:
		return XMPCaptureOne(r, groups), nil
	}
	return "", fmt.Errorf("unknown XMP format %q (want %s or %s)", format, FormatLightroom, FormatCaptureOne)
}

// hierPart makes a model keyword safe as one level of a hierarchical keyword.
func hierPart(v any) string { return strings.TrimSpace(strings.ReplaceAll(py.Str(v), "|", "/")) }

// XMPCaptureOne renders a record's sidecar the way Capture One imports it: one line, the properties as elements
// rather than attributes, and every keyword under lr:hierarchicalSubject only (Capture One reads dc:subject as well
// and would list each keyword twice). The keywords are XMPFor's hierarchy plus the model's keywords and adjectives
// under PhotoSort|Content and PhotoSort|Adjectives; stars, description and the photosort: fields as in XMPFor. The
// color label is your rating's, or on a photo you haven't rated, its focus tier's.
func XMPCaptureOne(r *Record, groups pj.Obj) string {
	_, hier := photosortTags(r, groups)
	if py.Truthy(r.Action) && !py.Eq(r.Action, "none") {
		hier = append(hier, "PhotoSort|Action|"+py.Str(r.Action))
	}
	for _, kw := range list(r.Keywords) {
		if k := hierPart(kw); k != "" {
			hier = append(hier, "PhotoSort|Content|"+k)
		}
	}
	for _, a := range list(r.Adjectives) {
		if k := hierPart(a); k != "" {
			hier = append(hier, "PhotoSort|Adjectives|"+k)
		}
	}

	var b strings.Builder
	el := func(name string, v any) {
		if v != nil {
			b.WriteString("<" + name + ">" + escText.Replace(py.Str(v)) + "</" + name + ">")
		}
	}
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/" xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:RDF>`)
	b.WriteString(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"` +
		` xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:lr="http://ns.adobe.com/lightroom/1.0/"` +
		` xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/" xmlns:photosort="http://photosort.local/ns/1.0/">`)
	if py.Truthy(r.QualityScore) {
		el("xmp:Rating", r.QualityScore)
	}
	label, ok := py.IntKey(r.Rating)
	if !ok { // unrated: the focus tier's label (tiers 0-3 share the matching rating's)
		label, ok = py.IntKey(r.FocusTier)
	}
	if l, known := RatingLabels[label]; ok && known {
		el("xmp:Label", l)
	}
	b.WriteString("<lr:hierarchicalSubject><rdf:Bag>")
	for _, h := range hier {
		el("rdf:li", h)
	}
	b.WriteString("</rdf:Bag></lr:hierarchicalSubject>")
	if d := py.Str(py.Or(r.Description, "")); d != "" {
		b.WriteString(`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">` + escText.Replace(d) + `</rdf:li></rdf:Alt></dc:description>`)
	}
	var notes []string
	for _, x := range []any{r.FocusNotes, r.QualityRemarks} {
		if py.Truthy(x) {
			notes = append(notes, py.Str(x))
		}
	}
	if s := strings.Join(notes, " "); s != "" {
		el("photoshop:Instructions", s)
	}
	el("photosort:FocusTier", r.FocusTier)
	el("photosort:FocusTierLocal", r.FocusTierLocal)
	el("photosort:FocusTierVLM", r.FocusTierVLM)
	el("photosort:QualityScore", r.QualityScore)
	el("photosort:Rating", r.Rating)
	if r.Keeper != nil {
		el("photosort:Keeper", py.Lower(py.Str(r.Keeper)))
	}
	b.WriteString("</rdf:Description></rdf:RDF></x:xmpmeta>")
	return b.String()
}

// rendered is a raw and a JPEG shot as one pair: when both would get the same <stem>.xmp, the raw's wins.
var rendered = map[string]bool{".jpg": true, ".jpeg": true, ".heic": true, ".heif": true, ".png": true, ".tif": true, ".tiff": true, ".webp": true}

// XMPZip is a zip of a sidecar per record with a tier, each at its image's folder relative to base with the name
// <stem>.xmp (the image's suffix dropped, as Capture One and Lightroom look for it): unzipped into base's folder,
// every sidecar lands next to its image. Records outside base are left out. Returns the zip and how many sidecars
// it holds.
func XMPZip(records []*Record, base, format string, groups pj.Obj) ([]byte, int, error) {
	type entry struct {
		name string
		r    *Record
	}
	var entries []entry
	at := map[string]int{}
	for _, r := range records {
		if r.FocusTier == nil {
			continue
		}
		rel, err := filepath.Rel(base, r.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		name := path.Join(filepath.ToSlash(filepath.Dir(rel)), py.Stem(r.Path)+".xmp")
		if i, ok := at[name]; ok {
			if rendered[strings.ToLower(py.Suffix(r.Path))] {
				continue
			}
			entries[i].r = r
			continue
		}
		at[name] = len(entries)
		entries = append(entries, entry{name, r})
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	now := time.Now()
	for _, e := range entries {
		x, err := XMPFormat(e.r, groups, format)
		if err != nil {
			return nil, 0, err
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: now})
		if err != nil {
			return nil, 0, err
		}
		if _, err := w.Write([]byte(x)); err != nil {
			return nil, 0, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), len(entries), nil
}
