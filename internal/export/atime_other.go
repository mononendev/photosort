//go:build !darwin && !linux

package export

import (
	"os"
	"time"
)

// atime falls back to the modification time where the platform's stat isn't read.
func atime(fi os.FileInfo) time.Time { return fi.ModTime() }
