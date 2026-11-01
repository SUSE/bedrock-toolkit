package admincli

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

// shareTable is a table of fleet report --format csv or markdown: plain values, no bars or marks.
type shareTable struct {
	Title  string
	Header []string
	Rows   [][]string
}

// sharing tells whether fleet report prints a view to share rather than for the terminal.
func (a *App) sharing() bool {
	return a.fleetFormat == "csv" || a.fleetFormat == "markdown"
}

// writeShare prints tables as csv (the first table only, for spreadsheets) or as markdown (the
// title, every table and the notes).
func (a *App) writeShare(title string, tables []shareTable, notes []string) error {
	if a.fleetFormat == "csv" {
		w := csv.NewWriter(a.Out)
		_ = w.Write(tables[0].Header)
		_ = w.WriteAll(tables[0].Rows)
		return w.Error()
	}
	a.printf("## %s\n", title)
	for _, t := range tables {
		if t.Title != "" {
			a.printf("\n### %s\n", t.Title)
		}
		a.printf("\n")
		writeMarkdownRow(a, t.Header)
		sep := make([]string, len(t.Header))
		for i := range sep {
			sep[i] = "---"
			if i > 0 && numeric(t.Rows, i) {
				sep[i] = "---:"
			}
		}
		writeMarkdownRow(a, sep)
		for _, r := range t.Rows {
			writeMarkdownRow(a, r)
		}
	}
	for i, n := range notes {
		if i == 0 {
			a.printf("\n")
		}
		a.printf("- %s\n", n)
	}
	return nil
}

func writeMarkdownRow(a *App, cells []string) {
	esc := make([]string, len(cells))
	for i, c := range cells {
		esc[i] = strings.ReplaceAll(c, "|", `\|`)
	}
	a.printf("| %s |\n", strings.Join(esc, " | "))
}

// numeric tells whether column i holds numbers (or nothing) in every non-empty cell, to right-align it.
func numeric(rows [][]string, i int) bool {
	found := false
	for _, r := range rows {
		if r[i] == "" {
			continue
		}
		if _, err := strconv.ParseFloat(r[i], 64); err != nil {
			return false
		}
		found = true
	}
	return found
}

func usd(v float64) string { return fmt.Sprintf("%.2f", v) }

func yes(b bool) string {
	if b {
		return "yes"
	}
	return ""
}
