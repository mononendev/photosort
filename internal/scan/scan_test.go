package scan

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFindImages(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.jpg", "a.CR2", "b.CR2", "c.txt", ".hidden.jpg", "sub/d.HEIC", "sub/e.nef"} {
		p := filepath.Join(root, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, nil, 0o644)
	}
	rel := func(fs []string) []string {
		var out []string
		for _, f := range fs {
			r, _ := filepath.Rel(root, f)
			out = append(out, r)
		}
		slices.Sort(out)
		return out
	}
	all := rel(FindImages([]string{root}, false))
	if !slices.Equal(all, []string{"a.CR2", "a.jpg", "b.CR2", "sub/d.HEIC", "sub/e.nef"}) {
		t.Errorf("all: %v", all)
	}
	dedup := rel(FindImages([]string{root}, true))
	if !slices.Equal(dedup, []string{"a.jpg", "b.CR2", "sub/d.HEIC", "sub/e.nef"}) {
		t.Errorf("dedup: %v", dedup)
	}
	one := rel(FindImages([]string{filepath.Join(root, "b.CR2"), filepath.Join(root, "c.txt"), filepath.Join(root, "nope.jpg")}, true))
	if !slices.Equal(one, []string{"b.CR2"}) {
		t.Errorf("files: %v", one)
	}
}
