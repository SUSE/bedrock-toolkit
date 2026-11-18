package admincli

import (
	"context"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
)

type whoami struct {
	Profile  string `json:"profile"`
	Account  string `json:"account"`
	Identity string `json:"identity"`
	ARN      string `json:"arn"`
	// ConfigAccount is the account in bedrock.yaml when it differs.
	ConfigAccount string `json:"config_account,omitempty"`
}

func (a *App) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who the admin profile signs in as, and whether it is bedrock.yaml's account",
		Long: `whoami shows the account and identity (SSO user and permission set, role or IAM user) of the
admin profile: --profile, then $AWS_PROFILE, then profile: in bedrock.yaml. It works without
bedrock.yaml; with one, it warns when the profile is for another account.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _ := a.load()
			profile := a.profile(cfg)
			arn, err := a.callerArn(cmd.Context(), profile)
			if err != nil {
				return err
			}
			w := whoami{Profile: profile, ARN: arn}
			if w.Profile == "" {
				if w.Profile = os.Getenv("AWS_PROFILE"); w.Profile == "" {
					w.Profile = "default"
				}
			}
			w.Account, w.Identity = awsx.Describe(arn)
			if cfg != nil && cfg.Account != w.Account {
				w.ConfigAccount = cfg.Account
			}
			if a.JSON {
				if err := cli.WriteJSON(a.Out, w); err != nil || w.ConfigAccount == "" {
					return err
				}
				return cli.Exit(cli.Problem)
			}
			a.printf("Profile:  %s\nAccount:  %s\nIdentity: %s\nARN:      %s\n", w.Profile, w.Account, w.Identity, w.ARN)
			if w.ConfigAccount != "" {
				a.printf("\nNote: bedrock.yaml (id %s) is for account %s: use that account's admin profile.\n", cfg.ID, cfg.Account)
				return cli.Exit(cli.Problem)
			}
			return nil
		},
	}
}

func (a *App) callerArn(ctx context.Context, profile string) (string, error) {
	if a.Identity != nil {
		return a.Identity(ctx, profile)
	}
	c, err := awsx.Load(ctx, profile, "")
	if err != nil {
		return "", err
	}
	id, err := sts.NewFromConfig(c).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", err
	}
	return aws.ToString(id.Arn), nil
}
