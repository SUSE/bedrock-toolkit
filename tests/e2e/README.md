# End-to-end tests

Robot Framework suites that run the real `bedrock-admin` and `bedrock` binaries (built for Linux)
against a real AWS account, in a container. They cover only what needs real binaries and real
AWS. Config validation, flags, the `plan` diff, the Parquet reader and report formatting are
covered by the Go unit tests (`go test ./...`).

The suites are run by hand, not in CI. Run the fast suites before merging a change that touches AWS
behavior, and `user-access` and `billing` before a release.

## Contents

- [Running](#running)
- [Suites](#suites)
- [Safety](#safety)
- [`billing`: phased](#billing-phased)

## Running

You need Docker (or `E2E_RUNTIME=podman`). Go is optional: without it, the CLIs are built in a
`golang` container.

```bash
cd tests/e2e
./run.sh cli                 # no AWS: smoke test of the built binaries
cp config/example.py config/$USER.py   # then fill it in
aws sso login --profile <ADMIN_PROFILE>
./run.sh account             # or users, user-access, iam-user, usage
./run.sh fast                # every suite but billing and web-search
./run.sh web-search          # opt-in: REGION us-east-1, eu-west-1 or ap-northeast-1
./run.sh users --test "Pause*"     # any robot option works after the suite
./run.sh shell               # a shell in the container
```

Reports go to `results/<suite>-<UTC time>/report.html`, and `results/latest` points to the newest.
`~/.aws` is mounted, so the container shares your SSO sign-in. The user CLI writes to a copy of your AWS config in the run's
directory, never to `~/.aws/config`.

Every suite except `cli` applies its own setup, with id `e2e<run id>` (6 hex digits), next to any
other setup in the account: its own deny policy, budget actions role, monthly Lambda and schedule,
cost export and bucket `bedrock-cur-<account>-<id>`, and personal roles `bedrock-user-<id>-<name>`.
At the end it removes its users and uninstalls the setup with `--delete-data`. `KEEP_SETUP = True`
leaves it in place to look at after a failure. A run stops at its start when apply shows a conflict.

## Suites

| Suite | Needs | Covers |
| --- | --- | --- |
| `cli` | nothing | The built binaries run. `configure` writes a file that `plan` loads, and plan then stops at the missing credentials. |
| `account` | admin | `apply` creates Steps 1.1–1.7 and a second `apply` changes nothing. `plan` names exactly the missing or drifted piece and `apply` fixes it. Step 1.7 handoff. `doctor`. `models` shows regions only for allowed models. `export` round trip. Central reporting: `fleet init` creates the report bucket (with `--accounts`, so no Organizations permission is needed), finds it again by its tag without `--bucket` (run with `-f`, so the region comes from the run's file), and refuses the cost export bucket; `apply`, the Lambda's daily run and `uninstall` write the report, the daily run also the setup's usage, `fleet report` (and `--by product`) shows them, and removing `report:` keeps the daily schedule. A personal role without the id tag is left alone. A second setup (another id) in the same account: its own resources, the same person a user of both, the Lambda and `uninstall` touching only their own id, `export` asking for `--id`. `uninstall` refuses while users remain, keeps the bucket, and `--delete-data` removes it. The report bucket is deleted at the end. |
| `users` | admin | Onboarding (role, budget, alerts, pause action), changes in place, and drift. A user's own `product:` retags only their role. Removing a user needs `--yes`, and `--no-delete` skips it. Pause and unpause by hand. `users` shows pause states and "never" for roles nobody used. `apply` and the monthly Lambda leave a manual pause in place. Off for the month. The Lambda re-arms users and saves the snapshot, with each role's product tag. |
| `user-access` | admin, SSO | `bedrock setup`, calls through the personal role, a baseline model `models:` doesn't list allowed, an unlisted model denied (`models:` allow-list), `bedrock doctor`, `bedrock usage` (with the daily usage file the Lambda writes), `bedrock models`, direct SSO calls denied (Step 1.7), and a manual pause seen from the user's side. |
| `iam-user` | admin | A user with an IAM user's access key instead of SSO: apply tags the IAM user `owner`, `product` and `bedrock-admin:id`, attaches the Step 1.9 policy and adds it to the pause action; `bedrock setup --source-profile` reads the email from the tag and writes a working profile; direct calls with the key denied, and with `block_direct_calls` on still denied when the key has its own Bedrock Allow (`AmazonBedrockFullAccess`); a pause denies the key and the personal role, despite that Allow, and unpause lifts it; removing `iam_user` releases the IAM user. The run creates and deletes its own IAM user. |
| `usage` | admin | `usage` on fixture cost export files and a snapshot uploaded to the setup's bucket: totals, tracked and untracked spend, a past month from its snapshot, `--months`, and one person. The Lambda's daily run writes each user's usage file from the fixture, a personal role reads its own file but not another's, and offboarding deletes the user's files. |
| `web-search` | admin; SSO optional | Opt-in. Turning `web_search` on creates the setup's gateway `bedrock-web-search-<id>` and its role, and gives a personal role the gateway rights; the role can search, the pause denies it and unpause restores it. With SSO: `bedrock doctor`'s web search check, and the MCP server `bedrock claude` prints, run through `uvx mcp-proxy-for-aws`, and for a profile in another region, the same gateway with its region. Turning it off removes the rights, the gateway and its role. Needs REGION `us-east-1`, `eu-west-1` or `ap-northeast-1`; a run costs a few cents. |
| `billing` | admin | Everything that waits for AWS billing, in phases (below). |

## Safety

- Each run is its own setup (`id: e2e<run id>`, users `e2e<run id>-*`). `apply`, `uninstall` and
  the setup's monthly Lambda only touch resources of their own id, so a run can't change a real
  setup or its users in the same account. The `usage` suite uses id `usage` (the fixtures' personal
  roles are `bedrock-user-usage-*`), so don't run two `usage` runs at once in one account.
- Org-level settings are shared: the cost allocation tag activation and the Step 1.7 permission set
  policies. The suites only read them.
- Use a dedicated test account anyway if you can.
- Every suite except `cli` creates real AWS resources, and `user-access`, `iam-user` and `billing` make paid
  model calls. A full `billing` run costs about $4.

## `billing`: phased

AWS Budgets updates spend about three times a day, and the cost export lags up to 24 h. So the
suite runs in phases that hand over through `results/billing/<run id>.json`:

```bash
./run.sh billing --include phase1    # any time: 4 users with a $1 limit; a, b, d spend $1.20, c $0.10
./run.sh billing --include phase2    # 12+ h later: a, b, d paused; unpause d (off); b's limit and
                                     # pause_at_percent; the monthly Lambda re-arms a and d
./run.sh billing --include phase3    # 24+ h after phase 1: a and d paused again; usage matches
                                     # the spend; untracked caller listed; cleanup
./run.sh billing --include manual    # after phase 2, with NOTIFY_EMAIL: asks whether the email arrived
```

Too early, or billing not caught up yet: the tests are skipped with "retry after <time>". 48 h after
phase 1 with no result, they fail. Later phases pick the newest unfinished run, and
`--variable RUN_ID:<id>` picks another.

Test users spend through their own personal role. A test-only trust statement lets the admin
profile assume the role with session name `<email>`, so the spend is attributed exactly as for a
real user. Without it, only the runner's own SSO login could assume a role.
