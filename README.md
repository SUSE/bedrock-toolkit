# bedrock-toolkit

Tools to give a team model access on Amazon Bedrock: each person signs in with SSO to their own role,
has a monthly budget, and is paused alone when they reach it. Admins manage everyone from one
file; users set up their machine with one command.

## What's in the toolkit

| Program | For | What it does |
| --- | --- | --- |
| `bedrock-admin` | Account admins | Sets up the AWS account and manages users, budgets and pauses from `bedrock.yaml` |
| `bedrock` | Users | Writes the AWS profile of their personal role, checks it, lists the models they may use, tests Claude Code, shows their spend |
| `monthly-unpause` | The account | A Lambda that saves a snapshot and re-arms every pause on the 1st of the month. Built into `bedrock-admin`, deployed by `apply` |

## Features

- **Personal access**: each user gets an IAM role tagged with their SSO email, which can call any
  Claude model (except Fable), or only the ones listed in `models:` plus the built-in baseline
  models (Claude Opus, Sonnet and Haiku 5.5).
- **A budget per person**: AWS bills each Bedrock call with that tag, and a budget per user tracks it,
  with email alerts.
- **Pausing only that person**: at the limit, the budget attaches a deny policy to that user's role.
  A monthly renewal process re-arms it. Admins can also pause and unpause by hand.
- **One file, plan and apply**: `bedrock.yaml` lists the settings and the users. `plan` shows what
  differs from the account, `apply` makes them match, and `export` writes the file back from the account.
- **Usage reports**: spend per user, and spend that bypasses personal roles.
- **Several setups**: each `bedrock.yaml` has an `id` (default `bedrock`) that every AWS name carries,
  so several setups, each with its own users, budgets and models, can share one AWS account, and
  one admin can run setups in several accounts.
- **Web search** (optional): Claude Code's and the desktop app's own search doesn't work on Bedrock,
  so the toolkit adds a gateway that personal roles may call and the pause also blocks. Search cost
  ($7 per 1,000) is not budgeted per user; budgets cover only Bedrock model spend.

## How it works

```
 User (SSO login)
    │  aws sso login, then profile "bedrock" (written by bedrock setup)
    ▼
 Personal role  bedrock-user-<id>-<name> Claude/allowed models only, tagged owner=<sso-email>
    │  bedrock:InvokeModel
    ▼
 Amazon Bedrock ──► billing records the caller and the role's tags
                        │
                        ▼
                  AWS Budget  bedrock-<id>-<name>  (filter: iamPrincipal/owner = <sso-email>)
                    alert_at_percent  → email
                    pause_at_percent  → attach bedrock-deny-<id> to that person's role (paused)
                        │
                        ▼
                  1st of the month, 06:00 UTC: Lambda bedrock-monthly-unpause-<id>
                  saves a snapshot, then re-arms every pause action
```

## Install

macOS and Linux, amd64 and arm64. The installer needs only `sh` and `curl`:

```bash
# Users
curl -fsSL https://raw.githubusercontent.com/SUSE/bedrock-toolkit/main/install.sh | sh -s -- bedrock
# Admins
curl -fsSL https://raw.githubusercontent.com/SUSE/bedrock-toolkit/main/install.sh | sh -s -- bedrock-admin
```

It installs the latest release to `/usr/local/bin`, or `~/.local/bin` when that isn't writable, after
checking the download against `checksums.txt`. Name both CLIs to install both. `VERSION` picks
another release and `INSTALL_DIR` another directory; set them on `sh`:

```bash
curl -fsSL https://raw.githubusercontent.com/SUSE/bedrock-toolkit/main/install.sh | VERSION=v1.2.0 INSTALL_DIR=~/bin sh -s -- bedrock bedrock-admin
```

<details>
<summary>Release files and building from source</summary>

| File | Contents |
| --- | --- |
| `bedrock-admin_<version>_<os>_<arch>.tar.gz` | `bedrock-admin` |
| `bedrock_<version>_<os>_<arch>.tar.gz` | `bedrock` |
| `checksums.txt` | SHA-256 of every archive |

`<os>` is `darwin` (macOS) or `linux`, and `<arch>` is `amd64` or `arm64`. There is no Windows
build. To install by hand, download the archive from the GitHub release, unpack it and put the
binary on your `PATH`.

With Go installed, you can also build from source:

```bash
go install github.com/SUSE/bedrock-toolkit/cmd/bedrock@latest
go install github.com/SUSE/bedrock-toolkit/cmd/bedrock-admin@latest
```

A `bedrock-admin` built by `go install` has no monthly Lambda inside, because the Lambda package is
generated at build time and not committed. `plan` then reports Step 1.4 as a conflict ("this
bedrock-admin build has no Lambda code"). Use a release binary, or clone the repository and run
`make build`.

</details>

## Quick start

Admin, once per account ([admin guide](admin-guide.md)):

```bash
bedrock-admin doctor                 # check your credentials and permissions
bedrock-admin configure              # write bedrock.yaml
#   ...add users under users: in bedrock.yaml
bedrock-admin plan                   # see what will change
bedrock-admin apply                  # set up the account and the users
```

Each user, after the admin has onboarded them ([user guide](user-guide.md)):

```bash
aws sso login                        # or: aws login
bedrock setup                        # write the bedrock profile and check it
bedrock claude                       # test Claude Code, print its settings
bedrock usage                        # this month's spend against your budget
bedrock models                       # the models you may use
```

## Commands

`bedrock-admin`, for the account admin:

| Command | What it does |
| --- | --- |
| `configure` | Writes the settings part of `bedrock.yaml`, by asking or from flags |
| `doctor` | Checks the admin's credentials, permissions, account setup, cost export data and models |
| `models` | Lists the region's inference profiles, the IDs `models:` takes. `--filter` narrows the list; REGIONS shows where each allowed model sends requests |
| `plan` | Shows what `apply` would change. Read-only; exits 2 when there are changes |
| `apply` | Makes the account and the users match `bedrock.yaml`. Removals need `--yes` |
| `export` | Prints the current account, including its users, as a `bedrock.yaml` |
| `pause` | Pauses a user by hand, until `unpause`. The monthly Lambda leaves it in place |
| `unpause` | Restores a paused user's access |
| `usage` | Spend per user, and spend outside personal roles. Exits 2 when it finds untracked spend |
| `uninstall` | Removes the account setup. Refuses while users remain |

`bedrock`, for users:

| Command | What it does |
| --- | --- |
| `setup` | Writes the `bedrock` profile for your personal role in `~/.aws/config`, then runs `doctor` |
| `doctor` | Checks your SSO sign-in, the `bedrock` profile, your personal role and test calls to the built-in models |
| `claude` | Tests Claude Code with the `bedrock` profile and prints the settings to use. Changes no Claude settings |
| `usage` | Shows this month's spend against your budget, and whether you are paused |
| `models` | Lists the models you may use in your profile's region |

Both CLIs exit with 0 when everything is fine, 1 on an error, and 2 when they ran but found a
problem. Run `<command> --help` for the flags.

## Documents

| You are | Read |
| --- | --- |
| A user who wants to use the Bedrock inference provider in code agents | [User guide](user-guide.md) |
| The admin of an AWS account: you set up the account and manage users | [Admin guide](admin-guide.md) |
| Reviewing how and why this solution works | [Design](design.md) |
| Building, testing, or releasing the CLIs | [CONTRIBUTING.md](CONTRIBUTING.md) |
| Running the end-to-end tests | [tests/e2e/README.md](tests/e2e/README.md) |

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/bedrock-admin`, `cmd/bedrock` | The two CLIs |
| `cmd/monthly-unpause` | The monthly Lambda, embedded in `bedrock-admin` at build time |
| `internal/` | Shared packages: config, account setup, users, pausing, usage, web search |
| `tests/e2e` | Robot Framework end-to-end suites and their container runner |
| `models.toml` | The baseline models, built into both CLIs: `configure` puts them in a new `bedrock.yaml`, and a set `models:` always allows them too |
| `install.sh` | The release installer |

## License

[Apache License 2.0](LICENSE)
