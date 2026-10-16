package cli

import (
	"fmt"
	"io"
)

// Check is one doctor result.
type Check struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	// Warn marks an OK check that needs attention, but not from this user: it doesn't fail doctor.
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// DoctorJSON is the doctor --json output.
type DoctorJSON struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// PrintChecks prints doctor results and returns Exit(Problem) when one failed.
func PrintChecks(w io.Writer, checks []Check, asJSON, quiet bool) error {
	ok, warnings := true, 0
	for _, c := range checks {
		ok = ok && c.OK
		if c.OK && c.Warn {
			warnings++
		}
	}
	if asJSON {
		if checks == nil {
			checks = []Check{}
		}
		if err := WriteJSON(w, DoctorJSON{OK: ok, Checks: checks}); err != nil {
			return err
		}
	} else {
		for _, c := range checks {
			if c.OK && !c.Warn && quiet {
				continue
			}
			mark := "ok  "
			switch {
			case !c.OK:
				mark = "FAIL"
			case c.Warn:
				mark = "WARN"
			}
			fmt.Fprintf(w, "%s  %s", mark, c.Name)
			if c.Detail != "" {
				fmt.Fprintf(w, ": %s", c.Detail)
			}
			fmt.Fprintln(w)
			if (!c.OK || c.Warn) && c.Fix != "" {
				fmt.Fprintf(w, "      fix: %s\n", c.Fix)
			}
		}
		switch {
		case ok && warnings == 1:
			fmt.Fprintf(w, "\nAll checks passed, with 1 warning.\n")
		case ok && warnings > 1:
			fmt.Fprintf(w, "\nAll checks passed, with %d warnings.\n", warnings)
		case ok:
			fmt.Fprintf(w, "\nAll checks passed.\n")
		}
	}
	if !ok {
		return Exit(Problem)
	}
	return nil
}
