//go:build !unix

package compose

import "os"

// restoreOwner is a no-op off Unix: there is no uid/gid ownership to restore,
// and the containerized-root scenario it exists for is Unix-only.
func restoreOwner(string, os.FileInfo) error { return nil }
