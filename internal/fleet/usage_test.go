package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/usage"
)

func fleetUsage(account, id, asOf string, products []usage.FleetProduct, days ...usage.FleetDay) *usage.FleetUsage {
	return &usage.FleetUsage{Schema: 1, Account: account, ID: id, Month: "2026-05", AsOf: asOf, Products: products, Days: days}
}

func TestReadUsage(t *testing.T) {
	s := store{}
	b, _ := json.Marshal(fleetUsage("1", "a", "2026-05-13T22:00:00Z", nil))
	s[UsageKey("1", "a", "2026-05")] = b
	s[UsageKey("2", "b", "2026-05")] = []byte("nope")
	reports := []Report{{Account: "1", ID: "a"}, {Account: "1", ID: "old"}}
	got, err := ReadUsage(context.Background(), s, "central", "2026-05", reports)
	if err != nil || len(got) != 1 || got["1/a.json"].ID != "a" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ReadUsage(context.Background(), s, "central", "2026-05", []Report{{Account: "2", ID: "b"}}); err == nil {
		t.Fatal("want a bad file to fail")
	}
}

func TestUsageAggregates(t *testing.T) {
	us := map[string]*usage.FleetUsage{
		"1/a.json": fleetUsage("1", "a", "2026-05-13T22:00:00Z",
			[]usage.FleetProduct{{Product: "rancher", ActiveUsers: 2, Onboarded: 3, TrackedUSD: 10}, {Product: "suma", ActiveUsers: 1, Onboarded: 1, TrackedUSD: 1}},
			usage.FleetDay{Date: "2026-05-01", ActiveUsers: 2, TrackedUSD: 4, Products: []usage.FleetProduct{{Product: "rancher", ActiveUsers: 1, TrackedUSD: 3}, {Product: "suma", ActiveUsers: 1, TrackedUSD: 1}}},
			usage.FleetDay{Date: "2026-05-13", ActiveUsers: 1, TrackedUSD: 7, Products: []usage.FleetProduct{{Product: "rancher", ActiveUsers: 1, TrackedUSD: 7}}}),
		"2/b.json": fleetUsage("2", "b", "2026-05-12T20:00:00Z",
			[]usage.FleetProduct{{Product: "rancher", ActiveUsers: 1, Onboarded: 4, TrackedUSD: 5}},
			usage.FleetDay{Date: "2026-05-01", ActiveUsers: 1, TrackedUSD: 5, Products: []usage.FleetProduct{{Product: "rancher", ActiveUsers: 1, TrackedUSD: 5}}}),
	}
	p := UsageByProduct(us, "")
	if len(p) != 2 || p[0] != (ProductUsage{"rancher", 2, 3, 7, 15}) || p[1].Product != "suma" {
		t.Fatalf("%+v", p)
	}
	if p = UsageByProduct(us, "suma"); len(p) != 1 || p[0].Setups != 1 {
		t.Fatalf("%+v", p)
	}
	now := time.Date(2026, 5, 14, 9, 0, 0, 0, time.UTC)
	d := UsageByDay(us, "2026-05", "", now)
	if len(d) != 14 || d[0] != (DayUsage{"2026-05-01", 3, 9, false}) || d[10].Partial || !d[11].Partial || d[11].Date != "2026-05-12" {
		t.Fatalf("partial from the oldest export's day: %+v", d)
	}
	if d = UsageByDay(us, "2026-05", "rancher", now); d[0].TrackedUSD != 8 || d[0].ActiveUsers != 2 || d[12].TrackedUSD != 7 {
		t.Fatalf("%+v", d)
	}
	if got := UsageProducts(us); len(got) != 2 || got[0] != "rancher" {
		t.Fatal(got)
	}
	if UsageAsOf(us) != "2026-05-12T20:00:00Z" {
		t.Fatal(UsageAsOf(us))
	}
}

func TestUsageByMonth(t *testing.T) {
	if got := LastMonths("2026-02", 3); strings.Join(got, ",") != "2025-12,2026-01,2026-02" {
		t.Fatal(got)
	}
	byMonth := map[string]map[string]*usage.FleetUsage{
		"2026-04": {"1/a.json": fleetUsage("1", "a", "", []usage.FleetProduct{{Product: "rancher", ActiveUsers: 1, Onboarded: 2, TrackedUSD: 3.5}})},
		"2026-05": {
			"1/a.json": fleetUsage("1", "a", "", []usage.FleetProduct{{Product: "rancher", ActiveUsers: 2, Onboarded: 3, TrackedUSD: 10}, {Product: "suma", ActiveUsers: 1, Onboarded: 1, TrackedUSD: 1}}),
			"2/b.json": fleetUsage("2", "b", "", []usage.FleetProduct{{Product: "suma", ActiveUsers: 1, Onboarded: 4, TrackedUSD: 5}}),
		},
	}
	now := time.Date(2026, 5, 14, 9, 0, 0, 0, time.UTC)
	m := UsageByMonth(byMonth, LastMonths("2026-05", 3), "", now)
	if len(m) != 3 || m[0] != (MonthUsage{Month: "2026-03"}) || m[1] != (MonthUsage{"2026-04", 1, 1, 2, 3.5, false}) ||
		m[2] != (MonthUsage{"2026-05", 2, 4, 8, 16, true}) {
		t.Fatalf("%+v", m)
	}
	if m = UsageByMonth(byMonth, []string{"2026-04", "2026-05"}, "suma", now); m[0] != (MonthUsage{Month: "2026-04"}) ||
		m[1] != (MonthUsage{"2026-05", 2, 2, 5, 6, true}) {
		t.Fatalf("product: %+v", m)
	}
}

func TestUseTrackedSpend(t *testing.T) {
	// The same person in two setups of one account: both budgets saw $3.65, the roles $3 and $0.65.
	at := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	asOf := "2026-05-19T05:00:00Z"
	reports := []Report{
		{Account: "1", ID: "old", Product: "sec", Month: "2026-05", SpendUSD: 3.65, ReportedAt: at,
			Products: []ProductSummary{{Product: "sec", SpendUSD: 3.65}, {Product: "qa", SpendUSD: 1}}},
		{Account: "1", ID: "new", Product: "sec", Month: "2026-05", SpendUSD: 3.65, ReportedAt: at, Products: []ProductSummary{{Product: "sec", SpendUSD: 3.65}}},
		{Account: "2", ID: "nousage", Month: "2026-05", SpendUSD: 7},
		{Account: "3", ID: "lastmonth", Month: "2026-04", SpendUSD: 9, ReportedAt: at},
		{Account: "4", ID: "stale", Month: "2026-05", SpendUSD: 900, ReportedAt: at},
	}
	us := map[string]*usage.FleetUsage{
		"1/old.json":       {Month: "2026-05", AsOf: asOf, Products: []usage.FleetProduct{{Product: usage.NoProduct, TrackedUSD: 2}, {Product: "sec", TrackedUSD: 1}}},
		"1/new.json":       {Month: "2026-05", AsOf: asOf, Products: []usage.FleetProduct{{Product: "sec", TrackedUSD: 0.4}, {Product: "moved", TrackedUSD: 0.25}}},
		"3/lastmonth.json": {Month: "2026-05", AsOf: asOf, Products: []usage.FleetProduct{{Product: "x", TrackedUSD: 1}}},
		// The cost export stopped on May 3: the file has only the first days.
		"4/stale.json": {Month: "2026-05", AsOf: "2026-05-03T18:00:00Z", Products: []usage.FleetProduct{{Product: "x", TrackedUSD: 24}}},
	}
	UseTrackedSpend(reports, us)
	old, new := reports[0], reports[1]
	if old.SpendUSD != 3 || old.SpendFrom != SpendFromUsage || old.Products[0] != (ProductSummary{Product: "sec", SpendUSD: 3}) ||
		old.Products[1].SpendUSD != 0 {
		t.Errorf("old: %+v", old)
	}
	if new.SpendUSD != 0.65 || len(new.Products) != 2 || new.Products[0].SpendUSD != 0.4 || new.Products[1] != (ProductSummary{Product: "moved", SpendUSD: 0.25}) {
		t.Errorf("new: %+v", new)
	}
	if reports[2].SpendUSD != 7 || reports[2].SpendFrom != "" || reports[3].SpendUSD != 9 || reports[3].SpendFrom != "" ||
		reports[4].SpendUSD != 900 || reports[4].SpendFrom != "" {
		t.Errorf("without a current usage file of the report's month, budgets' spend stays: %+v", reports[2:])
	}
}
