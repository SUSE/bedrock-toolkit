# Robot Framework variable file for the bedrock-admin e2e tests.
# Copy it to config/<you>.py (ignored by git); run.sh passes config/$USER.py, or E2E_CONFIG=<file>.
# The cli suite needs no variable file. Every value is optional unless marked required.

# Admin: an AWS CLI profile with admin rights in the test account (required for every suite but cli).
# Sign in before the run, for example: aws sso login --profile <ADMIN_PROFILE>
ADMIN_PROFILE = "default"

# Bedrock region of the test setup. The web-search suite needs the account setup's region, with web
# search on there (us-east-1, eu-west-1 or ap-northeast-1).
REGION = "us-west-2"

# models: in the test config: the only models the test users may call, which apply enables and the
# suites call. Empty: the Opus, Sonnet and Haiku 5.5 inference profiles for REGION (us., eu. or
# global. prefix). user-access checks that an unlisted model (Claude Haiku 4.5) is denied.
MODELS = []

# The id that owns the account setup (Steps 1.1-1.7) in the test account. The account suite
# creates and checks it under this id; the other suites only need it to exist (any id) and use
# their own id, e2e-<run id>, for their users. Use a dedicated test account: the account suite
# changes shared account resources, and the users and billing suites invoke the monthly Lambda,
# which acts on every user in the account. The library refuses to invoke it unless the Lambda is
# managed by ACCOUNT_ID.
ACCOUNT_ID = "e2e"

# product tag of the test users.
PRODUCT = "bedrock-e2e"

# Cost export bucket. Empty: bedrock-cur-<account id>.
CUR_BUCKET = ""

# Step 1.7 (block direct model calls), method permission-set: the admin permission set, and a test
# permission set an org admin has given the deny statement from block-direct-calls.json (the
# account suite then expects "ok"; empty: it expects "needs-org-admin").
ADMIN_PERMISSION_SET = "BedrockAdmin"
BLOCK_TEST_PERMISSION_SET = ""

# The account suite's uninstall --delete-data test deletes the cost export bucket and its history.
ALLOW_DELETE_DATA = False

# SSO: the runner's own IAM Identity Center profile in the test account, used by user-access (it
# onboards the runner as a test user named <run id>-me). Sign in before the run:
# aws sso login --profile <SSO_PROFILE>
SSO_PROFILE = ""

# billing: a profile that is not an SSO login (for example an IAM user's access key), for the
# untracked-spend check. Empty: ADMIN_PROFILE.
UNTRACKED_PROFILE = ""

# Emails of the test users, e2e-<run id>-<x>@<TEST_EMAIL_DOMAIN>. Budget emails to them bounce;
# set NOTIFY_EMAIL to receive them (the billing "manual" test asks whether one arrived).
TEST_EMAIL_DOMAIN = "example.com"
NOTIFY_EMAIL = ""

# Pick a run id instead of a random one (billing phases 2 and 3 pick the newest unfinished run).
RUN_ID = ""

# False: skip tests that ask a question (for unattended runs).
INTERACTIVE = True
