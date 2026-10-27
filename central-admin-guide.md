# Amazon Bedrock: central admin guide

For the one person who follows many Bedrock setups (several teams, accounts or ids) and wants to see
how far each one is: how many users are onboarded, have set up, are paused, and what they spent.
Admins of a single setup don't need this guide; see the [admin guide](admin-guide.md).

## Contents

- [How it works](#how-it-works)
- [What you need](#what-you-need)
- [1. Create the report bucket](#1-create-the-report-bucket)
- [2. Ask the teams to report](#2-ask-the-teams-to-report)
- [3. Read the reports](#3-read-the-reports)
- [What a report holds](#what-a-report-holds)
- [Who can do what](#who-can-do-what)
- [Add or remove accounts](#add-or-remove-accounts)
- [Stop collecting](#stop-collecting)
- [Cheat sheet](#cheat-sheet)

## How it works

You own one S3 bucket. Each setup whose `bedrock.yaml` has a `report:` section writes a short JSON
report to it, under its own account ID, with the setup admin's or its Lambda's own credentials:

- on every `bedrock-admin apply` that had no failures;
- daily at 07:00 UTC, from the setup's monthly Lambda (schedule `bedrock-daily-<id>`), with the
  setup's daily usage per product (`<account>/<id>/usage/YYYY-MM.json`, from v1.2.0);
- once more on `bedrock-admin uninstall`, marked `uninstalled`.

A setup can write only its own account's reports and can't read any. You need no access to the
teams' accounts. No shared bucket, organization or vendor is built in: the bucket is in your
account, and you choose who may write to it.

## What you need

- `bedrock-admin` (the same binary the teams use). `fleet` commands need no `bedrock.yaml`; if you
  are also a team admin, `-f bedrock.yaml` lends them the file's `profile:`, and its `region:` when
  the profile has none.
- A profile of the account that should hold the bucket (`--profile`, or `AWS_PROFILE`, or the
  default profile, as for every `bedrock-admin` command), with rights to create and configure an S3
  bucket (`s3:CreateBucket`, `s3:PutBucketPolicy`, `s3:PutBucketVersioning`,
  `s3:PutEncryptionConfiguration`, `s3:PutBucketOwnershipControls`, `s3:PutBucketPublicAccessBlock`,
  `s3:PutBucketTagging` and the matching reads), to list and read objects, and
  `s3:ListAllMyBuckets` to find the bucket again without `--bucket`.
- Who may report: your AWS organization (found with `organizations:DescribeOrganization`, or pass
  `--org-id o-...`), or a list of account IDs (`--accounts`).

## 1. Create the report bucket

```bash
bedrock-admin fleet init
```

It creates `bedrock-reports-<account ID>` in the profile's region (else the `-f` file's).
`--bucket NAME` picks another name (S3 bucket names are global, so `fleet init` stops if the name is
taken), and `--region` another region. After that, every `fleet` command finds the bucket by itself.

It shows what it will do and asks first (`--yes` skips the question, `--dry-run` only shows):

```text
Central report bucket s3://bedrock-reports-999999999999 in us-east-1 (account 999999999999), written by accounts of organization o-abc123def4

Account setup
+ bucket bedrock-reports-999999999999       create in us-east-1
+ settings of bedrock-reports-999999999999  block public access, bucket owner owns objects, versioning, SSE-S3, tag bedrock-admin:report-bucket
+ policy of bedrock-reports-999999999999    set

Plan: 3 to create.
Make these changes? (y/N): y
...

Admins of the setups add this to their bedrock.yaml, then run bedrock-admin apply:

  report:
    bucket: bedrock-reports-999999999999
    region: us-east-1

(add include_emails: true under it to also send users' emails)
```

<details>
<summary>Details</summary>

- The bucket: public access blocked, objects owned by the bucket owner (ACLs off), versioning on,
  default encryption SSE-S3, and the tag `bedrock-admin:report-bucket=true` (other tags are kept). Versioning keeps every report; nothing expires. Delete old versions
  yourself if you want to.
- The bucket policy allows `s3:PutObject` on `<bucket>/${aws:PrincipalAccount}/*`, to principals of
  the organization (`aws:PrincipalOrgID`) or of the listed accounts, and denies any request without
  TLS. Nothing else is allowed to other accounts.
- An existing bucket's own region is always used; `--region` matters only when creating one.
- With an existing bucket, `fleet init` checks it is in your account, and fixes settings, the tag
  or the policy changed by hand. Run it again any time; with nothing to fix it shows `No changes.`
- It refuses a setup's own cost export bucket (tagged `bedrock-admin:id`): keep reports apart.
- `--org-id` and `--accounts` can't be combined.

</details>

## 2. Ask the teams to report

Send each team's admin the `report:` section `fleet init` printed. They add it to `bedrock.yaml` (or
pass `--report-bucket` and `--report-region` to `bedrock-admin configure`) and run `bedrock-admin apply`:

```yaml
report:
  bucket: bedrock-reports-999999999999
  region: us-east-1        # the bucket's region
  include_emails: false    # true also sends each user's email, product, set up and paused
```

`apply` sends the first report at once and creates the daily schedule. Ask for
`include_emails: true` only if you need to see who, not just how many; it's the team's choice.

## 3. Read the reports

```bash
bedrock-admin fleet report
```

Without `--bucket`, `fleet report` (and a later `fleet init`) finds the bucket `fleet init` tagged
in the profile's account, in its own region, and prints `Using report bucket
bedrock-reports-999999999999 in us-east-1.` With several tagged buckets it lists them and asks for
`--bucket`.

```text
ACCOUNT       ID       REGION     VERSION  PRODUCTS             ONBOARDED  SET UP  PAUSED  ACTIVE  SPEND (USD)  MONTH    REPORTED  STATUS
111111111111  bedrock  us-east-1  v1.2.0   harvester, longhorn  25         10      0       8/25    412.30       2026-10  3h ago    active
222222222222  bedrock  eu-west-1  v1.2.0   rancher              12         3       1       2/12    57.10        2026-10  5h ago    active

PRODUCT    SETUPS  ONBOARDED  SET UP  PAUSED  SPEND (USD)
longhorn   1       22         8       0       380.00
rancher    1       12         3       1       57.10
harvester  1       3          2       0       32.30

2 installed setups: 37 onboarded, 13 set up, 1 paused; $469.40 this month.
ACTIVE: users who spent at least $0.01 this month, of those onboarded; - when the setup sends no daily usage yet.
```

One line per setup (account and id), its latest report. `PRODUCTS` lists the setup's `product:`
first, then the other products its users have (a user's own `product:` overrides the setup's). The
`PRODUCT` table and the totals count `active` setups only, and spend only of reports from this
month. A report from a version before v1.2.0 has no products and shows `-` until the team applies
with a newer one. `--json` prints the full reports, with emails where sent.
Objects that aren't valid reports, or whose content doesn't match their key, are listed as problems.

A `REPORTED` older than a day means the setup's Lambda didn't run or couldn't write: ask the team
to run `bedrock-admin doctor` and `apply`.

### Each team's active usage

`--by product` sums the setups' daily usage per product: how many of each team's users were active
(spent at least $0.01) this month and their tracked spend. `--by day` shows the fleet per day. Both
take `--product` to keep one team, and `--month YYYY-MM` for an earlier month:

```text
$ bedrock-admin fleet report --by product
Fleet usage in October 2026 (2 setups, cost export as of Oct 21 05:00 UTC)
PRODUCT    SETUPS  ACTIVE  SPEND (USD)
longhorn        1    6/22       380.00  ████████████████████████
rancher         1    2/12        57.10  ███▋
harvester       1     2/3        32.30  ██
total           2   10/37       469.40
ACTIVE: users who spent at least $0.01 in the month, of those onboarded. SPEND is tracked spend of personal roles.

$ bedrock-admin fleet report --by day --product longhorn
Fleet usage in October 2026, product longhorn (2 setups, cost export as of Oct 21 05:00 UTC)
DATE        ACTIVE  SPEND (USD)
2026-10-01       3        12.40   ███████▊
...
2026-10-20       5        38.10*  ████████████████████████
2026-10-21       0         0.00*
ACTIVE: users who spent at least $0.01 that day. SPEND is tracked spend of personal roles.
* still being billed: AWS adds spend up to a day late.
```

The numbers come from the setups' cost exports, not their budgets, so they can differ a little from
`SPEND` in the setup table. Setups that send no daily usage (before v1.2.0, or not applied since)
are listed below the table; uninstalled setups count for the months they sent. `--json` prints the
same.

## What a report holds

`s3://<bucket>/<account>/<id>.json`, overwritten each time (versioning keeps the old ones):

| Field | |
| --- | --- |
| `account`, `id`, `region`, `version` | The setup and the `bedrock-admin` version that applied it |
| `product` | The setup's default `product:` |
| `status`, `source`, `reported_at` | `active` or `uninstalled`; sent by `apply`, `lambda` or `uninstall`; when |
| `defaults` | Default `limit_usd`, `pause_at_percent`, `alert_at_percent` |
| `models`, `web_search` | The setup's `models:` (empty: any Claude model) and whether web search is on |
| `users` | Counts: `onboarded`, `set_up`, `paused`, `iam_users` |
| `month`, `spend_usd` | This month's actual spend, summed over the users' budgets (lags up to a day) |
| `products` | Per product (each user's, else the setup's): `product`, `users` counts, `spend_usd` |
| `people` | Only with `include_emails: true`: each user's `email`, `kind`, `product`, `set_up`, `paused` |

The daily usage, `s3://<bucket>/<account>/<id>/usage/YYYY-MM.json`, rewritten each day: `month`,
`as_of` (the newest cost export hour), `days` (each day's `active_users`, `tracked_usd` and the same
per product) and `products` (each product's `active_users`, `onboarded` and `tracked_usd` in the
month). It holds no emails.

"Set up" means the user's personal role has been used (`bedrock setup` uses it); AWS updates this
with up to about 4 hours of delay. It's the same figure the team sees in `bedrock-admin users`.

## Who can do what

| Who | Can |
| --- | --- |
| You (the bucket's account) | Everything: read, delete, change the policy |
| A setup's admin or Lambda | Write `<their account>/*.json` only. Not read, not list, not another account's prefix |
| Anyone else | Nothing |

A report never holds credentials, prompts or model output. A failed report is only a warning for
the team: it never stops their `apply`, `uninstall` or monthly Lambda.

## Add or remove accounts

With `--org-id` (or the default), new accounts of the organization can report without any change.
With `--accounts`, run `fleet init --accounts` again with the full new list; it updates the policy.

A setup that stops reporting keeps its last report in the bucket. Delete
`<account>/<id>.json` (and its versions) yourself to drop it from `fleet report`.

## Stop collecting

Ask the teams to remove `report:` and run `apply` (it removes the daily schedule). Then empty and delete the bucket in the S3 console or with the AWS CLI;
`fleet` doesn't delete it.

## Cheat sheet

| Task | Command |
| --- | --- |
| Create the bucket | `bedrock-admin fleet init` (`--bucket NAME`, `--region REGION` to choose) |
| Check or fix it | `bedrock-admin fleet init` |
| Only some accounts may report | `bedrock-admin fleet init --accounts 111111111111,222222222222` |
| See what `fleet init` would change | `bedrock-admin fleet init --dry-run` |
| Read every setup's latest report | `bedrock-admin fleet report` |
| Full reports as JSON | `bedrock-admin fleet report --json` |
| Each team's active users and spend | `bedrock-admin fleet report --by product` |
| The fleet, or one team, per day | `bedrock-admin fleet report --by day` (`--product NAME`) |
| An earlier month | add `--month YYYY-MM` to `--by product` or `--by day` |
| Several report buckets in the account | Add `--bucket NAME` to any of these |
| Use your team setup's profile and region | Add `-f bedrock.yaml` to any of these |
