// Package usercli is the bedrock user CLI: setup writes the personal role profile, doctor checks it, claude tests Claude Code, usage shows the user's own spend and models the models the user may call.
package usercli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// DefaultProfile is the profile setup writes.
const DefaultProfile = "bedrock"

// AWS is what the user CLI calls in AWS.
type AWS interface {
	// Identity returns the caller ARN of a profile.
	Identity(ctx context.Context, profile string) (string, error)
	// AssumeRole assumes roleArn with the credentials of profile.
	AssumeRole(ctx context.Context, profile, roleArn, session string) error
	// Invoke makes the smallest model call with a profile.
	Invoke(ctx context.Context, profile, region, model string) error
	// WebSearchURL returns the web search gateway's MCP URL, or "" when the admin hasn't enabled it.
	WebSearchURL(ctx context.Context, profile, region string) (string, error)
	// WebSearch checks the gateway with tools/list, or with one real search when search is true.
	WebSearch(ctx context.Context, profile, region, url string, search bool) error
	// Budget reads the user's own budget, pause threshold and pause state with their personal role.
	Budget(ctx context.Context, profile, account, role, budget string) (Budget, error)
	// RolePolicy reads the personal role's own invoke policy, decoded.
	RolePolicy(ctx context.Context, profile, role string) (string, error)
	// UserEmail reads the owner tag of the IAM user a profile signs in as.
	UserEmail(ctx context.Context, profile, user string) (string, error)
}

// App holds the CLI's settings and streams.
type App struct {
	Version  string
	In       io.Reader
	Out, Err io.Writer
	AWS      AWS
	// Terminal reports whether In is interactive; nil means check In.
	Terminal func() bool
	JSON     bool
	// Claude runs the claude CLI; nil means the real one.
	Claude ClaudeRunner
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// New returns the CLI with the real AWS calls and the process streams.
func New(version string) *App {
	return &App{Version: version, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, AWS: sdkAWS{}}
}

// Main runs the CLI and returns the exit code.
func (a *App) Main(ctx context.Context, args []string) int {
	root := a.Root()
	root.SetArgs(args)
	return cli.Run(ctx, root, a.Err)
}

func (a *App) printf(format string, args ...any) { fmt.Fprintf(a.Out, format, args...) }

func (a *App) terminal() bool {
	if a.Terminal != nil {
		return a.Terminal()
	}
	return cli.IsTerminal(a.In)
}

// Root returns the root command.
func (a *App) Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "bedrock",
		Short: "Set up and check your personal Amazon Bedrock profile",
		Long: `bedrock sets up the AWS profile of your personal Bedrock role, the one that counts toward your
monthly budget, and checks that it works.

  aws sso login        (nothing when you use an IAM user's access key)
  bedrock setup
  bedrock claude       (tests Claude Code, prints its settings)
  bedrock usage        (this month's spend against your budget)

Exit codes: 0 ok, 1 error, 2 a check failed.`,
		Version: a.Version,
	}
	root.SetIn(a.In)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.AddCommand(a.setupCmd(), a.doctorCmd(), a.claudeCmd(), a.usageCmd(), a.modelsCmd())
	return root
}

type setupFlags struct {
	srcProfile, roleArn, region, defRegion, profile, id, email string
	yes                                                        bool
}

func (a *App) setupCmd() *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Write the bedrock profile in ~/.aws/config, then run doctor",
		Long: `setup writes the [profile bedrock] block in ~/.aws/config (or $AWS_CONFIG_FILE).

It reads your identity to find the account and your email, derives your personal role
(arn:aws:iam::<account>:role/bedrock-users/<id>/bedrock-user-<id>-<name>, id bedrock by default),
sets role_session_name to your email and takes the region from the profile you sign in with.

You sign in with SSO (aws sso login) or, when your admin onboarded your IAM user,
with its access key: pass that profile with --source-profile. setup reads your email from the IAM
user's tags. Your admin tells you when you need --id, --region or --role-arn. The previous file is kept as config.bak; an existing bedrock profile is replaced only
after asking (or with --yes).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.setup(cmd.Context(), f) },
	}
	fl := cmd.Flags()
	fl.StringVar(&f.srcProfile, "source-profile", "", "profile you sign in with: SSO or your IAM user's access key (default: the only SSO profile)")
	fl.StringVar(&f.srcProfile, "sso-profile", "", "same as --source-profile")
	_ = fl.MarkHidden("sso-profile")
	fl.StringVar(&f.email, "email", "", "your email, when you sign in as an IAM user (default: read from the IAM user's tags)")
	fl.StringVar(&f.roleArn, "role-arn", "", "personal role ARN, when your admin gave you one")
	fl.StringVar(&f.id, "id", policy.DefaultID, "id of your admin's setup")
	fl.StringVar(&f.region, "region", "", "Bedrock region (default: the SSO profile's region)")
	fl.StringVar(&f.defRegion, "default-region", "", "region to use when neither --region nor the sign-in profile sets one")
	fl.StringVar(&f.profile, "profile-name", DefaultProfile, "name of the profile to write")
	fl.BoolVarP(&f.yes, "yes", "y", false, "replace an existing profile without asking")
	return cmd
}

func (a *App) setup(ctx context.Context, f setupFlags) error {
	if !config.ValidID(f.id) {
		return fmt.Errorf("--id %q: must be 1-20 lowercase letters and digits", f.id)
	}
	path, err := awsconf.Path()
	if err != nil {
		return err
	}
	file, err := awsconf.Read(path)
	if err != nil {
		return err
	}
	p := cli.NewPrompter(a.In, a.Out)

	src, err := a.signInProfile(file, path, f, p)
	if err != nil {
		return err
	}
	region := f.region
	if region == "" {
		if s := file.Profile(src); s != nil {
			region = s.Keys["region"]
		}
	}
	if region == "" && f.defRegion != "" && !a.terminal() {
		region = f.defRegion
	}
	if region == "" {
		if !a.terminal() {
			return fmt.Errorf("profile %s has no region: pass --region (your admin knows the Bedrock region)", src)
		}
		def := f.defRegion
		if def == "" {
			def = "us-west-2"
		}
		if region, err = p.Ask("Bedrock region", def); err != nil {
			return err
		}
	}

	if config.Geo(region) == "global." {
		a.printf("Note: region %s uses the global. Claude models. If your admin allows only us. or eu. models,\n"+
			"run again with a US or EU region, e.g. --region us-east-1.\n\n", region)
	}

	arn, err := a.AWS.Identity(ctx, src)
	if err != nil {
		return fmt.Errorf("reading your identity with profile %s: %w\n%s", src, err, signInFix(file, src))
	}
	id, err := parseIdentity(arn)
	if err != nil {
		return err
	}
	account, email := id.account, id.email
	if id.iamUser != "" {
		if email, err = a.iamUserEmail(ctx, src, id.iamUser, f.email); err != nil {
			return err
		}
	}
	roleArn := f.roleArn
	if roleArn == "" {
		roleArn = policy.UserRoleArn(account, f.id, policy.UserRoleName(f.id, config.NameFromEmail(email)))
	}
	kvs := []awsconf.KV{{Key: "role_arn", Value: roleArn}, {Key: "source_profile", Value: src},
		{Key: "role_session_name", Value: email}, {Key: "region", Value: region}}

	if old := file.Profile(f.profile); old != nil {
		if same(old, kvs) {
			a.printf("Profile %s in %s is already set up.\n\n", f.profile, path)
			return a.runDoctor(ctx, f.profile, nil)
		}
		a.printf("%s already has a %s profile:\n\n  %s\n\nIt becomes:\n\n  %s\n\n", path, f.profile,
			strings.Join(file.Lines[old.Start:old.End], "\n  "), strings.Join(awsconf.Block(f.profile, kvs), "\n  "))
		if !f.yes {
			if !a.terminal() {
				return errors.New("the profile exists: run again with --yes to replace it")
			}
			ok, err := p.Confirm("Replace it?")
			if err != nil {
				return err
			}
			if !ok {
				return cli.Exit(cli.Error)
			}
		}
	}
	file.SetProfile(f.profile, kvs)
	backup, err := file.Write(path)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	a.printf("Wrote profile %s to %s", f.profile, path)
	if backup != "" {
		a.printf(" (previous file: %s)", backup)
	}
	a.printf(":\n\n  %s\n\n", strings.Join(awsconf.Block(f.profile, kvs), "\n  "))
	return a.runDoctor(ctx, f.profile, nil)
}

// iamUserEmail is the email of an IAM user: --email, else its owner tag.
func (a *App) iamUserEmail(ctx context.Context, profile, user, flag string) (string, error) {
	email := flag
	if email == "" {
		var err error
		if email, err = a.AWS.UserEmail(ctx, profile, user); err != nil {
			return "", fmt.Errorf("reading your email from the tags of IAM user %s: %w\n"+
				"Ask your admin to onboard you with iam_user: %s, or pass --email", user, short(err), user)
		}
		if email == "" {
			return "", fmt.Errorf("IAM user %s has no %s tag: ask your admin to onboard you with iam_user: %s, or pass --email",
				user, policy.OwnerTag, user)
		}
	}
	if !config.IsEmail(email) {
		return "", fmt.Errorf("--email %q: not an email", email)
	}
	return email, nil
}

func (a *App) signInProfile(file *awsconf.File, path string, f setupFlags, p *cli.Prompter) (string, error) {
	if f.srcProfile != "" {
		// An access key profile may be only in the credentials file; reading the identity checks it.
		return f.srcProfile, nil
	}
	cands := slices.DeleteFunc(file.SSOProfiles(), func(s string) bool { return s == f.profile })
	switch {
	case len(cands) == 1:
		return cands[0], nil
	case len(cands) == 0:
		return "", fmt.Errorf("no SSO profile in %s: run aws configure sso first, or pass --source-profile", path)
	case !a.terminal():
		return "", fmt.Errorf("several SSO profiles in %s (%s): pass --source-profile", path, strings.Join(cands, ", "))
	}
	for {
		ans, err := p.Ask("Profile you sign in with ("+strings.Join(cands, ", ")+")", cands[0])
		if err != nil {
			return "", err
		}
		if slices.Contains(cands, ans) {
			return ans, nil
		}
		a.printf("Not one of: %s\n", strings.Join(cands, ", "))
	}
}

func same(s *awsconf.Section, kvs []awsconf.KV) bool {
	if len(s.Keys) != len(kvs) {
		return false
	}
	for _, kv := range kvs {
		if s.Keys[kv.Key] != kv.Value {
			return false
		}
	}
	return true
}

// identity is who a profile signs in as: an SSO login (email set) or an IAM user (iamUser set).
type identity struct{ account, email, iamUser string }

// parseIdentity reads an SSO login's session name (its email) from an assumed-role ARN, or the IAM
// user name from a user ARN.
func parseIdentity(arn string) (identity, error) {
	parts := strings.Split(arn, ":")
	if len(parts) >= 6 && strings.HasPrefix(parts[5], "user/") {
		return identity{account: parts[4], iamUser: parts[5][strings.LastIndex(parts[5], "/")+1:]}, nil
	}
	if len(parts) < 6 || !strings.HasPrefix(parts[5], "assumed-role/") {
		return identity{}, fmt.Errorf("signed in as %s: sign in with aws sso login, or use your IAM user's access key", arn)
	}
	email := awsx.Identity(arn)
	if !config.IsEmail(email) {
		return identity{}, fmt.Errorf("signed in as %s: the session name is not an email, so this is not an SSO login", arn)
	}
	return identity{account: parts[4], email: email}, nil
}

// signInFix tells how to get credentials for a profile: sign in, or check its access key.
func signInFix(file *awsconf.File, profile string) string {
	if s := file.Profile(profile); s != nil && s.IsSSO() {
		return "Sign in first: " + loginCommand(profile)
	}
	return "Check the access key of profile " + profile + " (aws configure --profile " + profile + "), or sign in with SSO"
}

func loginCommand(profile string) string {
	return "aws sso login --profile " + profile
}
