# Agent instructions

Instructions for coding agents working in this repository. The layout, build commands and release
steps are in [CONTRIBUTING.md](CONTRIBUTING.md). How and why the tools work is in
[design.md](design.md).

## Before you finish a change

- Run `make generate` once in a fresh clone, then `go vet ./...`, `golangci-lint run ./...` and
  `go test ./...`. These must pass. They don't touch AWS.
- Add or update unit tests for behaviour you change. Validation, parsing, plan diffs and report
  formatting belong in unit tests, not e2e tests.
- Update the docs that describe the changed behaviour: `README.md`, `admin-guide.md`,
  `central-admin-guide.md`, `user-guide.md`, `design.md` and `tests/e2e/README.md`. Samples of
  command output must match what the binaries print.

## AWS safety

- Do not run anything that creates, changes or deletes AWS resources without the user's approval.
  That includes `bedrock-admin apply`, `uninstall`, `users`, `pause`, `fleet init`, and every e2e
  suite except `cli`. These use real accounts and cost money.
- Read-only commands, such as `plan`, `usage`, `fleet report` and `aws ... describe/list/get`, are
  fine.
- Never commit real config files (`bedrock-*.yaml` at the repository root) or real account IDs,
  emails or bucket names. In docs and tests, use placeholders such as account `111122223333` and
  `achen@example.com`.

## End-to-end tests

- Run them with `tests/e2e/run.sh <suite> [--test <pattern>]`. Suites: `cli`, `account`, `users`,
  `user-access`, `usage`, `billing`. CI doesn't run them.
- E2e tests cover only behaviour that needs real AWS. Leave the rest to unit tests.
- Run long suites in a background shell and keep the log, not in a terminal that might close.
  A killed run leaves resources behind. Clean them up with `bedrock-admin uninstall --delete-data`.
  Versioned buckets also need every object version and delete marker removed.
- Robot variable names are case-insensitive, and `Set Global Variable` overwrites locals and
  arguments with the same name. Don't use names such as `${id}` or `${a}` that clash with global or
  suite variables.
- IAM trust changes take a while to apply. Wait about 20 seconds after `UpdateAssumeRolePolicy`
  before calling `AssumeRole`, and retry.

## Commits and releases

- Use Conventional Commits (`feat:`, `fix:`, `docs:`, `test(e2e):`, `fix(deps):`, …), signed off
  with `git commit -s`. Make one commit per logical change.
- Don't push, tag or release unless the user asks. To release, push an annotated `v*` tag.
