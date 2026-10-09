package transport

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// developmentDir is $XDG_RUNTIME_DIR when it is usable, and the temporary
// directory otherwise. /tmp is shared by every user of a Linux machine, so a
// fixed name in it would be one socket for all of them; the runtime directory
// is the user's own.
func developmentDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); usableSocketDir(dir) {
		return dir
	}
	return os.TempDir()
}

// usableSocketDir reports whether a socket can live in dir and nobody else can
// replace it: an absolute path of a directory that belongs to the user this
// process runs as, that group and others cannot write to, and in which the
// socket's path fits in sun_path.
func usableSocketDir(dir string) bool {
	if !filepath.IsAbs(dir) || len(filepath.Join(dir, socketName)) >= len(unix.RawSockaddrUnix{}.Path) {
		return false
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o022 != 0 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}
