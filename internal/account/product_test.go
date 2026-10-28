package account

import (
	"context"
	"strings"
	"testing"

	"github.com/SUSE/bedrock-toolkit/internal/config"
)

func TestProductWarning(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     config.Config
		want    []string
		noWarns bool
	}{
		{name: "team product", cfg: config.Config{Product: "rancher", Users: []config.User{{Email: "a@example.com"}}}, noWarns: true},
		{name: "old default", cfg: config.Config{Path: "bedrock.yaml", Product: "bedrock"}, want: []string{"set product in bedrock.yaml to"}},
		{name: "user override", cfg: config.Config{Path: "bedrock.yaml", Product: "rancher", Users: []config.User{{Email: "a@example.com", Product: "bedrock"}}},
			want: []string{"set product for a@example.com to"}},
		{name: "both", cfg: config.Config{Path: "bedrock.yaml", Product: "bedrock", Users: []config.User{{Email: "a@example.com", Product: "bedrock"}}},
			want: []string{"in bedrock.yaml and for a@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &Env{Cfg: &tc.cfg}
			items, err := e.product(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if tc.noWarns {
				if len(items) != 0 {
					t.Fatalf("want no item, got %+v", items[0])
				}
				return
			}
			if len(items) != 1 || len(items[0].Warnings) != 1 || items[0].Op.Pending() {
				t.Fatalf("want one ok item with a warning, got %+v", items)
			}
			for _, w := range tc.want {
				if !strings.Contains(items[0].Warnings[0], w) {
					t.Errorf("missing %q in %q", w, items[0].Warnings[0])
				}
			}
		})
	}
}
