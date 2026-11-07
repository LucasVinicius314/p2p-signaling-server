package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterRefillsAndIsPerKey(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.Allow("a", 3) {
			t.Fatalf("event %d should pass", i)
		}
	}
	if l.Allow("a", 3) {
		t.Fatal("4th event within the minute should be limited")
	}
	if !l.Allow("b", 3) {
		t.Fatal("other keys are independent")
	}
	now = now.Add(20 * time.Second) // 3/min refills one token per 20s
	if !l.Allow("a", 3) {
		t.Fatal("a token should have refilled")
	}
	if l.Allow("a", 3) {
		t.Fatal("only one token refilled")
	}
}

func TestLimiterEvictsIdleKeys(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	l.Allow("a", 5)
	now = now.Add(10 * time.Minute)
	l.Allow("b", 5) // triggers gc
	if _, ok := l.m["a"]; ok {
		t.Fatal("idle key should have been evicted")
	}
}

func TestLockout(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLockout()
	l.now = func() time.Time { return now }
	const lock = 5 * time.Minute

	for i := 0; i < 4; i++ {
		if l.Fail("ip", 5, lock) {
			t.Fatalf("failure %d should not lock yet", i+1)
		}
	}
	if l.Locked("ip") {
		t.Fatal("not locked before threshold")
	}
	if !l.Fail("ip", 5, lock) {
		t.Fatal("5th failure should lock")
	}
	if !l.Locked("ip") || l.Locked("other") {
		t.Fatal("lock applies to the key only")
	}
	now = now.Add(lock + time.Second)
	if l.Locked("ip") {
		t.Fatal("lock should expire")
	}
}

func TestLockoutFailuresAgeOut(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLockout()
	l.now = func() time.Time { return now }
	const lock = time.Minute
	for i := 0; i < 4; i++ {
		l.Fail("ip", 5, lock)
	}
	now = now.Add(2 * lock) // the old failures no longer count
	if l.Fail("ip", 5, lock) {
		t.Fatal("aged-out failures must not contribute")
	}
}
