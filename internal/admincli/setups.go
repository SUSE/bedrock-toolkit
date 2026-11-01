package admincli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/fleet"
	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/users"
)

// SetupsJSON is the setups --json output.
type SetupsJSON struct {
	Account string `json:"account"`
	// Current is the id of the loaded file, if any.
	Current string               `json:"current,omitempty"`
	Setups  []fleet.SetupSummary `json:"setups"`
}

func (a *App) setupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setups",
		Short: "Every setup (id) in the account, with its users, last use and spend",
		Long: `setups lists every setup in the account of the profile, whatever its id: one per team or tenant,
or an old one left behind. It needs no bedrock.yaml; with one, the profile's account must be the
file's account, and the file's id is marked *.

A setup is idle when none of its personal roles has been used for 30 days (and it is older than
that). To remove a leftover setup:

  bedrock-admin export --id <id> > <id>.yaml
  # set users: [] in <id>.yaml
  bedrock-admin apply -f <id>.yaml --yes
  bedrock-admin uninstall -f <id>.yaml`,
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
			c, err := a.connect(ctx, base, base.Region, base.Account)
			if err != nil {
				return err
			}
			infos, err := fleet.ListSetups(ctx, c.IAM)
			if err != nil {
				return fmt.Errorf("listing setups: %w", err)
			}
			a.info("Reading %d setups...\n", len(infos))
			got, err := fleet.CollectSetups(ctx, infos, func(id string) *pause.Manager { return users.NewPause(c, id) }, c.IAM, a.now())
			if err != nil {
				return err
			}
			out := SetupsJSON{Account: c.Account, Current: base.ID, Setups: got}
			if a.JSON {
				return cli.WriteJSON(a.Out, out)
			}
			return writeSetups(a.Out, out, a.profileLabel(base))
		},
	}
}

// profileLabel names the profile in use, for headers.
func (a *App) profileLabel(cfg *config.Config) string {
	if p := a.profile(cfg); p != "" {
		return "profile " + p
	}
	if p := os.Getenv("AWS_PROFILE"); p != "" {
		return "profile " + p
	}
	return "default credentials"
}

func writeSetups(w io.Writer, v SetupsJSON, profile string) error {
	_, _ = fmt.Fprintf(w, "Setups in account %s (%s)\n", v.Account, profile)
	if len(v.Setups) == 0 {
		_, err := fmt.Fprintln(w, "None: no bedrock-deny-<id> policy in this account.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tPRODUCT\tUSERS\tPAUSED\tLAST USED\tSPEND (USD)\tBUDGET (USD)\t")
	var idle []string
	current := false
	for _, s := range v.Setups {
		id := s.ID
		if s.ID == v.Current {
			id += " *"
			current = true
		}
		last := "never"
		if !s.LastUsed.IsZero() {
			last = s.LastUsed.Format(time.DateOnly)
		}
		product := s.Product
		if product == "" {
			product = "-"
		}
		flag := ""
		if s.Idle {
			flag = "idle"
			idle = append(idle, s.ID)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%.2f\t%.2f\t%s\n", id, product, s.Users, s.Paused, last, s.SpendUSD, s.LimitUSD, flag)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if current {
		_, _ = fmt.Fprintln(w, "* the id of the loaded bedrock.yaml.")
	}
	_, _ = fmt.Fprintln(w, "LAST USED is the latest use of any personal role (AWS updates it within about 4 hours). SPEND is this month's, from the users' budgets.")
	if len(idle) > 0 {
		_, _ = fmt.Fprintf(w, "idle: no personal role used for 30+ days (%s). If one is a leftover, remove it:\n", strings.Join(idle, ", "))
		_, _ = fmt.Fprintln(w, "  bedrock-admin export --id <id> > <id>.yaml, set users: [] in it, then")
		_, _ = fmt.Fprintln(w, "  bedrock-admin apply -f <id>.yaml --yes && bedrock-admin uninstall -f <id>.yaml")
	}
	return nil
}

// idleNotes are plan's notes about other idle setups in the account.
func idleNotes(own string, setups []fleet.SetupSummary) []string {
	var out []string
	for _, s := range setups {
		if s.ID != own && s.Idle {
			out = append(out, fmt.Sprintf("Setup %s in this account has had no personal role used for 30+ days: see bedrock-admin setups.", s.ID))
		}
	}
	return out
}
