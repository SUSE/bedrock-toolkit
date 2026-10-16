package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintChecksWarn(t *testing.T) {
	checks := []Check{
		{Name: "a", OK: true, Detail: "fine"},
		{Name: "b", OK: true, Warn: true, Detail: "can't check", Fix: "ask someone"},
	}
	var w bytes.Buffer
	if err := PrintChecks(&w, checks, false, true); err != nil {
		t.Fatalf("a warning failed doctor: %v", err)
	}
	out := w.String()
	for _, want := range []string{"WARN  b: can't check\n", "      fix: ask someone\n", "All checks passed, with 1 warning."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "fine") {
		t.Errorf("--quiet printed an ok check:\n%s", out)
	}

	w.Reset()
	err := PrintChecks(&w, append(checks, Check{Name: "c", Detail: "broken"}), false, false)
	if err == nil || strings.Contains(w.String(), "All checks passed") {
		t.Fatalf("a failure passed: %v\n%s", err, w.String())
	}

	w.Reset()
	if err := PrintChecks(&w, checks, true, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), `"ok": true`) || !strings.Contains(w.String(), `"warn": true`) {
		t.Errorf("JSON:\n%s", w.String())
	}
}
