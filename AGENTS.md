# Agent instructions

Build, test and layout: see [CONTRIBUTING.md](CONTRIBUTING.md).

- Ask the user before any command that creates, changes or deletes AWS resources, including every
  e2e suite except `cli`. Read-only commands are fine.
- Never commit the `bedrock-*.yaml` files at the repository root, or real account IDs, emails or
  bucket names. Use placeholders such as `111122223333` and `achen@example.com`.
- E2e tests cover only behaviour that needs real AWS, never output layout. Everything else goes in
  unit tests.
- Before committing a change to a command, flag, error message or output, run
  `grep -rn -- '<old name or text>' *.md tests/e2e/` and update every hit: the guides, the cheat
  sheets, `design.md` (including its e2e table) and the suites. `go test ./internal/docs` checks
  the command tables and the doc examples, but not prose.
- Robot variable names are case-insensitive, and `Set Global Variable` overwrites locals with the
  same name. Avoid names such as `${id}` or `${a}`.
- Wait about 20 seconds after an IAM trust change before calling `AssumeRole`.
- Run e2e suites in a background shell. If a run is killed, clean up with
  `bedrock-admin uninstall --delete-data`.
- Commits: Conventional Commits, signed off with `git commit -s`. Don't push, tag or release
  unless asked.
