package fleet

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// IdleAfter is how long a setup goes without any personal role being used before it counts as idle.
const IdleAfter = 30 * 24 * time.Hour

// SetupInfo is one setup in the account: an id with a pause policy (bedrock-deny-<id>).
type SetupInfo struct {
	ID      string
	Created time.Time
}

// ListSetups lists the setups in the account, sorted by id.
func ListSetups(ctx context.Context, c iam.ListPoliciesAPIClient) ([]SetupInfo, error) {
	var out []SetupInfo
	p := iam.NewListPoliciesPaginator(c, &iam.ListPoliciesInput{Scope: iamtypes.PolicyScopeTypeLocal, PathPrefix: aws.String(policy.Path)})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, pol := range r.Policies {
			if id, ok := strings.CutPrefix(aws.ToString(pol.PolicyName), policy.DenyPolicyName("")); ok && id != "" {
				out = append(out, SetupInfo{ID: id, Created: aws.ToTime(pol.CreateDate).UTC()})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// SetupSummary summarizes one setup's users.
type SetupSummary struct {
	ID       string    `json:"id"`
	Product  string    `json:"product,omitempty"`
	Created  time.Time `json:"created,omitzero"`
	Users    int       `json:"users"`
	Paused   int       `json:"paused"`
	LastUsed time.Time `json:"last_used,omitzero"`
	SpendUSD float64   `json:"spend_usd"`
	LimitUSD float64   `json:"limit_usd"`
	Idle     bool      `json:"idle"`
}

// Summarize sums a setup's users. Product is the most common user product.
func Summarize(info SetupInfo, us []User, now time.Time) SetupSummary {
	s := SetupSummary{ID: info.ID, Created: info.Created, Users: len(us)}
	products := map[string]int{}
	for _, u := range us {
		if u.Paused() {
			s.Paused++
		}
		if u.LastUsed.After(s.LastUsed) {
			s.LastUsed = u.LastUsed
		}
		s.SpendUSD += u.SpendUSD
		s.LimitUSD += u.LimitUSD
		if u.Product != "" {
			products[u.Product]++
		}
	}
	for p, n := range products {
		if n > products[s.Product] || (n == products[s.Product] && p < s.Product) {
			s.Product = p
		}
	}
	s.SpendUSD, s.LimitUSD = round2(s.SpendUSD), round2(s.LimitUSD)
	s.Idle = Idle(s.Created, s.LastUsed, now)
	return s
}

// Idle reports whether a setup created at created, whose roles were last used at lastUsed (zero
// when never), has gone IdleAfter without use. A setup younger than IdleAfter isn't idle.
func Idle(created, lastUsed, now time.Time) bool {
	since := created
	if lastUsed.After(since) {
		since = lastUsed
	}
	return !since.IsZero() && now.Sub(since) >= IdleAfter
}

// CollectSetups summarizes every setup in infos. manager returns the pause manager of an id.
func CollectSetups(ctx context.Context, infos []SetupInfo, manager func(id string) *pause.Manager, roles RoleGetter, now time.Time) ([]SetupSummary, error) {
	out := make([]SetupSummary, 0, len(infos))
	for _, info := range infos {
		us, err := Collect(ctx, manager(info.ID), roles)
		if err != nil {
			return nil, err
		}
		out = append(out, Summarize(info, us, now))
	}
	return out, nil
}

func round2(v float64) float64 {
	if v < 0 {
		return -float64(int64(-v*100+0.5)) / 100
	}
	return float64(int64(v*100+0.5)) / 100
}
