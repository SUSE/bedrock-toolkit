// Package fleet reads how far a setup's users got (onboarded, set up, paused) and builds and sends
// the optional central report.
package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// Kinds of user.
const (
	KindSSO     = "sso"
	KindIAMUser = "iam-user"
)

// User is one onboarded user as the account sees it: a personal role with a pause action.
type User struct {
	Email    string      `json:"email"`
	Role     string      `json:"role"`
	Budget   string      `json:"budget"`
	Kind     string      `json:"kind"`
	Product  string      `json:"product,omitempty"`
	IAMUsers []string    `json:"iam_users,omitempty"`
	LastUsed time.Time   `json:"last_used,omitzero"`
	State    pause.State `json:"state"`
	LimitUSD float64     `json:"limit_usd"`
	SpendUSD float64     `json:"spend_usd"`
}

// SetUp reports whether the user's personal role has been used: bedrock setup uses it, so a used
// role means the user set up their machine.
func (u User) SetUp() bool { return !u.LastUsed.IsZero() }

// Paused reports whether the user can't call models now.
func (u User) Paused() bool { return u.State == pause.Paused || u.State == pause.PausedByHand }

// RoleGetter reads a role (for RoleLastUsed).
type RoleGetter interface {
	GetRole(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error)
}

const concurrency = 5

// Collect reads every onboarded user of the manager's id, sorted by email.
func Collect(ctx context.Context, m *pause.Manager, roles RoleGetter) ([]User, error) {
	snap, actions, _, err := m.TakeSnapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	iamUsers := map[string][]string{}
	for _, a := range actions {
		if a.Definition != nil && a.Definition.IamActionDefinition != nil {
			iamUsers[aws.ToString(a.BudgetName)] = a.Definition.IamActionDefinition.Users
		}
	}
	spend, err := budgetSpend(ctx, m)
	if err != nil {
		return nil, fmt.Errorf("listing budgets: %w", err)
	}
	out := make([]User, len(snap.Users))
	errs := make([]error, len(snap.Users))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, s := range snap.Users {
		u := User{Email: s.Email, Role: s.Role, Budget: s.Budget, Kind: KindSSO, Product: s.Product, IAMUsers: iamUsers[s.Budget],
			State: s.State, LimitUSD: s.LimitUSD, SpendUSD: spend[s.Budget]}
		if len(u.IAMUsers) > 0 {
			u.Kind = KindIAMUser
		}
		out[i] = u
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r, err := roles.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(u.Role)})
			if err != nil {
				errs[i] = fmt.Errorf("reading role %s: %w", u.Role, err)
				return
			}
			if r.Role != nil && r.Role.RoleLastUsed != nil && r.Role.RoleLastUsed.LastUsedDate != nil {
				out[i].LastUsed = r.Role.RoleLastUsed.LastUsedDate.UTC()
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

// budgetSpend is this month's actual spend of every budget of the id.
func budgetSpend(ctx context.Context, m *pause.Manager) (map[string]float64, error) {
	out := map[string]float64{}
	prefix := policy.BudgetName(m.ID, "")
	var token *string
	for {
		r, err := m.Budgets.DescribeBudgets(ctx, &budgets.DescribeBudgetsInput{AccountId: aws.String(m.Account), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, b := range r.Budgets {
			name := aws.ToString(b.BudgetName)
			if strings.HasPrefix(name, prefix) && b.CalculatedSpend != nil && b.CalculatedSpend.ActualSpend != nil {
				v, _ := strconv.ParseFloat(aws.ToString(b.CalculatedSpend.ActualSpend.Amount), 64)
				out[name] = v
			}
		}
		if r.NextToken == nil {
			return out, nil
		}
		token = r.NextToken
	}
}

// Counts summarizes users.
type Counts struct {
	Onboarded int `json:"onboarded"`
	SetUp     int `json:"set_up"`
	Paused    int `json:"paused"`
	IAMUsers  int `json:"iam_users"`
}

// Count sums users up.
func Count(us []User) Counts {
	c := Counts{Onboarded: len(us)}
	for _, u := range us {
		if u.SetUp() {
			c.SetUp++
		}
		if u.Paused() {
			c.Paused++
		}
		if u.Kind == KindIAMUser {
			c.IAMUsers++
		}
	}
	return c
}

// Text is the one-line summary.
func (c Counts) Text() string {
	s := fmt.Sprintf("%d onboarded, %d set up, %d paused", c.Onboarded, c.SetUp, c.Paused)
	if c.IAMUsers > 0 {
		s += fmt.Sprintf(" (%d with an IAM user)", c.IAMUsers)
	}
	return s
}

// Report statuses and sources.
const (
	StatusActive      = "active"
	StatusUninstalled = "uninstalled"
	SourceApply       = "apply"
	SourceLambda      = "lambda"
	SourceUninstall   = "uninstall"
)

// SchemaVersion is the report format version.
const SchemaVersion = 1

// Defaults are the setup's defaults for new users.
type Defaults struct {
	LimitUSD       float64 `json:"limit_usd"`
	PauseAtPercent int     `json:"pause_at_percent"`
	AlertAtPercent []int   `json:"alert_at_percent"`
}

// Setup is the part of a report that comes from bedrock.yaml. apply passes it to the Lambda.
type Setup struct {
	ID            string   `json:"id"`
	Region        string   `json:"region"`
	Version       string   `json:"version"`
	Product       string   `json:"product,omitempty"`
	Defaults      Defaults `json:"defaults"`
	Models        []string `json:"models,omitempty"`
	WebSearch     bool     `json:"web_search"`
	Bucket        string   `json:"bucket"`
	BucketRegion  string   `json:"bucket_region"`
	IncludeEmails bool     `json:"include_emails,omitempty"`
}

// SetupFrom builds the setup part from the config.
func SetupFrom(cfg *config.Config, version string) Setup {
	d := cfg.ResolveUser(config.User{})
	return Setup{ID: cfg.ID, Region: cfg.Region, Version: version, Product: cfg.Product,
		Defaults: Defaults{LimitUSD: d.LimitUSD, PauseAtPercent: d.PauseAtPercent, AlertAtPercent: d.AlertAtPercent},
		Models:   cfg.Models, WebSearch: cfg.WebSearch.Enabled, Bucket: cfg.Report.Bucket, BucketRegion: cfg.Report.Region,
		IncludeEmails: cfg.Report.IncludeEmails}
}

// Person is one user in a report with include_emails.
type Person struct {
	Email   string `json:"email"`
	Kind    string `json:"kind"`
	Product string `json:"product,omitempty"`
	SetUp   bool   `json:"set_up"`
	Paused  bool   `json:"paused"`
}

// Report is what a setup sends to the central bucket.
type Report struct {
	Schema  int    `json:"schema"`
	Account string `json:"account"`
	ID      string `json:"id"`
	Region  string `json:"region"`
	Version string `json:"version"`
	// Product is the setup's default product; Products splits users and spend by each user's product.
	Product    string    `json:"product,omitempty"`
	Status     string    `json:"status"`
	Source     string    `json:"source"`
	ReportedAt time.Time `json:"reported_at"`
	Defaults   Defaults  `json:"defaults"`
	// Models is models: of bedrock.yaml; empty means any Claude model.
	Models    []string `json:"models,omitempty"`
	WebSearch bool     `json:"web_search"`
	Users     Counts   `json:"users"`
	// Month and SpendUSD: the sum of the users' budgets' actual spend this month (lags up to a day).
	Month    string           `json:"month"`
	SpendUSD float64          `json:"spend_usd"`
	Products []ProductSummary `json:"products,omitempty"`
	People   []Person         `json:"people,omitempty"`
}

// ProductSummary is one product's users and spend in a report.
type ProductSummary struct {
	Product  string  `json:"product"`
	Users    Counts  `json:"users"`
	SpendUSD float64 `json:"spend_usd"`
}

// ByProduct groups users by product (a user without one has the setup's), the most spend first.
func ByProduct(us []User, defaultProduct string) []ProductSummary {
	groups := map[string][]User{}
	for _, u := range us {
		p := u.Product
		if p == "" {
			p = defaultProduct
		}
		groups[p] = append(groups[p], u)
	}
	out := make([]ProductSummary, 0, len(groups))
	for p, g := range groups {
		s := ProductSummary{Product: p, Users: Count(g)}
		for _, u := range g {
			s.SpendUSD += u.SpendUSD
		}
		s.SpendUSD = cents(s.SpendUSD)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SpendUSD != out[j].SpendUSD {
			return out[i].SpendUSD > out[j].SpendUSD
		}
		return out[i].Product < out[j].Product
	})
	return out
}

func cents(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// Build makes a report.
func Build(s Setup, account, status, source string, us []User, now time.Time) Report {
	r := Report{Schema: SchemaVersion, Account: account, ID: s.ID, Region: s.Region, Version: s.Version, Product: s.Product, Status: status,
		Source: source, ReportedAt: now.UTC().Truncate(time.Second), Defaults: s.Defaults, Models: s.Models,
		WebSearch: s.WebSearch, Users: Count(us), Month: now.UTC().Format("2006-01"), Products: ByProduct(us, s.Product)}
	for _, u := range us {
		r.SpendUSD += u.SpendUSD
		if s.IncludeEmails {
			p := u.Product
			if p == "" {
				p = s.Product
			}
			r.People = append(r.People, Person{Email: u.Email, Kind: u.Kind, Product: p, SetUp: u.SetUp(), Paused: u.Paused()})
		}
	}
	r.SpendUSD = cents(r.SpendUSD)
	return r
}

// Key is the object key of a setup's report.
func Key(account, id string) string { return account + "/" + id + ".json" }

// UsageKey is a setup's daily usage of one month in the central bucket, written by the Lambda.
func UsageKey(account, id, month string) string {
	return account + "/" + id + "/usage/" + month + ".json"
}

// Putter writes an object.
type Putter interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// Send writes the report to the bucket.
func Send(ctx context.Context, p Putter, bucket string, r Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = p.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(Key(r.Account, r.ID)),
		Body: bytes.NewReader(b), ContentType: aws.String("application/json")})
	if err != nil {
		return fmt.Errorf("writing s3://%s/%s: %w", bucket, Key(r.Account, r.ID), err)
	}
	return nil
}

// Reader lists and reads the reports in the central bucket.
type Reader interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// Read returns the latest report of every setup in the bucket, sorted by account and id. Objects
// that aren't reports are skipped and returned as problems.
func Read(ctx context.Context, r Reader, bucket string) ([]Report, []string, error) {
	var out []Report
	var problems []string
	var token *string
	for {
		l, err := r.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), ContinuationToken: token})
		if err != nil {
			return nil, nil, fmt.Errorf("listing s3://%s: %w", bucket, err)
		}
		for _, o := range l.Contents {
			key := aws.ToString(o.Key)
			if !strings.HasSuffix(key, ".json") {
				continue
			}
			g, err := r.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			if err != nil {
				return nil, nil, fmt.Errorf("reading s3://%s/%s: %w", bucket, key, err)
			}
			var rep Report
			err = json.NewDecoder(g.Body).Decode(&rep)
			_ = g.Body.Close()
			switch {
			case err != nil:
				problems = append(problems, fmt.Sprintf("%s: not a report: %v", key, err))
			case Key(rep.Account, rep.ID) != key:
				problems = append(problems, fmt.Sprintf("%s: says it is account %s, id %s", key, rep.Account, rep.ID))
			default:
				out = append(out, rep)
			}
		}
		if !aws.ToBool(l.IsTruncated) {
			break
		}
		token = l.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account != out[j].Account {
			return out[i].Account < out[j].Account
		}
		return out[i].ID < out[j].ID
	})
	return out, problems, nil
}
