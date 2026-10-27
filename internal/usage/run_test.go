package usage

import (
	"context"
	"testing"
	"time"
)

func TestDailyMonths(t *testing.T) {
	for now, want := range map[time.Time]string{
		time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC):  "2026-09,2026-10",
		time.Date(2026, 10, 2, 7, 0, 0, 0, time.UTC):  "2026-09,2026-10",
		time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC):  "2026-10",
		time.Date(2026, 1, 1, 7, 0, 0, 0, time.UTC):   "2025-12,2026-01",
		time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC): "2026-03",
	} {
		got := DailyMonths(now)
		if s := join(got); s != want {
			t.Errorf("%s: %s, want %s", now, s, want)
		}
	}
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func TestDailyRun(t *testing.T) {
	us := fixtureUsers()
	for i := range us {
		us[i].Role = "bedrock-user-usage-" + string(rune('a'+i))
	}
	us = append(us, User{Email: "nobody@example.test"}) // no role: no file
	files := map[string]UserFile{}
	fleets := map[string]FleetUsage{}
	run := DailyRun{Account: acct, ID: "usage", Users: us,
		Source: exportDir(t, map[string]string{"2026-10": "cur-this-month.parquet"}),
		PutUsage: func(_ context.Context, key string, v any) error {
			files[key] = v.(UserFile)
			return nil
		},
		PutFleet: func(_ context.Context, month string, v FleetUsage) error {
			fleets[month] = v
			return nil
		},
		Now: time.Date(2026, 10, 2, 7, 0, 0, 0, time.UTC)}
	if err := run.Run(context.Background(), t.Logf); err != nil {
		t.Fatal(err)
	}
	if len(files) != 8 || len(fleets) != 2 {
		t.Fatalf("%d files, %d fleet usages", len(files), len(fleets))
	}
	a := files["bedrock-admin/usage/usage/bedrock-user-usage-a/2026-10.json"]
	if a.TotalUSD == 0 || len(a.Days) == 0 || a.Month != "2026-10" {
		t.Errorf("a: %+v", a)
	}
	if sep := files["bedrock-admin/usage/usage/bedrock-user-usage-a/2026-09.json"]; sep.TotalUSD != 0 || sep.Month != "2026-09" {
		t.Errorf("september (no export): %+v", sep)
	}
	if f := fleets["2026-10"]; f.Account != acct || f.ID != "usage" || len(f.Days) != 2 {
		t.Errorf("fleet: %+v", f)
	}
}
