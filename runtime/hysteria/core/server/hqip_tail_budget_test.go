package server

import (
	"testing"
	"time"
)

// An admission can succeed with a positive budget but still fail before the
// first ordinary payload is forwarded. Failed oversized payloads must not be
// silently charged just to make the subscription's displayed balance zero.
func TestHQIPTailBudgetRejectsOversizedPayloadWithoutCharging(t *testing.T) {
	l, closed := testLease(t, 478, 0, time.Minute)
	if err := l.consume(true, 512); err == nil {
		t.Fatal("payload exceeded the granted budget")
	}
	l.mu.Lock()
	used := l.usedUp + l.usedDown
	l.mu.Unlock()
	if used != 0 || closed.Load() != 1 {
		t.Fatalf("unforwarded payload charged or connection not closed: used=%d closed=%d", used, closed.Load())
	}
}

// The new-connection usability floor is a product policy, not a protocol
// minimum: an already-authorized connection may spend its final bytes.
func TestHQIPAuthorizedConnectionCanSpendExactTailBudget(t *testing.T) {
	l, closed := testLease(t, 478, 0, time.Minute)
	if err := l.consume(true, 1); err != nil {
		t.Fatal(err)
	}
	if err := l.consume(false, 477); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	upload, download := l.usedUp, l.usedDown
	l.mu.Unlock()
	if upload != 1 || download != 477 || closed.Load() != 0 {
		t.Fatalf("authorized tail was discarded: upload=%d download=%d closed=%d", upload, download, closed.Load())
	}
}
