package admincli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bcmdataexports"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/account"
	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
	"github.com/SUSE/bedrock-toolkit/internal/users"
)

func (a *App) exportCmd() *cobra.Command {
	var id, region, product string
	var models []string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write the current account, including its users, as a config file (to stdout)",
		Long: `export reads the account and prints a bedrock.yaml that matches it, so plan shows no changes.

Settings come from the file given with -f (or found as usual) when there is one; otherwise from the
account and the flags. Models come from --model, else the file, else a user's role policy.
Only users with this id are exported.

  bedrock-admin export > bedrock.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			base := &config.Config{}
			if path, err := config.Find(a.File); err == nil {
				if base, err = config.Parse(path); err != nil {
					return err
				}
			} else if a.File != "" {
				return err
			}
			if region != "" {
				base.Region = region
			}
			c, err := a.connect(ctx, base, base.Region, base.Account)
			if err != nil {
				return err
			}
			cfg, err := a.export(ctx, c, base, id, product, models)
			if err != nil {
				return err
			}
			if a.JSON {
				return cli.WriteJSON(a.Out, cfg)
			}
			b, err := config.Marshal(cfg)
			if err != nil {
				return err
			}
			a.printf("# Exported from account %s by bedrock-admin export on %s.\n", c.Account, a.now().UTC().Format("2006-01-02"))
			_, err = a.Out.Write(b)
			return err
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "id to export (default: from the file, else the only bedrock-deny-<id> policy's id)")
	cmd.Flags().StringVar(&region, "region", "", "Bedrock region (default: from the file, else the profile's)")
	cmd.Flags().StringVar(&product, "product", "", "product tag (default: from the file, else the users' roles)")
	cmd.Flags().StringSliceVar(&models, "model", nil, "model (inference profile ID); repeat for more (default: from the file)")
	return cmd
}

func mostCommon[T any](vals []T, key func(T) string) (T, bool) {
	counts := map[string]int{}
	var best T
	bestN, found := 0, false
	for _, v := range vals {
		k := key(v)
		counts[k]++
		if counts[k] > bestN {
			best, bestN, found = v, counts[k], true
		}
	}
	return best, found
}

// AWS returns subscribers without a primary address. Prefer the configured primary or the owner.
func exportNotify(emails []string, configured, owner string) string {
	for _, addr := range []string{configured, owner} {
		for _, email := range emails {
			if strings.EqualFold(email, addr) {
				return email
			}
		}
	}
	if len(emails) > 0 {
		return emails[0]
	}
	return owner
}

type exportedUser struct {
	r       config.Resolved
	limit   float64
	pause   int
	alerts  []int
	notify  string
	emails  []string
	product string
}

// nonEmailSubscribers lists the budget's subscribers that bedrock.yaml can't express, such as SNS
// topics added by hand.
func nonEmailSubscribers(o users.Observed, budget string) []string {
	all := slices.Clone(o.ActionSubscribers)
	for _, addresses := range o.Alerts {
		all = append(all, addresses...)
	}
	var out []string
	for _, addr := range all {
		if !config.IsEmail(addr) {
			out = append(out, fmt.Sprintf("%s: %s", budget, addr))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// mixedRecipients describes the budget when its alerts and pause action don't all email the same
// addresses, which bedrock.yaml can't express; it returns "" when they match.
func mixedRecipients(o users.Observed, budget string) string {
	key := func(addrs []string) string {
		l := make([]string, len(addrs))
		for i, a := range addrs {
			l[i] = strings.ToLower(a)
		}
		slices.Sort(l)
		return strings.Join(slices.Compact(l), ",")
	}
	var labels, keys []string
	if o.ActionExists {
		labels, keys = append(labels, "pause action"), append(keys, key(o.ActionSubscribers))
	}
	for _, p := range slices.Sorted(maps.Keys(o.Alerts)) {
		labels, keys = append(labels, fmt.Sprintf("%d%% alert", p)), append(keys, key(o.Alerts[p]))
	}
	if !slices.ContainsFunc(keys, func(k string) bool { return k != keys[0] }) {
		return ""
	}
	parts := make([]string, len(keys))
	for i := range keys {
		parts[i] = labels[i] + ": " + keys[i]
	}
	return budget + " (" + strings.Join(parts, "; ") + ")"
}

func exportNotifyDefaults(defaults []string, found []exportedUser) []string {
	defaults = slices.Clone(defaults)
	for _, g := range found {
		defaults = slices.DeleteFunc(defaults, func(addr string) bool {
			return !strings.EqualFold(addr, g.notify) && !slices.ContainsFunc(g.emails, func(email string) bool {
				return strings.EqualFold(addr, email)
			})
		})
	}
	return defaults
}

func exportConfigUser(cfg *config.Config, g exportedUser, iamUser string) config.User {
	u := config.User{Email: g.r.Email, IAMUser: iamUser}
	if g.r.Name != config.NameFromEmail(g.r.Email) {
		u.Name = g.r.Name
	}
	def := cfg.ResolveUser(u)
	if g.limit != def.LimitUSD {
		l := g.limit
		u.LimitUSD = &l
	}
	if g.pause != 0 && g.pause != def.PauseAtPercent {
		p := g.pause
		u.PauseAtPercent = &p
	}
	if !slices.Equal(g.alerts, def.AlertAtPercent) {
		al := g.alerts
		u.AlertAtPercent = &al
	}
	if g.notify != def.Notify {
		u.Notify = g.notify
	}
	for _, addr := range g.emails {
		if !strings.EqualFold(addr, g.notify) && !slices.ContainsFunc(cfg.Defaults.AdditionalNotifyEmails, func(email string) bool {
			return strings.EqualFold(addr, email)
		}) {
			u.AdditionalNotifyEmails = append(u.AdditionalNotifyEmails, addr)
		}
	}
	if g.product != "" && g.product != def.Product {
		u.Product = g.product
	}
	return u
}

func (a *App) export(ctx context.Context, c *awsx.Clients, base *config.Config, id, product string, models []string) (*config.Config, error) {
	cfg := *base
	cfg.Path = ""
	cfg.Account, cfg.Region = c.Account, c.Region
	if a.Profile != "" {
		cfg.Profile = a.Profile
	}
	if id != "" {
		cfg.ID = id
	}
	if cfg.ID == "" {
		ids, err := account.SetupIDs(ctx, c.IAM)
		if err != nil {
			return nil, err
		}
		switch len(ids) {
		case 0:
			return nil, errors.New("no bedrock-deny-<id> policy in this account; pass --id")
		case 1:
			cfg.ID = ids[0]
		default:
			return nil, fmt.Errorf("several setups in this account (ids %s); pass --id", strings.Join(ids, ", "))
		}
	}
	if len(models) > 0 {
		cfg.Models = models
	}
	if err := deployedSettings(ctx, c, &cfg); err != nil {
		return nil, err
	}

	// Users: every personal role with this id.
	roles, err := account.UserRoles(ctx, c.IAM, cfg.ID)
	if err != nil {
		return nil, err
	}
	env := a.usersEnv(ctx, &cfg, c)
	iamUsers, err := exportIAMUsers(ctx, c, cfg.ID)
	if err != nil {
		return nil, err
	}
	configuredNotify := map[string]string{}
	for _, u := range base.Resolve() {
		configuredNotify[strings.ToLower(u.Email)] = u.Notify
	}
	var found []exportedUser
	var nonEmail, mixed []string
	for _, role := range roles {
		tags, err := env.Pause.RoleTags(ctx, role)
		if err != nil {
			return nil, err
		}
		if tags[policy.IDTag] != cfg.ID {
			continue
		}
		email := tags[policy.OwnerTag]
		if email == "" {
			continue
		}
		name := strings.TrimPrefix(role, policy.UserRoleName(cfg.ID, ""))
		r := config.Resolved{Email: email, Name: name, RoleName: role, BudgetName: policy.BudgetName(cfg.ID, name),
			Product: tags[policy.ProductTag]}
		o, err := env.Observe(ctx, r)
		if err != nil {
			return nil, err
		}
		g := exportedUser{r: r, limit: o.Limit, pause: o.ActionThreshold, alerts: []int{}, product: r.Product}
		for p := range o.Alerts {
			g.alerts = append(g.alerts, p)
		}
		sort.Ints(g.alerts)
		g.emails = o.ActionSubscribers
		if !o.ActionExists && len(g.alerts) > 0 {
			g.emails = o.Alerts[g.alerts[0]]
		}
		nonEmail = append(nonEmail, nonEmailSubscribers(o, r.BudgetName)...)
		if m := mixedRecipients(o, r.BudgetName); m != "" {
			mixed = append(mixed, m)
		}
		g.notify = exportNotify(g.emails, configuredNotify[strings.ToLower(email)], email)
		found = append(found, g)
	}
	if len(nonEmail) > 0 {
		return nil, fmt.Errorf("cannot export: these budget subscribers aren't email addresses, probably added by hand:\n  %s\n"+
			"bedrock-admin manages only email recipients, so apply would remove them; remove them from the budgets, then export again",
			strings.Join(nonEmail, "\n  "))
	}
	if len(mixed) > 0 {
		return nil, fmt.Errorf("cannot export: these budgets email different addresses for different alerts or the pause action, "+
			"but bedrock.yaml has one recipient list per user:\n  %s\n"+
			"apply would set the same list everywhere; make them match on the budgets, then export again",
			strings.Join(mixed, "\n  "))
	}
	if product != "" {
		cfg.Product = product
	}
	if cfg.Product == "" {
		tagged := slices.DeleteFunc(slices.Clone(found), func(g exportedUser) bool { return g.product == "" })
		if g, ok := mostCommon(tagged, func(g exportedUser) string { return g.product }); ok {
			cfg.Product = g.product
		}
	}
	if cfg.Product == "" {
		return nil, errors.New("no product tag found; pass --product")
	}
	sort.Slice(found, func(i, j int) bool { return found[i].r.Email < found[j].r.Email })
	if len(models) == 0 && base.Path == "" && len(found) > 0 {
		if cfg.Models, err = roleModels(ctx, c, found[0].r.RoleName); err != nil {
			return nil, err
		}
		cfg.Models = withoutOtherGeoBaseline(cfg.Region, cfg.Models)
	}

	// Defaults: the file's when there is one, else the most common values.
	if base.Path == "" {
		if g, ok := mostCommon(found, func(g exportedUser) string { return fmt.Sprint(g.limit) }); ok {
			cfg.Defaults.LimitUSD = &g.limit
		} else {
			l := config.DefaultLimitUSD
			cfg.Defaults.LimitUSD = &l
		}
		if g, ok := mostCommon(found, func(g exportedUser) string { return fmt.Sprint(g.pause) }); ok && g.pause != config.DefaultPauseAtPercent {
			cfg.Defaults.PauseAtPercent = &g.pause
		}
		if g, ok := mostCommon(found, func(g exportedUser) string { return fmt.Sprint(g.alerts) }); ok && !slices.Equal(g.alerts, config.DefaultAlertAtPercent) {
			cfg.Defaults.AlertAtPercent = &g.alerts
		}
		cfg.Defaults.Notify = config.DefaultNotify
	}
	cfg.ApplyDefaults()
	// Additive defaults can only be kept when every exported user actually has them.
	cfg.Defaults.AdditionalNotifyEmails = exportNotifyDefaults(cfg.Defaults.AdditionalNotifyEmails, found)
	cfg.Users = []config.User{}
	for _, g := range found {
		cfg.Users = append(cfg.Users, exportConfigUser(&cfg, g, iamUsers[g.r.Email]))
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// roleModels reads the models a personal role's policy allows; nil when it allows any Claude model.
func roleModels(ctx context.Context, c *awsx.Clients, role string) ([]string, error) {
	p, err := c.IAM.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String(policy.InvokePolicyName)})
	if err != nil {
		return nil, err
	}
	return policy.ListedModels(policy.Decode(aws.ToString(p.PolicyDocument)))
}

// withoutOtherGeoBaseline drops the baseline models apply adds for other geographies than the
// region's, so the exported models: holds only models the region offers.
func withoutOtherGeoBaseline(region string, models []string) []string {
	here := config.BaselineModels(region)
	return slices.DeleteFunc(models, func(m string) bool {
		return slices.Contains(config.BaselineAllGeos(), m) && !slices.Contains(here, m)
	})
}

// exportIAMUsers maps emails to the IAM users the id's IAM user policy is attached to.
func exportIAMUsers(ctx context.Context, c *awsx.Clients, id string) (map[string]string, error) {
	names, err := account.AttachedIAMUsers(ctx, c.IAM, c.Account, id)
	if err != nil {
		return nil, fmt.Errorf("listing the IAM users of %s: %w", policy.IAMUsersPolicyName(id), err)
	}
	out := map[string]string{}
	for _, n := range names {
		r, err := c.IAM.ListUserTags(ctx, &iam.ListUserTagsInput{UserName: aws.String(n)})
		if err != nil {
			return nil, fmt.Errorf("reading the tags of IAM user %s: %w", n, err)
		}
		var email, tagID string
		for _, t := range r.Tags {
			switch aws.ToString(t.Key) {
			case policy.OwnerTag:
				email = aws.ToString(t.Value)
			case policy.IDTag:
				tagID = aws.ToString(t.Value)
			}
		}
		if email != "" && tagID == id {
			out[email] = n
		}
	}
	return out, nil
}

// deployedSettings fills the cost export and report settings that cfg doesn't set from what the
// account has for cfg.ID: the cost export named for the id and the Lambda's report settings.
func deployedSettings(ctx context.Context, c *awsx.Clients, cfg *config.Config) error {
	if cfg.CostExport.Name == "" {
		cfg.CostExport.Name = policy.ExportName(cfg.ID)
	}
	if cfg.CostExport.Bucket == "" {
		arn, err := account.FindExport(ctx, c.Exports, cfg.CostExport.Name)
		if err != nil {
			return err
		}
		if arn == "" {
			return fmt.Errorf("no cost export named %s; pass -f with a file that sets cost_export", cfg.CostExport.Name)
		}
		x, err := c.Exports.GetExport(ctx, &bcmdataexports.GetExportInput{ExportArn: aws.String(arn)})
		if err != nil {
			return err
		}
		d := x.Export.DestinationConfigurations.S3Destination
		cfg.CostExport.Bucket, cfg.CostExport.Prefix = aws.ToString(d.S3Bucket), aws.ToString(d.S3Prefix)
	}
	if !cfg.Report.Enabled() {
		r, err := account.DeployedReport(ctx, c.Lambda, cfg.ID)
		if err != nil {
			return err
		}
		cfg.Report = r
	}
	return nil
}
