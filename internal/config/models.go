package config

import "strings"

// DefaultModels returns the models bedrock doctor checks and bedrock claude uses for a region:
// Claude Opus, Sonnet and Haiku 5.5, through the region's geographic inference profile (us., eu.),
// or the global one elsewhere (apac. doesn't offer them).
func DefaultModels(region string) []string {
	geo := strings.SplitN(region, "-", 2)[0]
	if geo != "us" && geo != "eu" {
		geo = "global"
	}
	var out []string
	for _, m := range []string{"opus", "sonnet", "haiku"} {
		out = append(out, geo+".anthropic.claude-"+m+"-5-5")
	}
	return out
}

// DefaultTestModel is the model admin checks call when models: is empty: Claude Haiku 5.5 through
// the region's inference profile.
func DefaultTestModel(region string) string {
	return DefaultModels(region)[2]
}

// TestModels returns the models Step 1.5 enables and doctor calls: the listed ones, else Haiku 5.5.
func (c *Config) TestModels() []string {
	if len(c.Models) > 0 {
		return c.Models
	}
	return []string{DefaultTestModel(c.Region)}
}
