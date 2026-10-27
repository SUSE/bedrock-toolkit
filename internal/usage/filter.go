package usage

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Products is the set of products a filter keeps; empty keeps all.
type Products []string

// Has reports whether the filter keeps product p (NoProduct for none).
func (ps Products) Has(p string) bool {
	if len(ps) == 0 {
		return true
	}
	if p == "" {
		p = NoProduct
	}
	return slices.Contains(ps, p)
}

// Check fails for a product no user has, listing the known ones.
func (ps Products) Check(known []string) error {
	for _, p := range ps {
		if !slices.Contains(known, p) {
			k := slices.Clone(known)
			sort.Strings(k)
			return fmt.Errorf("no users with product %q; known products: %s", p, strings.Join(k, ", "))
		}
	}
	return nil
}

// KnownProducts are the products of the reports' users (NoProduct for users without one).
func KnownProducts(reports ...MonthReport) []string {
	var out []string
	for _, r := range reports {
		for _, u := range r.Users {
			if p := productOf(u); !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// Filter keeps the users of the products. Untracked spend and other setups' spend have no product,
// so a filter drops them.
func (r MonthReport) Filter(ps Products) MonthReport {
	if len(ps) == 0 {
		return r
	}
	r.ProductFilter = ps
	users := []UserRow{}
	r.TrackedUSD = 0
	for _, u := range r.Users {
		if ps.Has(u.Product) {
			users = append(users, u)
			r.TrackedUSD += u.SpentUSD
		}
	}
	r.Users, r.TrackedUSD = users, round2(r.TrackedUSD)
	r.Products = byProduct(users)
	r.Untracked, r.UntrackedUSD, r.OtherIDsUSD = []Caller{}, 0, 0
	r.ActiveUsers = activeUsers(users)
	return r
}

// Active is whether a user spent at least MinUSD.
func (u UserRow) Active() bool { return u.SpentUSD >= MinUSD }

func activeUsers(us []UserRow) int {
	n := 0
	for _, u := range us {
		if u.Active() {
			n++
		}
	}
	return n
}
