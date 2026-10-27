package usage

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func at(day int, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC) }

func TestDaily(t *testing.T) {
	us := []User{
		{Email: "a@x.test", Product: "web", LimitUSD: 10},
		{Email: "b@x.test", Product: "data", LimitUSD: 20},
		{Email: "c@x.test", Product: "web", LimitUSD: 5},
	}
	lines := []Line{
		{Principal: sts("bedrock-user-p-a", "a@x.test"), Owner: "a@x.test", Cost: 2, Start: at(1, 3)},
		{Principal: sts("bedrock-user-p-a", "a@x.test"), Owner: "a@x.test", Cost: 1, Start: at(1, 9)},
		{Principal: sts("bedrock-user-p-b", "b@x.test"), Owner: "b@x.test", Cost: 4, Start: at(1, 9)},
		{Principal: sts("bedrock-user-p-b", "b@x.test"), Owner: "b@x.test", Cost: 0.004, Start: at(2, 1)},
		{Principal: "arn:aws:iam::111122223333:user/ci", Cost: 6, Start: at(2, 5)},
		{Principal: sts("bedrock-user-other-z", "z@x.test"), Owner: "z@x.test", Cost: 9, Start: at(2, 5)},
		{Principal: sts("bedrock-user-p-a", "a@x.test"), Owner: "a@x.test", Cost: 1.5, Start: at(3, 2)},
	}
	now := at(4, 12)
	r := Summarize("2026-10", "p", lines, us, now)
	d := Daily(r, DailySpend("p", lines, us), now)
	if len(d.Days) != 4 || d.Days[3].Date != "2026-10-04" {
		t.Fatalf("days %+v", d.Days)
	}
	d1, d2, d3, d4 := d.Days[0], d.Days[1], d.Days[2], d.Days[3]
	if d1.ActiveUsers != 2 || d1.TrackedUSD != 7 || d1.UntrackedUSD != 0 || d1.Partial {
		t.Errorf("day 1 %+v", d1)
	}
	if d2.ActiveUsers != 0 || d2.TrackedUSD != 0 || d2.UntrackedUSD != 6 || d2.Partial {
		t.Errorf("day 2 (other setup left out, b under $0.01) %+v", d2)
	}
	if !d3.Partial || !d4.Partial || d4.TrackedUSD != 0 {
		t.Errorf("days 3-4 should be partial: %+v %+v", d3, d4)
	}
	if d.ActiveUsers != 2 || d.Users != 3 || d.TrackedUSD != 8.5 || d.UntrackedUSD != 6 {
		t.Errorf("totals %+v", d)
	}

	var out bytes.Buffer
	d.Write(&out, Sections{Tracked: true, Untracked: true})
	s := out.String()
	for _, want := range []string{"DATE", "UNTRACKED", "2026-10-01   ", "2026-10-03*", "████████████████████", "Total", "14.50", "*: still growing", "(2 of 3 users)"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}

	f := Daily(r.Filter(Products{"web"}), DailySpend("p", lines, us), now)
	out.Reset()
	f.Write(&out, Sections{Tracked: true, Untracked: true})
	if f.Days[0].TrackedUSD != 3 || f.Days[0].ActiveUsers != 1 || f.UntrackedUSD != 0 || f.Days[1].UntrackedUSD != 0 ||
		strings.Contains(out.String(), "UNTRACKED") || !strings.Contains(out.String(), "Product web") {
		t.Errorf("%+v\n%s", f, out.String())
	}

	past := Daily(Summarize("2026-09", "p", nil, us, now), DailySpend("p", nil, us), now)
	if len(past.Days) != 30 || past.Days[29].Partial {
		t.Errorf("past month: %d days", len(past.Days))
	}
}

func TestBar(t *testing.T) {
	for _, c := range []struct {
		v, top float64
		want   string
	}{{10, 10, strings.Repeat("█", 20)}, {5, 10, strings.Repeat("█", 10)}, {0, 10, ""}, {0.001, 10, "▏"}, {1, 16, "█▎"}} {
		if got := Bar(c.v, c.top, 20); got != c.want {
			t.Errorf("Bar(%v, %v) = %q, want %q", c.v, c.top, got, c.want)
		}
	}
}
