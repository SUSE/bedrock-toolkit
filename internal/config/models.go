package config

import (
	"slices"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	bedrocktoolkit "github.com/innobead/bedrock-toolkit"
	"github.com/innobead/bedrock-toolkit/internal/policy"
)

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

// TestModels returns the models doctor calls: those in models: the region can call, followed by the
// region's baseline models they don't include (only the baseline when models: is empty). Other
// geographies' inference profiles don't exist in the region, so they aren't tested.
func (c *Config) TestModels() []string {
	var here []string
	for _, m := range c.Models {
		if ModelRegion(c.Region, m) == c.Region {
			here = append(here, m)
		}
	}
	return appendMissing(here, BaselineModels(c.Region))
}

// EnableModels returns the models Step 1.5 enables: models: followed by every baseline model, with
// every prefix (also when models: is empty, though personal roles may then call any Claude model
// except Fable). Each is enabled from ModelRegion, as users in other regions call them too.
func (c *Config) EnableModels() []string {
	return appendMissing(c.Models, BaselineAllGeos())
}

// geoRegions are where Step 1.5 enables another geography's inference profiles.
var geoRegions = map[string]string{"us.": "us-east-1", "eu.": "eu-central-1"}

// ModelRegion returns the region to call a model from: the setup's region, unless the model is a
// us. or eu. inference profile of another geography, which only that geography's regions offer.
func ModelRegion(region, model string) string {
	for geo, r := range geoRegions {
		if strings.HasPrefix(model, geo) && Geo(region) != geo {
			return r
		}
	}
	return region
}

// AllowedModels returns the models personal roles may call: nil when models: is empty (any Claude
// model except Fable), else models: followed by the baseline models it doesn't list, with every
// geography's prefix (BaselineAllGeos): each user's CLI picks the prefix from the user's own region,
// which can differ from the setup's.
func (c *Config) AllowedModels() []string {
	if len(c.Models) == 0 {
		return nil
	}
	return appendMissing(c.Models, BaselineAllGeos())
}

func appendMissing(list, more []string) []string {
	out := slices.Clone(list)
	for _, m := range more {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

var baseline = sync.OnceValues(func() ([]string, error) { return ParseModelsTOML(bedrocktoolkit.ModelsTOML) })

// BaselineModels returns the built-in models.toml entries a region can call: those with the
// region's prefix (us. or eu.), then those with global.; only global. outside the US and EU.
func BaselineModels(region string) []string {
	all := BaselineAllGeos()
	out := []string{}
	for _, geo := range slices.Compact([]string{Geo(region), "global."}) {
		for _, m := range all {
			if strings.HasPrefix(m, geo) {
				out = append(out, m)
			}
		}
	}
	return out
}

// BaselineAllGeos returns every model in the built-in models.toml, whatever its prefix: personal
// roles may call them all, as each user's CLI picks the prefix from the user's own region. An ID
// without a prefix gets us., eu. and global.
func BaselineAllGeos() []string {
	m, err := baseline()
	if err != nil {
		panic("built-in models.toml: " + err.Error()) // TestBaselineModels guards it
	}
	out := []string{}
	for _, id := range m {
		ids := []string{id}
		if policy.FoundationModelID(id) == id {
			ids = []string{"us." + id, "eu." + id, "global." + id}
		}
		for _, id := range ids {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// ParseModelsTOML reads a models.toml, keeping only models personal roles may call.
func ParseModelsTOML(b []byte) ([]string, error) {
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
