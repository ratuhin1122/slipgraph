package httpserver

import (
	"testing"
	"time"
)

func TestGlobalRateBudgetBoundedAndRefills(t *testing.T) {
	now := time.Unix(0, 0)
	l := newIPLimiter()
	l.now = func() time.Time { return now }
	l.burst = 2
	l.rate = 1
	l.max = 1
	if !l.allow("192.0.2.1:1") || !l.allow("192.0.2.1:2") || l.allow("192.0.2.1:3") {
		t.Fatal("port changes bypassed budget")
	}
	if l.allow("192.0.2.2:1") {
		t.Fatal("unbounded IP map")
	}
	now = now.Add(time.Second)
	if !l.allow("192.0.2.1:4") {
		t.Fatal("did not refill")
	}
	now = now.Add(2 * time.Minute)
	if !l.allow("192.0.2.2:1") || len(l.entries) != 1 {
		t.Fatal("expired keys not reclaimed")
	}
}
