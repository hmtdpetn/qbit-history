package store

import (
	"sync"
	"time"

	"qbit-history/internal/model"
)

const sampleCost = 96 // approximate in-memory bytes per queued sample

// Queue is the bounded in-memory buffer between collectors and the single
// database writer. When the byte budget is exhausted new samples are refused
// (the collector records a gap); when the oldest pending sample is older than
// MaxSpan the stale pending set is discarded so a stuck writer cannot grow it.
type Queue struct {
	mu                sync.Mutex
	pending, inflight map[int64][]model.Sample
	dead              map[int64]bool
	bytes             int64
	oldest            int64
	Dropped           int64
	MaxBytes          int64
	MaxSpan           int64
	now               func() int64
}

func NewQueue() *Queue {
	return &Queue{pending: map[int64][]model.Sample{}, dead: map[int64]bool{}, MaxBytes: 32 << 20, MaxSpan: 120000, now: func() int64 { return time.Now().UnixMilli() }}
}
func (q *Queue) Push(b model.Batch) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.dead[b.SeriesID] || len(b.Samples) == 0 {
		return false
	}
	now := q.now()
	if q.oldest > 0 && now-q.oldest > q.MaxSpan {
		for _, ps := range q.pending {
			q.Dropped += int64(len(ps))
		}
		q.pending = map[int64][]model.Sample{}
		q.bytes = 0
		q.oldest = 0
	}
	if q.bytes+int64(len(b.Samples))*sampleCost > q.MaxBytes {
		q.Dropped += int64(len(b.Samples))
		return false
	}
	if q.oldest == 0 {
		q.oldest = now
	}
	q.pending[b.SeriesID] = append(q.pending[b.SeriesID], b.Samples...)
	q.bytes += int64(len(b.Samples)) * sampleCost
	return true
}

// Invalidate drops queued data for a series and refuses further pushes (delete barrier).
func (q *Queue) Invalidate(sid int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dead[sid] = true
	q.bytes -= int64(len(q.pending[sid])) * sampleCost
	delete(q.pending, sid)
	delete(q.inflight, sid)
}

// Revive allows a series id to be queued again (new generation reuses a fresh id, so this is rarely needed).
func (q *Queue) Revive(sid int64) { q.mu.Lock(); delete(q.dead, sid); q.mu.Unlock() }

// Tail returns not-yet-committed samples for a series (in-flight and pending).
func (q *Queue) Tail(sid int64) []model.Sample {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.dead[sid] {
		return nil
	}
	out := append([]model.Sample{}, q.inflight[sid]...)
	return append(out, q.pending[sid]...)
}
func (q *Queue) Stats() (bytes, oldest, dropped int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.bytes, q.oldest, q.Dropped
}
func (q *Queue) Flush(s *Store) error {
	q.mu.Lock()
	if len(q.pending) == 0 {
		q.mu.Unlock()
		return nil
	}
	q.inflight = q.pending
	q.pending = map[int64][]model.Sample{}
	q.bytes = 0
	q.oldest = 0
	var batches []model.Batch
	for id, ps := range q.inflight {
		batches = append(batches, model.Batch{SeriesID: id, Samples: ps})
	}
	q.mu.Unlock()
	e := s.Append(batches)
	q.mu.Lock()
	defer q.mu.Unlock()
	if e != nil {
		for _, ps := range q.inflight {
			q.Dropped += int64(len(ps))
		}
	}
	q.inflight = nil
	return e
}
