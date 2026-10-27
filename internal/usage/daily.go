package usage

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

const (
	kindTracked = iota
	kindOtherID
	kindUntracked
)

// classify sorts a line: a user's spend (tracked, with the user), another setup's personal role, or
// untracked.
func (s userSet) classify(l Line, id string) (User, int) {
	if u, ok := s.owner(l); ok {
		return u, kindTracked
	}
	if other, _, ok := policy.SplitUserRole(principalRole(l.Principal)); ok && other != id {
		return User{}, kindOtherID
	}
	return User{}, kindUntracked
}

// Spend is the cost export's spend of one month, per user and day.
type Spend struct {
	// Users maps a user's lower-case email to their spend per date (YYYY-MM-DD).
	Users map[string]map[string]float64
	// Untracked is the spend outside personal roles per date.
	Untracked map[string]float64
	// Latest is the start of the newest line, zero without lines.
	Latest time.Time
}

// DailySpend adds up lines per user and day. Other setups' spend is left out.
func DailySpend(id string, lines []Line, users []User) Spend {
	set := newUserSet(users)
	s := Spend{Users: map[string]map[string]float64{}, Untracked: map[string]float64{}}
	for _, l := range lines {
		day := l.Start.UTC().Format("2006-01-02")
		if l.Start.After(s.Latest) {
			s.Latest = l.Start
		}
		u, kind := set.classify(l, id)
		switch kind {
		case kindTracked:
			k := strings.ToLower(u.Email)
			if s.Users[k] == nil {
				s.Users[k] = map[string]float64{}
			}
			s.Users[k][day] += l.Cost
		case kindUntracked:
			s.Untracked[day] += l.Cost
		}
	}
	return s
}

// Dates are the days of month, through now's day for the current month.
func Dates(month string, now time.Time) []string {
	start, err := MonthStart(month)
	if err != nil {
		return nil
	}
	end := pause.NextMonth(start)
	var out []string
	for d := start; d.Before(end) && !d.After(now.UTC()); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

// PartialFrom is the first day whose spend may still grow: the day of the newest line (billing lags up
// to a day), or today without lines. Days before it are complete. It is "" for a past month.
func PartialFrom(month string, latest, now time.Time) string {
	if now.UTC().Format("2006-01") != month {
		return ""
	}
	if latest.IsZero() || latest.UTC().Format("2006-01") != month {
		return now.UTC().Format("2006-01-02")
	}
	return latest.UTC().Format("2006-01-02")
}

// DayRow is one day of a month.
type DayRow struct {
	Date         string  `json:"date"`
	ActiveUsers  int     `json:"active_users"`
	TrackedUSD   float64 `json:"tracked_usd"`
	UntrackedUSD float64 `json:"untracked_usd"`
	Partial      bool    `json:"partial"`
}

// DayReport is spend per day of one month.
type DayReport struct {
	Month         string   `json:"month"`
	Current       bool     `json:"current"`
	Through       string   `json:"through,omitempty"`
	Note          string   `json:"note,omitempty"`
	ProductFilter []string `json:"product_filter,omitempty"`
	Days          []DayRow `json:"days"`
	// ActiveUsers is the users who spent at least MinUSD in the month.
	ActiveUsers  int     `json:"active_users"`
	Users        int     `json:"users"`
	TrackedUSD   float64 `json:"tracked_usd"`
	UntrackedUSD float64 `json:"untracked_usd"`
}

// Daily is r per day: users active each day (spent at least MinUSD that day), tracked and untracked
// spend. With r's product filter, only those products' users and no untracked spend.
func Daily(r MonthReport, s Spend, now time.Time) DayReport {
	d := DayReport{Month: r.Month, Current: r.Current, Through: r.Through, Note: r.Note, ProductFilter: r.ProductFilter,
		Days: []DayRow{}, ActiveUsers: r.ActiveUsers, Users: len(r.Users), TrackedUSD: r.TrackedUSD}
	partial := PartialFrom(r.Month, s.Latest, now)
	for _, date := range Dates(r.Month, now) {
		row := DayRow{Date: date, Partial: partial != "" && date >= partial}
		for _, u := range r.Users {
			v := s.Users[strings.ToLower(u.Email)][date]
			row.TrackedUSD += v
			if v >= MinUSD {
				row.ActiveUsers++
			}
		}
		if len(r.ProductFilter) == 0 {
			row.UntrackedUSD = round2(s.Untracked[date])
			d.UntrackedUSD += s.Untracked[date]
		}
		row.TrackedUSD = round2(row.TrackedUSD)
		d.Days = append(d.Days, row)
	}
	d.UntrackedUSD = round2(d.UntrackedUSD)
	return d
}

// Write prints one row per day with a bar of the day's total, scaled to the largest day.
func (d DayReport) Write(w io.Writer, s Sections) {
	if d.Current {
		through, _ := time.Parse("2006-01-02", d.Through)
		fmt.Fprintf(w, "%s (to %s, billing lags up to ~16 h)\n", d.Month, through.Format("Jan 2"))
	} else {
		fmt.Fprintf(w, "%s (%s)\n", d.Month, firstNonEmpty(d.Note, "final"))
	}
	if len(d.ProductFilter) > 0 {
		fmt.Fprintf(w, "Product %s\n", strings.Join(d.ProductFilter, ", "))
	}
	tracked, untracked := s.Tracked, s.Untracked && len(d.ProductFilter) == 0
	total := func(r DayRow) float64 {
		t := 0.0
		if tracked {
			t += r.TrackedUSD
		}
		if untracked {
			t += r.UntrackedUSD
		}
		return t
	}
	top, partial := 0.0, false
	for _, r := range d.Days {
		top = math.Max(top, total(r))
		partial = partial || r.Partial
	}
	head := []string{"DATE"}
	if tracked {
		head = append(head, "ACTIVE", "TRACKED")
	}
	if untracked {
		head = append(head, "UNTRACKED")
	}
	rows := [][]string{append(head, "TOTAL")}
	bars := []string{""}
	for _, r := range d.Days {
		date := r.Date
		if r.Partial {
			date += "*"
		}
		cols := []string{date}
		if tracked {
			cols = append(cols, fmt.Sprint(r.ActiveUsers), fmt.Sprintf("%.2f", r.TrackedUSD))
		}
		if untracked {
			cols = append(cols, fmt.Sprintf("%.2f", r.UntrackedUSD))
		}
		rows = append(rows, append(cols, fmt.Sprintf("%.2f", total(r))))
		bars = append(bars, Bar(total(r), top, 20))
	}
	cols := []string{"Total"}
	t := 0.0
	if tracked {
		cols = append(cols, fmt.Sprint(d.ActiveUsers), fmt.Sprintf("%.2f", d.TrackedUSD))
		t += d.TrackedUSD
	}
	if untracked {
		cols = append(cols, fmt.Sprintf("%.2f", d.UntrackedUSD))
		t += d.UntrackedUSD
	}
	rows = append(rows, append(cols, fmt.Sprintf("%.2f", round2(t))))
	bars = append(bars, "")
	fmt.Fprintln(w)
	WriteTable(w, rows, bars)
	if tracked {
		fmt.Fprintf(w, "ACTIVE: users who spent at least $0.01 that day; Total: in the month (%d of %d users)\n", d.ActiveUsers, d.Users)
	}
	if partial {
		fmt.Fprintln(w, "*: still growing; billing lags up to ~16 h")
	}
	if len(d.ProductFilter) > 0 {
		fmt.Fprintln(w, "Untracked spend has no product; run without --product to see it.")
	}
}

var eighths = []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}

// Bar is v as up to width full blocks, scaled so that top fills the width; any spend shows at least
// an eighth.
func Bar(v, top float64, width int) string {
	if v <= 0 || top <= 0 {
		return ""
	}
	n := int(math.Round(v / top * float64(width*8)))
	if n < 1 {
		n = 1
	}
	return strings.Repeat("█", n/8) + eighths[n%8]
}

// WriteTable prints rows with the first column left-aligned and the others right-aligned, then each
// row's bar.
func WriteTable(w io.Writer, rows [][]string, bars []string) {
	width := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			width[i] = max(width[i], len([]rune(c)))
		}
	}
	for n, r := range rows {
		var b strings.Builder
		for i, c := range r {
			pad := strings.Repeat(" ", width[i]-len([]rune(c)))
			if i == 0 {
				b.WriteString(c + pad)
			} else {
				b.WriteString("  " + pad + c)
			}
		}
		if bars[n] != "" {
			b.WriteString("  " + bars[n])
		}
		fmt.Fprintln(w, b.String())
	}
}
