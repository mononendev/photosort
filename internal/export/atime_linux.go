package export

import (
	"os"
	"syscall"
	"time"
)

// atime is a file's last access time (copy2 carries it over), its mtime where the platform doesn't say.
func atime(fi os.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Atim.Unix())
	}
	return fi.ModTime()
}
