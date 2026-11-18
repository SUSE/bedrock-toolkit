package admincli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
	"github.com/SUSE/bedrock-toolkit/internal/sso"
)

type configureFlags struct {
	id, account, region, product, bucket, prefix string
	reportBucket, reportRegion                   string
	ssoStartURL, ssoRegion, ssoRole              string
	models                                       []string
	limit                                        float64
}

func (a *App) configureCmd() *cobra.Command {
	var f configureFlags
	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Write the settings part of bedrock.yaml (asks, or takes flags)",
		Long: `configure writes everything in bedrock.yaml except users:. It asks for each setting on a terminal;
with --yes, or without a terminal, it uses the flags and the defaults and asks nothing. An existing
file keeps its users: (including an !include) and any setting not given.

configure reads the account and region from your admin profile (--profile, else profile: in the
file): create it first with bedrock-admin configure sso. sso: (your users' SSO start URL and region,
for the commands apply prints) defaults to your SSO profile's.

Writes -f, or ./bedrock.yaml. --dry-run prints the file instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.configure(cmd, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.id, "id", "", "id that tags everything apply creates")
	fl.StringVar(&f.account, "account", "", "AWS account ID (default: the profile's)")
	fl.StringVar(&f.region, "region", "", "Bedrock region (default: the profile's)")
	fl.StringVar(&f.product, "product", "", "product tag on users' roles: the team or product whose users these are (required)")
	fl.StringSliceVar(&f.models, "model", nil, "model (inference profile ID); repeat for more")
	fl.Float64Var(&f.limit, "limit", 0, "default monthly limit in USD")
	fl.StringVar(&f.bucket, "bucket", "", "cost export bucket (default: bedrock-cur-<account>-<id>)")
	fl.StringVar(&f.prefix, "prefix", "", "cost export prefix (default: cur)")
	fl.StringVar(&f.reportBucket, "report-bucket", "", "central report bucket your central admin gave you (default: no report)")
	fl.StringVar(&f.reportRegion, "report-region", "", "region of --report-bucket")
	fl.StringVar(&f.ssoStartURL, "sso-start-url", "", "SSO start URL your users sign in at, for their setup commands (default: your SSO profile's; \"-\": none)")
	fl.StringVar(&f.ssoRegion, "sso-region", "", "region of IAM Identity Center (default: your SSO profile's)")
	fl.StringVar(&f.ssoRole, "sso-role", "", "your users' permission set (default: each user picks one of theirs)")
	cmd.AddCommand(a.configureSSOCmd())
	return cmd
}

func (a *App) configure(cmd *cobra.Command, f configureFlags) error {
	ctx := cmd.Context()
	path := a.File
	if path == "" {
		path = "bedrock.yaml"
	}
	cur := &config.Config{}
	var doc yaml.Node
	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if c, err := config.Parse(path); err == nil {
			cur = c
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if a.Profile != "" {
		cur.Profile = a.Profile
	}
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&cur.ID, f.id)
	set(&cur.Account, f.account)
	set(&cur.Region, f.region)
	set(&cur.Product, f.product)
	set(&cur.CostExport.Bucket, f.bucket)
	set(&cur.CostExport.Prefix, f.prefix)
	set(&cur.Report.Bucket, f.reportBucket)
	set(&cur.Report.Region, f.reportRegion)
	set(&cur.SSO.StartURL, f.ssoStartURL)
	set(&cur.SSO.Region, f.ssoRegion)
	set(&cur.SSO.Role, f.ssoRole)
	if len(f.models) > 0 {
		cur.Models = f.models
	}
	if f.limit > 0 {
		cur.Defaults.LimitUSD = &f.limit
	}
	cur.LegacyBlock = nil
	interactive := !a.Yes && cli.IsTerminal(a.In)
	a.profileHint(cur)
	a.fillFromAWS(ctx, cur)
	if !cmd.Flags().Changed("sso-start-url") && doc.Kind == 0 {
		a.fillSSO(cur)
	}

	// A new file starts with the baseline models (the built-in models.toml).
	newFile := doc.Kind == 0 && len(cur.Models) == 0
	if interactive {
		if err := a.ask(cur, cmd.Flags().Changed, newFile); err != nil {
			return err
		}
	} else if newFile && cur.Region != "" {
		cur.Models = config.BaselineModels(cur.Region)
	}
	if cur.Region == "" {
		return errors.New("no region: the profile has none; pass --region")
	}
	if cur.ID == "" {
		return errors.New("no id: pass --id, 1-20 lowercase letters and digits naming this setup (for example rancher); it names and tags everything bedrock-admin creates and users pass it to bedrock setup")
	}
	if cur.Product == "" {
		return errors.New("no product: pass --product with the team or product whose users these are (for example rancher); fleet reports group usage by it")
	}
	if cur.Defaults.LimitUSD == nil {
		l := config.DefaultLimitUSD
		cur.Defaults.LimitUSD = &l
	}
	if cur.SSO.StartURL == "-" {
		cur.SSO = config.SSO{}
	}
	cur.ApplyDefaults()
	if err := cur.Validate(); err != nil {
		return err
	}
	out, err := writeSettings(&doc, cur)
	if err != nil {
		return err
	}
	if a.DryRun {
		_, err := a.Out.Write(out)
		return err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return err
	}
	a.printf("Wrote %s. Next: add users: entries, then run bedrock-admin plan and bedrock-admin apply.\n", path)
	return nil
}

// AdminProfile is the SSO profile configure sso creates without --profile-name.
const AdminProfile = "bedrock-admin"

// configureSSOCmd creates the admin's SSO profile.
func (a *App) configureSSOCmd() *cobra.Command {
	l := a.Login
	if l == nil {
		l = &sso.Login{}
	}
	return sso.ConfigureCommand(l, AdminProfile, "bedrock-admin configure --profile <profile>")
}

// profileHint tells how to create the admin profile when there is none to sign in with.
func (a *App) profileHint(c *config.Config) {
	path, err := awsconf.Path()
	if err != nil {
		return
	}
	file, err := awsconf.Read(path)
	if err != nil {
		return
	}
	name := a.profile(c)
	switch {
	case name != "" && file.Profile(name) == nil:
		_, _ = fmt.Fprintf(a.Err, "No profile %s in %s: create it with bedrock-admin configure sso --profile-name %s.\n\n", name, path, name)
	case name == "" && os.Getenv("AWS_PROFILE") == "" && os.Getenv("AWS_ACCESS_KEY_ID") == "" &&
		len(file.SSOProfiles()) == 0 && file.Profile("default") == nil:
		_, _ = fmt.Fprintf(a.Err, "No AWS profile in %s: create yours with bedrock-admin configure sso.\n\n", path)
	}
}

// fillFromAWS uses the profile's account and region as defaults, when the credentials work.
func (a *App) fillFromAWS(ctx context.Context, c *config.Config) {
	if c.Account != "" && c.Region != "" {
		return
	}
	cfg, err := awsx.Load(ctx, a.profile(c), c.Region)
	if err != nil {
		return
	}
	if c.Region == "" {
		c.Region = cfg.Region
	}
	if c.Account != "" {
		return
	}
	// STS needs a region; the profile may have none.
	stsCfg := cfg.Copy()
	if stsCfg.Region == "" {
		stsCfg.Region = "us-east-1"
	}
	if id, err := sts.NewFromConfig(stsCfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err == nil {
		c.Account = aws.ToString(id.Account)
	}
}

// fillSSO uses the start URL and SSO region of the admin's SSO profile as the users' sign-in.
func (a *App) fillSSO(c *config.Config) {
	profile := a.profile(c)
	if profile == "" {
		profile = os.Getenv("AWS_PROFILE")
	}
	path, err := awsconf.Path()
	if profile == "" || err != nil {
		return
	}
	file, err := awsconf.Read(path)
	if err != nil {
		return
	}
	if s, err := sso.Resolve(file, path, profile); err == nil {
		c.SSO.StartURL, c.SSO.Region = s.StartURL, s.Region
	}
}

func (a *App) ask(c *config.Config, changed func(string) bool, newFile bool) error {
	p := cli.NewPrompter(a.In, a.Err)
	q := func(flag, question string, dst *string, def string) error {
		if changed(flag) {
			return nil
		}
		if *dst != "" {
			def = *dst
		}
		v, err := p.Ask(question, def)
		if err == nil {
			*dst = v
		}
		return err
	}
	const idQ = "id (names and tags everything bedrock-admin creates; 1-20 lowercase letters and digits, for example rancher)"
	if err := q("id", idQ, &c.ID, ""); err != nil {
		return err
	}
	for c.ID == "" && !changed("id") {
		if err := q("id", idQ, &c.ID, ""); err != nil {
			return err
		}
	}
	if err := q("account", "AWS account ID", &c.Account, ""); err != nil {
		return err
	}
	if err := q("region", "Bedrock region", &c.Region, ""); err != nil {
		return err
	}
	const productQ = "product (the team or product whose users these are, for example rancher; fleet reports group usage by it)"
	if err := q("product", productQ, &c.Product, ""); err != nil {
		return err
	}
	for c.Product == "" && !changed("product") {
		if err := q("product", productQ, &c.Product, ""); err != nil {
			return err
		}
	}
	if !changed("model") {
		def := c.Models
		if len(def) == 0 && newFile {
			def = config.BaselineModels(c.Region)
		}
		v, err := p.Ask("models users may call (comma-separated inference profile IDs; the baseline models are always added; empty: any Claude model except Fable)",
			strings.Join(def, ","))
		if err != nil {
			return err
		}
		c.Models = nil
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				c.Models = append(c.Models, m)
			}
		}
	}
	if !changed("limit") {
		def := strconv.FormatFloat(config.DefaultLimitUSD, 'f', -1, 64)
		if c.Defaults.LimitUSD != nil {
			def = strconv.FormatFloat(*c.Defaults.LimitUSD, 'f', -1, 64)
		}
		v, err := p.Ask("default monthly limit per user, USD", def)
		if err != nil {
			return err
		}
		l, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("limit: %w", err)
		}
		c.Defaults.LimitUSD = &l
	}
	bucketDef := ""
	if c.Account != "" {
		bucketDef = policy.BucketName(c.Account, c.ID)
	}
	if err := q("bucket", "cost export bucket", &c.CostExport.Bucket, bucketDef); err != nil {
		return err
	}
	if err := q("report-bucket", "central report bucket, if your central admin gave you one (\"-\": none)", &c.Report.Bucket, "-"); err != nil {
		return err
	}
	if c.Report.Bucket == "-" {
		c.Report = config.Report{}
	} else if err := q("report-region", "region of the central report bucket", &c.Report.Region, ""); err != nil {
		return err
	}
	sug := config.Config{Profile: c.Profile}
	a.fillSSO(&sug)
	startDef := "-"
	if sug.SSO.StartURL != "" {
		startDef = sug.SSO.StartURL
	}
	if err := q("sso-start-url", "SSO start URL your users sign in at, for their setup commands (\"-\": none)", &c.SSO.StartURL, startDef); err != nil {
		return err
	}
	if c.SSO.StartURL == "-" {
		return nil
	}
	if err := q("sso-region", "region of IAM Identity Center", &c.SSO.Region, sug.SSO.Region); err != nil {
		return err
	}
	return q("sso-role", "your users' permission set (empty: each user picks one of theirs)", &c.SSO.Role, "")
}

// writeSettings replaces every top-level key except users: in doc with the values of c.
func writeSettings(doc *yaml.Node, c *config.Config) ([]byte, error) {
	settings := *c
	settings.Users = nil
	var fresh yaml.Node
	if err := fresh.Encode(&settings); err != nil {
		return nil, err
	}
	var users *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		m := doc.Content[0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == "users" {
				users = m.Content[i+1]
			}
		}
	}
	if users == nil {
		users = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	}
	// Drop users: from the encoded settings (it encodes as null) and append the kept one.
	var content []*yaml.Node
	for i := 0; i+1 < len(fresh.Content); i += 2 {
		if fresh.Content[i].Value != "users" {
			content = append(content, fresh.Content[i], fresh.Content[i+1])
		}
	}
	content = append(content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "users"}, users)
	fresh.Content = content
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{&fresh}}); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}
