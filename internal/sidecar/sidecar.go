// Package sidecar reads the photographer's own verdicts (star rating, color label) from existing Lightroom/Bridge
// XMP sidecars next to the images.
package sidecar

import (
	"os"
	"regexp"

	"github.com/mononendev/photosort/internal/py"
)

// The patterns match what Python's re did: \s is Python's (Unicode) whitespace and \d any decimal digit (Nd).
var (
	ratingRe = regexp.MustCompile(`xmp:Rating` + py.SpaceClass + `*=` + py.SpaceClass + `*"(-?\p{Nd})"|<xmp:Rating>` +
		py.SpaceClass + `*(-?\p{Nd})` + py.SpaceClass + `*</xmp:Rating>`)
	labelRe = regexp.MustCompile(`xmp:Label` + py.SpaceClass + `*=` + py.SpaceClass + `*"([^"]*)"|<xmp:Label>` +
		py.SpaceClass + `*([^<]*?)` + py.SpaceClass + `*</xmp:Label>`)
)

// RatingLabel reads xmp:Rating and xmp:Label from XMP text, in attribute or element form. nil for either when
// absent; an empty label counts as absent.
func RatingLabel(text string) (rating *int, label *string) {
	if m := ratingRe.FindStringSubmatch(text); m != nil {
		g := m[1]
		if g == "" {
			g = m[2]
		}
		n, _ := py.Int(g) // one optional minus and one digit: always parses
		r := int(n)
		rating = &r
	}
	if m := labelRe.FindStringSubmatchIndex(text); m != nil {
		var l string
		if m[2] >= 0 {
			l = text[m[2]:m[3]]
		} else {
			l = text[m[4]:m[5]]
		}
		if l != "" {
			label = &l
		}
	}
	return rating, label
}

// LR is one image's sidecar verdict, stored in lr_json.
type LR struct {
	Rating  *int
	Label   *string
	Sidecar string // the sidecar's file name
}

// PyObject is the verdict as the Python dict {"rating", "label", "sidecar"}.
func (l *LR) PyObject() *py.Object {
	var r, lab any
	if l.Rating != nil {
		r = *l.Rating
	}
	if l.Label != nil {
		lab = *l.Label
	}
	return py.NewObject("rating", r, "label", lab, "sidecar", l.Sidecar)
}

// JSON is the lr_json text the DB stores: json.dumps of the verdict, or "{}" for an image without a sidecar (so it
// isn't looked up again).
func (l *LR) JSON() string {
	if l == nil {
		return "{}"
	}
	return py.Dumps(l)
}

// SidecarFor finds the .xmp/.XMP next to path. siblings, the names in its folder, saves a stat per candidate; with
// nil siblings the file system is asked. "" when there is none.
func SidecarFor(path string, siblings map[string]bool) string {
	for _, cand := range []string{py.WithSuffix(path, ".xmp"), py.WithSuffix(path, ".XMP")} {
		var ok bool
		if siblings != nil {
			ok = siblings[py.Name(cand)]
		} else {
			_, err := os.Stat(cand)
			ok = err == nil
		}
		if ok {
			return cand
		}
	}
	return ""
}

// ReadSidecar reads path's sidecar: nil when there is none (or it can't be read), otherwise its rating and label.
// The text is read as Python's read_text(errors="ignore") would: invalid UTF-8 dropped, newlines universal.
func ReadSidecar(path string, siblings map[string]bool) *LR {
	sc := SidecarFor(path, siblings)
	if sc == "" {
		return nil
	}
	b, err := os.ReadFile(sc)
	if err != nil {
		return nil
	}
	rating, label := RatingLabel(py.UniversalNewlines(py.ValidUTF8(string(b))))
	return &LR{Rating: rating, Label: label, Sidecar: py.Name(sc)}
}

// Row is an image to look up.
type Row struct {
	ID   int64
	Path string
}

// Item is one image's result for the DB: LR nil when it has no sidecar.
type Item struct {
	ID int64
	LR *LR
}

// Ingest reads the sidecar verdict of each row (meant for rows whose lr_json is NULL) and hands them all to set,
// which stores them (lr_json = item.LR.JSON()). One directory listing per folder instead of two stats per image.
// Returns how many had a sidecar.
func Ingest(rows []Row, set func(items []Item) error) (int, error) {
	listings := map[string]map[string]bool{}
	siblings := func(folder string) map[string]bool {
		if l, ok := listings[folder]; ok {
			return l
		}
		l := map[string]bool{} // never nil: an unreadable folder has no sidecars, it isn't stat'ed
		if ents, err := os.ReadDir(folder); err == nil {
			for _, e := range ents {
				l[e.Name()] = true
			}
		}
		listings[folder] = l
		return l
	}
	found := make([]Item, 0, len(rows))
	n := 0
	for _, r := range rows {
		lr := ReadSidecar(r.Path, siblings(py.Parent(r.Path)))
		if lr != nil {
			n++
		}
		found = append(found, Item{ID: r.ID, LR: lr})
	}
	if err := set(found); err != nil {
		return 0, err
	}
	return n, nil
}
