package admincli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/fleet"
	"github.com/SUSE/bedrock-toolkit/internal/users"
)

// UserRow is one line of bedrock-admin users.
type UserRow struct {
	Email string `json:"email"`
	Kind  string `json:"kind"`
	// SetUp is the date the personal role was last used, "never" or "not applied".
	SetUp string `json:"set_up"`
	Pause string `json:"pause"`
	// InFile is false for a managed role that apply would offboard.
	InFile bool `json:"in_file"`
	// Command is what to send the user to set up, with --commands, for applied users in the file.
	Command string `json:"command,omitempty"`
}

// UsersJSON is the users --json output.
type UsersJSON struct {
	Users  []UserRow    `json:"users"`
	Totals fleet.Counts `json:"totals"`
	// NotApplied counts users in the file without a role yet.
	NotApplied int `json:"not_applied"`
}

func (a *App) usersCmd() *cobra.Command {
	var commands bool
	cmd := &cobra.Command{
		Use:   "users",
		Short: "Who is onboarded, who has set up their machine, who is paused",
		Long: `users lists every user with their kind (SSO or IAM user), whether they ran bedrock setup and their
pause state. "Set up" is the day their personal role was last used: bedrock setup uses it, so "never"
means they haven't set up yet. AWS updates it with a delay of up to about 4 hours.

Users in the file that apply hasn't created yet show "not applied".

--commands prints the commands to send each user instead, the same ones apply prints when it
onboards them: for a user who lost them or hasn't set up yet.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg, err := a.load()
			if err != nil {
				return err
			}
			c, err := a.clients(ctx, cfg)
			if err != nil {
				return err
			}
			got, err := fleet.Collect(ctx, a.usersEnv(ctx, cfg, c).Pause, c.IAM)
			if err != nil {
				return err
			}
			out := UsersView(cfg, got)
			if commands {
				AddCommands(&out, cfg, c.Account, a.profileRegion(ctx, cfg))
			}
			if a.JSON {
				return cli.WriteJSON(a.Out, out)
			}
			if commands {
				return writeCommands(a.Out, out)
			}
			return writeUsers(a.Out, out)
		},
	}
	cmd.Flags().BoolVar(&commands, "commands", false, "print the commands to send each user to set up")
	return cmd
}

// AddCommands fills in the setup command of the applied users in the file.
func AddCommands(v *UsersJSON, cfg *config.Config, account, profileRegion string) {
	byEmail := map[string]config.Resolved{}
	for _, r := range cfg.Resolve() {
		byEmail[strings.ToLower(r.Email)] = r
	}
	for i, row := range v.Users {
		if r, ok := byEmail[strings.ToLower(row.Email)]; ok && row.InFile && row.SetUp != "not applied" {
			v.Users[i].Command = users.SetupCommand(cfg, account, profileRegion, r)
		}
	}
}

func writeCommands(w io.Writer, v UsersJSON) error {
	_, _ = fmt.Fprintln(w, "Send each user their commands (IAM users also need their access key):")
	for _, r := range v.Users {
		switch {
		case r.Command != "":
			_, _ = fmt.Fprintf(w, "  %s: %s\n", r.Email, r.Command)
		case r.InFile:
			_, _ = fmt.Fprintf(w, "  %s: not applied yet: run apply first\n", r.Email)
		}
	}
	return nil
}

// UsersView joins the users of the file with what the account has.
func UsersView(cfg *config.Config, got []fleet.User) UsersJSON {
	byEmail := map[string]fleet.User{}
	for _, u := range got {
		byEmail[strings.ToLower(u.Email)] = u
	}
	var out UsersJSON
	seen := map[string]bool{}
	for _, r := range cfg.Resolve() {
		key := strings.ToLower(r.Email)
		seen[key] = true
		u, ok := byEmail[key]
		if !ok {
			kind := fleet.KindSSO
			if r.IAMUser != "" {
				kind = fleet.KindIAMUser
			}
			out.Users = append(out.Users, UserRow{Email: r.Email, Kind: kind, SetUp: "not applied", Pause: "-", InFile: true})
			out.NotApplied++
			continue
		}
		out.Users = append(out.Users, userRow(u, true))
	}
	var managed []fleet.User
	for _, u := range got {
		managed = append(managed, u)
		if !seen[strings.ToLower(u.Email)] {
			out.Users = append(out.Users, userRow(u, false))
		}
	}
	out.Totals = fleet.Count(managed)
	if out.Users == nil {
		out.Users = []UserRow{}
	}
	return out
}

func userRow(u fleet.User, inFile bool) UserRow {
	set := "never"
	if u.SetUp() {
		set = u.LastUsed.Format("2006-01-02")
	}
	email := u.Email
	if email == "" {
		email = u.Role
	}
	return UserRow{Email: email, Kind: u.Kind, SetUp: set, Pause: string(u.State), InFile: inFile}
}

func writeUsers(w io.Writer, v UsersJSON) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "EMAIL\tKIND\tSET UP\tPAUSE")
	for _, r := range v.Users {
		email := r.Email
		if !r.InFile {
			email += " (not in the file: apply offboards)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", email, r.Kind, r.SetUp, r.Pause)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	s := "\n" + v.Totals.Text()
	if v.NotApplied > 0 {
		s += fmt.Sprintf("; %d in the file not applied yet", v.NotApplied)
	}
	_, err := fmt.Fprintln(w, s+".")
	return err
}
