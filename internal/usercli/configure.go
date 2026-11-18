package usercli

import (
	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/sso"
)

func (a *App) configureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Create the profile you sign in with: SSO or your IAM user's access key",
		Long: `configure creates the AWS profile you sign in with, once, before bedrock setup:

  bedrock configure sso          an SSO profile (bedrock-sso) in ~/.aws/config
  bedrock configure access-key   your IAM user's access key (bedrock-key) in ~/.aws/credentials

Your admin sends the command with its flags.`,
		Args: cobra.NoArgs,
	}
	l := a.Login
	if l == nil {
		l = &sso.Login{}
	}
	cmd.AddCommand(sso.ConfigureCommand(l, SSOProfile, "bedrock setup --source-profile <profile> (with the --id your admin sent)"), a.accessKeyCmd())
	return cmd
}

func (a *App) accessKeyCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "access-key",
		Short: "Write your IAM user's access key to ~/.aws/credentials",
		Long: `configure access-key asks for your IAM user's access key ID and secret (the secret without echo;
without a terminal, it reads them as two lines from stdin) and writes them to ~/.aws/credentials
(or $AWS_SHARED_CREDENTIALS_FILE) as profile ` + KeyProfile + `, or --profile-name.

Then run bedrock setup --source-profile ` + KeyProfile + `: it reads your email from the IAM user's tags.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := a.writeAccessKey(name, cli.NewPrompter(a.In, a.Out))
			return err
		},
	}
	cmd.Flags().StringVar(&name, "profile-name", KeyProfile, "name of the profile to write")
	return cmd
}
