package usercli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/sso"
)

func (a *App) loginCmd() *cobra.Command {
	var profile string
	l := a.Login
	if l == nil {
		l = &sso.Login{}
	}
	cmd := sso.Command(l, func() string { return profile }, func() []string {
		return []string{os.Getenv("AWS_PROFILE"), DefaultProfile}
	}, "$AWS_PROFILE, else the\nbedrock profile bedrock setup wrote", "bedrock configure sso (in the commands your admin sent you)")
	cmd.Flags().StringVar(&profile, "profile", "", "profile to sign in to (default: $AWS_PROFILE, the bedrock profile, or the only SSO sign-in)")
	return cmd
}
