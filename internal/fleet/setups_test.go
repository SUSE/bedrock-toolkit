package fleet

import (
	"testing"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/pause"
)

func TestIdle(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, c := range []struct {
		name              string
		created, lastUsed time.Time
		want              bool
	}{
		{"new, never used", now.Add(-10 * day), time.Time{}, false},
		{"old, never used", now.Add(-40 * day), time.Time{}, true},
		{"old, used lately", now.Add(-90 * day), now.Add(-2 * day), false},
		{"old, used long ago", now.Add(-90 * day), now.Add(-31 * day), true},
		{"unknown creation, never used", time.Time{}, time.Time{}, false},
	} {
		if got := Idle(c.created, c.lastUsed, now); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	last := now.Add(-48 * time.Hour)
	us := []User{
		{Email: "a@example.com", Product: "rancher", LastUsed: last, SpendUSD: 10.111, LimitUSD: 100, State: pause.Armed},
		{Email: "b@example.com", Product: "rancher", SpendUSD: 5.5, LimitUSD: 50, State: pause.Paused},
		{Email: "c@example.com", Product: "harvester", LimitUSD: 50},
	}
	s := Summarize(SetupInfo{ID: "team1", Created: now.Add(-100 * 24 * time.Hour)}, us, now)
	if s.ID != "team1" || s.Product != "rancher" || s.Users != 3 || s.Paused != 1 || !s.LastUsed.Equal(last) ||
		s.SpendUSD != 15.61 || s.LimitUSD != 200 || s.Idle {
		t.Fatalf("got %+v", s)
	}
	empty := Summarize(SetupInfo{ID: "old", Created: now.Add(-100 * 24 * time.Hour)}, nil, now)
	if empty.Users != 0 || empty.Product != "" || !empty.Idle {
		t.Fatalf("empty: %+v", empty)
	}
}
