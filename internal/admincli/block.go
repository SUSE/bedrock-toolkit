package admincli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"
	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/account"
	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/orgblock"
	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

func (a *App) blockCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "block",
		Short: "Block model calls outside personal roles (needs AWS Organizations rights)",
		Long: `Without a block, anyone whose permission set allows Bedrock can call models directly, outside
their personal role, so their use has no budget, no pause and no per-user cost. The block is a
deny on every SSO role except the admins' permission set: an SCP on the account, or a statement in
the inline policy of each permission set assigned to it.

Changing SCPs or permission sets needs AWS Organizations rights that setup admins usually don't
have. bedrock-admin block policy writes the policy and instructions for whoever does; they run
bedrock-admin block apply. bedrock-admin block status shows the result.`,
	}
	cmd.AddCommand(a.blockPolicyCmd(), a.blockStatusCmd(), a.blockApplyCmd())
	return cmd
}

func (a *App) blockPolicyCmd() *cobra.Command {
	var out, acct, adminPS string
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Write the deny policy and the instructions for the org admin",
		Long: `policy writes the deny (an SCP, or a statement for permission sets) to -o and prints the
instructions for your AWS Organizations admin. It changes nothing in AWS.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if acct == "" {
				c, err := a.fleetClients(cmd.Context(), "")
				if err != nil {
					return err
				}
				acct = c.Account
			} else if !accountRe.MatchString(acct) {
				return fmt.Errorf("--account: not a 12-digit account ID: %q", acct)
			}
			if err := os.WriteFile(out, []byte(policy.Block(adminPS)), 0o644); err != nil {
				return err
			}
			a.printf("%s", account.Instructions(out, acct, adminPS))
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", account.BlockPolicyFile, "file to write")
	cmd.Flags().StringVar(&acct, "account", "", "account to block (default: the profile's)")
	cmd.Flags().StringVar(&adminPS, "admin-permission-set", config.DefaultAdminPermissionSet, "permission set the deny leaves out (the admins')")
	return cmd
}

// BlockStatusJSON is the block status --json output.
type BlockStatusJSON struct {
	Account            string               `json:"account"`
	AdminPermissionSet string               `json:"admin_permission_set"`
	PermissionSets     []BlockPermissionSet `json:"permission_sets"`
}

// BlockPermissionSet is one permission set assigned to the account.
type BlockPermissionSet struct {
	Name string `json:"name"`
	// Deny is yes, no or exempt (the admin permission set).
	Deny       string `json:"deny"`
	AssumeRole bool   `json:"assume_role"`
}

func (a *App) blockStatusCmd() *cobra.Command {
	var adminPS string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show which permission sets in the account carry the deny",
		Long: `status reads the SSO roles of the profile's account (one per permission set assigned to it) and
shows whether each carries the deny and may assume the personal roles. The admin permission set is
the one an existing deny leaves out, else BedrockAdmin. A deny applied by an SCP isn't visible from
the account: status shows "no" for it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.fleetClients(cmd.Context(), "")
			if err != nil {
				return err
			}
			v, err := blockStatus(cmd.Context(), c, adminPS)
			if err != nil {
				return err
			}
			if a.JSON {
				return cli.WriteJSON(a.Out, v)
			}
			return writeBlockStatus(a.Out, v)
		},
	}
	cmd.Flags().StringVar(&adminPS, "admin-permission-set", "", "permission set the deny leaves out (default: the one in the deny, else BedrockAdmin)")
	return cmd
}

func blockStatus(ctx context.Context, c *awsx.Clients, adminPS string) (BlockStatusJSON, error) {
	roles, err := account.SSORoles(ctx, c.IAM)
	if err != nil {
		return BlockStatusJSON{}, fmt.Errorf("reading the SSO roles: %w", err)
	}
	if adminPS == "" {
		adminPS = account.AdminPermissionSet(roles)
	}
	v := BlockStatusJSON{Account: c.Account, AdminPermissionSet: adminPS, PermissionSets: []BlockPermissionSet{}}
	chk := account.CheckSSORoles(roles, adminPS, c.Account)
	for _, r := range roles {
		p := BlockPermissionSet{Name: r.PermissionSet, Deny: "yes", AssumeRole: true}
		switch {
		case r.PermissionSet == adminPS:
			p.Deny = "exempt"
		case slices.Contains(chk.MissingBlock, r.PermissionSet):
			p.Deny = "no"
		}
		if slices.Contains(chk.MissingAssume, r.PermissionSet) || (r.PermissionSet == adminPS && !policy.AllowsAssumePersonalRole(r.Inline)) {
			p.AssumeRole = false
		}
		v.PermissionSets = append(v.PermissionSets, p)
	}
	return v, nil
}

func writeBlockStatus(w io.Writer, v BlockStatusJSON) error {
	_, _ = fmt.Fprintf(w, "Direct model calls in account %s (admin permission set %s)\n\n", v.Account, v.AdminPermissionSet)
	if len(v.PermissionSets) == 0 {
		_, err := fmt.Fprintln(w, "No permission sets are assigned to this account.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PERMISSION SET\tDENY\tASSUME ROLE")
	missing := 0
	for _, p := range v.PermissionSets {
		assume := "no"
		if p.AssumeRole {
			assume = "yes"
		}
		if p.Deny == "no" {
			missing++
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, p.Deny, assume)
	}
	_ = tw.Flush()
	if missing > 0 {
		_, _ = fmt.Fprintf(w, "\n%d permission sets can call models outside personal roles (unless an SCP blocks them, which isn't visible here).\n"+
			"Run bedrock-admin block policy and send the result to your AWS Organizations admin.\n", missing)
	}
	return nil
}

func (a *App) blockApplyCmd() *cobra.Command {
	var acct, method, adminPS, ssoRegion, instance string
	var userSets []string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply the deny to an account (org admin)",
		Long: `apply blocks model calls outside personal roles in --account. Run it as an AWS Organizations
admin, in the management account or a delegated administrator account.

--method scp creates the SCP bedrock-personal-role-only-<account> and attaches it to the account.
It covers every permission set, including future ones, but has no effect in the management account.

--method permission-set adds the deny, limited to the account, to the inline policy of every
permission set provisioned to it except --admin-permission-set, and re-provisions them.
--user-permission-set also lets the named permission sets assume the personal roles.

It asks before changing anything unless --yes is given; --dry-run only shows what it would do.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if !accountRe.MatchString(acct) {
				return fmt.Errorf("--account: not a 12-digit account ID: %q", acct)
			}
			if method != "scp" && method != "permission-set" {
				return fmt.Errorf("--method: must be scp or permission-set (got %q)", method)
			}
			if method == "scp" && (len(userSets) > 0 || ssoRegion != "" || instance != "") {
				return errors.New("--user-permission-set, --sso-region and --instance-arn are for --method permission-set")
			}
			c, err := a.fleetClients(ctx, ssoRegion)
			if err != nil {
				return err
			}
			var items []*plan.Item
			header := fmt.Sprintf("Block model calls outside personal roles in %s (%s; admin permission set %s)", acct, method, adminPS)
			if method == "scp" {
				items, err = orgblock.SCPItems(ctx, organizations.NewFromConfig(c.Cfg), acct, adminPS)
			} else {
				s := ssoadmin.NewFromConfig(c.Cfg)
				var arn string
				if arn, err = orgblock.Instance(ctx, s, instance); err == nil {
					items, err = orgblock.PermissionSetItems(ctx, s, arn, acct, adminPS, userSets)
				}
			}
			if err != nil {
				if awsx.IsAccessDenied(err) {
					return fmt.Errorf("%w\nblock apply needs AWS Organizations rights: run it in the management account or a delegated administrator account", err)
				}
				return err
			}
			return a.applyBlock(ctx, &plan.Plan{Header: header, Items: items})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&acct, "account", "", "account to block (required)")
	fl.StringVar(&method, "method", "", "scp or permission-set (required)")
	fl.StringVar(&adminPS, "admin-permission-set", config.DefaultAdminPermissionSet, "permission set the deny leaves out (the admins')")
	fl.StringSliceVar(&userSets, "user-permission-set", nil, "permission set users sign in with: also allow it to assume the personal roles; repeat for more")
	fl.StringVar(&ssoRegion, "sso-region", "", "IAM Identity Center region (default: the profile's)")
	fl.StringVar(&instance, "instance-arn", "", "IAM Identity Center instance (default: the only one)")
	_ = cmd.MarkFlagRequired("account")
	_ = cmd.MarkFlagRequired("method")
	return cmd
}

func (a *App) applyBlock(ctx context.Context, p *plan.Plan) error {
	if a.JSON {
		if err := plan.RenderJSON(a.Out, p); err != nil {
			return err
		}
	} else {
		plan.Render(a.Out, p, a.Quiet)
	}
	if a.DryRun {
		if p.Changes() > 0 {
			return cli.Exit(cli.Problem)
		}
		return nil
	}
	if p.Changes() == 0 {
		return nil
	}
	if !a.Yes {
		if !cli.IsTerminal(a.In) {
			return errors.New("block apply needs --yes when not run from a terminal")
		}
		ok, err := cli.NewPrompter(a.In, a.Err).Confirm("Make these changes?")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled")
		}
	}
	for _, it := range p.Items {
		if it.Apply == nil {
			continue
		}
		a.info("- %s\n", it.Target)
		if err := it.Apply(ctx); err != nil {
			return err
		}
	}
	a.info("Done. The setup admin can check it with bedrock-admin block status.\n")
	return nil
}

// blockCheck is the doctor check for direct model calls. It only warns: fixing it needs the org admin.
func blockCheck(ctx context.Context, c *awsx.Clients) Check {
	ch := Check{Name: "direct model calls", OK: true}
	v, err := blockStatus(ctx, c, "")
	if err != nil {
		if !awsx.IsAccessDenied(err) {
			ch.OK, ch.Detail = false, err.Error()
			return ch
		}
		ch.Warn, ch.Detail = true, "can't read the SSO roles: "+awsx.ErrorCode(err)
		return ch
	}
	var missing []string
	for _, p := range v.PermissionSets {
		if p.Deny == "no" {
			missing = append(missing, p.Name)
		}
	}
	if len(missing) == 0 {
		ch.Detail = "blocked outside personal roles"
		return ch
	}
	ch.Warn = true
	ch.Detail = "permission sets without the deny (unless an SCP blocks them): " + strings.Join(missing, ", ")
	ch.Fix = "see bedrock-admin block status"
	return ch
}
