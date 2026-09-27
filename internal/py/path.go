package py

import (
	"path/filepath"
	"strings"
)

// Name is PurePosixPath(p).name: the last component, ignoring empty and "." components; "" for "", "." and "/".
func Name(p string) string {
	parts := strings.Split(p, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" && parts[i] != "." {
			return parts[i]
		}
	}
	return ""
}

// Suffix is PurePath(p).suffix: from the last dot of the name, unless that dot starts or ends the name.
func Suffix(p string) string {
	name := Name(p)
	if i := strings.LastIndexByte(name, '.'); i > 0 && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

// Stem is PurePath(p).stem: the name without its suffix.
func Stem(p string) string {
	name := Name(p)
	return name[:len(name)-len(Suffix(p))]
}

// WithSuffix is PurePath(p).with_suffix(suffix): the same folder, the name's suffix replaced (or added).
func WithSuffix(p, suffix string) string {
	return Join(Parent(p), Stem(p)+suffix)
}

// Parent is PurePath(p).parent: the path without its last component ("." for a bare name).
func Parent(p string) string {
	abs, parts := split(p)
	if len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	return build(abs, parts)
}

// Join is `base / part / ...` on a pathlib.Path: an absolute part starts over from itself. Like pathlib (and unlike
// filepath.Join) it keeps ".." components, dropping only empty and "." ones, so the OS resolves them.
func Join(base string, parts ...string) string {
	out := base
	for _, p := range parts {
		if filepath.IsAbs(p) {
			out = p
		} else {
			out = out + "/" + p
		}
	}
	abs, ps := split(out)
	return build(abs, ps)
}

func split(p string) (abs bool, parts []string) {
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			parts = append(parts, s)
		}
	}
	return strings.HasPrefix(p, "/"), parts
}

func build(abs bool, parts []string) string {
	s := strings.Join(parts, "/")
	switch {
	case abs:
		return "/" + s
	case s == "":
		return "."
	}
	return s
}
