package admincli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/account"
	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/fleet"
	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

func (a *App) moveCmd() *cobra.Command {
	var from string
	var deleteData bool
	cmd := &cobra.Command{
		Use:   "move --from <old-id>",
		Short: "Replace another setup in the account with this one: offboard its users and uninstall it",
		Long: `move finishes moving users from the setup --from to this file's setup in the same account. It
offboards every user of the old id (their roles, budgets and IAM users) and uninstalls the old setup,
like users: [], apply --yes and uninstall with the old id's file. Its last central report has status
moved, so fleet report shows it as "moved to <id>" and leaves it out of the totals; its earlier reports
and daily usage stay.

Apply this file first. Users who haven't run their setup command for this id yet lose access until
they do (bedrock-admin users --commands prints the commands); users who aren't in this file lose it
for good, and move names them before it asks. The old cost export bucket is kept unless --delete-data
is given. It asks first unless --yes is given.

  bedrock-admin move --from bedrock`,
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
			ids, err := account.SetupIDs(ctx, c.IAM)
			if err != nil {
				return err
			}
			if from != "" && !slices.Contains(ids, from) {
				// A move that stopped after the pause policy went leaves only the cost export; resume it.
				arn, err := account.FindExport(ctx, c.Exports, policy.ExportName(from))
				if err != nil {
					return err
				}
				if arn != "" {
					ids = append(ids, from)
				}
			}
			newRoles, err := account.UserRoles(ctx, c.IAM, cfg.ID)
			if err != nil {
				return err
			}
			if err := checkMove(cfg, from, ids, newRoles); err != nil {
				return err
			}
			old, err := a.movedSetup(ctx, c, cfg, from)
			if err != nil {
				return err
			}
			if deleteData && strings.EqualFold(old.CostExport.Bucket, cfg.CostExport.Bucket) {
				return fmt.Errorf("--delete-data would delete %s, which id %s uses too; move without it", old.CostExport.Bucket, cfg.ID)
			}
			uitems, err := a.usersEnv(ctx, old, c).Plan(ctx, false)
			if err != nil {
				return err
			}
			aitems, err := a.accountEnv(old, c, a.Err).Uninstall(ctx, deleteData)
			if err != nil {
				return err
			}
			p := &plan.Plan{Header: fmt.Sprintf("Move id %s to id %s (%s) in account %s", from, cfg.ID, cfg.Path, c.Account),
				Items: append(uitems, aitems...), Notes: moveNotes(cfg, from, uitems)}
			if err := a.showPlan(p); err != nil {
				return err
			}
			if a.DryRun {
				return nil
			}
			if p.Changes() > 0 {
				if err := a.confirmRemoval("move", p.Items); err != nil {
					return err
				}
				for _, it := range p.Items {
					if it.Apply == nil || !it.Op.Pending() {
						continue
					}
					a.info("- %s %s\n", symbol(it.Op), itemLabel(it))
					if err := it.Apply(ctx); err != nil {
						return fmt.Errorf("removing %s: %w; run move again", itemLabel(it), err)
					}
				}
			}
			a.info("Moved: id %s is uninstalled.\n", from)
			a.sendReport(ctx, old, c, fleet.StatusMoved, fleet.SourceMove, cfg.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "id of the setup to replace (required)")
	cmd.Flags().BoolVar(&deleteData, "delete-data", false, "also delete the old setup's cost export bucket (billing history and snapshots)")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}

// checkMove refuses a move that can't be right: the same id, an old id that isn't in the account, or
// a new setup that isn't applied yet (ids lists the account's setups, newRoles the new id's roles).
func checkMove(cfg *config.Config, from string, ids, newRoles []string) error {
	switch {
	case from == cfg.ID:
		return fmt.Errorf("--from %s is this file's id; give the id to replace", from)
	case !slices.Contains(ids, from):
		return fmt.Errorf("no setup with id %s in this account (setups: %s)", from, strings.Join(ids, ", "))
	case !slices.Contains(ids, cfg.ID):
		return fmt.Errorf("id %s isn't set up in this account; run bedrock-admin apply first", cfg.ID)
	}
	var missing []string
	for _, u := range cfg.Resolve() {
		if !slices.Contains(newRoles, u.RoleName) {
			missing = append(missing, u.Email)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%d users of this file have no role in id %s yet (%s); run bedrock-admin apply first",
			len(missing), cfg.ID, strings.Join(missing, ", "))
	}
	return nil
}

// movedSetup is the old setup's config: this file's settings with the old id, no users, and the cost
// export and report settings the old setup deployed (this file's report when its Lambda has none).
func (a *App) movedSetup(ctx context.Context, c *awsx.Clients, cfg *config.Config, from string) (*config.Config, error) {
	old := *cfg
	old.Path, old.ID, old.Users = "", from, nil
	old.CostExport = config.CostExport{}
	old.Report = config.Report{}
	if err := deployedSettings(ctx, c, &old); err != nil {
		return nil, fmt.Errorf("id %s: %w", from, err)
	}
	if !old.Report.Enabled() {
		old.Report = cfg.Report
	}
	return &old, nil
}

// moveNotes names the old id's users who aren't in the new file, and tells how the others regain access.
func moveNotes(cfg *config.Config, from string, offboard []*plan.Item) []string {
	var lost []string
	n := 0
	for _, it := range offboard {
		if it.Op != plan.Delete || it.Email == "" {
			continue
		}
		n++
		if _, ok := cfg.FindUser(it.Email); !ok {
			lost = append(lost, it.Email)
		}
	}
	if n == 0 {
		return nil
	}
	notes := []string{fmt.Sprintf("Users who still use id %s lose access until they run their setup command for id %s; "+
		"bedrock-admin users --commands prints them.", from, cfg.ID)}
	if len(lost) > 0 {
		notes = append(notes, fmt.Sprintf("%d users of id %s aren't in %s and lose access for good: %s.",
			len(lost), from, cfg.Path, strings.Join(lost, ", ")))
	}
	return notes
}
