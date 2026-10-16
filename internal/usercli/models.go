package usercli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/spf13/cobra"

	"github.com/innobead/bedrock-toolkit/internal/awsconf"
	"github.com/innobead/bedrock-toolkit/internal/awsx"
	"github.com/innobead/bedrock-toolkit/internal/config"
	"github.com/innobead/bedrock-toolkit/internal/policy"
)

type modelsJSON struct {
	Region string `json:"region"`
	// AnyClaude is true when the admin allows any Claude model except Fable; Models are then the
	// built-in ones the admin has enabled for sure.
	AnyClaude bool     `json:"any_claude"`
	Models    []string `json:"models"`
	// Source is "role" when read from the personal role's policy, "built-in" when the role can't
	// read it yet and Models are only the built-in models, which are always allowed.
	Source string `json:"source"`
}

func (a *App) modelsCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the models you may use in your profile's region",
		Long: `models reads your personal role's policy, with the bedrock profile, and lists the models your admin
allows that you can call from the profile's region: the us. or eu. inference profiles there,
global. ones everywhere. Use these IDs as the model in Claude Code or the desktop app.

When your admin allows any Claude model except Fable, it lists the built-in models, which your
admin always enables; other Claude models work once your admin has enabled them in the account.
A paused role still lists its models: run bedrock usage to see whether you are paused.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.models(cmd.Context(), profile) },
	}
	cmd.Flags().StringVar(&profile, "profile-name", DefaultProfile, "profile of your personal role")
	cmd.Flags().BoolVar(&a.JSON, "json", false, "print as JSON")
	return cmd
}

func (a *App) models(ctx context.Context, profile string) error {
	_, _, role, err := profileRole(profile)
	if err != nil {
		return err
	}
	region, err := profileRegion(profile)
	if err != nil {
		return err
	}
	out := modelsJSON{Region: region, Source: "role"}
	doc, err := a.AWS.RolePolicy(ctx, profile, role)
	switch {
	case awsx.IsAccessDenied(err):
		out.Source = "built-in"
	case err != nil:
		return fmt.Errorf("reading your personal role's policy: %w", short(err))
	}
	var listed []string
	if out.Source == "role" {
		if listed, err = policy.ListedModels(doc); err != nil {
			return fmt.Errorf("reading your personal role's policy: %w", err)
		}
	}
	out.AnyClaude = out.Source == "role" && listed == nil
	if listed == nil {
		listed = config.BaselineAllGeos()
	}
	out.Models = config.CallableModels(region, listed)

	if a.JSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		a.printf("%s\n", b)
		return nil
	}
	switch {
	case out.Source == "built-in":
		a.printf("Your personal role can't read its policy yet: ask your admin to run bedrock-admin apply.\n" +
			"Until then, these built-in models are the ones you may use for sure")
	case out.AnyClaude:
		a.printf("Your admin allows any %s. These are enabled for you", policy.AllowedModelsText)
	default:
		a.printf("Your admin allows these models")
	}
	a.printf(" in %s (profile %s):\n\n", region, profile)
	for _, m := range out.Models {
		a.printf("  %s\n", m)
	}
	if out.AnyClaude {
		a.printf("\nOther Claude models work once your admin has enabled them in the account.\n")
	}
	return nil
}

// profileRegion returns the region of a profile in ~/.aws/config.
func profileRegion(profile string) (string, error) {
	path, err := awsconf.Path()
	if err != nil {
		return "", err
	}
	file, err := awsconf.Read(path)
	if err != nil {
		return "", err
	}
	s := file.Profile(profile)
	if s == nil || s.Keys["region"] == "" {
		return "", fmt.Errorf("no profile %s with a region in %s: run bedrock setup first", profile, path)
	}
	return s.Keys["region"], nil
}

func (sdkAWS) RolePolicy(ctx context.Context, profile, role string) (string, error) {
	cfg, err := load(ctx, profile, "")
	if err != nil {
		return "", err
	}
	r, err := iam.NewFromConfig(cfg).GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: aws.String(role),
		PolicyName: aws.String(policy.InvokePolicyName)})
	if err != nil {
		return "", err
	}
	return policy.Decode(aws.ToString(r.PolicyDocument)), nil
}
