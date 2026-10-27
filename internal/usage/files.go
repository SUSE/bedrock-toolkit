package usage

import (
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// UsagePrefix is where a personal role's daily usage files live in the cost export bucket; the role
// may read only its own prefix.
func UsagePrefix(id, role string) string { return policy.UserUsagePrefix(id, role) }

// UsageKey is a personal role's usage file of one month.
func UsageKey(id, role, month string) string { return UsagePrefix(id, role) + month + ".json" }

// UserFile is one user's spend of one month, per day and model, written daily by the Lambda so the
// user's own role can read it (users can't read the cost export).
type UserFile struct {
	Month      string   `json:"month"`
	UpdatedAt  string   `json:"updated_at"`
	ExportAsOf string   `json:"export_as_of,omitempty"`
	Role       string   `json:"role"`
	Days       []DayUSD `json:"days"`
	Models     []Amount `json:"models"`
	TotalUSD   float64  `json:"total_usd"`
}

// DayUSD is the spend of one day.
type DayUSD struct {
	Date string  `json:"date"`
	USD  float64 `json:"usd"`
}

// UserFiles builds every user's file of month, also for users without spend. Days list only days
// with spend.
func UserFiles(month, id string, lines []Line, users []User, now time.Time) []UserFile {
	set := newUserSet(users)
	type acc struct{ days, models map[string]float64 }
	by := map[string]*acc{}
	var latest time.Time
	for _, l := range lines {
		if l.Start.After(latest) {
			latest = l.Start
		}
		u, kind := set.classify(l, id)
		if kind != kindTracked {
			continue
		}
		k := strings.ToLower(u.Email)
		if by[k] == nil {
			by[k] = &acc{map[string]float64{}, map[string]float64{}}
		}
		by[k].days[l.Start.UTC().Format("2006-01-02")] += l.Cost
		by[k].models[l.Model] += l.Cost
	}
	asOf := ""
	if !latest.IsZero() {
		asOf = latest.UTC().Format(time.RFC3339)
	}
	var out []UserFile
	for _, u := range users {
		f := UserFile{Month: month, UpdatedAt: now.UTC().Format(time.RFC3339), ExportAsOf: asOf, Role: u.Role,
			Days: []DayUSD{}, Models: []Amount{}}
		if a := by[strings.ToLower(u.Email)]; a != nil {
			for _, d := range slices.Sorted(maps.Keys(a.days)) {
				f.Days = append(f.Days, DayUSD{d, round2(a.days[d])})
				f.TotalUSD += a.days[d]
			}
			for m, v := range a.models {
				f.Models = append(f.Models, Amount{m, round2(v)})
			}
			sort.Slice(f.Models, func(i, j int) bool { return f.Models[i].USD > f.Models[j].USD })
		}
		f.TotalUSD = round2(f.TotalUSD)
		out = append(out, f)
	}
	return out
}

// FleetUsageSchema is the version of FleetUsage.
const FleetUsageSchema = 1

// FleetUsage is a setup's tracked spend of one month per day and product, without emails, written
// daily by the Lambda to the central report bucket.
type FleetUsage struct {
	Schema   int            `json:"schema"`
	Account  string         `json:"account"`
	ID       string         `json:"id"`
	Month    string         `json:"month"`
	AsOf     string         `json:"as_of,omitempty"`
	Days     []FleetDay     `json:"days"`
	Products []FleetProduct `json:"products"`
}

// FleetDay is one day of a setup.
type FleetDay struct {
	Date        string         `json:"date"`
	ActiveUsers int            `json:"active_users"`
	TrackedUSD  float64        `json:"tracked_usd"`
	Products    []FleetProduct `json:"products"`
}

// FleetProduct is one product's users and spend, in a month or a day (Onboarded only in a month).
type FleetProduct struct {
	Product     string  `json:"product"`
	ActiveUsers int     `json:"active_users"`
	Onboarded   int     `json:"onboarded,omitempty"`
	TrackedUSD  float64 `json:"tracked_usd"`
}

// BuildFleetUsage sums lines per day and product for the central report. Active means at least MinUSD
// that day (per day) or that month (per product).
func BuildFleetUsage(account, id, month string, lines []Line, users []User, now time.Time) FleetUsage {
	s := DailySpend(id, lines, users)
	f := FleetUsage{Schema: FleetUsageSchema, Account: account, ID: id, Month: month, Days: []FleetDay{}, Products: []FleetProduct{}}
	if !s.Latest.IsZero() {
		f.AsOf = s.Latest.UTC().Format(time.RFC3339)
	}
	products := map[string]*FleetProduct{}
	for _, u := range users {
		p := u.Product
		if p == "" {
			p = NoProduct
		}
		fp := products[p]
		if fp == nil {
			fp = &FleetProduct{Product: p}
			products[p] = fp
		}
		fp.Onboarded++
		total := 0.0
		for _, v := range s.Users[strings.ToLower(u.Email)] {
			total += v
		}
		fp.TrackedUSD += total
		if total >= MinUSD {
			fp.ActiveUsers++
		}
	}
	for _, p := range slices.Sorted(maps.Keys(products)) {
		fp := *products[p]
		fp.TrackedUSD = round2(fp.TrackedUSD)
		f.Products = append(f.Products, fp)
	}
	for _, date := range Dates(month, now) {
		d := FleetDay{Date: date, Products: []FleetProduct{}}
		day := map[string]*FleetProduct{}
		for _, u := range users {
			v := s.Users[strings.ToLower(u.Email)][date]
			if v == 0 {
				continue
			}
			p := u.Product
			if p == "" {
				p = NoProduct
			}
			if day[p] == nil {
				day[p] = &FleetProduct{Product: p}
			}
			day[p].TrackedUSD += v
			d.TrackedUSD += v
			if v >= MinUSD {
				day[p].ActiveUsers++
				d.ActiveUsers++
			}
		}
		for _, p := range slices.Sorted(maps.Keys(day)) {
			fp := *day[p]
			fp.TrackedUSD = round2(fp.TrackedUSD)
			d.Products = append(d.Products, fp)
		}
		d.TrackedUSD = round2(d.TrackedUSD)
		f.Days = append(f.Days, d)
	}
	return f
}
