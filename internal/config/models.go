package config

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/innobead/bedrock-toolkit/internal/policy"
)

// SuggestedModelsURL is models.toml in the repository: the models configure puts in a new file.
const SuggestedModelsURL = "https://raw.githubusercontent.com/innobead/bedrock-toolkit/main/models.toml"

// Geo returns the region's inference profile prefix: us. or eu., else global. (apac. doesn't offer
// the current Claude models).
func Geo(region string) string {
	geo := strings.SplitN(region, "-", 2)[0]
	if geo != "us" && geo != "eu" {
		geo = "global"
	}
	return geo + "."
}

// WithGeo adds the region's prefix to model IDs that have none.
func WithGeo(region string, models []string) []string {
	out := []string{}
	for _, m := range models {
		if policy.FoundationModelID(m) == m {
			m = Geo(region) + m
		}
		out = append(out, m)
	}
	return out
}

// DefaultModels returns the models bedrock doctor checks and bedrock claude uses for a region:
// Claude Opus, Sonnet and Haiku 5.5, through the region's inference profile.
func DefaultModels(region string) []string {
	return WithGeo(region, []string{"anthropic.claude-opus-5-5", "anthropic.claude-sonnet-5-5", "anthropic.claude-haiku-5-5"})
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

// ParseSuggestedModels reads a models.toml, keeping only models personal roles may call.
func ParseSuggestedModels(b []byte) ([]string, error) {
	var f struct {
		Models []string `toml:"models"`
	}
	if _, err := toml.Decode(string(b), &f); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range f.Models {
		if m = strings.TrimSpace(m); m != "" && policy.AllowedModel(m) {
			out = append(out, m)
		}
	}
	return out, nil
}

// FetchSuggestedModels downloads and parses models.toml from url.
func FetchSuggestedModels(ctx context.Context, url string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, r.Status)
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return ParseSuggestedModels(b)
}
