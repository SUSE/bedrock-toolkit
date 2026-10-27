package usercli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	btypes "github.com/aws/aws-sdk-go-v2/service/budgets/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
	"github.com/SUSE/bedrock-toolkit/internal/usage"
)

// Budget is a user's own budget and pause state, as their personal role reads them.
type Budget struct {
	Limit, Spent, Forecast float64
	// Updated is when AWS last refreshed the spend.
	Updated time.Time
	// PauseAt is the pause action's threshold: a percent of Limit, or dollars when PauseAtUSD.
	PauseAt    float64
	PauseAtUSD bool
	Pause      pause.Status
}

type usageJSON struct {
	Month      string    `json:"month"`
	Budget     string    `json:"budget"`
	LimitUSD   float64   `json:"limit_usd"`
	SpentUSD   float64   `json:"spent_usd"`
	Forecast   float64   `json:"forecast_usd"`
	TriggerUSD float64   `json:"pause_at_usd,omitempty"`
	State      string    `json:"state"`
	Detail     string    `json:"detail"`
	AsOf       time.Time `json:"as_of,omitzero"`
	daily
}

type monthJSON struct {
	Month    string  `json:"month"`
	TotalUSD float64 `json:"total_usd"`
	daily
}

func (a *App) usageCmd() *cobra.Command {
	var profile, month string
	var days int
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show this month's spend against your budget, and whether you are paused",
		Long: `usage reads your own monthly budget and your personal role, with the bedrock profile: what you
spent this month, the forecast, where you get paused and whether you are paused now. Then it shows
your spend per day of the last --days days and per model this month, from the usage file the
account writes for you every day at 07:00 UTC.

With --month YYYY-MM it shows that month's days and models only, without the budget.

AWS refreshes budget spend a few times a day and the cost export up to a day late, so the last hours
are not in them yet. Web search is not counted: the account pays for it.`,
		Example: `  bedrock usage
  bedrock usage --days 30
  bedrock usage --month 2026-04`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if days < 1 || days > 62 {
				return fmt.Errorf("--days must be 1 to 62 (got %d)", days)
			}
			if month != "" {
				if _, err := time.Parse("2006-01", month); err != nil {
					return fmt.Errorf("--month must be YYYY-MM (got %q)", month)
				}
				if month > a.now().UTC().Format("2006-01") {
					return fmt.Errorf("--month %s is in the future", month)
				}
				if cmd.Flags().Changed("days") {
					return errors.New("--days and --month don't go together")
				}
				return a.usageMonth(cmd.Context(), profile, month)
			}
			return a.usage(cmd.Context(), profile, days)
		},
	}
	cmd.Flags().StringVar(&profile, "profile-name", DefaultProfile, "profile of your personal role")
	cmd.Flags().IntVar(&days, "days", 14, "days of daily spend to show")
	cmd.Flags().StringVar(&month, "month", "", "show the days and models of this month (YYYY-MM) instead")
	cmd.Flags().BoolVar(&a.JSON, "json", false, "print as JSON")
	return cmd
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// ownBudget reads the profile's role ARN and returns the account, role and budget names.
func ownBudget(profile string) (account, role, budget string, err error) {
	account, _, role, err = profileRole(profile)
	if err != nil {
		return "", "", "", err
	}
	return account, role, policy.BudgetForRole(role), nil
}

// profileRole reads the profile's role ARN (arn:aws:iam::<account>:role/bedrock-users/<id>/bedrock-user-<id>-<name>)
// and returns the account, the setup's id and the role name.
func profileRole(profile string) (account, id, role string, err error) {
	path, err := awsconf.Path()
	if err != nil {
		return "", "", "", err
	}
	file, err := awsconf.Read(path)
	if err != nil {
		return "", "", "", err
	}
	s := file.Profile(profile)
	if s == nil || s.Keys["role_arn"] == "" {
		return "", "", "", fmt.Errorf("no profile %s with a role_arn in %s: run bedrock setup", profile, path)
	}
	arn := s.Keys["role_arn"]
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) == 6 && parts[2] == "iam" {
		res := strings.TrimPrefix(parts[5], "role")
		role = res[strings.LastIndex(res, "/")+1:]
		if id, _, ok := policy.SplitUserRole(role); ok && res == policy.UserPath(id)+role {
			return parts[4], id, role, nil
		}
	}
	return "", "", "", fmt.Errorf("profile %s: role_arn %s is not a personal Bedrock role", profile, arn)
}

// usageMonth shows one month's days and models, from the usage file only.
func (a *App) usageMonth(ctx context.Context, profile, month string) error {
	_, role, _, err := ownBudget(profile)
	if err != nil {
		return err
	}
	now := a.now().UTC()
	start, _ := time.Parse("2006-01", month)
	d, err := a.readDaily(ctx, profile, role, month, usage.Dates(month, now), now)
	if err != nil {
		return fmt.Errorf("%w\nif your sign-in expired: aws sso login, then try again", short(err))
	}
	total := 0.0
	for _, r := range d.Days {
		total += r.USD
	}
	if a.JSON {
		return cli.WriteJSON(a.Out, monthJSON{Month: month, TotalUSD: math.Round(total*100) / 100, daily: d})
	}
	d.write(a.Out, fmt.Sprintf("Your spend in %s (%s)", start.Format("January 2006"), asOfText(d.AsOf)), "Per model")
	if d.Note == "" {
		a.printf("\nTotal %s\n", usd(total))
	}
	return nil
}

func (a *App) usage(ctx context.Context, profile string, days int) error {
	account, role, budget, err := ownBudget(profile)
	if err != nil {
		return err
	}
	b, err := a.AWS.Budget(ctx, profile, account, role, budget)
	if err != nil {
		switch {
		case awsx.IsAccessDenied(err):
			return fmt.Errorf("%w\nyour role cannot read its budget yet: ask your admin to run bedrock-admin apply", short(err))
		case awsx.IsNotFound(err):
			return fmt.Errorf("no budget %s: ask your admin to onboard you", budget)
		}
		return fmt.Errorf("%w\nif your sign-in expired: aws sso login, then try again", short(err))
	}
	now := a.now().UTC()
	trigger := b.PauseAt
	if !b.PauseAtUSD {
		trigger = b.Limit * b.PauseAt / 100
	}
	state, detail := userState(b.Pause, now)
	d, err := a.readDaily(ctx, profile, role, now.Format("2006-01"), lastDays(days, now), now)
	if err != nil {
		d = daily{Days: []usage.DayUSD{}, Models: []usage.Amount{}, Note: "Could not read your daily usage: " + short(err).Error()}
	}
	if a.JSON {
		return cli.WriteJSON(a.Out, usageJSON{Month: now.Format("2006-01"), Budget: budget, LimitUSD: b.Limit, SpentUSD: b.Spent,
			Forecast: b.Forecast, TriggerUSD: trigger, State: state, Detail: detail, AsOf: b.Updated, daily: d})
	}
	a.printf("Budget     %s, %s (resets %s)\n", budget, now.Format("January 2006"), pause.NextMonth(now).Format("Jan 2"))
	a.printf("Spent      %s of %s%s\n", usd(b.Spent), usd(b.Limit), percent(b.Spent, b.Limit))
	if b.Forecast > 0 {
		a.printf("Forecast   %s by the end of the month\n", usd(b.Forecast))
	}
	if trigger > 0 {
		a.printf("Pauses at  %s%s\n", usd(trigger), percent(trigger, b.Limit))
	}
	a.printf("Access     %s\n", detail)
	if !b.Updated.IsZero() {
		a.printf("As of      %s (AWS refreshes budgets a few times a day)\n", b.Updated.UTC().Format("Jan 2 15:04 MST"))
	}
	a.printf("\n")
	d.write(a.Out, fmt.Sprintf("Per day (%s)", asOfText(d.AsOf)), "Per model this month")
	a.printf("\nWeb search is not counted here: the account pays for it.\n")
	return nil
}

// userState says what a pause state means for the user.
func userState(s pause.Status, now time.Time) (string, string) {
	switch s.State {
	case pause.Armed, pause.NoAction:
		return "active", "active"
	case pause.Off:
		return "active", "active; your admin turned the pause off until " + pause.NextMonth(now).Format("Jan 2")
	case pause.Paused:
		d := "PAUSED: monthly budget used up"
		if !s.PausedAt.IsZero() {
			d += " on " + s.PausedAt.UTC().Format("Jan 2")
		}
		return "paused", d + "; back on " + pause.NextMonth(now).Format("Jan 2") + ", or ask your admin"
	case pause.PausedByHand:
		d := "PAUSED by your admin"
		if s.By != "" {
			d += " (" + s.By + ")"
		}
		if s.Reason != "" {
			d += ": " + s.Reason
		}
		return "paused", d
	}
	return string(s.State), s.Text(now)
}

func usd(v float64) string { return fmt.Sprintf("$%.2f", v) }

func percent(v, of float64) string {
	if of <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%.0f%%)", v/of*100)
}

func (sdkAWS) Budget(ctx context.Context, profile, account, role, budget string) (Budget, error) {
	cfg, err := load(ctx, profile, "")
	if err != nil {
		return Budget{}, err
	}
	bc := budgets.NewFromConfig(cfg)
	r, err := bc.DescribeBudget(ctx, &budgets.DescribeBudgetInput{AccountId: aws.String(account), BudgetName: aws.String(budget)})
	if err != nil {
		return Budget{}, err
	}
	if r.Budget == nil {
		return Budget{}, errors.New("empty budget")
	}
	var out Budget
	if l := r.Budget.BudgetLimit; l != nil {
		out.Limit = amount(l.Amount)
	}
	if c := r.Budget.CalculatedSpend; c != nil {
		if c.ActualSpend != nil {
			out.Spent = amount(c.ActualSpend.Amount)
		}
		if c.ForecastedSpend != nil {
			out.Forecast = amount(c.ForecastedSpend.Amount)
		}
	}
	if r.Budget.LastUpdatedTime != nil {
		out.Updated = *r.Budget.LastUpdatedTime
	}
	m := &pause.Manager{Budgets: bc, IAM: iam.NewFromConfig(cfg), Account: account}
	act, err := m.Action(ctx, budget)
	if err != nil {
		return Budget{}, err
	}
	if act != nil && act.ActionThreshold != nil {
		out.PauseAt = act.ActionThreshold.ActionThresholdValue
		out.PauseAtUSD = act.ActionThreshold.ActionThresholdType == btypes.ThresholdTypeAbsoluteValue
	}
	out.Pause, err = m.Status(ctx, role, budget)
	return out, err
}

func amount(s *string) float64 {
	v, _ := strconv.ParseFloat(aws.ToString(s), 64)
	return v
}
