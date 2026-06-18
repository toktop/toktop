// Package fsx holds tiny filesystem predicates shared across toktop. It exists so
// the "does this directory exist on disk" check has one implementation instead
// of a hand-rolled os.Stat+IsDir in every caller (cli, httpapi, diagnostics,
// ingest auto-detect).
package fsx

import (
	"os"
	"path/filepath"
	"strings"
)

// DirExists reports whether path exists and is a directory.
func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// FileExists reports whether path exists and is a regular file (not a directory).
// Used by the opencode collector to probe for its single opencode.db before
// opening it.
func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// PathWithin reports whether path is inside dir (or equal to it): the relative
// path from dir to path does not escape via "..". Callers pass already-cleaned
// paths. One definition for the per-provider transcript-root containment checks.
func PathWithin(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
