package admincli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	btypes "github.com/aws/aws-sdk-go-v2/service/budgets/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"

	"github.com/innobead/bedrock-toolkit/internal/account"
	"github.com/innobead/bedrock-toolkit/internal/awsx"
	"github.com/innobead/bedrock-toolkit/internal/cli"
	"github.com/innobead/bedrock-toolkit/internal/config"
	"github.com/innobead/bedrock-toolkit/internal/plan"
	"github.com/innobead/bedrock-toolkit/internal/policy"
	"github.com/innobead/bedrock-toolkit/internal/usage"
	"github.com/innobead/bedrock-toolkit/internal/users"
)

func (a *App) uninstallCmd() *cobra.Command {
	var deleteData bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the account setup (refuses while users remain)",
		Long: `uninstall removes the account setup this config's id created: the monthly Lambda and its schedule,
the budget actions role, the bedrock-deny policy, the web search gateway and the cost export. It refuses while personal roles
remain (remove the users from the file and apply first). The cost export bucket, with the billing history
and snapshots, is kept unless --delete-data is given. It asks first unless --yes is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg, err := a.load()
			if err != nil {
				return err
			}
			c, err := a.clients(ctx, cfg)
			if err != nil {
				return err
			}
			env := &account.Env{C: c, Cfg: cfg, Out: a.Err, Dir: "."}
			items, err := env.Uninstall(ctx, deleteData)
			if err != nil {
				return err
			}
			p := &plan.Plan{Header: fmt.Sprintf("Uninstall %s (id %s) from account %s", cfg.Path, cfg.ID, c.Account), Items: items}
			if p.Changes() == 0 {
				if a.JSON {
					return plan.RenderJSON(a.Out, p)
				}
				plan.Render(a.Out, p, a.Quiet)
				return nil
			}
			roles, err := account.UserRoles(ctx, c.IAM, cfg.ID)
			if err != nil {
				return err
			}
			if len(roles) > 0 {
				return fmt.Errorf("%d personal roles remain (%s); offboard them first (users: [] then bedrock-admin apply --yes)",
					len(roles), strings.Join(roles, ", "))
			}
			if a.JSON {
				if err := plan.RenderJSON(a.Out, p); err != nil {
					return err
				}
			} else {
				plan.Render(a.Out, p, a.Quiet)
			}
			if a.DryRun {
				return nil
			}
			if !a.Yes {
				if !cli.IsTerminal(a.In) {
					return errors.New("uninstall needs --yes when not run from a terminal")
				}
				pr := cli.NewPrompter(a.In, a.Err)
				ok, err := pr.Confirm("Remove these?")
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("cancelled")
				}
				for _, it := range items {
					if it.Apply == nil || !account.IsBucket(it) {
						continue
					}
					_, _ = fmt.Fprintf(a.Err, "\nThe %s %s.\n", it.Target, account.BucketDeleteNote)
					ok, err := pr.Confirm("Delete the bucket too?")
					if err != nil {
						return err
					}
					if !ok {
						it.Apply = nil
						a.info("Keeping %s.\n", it.Target)
					}
				}
			}
			for _, it := range items {
				if it.Apply == nil {
					continue
				}
				a.info("- Step %s %s\n", it.Step, it.Target)
				if err := it.Apply(ctx); err != nil {
					return fmt.Errorf("removing %s: %w", it.Target, err)
				}
			}
			a.info("Uninstalled.\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteData, "delete-data", false, "also delete the cost export bucket (billing history and snapshots); S3 may not let you reuse its name right away")
	return cmd
}

// Check is one doctor result.
type Check = cli.Check

func printChecks(a *App, checks []Check) error {
	return cli.PrintChecks(a.Out, checks, a.JSON, a.Quiet)
}

func (a *App) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the admin's environment: credentials, permissions, account setup, cost export data, models",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printChecks(a, a.doctor(cmd.Context()))
		},
	}
}

func (a *App) doctor(ctx context.Context) []Check {
	var out []Check
	cfg, err := a.load()
	if err != nil {
		return append(out, Check{Name: "config", Detail: err.Error(), Fix: "fix the file, or create one with bedrock-admin configure"})
	}
	out = append(out, Check{Name: "config", OK: true, Detail: fmt.Sprintf("%s (id %s, %d users)", cfg.Path, cfg.ID, len(cfg.Users))})
	c, err := a.clients(ctx, cfg)
	if err != nil {
		fix := "aws sso login"
		if p := a.profile(cfg); p != "" {
			fix += " --profile " + p
		}
		return append(out, Check{Name: "credentials", Detail: err.Error(), Fix: fix})
	}
	out = append(out, Check{Name: "credentials", OK: true, Detail: fmt.Sprintf("%s (account %s)", c.CallerArn, c.Account)})
	env := &account.Env{C: c, Cfg: cfg, Dir: "."}
	items, err := env.Plan(ctx)
	if err != nil {
		fix := ""
		if awsx.IsAccessDenied(err) {
			fix = "use an admin role with IAM, Budgets, Data Exports, S3, Lambda, EventBridge Scheduler, Cost Explorer, Bedrock and Bedrock AgentCore rights"
		}
		out = append(out, Check{Name: "permissions", Detail: err.Error(), Fix: fix})
	} else {
		out = append(out, Check{Name: "permissions", OK: true, Detail: "read every part of the account setup"})
		for _, it := range items {
			ch := Check{Name: "Step " + it.Step + " " + it.Target, OK: !it.Op.Pending(), Detail: it.Status}
			if len(it.Details) > 0 {
				ch.Detail = strings.Join(it.Details, ", ")
			}
			switch it.Op {
			case plan.Handoff:
				ch.Fix = "bedrock-admin apply writes bedrock-block-policy.json and the instructions for the org admin"
				if it.Step == "1.6" {
					ch = a.costTagCheck(ctx, cfg, c, ch.Name)
				}
			case plan.Conflict:
				ch.Fix = "see the detail; bedrock-admin never takes over resources it didn't create"
			case plan.Create, plan.Update, plan.Delete:
				ch.Fix = "bedrock-admin apply"
			}
			out = append(out, ch)
		}
	}
	out = append(out, a.exportData(ctx, cfg, c))
	for _, m := range cfg.TestModels() {
		ch := Check{Name: "model " + m}
		if err := account.TestCall(ctx, c.Runtime, m); err != nil {
			ch.Detail, ch.Fix = err.Error(), "enable the model in the Bedrock console (Model access) or check models: in the file"
		} else {
			ch.OK, ch.Detail = true, "test call ok"
		}
		out = append(out, ch)
	}
	return out
}

// costTagCheck is Step 1.6 when this account can't read cost allocation tags (only the management
// account can). Budget spend or owner tags in the cost export show the tag is active; without
// either, it's a warning: the admin can't check or fix it, but budgets count nothing until it's on.
func (a *App) costTagCheck(ctx context.Context, cfg *config.Config, c *awsx.Clients, name string) Check {
	if why := a.costTagEvidence(ctx, cfg, c); why != "" {
		return Check{Name: name, OK: true, Detail: "active (" + why + ")"}
	}
	return Check{Name: name, OK: true, Warn: true,
		Detail: "can't check from this account (only the management account can); budgets count spend only once it's active",
		Fix:    "ask your org admin to activate " + policy.CostAllocationOwnerTag + " in the management account (Billing → Cost allocation tags)"}
}

// costTagEvidence says why the owner tag is known to be active, or "" if nothing shows it.
func (a *App) costTagEvidence(ctx context.Context, cfg *config.Config, c *awsx.Clients) string {
	var all []btypes.Budget
	p := budgets.NewDescribeBudgetsPaginator(c.Budgets, &budgets.DescribeBudgetsInput{AccountId: aws.String(c.Account)})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			break
		}
		all = append(all, r.Budgets...)
	}
	if name := tagBudgetWithSpend(all); name != "" {
		return "budget " + name + " counts spend"
	}
	src := &usage.S3Source{S3: c.BucketS3(ctx, cfg.CostExport.Bucket), Bucket: cfg.CostExport.Bucket, Prefix: cfg.ExportPrefix()}
	now := a.now().UTC()
	for _, month := range []string{now.Format("2006-01"), now.AddDate(0, -1, 0).Format("2006-01")} {
		if found, _ := exportHasOwnerTag(ctx, src, month); found {
			return "the cost export of " + month + " has " + policy.CostAllocationOwnerTag
		}
	}
	return ""
}

// tagBudgetWithSpend returns a budget that filters on the owner tag and has spend this month. Budgets
// count spend only for active tags, so one is enough to show the tag is active.
func tagBudgetWithSpend(bs []btypes.Budget) string {
	for i := range bs {
		b := &bs[i]
		f := b.FilterExpression
		if f == nil || f.Tags == nil || aws.ToString(f.Tags.Key) != policy.CostAllocationOwnerTag {
			continue
		}
		if actual, _ := users.Spend(b); actual > 0 {
			return aws.ToString(b.BudgetName)
		}
	}
	return ""
}

// exportHasOwnerTag reports whether a month's cost export has a row with the owner tag. The export
// carries only active cost allocation tags.
func exportHasOwnerTag(ctx context.Context, src usage.Source, month string) (bool, error) {
	found := false
	_, err := usage.ReadMonth(ctx, src, month, func(l usage.Line) {
		found = found || l.Owner != ""
	})
	return found, err
}

// exportData checks that the cost export has delivered files lately.
func (a *App) exportData(ctx context.Context, cfg *config.Config, c *awsx.Clients) Check {
	ch := Check{Name: "cost export data"}
	prefix := cfg.ExportPrefix() + "/data/"
	var newest time.Time
	var keys []string
	p := s3.NewListObjectsV2Paginator(c.BucketS3(ctx, cfg.CostExport.Bucket), &s3.ListObjectsV2Input{Bucket: aws.String(cfg.CostExport.Bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			ch.Detail, ch.Fix = err.Error(), "bedrock-admin apply (Step 1.1)"
			return ch
		}
		for _, o := range r.Contents {
			if o.LastModified != nil && o.LastModified.After(newest) {
				newest = *o.LastModified
			}
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	if len(keys) == 0 {
		ch.Detail = fmt.Sprintf("no files under s3://%s/%s yet", cfg.CostExport.Bucket, prefix)
		ch.Fix = "the first delivery comes up to 24 h after the export is created; check again later"
		return ch
	}
	sort.Strings(keys)
	age := a.now().Sub(newest)
	ch.Detail = fmt.Sprintf("%d files, newest %s (%s ago)", len(keys), newest.UTC().Format("2006-01-02 15:04 UTC"), age.Round(time.Hour))
	if age > 48*time.Hour {
		ch.Fix = "AWS refreshes the export daily; check the export's status in the Billing console (Data Exports)"
		return ch
	}
	ch.OK = true
	return ch
}
