package account

import (
	"context"
	"strings"

	"github.com/SUSE/bedrock-toolkit/internal/plan"
)

// OldDefaultProduct was configure's default product. It names no team, so fleet reports can't
// tell the setups that kept it apart.
const OldDefaultProduct = "bedrock"

// product warns when the file, or a user, still has the old default product.
func (e *Env) product(context.Context) ([]*plan.Item, error) {
	var users []string
	for _, u := range e.Cfg.Users {
		if u.Product == OldDefaultProduct {
			users = append(users, u.Email)
		}
	}
	if e.Cfg.Product != OldDefaultProduct && len(users) == 0 {
		return nil, nil
	}
	it := e.item("product", e.Cfg.Product)
	w := "product " + OldDefaultProduct + " is the old default and names no team; set product"
	if e.Cfg.Product == OldDefaultProduct {
		w += " in " + e.Cfg.Path
	}
	if len(users) > 0 {
		if e.Cfg.Product == OldDefaultProduct {
			w += " and"
		}
		w += " for " + strings.Join(users, ", ")
	}
	it.Warnings = []string{w + " to the team or product, so fleet reports tell the setups apart"}
	return one(it), nil
}
