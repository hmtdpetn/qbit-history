package collector

import "testing"

func TestExplicitRemovalNeedsFreshFullConfirmation(t *testing.T) {
	now := int64(1_000_000)
	l := NewLifecycle(now)
	l.ProtectedUntil = 0
	l.Missing("a", true, now)
	if len(l.Ready(now+9000, false)) != 0 {
		t.Fatal("must not delete without a fresh complete list")
	}
	if len(l.Ready(now+5000, true)) != 0 {
		t.Fatal("must wait at least ~8 s before confirming an explicit removal")
	}
	if !l.ConfirmationDue(now + 9000) {
		t.Fatal("confirmation should be due after 8 s")
	}
	if got := l.Ready(now+9000, true); len(got) != 1 || got[0] != "a" {
		t.Fatalf("expected a to be ready, got %v", got)
	}
}

func TestImplicitMissingNeedsThreeSpacedConfirmations(t *testing.T) {
	now := int64(1_000_000)
	l := NewLifecycle(now)
	l.ProtectedUntil = 0
	known := map[string]bool{"a": true, "b": true}
	present := map[string]bool{"b": true}
	l.ObserveFull(known, present, now)
	l.ObserveFull(known, present, now+5000) // too soon, does not count
	l.ObserveFull(known, present, now+31000)
	if len(l.Ready(now+31000, true)) != 0 {
		t.Fatal("two confirmations are not enough")
	}
	l.ObserveFull(known, present, now+62000)
	if got := l.Ready(now+62000, true); len(got) != 1 || got[0] != "a" {
		t.Fatalf("expected a ready after 3 confirmations spanning 62 s, got %v", got)
	}
	// Reappearance cancels the candidate.
	l.ObserveFull(known, map[string]bool{"a": true, "b": true}, now+63000)
	if len(l.Candidates) != 0 {
		t.Fatal("reappearing torrent must be cleared")
	}
}

func TestStartupProtectionBlocksDeletion(t *testing.T) {
	now := int64(1_000_000)
	l := NewLifecycle(now)
	l.Missing("a", true, now)
	if len(l.Ready(now+100000, true)) != 0 {
		t.Fatal("protected window must block confirmation")
	}
	if got := l.Ready(now+130000, true); len(got) != 1 {
		t.Fatalf("after protection expiry deletion should proceed, got %v", got)
	}
}

func TestBulkProtection(t *testing.T) {
	now := int64(1_000_000)
	l := NewLifecycle(now)
	l.ProtectedUntil = 0
	known := map[string]bool{}
	for i := 0; i < 50; i++ {
		known[string(rune('a'+i))] = true
	}
	present := map[string]bool{}
	for k := range known {
		if len(present) < 38 {
			present[k] = true
		}
	}
	// 12 of 50 missing >= max(10, ceil(20% of 50)=10) -> bulk.
	for _, dt := range []int64{0, 31000, 62000} {
		l.ObserveFull(known, present, now+dt)
	}
	if !l.Bulk {
		t.Fatal("bulk protection should be active")
	}
	if len(l.Ready(now+62000, true)) != 0 {
		t.Fatal("bulk-protected candidates must not be deleted")
	}
	l.Approved = true
	if len(l.Ready(now+62000, true)) != 12 {
		t.Fatal("after explicit approval all vanished torrents are ready")
	}
	empty := NewLifecycle(now)
	empty.ProtectedUntil = 0
	empty.ObserveFull(map[string]bool{"a": true, "b": true}, map[string]bool{}, now)
	if !empty.Bulk {
		t.Fatal("non-empty -> suddenly empty must trigger bulk protection")
	}
	for _, dt := range []int64{31000, 62000, 93000} {
		empty.ObserveFull(map[string]bool{"a": true, "b": true}, map[string]bool{}, now+dt)
	}
	if len(empty.Ready(now+93000, true)) != 0 {
		t.Fatal("repeated empty lists must not bypass bulk protection")
	}
}
