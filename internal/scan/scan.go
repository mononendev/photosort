// Package scan finds image files.
package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RawExt are the camera RAW extensions photosort reads (through the embedded preview).
var RawExt = set(".arw", ".cr2", ".cr3", ".nef", ".nrw", ".dng", ".raf", ".orf", ".rw2", ".pef", ".srw", ".3fr", ".iiq")

// ImgExt are the other image extensions.
var ImgExt = set(".jpg", ".jpeg", ".png", ".heic", ".heif", ".tif", ".tiff", ".webp")

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// IsRaw reports a RAW file by extension.
func IsRaw(p string) bool { return RawExt[strings.ToLower(filepath.Ext(p))] }

// IsImage reports a file photosort reads, by extension; dotfiles never count.
func IsImage(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return (RawExt[ext] || ImgExt[ext]) && !strings.HasPrefix(filepath.Base(name), ".")
}

// FindImages lists the image files among paths and under the folders in it. With skipRawDupes, a RAW whose stem also
// has a JPEG/HEIC/... next to it is dropped (the other decodes faster). WalkDir reads file types from the directory
// listing, so a network mount isn't stat'ed once per file.
func FindImages(paths []string, skipRawDupes bool) []string {
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() && IsImage(d.Name()) {
					files = append(files, path)
				}
				return nil
			})
		} else if IsImage(p) {
			files = append(files, p)
		}
	}
	if !skipRawDupes {
		return files
	}
	stems := map[string]bool{}
	for _, f := range files {
		if !IsRaw(f) {
			stems[strings.TrimSuffix(f, filepath.Ext(f))] = true
		}
	}
	out := files[:0]
	for _, f := range files {
		if !IsRaw(f) || !stems[strings.TrimSuffix(f, filepath.Ext(f))] {
			out = append(out, f)
		}
	}
	return out
}
