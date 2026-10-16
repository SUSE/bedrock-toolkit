package admincli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
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
	// Users is "allowed" when personal roles may call it under the file's models: (plus the baseline
	// models), "not listed" when they may call others, and "never" for models policy.AllowedModel rejects.
	Users string `json:"users"`
	// Regions, with --regions, are where an allowed profile routes requests. It is ["any"] for global.
	// profiles, which AWS may route to any commercial region.
	Regions []string `json:"regions,omitempty"`
}

// anyRegion stands for every commercial region, where global. inference profiles route requests.
const anyRegion = "any"

func (a *App) modelsCmd() *cobra.Command {
	var region, filter string
	var regions bool
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the inference profiles in the region, for models: in the file",
		Long: `models lists the inference profiles the region offers, from every provider. Their IDs are what
models: in bedrock.yaml takes. --filter keeps those whose ID or name contains the text (any case).
The USERS column says whether personal roles may call each one under the file: "allowed" (models:
is empty, or it or the built-in baseline models list it), "not listed" (they list others) or
"never" (personal roles may call only Claude models, and never Fable).

--regions adds a REGIONS column: the regions each allowed profile sends requests to, as AWS reports
them now (they can change). A global. profile may go to any commercial region. This shows where
users' requests are processed.

It lists only the region's profiles: eu. profiles exist only in EU regions, for example. Users can
pick another region in bedrock setup; use --region to see what they get there.

  bedrock-admin models
  bedrock-admin models --filter claude
  bedrock-admin models --filter claude --regions
  bedrock-admin models --filter nova --region eu-west-1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg := &config.Config{}
			path, findErr := config.Find(a.File)
			if findErr == nil {
				var err error
				if cfg, err = config.Parse(path); err != nil {
					return err
				}
			}
			if region != "" {
				cfg.Region = region
			}
			if cfg.Region == "" {
				cfg.Region = a.profileRegion(ctx, cfg)
			}
			if cfg.Region == "" {
				if findErr != nil {
					return fmt.Errorf("%w; or pass --region (the AWS profile has no region either)", findErr)
				}
				return fmt.Errorf("%s has no region: pass --region", path)
			}
			c, err := awsx.New(ctx, a.profile(cfg), cfg.Region, cfg.Account)
			if err != nil {
				return err
			}
			profiles, err := inferenceProfiles(ctx, c.Bedrock)
			if err != nil {
				return err
			}
			rows := modelRows(profiles, cfg.AllowedModels(), filter)
			if regions {
				if err := routeRegions(ctx, c.Bedrock, rows); err != nil {
					return err
				}
			}
			if a.JSON {
				return cli.WriteJSON(a.Out, rows)
			}
			return a.printModels(cfg, rows, filter, regions)
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "region (default: the file's, else the profile's)")
	cmd.Flags().StringVar(&filter, "filter", "", "only models whose ID or name contains this text (any case)")
	cmd.Flags().BoolVar(&regions, "regions", false, "show the regions each allowed model sends requests to")
	return cmd
}

// inferenceProfiles lists the region's system-defined inference profiles.
func inferenceProfiles(ctx context.Context, c *bedrock.Client) ([]btypes.InferenceProfileSummary, error) {
	var out []btypes.InferenceProfileSummary
	p := bedrock.NewListInferenceProfilesPaginator(c, &bedrock.ListInferenceProfilesInput{TypeEquals: btypes.InferenceProfileTypeSystemDefined})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list inference profiles: %w", err)
		}
		out = append(out, r.InferenceProfileSummaries...)
	}
	return out, nil
}

// modelRows keeps the profiles whose ID or name contains filter (any case), sorts them by ID and says
// whether personal roles may call each under models.
func modelRows(profiles []btypes.InferenceProfileSummary, models []string, filter string) []modelRow {
	rows := []modelRow{}
	filter = strings.ToLower(filter)
	for _, s := range profiles {
		id, name := aws.ToString(s.InferenceProfileId), aws.ToString(s.InferenceProfileName)
		if filter != "" && !strings.Contains(strings.ToLower(id), filter) && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}
		r := modelRow{ID: id, Name: name, Status: string(s.Status), Users: "allowed"}
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

// routeRegions fills in Regions for the rows personal roles may call: the regions of the profile's
// foundation models, or anyRegion for a global. profile (AWS lists only the calling region for those).
func routeRegions(ctx context.Context, c *bedrock.Client, rows []modelRow) error {
	var wg sync.WaitGroup
	errs := make([]error, len(rows))
	sem := make(chan struct{}, 8)
	for i := range rows {
		if rows[i].Users != "allowed" {
			continue
		}
		if strings.HasPrefix(rows[i].ID, "global.") {
			rows[i].Regions = []string{anyRegion}
			continue
		}
		wg.Add(1)
		go func(r *modelRow, err *error) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, e := c.GetInferenceProfile(ctx, &bedrock.GetInferenceProfileInput{InferenceProfileIdentifier: aws.String(r.ID)})
			if e != nil {
				*err = fmt.Errorf("get inference profile %s: %w", r.ID, e)
				return
			}
			r.Regions = profileRegions(out.Models)
		}(&rows[i], &errs[i])
	}
	wg.Wait()
	return errors.Join(errs...)
}

// profileRegions returns the sorted, distinct regions of the profile's foundation model ARNs
// (arn:aws:bedrock:<region>::foundation-model/<id>).
func profileRegions(models []btypes.InferenceProfileModel) []string {
	var out []string
	for _, m := range models {
		if parts := strings.Split(aws.ToString(m.ModelArn), ":"); len(parts) > 3 && parts[3] != "" && !slices.Contains(out, parts[3]) {
			out = append(out, parts[3])
		}
	}
	sort.Strings(out)
	return out
}

// regionsCell is the REGIONS column for a row.
func regionsCell(r modelRow) string {
	switch {
	case len(r.Regions) == 0:
		return "-"
	case r.Regions[0] == anyRegion:
		return "any region (global)"
	}
	return strings.Join(r.Regions, " ")
}

func (a *App) printModels(cfg *config.Config, rows []modelRow, filter string, regions bool) error {
	if len(rows) == 0 {
		if filter != "" {
			_, err := fmt.Fprintf(a.Out, "No inference profiles in %s match %q.\n", cfg.Region, filter)
			return err
		}
		_, err := fmt.Fprintf(a.Out, "No inference profiles in %s.\n", cfg.Region)
		return err
	}
	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	head := "MODEL ID\tNAME\tSTATUS\tUSERS"
	if regions {
		head += "\tREGIONS"
	}
	fmt.Fprintln(w, head)
	for _, r := range rows {
		line := fmt.Sprintf("%s\t%s\t%s\t%s", r.ID, r.Name, r.Status, r.Users)
		if regions {
			line += "\t" + regionsCell(r)
		}
		fmt.Fprintln(w, line)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	var missing []string
	for _, m := range cfg.Models {
		if filter != "" {
			break // a filtered list can't tell a typo from a model filtered out
		}
		if !slices.ContainsFunc(rows, func(r modelRow) bool { return r.ID == m }) {
			missing = append(missing, m)
		}
	}
	if !a.Quiet {
		if len(cfg.Models) == 0 {
			fmt.Fprintf(a.Out, "\nmodels: is empty, so personal roles may call any Claude model except Fable. List IDs\n"+
				"under models: to allow only those plus the baseline models (%s), which apply\n"+
				"enables and doctor test-calls either way.\n", strings.Join(config.BaselineModels(cfg.Region), ", "))
		}
		if len(missing) > 0 {
			fmt.Fprintf(a.Out, "\nNot inference profiles in %s (foundation model IDs, or typos): %s\n", cfg.Region, strings.Join(missing, ", "))
		}
	}
	return nil
}
