//go:build unix

package compose

import (
	"os"
	"syscall"
)

// restoreOwner chowns tmpName to orig's uid:gid when that differs from the
// current process's. See WriteFileAtomic for why (root-in-container write-back
// over a bind mount must not flip the host file to root:root).
func restoreOwner(tmpName string, orig os.FileInfo) error {
	st, ok := orig.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(st.Uid) == os.Getuid() && int(st.Gid) == os.Getgid() {
		return nil
	}
	return os.Chown(tmpName, int(st.Uid), int(st.Gid))
}
