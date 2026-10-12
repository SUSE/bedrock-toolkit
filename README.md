# Amazon Bedrock: per-person budgets and automatic pausing

Let people use Claude through Amazon Bedrock with their SSO login, with a monthly budget per
person. When someone reaches their limit, only that person is paused, and everyone is re-armed on
the 1st of the month.

Two command-line tools do the work:

- `bedrock-admin`, for the account admin: sets up the AWS account and manages users from one file,
  `bedrock.yaml`.
- `bedrock`, for users: sets up the AWS profile of their personal role and checks it.

## How it works

- Each user gets a personal IAM role that can only call Claude models (only the ones in `models:`, when
  listed), tagged with their SSO email.
- AWS bills each Bedrock call with that tag, and an AWS Budget per user tracks it.
- At the limit, the budget attaches a deny policy to that user's role. A monthly Lambda re-arms it.
- `bedrock-admin apply` creates all of this from `bedrock.yaml`.
- Optional: web search for Claude Code and the desktop app (their own search doesn't work on
  Bedrock), through an AgentCore gateway that personal roles may call and the pause also blocks.
  Search cost ($7 per 1,000) is not budgeted per user; the setup manages only Bedrock model spend.

```
 User (SSO login)
    │  aws sso login, then profile "bedrock" (written by bedrock setup)
    ▼
 Personal role  bedrock-user-<name>      Claude models only, tagged owner=<sso-email>
    │  bedrock:InvokeModel
    ▼
 Amazon Bedrock ──► billing records the caller and the role's tags
                        │
                        ▼
                  AWS Budget  bedrock-<name>  (filter: iamPrincipal/owner = <sso-email>)
                    alert_at_percent  → email
                    pause_at_percent  → attach bedrock-deny to that person's role (paused)
                        │
                        ▼
                  1st of the month, 06:00 UTC: Lambda bedrock-monthly-unpause
                  saves a snapshot, then re-arms every pause action
```

## Install

macOS and Linux, amd64 and arm64. The installer needs only `sh` and `curl`:

```bash
# Users
curl -fsSL https://raw.githubusercontent.com/SUSE/high-impact-ai-initiative/main/install.sh | sh -s -- bedrock
# Admins
curl -fsSL https://raw.githubusercontent.com/SUSE/high-impact-ai-initiative/main/install.sh | sh -s -- bedrock-admin
```

It installs the latest release to `/usr/local/bin`, or `~/.local/bin` when that isn't writable, after
checking the download against `checksums.txt`. Name both CLIs to install both. `VERSION` picks
another release and `INSTALL_DIR` another directory; set them on `sh`:

```bash
curl -fsSL https://raw.githubusercontent.com/SUSE/high-impact-ai-initiative/main/install.sh | VERSION=v1.2.0 INSTALL_DIR=~/bin sh -s -- bedrock bedrock-admin
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
go install github.com/SUSE/high-impact-ai-initiative/cmd/bedrock@latest
go install github.com/SUSE/high-impact-ai-initiative/cmd/bedrock-admin@latest
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
```

## Commands

`bedrock-admin`, for the account admin:

| Command | What it does |
| --- | --- |
| `configure` | Writes the settings part of `bedrock.yaml`, by asking or from flags |
| `doctor` | Checks the admin's credentials, permissions, account setup, cost export data and models |
| `models` | Lists the region's Claude inference profiles, the IDs `models:` takes |
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
| `doctor` | Checks your SSO sign-in, the `bedrock` profile, your personal role and a model call |
| `claude` | Tests Claude Code with the `bedrock` profile and prints the settings to use. Changes no Claude settings |

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
