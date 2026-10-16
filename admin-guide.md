# Amazon Bedrock: admin guide

For the admin of an AWS account where people use models through Bedrock. You set up the account
and manage users with `bedrock-admin`, from one file, `bedrock.yaml`.

## Contents

- [Quick start](#quick-start)
- [How it works, in short](#how-it-works-in-short)
- [Commands](#commands)
- [1. Install and check your environment](#1-install-and-check-your-environment)
- [2. Create `bedrock.yaml`](#2-create-bedrockyaml)
- [3. Set up the account: `plan`, then `apply`](#3-set-up-the-account-plan-then-apply)
- [4. Day-to-day changes are file edits](#4-day-to-day-changes-are-file-edits)
- [5. Someone got paused](#5-someone-got-paused)
- [6. Pause someone yourself](#6-pause-someone-yourself)
- [7. Read spend](#7-read-spend)
- [8. Check production](#8-check-production)
- [9. Block direct model calls (Step 1.7)](#9-block-direct-model-calls-step-17)
- [10. Turn on web search (optional)](#10-turn-on-web-search-optional)
- [11. Remove everything](#11-remove-everything)
- [The `id`: several setups in one account](#the-id-several-setups-in-one-account)
- [Several accounts](#several-accounts)
- [Emails](#emails)
- [Things to know](#things-to-know)
- [Run the tests](#run-the-tests)
- [Cheat sheet](#cheat-sheet)

Each step starts with the commands to run. Click the folded sections for sample output and details.

## Quick start

First-time setup of an account:

```bash
aws configure sso --profile bedrock-admin         # once: pick the account and your admin permission set
aws sso login --profile bedrock-admin
bedrock-admin configure --profile bedrock-admin   # write bedrock.yaml (step 2)
#   ...add your users under users: in bedrock.yaml
bedrock-admin doctor                              # check credentials and permissions (step 1)
bedrock-admin plan                                # see what apply will create (step 3)
bedrock-admin apply                               # create it; prints the command for each new user
```

Then send each new user the `bedrock setup` command that `apply` prints, and send your org admin the
`bedrock-block-policy.json` file that `apply` writes ([step 9](#9-block-direct-model-calls-step-17)).

After that:

```bash
# add, remove or change users: edit bedrock.yaml, then
bedrock-admin plan
bedrock-admin apply                               # --yes when it removes users
bedrock-admin usage                               # spend per user, and spend outside personal roles
```

## How it works, in short

- Each user gets a personal IAM role, `bedrock-user-<id>-<name>`, that can only call Claude models: the
  ones in `models:` plus the baseline models, or any Claude model except Fable when `models:` is empty.
  They reach it from their SSO login.
- The role is tagged with their SSO email. AWS bills each Bedrock call with that tag.
- Each user has a monthly budget on that tag. At `alert_at_percent` they get an email. At
  `pause_at_percent` AWS Budgets pauses only them, by attaching the `bedrock-deny-<id>` policy to
  their role.
- On the 1st of each month, a Lambda saves a snapshot of everyone's limit and pause state, then
  re-arms every pause.
- `bedrock.yaml` says who has access and how much they may spend. `bedrock-admin plan` shows what
  differs between the file and the account. `bedrock-admin apply` makes the account match.
- `pause` and `unpause` are commands, not file settings. Applying the file never undoes them.

## Commands

| Command | What it does | Section |
| --- | --- | --- |
| `bedrock-admin configure` | Writes the settings part of `bedrock.yaml`, by asking or from flags | [2](#2-create-bedrockyaml) |
| `bedrock-admin doctor` | Checks your credentials, permissions, account setup, cost export data and models | [1](#1-install-and-check-your-environment), [8](#8-check-production) |
| `bedrock-admin models` | Lists the region's inference profiles, the IDs `models:` takes. `--filter` narrows the list; REGIONS shows where each allowed model sends requests | [2](#choosing-models) |
| `bedrock-admin plan` | Shows what `apply` would change. Read-only; exits 2 when there are changes | [3](#3-set-up-the-account-plan-then-apply) |
| `bedrock-admin apply` | Makes the account and the users match the file. Removals need `--yes` | [3](#3-set-up-the-account-plan-then-apply), [4](#4-day-to-day-changes-are-file-edits) |
| `bedrock-admin export` | Prints the current account, including its users, as a `bedrock.yaml` | [2](#2-create-bedrockyaml) |
| `bedrock-admin pause` | Pauses a user by hand, until `unpause`. The monthly Lambda leaves it in place | [6](#6-pause-someone-yourself) |
| `bedrock-admin unpause` | Restores a paused user's access | [5](#5-someone-got-paused) |
| `bedrock-admin usage` | Spend per user, and spend outside personal roles. Exits 2 on untracked spend | [7](#7-read-spend) |
| `bedrock-admin uninstall` | Removes the account setup. Refuses while users remain | [11](#11-remove-everything) |

Common flags: `-f` (config file), `--profile`, `--json`, `--quiet`, `--dry-run` and `--yes`. Exit
codes: 0 ok, 1 error, 2 ran fine but found a problem. Run `bedrock-admin <command> --help` for
details.

## 1. Install and check your environment

### Install

```bash
curl -fsSL https://raw.githubusercontent.com/innobead/bedrock-toolkit/main/install.sh | sh -s -- bedrock-admin
bedrock-admin --version
```

Other versions, release files and building from source: [README](README.md#install). Use a
release binary: a `go install` build has no monthly Lambda inside.

### Check

Create an AWS CLI profile for your admin permission set once, then sign in and check:

```bash
aws configure sso --profile bedrock-admin   # once; skip if you already have a profile
aws sso login --profile bedrock-admin
bedrock-admin doctor --profile bedrock-admin
```

`aws configure sso` asks for your SSO start URL and region, opens the browser, then lets you pick the
account and your admin permission set. `bedrock-admin` is only a suggested name: any profile for the
admin permission set works, so use its name in place of `bedrock-admin` after `--profile`.

`doctor` prints `ok` or `FAIL` per check, with the fix under each failure, and exits 2 when one
failed. Before you have a `bedrock.yaml` it stops at the first check and tells you to run
`bedrock-admin configure` (step 2); run it again afterwards.

<details>
<summary>What doctor checks</summary>

| Check | Passes when |
| --- | --- |
| `config` | The file loads and is valid |
| `credentials` | The profile has valid credentials. Fix: `aws sso login --profile <profile>` |
| `permissions` | It could read every part of the account setup |
| `Step 1.1` to `Step 1.8` | Each account setup item is in place (one line per item, as in `plan`) |
| `cost export data` | The export has delivered files, and the newest is less than 48 hours old |
| `model <id>` | A test call to each model in `models:` and the baseline works (only the baseline when `models:` is empty) |

In an account that isn't the organization's management account, Steps 1.6 and 1.7 wait for your org admin.

</details>

<details>
<summary>Sample output</summary>

```text
$ bedrock-admin doctor
ok    config: bedrock.yaml (id acmeprod, 3 users)
ok    credentials: arn:aws:sts::111122223333:assumed-role/AWSReservedSSO_BedrockAdmin_0a1b2c/admin@example.com (account 111122223333)
ok    permissions: read every part of the account setup
ok    Step 1.1 cost export bucket bedrock-cur-111122223333-acmeprod: ok
ok    Step 1.1 cost export bedrock-cur-acmeprod: ok
ok    Step 1.2 pause policy bedrock-deny-acmeprod: ok
ok    Step 1.3 budget actions role bedrock-budget-actions-acmeprod: ok
ok    Step 1.4 Lambda role bedrock-monthly-unpause-acmeprod: ok
ok    Step 1.4 scheduler role bedrock-monthly-unpause-scheduler-acmeprod: ok
ok    Step 1.4 Lambda bedrock-monthly-unpause-acmeprod: ok
ok    Step 1.4 schedule bedrock-monthly-unpause-acmeprod: ok
ok    Step 1.5 model us.anthropic.claude-opus-5-5: ok
ok    Step 1.5 model us.anthropic.claude-sonnet-5-5: ok
ok    Step 1.5 model us.anthropic.claude-haiku-5-5: ok
FAIL  Step 1.6 cost allocation tag iamPrincipal/owner: needs org admin: activate it in the management account (Billing → Cost allocation tags)
      fix: ask your org admin to activate it in the management account (Billing → Cost allocation tags)
FAIL  Step 1.7 block direct model calls: needs org admin: deny missing on AWSAdministratorAccess, AWSPowerUserAccess
      fix: bedrock-admin apply writes bedrock-block-policy.json and the instructions for the org admin
ok    cost export data: 12 files, newest 2026-10-08 11:08 UTC (5h0m0s ago)
ok    model us.anthropic.claude-opus-5-5: test call ok
ok    model us.anthropic.claude-sonnet-5-5: test call ok
ok    model us.anthropic.claude-haiku-5-5: test call ok
```

</details>

### Admin permissions

A permission set with `AdministratorAccess` has everything you need. You don't need AWS
Organizations rights. Name the permission set `BedrockAdmin` (or whatever you set in
`block_direct_calls.admin_permission_set`).

<details>
<summary>Rights per service</summary>

The admin role needs rights in these services. A permission set with `AdministratorAccess` has
them all.

| Service | Used for |
| --- | --- |
| IAM | Roles and policies under the paths `/bedrock/` and `/bedrock-users/`, their tags and inline policies, `iam:PassRole` for the Lambda, scheduler and budget actions roles, and reading the inline policy of the `AWSReservedSSO_*` roles (Step 1.7) |
| AWS Budgets | Budgets, notifications and budget actions, including running and resetting actions |
| Data Exports (`bcm-data-exports`) | The CUR 2.0 export (Step 1.1) |
| S3 | Creating the cost export bucket, its policy and public access block; listing and reading the export and snapshot files. Include `s3:GetBucketLocation` on the bucket: `usage` and `doctor` read the bucket's region with it. |
| Lambda | The monthly Lambda (Step 1.4) |
| EventBridge Scheduler | The monthly schedule (Step 1.4) |
| Cost Explorer | `ce:ListCostAllocationTags` and `ce:UpdateCostAllocationTagsStatus` for the owner tag (Step 1.6). Spend is not read from Cost Explorer. |
| Bedrock | Model availability and the first and test calls to each model (Step 1.5, `doctor`) |
| Bedrock AgentCore | The web search gateway and its target (Step 1.8, only with `web_search.enabled`) |
| STS | `sts:GetCallerIdentity` |

You don't need AWS Organizations rights. The one step that does, Step 1.7, is handed to your org
admin (see [step 9](#9-block-direct-model-calls-step-17)).

Name your admin permission set to match `block_direct_calls.admin_permission_set` (default
`BedrockAdmin`). Step 1.7 blocks model calls from every other SSO role, and `apply` and `doctor`
make model calls.

</details>

## 2. Create `bedrock.yaml`

```bash
bedrock-admin configure --profile bedrock-admin
```

`configure` asks a few questions and writes everything except your users. Then add them:

```yaml
users:
  - email: achen@example.com
  - email: bkim@example.com
    limit_usd: 100              # overrides defaults.limit_usd
```

Use each user's SSO username, the email they sign in with, not an alias or another address. Their
personal role trusts only that exact name, so with any other address `bedrock setup` can't use it.
When you're not sure, ask the user to run this with their SSO profile (not the `bedrock` profile,
which works only once the email is right):

```bash
aws sts get-caller-identity --profile <their-sso-profile>
```

The address to use is the end of `Arn`, here `achen@example.com`:

```json
"Arn": "arn:aws:sts::111122223333:assumed-role/AWSReservedSSO_Dev_abc123/achen@example.com"
```

`bedrock doctor` shows the same address as `SSO sign-in: achen@example.com`. If you can see IAM
Identity Center, it is the user's **Username** there.

The file holds no secrets. Keep it in Git and review changes in pull requests.

<details>
<summary>configure without questions, and its flags</summary>

With `--yes`, or without a terminal, it asks nothing and takes the flags and defaults:

```bash
bedrock-admin configure --profile bedrock-admin --yes --id acmeprod --product genai-tools --limit 50
```

| Flag | Default |
| --- | --- |
| `--id` | `bedrock` |
| `--account` | The profile's account |
| `--region` | The profile's region |
| `--product` | `bedrock` |
| `--limit` | 100 |
| `--model` (repeatable) | Claude Opus, Sonnet and Haiku 5.5 through the region's `us.` or `eu.` inference profiles, or `global.` in other regions |
| `--bucket` | `bedrock-cur-<account>-<id>` |
| `--prefix` | `cur` |
| `--block-direct-calls` | `true` |
| `--method` | `permission-set` |
| `--admin-permission-set` | `BedrockAdmin` |

`configure` writes `-f`, or `./bedrock.yaml`. On an existing file it keeps `users:`. `--dry-run`
prints the file instead of writing it. It ends with "Next: add users: entries, then run
bedrock-admin plan and bedrock-admin apply."

</details>

<details>
<summary>A full bedrock.yaml</summary>

```yaml
id: acmeprod                    # names and tags what apply creates; default bedrock
account: "111122223333"         # in quotes
region: us-west-2
profile: bedrock-admin          # AWS profile; --profile and $AWS_PROFILE win over it
product: genai-tools            # product tag on the personal roles; a user's product: overrides it

models:                         # optional: users may call only these and the baseline models; apply enables each, doctor tests each
  - us.anthropic.claude-opus-5-5
  - us.anthropic.claude-sonnet-5-5
  - us.anthropic.claude-haiku-5-5

defaults:
  limit_usd: 50                 # monthly limit per user
  pause_at_percent: 100         # pause when spend reaches 100% of the limit
  alert_at_percent: [80]        # email at 80%; [] for none
  notify: owner                 # the user's SSO email, or a fixed address

cost_export:                    # optional; these are the defaults
  bucket: bedrock-cur-111122223333-acmeprod
  prefix: cur

block_direct_calls:
  enabled: true                 # false skips Step 1.7
  method: permission-set        # how your org admin applied it: permission-set or scp
  admin_permission_set: BedrockAdmin

web_search:                     # optional, see step 10
  enabled: false
  exclude_domains: []           # domains never returned, e.g. [example.com]

users:                          # or: users: !include users.yaml
  - email: achen@example.com
  - email: bkim@example.com
    limit_usd: 100
    pause_at_percent: 110       # 10% grace before the pause
  - email: cdiaz@example.com
    limit_usd: 30
    notify: cdiaz-lead@example.com
    product: data-platform      # her role is tagged with this product instead
```

</details>

<details>
<summary>All settings</summary>

| Key | Notes |
| --- | --- |
| `id` | 1–20 lowercase letters and digits. Default `bedrock`. Every name `apply` creates ends in it, and it tags everything. See [the id](#the-id-several-setups-in-one-account). |
| `account` | 12 digits, in quotes |
| `product` | Required. The `product` tag on every personal role, to charge spend to a team or product, like `owner` charges it to a person. Billing only shows it once your org admin activates it as a cost allocation tag, as in Step 1.6. Letters, digits, spaces and `_.:/=+@-`, up to 128. A user's own `product:` overrides it for that user. |
| `models` | Optional. Inference profile IDs (or foundation model IDs) personal roles may call, and only those plus the baseline models (see [Choosing models](#choosing-models)); `apply` enables each and `doctor` tests each. Empty: personal roles may call any Claude model except the Claude Fable family, and `apply` still enables every baseline model and `doctor` tests the region's. Only Claude models other than Fable may be listed. |
| `limit_usd` | More than 0, at most 1,000,000, at most 2 decimals. Each user needs one, or `defaults.limit_usd`. |
| `pause_at_percent` | 1–200. The pause fires when spend reaches `limit_usd × pause_at_percent / 100` (the trigger). Default 100. |
| `alert_at_percent` | Up to 4 values, each 1–200. Default `[80]`. |
| `notify` | `owner` (the user's SSO email) or an email address. Use an address when the SSO email gets no mail. |
| `name` (per user) | Overrides the name derived from the email (`achen@example.com` → `achen`). At most 30 characters, so the role name fits IAM's 64. The role is `bedrock-user-<id>-<name>`, the budget `bedrock-<id>-<name>`. |
| `cost_export.bucket` | Default `bedrock-cur-<account>-<id>`. |
| `cost_export.name` | Export name. Default `bedrock-cur-<id>`. |
| `cost_export.snapshot_prefix` | Where the Lambda writes snapshots in the bucket. Default `bedrock-admin/snapshots`. |

`!include` works anywhere in the file. Duplicate emails or names are rejected. Every problem in the
file is reported at once.

Which file is used: `-f`, then `$BEDROCK_ADMIN_CONFIG`, then `./bedrock.yaml`, then
`~/.config/bedrock-admin/bedrock.yaml`.

</details>

### Choosing models

Leave `models:` empty to let users call any Claude model except Fable, or list the ones they may
call. When you list any, users may always call the **baseline models** too: the models in
[`models.toml`](models.toml) (now Claude Opus, Sonnet and Haiku 5.5), built into both CLIs, so
the Claude models the `bedrock` user CLI and Claude Code use always work. A new file from
`configure` starts with them. To see which models your region offers, and which your file allows:

```bash
bedrock-admin models                    # every provider's inference profiles
bedrock-admin models --filter claude    # only those whose ID or name contains "claude" (any case)
```

Personal roles may call only Claude models other than Fable, so every other model shows `never`
under USERS.

The REGIONS column shows the regions each allowed model sends requests to, which is what
matters for data residency. A `global.` profile shows `any region (global)`. The column shows
where AWS processes users' calls.

`models` lists only the region's profiles: `eu.` profiles exist only in EU regions, for example.
Users can pick another region in `bedrock setup`, so use `--region` to see what they get there.
`bedrock claude` gives users Claude Opus, Sonnet and Haiku 5.5 through their region's profiles,
which the baseline always allows.

<details>
<summary>Sample output and notes</summary>

```text
MODEL ID                                 NAME                                STATUS  USERS       REGIONS
global.anthropic.claude-fable-5          Global Anthropic Claude Fable 5     ACTIVE  never       -
...
us.anthropic.claude-haiku-5-5            US Anthropic Claude Haiku 5.5       ACTIVE  allowed     us-east-1 us-east-2 us-west-2
us.anthropic.claude-opus-4-8             US Anthropic Claude Opus 4.8        ACTIVE  not listed  -
us.anthropic.claude-opus-5-5             US Anthropic Claude Opus 5.5        ACTIVE  allowed     us-east-1 us-east-2 us-west-2
us.anthropic.claude-sonnet-5-5           US Anthropic Claude Sonnet 5.5      ACTIVE  allowed     us-east-1 us-east-2 us-west-2
```

- `USERS` is `allowed` when `models:` is empty or it or the baseline lists the model, `not listed`
  when they list others, and `never` for the Claude Fable family. `--region` picks another region; `--json`
  prints JSON (allowed models have a `regions` list; `["any"]` for a `global.` profile).
- REGIONS is what AWS reports today: one read-only `GetInferenceProfile` call per allowed model. AWS
  can add regions to a profile later.
- A listed inference profile allows the profile and its foundation model in every region, since
  cross-region profiles route requests to several regions. Changing `models:` updates every personal
  role's policy on the next `apply`.

- The prefix picks where requests run. `us.`/`eu.`/`apac.` keep them in that geography. `global.`
  may use any commercial region, for better availability and a slightly lower price. The `bedrock`
  user CLI picks the prefix from each user's own region (`us.`/`eu.`, `global.` elsewhere), so list
  both a `us.` or `eu.` and a `global.` ID when users work outside your geography. Leave out
  `global.` if requests must stay in your geography.
- Then run `bedrock-admin plan`: Step 1.5 shows `unknown model in <region>` for a wrong ID, and
  `first call` for a model the account hasn't enabled yet (`apply` enables it).
- Users can't enable a model themselves: their first call to a model the account hasn't enabled
  fails with AccessDenied (personal roles have no AWS Marketplace permissions). `apply` enables the
  models in `models:` and every baseline model, whatever your region (`us.` profiles from us-east-1 and `eu.` ones from eu-central-1 when they aren't your region's). With an empty `models:`, users may call any
  other Claude model except Fable only once the account has enabled it (for example, an admin's
  first call); list it in `models:` to have `apply` enable it.
- To see one model's state: `aws bedrock get-foundation-model-availability --model-id
  anthropic.claude-sonnet-5-5` (the ID without the prefix). Ready means `AUTHORIZED`, and `AVAILABLE`
  for region, agreement and entitlement.
- `models.toml` lists each baseline model with every prefix (`us.`, `eu.` and `global.`), and the
  personal role policy allows all of them, since each user's `bedrock` CLI picks the prefix from the
  user's own region, which can differ from `region:`. Step 1.5 enables them all, each from a region of
  its geography. `doctor` and `configure` use only your region's: its prefix (`us.` or `eu.`), or
  `global.` in other regions (a US or EU region can call the `global.` ones too, but they're the
  same models). Each `bedrock-admin` release carries its own copy of [`models.toml`](models.toml), so a
  new baseline arrives with a new release (`plan` then shows the personal role policies changing).
  You can't remove a baseline model; leave `models:` empty instead to allow any Claude model.
- For a new file, `bedrock-admin configure` writes the region's baseline models into `models:`. `--model` or
  your answer wins (the baseline is still allowed); an existing file keeps its `models:`.
  The `bedrock` user CLI has its own copy of `models.toml`: `bedrock doctor` tests the baseline
  models with the user's region's prefix (`us.`/`eu.` by region, `global.` elsewhere) and `bedrock claude` prints its Opus, Sonnet and Haiku. The user CLI doesn't read
  `bedrock.yaml`, but `bedrock models` reads the user's own role policy and lists the models
  allowed there. If you list other models, announce them: users then update the model IDs in
  their own Claude Code settings and Claude desktop app model list, and can test one with
  `bedrock doctor --model <id>`.

</details>

### Capture an existing account

```bash
bedrock-admin export > bedrock.yaml
```

<details>
<summary>Details</summary>

The file starts with `# Exported from account 111122223333 by bedrock-admin export on <date>.` It
exports only users with this config's `id`. Without a file or `--id` it reads the id from the
`bedrock-deny-<id>` policy; with several setups in the account it asks for `--id`. It reads `models:` from a personal role's policy, baseline models included (empty when it allows
any Claude model); `--model` overrides it. `product:` is the product tag most roles have (`--product`
overrides it); a user whose role has another one gets their own `product:`.

</details>

## 3. Set up the account: `plan`, then `apply`

```bash
bedrock-admin plan     # read-only; exits 2 when there are changes
bedrock-admin apply    # creates what is missing; safe to re-run
```

`apply` prints each change and, for each new user, the command to send them:

```
Send each new user their setup command (after aws sso login):
  achen@example.com: bedrock setup
  bkim@example.com: bedrock setup
  cdiaz@example.com: bedrock setup
```

Send it, and point them to the [user guide](user-guide.md). Step 1.6 clears by itself within 24
hours of the first user call. Step 1.7 needs your org admin ([step 9](#9-block-direct-model-calls-step-17)).

<details>
<summary>Sample plan output</summary>

```
bedrock.yaml: id acmeprod, account 111122223333, us-west-2, 3 users

Account setup
+ 1.1 cost export bucket bedrock-cur-111122223333-acmeprod  create (private, writable only by Data Exports)
+ 1.1 cost export bedrock-cur-acmeprod                     create (CUR 2.0, Parquet, to s3://bedrock-cur-111122223333-acmeprod/cur/bedrock-cur-acmeprod)
+ 1.2 pause policy bedrock-deny-acmeprod                   create
+ 1.3 budget actions role bedrock-budget-actions-acmeprod  create
+ 1.4 Lambda role bedrock-monthly-unpause-acmeprod         create
+ 1.4 scheduler role bedrock-monthly-unpause-scheduler-acmeprod  create
+ 1.4 Lambda bedrock-monthly-unpause-acmeprod              create (runs 06:00 UTC on the 1st)
+ 1.4 schedule bedrock-monthly-unpause-acmeprod            create (cron(0 6 1 * ? *) UTC)
  1.5 model us.anthropic.claude-opus-5-5                   ok
  1.5 model us.anthropic.claude-sonnet-5-5                 ok
  1.5 model us.anthropic.claude-haiku-5-5                  ok
  1.6 cost allocation tag iamPrincipal/owner               waiting for first use (appears up to 24 h after the first personal-role call)
? 1.7 block direct model calls                             needs org admin: deny missing on AWSAdministratorAccess, AWSPowerUserAccess

Users
+ achen@example.com  onboard, limit 50
+ bkim@example.com   onboard, limit 100
+ cdiaz@example.com  onboard, limit 30

Plan: 11 to create, 1 needs org admin.
```

</details>

<details>
<summary>Plan marks and the account setup steps</summary>

| Mark | Meaning |
| --- | --- |
| `+` | Create |
| `~` | Change |
| `-` | Remove |
| `?` | Needs your org admin (Step 1.7, or Step 1.6 outside the management account) |
| `!` | Conflict: something exists that `bedrock-admin` didn't create. It is left alone. |
| (blank) | Nothing to do |

| Step | Creates or checks |
| --- | --- |
| 1.1 | A private S3 bucket that only AWS Data Exports may write to, and a CUR 2.0 export (Parquet, hourly, with caller identity) into it |
| 1.2 | `bedrock-deny-<id>`, the policy that pauses someone when attached to their role |
| 1.3 | `bedrock-budget-actions-<id>`, the role AWS Budgets uses. It may only attach and detach `bedrock-deny-<id>` on the id's `/bedrock-users/<id>/` roles. |
| 1.4 | The monthly Lambda `bedrock-monthly-unpause-<id>`, its role, and an EventBridge Scheduler schedule at 06:00 UTC on the 1st. The Lambda code is built into `bedrock-admin`; after an upgrade, `plan` shows an update and `apply` deploys the new code. |
| 1.5 | Each model in `models:` and every baseline model (only those when `models:` is empty) is available, checked (`us.` profiles from us-east-1 and `eu.` ones from eu-central-1 when they aren't your region's). If not, `apply` makes the first call, which enables it and accepts its agreement. AWS updates the status a few minutes later, so a `plan` right after `apply` can still show `first call`; it clears by itself. |
| 1.6 | The cost allocation tag `iamPrincipal/owner` is active. It appears only after the first call through a personal role, up to 24 hours later. Only the management account can activate it; elsewhere `plan` shows "needs org admin". |
| 1.7 | Model calls are blocked outside personal roles. Your org admin applies it; see [step 9](#9-block-direct-model-calls-step-17). |
| 1.8 | Only with `web_search.enabled`: the role `bedrock-web-search-gateway-<id>`, the AgentCore gateway `bedrock-web-search-<id>` and its Web Search target; see [step 10](#10-turn-on-web-search-optional). |

</details>

<details>
<summary>More about apply</summary>

`apply` creates what is missing and fixes what differs, so running it again is safe. It ends with a
summary such as `Done: 11 created. 1 still pending.`, then a `Failed:` and a `Pending:` list with
the reason under each item. If a step fails, fix the cause and run `apply` again; users wait until
the account setup succeeds.

The `bedrock setup` command it prints has `--region` when your profile's region differs from the file's region, and
`--id <id>` when the id isn't `bedrock`, or `--role-arn` when the user has a custom `name:`. Send it
as printed: users can't look up their role, so they need these flags from you. A message to a new
user:

1. Install the `bedrock` CLI and set up AWS SSO, as in the [user guide](user-guide.md).
2. Run `aws sso login`, then the `bedrock setup ...` line `apply` printed for them.
3. Run `bedrock claude` for the Claude Code and Claude desktop app settings.

`apply` exits 2 when something is still pending, such as Step 1.7. `apply --dry-run` shows the plan
and changes nothing. Once Step 1.7 is done, `plan` prints `No changes.` and exits 0.

</details>

## 4. Day-to-day changes are file edits

Edit `bedrock.yaml` (ideally in a pull request, with the `plan` output for review), then:

```bash
bedrock-admin plan
bedrock-admin apply          # add --yes when it removes users
```

| Task | Edit |
| --- | --- |
| Give dlee access | Add `- email: dlee@example.com` |
| Raise bkim's limit | `limit_usd: 100` → `150` |
| Let bkim go 10% over before the pause | `pause_at_percent: 110` on bkim |
| Send cdiaz's alerts to her lead | `notify: cdiaz-lead@example.com` on cdiaz |
| Charge cdiaz's spend to another product | `product: data-platform` on cdiaz |
| No alerts for anyone | `defaults.alert_at_percent: []` |
| Remove achen | Delete the entry, then `apply --yes` |
| Fix a wrong email (not the SSO username) | Change `email` to the SSO username ([how to find it](#2-create-bedrockyaml)), `apply --yes`, then the user runs `bedrock setup --yes` |

<details>
<summary>Sample plan output, removals and drift</summary>

`plan` shows what a change does to someone's pause state before you apply it:

```
Users
  achen@example.com  no changes                                   armed
~ bkim@example.com   limit 100 → 150  (paused → unpaused, spent $104.20)
~ cdiaz@example.com  limit 30 → 25  (warning: spent $27.00, will be paused)
- dlee@example.com   offboard  (needs --yes)
```

- Removing a user deletes their pause action, budget and role. It needs `--yes`. Without it,
  `apply` prints the plan and stops:

  ```text
  $ bedrock-admin apply
  ...
  Users
  - dlee@example.com  offboard  (needs --yes)

  Plan: 1 to remove.
  error: dlee@example.com need --yes (removals can be skipped with --no-delete); nothing was changed
  ```

  `apply --yes` then prints `- dlee@example.com: offboard` and `Done: 1 removed.` `apply
  --no-delete` applies everything else and skips removals.
- A pause action changed outside the file (for example in the console) is recreated, which re-arms
  it. That also needs `--yes`.
- Removing a user and adding them back gives them a new, armed budget action.
- Other drift, such as a budget limit edited in the console, shows as `~` in `plan`. `apply` puts
  back the file's version.

</details>

<details>
<summary>How a limit change affects the pause</summary>

| State before | Change | `apply` does |
| --- | --- | --- |
| Armed | Any | Updates the limit; the pause fires at the new trigger |
| Armed | New trigger at or below spend | Same; AWS Budgets pauses them within hours (`plan` warns) |
| Paused (over the limit) | New trigger above spend | Updates the limit, unpauses and re-arms at the new trigger |
| Paused (over the limit) | New trigger at or below spend | Updates the limit; they stay paused |
| Paused by hand | Any | Updates the limit; they stay paused |
| Off for the month | Any | Updates the limit; the pause stays off until the 1st |

"Spend" is the actual spend AWS Budgets reports, the figure that triggers the pause.

</details>

## 5. Someone got paused

```bash
bedrock-admin usage bkim@example.com                 # see their spend

# keep a cap: raise limit_usd or pause_at_percent in bedrock.yaml, then
bedrock-admin apply

# or no cap until the 1st of next month:
bedrock-admin unpause bkim@example.com --reason "release deadline, approved by Kim"
```

Access returns within about a minute.

<details>
<summary>Details</summary>

When a user's spend reaches their trigger, AWS Budgets attaches `bedrock-deny-<id>` to their role. Their
model calls fail with an explicit deny, and the address in `notify` gets an email from AWS Budgets.

**Give them more budget and keep a cap.** Raise `limit_usd` or `pause_at_percent` in the file and
`apply`. If their spend is below the new trigger, `apply` unpauses them and re-arms the pause at the
new trigger. The reason is saved on the role as "limit raised to $150 in bedrock.yaml".

**Let them continue this month without a cap.** `unpause` prints:

```
Unpaused bkim@example.com. Their pause is off until Nov 1, so spending is not capped this month.
To keep a cap instead, raise limit_usd in bedrock.yaml and run bedrock-admin apply.
```

The pause is off until the 1st, when the monthly Lambda re-arms it. Re-arming it now would pause
them again within hours. The reason is saved as a tag on their role, and the call is in CloudTrail.

</details>

## 6. Pause someone yourself

For a leaked key, a runaway script or anything urgent:

```bash
bedrock-admin pause cdiaz@example.com --reason "runaway agent loop, INC-4521"
bedrock-admin unpause cdiaz@example.com --reason "fixed"
```

Model calls fail within seconds. The pause stays until you `unpause`.

<details>
<summary>Sample output and details</summary>

```text
Paused cdiaz@example.com by hand. Model calls now fail with an explicit deny.
Undo: bedrock-admin unpause cdiaz@example.com
```

`pause` attaches `bedrock-deny-<id>` to the role and tags it with who, when and why. A pause by hand
stays until you `unpause`. `apply`, limit changes and the monthly Lambda leave it alone.
`--reason` is required for `pause`.

`unpause` on someone who isn't paused prints "... is not paused (...). Nothing changed." and exits
0. If someone is paused by hand and also over their limit, the first `unpause` removes the hand
pause and they stay paused by the budget; run `unpause` again to turn that off for the month.

`pause` and `unpause` work on users in the file. You can give a user's name instead of their email.

</details>

## 7. Read spend

```bash
bedrock-admin usage                       # this month: users, then untracked callers
bedrock-admin usage --month 2026-09       # a past month, with limits and pause state from its snapshot
bedrock-admin usage --months 6            # the last 6 months, one column per month
bedrock-admin usage achen@example.com     # one person: per day, per model, per way of calling
bedrock-admin usage --untracked           # only spend outside personal roles
bedrock-admin usage --tracked             # only spend in personal roles
bedrock-admin usage --json                # for scripts
```

`usage` exits 2 when it finds spend outside personal roles, so a scheduled job can alert on it.

Users can check their own spend and pause state with `bedrock usage`, and their allowed models with
`bedrock models`: their personal role may read only its own budget, its own tags and its own
`bedrock-invoke` policy. Roles created before this show a `~` on `bedrock-invoke` in `plan`;
`apply` updates them.

<details>
<summary>Sample output</summary>

```
2026-10 (to Oct 8, billing lags up to ~16 h)

USERS              LIMIT  SPENT    USED  FORECAST  PAUSE
achen@example.com  $50    $41.20   82%   $160      armed
bkim@example.com   $100   $104.20  104%  -         PAUSED (Oct 7)
cdiaz@example.com  $30    $3.10    10%   $12       PAUSED by hand (Oct 8: runaway agent loop, INC-4521)
Tracked total             $148.50

UNTRACKED (outside personal roles)
WHO               USED VIA                       USD   LAST USED
dlee@example.com  SSO role AWSPowerUserAccess    6.40  2026-10-07
ci-bot            IAM user                       3.20  2026-10-06
Untracked total                                  $9.60
```

One person:

```text
$ bedrock-admin usage achen@example.com
achen@example.com  2026-10
Limit $50, spent $41.20 (82%), armed

DAY         USD
2026-10-01  4.10
...
2026-10-08  6.30

MODEL                                       USD
Claude Sonnet 5.5 (Amazon Bedrock Edition)  35.80
Claude Haiku 5.5 (Amazon Bedrock Edition)    5.40

USED VIA                         USD
personal role                    41.20

Total  $41.20
```

Several months, one column each. `-` means no data for that user in that month:

```text
$ bedrock-admin usage --months 3
USERS              2026-08  2026-09  2026-10   TOTAL
achen@example.com    38.10    47.90    41.20  127.20
bkim@example.com         -    96.40   104.20  200.60
cdiaz@example.com    12.00    20.50     3.10   35.60
Tracked total        50.10   164.80   148.50  363.40

Everyone used their personal role. Nothing to follow up.
```

</details>

<details>
<summary>Columns and options</summary>

A past month with no export files prints a note on top, for example when the export didn't exist
yet.

- Pause states: `armed`, `PAUSED (date)` (over the limit), `PAUSED by hand (date: reason)`,
  `off until <date>` (unpaused for the month), and `no pause action`.
- Forecast is spend so far scaled to the whole month. It is left out when the user is paused or off.
- Spend counts as tracked only when its owner tag is a user in the file with a budget. Everything
  else is untracked.
- Callers under $0.01 are left out.
- With no untracked spend, the section is one line: "Everyone used their personal role. Nothing to
  follow up."
- `usage` exits 2 when there is untracked spend, so a scheduled job can alert on it.
- `--month` and `--months` can't be combined. `--months` takes 1 to 24 and no email.

</details>

<details>
<summary>What to do about untracked spend</summary>

| Used via | Meaning | Action |
| --- | --- | --- |
| `SSO role <permission set>` | Called Bedrock straight from their SSO login, not the `bedrock` profile | Ask them to follow the [user guide](user-guide.md). Onboard them if they aren't yet. Step 1.7 stops this. |
| `personal role without owner tag: <role>` | A personal role is missing its `owner` tag | `apply` |
| `personal role without budget: <role>` | A personal role with an owner who has no budget in this file | Add the user to the file, or remove the role |
| `role <role>` | Another IAM role, such as an application role | Find the owner of the role |
| `IAM user` | An IAM user or a long-term Bedrock API key | Retire the user or key |

</details>

<details>
<summary>Where the numbers come from</summary>

| Data | Source |
| --- | --- |
| This month, limit and pause state | AWS Budgets |
| Spend, this month and past months | The cost export Parquet files in the bucket |
| Past months, limit and pause state | The snapshot `s3://<bucket>/<snapshot_prefix>/YYYY-MM.json` (default prefix `bedrock-admin/snapshots`), written by the monthly Lambda before it re-arms. Without a snapshot, limits come from the file and pause state is unknown. |

The export keeps settling for a few days after the month ends, so recent past months can still
change slightly. Snapshots are not deleted by `uninstall` unless you pass `--delete-data`.

</details>

## 8. Check production

```bash
bedrock-admin plan -f prod/bedrock.yaml
```

Exit 0 means the account matches the file. Exit 2 means something differs, for example a budget
edited in the console. `apply` puts back the file's version. `--quiet` prints only the changes.

## 9. Block direct model calls (Step 1.7)

Users can call Bedrock straight from their SSO login and bypass their budget. Step 1.7 blocks that.
It needs AWS Organizations rights, so your org admin applies it:

1. Keep `block_direct_calls.enabled: true` in `bedrock.yaml` (the `configure` default).
2. `bedrock-admin apply` writes `bedrock-block-policy.json` in the current directory and prints
   instructions; the rest of `apply` continues. Send both to your org admin.
3. When they are done, set `block_direct_calls.method` to what they used (`permission-set` or
   `scp`), and run `bedrock-admin plan`.

<details>
<summary>The setting and the instructions apply prints</summary>

```yaml
block_direct_calls:
  enabled: true
  method: permission-set
  admin_permission_set: BedrockAdmin
```

```text
Step 1.7 needs your AWS Organizations admin. Wrote bedrock-block-policy.json. Send it with these instructions:

  Block Bedrock model calls outside personal roles in account 111122223333, either way:
  - SCP (preferred; covers every permission set, including future ones; has no effect if 111122223333
    is the management account): create an SCP from the file and attach it to the account.
  - Permission sets: add the statement in the file to the inline policy of every permission set
    assigned to the account except BedrockAdmin, then re-provision them.

Then set block_direct_calls.method in bedrock.yaml to match (permission-set or scp) and run plan.
```

</details>

<details>
<summary>How plan checks it, and what it doesn't cover</summary>

| `method` | What `plan` shows |
| --- | --- |
| `permission-set` | Reads the inline policy of each `AWSReservedSSO_*` role in the account. `needs org admin: deny missing on X, Y` until each permission set carries the deny, then `ok (N permission sets carry the deny)`. |
| `scp` | `applied by SCP (not checkable here)`. SCPs can't be read from a member account. It doesn't count as a change. |

With `enabled: false`, `plan` shows `skipped (enabled: false)`. That doesn't remove a deny that is
already in place; that is also your org admin's job.

Users' permission sets must also allow `sts:AssumeRole` on `role/bedrock-users/*`, or users can't
use their personal role. `plan` warns about permission sets that lack it (sets with
`AdministratorAccess` or `PowerUserAccess` already have it), and `apply` adds the statement to the
org admin instructions.

From then on, only personal roles (tracked and capped) and the admin permission set can call
models. Check with `bedrock-admin usage --untracked`: no SSO roles should appear. If one does, the
block isn't covering it.

The deny doesn't cover IAM users, application roles or long-term Bedrock API keys. Keep the rights
to create those out of users' permission sets. The untracked section of `usage` is the safety net.

</details>

## 10. Turn on web search (optional)

```yaml
# bedrock.yaml
web_search:
  enabled: true
  exclude_domains: [example.com]   # optional, up to 100
```

```bash
bedrock-admin apply
```

Claude Code's built-in web search doesn't work on Bedrock. With `web_search.enabled`, Step 1.8
creates an AgentCore gateway with the AWS Web Search tool, and each personal role may call it.
Users then run `bedrock claude`, which tests one search and prints the MCP server to add to Claude
Code and the desktop app ([user guide](user-guide.md#web-search)).

<details>
<summary>Cost, regions and what apply changes</summary>

- Web Search costs $7 per 1,000 searches; the gateway itself is free. This cost is not managed
  here: the setup manages only Bedrock model spend. Billing records no caller on search rows, so
  searches don't count against personal budgets and nothing limits them.
- The gateway is in the file's `region`, which must be `us-east-1`, `eu-west-1` or `ap-northeast-1`.
  Users whose profile is in another region still use it: `bedrock claude` finds the gateway and
  prints its region.
- `plan` shows Step 1.8 (role, gateway, target) and a `~` on each personal role: `bedrock-invoke`
  gains the right to call the gateway. Paused users can't search: `bedrock-deny-<id>` denies it too.
- Searches show in `usage` under untracked spend as `web search (AgentCore)`, one line for the
  whole account.
- Setting `enabled: false` and running `apply` removes the gateway, its target and the role, and
  takes the right back from the personal roles. `uninstall` removes them too.
- `exclude_domains` are lowercase domain names; results from them are never returned. Changing the
  list updates the target in place.
- `doctor` shows the gateway and target status and the connector version.
- Users' searches go to AWS only. AWS's Web Search terms ask that results shown to people keep their
  source links; Claude Code shows them by default.

</details>

## 11. Remove everything

```bash
# set users: [] in bedrock.yaml, then
bedrock-admin apply --yes
bedrock-admin uninstall          # add --delete-data to also delete the cost export bucket
```

<details>
<summary>Details</summary>

`uninstall` removes this id's schedule, Lambda, roles, `bedrock-deny-<id>`, web search gateway and
cost export. Other setups in the account are left alone. It asks first; without a terminal it needs `--yes`.

It refuses while personal roles remain:

```text
$ bedrock-admin uninstall
error: 3 personal roles remain (bedrock-user-acmeprod-achen, bedrock-user-acmeprod-bkim, bedrock-user-acmeprod-cdiaz); offboard them first (users: [] then bedrock-admin apply --yes)
```

Offboard everyone first: set `users: []`, then `bedrock-admin apply --yes`. `uninstall --dry-run`
prints what it would remove and changes nothing.

It keeps the cost export bucket, with the billing history and the snapshots. Add `--delete-data`
to delete it too. Do that only if you won't set up again soon: S3 bucket names are global, so after
deleting, S3 may hold the name for a while (`apply` then fails with `OperationAborted`), and another
AWS account could claim it. In a terminal, `uninstall` asks again before deleting the bucket;
`--yes --delete-data` deletes it without asking.

</details>

## The `id`: several setups in one account

Each `bedrock.yaml` is one setup, named by its `id` (default `bedrock`). Every name `apply` creates
ends in the id, and everything is tagged `bedrock-admin:id` with it (schedules can't be tagged, so
the id is in the schedule's description). So several setups can share one AWS account, each with its
own users, budgets, `models:`, pause policy, monthly Lambda, cost export and bucket:

| Resource | Name |
| --- | --- |
| Personal role | `bedrock-user-<id>-<name>`, path `/bedrock-users/<id>/` |
| Budget | `bedrock-<id>-<name>` |
| Pause policy | `bedrock-deny-<id>` |
| Budget actions role | `bedrock-budget-actions-<id>` |
| Monthly Lambda, its role and schedule | `bedrock-monthly-unpause-<id>` |
| Scheduler role | `bedrock-monthly-unpause-scheduler-<id>` |
| Web search gateway and its role | `bedrock-web-search-<id>`, `bedrock-web-search-gateway-<id>` |
| Cost export and bucket | `bedrock-cur-<id>`, `bedrock-cur-<account>-<id>` |

One person can be a user of several setups; they get a role in each. `apply`, offboarding,
`uninstall` and the monthly Lambda only touch their own id, so you can't offboard someone by accident
from another setup's file.

- A resource with the setup's name but no id tag, or another id's tag, is a conflict (`!`), for
  example "exists without a bedrock-admin:id tag (made outside bedrock-admin); remove it, then
  apply". It is left alone.
- A personal role of the id without the tag is listed under Users as `not managed (...)` and left
  alone.
- `usage` counts spend through other setups' personal roles neither as tracked nor untracked; it
  prints it as one line, "Other setups in this account".
- Shared by every setup in the account: the cost allocation tag (Step 1.6) and the Step 1.7 deny
  your org admin adds to the permission sets.
- Users of a setup whose id isn't `bedrock` run `bedrock setup --id <id>`; `apply` prints that
  command. Someone in two setups runs `bedrock setup` once per setup, and adds
  `--profile-name <name>` to the second so it doesn't replace the first `bedrock` profile, for
  example `bedrock setup --id longhorn --profile-name bedrock-longhorn`. `bedrock doctor`, `claude`
  and `usage` take the same `--profile-name`.

## Several accounts

Each AWS account has its own `bedrock.yaml`, for example `bedrock-longhorn.yaml` and
`bedrock-harvester.yaml`. Each file has its own `account` and `profile`, so pass the file to every
command:

```sh
bedrock-admin plan  -f bedrock-harvester.yaml
bedrock-admin apply -f bedrock-harvester.yaml
bedrock-admin usage -f bedrock-harvester.yaml
```

(or `export BEDROCK_ADMIN_CONFIG=bedrock-harvester.yaml`). Nothing is shared between accounts: each
has its own users, budgets, monthly Lambda, web search gateway and cost export, and each needs its
own Step 1.6 and Step 1.7 from your org admin.

The same person can be a user in several accounts; they get a role in each. `bedrock setup` takes
the account from the SSO profile it signs in with, so they need an SSO profile for each account
(`aws configure sso` once per account, with any role they have there). The messages you send differ
per account:

- First account: `bedrock setup --sso-profile <SSO profile for account A>`. It writes the `bedrock`
  profile.
- Second account: `bedrock setup --sso-profile <SSO profile for account B> --profile-name
  bedrock-<product>`, for example `--profile-name bedrock-harvester`, so it doesn't replace the
  first one.

`apply` prints only `bedrock setup` (with `--id`, `--region` or `--role-arn` when needed); add
`--sso-profile` and `--profile-name` yourself for someone already in another account. They then use
`bedrock doctor --profile-name bedrock-harvester`, and the same flag for `claude` and `usage`, or
`AWS_PROFILE=bedrock-harvester` for Claude Code and other tools.

## Emails

Only AWS Budgets sends email, from `budgets@costalerts.amazonaws.com`, with fixed wording:

| Email | Sent to |
| --- | --- |
| Alert at each `alert_at_percent` | The user's `notify` address |
| Paused at `pause_at_percent` | The user's `notify` address |

No email is sent for a pause or unpause by hand, an unpause by `apply`, or a monthly report.

## Things to know

- The pause is a soft limit. AWS Budgets updates spend about three times a day, so the pause comes
  3 to 16 hours after someone crosses the trigger, and they can go somewhat over.
- After a limit raise unpauses someone, more spend can still arrive. If it passes the new trigger,
  they are paused again, which is intended.
- A pause takes effect within seconds of the policy change, including in running sessions.
- Personal role credentials last one hour and are refreshed automatically while the SSO sign-in is
  valid.
- `--json` works on every command, for scripts. Exit codes: 0 ok, 1 error, 2 ran fine but found a
  problem.

## Run the tests

The end-to-end tests run the real binaries against a real AWS account, by hand, in a container. See
[tests/e2e/README.md](tests/e2e/README.md). The unit tests run with `go test ./...`; see
[CONTRIBUTING.md](CONTRIBUTING.md#development).

## Cheat sheet

| I want to | Run |
| --- | --- |
| Write the settings | `bedrock-admin configure` |
| See what would change | `bedrock-admin plan` |
| Make it so | `bedrock-admin apply` (`--yes` to remove users, `--no-delete` to skip removals) |
| Add, remove or change a user | Edit `bedrock.yaml`, then `plan` and `apply` |
| Unblock a paused user and keep a cap | Raise `limit_usd` or `pause_at_percent`, then `apply` |
| Unblock a paused user for the month | `bedrock-admin unpause <email> --reason "..."` |
| Block a user now | `bedrock-admin pause <email> --reason "..."` |
| See spend | `bedrock-admin usage` |
| Capture an account in a file | `bedrock-admin export > bedrock.yaml` |
| Check my setup | `bedrock-admin doctor` |
| Remove everything | `users: []`, `bedrock-admin apply --yes`, then `bedrock-admin uninstall` |
| Set up a user's machine | `bedrock setup` (the user runs it) |
