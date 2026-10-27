# Contributing

How the repository is laid out, how to build and test it, and how to release. What the project does
is in the [README](README.md).

## Repository layout

| Path | What it is |
| --- | --- |
| [`user-guide.md`](user-guide.md) | User setup, troubleshooting, what happens when you're paused |
| [`admin-guide.md`](admin-guide.md) | Account setup, managing users, pauses, spend, cheat sheet |
| [`central-admin-guide.md`](central-admin-guide.md) | The central report bucket and `bedrock-admin fleet` |
| [`design.md`](design.md) | How and why it works: the model, each component, security, limits, testing, open items |
| [`AGENTS.md`](AGENTS.md) | Instructions for coding agents: checks, AWS safety, e2e pitfalls, commits |
| `cmd/bedrock-admin/` | Admin CLI |
| `cmd/bedrock/` | User CLI |
| `cmd/monthly-unpause/` | The monthly Lambda (`provided.al2023`), embedded in `bedrock-admin` by `make build` and releases (not `go install`) |
| `internal/` | Shared packages: config, plan and apply, account steps, users, pause, usage (Parquet reader), fleet (`users` and central reports), the user CLI |
| `tests/e2e/` | Robot Framework suites that run the real binaries against a real AWS account, by hand. See [tests/e2e/README.md](tests/e2e/README.md). |
| `tests/fixtures/` | Synthetic cost export Parquet files and a snapshot, used by the unit tests and the `usage` suite |
| `.github/workflows/` | `ci.yml` (vet, lint, unit tests), `release.yml` (GoReleaser on a `v*` tag) and `dependabot-merge.yml` (merges Dependabot patch updates after CI passes) |
| `.github/dependabot.yml` | Security updates only, for Go modules, Actions and the e2e image |

## Development

You need Go (the version in `go.mod`) and, for linting, `golangci-lint`.

| Command | Does |
| --- | --- |
| `make` | `generate`, `vet`, `lint`, `test` and `build` |
| `make generate` | `go generate ./...`: builds the monthly Lambda package that `bedrock-admin` embeds |
| `make build` | Builds `bin/bedrock-admin` and `bin/bedrock` |
| `make test` | `go test ./...` |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run ./...` |
| `make e2e SUITE=<suite>` | `tests/e2e/run.sh <suite>` |
| `make clean` | Removes `bin/`, `dist/` and the generated Lambda package |

Run `make generate` once before `go vet`, `go test` or `go build` in a fresh clone. Without it,
the build works, but `bedrock-admin` has no Lambda to deploy.

CI (`.github/workflows/ci.yml`) runs `go generate`, `go vet`, `golangci-lint` and
`go test -race ./...` on pull requests and on `main`. It does not run the end-to-end tests. Run
them by hand with `tests/e2e/run.sh`, as described in [tests/e2e/README.md](tests/e2e/README.md).
Every suite except `cli` uses a real AWS account.

Dependabot (`.github/dependabot.yml`) opens PRs only for security updates, not routine version bumps.
When CI passes on a Dependabot PR's latest commit and every update in it is a patch version,
`.github/workflows/dependabot-merge.yml` squash-merges it. Minor and major updates stay open for
review. Go module fixes use the `fix(deps)` prefix, so they appear under Bug fixes in the next
release's notes.

## Releasing

To release, push a `v*` tag. `.github/workflows/release.yml` runs GoReleaser
(`.goreleaser.yaml`), which builds both CLIs and attaches the archives and checksums to the GitHub
release.

> Account IDs, names, emails, bucket names and resource IDs in this repository are placeholders
> (for example account `111122223333` and `achen@example.com`). Replace them with your own values.
