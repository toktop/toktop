package runtime

import "time"

func (s *Service) setState(state State) {
	s.mu.Lock()
	s.prog.state = state
	s.mu.Unlock()
}

func (s *Service) setStartedAt(at time.Time) {
	s.mu.Lock()
	s.prog.startedAt = at
	s.mu.Unlock()
}

func (s *Service) setWatchedPaths(n int) {
	s.mu.Lock()
	s.prog.watchedPaths = n
	s.mu.Unlock()
}

func (s *Service) setPendingFiles(n int) {
	s.mu.Lock()
	s.prog.pendingFiles = n
	s.mu.Unlock()
}

func (s *Service) recordFull(reason string) {
	s.mu.Lock()
	s.prog.lastFullAt = time.Now().UTC()
	s.prog.lastFullReason = reason
	s.prog.counters.FullRuns++
	s.mu.Unlock()
}

func (s *Service) recordFile(path string) {
	s.mu.Lock()
	s.prog.lastFileAt = time.Now().UTC()
	s.prog.lastFilePath = path
	s.prog.counters.FileRuns++
	s.mu.Unlock()
}

func (s *Service) incrFullFailure() {
	s.mu.Lock()
	s.prog.counters.FullFailures++
	s.mu.Unlock()
}

func (s *Service) incrFileFailure() {
	s.mu.Lock()
	s.prog.counters.FileFailures++
	s.mu.Unlock()
}

func (s *Service) incrUnmapped() {
	s.mu.Lock()
	s.prog.counters.UnmappedFiles++
	s.mu.Unlock()
}

func (s *Service) claimUnmappedFullSlot(now time.Time, window time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.prog.lastUnmappedFullAt.IsZero() && now.Sub(s.prog.lastUnmappedFullAt) < window {
		return false
	}
	s.prog.lastUnmappedFullAt = now
	return true
}
