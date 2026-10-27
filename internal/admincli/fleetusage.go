package admincli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/fleet"
	"github.com/SUSE/bedrock-toolkit/internal/usage"
)

// fleetBarWidth is the width of the largest bar in fleet usage views.
const fleetBarWidth = 24

func checkFleetUsageFlags(by, product, month string, now time.Time) error {
	switch by {
	case "setup", "product", "day":
	default:
		return fmt.Errorf("--by must be setup, product or day (got %q)", by)
	}
	if by == "setup" && (product != "" || month != "") {
		return errors.New("--product and --month go with --by product or --by day")
	}
	if month != "" {
		if _, err := time.Parse("2006-01", month); err != nil {
			return fmt.Errorf("--month must be YYYY-MM (got %q)", month)
		}
		if month > now.Format("2006-01") {
			return fmt.Errorf("--month %s is in the future", month)
		}
	}
	return nil
}

// FleetUsageJSON is fleet report --by product or day as JSON.
type FleetUsageJSON struct {
	Month   string `json:"month"`
	Product string `json:"product,omitempty"`
	AsOf    string `json:"as_of,omitempty"`
	// Setups sent daily usage for the month; NoData didn't.
	Setups   []string             `json:"setups"`
	NoData   []string             `json:"no_daily_data"`
	Products []fleet.ProductUsage `json:"products,omitempty"`
	Days     []fleet.DayUsage     `json:"days,omitempty"`
}

// writeFleetUsage prints the setups' daily usage of month per product or per day.
func writeFleetUsage(a *App, by, product, month string, reports []fleet.Report, us map[string]*usage.FleetUsage, now time.Time) error {
	if product != "" && len(us) > 0 && !slices.Contains(fleet.UsageProducts(us), product) {
		return fmt.Errorf("no setup has users with product %s in %s; known products: %s", product, month,
			strings.Join(fleet.UsageProducts(us), ", "))
	}
	out := FleetUsageJSON{Month: month, Product: product, AsOf: fleet.UsageAsOf(us), Setups: []string{}, NoData: []string{}}
	for _, r := range reports {
		name := r.Account + "/" + r.ID
		if us[fleet.Key(r.Account, r.ID)] != nil {
			out.Setups = append(out.Setups, name)
		} else if r.Status == fleet.StatusActive {
			out.NoData = append(out.NoData, name)
		}
	}
	if by == "product" {
		out.Products = fleet.UsageByProduct(us, product)
	} else {
		out.Days = fleet.UsageByDay(us, month, product, now)
	}
	if a.JSON {
		return cli.WriteJSON(a.Out, out)
	}
	start, _ := time.Parse("2006-01", month)
	title := "Fleet usage in " + start.Format("January 2006")
	if product != "" {
		title += ", product " + product
	}
	asOf := "no daily data"
	if t, err := time.Parse(time.RFC3339, out.AsOf); err == nil {
		asOf = "cost export as of " + t.UTC().Format("Jan 2 15:04 MST")
	}
	a.printf("%s (%d setups, %s)\n", title, len(out.Setups), asOf)
	if len(us) == 0 {
		a.printf("No setup sent daily usage for %s yet: setups send it daily at 07:00 UTC once applied with this version.\n", month)
		writeNoData(a, out.NoData)
		return nil
	}
	if by == "product" {
		top, totalActive, totalOnboarded, total := 0.0, 0, 0, 0.0
		for _, p := range out.Products {
			top = max(top, p.TrackedUSD)
			totalActive += p.ActiveUsers
			totalOnboarded += p.Onboarded
			total += p.TrackedUSD
		}
		rows, bars := [][]string{{"PRODUCT", "SETUPS", "ACTIVE", "SPEND (USD)"}}, []string{""}
		for _, p := range out.Products {
			rows = append(rows, []string{p.Product, fmt.Sprint(p.Setups), fmt.Sprintf("%d/%d", p.ActiveUsers, p.Onboarded),
				fmt.Sprintf("%.2f", p.TrackedUSD)})
			bars = append(bars, usage.Bar(p.TrackedUSD, top, fleetBarWidth))
		}
		if product == "" {
			rows = append(rows, []string{"total", fmt.Sprint(len(out.Setups)), fmt.Sprintf("%d/%d", totalActive, totalOnboarded),
				fmt.Sprintf("%.2f", total)})
			bars = append(bars, "")
		}
		usage.WriteTable(a.Out, rows, bars)
		a.printf("ACTIVE: users who spent at least $0.01 in the month, of those onboarded. SPEND is tracked spend of personal roles.\n")
	} else {
		top, partial := 0.0, false
		for _, d := range out.Days {
			top = max(top, d.TrackedUSD)
		}
		rows, bars := [][]string{{"DATE", "ACTIVE", "SPEND (USD)"}}, []string{""}
		for _, d := range out.Days {
			mark := " "
			if d.Partial {
				mark, partial = "*", true
			}
			rows = append(rows, []string{d.Date, fmt.Sprint(d.ActiveUsers), fmt.Sprintf("%.2f", d.TrackedUSD) + mark})
			bars = append(bars, usage.Bar(d.TrackedUSD, top, fleetBarWidth))
		}
		usage.WriteTable(a.Out, rows, bars)
		a.printf("ACTIVE: users who spent at least $0.01 that day. SPEND is tracked spend of personal roles.\n")
		if partial {
			a.printf("* still being billed: AWS adds spend up to a day late.\n")
		}
	}
	writeNoData(a, out.NoData)
	return nil
}

func writeNoData(a *App, names []string) {
	if len(names) > 0 {
		a.printf("No daily data from %s (older version, reporting off, or the daily run hasn't happened yet).\n", strings.Join(names, ", "))
	}
}
