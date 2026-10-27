package usage

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func filterReport() MonthReport {
	us := []User{
		{Email: "a@x.test", Product: "web", LimitUSD: 10},
		{Email: "b@x.test", Product: "web", LimitUSD: 20},
		{Email: "c@x.test", Product: "data", LimitUSD: 50},
		{Email: "d@x.test", LimitUSD: 5},
	}
	lines := []Line{
		{Principal: sts("bedrock-user-p-a", "a@x.test"), Owner: "a@x.test", Cost: 1},
		{Principal: sts("bedrock-user-p-b", "b@x.test"), Owner: "b@x.test", Cost: 0.004},
		{Principal: sts("bedrock-user-p-c", "c@x.test"), Owner: "c@x.test", Cost: 5},
		{Principal: "arn:aws:iam::111122223333:user/ci", Cost: 7},
	}
	return Summarize("2026-10", "p", lines, us, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
}

func TestActiveUsers(t *testing.T) {
	r := filterReport()
	if r.ActiveUsers != 2 {
		t.Errorf("active %d, want 2 (b spent under $0.01, d nothing)", r.ActiveUsers)
	}
	web := r.Products[2]
	if web.Product != "web" || web.Users != 2 || web.ActiveUsers != 1 {
		t.Errorf("web: %+v", web)
	}
	var out bytes.Buffer
	r.Write(&out, Sections{Tracked: true, ByProduct: true})
	for _, s := range []string{"ACTIVE", "1/2", "2/4"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("missing %q in\n%s", s, out.String())
		}
	}
	out.Reset()
	r.Write(&out, Sections{Tracked: true})
	if !strings.Contains(out.String(), "2 of 4 users active") {
		t.Errorf("no active line:\n%s", out.String())
	}
}

func TestFilter(t *testing.T) {
	r := filterReport()
	if !r.HasUntracked() {
		t.Fatal("fixture should have untracked spend")
	}
	f := r.Filter(Products{"web"})
	if len(f.Users) != 2 || f.TrackedUSD != 1 || f.ActiveUsers != 1 || f.HasUntracked() || f.UntrackedUSD != 0 ||
		len(f.Products) != 1 || f.Products[0].Product != "web" {
		t.Fatalf("%+v", f)
	}
	var out bytes.Buffer
	f.Write(&out, Sections{Tracked: true, Untracked: true})
	s := out.String()
	if !strings.Contains(s, "Product web") || !strings.Contains(s, "run without --product") ||
		strings.Contains(s, "c@x.test") || strings.Contains(s, "UNTRACKED") {
		t.Errorf("%s", s)
	}
	if g := r.Filter(Products{NoProduct}); len(g.Users) != 1 || g.Users[0].Email != "d@x.test" {
		t.Errorf("(none): %+v", g.Users)
	}
	if g := r.Filter(nil); !g.HasUntracked() || len(g.Users) != 4 {
		t.Error("an empty filter changed the report")
	}

	prev := f
	prev.Month = "2026-09"
	m := Combine([]MonthReport{prev, f})
	out.Reset()
	m.Write(&out, Sections{Tracked: true, Untracked: true, ByProduct: true})
	if len(m.Untracked) != 0 || !strings.Contains(out.String(), "1.00 (1)") || !strings.Contains(out.String(), "Product web") {
		t.Errorf("%+v\n%s", m, out.String())
	}
}

func TestCheckProducts(t *testing.T) {
	known := KnownProducts(filterReport())
	if err := (Products{"web", NoProduct}).Check(known); err != nil {
		t.Error(err)
	}
	err := Products{"mobile"}.Check(known)
	if err == nil || !strings.Contains(err.Error(), "known products: (none), data, web") {
		t.Errorf("%v", err)
	}
}
