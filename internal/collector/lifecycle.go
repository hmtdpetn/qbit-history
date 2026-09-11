package collector

import (
	"math"
	"time"
)

type Candidate struct {
	First, Last int64
	Count       int
	Explicit    bool
}
type Lifecycle struct {
	Candidates     map[string]Candidate
	ProtectedUntil int64
	Bulk           bool
	Approved       bool
}

func NewLifecycle(now int64) Lifecycle {
	return Lifecycle{Candidates: map[string]Candidate{}, ProtectedUntil: now + 120000}
}
func (l *Lifecycle) Protect(now int64) { l.ProtectedUntil = now + 120000 }
func (l *Lifecycle) Missing(key string, explicit bool, now int64) {
	c, ok := l.Candidates[key]
	if !ok {
		c = Candidate{First: now, Last: now, Count: 1}
	} else if now-c.Last >= 30000 {
		c.Last = now
		c.Count++
	}
	c.Explicit = c.Explicit || explicit
	l.Candidates[key] = c
}
func (l *Lifecycle) ObserveFull(known, present map[string]bool, now int64) {
	missing := 0
	for k := range known {
		if !present[k] {
			missing++
			l.Missing(k, false, now)
		} else {
			delete(l.Candidates, k)
		}
	}
	threshold := int(math.Max(10, math.Ceil(float64(len(known))*.2)))
	if missing >= threshold || len(known) > 0 && len(present) == 0 {
		l.Bulk = true
	}
	if len(l.Candidates) == 0 {
		l.Bulk = false
		l.Approved = false
	}
}
func (l *Lifecycle) Ready(now int64, freshComplete bool) []string {
	if !freshComplete || l.Bulk && !l.Approved || now < l.ProtectedUntil && !l.Approved {
		return nil
	}
	var out []string
	for k, c := range l.Candidates {
		if l.Approved || c.Explicit && now-c.First >= 8000 || !c.Explicit && c.Count >= 3 && c.Last-c.First >= 60000 {
			out = append(out, k)
		}
	}
	return out
}
func (l *Lifecycle) ConfirmationDue(now int64) bool {
	for _, c := range l.Candidates {
		if c.Explicit && now-c.First >= 8000 && now-c.Last >= 8000 || !c.Explicit && now-c.Last >= 30000 {
			return true
		}
	}
	return l.Approved
}
func millis(t time.Time) int64 { return t.UTC().UnixMilli() }
