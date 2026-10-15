# Amazon Bedrock: user guide

For anyone who uses Claude through Amazon Bedrock, for example in Claude Code. Setup takes a few
minutes.

> Before you begin, your admin must have set up the AWS account and onboarded you. They send you
> the command to run, usually just `bedrock setup`. Until you are onboarded, setup fails at the
> `permission-set access` check.

## Contents

- [Quick start](#quick-start)
- [How it works](#how-it-works)
- [Why use your personal role](#why-use-your-personal-role)
- [Commands](#commands)
- [Before you start](#before-you-start)
- [Setup](#setup)
  - [Step 1. Sign in with SSO](#step-1-sign-in-with-sso)
  - [Step 2. Write your Bedrock profile](#step-2-write-your-bedrock-profile)
  - [Step 3. Check it](#step-3-check-it)
  - [Step 4. Set up Claude Code](#step-4-set-up-claude-code)
  - [Web search](#web-search)
- [Check your spend](#check-your-spend)
- [If something goes wrong](#if-something-goes-wrong)
- [When you're paused](#when-youre-paused)

## Quick start

```bash
aws sso login --profile <your-sso-profile>   # or: aws login
bedrock setup                                # the command your admin sent you
bedrock claude                               # test Claude Code, print the settings to add
```

Then add the printed settings to Claude Code (or the Claude desktop app). Each step is explained
below; click the folded sections for sample output and details.

## How it works

- You use Bedrock through a personal AWS role that only you can use, from your SSO login.
- You can use Anthropic Claude models, except the Claude Fable family, which is not approved. Your
  admin may allow only some of them.
- You have a monthly budget. You get an email from AWS Budgets when you pass the alert threshold
  (usually 80%). When you reach your limit, your Bedrock access is paused until your admin unpauses
  you, or until the 1st of next month.

## Why use your personal role

Calls through your personal role (the `bedrock` profile) count toward your budget and are capped.
Calls from any other profile still show your email in the admin's report, but have no budget, no
alert and no cap, and may be blocked.

<details>
<summary>Personal role compared with other profiles</summary>

You can call Bedrock from other profiles, such as an `AWSAdministratorAccess` SSO login, but
please don't. Those calls are not anonymous: billing still records your SSO email, and your admin
sees them in the monthly `usage` report. What you lose is everything else:

| | Personal role (`bedrock` profile) | Any other profile |
| --- | --- | --- |
| Who made the call | Your email | Your email (found later in a report) |
| Counts toward your budget | Yes | No |
| Alert email before the limit | Yes | No |
| Pause at the limit | Yes | No, so there is no limit at all |
| Cost charged to your team or product | Yes | No, it shows as untracked spend |
| What a coding agent such as Claude Code can do in AWS | Only call Claude models | Everything that profile allows |

Your admin may also block model calls from other SSO profiles. Then the personal role is the only
way in.

</details>

## Commands

| Command | What it does | Section |
| --- | --- | --- |
| `bedrock setup` | Writes the `bedrock` profile for your personal role in `~/.aws/config`, then runs `doctor` | [Step 2](#step-2-write-your-bedrock-profile) |
| `bedrock doctor` | Checks your SSO sign-in, the `bedrock` profile, your personal role and a model call | [Step 3](#step-3-check-it), [If something goes wrong](#if-something-goes-wrong) |
| `bedrock claude` | Tests Claude Code with the `bedrock` profile and prints the settings to use. Changes no Claude settings | [Step 4](#step-4-set-up-claude-code) |
| `bedrock usage` | Shows this month's spend against your budget, where you get paused and whether you are paused | [Check your spend](#check-your-spend) |

Exit codes: 0 ok, 1 error, 2 a check failed. Run `bedrock <command> --help` for the flags.

## Before you start

- You can sign in to the AWS account with SSO, and you have an SSO profile in `~/.aws/config`
  (from `aws configure sso`), or you sign in with `aws login`.
- The AWS CLI v2 is installed.
- The `bedrock` CLI is installed (macOS or Linux; other ways: [README](README.md#install)):

  ```bash
  curl -fsSL https://raw.githubusercontent.com/innobead/bedrock-toolkit/main/install.sh | sh -s -- bedrock
  ```
- Your admin has onboarded you with your SSO username, the email you sign in with (not an alias).
  If you're not sure which one that is, run this with your SSO profile and send your admin the
  address at the end of `Arn` (here `achen@example.com`):

  ```bash
  aws sts get-caller-identity --profile <your-sso-profile>
  ```

  ```json
  "Arn": "arn:aws:sts::111122223333:assumed-role/AWSReservedSSO_Dev_abc123/achen@example.com"
  ```
- If your SSO email doesn't receive mail, give your admin your real mailbox address for the budget
  emails.

## Setup

### Step 1. Sign in with SSO

```bash
aws sso login --profile <your-sso-profile>     # or: aws login
```

`aws login` reuses whatever identity your browser console is signed in as. `bedrock setup` checks
that it is an SSO login.

### Step 2. Write your Bedrock profile

Run the command your admin sent you. Usually it is:

```bash
bedrock setup
```

It writes a `[profile bedrock]` block in `~/.aws/config` (or `$AWS_CONFIG_FILE`) for your personal role, then runs
`bedrock doctor`.

<details>
<summary>What it writes, and options</summary>

```ini
[profile bedrock]
role_arn = arn:aws:iam::111122223333:role/bedrock-users/bedrock/bedrock-user-bedrock-achen
source_profile = my-sso
role_session_name = achen@example.com
region = us-west-2
```

- It reads your SSO identity to find the account and your SSO email, and derives your personal role
  from the email.
- `role_session_name` is set to your exact SSO email. The role only trusts that session name.
- The region comes from your SSO profile. Your admin adds `--region` to the command when Bedrock
  runs in another region, `--id <id>` when your admin's setup has another id than `bedrock`
  (one AWS account can hold several setups; you may be in more than one, with a role in each: use
  `--profile-name` to keep their profiles apart), and `--role-arn` when your role has a custom name.
  Outside a US or EU region the default models are `global.` ones, and `setup` prints a note: if your
  admin allows only `us.` or `eu.` models, pass a matching `--region`.
- It keeps the previous file as `config.bak`. If a `bedrock` profile already exists, it shows the
  old and new block and asks before replacing it. `--yes` replaces it without asking.
- If you have several SSO profiles, it asks which one you sign in with, or pass
  `--sso-profile <profile>`.
- `--profile-name` writes a profile with another name.

</details>

### Step 3. Check it

```bash
bedrock doctor
```

Every line should say `ok`, ending with `All checks passed.` If not, it prints the fix under the
failed check; see [If something goes wrong](#if-something-goes-wrong).

<details>
<summary>Sample output and options</summary>

```
ok    profile: bedrock in /Users/achen/.aws/config
ok    SSO sign-in: achen@example.com (profile my-sso)
ok    permission-set access: may use arn:aws:iam::111122223333:role/bedrock-users/bedrock/bedrock-user-bedrock-achen
ok    personal role: arn:aws:sts::111122223333:assumed-role/bedrock-user-bedrock-achen/achen@example.com
ok    model us.anthropic.claude-opus-5-5: test call works
ok    model us.anthropic.claude-sonnet-5-5: test call works
ok    model us.anthropic.claude-haiku-5-5: test call works

All checks passed.
```

By default it tests Claude Opus, Sonnet and Haiku 5.5 (`us.`/`eu.` inference profiles, `global.` elsewhere). Test
another model with `--model <id>` (repeatable). `--json` prints the checks as JSON. It exits 2 when
a check fails, and prints the fix under it. See [If something goes wrong](#if-something-goes-wrong).

</details>

### Step 4. Set up Claude Code

```bash
bedrock claude
```

It makes one test call with Claude Code and prints the settings to add, for Claude Code and for the
Claude desktop app. It never changes your Claude settings: you add them yourself. If your admin
turned on web search, it also prints how to add it to both; see [Web search](#web-search).

- Your personal role can only call Claude models. For any other AWS command, use your SSO profile.
- Credentials renew automatically while your SSO sign-in is valid.
- To change the profile or region later, edit the settings or run `/setup-bedrock` again.

<details>
<summary>Sample output</summary>

```text
$ bedrock claude
Testing Claude Code with the bedrock profile (one short call; your Claude Code settings are not used)...

ok    Claude Code with us.anthropic.claude-sonnet-5-5: answered OK

Claude Code settings for the bedrock profile. bedrock doesn't write them; add them yourself:

  {
    "awsAuthRefresh": "aws sso login --profile sso",
    "env": {
      "ANTHROPIC_DEFAULT_HAIKU_MODEL": "us.anthropic.claude-haiku-5-5",
      "ANTHROPIC_DEFAULT_OPUS_MODEL": "us.anthropic.claude-opus-5-5",
      "ANTHROPIC_DEFAULT_SONNET_MODEL": "us.anthropic.claude-sonnet-5-5",
      "ANTHROPIC_MODEL": "us.anthropic.claude-sonnet-5-5",
      "AWS_PROFILE": "bedrock",
      "AWS_REGION": "us-west-2",
      "CLAUDE_CODE_USE_BEDROCK": "1"
    }
  }

Where to put them, either way:
  - Work only: merge them into ~/.claude/settings.json. Every Claude Code session on this machine
    reads that file, so all of them then bill to your budget.
  - Next to a personal Claude account: put them in ~/.claude-work/settings.json and start Claude
    Code for work with: CLAUDE_CONFIG_DIR=~/.claude-work claude (for example as an alias in ~/.zshrc).

Or run claude, type /setup-bedrock (or /login, then 3rd-party platform, Amazon Bedrock), pick the
AWS profile bedrock and region us-west-2: the wizard writes the same env block.

Check in Claude Code with /status: it shows Amazon Bedrock and the bedrock profile.
us.anthropic.claude-opus-5-5 costs much more than Sonnet: switch to it with /model only when you need it.

Claude desktop app (Settings, Amazon Bedrock):
  AWS region:        us-west-2
  AWS profile name:  bedrock
  AWS CLI path:      /opt/homebrew/bin/aws
  Model list:        us.anthropic.claude-sonnet-5-5  (the first is the default)
                     us.anthropic.claude-opus-5-5
                     us.anthropic.claude-haiku-5-5
```

</details>

<details>
<summary>How the test works, and keeping models up to date</summary>

The test runs `claude -p` once in a temporary, empty config folder (`CLAUDE_CONFIG_DIR`) with no
tools, MCP servers, hooks or plugins, and removes it afterwards, so your own Claude Code settings and
login are untouched. When it fails, it prints the fix and exits 2; the settings are printed anyway.
Without `claude` on your PATH, or with `--no-test`, it only prints the settings. The last block
holds the values to enter in the Claude desktop app's Amazon Bedrock settings; the AWS CLI path line
appears when `aws` is on your PATH (the app may not see your shell's PATH). `--model` picks the
test model.

The model IDs are the standard defaults (Claude Opus, Sonnet and Haiku 5.5). Your settings are yours
to keep up to date: if your admin announces other models, change the model IDs in your Claude Code
settings and the desktop app's model list, and check one with `bedrock doctor --model <id>`.

</details>

### Web search

Claude Code's own web search doesn't work on Bedrock. If your admin turned on web search,
`bedrock claude` also runs one test search and prints an MCP server to add to Claude Code. Without
it, there is no web search section in the output.

1. Install [uv](https://docs.astral.sh/uv/getting-started/installation/): Claude Code starts the
   server with `uvx`.
2. Run the `claude mcp add-json web-search ... --scope user` command that `bedrock claude` prints.
   `--scope user` makes it work in every project. If you start Claude Code for work with
   `CLAUDE_CONFIG_DIR=~/.claude-work`, put that in front of the command too.
3. Add this line to `~/.claude/CLAUDE.md` (or `~/.claude-work/CLAUDE.md`), because Claude sometimes
   uses WebFetch on a guessed URL instead of searching:
   `For web searches, use the web-search MCP tool. Use WebFetch only to read specific URLs.`
4. In Claude Code, `/mcp` shows `web-search` as connected. Ask something that needs a search.
   On the first start `uvx` downloads the proxy, which can take longer than Claude Code waits: if
   `web-search` shows as pending or failed, restart Claude Code, or start it once with
   `MCP_TIMEOUT=60000`.

- Each search costs about $0.007, paid by the account: it doesn't count against your monthly
  budget. When you're paused, search stops too.
- The server is AWS's `mcp-proxy-for-aws`, signing each call with the `bedrock` profile. Searches go
  to AWS only.
- When you share search results, keep the source links Claude shows: the search service's terms ask
  for it.
- For the Claude desktop app in Amazon Bedrock mode, add it under Configure third-party inference,
  Connectors, Managed MCP servers, as a local (stdio) server with the command and arguments
  `bedrock claude` prints. Not Streamable HTTP: the gateway needs AWS-signed requests, which only
  the local proxy makes. Then Sign in & test, save, and quit and reopen the app.
- `bedrock doctor` checks that `uvx` is installed and that you may use the gateway (no search, free).
  `bedrock claude --no-search-test` skips the test search.

<details>
<summary>Sample output</summary>

```text
Testing web search (one search through your personal role)...
ok    web search: answered

...

Web search (enabled by your admin). Claude Code's own WebSearch tool doesn't work on Bedrock; this
MCP server searches through your personal role instead. It needs uv. Searches are paid by the
account, not counted against your monthly budget.

Add it for every project (with CLAUDE_CONFIG_DIR=~/.claude-work in front if you use that):

  claude mcp add-json web-search '{"args":["mcp-proxy-for-aws@1.7.0","https://bedrock-web-search-abc123.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp","--service","bedrock-agentcore","--profile","bedrock","--region","us-east-1"],"command":"uvx"}' --scope user

Claude sometimes uses WebFetch on a guessed URL instead of searching. To steer it, add this line
to ~/.claude/CLAUDE.md:

  For web searches, use the web-search MCP tool. Use WebFetch only to read specific URLs.

...

Claude desktop app (Amazon Bedrock mode): Configure third-party inference, Connectors, Managed MCP
servers, Add, a local (stdio) server. Not Streamable HTTP: the gateway needs AWS-signed requests,
which only the local proxy makes.

  Name:     web-search
  Command:  /opt/homebrew/bin/uvx
  Args:     (one per line)
      mcp-proxy-for-aws@1.7.0
      https://bedrock-web-search-abc123.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp
      --service
      bedrock-agentcore
      --profile
      bedrock
      --region
      us-east-1
  OAuth:    None; no headers
```

</details>

## Check your spend

```text
$ bedrock usage
Budget     bedrock-bedrock-achen, May 2026 (resets Jun 1)
Spent      $42.50 of $100.00 (42%)
Forecast   $90.00 by the end of the month
Pauses at  $100.00 (100%)
Access     active
As of      May 14 06:30 UTC (AWS refreshes budgets a few times a day)

Web search is not counted here: the account pays for it.
```

It reads only your own budget and your own role, with the `bedrock` profile; `--json` prints the same
as JSON. The spend lags: AWS refreshes it a few times a day, so the last hours aren't in it yet.
If it says your role cannot read its budget, your role predates `bedrock usage`: ask your admin to run
`bedrock-admin apply`.

## If something goes wrong

Run `bedrock doctor`. It stops at the first failed setup check (the model checks all run) and prints
the fix under it:

| Failed check | Message | Fix |
| --- | --- | --- |
| `profile` | `no profile bedrock in <file>` | `bedrock setup` |
| `profile` | `missing role_arn, ...` | `bedrock setup --yes` |
| `profile` | `<key> has a comment at the end of the line` | Put comments on their own line, or run `bedrock setup --yes` |
| `SSO sign-in` | Your SSO session expired or you are not signed in | `aws sso login --profile <profile>` (or `aws login`) |
| `SSO sign-in` | `signed in as ..., not with SSO` | Set `source_profile` to your SSO profile, or run `bedrock setup --sso-profile <profile>` |
| `session name` | `role_session_name is ..., but it must be exactly your SSO email ...` | `bedrock setup --yes` |
| `permission-set access` | `cannot use <role ARN>: ... AccessDenied` | Ask your admin to onboard you with the address shown under `SSO sign-in` (not an alias), and check `role_arn` if they gave you one. Or ask them to let your SSO permission set use the `bedrock-users` roles. |
| `personal role` | The profile can't get credentials | `bedrock setup --yes` |
| `model <id>` | `AccessDeniedException ... explicit deny in an identity-based policy` (or no message), on a Claude model | Your Bedrock access is paused (monthly budget used up). It comes back on the 1st, or ask your admin to unpause you. See [When you're paused](#when-youre-paused). |
| `model <id>` | `AccessDeniedException ... no identity-based policy allows` | Your admin doesn't allow this model. If the model starts with `global.`, your profile's region isn't a US or EU one: when your admin allows `us.` or `eu.` models, run `bedrock setup --region us-east-1 --yes` (or an EU region). |
| `model <id>` | `AccessDeniedException ... service control policy` | Your organization blocks the call: ask your admin |
| `model <id>` | `AccessDeniedException`, on a non-Claude model | Your role may only call Anthropic Claude models (not Fable), and only the ones your admin allows. Ask your admin if you need this one. |
| `model <id>` | `ValidationException` or not found | The model is not offered in your region. Use another model, or ask your admin for the right region. |
| `uvx` | Not on your PATH (only when web search is on) | Install [uv](https://docs.astral.sh/uv/getting-started/installation/) |
| `web search` | `gateway answered 403` | You're paused, or your role predates web search: ask your admin |

`bedrock setup` itself can stop with:

| Message | Fix |
| --- | --- |
| `no SSO profile in <file>` | Run `aws configure sso` (or `aws login`) first, or pass `--sso-profile` |
| `several SSO profiles in <file> (...)` | Pass `--sso-profile <profile>` (it asks when run in a terminal) |
| `profile <name> has no region` | Pass `--region`. Your admin knows the Bedrock region. |
| `reading your SSO identity ...` | Sign in first, with the command it prints |
| `the profile exists: run again with --yes to replace it` | Run `bedrock setup --yes` |

After your admin removes you, `doctor` stops at your role:

```text
$ bedrock doctor
ok    profile: bedrock in /Users/achen/.aws/config
ok    SSO sign-in: achen@example.com (profile my-sso)
FAIL  permission-set access: cannot use arn:aws:iam::111122223333:role/bedrock-users/bedrock/bedrock-user-bedrock-achen: AccessDenied: User: arn:aws:sts::111122223333:assumed-role/AWSReservedSSO_Dev_abc123/achen@example.com is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::111122223333:role/bedrock-users/bedrock/bedrock-user-bedrock-achen
      fix: ask your admin to onboard achen@example.com (and check role_arn, if they gave you one), or to let your SSO permission set use the bedrock-users roles
```

In Claude Code, if it uses the wrong identity or account, run `/login` again, pick the `bedrock`
profile and check with `/status`.

## When you're paused

When you reach your limit, AWS Budgets attaches a deny policy to your personal role. Bedrock calls
fail with `AccessDeniedException`, and you get an email from AWS Budgets. `bedrock doctor` reports
it on the model checks:

```text
$ bedrock doctor
ok    profile: bedrock in /Users/achen/.aws/config
ok    SSO sign-in: achen@example.com (profile my-sso)
ok    permission-set access: may use arn:aws:iam::111122223333:role/bedrock-users/bedrock/bedrock-user-bedrock-achen
ok    personal role: arn:aws:sts::111122223333:assumed-role/bedrock-user-bedrock-achen/achen@example.com
FAIL  model us.anthropic.claude-opus-5-5: AccessDeniedException
      fix: your Bedrock access is paused (monthly budget used up, or paused by your admin): ask your admin, or wait for the 1st
FAIL  model us.anthropic.claude-sonnet-5-5: AccessDeniedException
      fix: your Bedrock access is paused (monthly budget used up, or paused by your admin): ask your admin, or wait for the 1st
FAIL  model us.anthropic.claude-haiku-5-5: AccessDeniedException
      fix: your Bedrock access is paused (monthly budget used up, or paused by your admin): ask your admin, or wait for the 1st
```

`bedrock usage` tells you why: `PAUSED: monthly budget used up`, or `PAUSED by your admin` with
their reason.

The pause can come a few hours after you cross the limit, because billing runs behind.

- Your access comes back by itself on the 1st of next month.
- If you need it sooner, ask your admin. They can raise your limit, or unpause you for the rest of
  the month. Access returns within about a minute.
- Your admin can also pause you by hand, for example when a script runs away. That pause stays until
  they remove it, also past the 1st.
- Don't switch to another profile to keep working. That spend isn't counted against your budget,
  shows up in your admin's report, and may be blocked.
