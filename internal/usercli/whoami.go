package usercli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
	"github.com/SUSE/bedrock-toolkit/internal/awsx"
)

func (a *App) whoamiCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show who a profile signs in as: account, SSO user, role or IAM user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profile == "" && os.Getenv("AWS_PROFILE") == "" {
				if path, err := awsconf.Path(); err == nil {
					if file, err := awsconf.Read(path); err == nil && file.Profile(DefaultProfile) != nil {
						profile = DefaultProfile
					}
				}
			}
			label := profile
			if label == "" {
				label = os.Getenv("AWS_PROFILE")
			}
			if label == "" {
				label = "default"
			}
			arn, err := a.AWS.Identity(cmd.Context(), profile)
			if err != nil {
				return err
			}
			account, who := awsx.Describe(arn)
			a.printf("Profile:  %s\nAccount:  %s\nIdentity: %s\nARN:      %s\n", label, account, who, arn)
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", "profile to check (default: $AWS_PROFILE, else the bedrock profile)")
	return cmd
}
