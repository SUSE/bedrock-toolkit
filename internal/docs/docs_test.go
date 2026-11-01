// Package docs_test checks the Markdown docs against the CLIs' command trees, so a renamed command,
// a removed flag or a newly required one fails go test instead of going stale in the docs.
package docs_test

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/admincli"
	"github.com/SUSE/bedrock-toolkit/internal/usercli"
)

const repo = "../.."

var docs = []string{"README.md", "admin-guide.md", "central-admin-guide.md", "user-guide.md", "design.md", "CONTRIBUTING.md"}

// roots returns fresh command trees, keyed by binary name: parsed flags keep their values, so each
// check needs new ones.
func roots() map[string]*cobra.Command {
	return map[string]*cobra.Command{
		"bedrock-admin": admincli.New("test").Root(),
		"bedrock":       usercli.New("test").Root(),
	}
}

// oneRequired lists flags a command checks itself in RunE, where cobra can't see them: at least
// one flag of each group must be set. Add a command here when it gets such a check.
var oneRequired = map[string][][]string{
	"bedrock setup": {{"id", "role-arn"}},
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func commands(root *cobra.Command) []string {
	var names []string
	for _, c := range root.Commands() {
		if !c.Hidden && c.Name() != "help" && c.Name() != "completion" {
			names = append(names, c.Name())
		}
	}
	return names
}

// tableCommands returns the commands in the first column of the Markdown tables in doc, without
// prefix.
func tableCommands(doc, prefix string) []string {
	re := regexp.MustCompile("(?m)^\\| `" + regexp.QuoteMeta(prefix) + "([a-z-]+)` \\|")
	var names []string
	for _, m := range re.FindAllStringSubmatch(doc, -1) {
		names = append(names, m[1])
	}
	return names
}

// section returns the text of doc from the line starting with start to the next line starting
// with end.
func section(t *testing.T, doc, start, end string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+start)
	if i < 0 {
		t.Fatalf("no %q", start)
	}
	s := doc[i+1+len(start):]
	if j := strings.Index(s, "\n"+end); j >= 0 {
		s = s[:j]
	}
	return s
}

func TestCommandTables(t *testing.T) {
	r := roots()
	admin, user := commands(r["bedrock-admin"]), commands(r["bedrock"])
	readme := read(t, "README.md")
	cases := []struct {
		doc, prefix, text string
		want              []string
	}{
		{"README.md (bedrock-admin)", "", section(t, readme, "`bedrock-admin`, for the account admin:", "`bedrock`, for users:"), admin},
		{"README.md (bedrock)", "", section(t, readme, "`bedrock`, for users:", "## "), user},
		// fleet is for the central admin, in central-admin-guide.md.
		{"admin-guide.md", "bedrock-admin ", section(t, read(t, "admin-guide.md"), "## Commands", "## "),
			slices.DeleteFunc(slices.Clone(admin), func(s string) bool { return s == "fleet" })},
		{"user-guide.md", "bedrock ", section(t, read(t, "user-guide.md"), "## Commands", "## "), user},
	}
	for _, c := range cases {
		got := tableCommands(c.text, c.prefix)
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: the command table lacks %s%s", c.doc, c.prefix, w)
			}
		}
		for _, g := range got {
			if !slices.Contains(c.want, g) {
				t.Errorf("%s: the command table lists %s%s, which isn't a command", c.doc, c.prefix, g)
			}
		}
	}
	design := read(t, "design.md")
	for bin, names := range map[string][]string{"bedrock-admin": admin, "bedrock": user} {
		row := regexp.MustCompile("(?m)^\\| `" + bin + "` \\|.*$").FindString(design)
		if row == "" {
			t.Fatalf("design.md: no CLI row for %s", bin)
		}
		for _, n := range names {
			if !strings.Contains(row, "`"+n+"`") {
				t.Errorf("design.md: the %s row lacks `%s`", bin, n)
			}
		}
	}
	central := read(t, "central-admin-guide.md")
	fleet, _, err := r["bedrock-admin"].Find([]string{"fleet"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range commands(fleet) {
		if !strings.Contains(central, "bedrock-admin fleet "+n) {
			t.Errorf("central-admin-guide.md lacks bedrock-admin fleet %s", n)
		}
	}
}

// TestDocExamples parses every bedrock or bedrock-admin command line in the docs' code blocks, as
// the CLI would, without running it.
func TestDocExamples(t *testing.T) {
	n := 0
	for _, doc := range docs {
		for _, ex := range examples(t, doc) {
			n++
			if err := check(ex.args); err != nil {
				t.Errorf("%s:%d: %s: %v", doc, ex.line, strings.Join(ex.args, " "), err)
			}
		}
	}
	t.Logf("%d examples", n)
	if n < 50 {
		t.Errorf("only %d examples found; is the parser broken?", n)
	}
}

// TestDocInlineCommands checks the commands in inline code, such as `bedrock setup --yes`, for
// unknown commands and flags. Prose often shows only the option it explains, so missing required
// flags and arguments aren't errors here.
func TestDocInlineCommands(t *testing.T) {
	re := regexp.MustCompile("`(bedrock(?:-admin)? [^`]+)`")
	n := 0
	for _, doc := range docs {
		for i, line := range strings.Split(read(t, doc), "\n") {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				args := split(m[1])
				if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "<command") }) {
					continue
				}
				n++
				if _, _, err := parse(args); err != nil && !strings.HasPrefix(err.Error(), "flag needs an argument") {
					t.Errorf("%s:%d: %s: %v", doc, i+1, m[1], err)
				}
			}
		}
	}
	t.Logf("%d inline commands", n)
}

type example struct {
	line int
	args []string
}

// examples returns the command lines in the doc's fenced code blocks: every line in bash and sh
// blocks, and lines with a "$ " prompt in the others, where the rest is output.
func examples(t *testing.T, doc string) []example {
	t.Helper()
	var out []example
	sc := bufio.NewScanner(strings.NewReader(read(t, doc)))
	inBlock, prompted, pending, start := false, false, "", 0
	for ln := 1; sc.Scan(); ln++ {
		trim := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(trim, "```") {
			lang := strings.TrimPrefix(trim, "```")
			inBlock, prompted = !inBlock, lang != "bash" && lang != "sh"
			continue
		}
		if !inBlock {
			continue
		}
		if pending != "" {
			pending += " " + trim
		} else {
			if prompted {
				var ok bool
				if trim, ok = strings.CutPrefix(trim, "$ "); !ok {
					continue
				}
			}
			if first, _, _ := strings.Cut(trim, " "); first != "bedrock" && first != "bedrock-admin" {
				continue
			}
			pending, start = trim, ln
		}
		if strings.HasSuffix(pending, "\\") {
			pending = strings.TrimSuffix(pending, "\\")
			continue
		}
		out = append(out, example{start, split(pending)})
		pending = ""
	}
	return out
}

// split cuts a shell line into words, up to a comment, pipe, redirect or "&&", and drops quotes.
func split(s string) []string {
	var words []string
	var cur strings.Builder
	quote, quoted := rune(0), false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, quoted = r, true
		case r == ' ' || r == '\t':
			if cur.Len() > 0 || quoted {
				words = append(words, cur.String())
				cur.Reset()
				quoted = false
			}
		case strings.ContainsRune("#|>;&", r) && cur.Len() == 0 && !quoted:
			return words
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 || quoted {
		words = append(words, cur.String())
	}
	return words
}

// parse finds the command of args and parses its flags. "..." stands for more options, so args are
// parsed only up to it.
func parse(args []string) (*cobra.Command, bool, error) {
	partial := false
	if i := slices.Index(args, "..."); i >= 0 {
		// After a flag, "..." is its value.
		if strings.HasPrefix(args[i-1], "-") && !strings.Contains(args[i-1], "=") {
			i++
		}
		args, partial = args[:i], true
	}
	cmd, rest, err := roots()[args[0]].Find(args[1:])
	if err != nil {
		return nil, false, err
	}
	cmd.InitDefaultHelpFlag()
	cmd.InitDefaultVersionFlag()
	return cmd, partial, cmd.ParseFlags(rest)
}

// check parses args as the CLI would, and checks the arguments and required flags too.
func check(args []string) error {
	cmd, partial, err := parse(args)
	if err != nil {
		return err
	}
	if partial || cmd.Flags().Changed("help") || cmd.Flags().Changed("version") {
		return nil
	}
	if !cmd.Runnable() {
		return fmt.Errorf("%s needs a subcommand", cmd.CommandPath())
	}
	if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
		return err
	}
	if err := cmd.ValidateRequiredFlags(); err != nil {
		return err
	}
	for _, group := range oneRequired[cmd.CommandPath()] {
		if !slices.ContainsFunc(group, func(f string) bool { return cmd.Flags().Changed(f) }) {
			return fmt.Errorf("needs --%s", strings.Join(group, " or --"))
		}
	}
	return nil
}
