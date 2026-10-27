package fleet

import (
	"context"
	"encoding/json"
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
