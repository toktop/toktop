//go:build !windows

package collector

import (
	"os"
	"syscall"
)

// fileInode returns the inode number from a stat result on unix-like systems.
// Folding it into the change-detection fingerprint lets rotation (a recreated
// file reusing the same path) be detected even when size and mtime coincide.
func fileInode(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
