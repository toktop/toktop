//go:build windows

package collector

import "os"

// fileInode has no portable inode on Windows, so rotation detection there falls
// back to the (size, mtime) fingerprint. Returning 0 keeps Fingerprint equality
// reducing to size+mtime, matching the pre-inode behaviour on that platform and
// preserving single-binary cross-compilation.
func fileInode(info os.FileInfo) int64 { return 0 }
