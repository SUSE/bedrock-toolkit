package admincli

import "testing"

func TestExportNotify(t *testing.T) {
	const owner = "owner@example.com"
	const lead = "lead@example.com"
	const ops = "ops@example.com"
	for _, tc := range []struct {
		name       string
		emails     []string
		configured string
		want       string
	}{
		{"configured primary", []string{ops, owner, lead}, lead, lead},
		{"owner without config", []string{ops, owner}, "", owner},
		{"owner absent", []string{lead, ops}, owner, lead},
		{"no observed recipients", nil, lead, owner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exportNotify(tc.emails, tc.configured, owner); got != tc.want {
				t.Errorf("primary recipient = %q, want %q", got, tc.want)
			}
		})
	}
}
