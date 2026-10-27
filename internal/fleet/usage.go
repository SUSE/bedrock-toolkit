package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/usage"
)

// ReadUsage reads the daily usage of month of every reported setup, keyed by Key(account, id). Setups
// without a usage file (older versions, or the Lambda hasn't run yet) are missing from the map.
func ReadUsage(ctx context.Context, r Reader, bucket, month string, reports []Report) (map[string]*usage.FleetUsage, error) {
	out := map[string]*usage.FleetUsage{}
	for _, rep := range reports {
		key := UsageKey(rep.Account, rep.ID, month)
		g, err := r.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		if awsx.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading s3://%s/%s: %w", bucket, key, err)
		}
		var u usage.FleetUsage
		err = json.NewDecoder(g.Body).Decode(&u)
		_ = g.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3://%s/%s: not a usage file: %w", bucket, key, err)
		}
		out[Key(rep.Account, rep.ID)] = &u
	}
	return out, nil
}

// ProductUsage is one product's month across setups.
type ProductUsage struct {
	Product     string  `json:"product"`
	Setups      int     `json:"setups"`
	ActiveUsers int     `json:"active_users"`
	Onboarded   int     `json:"onboarded"`
	TrackedUSD  float64 `json:"tracked_usd"`
}

// DayUsage is one day across setups.
type DayUsage struct {
	Date        string  `json:"date"`
	ActiveUsers int     `json:"active_users"`
	TrackedUSD  float64 `json:"tracked_usd"`
	Partial     bool    `json:"partial"`
}

// UsageByProduct sums the setups' month per product, most spend first; product keeps only that one.
func UsageByProduct(us map[string]*usage.FleetUsage, product string) []ProductUsage {
	by := map[string]*ProductUsage{}
	for _, u := range us {
		for _, p := range u.Products {
			if product != "" && p.Product != product {
				continue
			}
			t := by[p.Product]
			if t == nil {
				t = &ProductUsage{Product: p.Product}
				by[p.Product] = t
			}
			t.Setups++
			t.ActiveUsers += p.ActiveUsers
			t.Onboarded += p.Onboarded
			t.TrackedUSD += p.TrackedUSD
		}
	}
	out := make([]ProductUsage, 0, len(by))
	for _, t := range by {
		t.TrackedUSD = cents(t.TrackedUSD)
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TrackedUSD != out[j].TrackedUSD {
			return out[i].TrackedUSD > out[j].TrackedUSD
		}
		return out[i].Product < out[j].Product
	})
	return out
}

// UsageByDay sums the setups per day of month, through today; product counts only that product.
// Days from the oldest setup's export time on are partial in the current month.
func UsageByDay(us map[string]*usage.FleetUsage, month, product string, now time.Time) []DayUsage {
	by := map[string]*DayUsage{}
	var oldest time.Time
	for _, u := range us {
		if t, err := time.Parse(time.RFC3339, u.AsOf); err == nil && (oldest.IsZero() || t.Before(oldest)) {
			oldest = t
		}
		for _, d := range u.Days {
			t := by[d.Date]
			if t == nil {
				t = &DayUsage{Date: d.Date}
				by[d.Date] = t
			}
			if product == "" {
				t.ActiveUsers += d.ActiveUsers
				t.TrackedUSD += d.TrackedUSD
				continue
			}
			for _, p := range d.Products {
				if p.Product == product {
					t.ActiveUsers += p.ActiveUsers
					t.TrackedUSD += p.TrackedUSD
				}
			}
		}
	}
	partial := usage.PartialFrom(month, oldest, now)
	var out []DayUsage
	for _, date := range usage.Dates(month, now) {
		d := DayUsage{Date: date}
		if t := by[date]; t != nil {
			d = *t
		}
		d.TrackedUSD = cents(d.TrackedUSD)
		d.Partial = partial != "" && date >= partial
		out = append(out, d)
	}
	return out
}

// UsageProducts is every product in the setups' usage, sorted.
func UsageProducts(us map[string]*usage.FleetUsage) []string {
	set := map[string]bool{}
	for _, u := range us {
		for _, p := range u.Products {
			set[p.Product] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// UsageAsOf is the oldest export time of the setups, or "".
func UsageAsOf(us map[string]*usage.FleetUsage) string {
	oldest := ""
	for _, u := range us {
		if u.AsOf != "" && (oldest == "" || u.AsOf < oldest) {
			oldest = u.AsOf
		}
	}
	return oldest
}
