package runtime

import (
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"

	"toktop.unceas.dev/internal/diagnostics"
	"toktop.unceas.dev/internal/ingest"
)

type sourceWatcher struct {
	*fsnotify.Watcher
	watched map[string]bool
	// transcriptExts is the set of transcript file extensions of the watched
	// providers, used by ShouldIngest to pre-filter events. Provider-declared
	// (ingest.TranscriptExt) rather than a hardcoded ".jsonl", so the neutral
	// watcher carries no per-provider format knowledge.
	transcriptExts map[string]bool
}

func newSourceWatcher(sourceNames []string, rootsBySource map[string][]string) (*sourceWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	sw := &sourceWatcher{
		Watcher:        watcher,
		watched:        make(map[string]bool),
		transcriptExts: make(map[string]bool),
	}
	for _, sourceName := range sourceNames {
		if ext := ingest.TranscriptExt(sourceName); ext != "" {
			sw.transcriptExts[ext] = true
		}
		for _, dir := range WatchDirs(sourceName, rootsBySource[sourceName]) {
			if err := sw.addRecursive(dir); err != nil {
				_ = watcher.Close()
				return nil, err
			}
		}
	}
	return sw, nil
}

func WatchDirs(sourceName string, roots []string) []string {
	return diagnostics.SourceWatchDirs(sourceName, ingest.DiscoverRootPaths(sourceName, roots))
}

func (s *sourceWatcher) addRecursive(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if s.watched[path] {
			return nil
		}
		if err := s.Add(path); err != nil {
			return err
		}
		s.watched[path] = true
		return nil
	})
}

func (s *sourceWatcher) AddCreatedPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return s.addRecursive(path)
}

func (s *sourceWatcher) ShouldIngest(event fsnotify.Event) bool {
	if !s.transcriptExts[filepath.Ext(event.Name)] {
		return false
	}
	// Only content-producing ops ingest. Rename/Remove signal a vanished file, so
	// queuing them as a file job just hits os.Open(ErrNotExist) — a spurious
	// "daemon file ingest failed" warning + FileFailures bump on every routine log
	// rotation. Deleted rows are reconciled by the periodic full pass instead.
	return event.Op&(fsnotify.Create|fsnotify.Write) != 0
}
