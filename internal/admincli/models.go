package admincli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	btypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/spf13/cobra"

	"github.com/innobead/bedrock-toolkit/internal/awsx"
	"github.com/innobead/bedrock-toolkit/internal/cli"
	"github.com/innobead/bedrock-toolkit/internal/config"
	"github.com/innobead/bedrock-toolkit/internal/policy"
)

// modelRow is one inference profile users could be allowed to call.
type modelRow struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// Users is "allowed" when personal roles may call it under the file's models:, "not listed" when
	// models: lists others, and "never" for models personal roles can't call (Fable).
	Users string `json:"users"`
}

func (a *App) modelsCmd() *cobra.Command {
	var region string
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the Claude inference profiles in the region, for models: in the file",
		Long: `models lists the Claude inference profiles the region offers. Their IDs are what models: in
bedrock.yaml takes. The USERS column says whether personal roles may call each one under the file:
"allowed" (models: is empty, or lists it), "not listed" (models: lists others) or "never" (Fable).

  bedrock-admin models
  bedrock-admin models --region eu-west-1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg := &config.Config{}
			if path, err := config.Find(a.File); err == nil {
				if cfg, err = config.Parse(path); err != nil {
					return err
				}
			} else if a.File != "" {
				return err
			}
			if region != "" {
				cfg.Region = region
			}
			if cfg.Region == "" {
				cfg.Region = a.profileRegion(ctx, cfg)
			}
			if cfg.Region == "" {
				return errors.New("no region: pass --region")
			}
			c, err := awsx.New(ctx, a.profile(cfg), cfg.Region, cfg.Account)
			if err != nil {
				return err
			}
			profiles, err := claudeProfiles(ctx, c.Bedrock)
			if err != nil {
				return err
			}
			rows := modelRows(profiles, cfg.Models)
			if a.JSON {
				return cli.WriteJSON(a.Out, rows)
			}
			return a.printModels(cfg, rows)
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "region (default: the file's, else the profile's)")
	return cmd
}

// claudeProfiles lists the region's system-defined Claude inference profiles.
func claudeProfiles(ctx context.Context, c *bedrock.Client) ([]btypes.InferenceProfileSummary, error) {
	var out []btypes.InferenceProfileSummary
	p := bedrock.NewListInferenceProfilesPaginator(c, &bedrock.ListInferenceProfilesInput{TypeEquals: btypes.InferenceProfileTypeSystemDefined})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list inference profiles: %w", err)
		}
		for _, s := range r.InferenceProfileSummaries {
			if strings.Contains(aws.ToString(s.InferenceProfileId), "anthropic.claude-") {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// modelRows sorts the profiles by ID and says whether personal roles may call each under models.
func modelRows(profiles []btypes.InferenceProfileSummary, models []string) []modelRow {
	rows := []modelRow{}
	for _, s := range profiles {
		id := aws.ToString(s.InferenceProfileId)
		r := modelRow{ID: id, Name: aws.ToString(s.InferenceProfileName), Status: string(s.Status), Users: "allowed"}
		switch {
		case !policy.AllowedModel(id):
			r.Users = "never"
		case len(models) > 0 && !slices.Contains(models, id):
			r.Users = "not listed"
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func (a *App) printModels(cfg *config.Config, rows []modelRow) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintf(a.Out, "No Claude inference profiles in %s.\n", cfg.Region)
		return err
	}
	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL ID\tNAME\tSTATUS\tUSERS")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Status, r.Users)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	var missing []string
	for _, m := range cfg.Models {
		if !slices.ContainsFunc(rows, func(r modelRow) bool { return r.ID == m }) {
			missing = append(missing, m)
		}
	}
	if !a.Quiet {
		if len(cfg.Models) == 0 {
			fmt.Fprintf(a.Out, "\nmodels: is empty, so personal roles may call any Claude model except Fable. List IDs\n"+
				"under models: to allow only those; doctor then test-calls them instead of %s.\n", config.DefaultTestModel(cfg.Region))
		}
		if len(missing) > 0 {
			fmt.Fprintf(a.Out, "\nNot inference profiles in %s (foundation model IDs, or typos): %s\n", cfg.Region, strings.Join(missing, ", "))
		}
	}
	return nil
}
