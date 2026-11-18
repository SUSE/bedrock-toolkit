package sso

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
)

// Command is the login command of both CLIs. profile returns the --profile flag the caller
// defines, and defaults the profiles to try when it is empty (see Resolve), which defaultsHelp
// describes. createHelp names the command that creates the profile.
func Command(l *Login, profile func() string, defaults func() []string, defaultsHelp, createHelp string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in again to your AWS SSO profile in the browser (replaces aws sso login)",
		Long: `login signs in to an SSO profile in ~/.aws/config (or $AWS_CONFIG_FILE) the way aws sso login does,
without the AWS CLI: it opens the browser on the sign-in page with the code filled in, waits until
you approve, then writes the token to ~/.aws/sso/cache, where the AWS CLI and SDKs read it.

Without --profile it signs in to ` + defaultsHelp + `.
Failing those, it signs in to the only SSO sign-in in the file: its one SSO profile, or several
that share one sso-session. A profile that assumes a role signs in to the SSO profile behind its
source_profile.

Where no browser can open (an SSH session), it prints the URL and the code to enter.

login only signs in again: ` + createHelp + ` creates the profile.`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().BoolVar(&l.NoBrowser, "no-browser", false, "don't open a browser; only print the URL and the code")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		path, err := awsconf.Path()
		if err != nil {
			return err
		}
		file, err := awsconf.Read(path)
		if err != nil {
			return err
		}
		if l.Out == nil {
			l.Out = cmd.OutOrStdout()
		}
		if profile() == "" && len(file.SSOProfiles()) == 0 {
			return fmt.Errorf("no SSO profile in %s: %s creates it", path, createHelp)
		}
		s, err := Resolve(file, path, profile(), defaults()...)
		if err != nil {
			return err
		}
		return l.Run(cmd.Context(), s)
	}
	return cmd
}

// ConfigureCommand is the configure sso command of both CLIs: it creates an SSO profile the way
// aws configure sso does, named defaultName without --profile-name. next is what to run after it.
func ConfigureCommand(l *Login, defaultName, next string) *cobra.Command {
	var np NewProfile
	cmd := &cobra.Command{
		Use:   "sso",
		Short: "Create your SSO profile in ~/.aws/config and sign in (replaces aws configure sso)",
		Long: `configure sso creates an SSO profile in ~/.aws/config (or $AWS_CONFIG_FILE) the way aws configure sso
does, without the AWS CLI: it signs you in in the browser, picks the account and the permission set
(asking when you may use several and didn't pass --account or --role) and writes the profile and
its [sso-session]. On a terminal it asks for a missing --start-url or --sso-region.

The profile is named ` + defaultName + ` unless you pass --profile-name. When the file already has
a profile with the same sign-in, account and role, configure sso signs in to it instead.

Your admin knows the start URL and the region of IAM Identity Center (they are on the IAM Identity
Center Settings page of the AWS console). After that, login signs in again when the session expires.`,
		Args: cobra.NoArgs,
	}
	fl := cmd.Flags()
	fl.StringVar(&np.StartURL, "start-url", "", "SSO start URL (https://<name>.awsapps.com/start)")
	fl.StringVar(&np.Region, "sso-region", "", "region of IAM Identity Center")
	fl.StringVar(&np.Account, "account", "", "account ID (default: the only one you may use, else asks)")
	fl.StringVar(&np.Role, "role", "", "permission set (default: the only one you may use, else asks)")
	fl.StringVar(&np.ProfileRegion, "region", "", "region of the profile (default: --sso-region)")
	fl.StringVar(&np.Name, "profile-name", "", "name of the profile (default "+defaultName+")")
	fl.BoolVar(&l.NoBrowser, "no-browser", false, "don't open a browser; only print the URL and the code")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		in, out := cmd.InOrStdin(), cmd.OutOrStdout()
		terminal := cli.IsTerminal(in)
		p := cli.NewPrompter(in, out)
		var err error
		if np.StartURL == "" && terminal {
			if np.StartURL, err = p.Ask("SSO start URL (https://<name>.awsapps.com/start)", ""); err != nil {
				return err
			}
		}
		if np.StartURL == "" {
			return errors.New("no --start-url: pass your SSO start URL (your admin knows it)")
		}
		if np.Region == "" && terminal {
			if np.Region, err = p.Ask("Region of IAM Identity Center", ""); err != nil {
				return err
			}
		}
		if np.Region == "" {
			return errors.New("no --sso-region: pass the region of IAM Identity Center (your admin knows it)")
		}
		path, err := awsconf.Path()
		if err != nil {
			return err
		}
		file, err := awsconf.Read(path)
		if err != nil {
			return err
		}
		existed := map[string]bool{}
		for _, n := range file.SSOProfiles() {
			existed[n] = true
		}
		if l.Out == nil {
			l.Out = out
		}
		if l.Choose == nil && terminal {
			l.Choose = Chooser(in, out)
		}
		np.DefaultName, np.AccountFlag, np.RoleFlag = defaultName, "--account", "--role"
		name, err := l.Create(cmd.Context(), file, path, np)
		if err != nil {
			return err
		}
		if existed[name] {
			_, _ = fmt.Fprintf(out, "Profile %s in %s already has this sign-in, and you are signed in.\n", name, path)
		}
		_, _ = fmt.Fprintf(out, "Next: %s\n", strings.ReplaceAll(next, "<profile>", name))
		return nil
	}
	return cmd
}

// Chooser asks on a terminal which of the options to take, by number or value.
func Chooser(in io.Reader, out io.Writer) func(string, []string) (string, error) {
	p := cli.NewPrompter(in, out)
	return func(question string, opts []string) (string, error) {
		for i, o := range opts {
			_, _ = fmt.Fprintf(out, "  %d) %s\n", i+1, o)
		}
		for {
			ans, err := p.Ask(question, "1")
			if err != nil {
				return "", err
			}
			if n, err := strconv.Atoi(ans); err == nil && n >= 1 && n <= len(opts) {
				return opts[n-1], nil
			}
			for _, o := range opts {
				if strings.Fields(o)[0] == ans {
					return o, nil
				}
			}
			_, _ = fmt.Fprintf(out, "Enter a number from 1 to %d.\n", len(opts))
		}
	}
}
