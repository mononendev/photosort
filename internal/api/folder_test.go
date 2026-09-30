package api

import (
	"slices"
	"testing"
)

func TestFuzzyFolders(t *testing.T) {
	root := "/photos"
	all := []string{"/photos/2024/Jen_Wedding", "/photos/2024/Jen_Wedding/raw", "/photos/2023/Birthday", "/photos/2024/Hike"}
	for _, c := range []struct {
		q         string
		recursive bool
		want      []string
	}{
		{"wedding", true, all[:2]},
		{"wedding", false, all[:1]},
		{"JEN wedding", true, all[:2]},
		{"jen-wedding", false, all[:1]},
		{"2024", false, nil}, // only 2024 itself, which has no photos of its own
		{"2024", true, []string{"/photos/2024/Jen_Wedding", "/photos/2024/Jen_Wedding/raw", "/photos/2024/Hike"}},
		{"wed raw", false, all[1:2]},
		{"birth", true, all[2:3]},
		{"nope", true, nil},
	} {
		if got := fuzzyFolders(c.q, root, all, c.recursive); !slices.Equal(got, c.want) {
			t.Errorf("fuzzyFolders(%q, recursive=%v) = %v, want %v", c.q, c.recursive, got, c.want)
		}
	}
}
