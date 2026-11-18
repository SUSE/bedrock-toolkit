package admincli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/sso"
)

// loginCmd signs in to the admin profile: --profile, then $AWS_PROFILE, then profile: in the
// config file, then the only SSO sign-in.
func (a *App) loginCmd() *cobra.Command {
	l := a.Login
	if l == nil {
		l = &sso.Login{}
	}
	return sso.Command(l, func() string { return a.Profile }, func() []string {
		defaults := []string{os.Getenv("AWS_PROFILE")}
		if path, err := config.Find(a.File); err == nil {
			if cfg, err := config.Parse(path); err == nil {
				defaults = append(defaults, cfg.Profile)
			}
		}
		return defaults
	}, "$AWS_PROFILE, else the profile:\nin bedrock.yaml", "bedrock-admin configure sso")
}
