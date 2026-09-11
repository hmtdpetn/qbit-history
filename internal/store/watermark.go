package store

func (s *Store) CommitWatermark() int64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.LastCommit }
func (s *Store) PersistStatus() bool   { s.mu.RLock(); defer s.mu.RUnlock(); return s.Paused }
