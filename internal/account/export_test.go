package account

import (
	"maps"
	"slices"
	"testing"
)

func TestTagUpdate(t *testing.T) {
	have := map[string]string{"bedrock-admin:id": "acme", "org": "x", "pcw_ignore": "0"}
	cases := map[string]struct {
		have, want, merged map[string]string
		details            []string
	}{
		"nothing wanted": {have: have},
		"already set":    {have: have, want: map[string]string{"org": "x"}},
		"missing and changed, others kept": {have: have, want: map[string]string{"pcw_ignore": "1", "team": "qe"},
			merged:  map[string]string{"bedrock-admin:id": "acme", "org": "x", "pcw_ignore": "1", "team": "qe"},
			details: []string{`tag pcw_ignore is "0", the file says "1"`, "tag team missing"}},
		"untagged bucket": {want: map[string]string{"pcw_ignore": "1"},
			merged: map[string]string{"pcw_ignore": "1"}, details: []string{"tag pcw_ignore missing"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			merged, d := tagUpdate(tc.have, tc.want)
			if !maps.Equal(merged, tc.merged) || (merged == nil) != (tc.merged == nil) || !slices.Equal(d, tc.details) {
				t.Fatalf("got %v %q, want %v %q", merged, d, tc.merged, tc.details)
			}
		})
	}
	if have["pcw_ignore"] != "0" {
		t.Fatal("tagUpdate changed its input")
	}
}
