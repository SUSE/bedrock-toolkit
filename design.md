# Amazon Bedrock: design

How and why per-person Bedrock budgets work, for reviewers in IT, security and finance. To set it
up, see the [admin guide](admin-guide.md). Users follow the [user guide](user-guide.md).

## Contents

1. [Goals](#1-goals)
2. [Key concepts](#2-key-concepts)
3. [Desired state: `bedrock.yaml`](#3-desired-state-bedrockyaml)
4. [Account setup steps](#4-account-setup-steps)
5. [Personal roles](#5-personal-roles)
6. [Budgets and the pause](#6-budgets-and-the-pause)
7. [Pause states and limit changes](#7-pause-states-and-limit-changes)
8. [The monthly Lambda, snapshots and central reports](#8-the-monthly-lambda-snapshots-and-central-reports)
9. [Usage and untracked spend](#9-usage-and-untracked-spend)
10. [The `id`: several setups in one account](#10-the-id-several-setups-in-one-account)
11. [Security model](#11-security-model)
12. [Behavior and limits](#12-behavior-and-limits)
13. [Testing](#13-testing)
14. [Implementation and release](#14-implementation-and-release)
15. [Later and open items](#15-later-and-open-items)

## 1. Goals

- Each user has a monthly Bedrock budget. When they reach it, only they are paused.
- One command, `bedrock-admin apply`, sets up the whole account. It is safe to re-run.
- Users are managed by editing one file, reviewed in Git and applied by `apply`.
- One view of everyone's spend, this month or past months, including spend outside the budgets.
- Direct model calls outside personal roles can be blocked, not only reported.
- Configuration (who has access, limits) lives only in the file. Runtime actions (`pause`,
  `unpause`) stay commands, and re-applying the file never undoes them.
- Raising a paused user's limit in the file is enough to restore their access.
- Everything is headless: flags, `--json`, `--yes` and meaningful exit codes (0 ok, 1 error, 2 ran
  fine but found a problem).

There are two CLIs:

| CLI | For | Commands |
| --- | --- | --- |
| `bedrock-admin` | The account admin | `configure`, `doctor`, `models`, `plan`, `apply`, `export`, `uninstall`, `pause`, `unpause`, `usage` |
| `bedrock` | Users | `setup`, `doctor`, `claude`, `models`, `usage` |

## 2. Key concepts

### SSO users and IAM users

| | IAM Identity Center (SSO) | IAM user |
| --- | --- | --- |
| Credentials | Temporary only | Password or long-lived access keys |
| Lifecycle | Managed centrally from the identity provider; offboarding is automatic | Managed separately in each account |
| Caller ARN | `...:assumed-role/AWSReservedSSO_<Set>_<id>/achen@example.com` | `...:user/achen` |
| CLI sign-in | `aws sso login` | Access key (no console access needed) |

SSO is the default. A user without SSO may sign in with an IAM user's access key instead (`iam_user:`
on their entry, see [IAM users](#iam-users)); they still go through their personal role, with the
same budget and pause.

### Why SSO alone is not enough for per-person budgets

- Everyone assigned a permission set shares one IAM role in the account.
- The person's email appears only as the session name, at the end of the caller ARN.
- The session name is enough for reports: the cost export's `line_item_iam_principal` column has
  it.
- It is not enough for AWS Budgets, which filters on tags, not on parts of an ARN.
- A budget action on the shared role would pause everyone who uses it.

So each person gets their own IAM role, with their own tags.

### Resource tags and IAM principal tags

| | Resource tag | IAM principal tag |
| --- | --- | --- |
| Answers | What was used? | Who used it? |
| Set on | The resource (EC2, S3, inference profile) | The IAM role or user making the call |
| Services | Most AWS services | Amazon Bedrock only |
| Can be activated | After a tagged resource has cost | After the tagged principal has made at least one Bedrock call, then up to 24 hours |
| CUR 2.0 key | `resourceTags/...` | `iamPrincipal/...` |

The same key, for example `owner`, can exist as both types. They are separate cost allocation tags
and must be activated separately. The budgets use the IAM principal tag `iamPrincipal/owner`.
Reference: [Using IAM principal for cost allocation](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/iam-principal-cost-allocation.html).

### How Claude models are billed

Claude models are billed through AWS Marketplace, one product per model, for example
`Claude Sonnet 4.5 (Amazon Bedrock Edition)`. A budget or report filtered on the service "Amazon
Bedrock" misses most model spend. The budgets filter on the owner tag instead, which covers
Marketplace charges too.

### Design decisions

| Decision | Chosen | Rejected and why |
| --- | --- | --- |
| Identity | SSO | IAM users: long-lived keys, separate lifecycle, not covered by SSO offboarding |
| Per-person cost attribution | IAM principal tag on a personal role | Application inference profiles per user: one per user per model, too many. SSO session tags: need IT changes, and billing support is unconfirmed. |
| Per-person enforcement | A budget action attaches a shared deny policy to the personal role | A deny on the shared SSO role: pauses everyone, or needs one conditional policy per user, and Identity Center may overwrite it |
| Model access | Anthropic Claude models, or only those in `models:` plus the built-in baseline models when listed; the Claude Fable family is always denied (not approved) | All models: less predictable spend, and most use is Claude. Per-model budgets: Budgets can't pause per model. |
| Getting into the personal role | SSO (or an IAM user's access key), then `sts:AssumeRole` (role chaining) | IAM account access manager: no chaining, but IT must enable it in the management account. See [Later](#15-later-and-open-items). |
| Configuration | A desired-state file and `plan`/`apply` | Copy-paste commands and per-user scripts: easy to drift, no review |
| Reading spend | The cost export's Parquet files, read directly | Cost Explorer: can't group by caller. A query engine (DuckDB): an extra install for four columns. |

## 3. Desired state: `bedrock.yaml`

One file describes the account and its users. It has no secrets, only names, so it can live in
Git. `bedrock-admin plan` compares it with the account, and `bedrock-admin apply` makes the account
match.

```yaml
id: acmeprod                    # names and tags what apply creates; default bedrock
account: "111122223333"
region: us-west-2
profile: bedrock-admin          # AWS CLI profile
product: genai-tools            # product tag on every personal role; a user's product: overrides it

models:                         # optional: the only models users may call, with the baseline; checked by apply and doctor
  - us.anthropic.claude-opus-5-5
  - us.anthropic.claude-sonnet-5-5
  - us.anthropic.claude-haiku-5-5

defaults:
  limit_usd: 50
  pause_at_percent: 100         # pause when actual spend reaches this % of the limit
  alert_at_percent: [80]        # email alerts on actual spend; [] for none
  notify: owner                 # the user's email, or a fixed address

cost_export:                    # optional; the default bucket is bedrock-cur-<account>-<id>
  bucket: bedrock-cur-111122223333-acmeprod

block_direct_calls:
  enabled: true
  method: permission-set        # or scp
  admin_permission_set: BedrockAdmin

web_search:                     # optional: Step 1.8
  enabled: false                # true needs region us-east-1, eu-west-1 or ap-northeast-1
  exclude_domains: [example.com]

report:                         # optional: central reporting, see section 8
  bucket: bedrock-reports-999999999999
  region: us-east-1             # the bucket's region
  include_emails: false

users:
  - email: achen@example.com
  - email: bkim@example.com
    limit_usd: 100
    pause_at_percent: 110
    product: research             # this user's role gets another product tag
```

- Which file: `-f`, then `$BEDROCK_ADMIN_CONFIG`, then `./bedrock.yaml`, then
  `~/.config/bedrock-admin/bedrock.yaml`. Flags win over environment variables, which win over the
  file.
- The `id` is 1 to 20 lowercase letters and digits, default `bedrock`. Each file is one setup; see
  [section 10](#10-the-id-several-setups-in-one-account).
- The user's name is derived from the email unless `name:` is set, at most 30 characters. It names
  the role (`bedrock-user-<id>-<name>`) and the budget (`bedrock-<id>-<name>`).
- The trigger is `limit_usd × pause_at_percent / 100`. Both are percentages on the budget, so the
  trigger follows the limit. `pause_at_percent` is 1 to 200. Forecast-based pauses and fixed-dollar
  thresholds are not offered: forecasts are noisy early in the month, and fixed amounts would not
  follow the limit.
- `models:` is optional. When listed, personal roles may call only those models and the baseline models (`Config.AllowedModels`): each inference profile and its foundation model in every region (cross-region profiles route to several). The baseline is `models.toml` in the repository, built into `bedrock-admin` and `bedrock` with `go:embed` (each release pins its copy), so the Claude models the user CLI and Claude Code use always stay allowed; it lists each model with every prefix (`us.`, `eu.`, `global.`; an ID without a prefix gets all three) and the policy allows them all, since the user CLI picks the prefix from each user's region, which can differ from the setup's; Step 1.5 enables them all, calling another geography's `us.`/`eu.` profiles from us-east-1 or eu-central-1 (`Config.ModelRegion`), so users in any region find them enabled; `doctor`, `configure`, `bedrock doctor` and `bedrock models` use only the region's (`config.BaselineModels`: its `us.`/`eu.` prefix, or `global.` in other regions; a US or EU region can call the `global.` ones too, but they're the same models), and `export` drops the other baseline entries. When empty, they may call any Claude model except Fable, and `apply` still enables every baseline model (personal roles can't enable a model themselves) and `doctor` tests the region's. Listing a non-Claude or Fable model is a config error; `policy.AllowedModel` is the only rule on what may be listed (its wording is `policy.AllowedModelsText`), and the policy statements, budgets, usage and `Converse` test calls don't depend on the provider, so allowing other providers later means changing that rule. `bedrock-admin models` lists the region's inference profiles from every provider, `--filter` keeps those whose ID or name contains a text, and its USERS column marks what personal roles may call. Its REGIONS column shows the regions each allowed profile routes requests to, from `GetInferenceProfile` (`any` for `global.` profiles, which AWS reports only as the calling region). `configure` fills `models:` in a new file with the baseline models.
- The full list of fields and defaults is in the
  [admin guide](admin-guide.md#2-create-bedrockyaml).

### Configuration and runtime actions

| | Configuration, in `bedrock.yaml` | Runtime actions, as commands |
| --- | --- | --- |
| What | Who has access, limit, pause and alert thresholds, notify address, name, account settings | `pause`, `unpause` |
| Changed by | Editing the file, then `plan` and `apply` | Running the command |
| Lasts | Until the file changes | Until AWS Budgets, the monthly Lambda or another command changes it |
| Record | Git history | `--reason`, saved as tags on the role, plus CloudTrail |

Pause state is not in the file. The only way the file affects it is a change to the trigger (see
[limit changes](#limit-changes)). Urgent runtime actions skip review on purpose: they are temporary,
and the record is the role tag and CloudTrail. Pause overrides in the file (for example an
"unpaused until" date) were rejected: a date left in the file is easy to forget.

## 4. Account setup steps

`plan` and `apply` handle these steps, in this order, then the users.

| Step | Creates or checks | Why |
| --- | --- | --- |
| 1.1 | A CUR 2.0 cost export `bedrock-cur-<id>` with caller identity, and its private bucket `bedrock-cur-<account>-<id>` (writable only by Data Exports) | The only source of who made each call, including calls no budget sees. Cost Explorer can't group by caller. |
| 1.2 | The `bedrock-deny-<id>` policy (path `/bedrock/`) | The pause: one policy per setup that denies `bedrock:InvokeModel`, `InvokeModelWithResponseStream`, `CreateModelInvocationJob`, `CallWithBearerToken` and `bedrock-agentcore:InvokeGateway` (web search) |
| 1.3 | The budget actions role `bedrock-budget-actions-<id>` | AWS Budgets needs a role to attach the policy. It may only attach and detach `bedrock-deny-<id>` on the id's roles under `/bedrock-users/<id>/` and on IAM users tagged `bedrock-admin:id=<id>`. |
| 1.4 | The monthly Lambda `bedrock-monthly-unpause-<id>` and its schedule (06:00 UTC on the 1st); with `report:`, a daily report schedule | Re-arms every pause of the id's users each month, after saving a snapshot; sends the central report. See [section 8](#8-the-monthly-lambda-snapshots-and-central-reports). |
| 1.5 | Model access: a first call to each model in `models:` and every baseline model (only those when `models:` is empty), from a region of the model's geography | Some models need a first call, from the account, before anyone can use them |
| 1.6 | The `iamPrincipal/owner` cost allocation tag is active | Budgets can only filter on active tags. It can be activated only after a personal role has made a Bedrock call, so it may wait up to a day after the first user starts. Only the management account can read and activate it; from a member account Step 1.6 is `ok` when a budget on the tag has spend or the cost export carries the tag, else a warning (`WARN` in `doctor`) that doesn't change the exit code. |
| 1.7 | Model calls outside personal roles are blocked | See below |
| 1.8 | Only with `web_search.enabled`: the web search gateway, its target and its role | Claude Code's built-in web search doesn't work on Bedrock. See [Step 1.8](#step-18-web-search). |
| 1.9 | Only when a user has `iam_user:`: the managed policy `bedrock-iam-users-<id>` (path `/bedrock/`) | Lets those IAM users assume a personal role and read their own tags, and carries the Step 1.7 deny for them. `apply` attaches it per user. See [IAM users](#iam-users). |

If an account step fails, `apply` skips the users. Step 1.6 shows "waiting for first use" until
the tag can be activated. Steps that need the org admin (1.7) stay pending; the rest of `apply` continues, and `apply` exits 2 while something is pending.

### Step 1.7: block direct model calls

`bedrock-admin` runs with account-level admin rights only. It has no AWS Organizations permission.
Both ways to apply the deny are managed from the Organizations management account (or a delegated
admin): a service control policy (SCP), or an inline policy on the permission sets. So Step 1.7 is
a handoff:

1. `plan` shows Step 1.7 as "needs org admin". `apply` writes the policy to
   `bedrock-block-policy.json` in the current directory and prints instructions for the org admin,
   for both methods:
   - SCP on the Bedrock account. Preferred: it covers every permission set, including future ones.
     It has no effect if the Bedrock account is the management account.
   - Permission sets: add the statement to each permission set assigned to the account, except
     `admin_permission_set`, and re-provision.
2. The org admin applies it, and the admin sets `block_direct_calls.method` to match.
3. With `method: permission-set`, `plan` and `doctor` read the inline policy of each
   `AWSReservedSSO_*` role in the account and list any permission set missing the deny (other than
   the admin set). With `method: scp`, SCPs can't be read from a member account, so `plan` shows it
   as applied by SCP and not checkable.

The policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Sid": "BedrockOnlyViaPersonalRole",
    "Effect": "Deny",
    "Action": ["bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream",
               "bedrock:CreateModelInvocationJob", "bedrock:CallWithBearerToken",
               "bedrock:InvokeAgent", "bedrock:InvokeFlow", "bedrock:RetrieveAndGenerate"],
    "Resource": "*",
    "Condition": {
      "ArnLike": {"aws:PrincipalArn":
        "arn:aws:iam::*:role/aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_*"},
      "ArnNotLike": {"aws:PrincipalArn":
        "arn:aws:iam::*:role/aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_BedrockAdmin_*"}
    }
  }]
}
```

The users' permission sets must also allow `sts:AssumeRole` on `role/bedrock-users/*`. The personal
role trusts the account root, so the SSO role needs that permission itself. That is also an org
admin change. `plan` and `doctor` check it the same way (`AdministratorAccess` and
`PowerUserAccess` count as allowing it), and `apply` includes it in the handoff when it's missing.

`enabled: false` only skips the step. Removing a deny that is in place is also the org admin's job.

The deny doesn't cover:

- Users who can create IAM users, roles, access keys or long-term Bedrock API keys, or pass roles
  to Lambda or EC2. Keep these rights out of users' permission sets.
- Admin-level permission sets, which can remove the deny. The untracked section of `usage` is the
  safety net.

The real checks are end to end: `user-access.robot` makes a direct SSO call and expects a deny, and
`usage` lists any spend from SSO roles.

### Step 1.8: web search

Claude Code's `WebSearch` tool is a server-side Anthropic API tool, which Bedrock doesn't offer.
Bedrock AgentCore offers Web Search as a gateway connector instead: an MCP server, with IAM (SigV4)
inbound auth, that Claude Code reaches through AWS's local `mcp-proxy-for-aws`. IAM auth needs no
identity provider, and calls are signed with the user's personal role, so pausing works the same as
for models.

| Resource | Name | Notes |
| --- | --- | --- |
| Role | `bedrock-web-search-gateway-<id>` (path `/bedrock/`) | Trusts `bedrock-agentcore.amazonaws.com` for the setup's gateway. May call only `InvokeWebSearch` on the Web Search tool (and `InvokeGateway`). No model or data access. |
| Gateway | `bedrock-web-search-<id>` | MCP, `AWS_IAM` authorizer, tagged with the `id`. Its ID and URL are generated. |
| Target | `web-search` | Connector `web-search`, version pinned in `bedrock-admin`, credentials from the gateway role, `exclude_domains` as its domain filter. The MCP tool is `web-search___WebSearch`. |

- Personal roles: with web search on, `bedrock-invoke` also allows `InvokeGateway` on
  the setup's gateway (`gateway/bedrock-web-search-<id>-*` in the region: AgentCore adds a random
  suffix to the name), and `ListGateways`/`GetGateway` so `bedrock claude` and `bedrock doctor` can
  find the URL. They look in the profile's region, then in each web search region
  (`config.WebSearchRegions`), since a user's region can differ from the setup's, and use the
  gateway's region (from its URL) for the MCP proxy's `--region` and the test calls. The
  wildcard keeps the policy fixed before the gateway exists. With it off, the
  statements go away.
- `bedrock-deny-<id>` always denies `InvokeGateway`, so the pause covers search whether web search is on
  or not, and the pause policy doesn't change with the setting.
- Gateway and target creation are asynchronous; `apply` waits for `READY` (2 minutes at most) and
  retries creation while the new role propagates. A `FAILED` gateway or target is a conflict: turn
  web search off, apply, then on again.
- Turning it off deletes the target, then the gateway, then the role. `uninstall` does the same.
- Only `us-east-1`, `eu-west-1` and `ap-northeast-1` offer Web Search; the config is rejected
  elsewhere.
- `bedrock claude` tests one search (one signed MCP `tools/call`, less than $0.01) and prints the
  MCP server for `claude mcp add-json --scope user` and the desktop app, with `mcp-proxy-for-aws`
  pinned to a tested version (it runs with the user's credentials). `bedrock doctor` checks `uvx`
  and makes a free `tools/list` call.
- Searches cost $7 per 1,000. This is a real cost that the setup doesn't manage: it manages only
  Bedrock model spend. The cost export records no caller and no `iamPrincipal/owner` tag on
  AgentCore rows (checked 2026-10), so personal budgets and the pause never count searches.
  `usage` lists them as untracked spend labelled `web search (AgentCore)`.

## 5. Personal roles

Each user gets one role per setup, `bedrock-user-<id>-<name>`, under the path `/bedrock-users/<id>/`.

| Property | Value | Why |
| --- | --- | --- |
| Tag `owner` | The user's SSO email | Billing records it on every call as `iamPrincipal/owner`; the budget filters on it |
| Tag `product` | The user's `product`, else `product` from the file | Charge spend to a team or product |
| Tag `bedrock-admin:id` | The file's `id` | Ownership (see [section 10](#10-the-id-several-setups-in-one-account)) |
| Inline policy `bedrock-invoke` | Call and stream the models in `models:` and the baseline, or any `anthropic.claude-*` foundation model and inference profile when empty; list and read models; deny the Claude Fable family; read the user's own budget (`ViewBudget`, its budget actions and their history) and the role itself (`GetRole`, `ListRoleTags`, `GetRolePolicy`) | Bedrock only, Claude only; `bedrock usage` sees nobody else's spend, `bedrock models` nobody else's models |
| Max session | 1 hour | The limit for role chaining |

### Trust policy

The role trusts the account root, with three conditions that must all hold:

```json
{
  "Effect": "Allow",
  "Principal": {"AWS": "arn:aws:iam::111122223333:root"},
  "Action": "sts:AssumeRole",
  "Condition": {
    "ArnLike":      {"aws:PrincipalArn": "arn:aws:iam::111122223333:role/aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_*"},
    "StringLike":   {"aws:userid": "*:achen@example.com"},
    "StringEquals": {"sts:RoleSessionName": "achen@example.com"}
  }
}
```

- `aws:PrincipalArn`: only SSO roles may use this statement, not IAM users or other roles, and not
  another personal role. IAM users get their own statement, below.
- `aws:userid`: Identity Center sets the part after the colon to the user's SSO email. The caller
  can't choose it.
- `sts:RoleSessionName`: the session name must be the email, so the caller ARN of every call ends
  in the email, in CloudTrail and in the cost export.

### IAM users

With `iam_user: <name>` on a user, the trust gets a second statement (Sid `IAMUser`), and the SSO
statement stays, so the user may use either:

```json
{
  "Sid": "IAMUser",
  "Effect": "Allow",
  "Principal": {"AWS": "arn:aws:iam::111122223333:root"},
  "Action": "sts:AssumeRole",
  "Condition": {"StringEquals": {
    "aws:PrincipalType": "User", "aws:username": "dlee-bedrock",
    "sts:RoleSessionName": "dlee@example.com"}}
}
```

- The IAM user is created by the admin, outside `bedrock-admin`; `apply` never creates or deletes
  IAM users or keys. `aws:username` is case-sensitive, so `plan` warns when the name differs in case.
- `apply` attaches the Step 1.9 policy `bedrock-iam-users-<id>` to the IAM user: `sts:AssumeRole`
  on the id's personal roles (each trust admits only its own IAM user), `iam:ListUserTags` on itself,
  and with `block_direct_calls.enabled` the same deny as Step 1.7, so the access key can't call
  models directly. This needs no org admin. One managed policy instead of an inline one per user,
  so removals are found with `ListEntitiesForPolicy`.
- It tags the IAM user like the personal role, `owner=<email>`, `product` and `bedrock-admin:id`, so
  the budget's `iamPrincipal/owner` filter counts the IAM user's direct Bedrock calls, the same as an
  SSO user's role. Billing records IAM principal tags only on Bedrock line items, so the IAM user's
  other AWS spend never reaches the budget. `bedrock setup --source-profile <key profile>` reads the
  email from the `owner` tag (or `--email`). The setup command `apply` prints adds
  `--default-region <setup region>`, used only when neither `--region` nor the key profile sets one. `export` rebuilds `iam_user:` from the tags. `usage` counts lines of an IAM user tagged
  with a user's `owner` as that user's.
- The pause action's `IamActionDefinition` lists the IAM user in `Users` next to the role, so AWS
  Budgets attaches and detaches `bedrock-deny-<id>` on both. `apply` updates `Users` in place (no
  re-arm) and, when the user is paused, attaches or detaches the deny itself. `pause`/`unpause` act
  on the action's `Users` too, and the monthly Lambda re-attaches a pause by hand to them after the
  reverse. The budget actions role (Step 1.3) may `iam:AttachUserPolicy`/`DetachUserPolicy` and
  the Lambda role (Step 1.4) `iam:AttachUserPolicy`, only for the deny policy and only on IAM users
  tagged `bedrock-admin:id=<id>`.
- Removing `iam_user:` (or the user) detaches the policy and any pause, removes the tags and takes
  the IAM user out of the pause action, with `--yes`. The
  policy is deleted when no user has `iam_user:` left; `uninstall` detaches it from every user and
  group first.

`bedrock setup` writes an AWS CLI profile that uses the SSO profile as `source_profile` and sets
`role_session_name` to the SSO email. `bedrock doctor` checks each part. `bedrock claude` runs
`claude -p` once with the profile, in a temporary empty `CLAUDE_CONFIG_DIR` (no tools, MCP servers,
hooks or plugins), to show it works, then prints the Claude Code settings (`env` block, and
`awsAuthRefresh` when the source profile signs in with SSO; an access key profile
needs none). It also prints the values for the Claude desktop app's Amazon Bedrock settings (region, profile, AWS CLI
path, model list). It never writes Claude Code's settings: the user adds them, or uses `/setup-bedrock`.
`bedrock usage` reads the user's own budget (spend, forecast, the pause action's threshold) and the
pause state (budget action status and the role's pause tags) with the personal role. The budget name
comes from the profile's `role_arn`. It doesn't read the cost export, which holds everyone's spend.
`bedrock models` reads the role's own `bedrock-invoke` policy (`policy.ListedModels`) and lists the
allowed models the profile's region can call (`config.CallableModels`); when the policy allows any
Claude model, or the role predates `GetRolePolicy`, it lists the region's baseline models.
`bedrock doctor` tests the region's baseline models (`config.BaselineModels`), and `bedrock claude`
takes its Opus, Sonnet and Haiku from them (`config.ClaudeModels`): the user CLI embeds the same
`models.toml`.

## 6. Budgets and the pause

Each user has a monthly cost budget, `bedrock-<id>-<name>`:

- Limit: `limit_usd`. Filter: the cost allocation tag `iamPrincipal/owner` equals the user's email.
  Metric: unblended cost.
- One email notification on actual spend for each `alert_at_percent`, sent to the `notify`
  address.
- One budget action: at `pause_at_percent` of actual spend, with automatic approval, attach
  `bedrock-deny-<id>` to the user's role, run by the budget actions role. It also emails the `notify`
  address.

`apply` updates the limit, the notifications and the action's threshold in place. It never
recreates the action for a limit change, because a new action is armed.

The pause is a deny, so it wins over the role's allow and takes effect within seconds, including in
running sessions. The user sees `AccessDeniedException ... explicit deny`.

`bedrock-admin pause` attaches `bedrock-deny-<id>` to the role directly, without the budget action, and
tags the role with `bedrock:paused-by`, `bedrock:paused-at` and `bedrock:pause-reason`. `unpause`
records `bedrock:unpaused-by`, `-at` and `-reason`. Every change is also in CloudTrail.

## 7. Pause states and limit changes

Pause state lives in each user's budget action, plus the `bedrock:paused-by` tag for pauses by hand.
AWS Budgets, `pause`/`unpause`, the monthly Lambda and, in one case, `apply` change it.

| State | Recorded as | Meaning | Shown as |
| --- | --- | --- | --- |
| Armed | Action `STANDBY` or `PENDING` | Access on; pauses at the trigger | `armed` |
| Paused (over the limit) | Action `EXECUTION_SUCCESS` | AWS Budgets attached `bedrock-deny-<id>` | `PAUSED (date)` |
| Paused by hand | `bedrock:paused-by` tag; `bedrock-deny-<id>` attached directly | An admin paused them; the action is untouched | `PAUSED by hand (date: reason)` |
| Off for the month | Action `REVERSE_SUCCESS` | Access on; won't pause again this month | `off until <1st>` |

| Event | From | To |
| --- | --- | --- |
| Spend reaches the trigger | Armed | Paused (over the limit) |
| `pause` | Any | Paused by hand |
| `unpause` | Paused by hand | Back to the action's state (usually armed) |
| `unpause` | Paused (over the limit) | Off for the month |
| `unpause` | Armed or off | Unchanged ("not paused", exit 0) |
| `apply` raises the trigger above spend | Paused (over the limit) | Armed, at the new trigger |
| `apply`, any other change | Any | Unchanged |
| Monthly Lambda on the 1st | Paused (over the limit) or off | Armed (snapshot saved first) |
| Monthly Lambda on the 1st | Paused by hand | Unchanged |

Why `unpause` turns the pause off for the month instead of re-arming it: re-arming would pause the
user again within hours, because their spend is already over the trigger.

### Limit changes

What `apply` does when `limit_usd` or `pause_at_percent` changes depends on the state. What matters
is the new trigger. "Spend" is the actual spend AWS Budgets reports, the figure that triggers the
pause.

| State before | Change | `apply` does | `plan` notes |
| --- | --- | --- | --- |
| Armed | Any | Updates the limit; the pause fires at the new trigger | |
| Armed | New trigger at or below spend | Same; AWS Budgets pauses them within hours | `warning: spent $X, will be paused` |
| Paused (over the limit) | New trigger above spend | Updates the limit, unpauses and re-arms at the new trigger | `paused → unpaused, spent $X` |
| Paused (over the limit) | New trigger at or below spend | Updates the limit; stays paused | `still over: spent $X, stays paused` |
| Paused by hand | Any | Updates the limit; stays paused | `paused by hand, unchanged` |
| Off for the month | Any | Updates the limit; stays off until the 1st | `pause off until <1st>, unchanged` |

- The unpause reverses the action, waits, then resets it to armed. It records
  "limit raised to $X in bedrock.yaml" as the reason on the role.
- Billing lags 3 to 16 hours, so more spend may arrive after the unpause. If it passes the new
  trigger, the user is paused again, which is intended.
- There is no option to skip the unpause: the file is the source of truth for limits. To keep
  someone paused anyway, use `pause --reason`, which `apply` leaves alone.
- A budget action changed outside the file must be recreated, which re-arms it. `apply` refuses
  without `--yes`.
- Removing a user and adding them back creates a new, armed action. Removals need `--yes`.

## 8. The monthly Lambda, snapshots and central reports

A Go Lambda, `cmd/monthly-unpause`, runs at 06:00 UTC on the 1st of each month
(EventBridge Scheduler, `cron(0 6 1 * ? *)`). For every pause action on a `bedrock-*` budget in
the account:

1. It saves a snapshot of the month that just ended.
2. It re-arms each pause action that is paused (over the limit) or off: it reverses the action if
   needed, then resets it to armed.
3. It leaves pauses by hand alone. They last until an admin runs `unpause`.

The Lambda uses the `provided.al2023` runtime on arm64. It shares the pause, re-arm and snapshot
code with the CLI. It is built by `go generate` and embedded in the `bedrock-admin` binary with
`go:embed`, and `apply` creates or updates it (Step 1.4). `plan` compares the deployed code's
SHA-256 with the embedded one and shows an update when they differ, so upgrading the CLI and running
`apply` upgrades the Lambda. A binary built without `go generate` (for example by `go install`) has
no Lambda code, and Step 1.4 shows a conflict.

### Snapshots

AWS Budgets keeps no history of limits or pause states, so the Lambda saves one before it re-arms:
`s3://<bucket>/<snapshot_prefix>/YYYY-MM.json` in the cost export bucket (default prefix
`bedrock-admin/snapshots`). The Lambda's role may write only there; the bucket is in the same
account, so the bucket policy needs no change.

| Field | Content |
| --- | --- |
| `month`, `taken_at`, `account` | Which month, when, which account |
| `users[]` | `email`, `name`, `budget`, `role`, `limit_usd`, `pause_at_percent`, `trigger_usd`, `state`, and for a pause, `paused_at`, `paused_by` and `reason` |

`bedrock-admin usage --month YYYY-MM` uses the snapshot for past limits and pause states. Without
one, limits come from the file and pause state is unknown.

### Who has set up

`bedrock-admin users` (`internal/fleet.Collect`) lists the id's onboarded users (personal roles with a
pause action) with their pause state and the IAM `RoleLastUsed` date of each role. `bedrock setup`
uses the role, so a date means the person has set up; AWS updates it with up to about 4 hours of
delay. Users in the file without a role show `not applied`.

### Central reports

Optional, for one central admin following many setups, possibly in many accounts. The tool assumes
no vendor: the central admin ([central admin guide](central-admin-guide.md)) creates the bucket in
their own account with `bedrock-admin fleet init` and gives the `report:` section to the setups'
admins.

- **Bucket** (`fleet init`, in the central admin's account): public access blocked,
  `BucketOwnerEnforced`, versioning on, SSE-S3, no lifecycle (reports are kept). The bucket policy
  allows `s3:PutObject` on `arn:aws:s3:::<bucket>/${aws:PrincipalAccount}/*` to principals of one
  organization (`aws:PrincipalOrgID`, default: the caller's, read with `DescribeOrganization`) or to
  listed accounts, and denies requests without TLS. Setups can't list or read, and can't write
  another account's prefix. `fleet init` refuses a bucket tagged `bedrock-admin:id` (a setup's cost
  export bucket), and tags its bucket `bedrock-admin:report-bucket`: without `--bucket`, `fleet init`
  and `fleet report` list the account's buckets (`ListBuckets`, `GetBucketTagging`) and use the one
  tagged bucket, in its own region; with several they ask for `--bucket`. With none, `fleet init`
  creates `bedrock-reports-<account>` and `fleet report` stops.
- **Report** (`internal/fleet.Report`, schema 1): `s3://<bucket>/<account>/<id>.json` with the id,
  region, `bedrock-admin` version, status (`active` or `uninstalled`), source (`apply`, `lambda` or
  `uninstall`), time, default limit, pause and alert percentages, `models:`, web search, user counts
  (onboarded, set up, paused, with an IAM user), the month and the sum of the id's budgets' actual
  spend. With `include_emails: true`, also each user's email, kind, set up and paused.
- **Senders**: `apply` after a run with no failures, and `uninstall` (status `uninstalled`), with
  the admin's credentials; a failure is a warning. The Lambda, invoked daily at 07:00 UTC by a
  second schedule `bedrock-daily-report-<id>` with input `{"action":"report"}`. `apply` passes it
  the setup part as the `REPORT_SETUP` environment variable (`fleet.Setup` as JSON) and grants its
  role `s3:PutObject` on `<bucket>/<account>/*`, plus `iam:GetRole` on the id's personal roles. With
  `report:` removed, `apply` removes the variable, the permission and the schedule.
- **Reading**: `fleet report` lists the bucket and shows the latest report of each setup; it skips
  objects whose content doesn't match their key.
- The report's `region` is in the file so neither the CLI nor the Lambda needs to read the bucket's
  location across accounts.

## 9. Usage and untracked spend

`bedrock-admin usage` reads spend from two places, and no Cost Explorer:

| Data | Source |
| --- | --- |
| This month's limits and pause states | AWS Budgets |
| Spend, tracked and untracked, this month and past months | The cost export's Parquet files |
| Past limits and pause states | The snapshots |

The forecast for this month is the spend so far, scaled to the whole month.

The CLI lists `s3://<bucket>/<prefix>/<export>/data/BILLING_PERIOD=YYYY-MM/*.parquet`, streams each
file, reads only four columns, and adds up the cost per caller. No query engine is needed.

| Column | Use |
| --- | --- |
| `line_item_iam_principal` | The caller ARN; rows without one are not model calls |
| `tags` | `iamPrincipal/owner`; a row without a known owner is untracked |
| `line_item_unblended_cost` | Cost |
| `line_item_usage_start_date` | Last used |

Untracked spend is spend that no budget counted. Each caller is classified by its ARN:

| Used via | Caller |
| --- | --- |
| `SSO role <permission set>` | An SSO session called Bedrock directly, outside the personal role |
| `personal role without owner tag: <role>` | A personal role whose calls had no owner tag, for example before Step 1.6 was active |
| `personal role without budget: <role>` | A personal role tagged with an owner who has no budget in the file |
| `role <role>` | Any other IAM role |
| `IAM user` | An IAM user, including long-term Bedrock API keys |

The "who" is the last part of the ARN: the SSO email for SSO sessions and personal roles. So
untracked spend is still attributed to a person, after the fact. The cost export lags up to about
16 hours.

## 10. The `id`: several setups in one account

Each `bedrock.yaml` is one setup, named by its `id`. Every account-level name ends in the id, and
everything `apply` creates is tagged `bedrock-admin:id` with it (EventBridge schedules can't be
tagged, so for the schedule the id is in its description). Several setups can therefore share one
AWS account, each with its own users, limits, models, pause policy, monthly Lambda, cost export and
bucket. One account with a single setup is the same thing with one id.

| Resource | Name |
| --- | --- |
| Personal role | `bedrock-user-<id>-<name>` under `/bedrock-users/<id>/` (≤ 64 characters: id ≤ 20, name ≤ 30) |
| Budget | `bedrock-<id>-<name>` |
| Pause policy | `bedrock-deny-<id>` |
| Budget actions role | `bedrock-budget-actions-<id>` |
| Monthly Lambda, its role, its schedule | `bedrock-monthly-unpause-<id>` |
| Scheduler role | `bedrock-monthly-unpause-scheduler-<id>` |
| IAM user policy | `bedrock-iam-users-<id>` |
| Web search gateway, its role | `bedrock-web-search-<id>`, `bedrock-web-search-gateway-<id>` |
| Cost export, bucket | `bedrock-cur-<id>`, `bedrock-cur-<account>-<id>` |

`apply`, offboarding, `uninstall` and the monthly Lambda only change resources of their own id:

| Found | Result |
| --- | --- |
| A setup resource tagged with the same id | Managed: created, updated or removed as the file says |
| A setup resource with the same name but another id's tag, or none | Conflict: remove it, then apply |
| A personal role under `/bedrock-users/<id>/` without the tag | `not managed`, left alone |
| Another id's resources | Not looked at (except by `usage`, below) |

- One person can be a user of several setups, with a role and a budget in each. `bedrock setup
  --id <id>` picks the role; `apply` prints the command with `--id` when the id isn't `bedrock`.
- Each setup's cost export covers the whole account. `usage` counts spend through other ids'
  personal roles as neither tracked nor untracked and prints it as one line, "Other setups in this
  account".
- `export` without `--id` takes the id of the only `bedrock-deny-<id>` policy, and asks for `--id`
  when there are several.
- Shared by every setup in the account (org level): the `iamPrincipal/owner` cost allocation tag
  (Step 1.6) and the Step 1.7 deny on the permission sets, and Identity Center itself.

There is no adoption of untagged resources. `bedrock-admin export` captures an existing setup's users
into a file, as a convenience.

## 11. Security model

| Who | Can use a personal role? |
| --- | --- |
| The user, signed in with SSO, session name = their email | Yes, their own role only |
| Another user, with any session name | No |
| An account admin (for example `AWSAdministratorAccess`) | No: the trust conditions apply to every caller |
| IAM users, other roles, or a personal role chaining into another | No: only SSO sessions are trusted |

The protection doesn't depend on admin rights, because `aws:userid` is a value Identity Center
sets.

| Control | Covers | Doesn't cover |
| --- | --- | --- |
| Trust policy | Who can use a personal role | Admins who edit trust policies or tags |
| `bedrock-invoke` policy | Personal roles: Claude only, Fable denied | Calls from other roles |
| Budget and pause | Spend through personal roles | Calls outside personal roles |
| Step 1.7 deny | Model calls from SSO roles except the admin set | IAM users, other roles, API keys, the admin set |
| Web search gateway role | The gateway may call only the Web Search tool | Who may call the gateway: that is the personal roles' `InvokeGateway` right on their setup's gateway |
| `usage` untracked section | Every Bedrock caller in the cost export, after the fact | Nothing in real time |

- `bedrock-admin` needs only account-level admin rights. It never changes AWS Organizations;
  `fleet init` only reads the caller's organization ID, and `fleet` lists the caller's own buckets
  and their tags to find the report bucket.
- Central reports leave the account only when `report:` is set, go only to that bucket, and hold
  emails only with `include_emails: true`.
- `bedrock-deny-<id>` and the budget actions role are scoped to the id's `/bedrock-users/<id>/` roles
  and the IAM users tagged `bedrock-admin:id=<id>`.
- The cost export bucket is private and writable only by Data Exports.
- `bedrock.yaml` has no secrets.
- Personal roles can call only Bedrock. An agent such as Claude Code running with the personal role
  can't do anything else in AWS.

## 12. Behavior and limits

| Topic | Behavior |
| --- | --- |
| Billing delay | Budgets and the cost export run 3 to 16 hours behind usage |
| Pause delay | A soft limit: the pause comes 3 to 16 hours after spend crosses the trigger, so users can go somewhat over |
| Pause effect | Within seconds, including running sessions. Access returns within about a minute of an unpause. |
| Re-arming | A reversed action never fires again until it is reset. `apply` and the Lambda always do both. |
| Untracked spend | No budget, no alert, no pause, no team charge. Found after the fact by `usage`. |
| Credentials | Role chaining limits personal-role credentials to 1 hour. The AWS CLI, the SDKs and Claude Code refresh them while the source profile's sign-in is valid: the Identity Center session (8 hours by default, set by the Organizations admin), or, for an IAM user access key, indefinitely. |
| Personal role scope | Bedrock only; other AWS commands need the SSO profile |
| Emails | Only AWS Budgets sends email (alerts and the automatic pause), with fixed wording. No email for a pause or unpause by hand. |
| Claude Code settings | The `/login` wizard writes `~/.claude/settings.json`, which every Claude Code session on the machine reads. Users with a personal Claude account should use a separate `CLAUDE_CONFIG_DIR`. |

## 13. Testing

Go unit tests cover the logic that doesn't need AWS: config validation, flags, the plan diff, the
Parquet reader (against the fixtures in `tests/fixtures/`), the caller classification and the
output formats. They run with `go test ./...`, and CI runs them with `go vet` and `golangci-lint` on
every pull request.

The Robot Framework suites in `tests/e2e/` cover only end-to-end behavior, with the real binaries
and a real AWS account:

| Suite | Covers |
| --- | --- |
| `cli` | A smoke test of the built binaries, with no AWS |
| `account` | The account steps: `plan`, `apply`, re-runs, a second setup in the same account, `uninstall`; central reports: `fleet init` (and finding the bucket by its tag), the reports from `apply`, the Lambda and `uninstall`, `fleet report` |
| `users` | Onboarding, limit changes and offboarding, pause and unpause by hand, `bedrock-admin users` |
| `user-access` | The user flow: `bedrock setup`, calls through the personal role, `bedrock doctor`, `bedrock usage`, `bedrock models`, direct SSO calls denied, a pause seen by the user |
| `iam-user` | A user with an IAM user's access key: `apply` tags the IAM user `owner`, attaches Step 1.9 and adds it to the pause action, `bedrock setup --source-profile`, calls through the personal role, direct key calls denied (with `block_direct_calls` on, even with the key's own Bedrock Allow), a pause denies the key and the personal role, removing `iam_user:` releases the IAM user |
| `usage` | The report, against Parquet fixtures uploaded to the bucket |
| `web-search` | Opt-in: turning `web_search` on creates the setup's gateway and gives a personal role the rights, the role can search, the pause denies it, the printed MCP server runs through the proxy, and turning it off removes the rights, the gateway and its role |
| `billing` | Real spend, the automatic pause and the monthly Lambda, in phases over a day or more |

The e2e suites are run by hand with `tests/e2e/run.sh`, not in CI. Each run applies its own setup
(`id: e2e<run id>`) and uninstalls it at the end, so it can't touch real setups or users in the same
account. See [tests/e2e/README.md](tests/e2e/README.md).

Differences from the original design spec:

- The `cli` suite doesn't test each validation rule. Validation moved to the unit tests.
- `billing.robot` needs only the admin profile. Test users spend through their own personal role:
  a test-only trust statement lets the admin profile assume it with the session name `<email>`.
- In billing phase 2 the monthly Lambda re-arms both user a (paused) and user d (off), so phase 3
  expects a and d paused again. The check that "off stays off despite more spend" is not part of
  phase 3, since the mid-month Lambda run re-arms d.

## 14. Implementation and release

- Go, one module, standard layout: `cmd/bedrock-admin`, `cmd/bedrock`, `cmd/monthly-unpause` and
  `internal/`. One static binary per CLI, nothing else to install.
- Libraries: the AWS SDK for Go v2, `parquet-go` for the cost export, `go.yaml.in/yaml/v3`, `cobra`,
  and `text/tabwriter` for tables.
- CI (`.github/workflows/ci.yml`): `go generate`, `go vet`, `golangci-lint`, `go test -race` on amd64
  and arm64 Linux runners, and a build of both CLIs for every release target.
- Dependabot (`.github/dependabot.yml`): security updates only (Go modules, Actions, the e2e image);
  `.github/workflows/dependabot-merge.yml` squash-merges patch updates after CI passes; minor and
  major ones stay open for review.
- Release (`.github/workflows/release.yml`): on a `v*` tag, GoReleaser builds both CLIs for macOS
  and Linux (amd64 and arm64; no Windows), with the Lambda embedded in `bedrock-admin`, and attaches
  them and checksums to the GitHub release. `install.sh` (sh and curl only) installs either CLI from a
  release, checking `checksums.txt`.

## 15. Later and open items

| Item | Notes |
| --- | --- |
| SES email | For what AWS Budgets can't send: pause and unpause by hand, an unpause by `apply`, a monthly spend report. Needs a verified sender domain and SES production access. |
| Data freshness in `usage` | Show how recent the cost export data is in the untracked section |
| IAM account access manager | Assigns the personal role directly, without role chaining. IT must enable it in the management account; only the trust policy and the user sign-in change. |
| Exception approvals | Decide who may approve an unpause or a limit raise mid-month, and how |
| Exact gateway ARN | `bedrock-invoke` allows `gateway/*`. Narrow it to the one gateway if the account hosts others. |
