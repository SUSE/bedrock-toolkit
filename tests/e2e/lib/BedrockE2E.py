"""Robot Framework library for the bedrock-admin e2e tests.

The CLIs under test are run as subprocesses. Every check reads AWS directly with boto3 and never
trusts the CLI's own output. Settings come from the variable file (see config/example.py).

Each run is its own setup: its id, e2e<6 hex>, is the config's id:, so every account-level
resource (bedrock-deny-<id>, bedrock-monthly-unpause-<id>, the bucket bedrock-cur-<account>-<id>, ...)
and every personal role (bedrock-user-<id>-<name>) belongs to the run alone. A suite applies the
setup at the start and uninstalls it, with its bucket, at the end. Test users' emails are
<run id>-<x>@TEST_EMAIL_DOMAIN.
"""
import datetime as dt
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import time
import tomllib
import urllib.error
import urllib.request
from pathlib import Path

import boto3
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.exceptions import ClientError
from robot.api import Failure, SkipExecution, logger
from robot.libraries.BuiltIn import BuiltIn

HERE = Path(__file__).resolve().parent.parent
BIN = Path(os.environ.get("E2E_BIN", HERE / "bin"))
FIXTURES = Path(os.environ.get("E2E_FIXTURES", HERE.parent / "fixtures"))
STATE_DIR = HERE / "results" / "billing"

ID_TAG = "bedrock-admin:id"
OWNER_TAG = "owner"
PAUSED_BY_TAG = "bedrock:paused-by"
PAUSE_REASON_TAG = "bedrock:pause-reason"
WEB_SEARCH_TARGET = "web-search"
WEB_SEARCH_TOOL = "web-search___WebSearch"
WEB_SEARCH_REGIONS = ("us-east-1", "eu-west-1", "ap-northeast-1")
PENDING_OPS = {"create", "update", "delete", "needs-org-admin", "conflict"}

DEFAULTS = {
    "ADMIN_PROFILE": "default",
    "REGION": "us-west-2",
    "MODELS": [],
    "PRODUCT": "bedrock-e2e",
    "ADMIN_PERMISSION_SET": "BedrockAdmin",
    "BLOCK_TEST_PERMISSION_SET": "",
    "KEEP_SETUP": False,
    "SSO_PROFILE": "",
    "UNTRACKED_PROFILE": "",
    "TEST_EMAIL_DOMAIN": "example.com",
    "NOTIFY_EMAIL": "",
    "RUN_ID": "",
    "INTERACTIVE": True,
}

# USD per million input and output tokens, to estimate what a call cost.
PRICES = {"opus-4-5": (5, 25), "opus": (15, 75), "sonnet": (3, 15), "haiku": (1, 5)}


def _bool(v):
    return v if isinstance(v, bool) else str(v).strip().lower() in ("1", "true", "yes", "y")


def _value(v):
    """Turns a Robot argument into a config value: numbers, booleans, lists and null as JSON."""
    if not isinstance(v, str):
        return v
    try:
        return json.loads(v)
    except ValueError:
        return v


def _console(msg):
    sys.__stdout__.write(msg + "\n")
    sys.__stdout__.flush()


def name_from_email(email):
    """config.NameFromEmail: the user name derived from an email."""
    n = re.sub(r"[^a-z0-9.-]+", "-", email.split("@", 1)[0].lower()).strip("-.")
    return n[:30].strip("-.") if len(n) > 30 else n


def setup_names(account, id_):
    """The account-level resource names of setup id_ (internal/policy)."""
    return {
        "deny": f"bedrock-deny-{id_}",
        "budget_actions_role": f"bedrock-budget-actions-{id_}",
        "lambda": f"bedrock-monthly-unpause-{id_}",
        "lambda_role": f"bedrock-monthly-unpause-{id_}",
        "scheduler_role": f"bedrock-monthly-unpause-scheduler-{id_}",
        "schedule": f"bedrock-monthly-unpause-{id_}",
        "web_search_role": f"bedrock-web-search-gateway-{id_}",
        "web_search_gateway": f"bedrock-web-search-{id_}",
        "export": f"bedrock-cur-{id_}",
        "bucket": f"bedrock-cur-{account}-{id_}",
    }


def _geo(region):
    geo = region.split("-", 1)[0]
    return geo if geo in ("us", "eu") else "global"


def default_models(region):
    return [f"{_geo(region)}.anthropic.claude-{m}-5-5" for m in ("opus", "sonnet", "haiku")]


GEO_PREFIXES = ("us.", "eu.", "apac.", "global.", "jp.", "au.", "ca.", "us-gov.")


def baseline_all_geos():
    """Every models.toml entry, whatever its prefix (config.BaselineAllGeos). An ID without a
    prefix gets each."""
    with open(HERE.parent.parent / "models.toml", "rb") as f:
        models = tomllib.load(f).get("models", [])
    every = []
    for m in models:
        every += [m] if m.startswith(GEO_PREFIXES) else [f"{g}.{m}" for g in ("us", "eu", "global")]
    return every


def baseline_models(region):
    """The models.toml entries for REGION (config.BaselineModels): those with its prefix (us./eu.,
    else global.)."""
    return [m for m in baseline_all_geos() if m.startswith(_geo(region) + ".")]


class Result:
    def __init__(self, rc, out, err):
        self.rc, self.stdout, self.stderr = rc, out, err

    @property
    def json(self):
        try:
            return json.loads(self.stdout)
        except ValueError as e:
            raise Failure(f"not JSON: {e}\n{self.stdout}")


class BedrockE2E:
    ROBOT_LIBRARY_SCOPE = "GLOBAL"

    def __init__(self):
        self.s = None
        self.cfg = None
        self.run_id = ""
        self.account = ""
        self.work = None
        self.sso_email = ""
        self.sso_permission_set = ""
        self.user_aws_config = None
        self.offline = False
        self._sessions = {}
        self._user_sessions = {}

    # ---------------------------------------------------------------- settings and run

    def _settings(self):
        if self.s is None:
            bi = BuiltIn()
            self.s = {k: bi.get_variable_value("${%s}" % k, d) for k, d in DEFAULTS.items()}
            self.s["INTERACTIVE"] = _bool(self.s["INTERACTIVE"])
            self.s["KEEP_SETUP"] = _bool(self.s["KEEP_SETUP"])
            if not self.s["MODELS"]:
                self.s["MODELS"] = default_models(self.s["REGION"])
            for k, v in self.s.items():
                bi.set_global_variable("${%s}" % k, v)
        return self.s

    def start_run(self, needs_aws=True):
        """Starts a test run: picks the run id, finds the account and writes the base config.

        Returns the run id. With needs_aws=False (the cli suite) nothing is read from AWS."""
        s = self._settings()
        self.run_id = s["RUN_ID"] or "e2e" + secrets.token_hex(3)
        out = Path(BuiltIn().get_variable_value("${OUTPUT DIR}"))
        self.work = out / "work"
        self.work.mkdir(parents=True, exist_ok=True)
        # The CLIs run in an empty directory, so a ./bedrock.yaml is never picked up by accident.
        self.cwd = self.work / "cwd"
        self.cwd.mkdir(exist_ok=True)
        self.account = "111122223333"
        self.offline = not _bool(needs_aws)
        if not self.offline:
            ident = self._session("admin").client("sts").get_caller_identity()
            self.account = ident["Account"]
            logger.info(f"Admin: {ident['Arn']}", also_console=True)
        self.cfg = {
            "id": self.run_id,
            "account": self.account,
            "region": s["REGION"],
            "product": s["PRODUCT"],
            "models": list(s["MODELS"]),
            "defaults": {"limit_usd": 1, "notify": s["NOTIFY_EMAIL"] or "owner"},
            "block_direct_calls": {"enabled": False},
            "users": [],
        }
        logger.info(f"Run id: {self.run_id}", also_console=True)
        BuiltIn().set_global_variable("${RUN}", self.run_id)
        BuiltIn().set_global_variable("${ACCOUNT}", self.account)
        BuiltIn().set_global_variable("${PRODUCT}", self.cfg["product"])
        self._publish_id()
        return self.run_id

    def _publish_id(self):
        BuiltIn().set_global_variable("${ID}", self.cfg["id"])
        BuiltIn().set_global_variable("${N}", setup_names(self.account, self.cfg["id"]))

    def use_id(self, id_):
        """Makes the run's setup use id_ instead of the run id (the usage suite: the fixtures' id)."""
        self.cfg["id"] = id_
        self._publish_id()
        return id_

    def enable_block_direct_calls(self):
        """Step 1.7 on, method permission-set, as a real setup has it."""
        self.cfg["block_direct_calls"] = {"enabled": True, "method": "permission-set",
                                          "admin_permission_set": self._settings()["ADMIN_PERMISSION_SET"]}

    def names(self, id_=None):
        """The account-level resource names of setup id_ (default: the run's)."""
        return setup_names(self.account, id_ or self.cfg["id"])

    @property
    def bucket(self):
        return self.names()["bucket"]

    # ---------------------------------------------------------------- the config file

    def keep_setup(self):
        """KEEP_SETUP from the variable file."""
        return self._settings()["KEEP_SETUP"]

    def user_email(self, x):
        """Email of test user x: <run id>-x@TEST_EMAIL_DOMAIN; "me" is the runner's SSO email."""
        if x == "me":
            if not self.sso_email:
                raise Failure("Start SSO first")
            return self.sso_email
        if "@" in x:
            return x
        return f"{self.run_id}-{x}@{self._settings()['TEST_EMAIL_DOMAIN']}"

    def user_name(self, x):
        """Name of test user x: its name: in the config, else the one derived from its email."""
        for u in self.cfg["users"]:
            if u["email"] == self.user_email(x) and u.get("name"):
                return u["name"]
        return name_from_email(self.user_email(x))

    def role_of(self, x, id_=None):
        return f"bedrock-user-{id_ or self.cfg['id']}-{self.user_name(x)}"

    def budget_of(self, x, id_=None):
        return f"bedrock-{id_ or self.cfg['id']}-{self.user_name(x)}"

    def role_arn_of(self, x, id_=None):
        id_ = id_ or self.cfg["id"]
        return f"arn:aws:iam::{self.account}:role/bedrock-users/{id_}/{self.role_of(x, id_)}"

    def set_users(self, *xs):
        self.cfg["users"] = []
        for x in xs:
            self.add_user(x)

    def add_user(self, x, **options):
        """Adds test user x ("me": the runner's SSO email; or a full email) with options."""
        u = {"email": self.user_email(x)}
        u.update({k: _value(v) for k, v in options.items()})
        self.cfg["users"] = [v for v in self.cfg["users"] if v["email"] != u["email"]] + [u]

    def remove_user(self, x):
        self.cfg["users"] = [u for u in self.cfg["users"] if u["email"] != self.user_email(x)]

    def set_user(self, x, key, value):
        for u in self.cfg["users"]:
            if u["email"] == self.user_email(x):
                u[key] = _value(value)
                return
        raise Failure(f"no user {x}")

    def unset_user(self, x, key):
        for u in self.cfg["users"]:
            if u["email"] == self.user_email(x):
                u.pop(key, None)

    def set_config(self, path, value):
        """Sets a config value by dotted path, for example defaults.pause_at_percent."""
        d = self.cfg
        *parents, last = path.split(".")
        for p in parents:
            d = d.setdefault(p, {})
        d[last] = _value(value)

    def unset_config(self, path):
        d = self.cfg
        *parents, last = path.split(".")
        for p in parents:
            d = d.get(p, {})
        d.pop(last, None)

    def write_config(self, name="bedrock.yaml", cfg=None):
        """Writes the run's config (JSON is valid YAML) and returns its path."""
        path = self.work / name
        path.write_text(json.dumps(cfg or self.cfg, indent=2) + "\n")
        return str(path)

    def write_raw_config(self, text, name="raw.yaml"):
        path = self.work / name
        path.write_text(text)
        return str(path)

    def write_config_with_id(self, id_, *xs, name=None):
        """Writes a copy of the config with another id (a second setup in the account) and users xs,
        Step 1.7 off; returns its path."""
        cfg = json.loads(json.dumps(self.cfg))
        cfg["id"] = id_
        cfg["block_direct_calls"] = {"enabled": False}
        cfg["users"] = [{"email": self.user_email(x)} for x in xs]
        return self.write_config(name or f"{id_}.yaml", cfg)

    # ---------------------------------------------------------------- running the CLIs

    def _exec(self, cmd, env, stdin, label):
        logger.info("$ " + " ".join(cmd))
        start = time.time()
        p = subprocess.run(cmd, input=stdin, capture_output=True, text=True, env=env, cwd=self.cwd, timeout=1800)
        logger.info(f"exit {p.returncode} after {time.time() - start:.0f}s\n--- stdout\n{p.stdout}\n--- stderr\n{p.stderr}")
        _console(f"    {label} → exit {p.returncode} ({time.time() - start:.0f}s)")
        return Result(p.returncode, p.stdout, p.stderr)

    def _env(self):
        """The CLIs' environment. Offline (the cli suite) it has no AWS credentials at all."""
        env = dict(os.environ)
        if self.offline:
            for k in [k for k in env if k.startswith("AWS_")]:
                del env[k]
            empty = str(self.work / "empty")
            Path(empty).touch()
            env.update({"AWS_CONFIG_FILE": empty, "AWS_SHARED_CREDENTIALS_FILE": empty,
                        "AWS_EC2_METADATA_DISABLED": "true", "HOME": str(self.cwd)})
        return env

    @staticmethod
    def _only_handoffs_pending(stdout):
        """True when a plan/apply --json exit 2 is caused only by org admin handoffs (a linked account)."""
        try:
            d = json.loads(stdout)
        except ValueError:
            # Text output: a "Pending:" section of "  <label>" lines, each followed by "    <reason>".
            lines = stdout.splitlines()
            if "Pending:" not in lines:
                return False
            reasons = []
            for l in lines[lines.index("Pending:") + 1:]:
                if not l.strip() or not l.startswith("  "):
                    break
                if l.startswith("    "):
                    reasons.append(l)
            return bool(reasons) and all("needs org admin" in l for l in reasons)
        pending = d.get("pending") or [i for i in d.get("items", []) if i.get("op") in PENDING_OPS]
        return bool(pending) and all(i.get("op") == "needs-org-admin" for i in pending)

    @staticmethod
    def _check_rc(r, rc, cmd):
        if str(rc) != "any" and r.rc != int(rc):
            raise Failure(f"{cmd}: exit {r.rc}, expected {rc}\n{r.stdout}\n{r.stderr}")

    def admin(self, *args, rc=0, config="", stdin="", output="stdout"):
        """Runs bedrock-admin -f <run config> args. Checks the exit code (rc=any to skip).

        config=none runs it without -f. Returns stdout, parsed JSON when --json is given, or the
        whole result (output=result)."""
        env = self._env()
        if not self.offline:
            env["AWS_PROFILE"] = self._settings()["ADMIN_PROFILE"]
        env.pop("BEDROCK_ADMIN_CONFIG", None)
        cmd = [str(BIN / "bedrock-admin")]
        if config is not None and str(config).lower() != "none":
            cmd += ["-f", config or self.write_config()]
        cmd += [str(a) for a in args]
        r = self._exec(cmd, env, stdin, "bedrock-admin " + " ".join(str(a) for a in args))
        if str(rc) == "0" and r.rc == 2 and self._only_handoffs_pending(r.stdout):
            r = Result(0, r.stdout, r.stderr)
        self._check_rc(r, rc, "bedrock-admin " + " ".join(str(a) for a in args))
        if output == "result":
            return r
        if "--json" in args and output == "stdout":
            try:
                return json.loads(r.stdout)
            except ValueError as e:
                raise Failure(f"not JSON: {e}\n{r.stdout}")
        return r.stdout

    def bedrock(self, *args, rc=0, stdin="", output="stdout"):
        """Runs the user CLI with the run's own AWS config file (see Start SSO)."""
        env = self._env()
        env.pop("AWS_PROFILE", None)
        if self.user_aws_config:
            env["AWS_CONFIG_FILE"] = str(self.user_aws_config)
        r = self._exec([str(BIN / "bedrock")] + [str(a) for a in args], env, stdin, "bedrock " + " ".join(args))
        self._check_rc(r, rc, "bedrock " + " ".join(args))
        if output == "result":
            return r
        if "--json" in args:
            return json.loads(r.stdout)
        return r.stdout

    # ---------------------------------------------------------------- plan --json

    @staticmethod
    def plan_item(plan, target, section=None):
        """The plan item whose target is target (or, failing that, contains it)."""
        items = [i for i in plan["items"] if section is None or i["section"] == section]
        for i in items:
            if i["target"] == target:
                return i
        found = [i for i in items if target in i["target"]]
        if len(found) == 1:
            return found[0]
        raise Failure(f"no single plan item {target!r} in: {[i['target'] for i in items]}")

    def plan_op_should_be(self, plan, target, op):
        i = self.plan_item(plan, target)
        if i["op"] != op:
            raise Failure(f"{target}: op {i['op']} ({i.get('status')}, {i.get('details')}), expected {op}")
        return i

    @staticmethod
    def pending_targets(plan):
        return [i["target"] for i in plan["items"] if i["op"] in PENDING_OPS]

    @staticmethod
    def changes_but_handoffs(plan):
        """Pending targets other than the org admin handoffs (Step 1.7)."""
        return [i["target"] for i in plan["items"] if i["op"] in PENDING_OPS and i["op"] != "needs-org-admin"]

    def plan_should_have_no_changes(self, plan):
        p = self.changes_but_handoffs(plan)
        if p:
            raise Failure(f"plan has changes: {p}")

    def only_pending_should_be(self, plan, *targets):
        p = self.pending_targets(plan)
        bad = [t for t in p if not any(x == t or x in t for x in targets)]
        missing = [x for x in targets if not any(x == t or x in t for t in p)]
        if bad or missing:
            raise Failure(f"pending {p}, expected exactly {list(targets)}")

    # ---------------------------------------------------------------- AWS checks

    def _session(self, which):
        s = self._settings()
        profile = {"admin": s["ADMIN_PROFILE"], "sso": s["SSO_PROFILE"],
                   "untracked": s["UNTRACKED_PROFILE"] or s["ADMIN_PROFILE"]}.get(which, which)
        if which not in self._sessions:
            self._sessions[which] = boto3.Session(profile_name=profile or None, region_name=s["REGION"])
        return self._sessions[which]

    def _user_session(self, x):
        """A session in test user x's personal role, assumed from the admin profile with session
        name <email> (needs Let Admin Assume first). Cached for 45 minutes."""
        c = self._user_sessions.get(x)
        if c and c[1] > time.time():
            return c[0]
        email = self.user_email(x)
        arn = self.role_arn_of(x)
        sts = self._session("admin").client("sts")
        for attempt in range(12):
            try:
                cr = sts.assume_role(RoleArn=arn, RoleSessionName=email, DurationSeconds=3600)["Credentials"]
                break
            except ClientError as e:
                if attempt == 11 or e.response["Error"]["Code"] != "AccessDenied":
                    raise Failure(f"can't assume {arn} as {email}: {e}")
                time.sleep(5)
        sess = boto3.Session(aws_access_key_id=cr["AccessKeyId"], aws_secret_access_key=cr["SecretAccessKey"],
                             aws_session_token=cr["SessionToken"], region_name=self.s["REGION"])
        self._user_sessions[x] = (sess, time.time() + 45 * 60)
        return sess

    def let_admin_assume(self, x):
        """Test only: adds a trust statement so the admin profile can assume test user x's role with
        session name <email>, to spend as x. The role's owner tag still attributes the spend to x.
        The statement goes away with the role at cleanup (apply does not check the trust policy)."""
        arn = self._session("admin").client("sts").get_caller_identity()["Arn"]
        m = re.match(r"^arn:aws:sts::\d{12}:assumed-role/([^/]+)/", arn)
        principal = self._iam().get_role(RoleName=m.group(1))["Role"]["Arn"] if m else arn
        role = self.role_of(x)
        doc = self._iam().get_role(RoleName=role)["Role"]["AssumeRolePolicyDocument"]
        doc["Statement"] = [st for st in doc["Statement"] if st.get("Sid") != "E2EAdminSpends"] + [{
            "Sid": "E2EAdminSpends", "Effect": "Allow", "Principal": {"AWS": principal}, "Action": "sts:AssumeRole",
            "Condition": {"StringEquals": {"sts:RoleSessionName": self.user_email(x)}}}]
        self._iam().update_assume_role_policy(RoleName=role, PolicyDocument=json.dumps(doc))
        return f"user:{x}"

    def _iam(self):
        return self._session("admin").client("iam")

    def _budgets(self):
        return self._session("admin").client("budgets", region_name="us-east-1")

    def _s3(self):
        return self._session("admin").client("s3", region_name=self._bucket_region())

    def _bucket_region(self):
        c = self._session("admin").client("s3")
        loc = c.get_bucket_location(Bucket=self.bucket).get("LocationConstraint")
        return loc or "us-east-1"

    def role_exists(self, role):
        try:
            self._iam().get_role(RoleName=role)
            return True
        except ClientError as e:
            if e.response["Error"]["Code"] == "NoSuchEntity":
                return False
            raise

    def role_should_exist(self, role):
        if not self.role_exists(role):
            raise Failure(f"role {role} does not exist")

    def role_should_not_exist(self, role):
        if self.role_exists(role):
            raise Failure(f"role {role} still exists")

    def role_tags(self, role):
        tags = self._iam().list_role_tags(RoleName=role)["Tags"]
        return {t["Key"]: t["Value"] for t in tags}

    def role_tag_should_be(self, role, key, value):
        got = self.role_tags(role).get(key)
        if got != value:
            raise Failure(f"{role}: tag {key} is {got!r}, expected {value!r}")

    def role_should_not_have_tag(self, role, key):
        got = self.role_tags(role).get(key)
        if got is not None:
            raise Failure(f"{role}: has tag {key}={got!r}")

    def role_should_have_trust_for(self, role, email):
        doc = json.dumps(self._iam().get_role(RoleName=role)["Role"]["AssumeRolePolicyDocument"])
        if email not in doc:
            raise Failure(f"{role}: trust policy does not name {email}: {doc}")

    def _deny_arn(self, id_=None):
        return f"arn:aws:iam::{self.account}:policy/bedrock/{self.names(id_)['deny']}"

    def deny_attached(self, role):
        pols = self._iam().list_attached_role_policies(RoleName=role)["AttachedPolicies"]
        return any(p["PolicyArn"] == self._deny_arn() for p in pols)

    def deny_policy_should_be_attached(self, role):
        if not self.deny_attached(role):
            raise Failure(f"{self.names()['deny']} is not attached to {role}")

    def deny_policy_should_not_be_attached(self, role):
        if self.deny_attached(role):
            raise Failure(f"{self.names()['deny']} is attached to {role}")

    def budget(self, name):
        try:
            return self._budgets().describe_budget(AccountId=self.account, BudgetName=name)["Budget"]
        except ClientError as e:
            if e.response["Error"]["Code"] == "NotFoundException":
                return None
            raise

    def budget_should_not_exist(self, name):
        if self.budget(name) is not None:
            raise Failure(f"budget {name} still exists")

    def budget_limit_should_be(self, name, usd):
        b = self.budget(name)
        if b is None:
            raise Failure(f"no budget {name}")
        got = float(b["BudgetLimit"]["Amount"])
        if abs(got - float(usd)) > 0.001:
            raise Failure(f"{name}: limit {got}, expected {usd}")

    def budget_alerts_should_be(self, name, *percents):
        ns = self._budgets().describe_notifications_for_budget(AccountId=self.account, BudgetName=name)["Notifications"]
        got = sorted(int(n["Threshold"]) for n in ns)
        want = sorted(int(p) for p in percents)
        if got != want:
            raise Failure(f"{name}: alerts at {got}%, expected {want}%")

    def budget_action(self, name):
        acts = self._budgets().describe_budget_actions_for_budget(AccountId=self.account, BudgetName=name)["Actions"]
        if len(acts) != 1:
            raise Failure(f"{name}: {len(acts)} budget actions, expected 1")
        return acts[0]

    def budget_action_should_be(self, name, threshold=None, status=None):
        a = self.budget_action(name)
        got_t = int(a["ActionThreshold"]["ActionThresholdValue"])
        if threshold is not None and got_t != int(threshold):
            raise Failure(f"{name}: action threshold {got_t}%, expected {threshold}%")
        if status is not None and a["Status"] != status:
            raise Failure(f"{name}: action status {a['Status']}, expected {status}")
        return a

    def budget_action_id(self, name):
        return self.budget_action(name)["ActionId"]

    def pause_state(self, x):
        """The user's pause state, read from the role's tags and the budget action (not the CLI)."""
        if PAUSED_BY_TAG in self.role_tags(self.role_of(x)):
            return "paused-by-hand"
        st = self.budget_action(self.budget_of(x))["Status"]
        return {"STANDBY": "armed", "PENDING": "armed", "EXECUTION_SUCCESS": "paused",
                "REVERSE_SUCCESS": "off"}.get(st, st)

    def pause_states(self, *xs):
        return {x: self.pause_state(x) for x in xs}

    def user_row(self, report, x):
        """The users row of test user x in a usage --json report."""
        email = self.user_email(x).lower()
        rows = [r for r in report["users"] if r["email"].lower() == email]
        if not rows:
            raise Failure(f"no usage row for {email}")
        return rows[0]

    def pause_state_should_be(self, x, state):
        got = self.pause_state(x)
        if got != state:
            raise Failure(f"{x}: pause state {got}, expected {state}")

    def set_budget_limit_directly(self, name, usd):
        """Drift: changes a budget's limit outside the file."""
        b = self.budget(name)
        b["BudgetLimit"]["Amount"] = str(usd)
        for k in ("CalculatedSpend", "LastUpdatedTime", "PlannedBudgetLimits"):
            b.pop(k, None)
        self._budgets().update_budget(AccountId=self.account, NewBudget=b)

    def remove_role_tag(self, role, key):
        self._iam().untag_role(RoleName=role, TagKeys=[key])

    def change_budget_action_directly(self, x):
        """Drift: switches the pause action to manual approval, outside the file."""
        name = self.budget_of(x)
        a = self.budget_action(name)
        self._budgets().update_budget_action(AccountId=self.account, BudgetName=name, ActionId=a["ActionId"],
                                             ApprovalModel="MANUAL")

    def _lambda(self):
        return self._session("admin").client("lambda")

    def change_lambda_setting_directly(self):
        """Drift: changes the monthly Lambda's SNAPSHOT_PREFIX outside the file."""
        lam, fn = self._lambda(), self.names()["lambda"]
        env = lam.get_function_configuration(FunctionName=fn)["Environment"]["Variables"]
        env["SNAPSHOT_PREFIX"] = "e2e-drift/"
        lam.update_function_configuration(FunctionName=fn, Environment={"Variables": env})
        lam.get_waiter("function_updated_v2").wait(FunctionName=fn)

    def lambda_setting(self, key, id_=None):
        fn = self.names(id_)["lambda"]
        return self._lambda().get_function_configuration(FunctionName=fn)["Environment"]["Variables"].get(key)

    def lambda_should_exist(self, id_=None):
        self._lambda().get_function(FunctionName=self.names(id_)["lambda"])

    def lambda_should_not_exist(self, id_=None):
        fn = self.names(id_)["lambda"]
        try:
            self._lambda().get_function(FunctionName=fn)
        except ClientError as e:
            if e.response["Error"]["Code"] == "ResourceNotFoundException":
                return
            raise
        raise Failure(f"Lambda {fn} still exists")

    def deny_policy_id(self, id_=None):
        """The bedrock-admin:id tag of setup id_'s deny policy; None when there is no policy."""
        try:
            tags = self._iam().list_policy_tags(PolicyArn=self._deny_arn(id_))["Tags"]
        except ClientError as e:
            if e.response["Error"]["Code"] == "NoSuchEntity":
                return None
            raise
        return {t["Key"]: t["Value"] for t in tags}.get(ID_TAG, "")

    def deny_policy_should_exist(self, id_=None):
        id_ = id_ or self.cfg["id"]
        got = self.deny_policy_id(id_)
        if got != id_:
            raise Failure(f"policy {self.names(id_)['deny']}: id tag {got!r}, expected {id_!r}")

    def deny_policy_should_not_exist(self, id_=None):
        if self.deny_policy_id(id_) is not None:
            raise Failure(f"policy {self.names(id_)['deny']} still exists")

    def _exports(self):
        c = self._session("admin").client("bcm-data-exports", region_name="us-east-1")
        return [e["ExportName"] for p in c.get_paginator("list_exports").paginate() for e in p["Exports"]]

    def cost_export_should_exist(self, id_=None):
        name = self.names(id_)["export"]
        if name not in self._exports():
            raise Failure(f"no cost export {name}")

    def cost_export_should_not_exist(self, id_=None):
        name = self.names(id_)["export"]
        if name in self._exports():
            raise Failure(f"cost export {name} still exists")

    def cost_allocation_tag_should_be_active(self, key="iamPrincipal/owner"):
        ce = self._session("admin").client("ce", region_name="us-east-1")
        try:
            tags = ce.list_cost_allocation_tags(TagKeys=[key])["CostAllocationTags"]
        except ClientError as e:
            if "linked account" in str(e).lower() or e.response["Error"]["Code"] == "AccessDeniedException":
                raise SkipExecution(f"cost allocation tag {key}: not readable from this account, the org admin "
                                    f"activates it in the management account ({e.response['Error']['Message']})")
            raise
        if not tags or tags[0]["Status"] != "Active":
            raise Failure(f"cost allocation tag {key}: {tags}")

    def personal_roles(self, id_=None):
        """The personal roles of setup id_ (under /bedrock-users/<id>/)."""
        roles = []
        path = f"/bedrock-users/{id_ or self.cfg['id']}/"
        for page in self._iam().get_paginator("list_roles").paginate(PathPrefix=path):
            roles += [r["RoleName"] for r in page["Roles"]]
        return sorted(roles)

    def create_unmanaged_role(self, x):
        """A personal role of this setup made outside bedrock-admin (no id tag). Returns its name."""
        role = self.role_of(x)
        trust = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "sts:AssumeRole",
                 "Principal": {"AWS": f"arn:aws:iam::{self.account}:root"}}]}
        self._iam().create_role(RoleName=role, Path=f"/bedrock-users/{self.cfg['id']}/", AssumeRolePolicyDocument=json.dumps(trust),
                                Tags=[{"Key": "owner", "Value": self.user_email(x)}])
        return role

    def delete_role(self, role):
        iam = self._iam()
        if not self.role_exists(role):
            return
        for p in iam.list_attached_role_policies(RoleName=role)["AttachedPolicies"]:
            iam.detach_role_policy(RoleName=role, PolicyArn=p["PolicyArn"])
        for n in iam.list_role_policies(RoleName=role)["PolicyNames"]:
            iam.delete_role_policy(RoleName=role, PolicyName=n)
        iam.delete_role(RoleName=role)

    def block_policy_file_should_exist(self):
        """apply's Step 1.7 handoff wrote the policy file for the org admin."""
        path = self.cwd / "bedrock-block-policy.json"
        if not path.exists():
            raise Failure(f"no {path}")
        doc = json.loads(path.read_text())
        if "BedrockOnlyViaPersonalRole" not in json.dumps(doc):
            raise Failure(f"{path} has no BedrockOnlyViaPersonalRole statement")
        return str(path)

    def delete_monthly_schedule(self):
        """A missing piece: deletes the monthly schedule (Step 1.4)."""
        self._session("admin").client("scheduler").delete_schedule(Name=self.names()["schedule"])

    def schedule_should_exist(self, id_=None):
        self._session("admin").client("scheduler").get_schedule(Name=self.names(id_)["schedule"])

    def bucket_exists(self, id_=None):
        try:
            self._session("admin").client("s3").head_bucket(Bucket=self.names(id_)["bucket"])
            return True
        except ClientError:
            return False

    def bucket_should_exist(self, id_=None):
        if not self.bucket_exists(id_):
            raise Failure(f"no bucket {self.names(id_)['bucket']}")

    def bucket_should_not_exist(self, id_=None):
        if self.bucket_exists(id_):
            raise Failure(f"bucket {self.names(id_)['bucket']} still exists")

    def s3_object_should_exist(self, key):
        try:
            self._s3().head_object(Bucket=self.bucket, Key=key)
        except ClientError as e:
            raise Failure(f"s3://{self.bucket}/{key}: {e.response['Error']['Code']}")

    def upload_file(self, local, key):
        self._s3().upload_file(str(local), self.bucket, key)
        logger.info(f"uploaded {local} to s3://{self.bucket}/{key}")

    def upload_usage_fixtures(self):
        """Uploads the Parquet fixtures as this and last month's cost export, and the snapshot as
        last month's, under e2e-fixtures/ in the setup's bucket (apart from the real export's prefix).
        Returns the path of a config that reads them."""
        now = dt.datetime.now(dt.timezone.utc)
        this = now.strftime("%Y-%m")
        last = (now.replace(day=1) - dt.timedelta(days=1)).strftime("%Y-%m")
        base = "e2e-fixtures"
        cfg = json.loads(json.dumps(self.cfg))
        cfg["cost_export"] = {"prefix": base + "/cur", "snapshot_prefix": base + "/snapshots"}
        data = f"{base}/cur/{self.names()['export']}/data"
        self.upload_file(FIXTURES / "cur-this-month.parquet", f"{data}/BILLING_PERIOD={this}/fixture-00001.parquet")
        self.upload_file(FIXTURES / "cur-last-month.parquet", f"{data}/BILLING_PERIOD={last}/fixture-00001.parquet")
        self.upload_file(FIXTURES / "snapshot-last-month.json", f"{base}/snapshots/{last}.json")
        BuiltIn().set_global_variable("${THIS_MONTH}", this)
        BuiltIn().set_global_variable("${LAST_MONTH}", last)
        return self.write_config("usage.yaml", cfg)

    # ---------------------------------------------------------------- the monthly Lambda

    def invoke_monthly_lambda(self):
        """Invokes the setup's monthly Lambda (it acts only on this setup's users) and returns its result."""
        r = self._lambda().invoke(FunctionName=self.names()["lambda"], Payload=b"{}")
        body = r["Payload"].read().decode()
        logger.info(f"Lambda result: {body}")
        if r.get("FunctionError"):
            raise Failure(f"Lambda failed: {body}")
        return json.loads(body)

    def lambda_snapshot_should_exist(self, result):
        """The snapshot in the Lambda result was saved where the Lambda's settings say."""
        env = self._lambda().get_function_configuration(FunctionName=self.names()["lambda"])["Environment"]["Variables"]
        key = f"{env['SNAPSHOT_PREFIX'].strip('/')}/{result['snapshot']['month']}.json"
        try:
            self._session("admin").client("s3", region_name=self._bucket_region()).head_object(Bucket=env["SNAPSHOT_BUCKET"], Key=key)
        except ClientError as e:
            raise Failure(f"no snapshot s3://{env['SNAPSHOT_BUCKET']}/{key}: {e.response['Error']['Code']}")
        return key

    # ---------------------------------------------------------------- SSO and model calls

    def start_sso(self):
        """Checks the runner's SSO session and prepares the run's own AWS config file (a copy of
        ~/.aws/config without any bedrock profile) for the user CLI. Returns the SSO email."""
        s = self._settings()
        if not s["SSO_PROFILE"]:
            raise SkipExecution("SSO_PROFILE is not set in the variable file")
        try:
            arn = self._session("sso").client("sts").get_caller_identity()["Arn"]
        except Exception as e:  # noqa: BLE001 - any failure means: sign in first
            raise Failure(f"no SSO session for {s['SSO_PROFILE']}: run aws sso login --profile {s['SSO_PROFILE']} ({e})")
        m = re.match(r"^arn:aws:sts::(\d{12}):assumed-role/AWSReservedSSO_(.+)_[0-9a-f]+/(.+)$", arn)
        if not m:
            raise Failure(f"{s['SSO_PROFILE']} is not an SSO login: {arn}")
        if m.group(1) != self.account:
            raise Failure(f"{s['SSO_PROFILE']} is in account {m.group(1)}, the admin profile in {self.account}")
        self.sso_permission_set, self.sso_email = m.group(2), m.group(3)
        src = Path(os.environ.get("AWS_CONFIG_FILE", Path.home() / ".aws" / "config"))
        self.user_aws_config = self.work / "aws-config"
        text = src.read_text() if src.exists() else ""
        text = re.sub(r"(?ms)^\[profile bedrock\][^\[]*", "", text)
        self.user_aws_config.write_text(text)
        BuiltIn().set_global_variable("${SSO_EMAIL}", self.sso_email)
        BuiltIn().set_global_variable("${SSO_PROFILE}", s["SSO_PROFILE"])
        BuiltIn().set_global_variable("${USER_AWS_CONFIG}", str(self.user_aws_config))
        logger.info(f"SSO: {arn}", also_console=True)
        return self.sso_email

    def get_sso_permission_set(self):
        return self.sso_permission_set

    def user_aws_config_text(self):
        return self.user_aws_config.read_text()

    def _call(self, profile, model, max_tokens, prompt):
        """One Converse call. profile is admin, sso, untracked, or a profile in the run's AWS config."""
        model = model or self._settings()["MODELS"][0]
        old = os.environ.get("AWS_CONFIG_FILE")
        try:
            if profile.startswith("user:"):
                sess = self._user_session(profile[5:])
            elif profile not in ("admin", "sso", "untracked") and self.user_aws_config:
                os.environ["AWS_CONFIG_FILE"] = str(self.user_aws_config)
                sess = boto3.Session(profile_name=profile)
            else:
                sess = self._session(profile)
            rt = sess.client("bedrock-runtime", region_name=self.s["REGION"])
            r = rt.converse(modelId=model, messages=[{"role": "user", "content": [{"text": prompt}]}],
                            inferenceConfig={"maxTokens": int(max_tokens)})
            u = r["usage"]
            return {"ok": True, "code": "", "message": "", "cost": self._cost(model, u["inputTokens"], u["outputTokens"])}
        except ClientError as e:
            return {"ok": False, "code": e.response["Error"]["Code"], "message": e.response["Error"]["Message"], "cost": 0}
        finally:
            if old is None:
                os.environ.pop("AWS_CONFIG_FILE", None)
            else:
                os.environ["AWS_CONFIG_FILE"] = old

    @staticmethod
    def _cost(model, tin, tout):
        for k, (pin, pout) in PRICES.items():
            if k in model:
                return (tin * pin + tout * pout) / 1e6
        return (tin * 3 + tout * 15) / 1e6

    def model_call_should_work(self, profile, model=None):
        r = self._call(profile, model, 1, "Reply with OK.")
        if not r["ok"]:
            raise Failure(f"call as {profile} failed: {r['code']}: {r['message']}")

    def wait_until_sso_can_assume(self, role_arn, timeout=180):
        """New roles take a while to be assumable (IAM propagation); waits until the SSO login can."""
        sts = self._session("sso").client("sts")
        deadline, err = time.time() + int(timeout), None
        while time.time() < deadline:
            try:
                sts.assume_role(RoleArn=role_arn, RoleSessionName=self.sso_email, DurationSeconds=900)
                return
            except Exception as e:  # noqa: BLE001 - AccessDenied until IAM propagates
                err = e
                time.sleep(5)
        raise Failure(f"SSO login still can't assume {role_arn} after {timeout}s: {err}")

    def model_call_should_be_denied(self, profile, model=None, explicit=True):
        r = self._call(profile, model, 1, "Reply with OK.")
        if r["ok"]:
            raise Failure(f"call as {profile} worked, expected AccessDenied")
        if "AccessDenied" not in r["code"]:
            raise Failure(f"call as {profile}: {r['code']}: {r['message']}, expected AccessDenied")
        if _bool(explicit) and "explicit deny" not in r["message"]:
            raise Failure(f"call as {profile} denied, but not by an explicit deny: {r['message']}")
        return r["message"]

    def allowed_models(self):
        """The models the test users use from REGION (config.RegionModels): MODELS without baseline
        entries, followed by REGION's baseline models. Other prefixes' baseline models are allowed
        but not tested."""
        s = self._settings()
        own = [m for m in s["MODELS"] if m not in baseline_all_geos()]
        return own + baseline_models(s["REGION"])

    def baseline_only_model(self):
        """A baseline model for REGION that MODELS doesn't list (the user may still call it), or ""."""
        s = self._settings()
        return next((m for m in baseline_models(s["REGION"]) if m not in s["MODELS"]), "")

    def unlisted_model(self):
        """A Claude Haiku 4.5 inference profile for REGION, or "" when MODELS or the baseline lists it."""
        geo = self.s["MODELS"][0].split(".", 1)[0]
        m = f"{geo}.anthropic.claude-haiku-4-5-20251001-v1:0"
        return "" if m in self.s["MODELS"] + baseline_all_geos() else m

    def model_call_is_denied(self, profile, model=None):
        r = self._call(profile, model, 1, "Reply with OK.")
        return not r["ok"] and "AccessDenied" in r["code"]

    def spend(self, profile, usd, model=None):
        """Makes calls as profile until about usd is spent (estimated from token counts).
        Returns the estimated spend."""
        target, spent, n = float(usd), 0.0, 0
        prompt = "Write a long, detailed essay on the history of bridges. Do not stop early."
        while spent < target:
            r = self._call(profile, model, 4096, prompt)
            if not r["ok"]:
                raise Failure(f"spending as {profile}: {r['code']}: {r['message']} (spent ${spent:.2f})")
            spent, n = spent + r["cost"], n + 1
            if n % 5 == 0:
                _console(f"    spent ${spent:.2f} of ${target:.2f} as {profile}")
        logger.info(f"spent ${spent:.4f} in {n} calls as {profile}", also_console=True)
        return round(spent, 4)

    def identity(self, profile):
        if profile.startswith("user:"):
            return self._user_session(profile[5:]).client("sts").get_caller_identity()["Arn"]
        if profile in ("admin", "sso", "untracked"):
            return self._session(profile).client("sts").get_caller_identity()["Arn"]
        old = os.environ.get("AWS_CONFIG_FILE")
        os.environ["AWS_CONFIG_FILE"] = str(self.user_aws_config)
        try:
            return boto3.Session(profile_name=profile).client("sts").get_caller_identity()["Arn"]
        finally:
            if old is None:
                os.environ.pop("AWS_CONFIG_FILE", None)
            else:
                os.environ["AWS_CONFIG_FILE"] = old

    # ---------------------------------------------------------------- IAM users

    def _iam_users_policy_arn(self, id_=None):
        return f"arn:aws:iam::{self.account}:policy/bedrock/bedrock-iam-users-{id_ or self.cfg['id']}"

    def create_test_iam_user(self, x):
        """Creates IAM user <run id>-x with an access key, and adds profile e2e-key-x with the key to
        the run's AWS config (written here when SSO wasn't started). Returns [name, profile]."""
        name, profile = f"{self.run_id}-{x}", f"e2e-key-{x}"
        iam = self._iam()
        iam.create_user(UserName=name, Tags=[{"Key": "e2e-run", "Value": self.run_id}])
        key = iam.create_access_key(UserName=name)["AccessKey"]
        if not self.user_aws_config:
            self.user_aws_config = self.work / "aws-config"
            self.user_aws_config.write_text("")
            BuiltIn().set_global_variable("${USER_AWS_CONFIG}", str(self.user_aws_config))
        with open(self.user_aws_config, "a") as f:
            f.write(f"\n[profile {profile}]\naws_access_key_id = {key['AccessKeyId']}\n"
                    f"aws_secret_access_key = {key['SecretAccessKey']}\nregion = {self.s['REGION']}\n")
        logger.info(f"IAM user {name}, profile {profile}", also_console=True)
        return [name, profile]

    def wait_until_access_key_works(self, profile, timeout=120):
        """New access keys take a few seconds to be usable."""
        deadline, err = time.time() + int(timeout), None
        while time.time() < deadline:
            try:
                return self.identity(profile)
            except Exception as e:  # noqa: BLE001 - InvalidClientTokenId until the key propagates
                err = e
                time.sleep(5)
        raise Failure(f"access key of {profile} still doesn't work after {timeout}s: {err}")

    def iam_user_tags(self, name):
        tags = self._iam().list_user_tags(UserName=name)["Tags"]
        return {t["Key"]: t["Value"] for t in tags}

    def iam_users_policy_attached(self, name):
        pols = self._iam().list_attached_user_policies(UserName=name)["AttachedPolicies"]
        return any(p["PolicyArn"] == self._iam_users_policy_arn() for p in pols)

    def iam_user_should_be_onboarded(self, name, email, product=None):
        """Tagged like the personal role (owner, product, bedrock-admin:id), so the user's budget
        counts its spend, with the setup's IAM user policy attached."""
        tags = self.iam_user_tags(name)
        want = {"bedrock-admin:id": self.cfg["id"], "owner": email, "product": product or self.cfg["product"]}
        for k, v in want.items():
            if tags.get(k) != v:
                raise Failure(f"IAM user {name}: tag {k} is {tags.get(k)!r}, expected {v!r}")
        if not self.iam_users_policy_attached(name):
            raise Failure(f"IAM user {name}: {self._iam_users_policy_arn()} is not attached")

    def iam_user_should_be_released(self, name):
        """Detached, unpaused and untagged, but not deleted."""
        tags = self.iam_user_tags(name)
        left = [k for k in tags if k in ("owner", "product") or k.startswith("bedrock-admin:")]
        if left:
            raise Failure(f"IAM user {name} still has tags {left}")
        if self.iam_users_policy_attached(name):
            raise Failure(f"IAM user {name}: {self._iam_users_policy_arn()} is still attached")
        if self.deny_attached_to_iam_user(name):
            raise Failure(f"IAM user {name}: {self.names()['deny']} is still attached")

    def deny_attached_to_iam_user(self, name):
        pols = self._iam().list_attached_user_policies(UserName=name)["AttachedPolicies"]
        return any(p["PolicyArn"] == self._deny_arn() for p in pols)

    def deny_policy_should_be_attached_to_iam_user(self, name):
        if not self.deny_attached_to_iam_user(name):
            raise Failure(f"{self.names()['deny']} is not attached to IAM user {name}")

    def deny_policy_should_not_be_attached_to_iam_user(self, name):
        if self.deny_attached_to_iam_user(name):
            raise Failure(f"{self.names()['deny']} is attached to IAM user {name}")

    def budget_action_iam_users_should_be(self, x, *names):
        """The IAM users the user's pause action denies, besides the personal role."""
        d = self.budget_action(self.budget_of(x))["Definition"]["IamActionDefinition"]
        got = sorted(d.get("Users") or [])
        if got != sorted(names):
            raise Failure(f"{self.budget_of(x)}: pause action IAM users {got}, expected {sorted(names)}")

    def _iam_users_policy_exists(self, id_=None):
        try:
            self._iam().get_policy(PolicyArn=self._iam_users_policy_arn(id_))
            return True
        except ClientError as e:
            if e.response["Error"]["Code"] == "NoSuchEntity":
                return False
            raise

    def iam_users_policy_should_exist(self, id_=None):
        if not self._iam_users_policy_exists(id_):
            raise Failure(f"{self._iam_users_policy_arn(id_)} does not exist")

    def iam_users_policy_should_not_exist(self, id_=None):
        if self._iam_users_policy_exists(id_):
            raise Failure(f"{self._iam_users_policy_arn(id_)} still exists")

    def delete_test_iam_users(self):
        """Deletes the IAM users this run created (detaching policies and deleting keys first)."""
        if not self.run_id or self.offline:
            return
        iam = self._iam()
        for page in iam.get_paginator("list_users").paginate():
            for u in page["Users"]:
                name = u["UserName"]
                if not name.startswith(self.run_id + "-"):
                    continue
                for p in iam.list_attached_user_policies(UserName=name)["AttachedPolicies"]:
                    iam.detach_user_policy(UserName=name, PolicyArn=p["PolicyArn"])
                for k in iam.list_access_keys(UserName=name)["AccessKeyMetadata"]:
                    iam.delete_access_key(UserName=name, AccessKeyId=k["AccessKeyId"])
                iam.delete_user(UserName=name)
                logger.info(f"deleted IAM user {name}", also_console=True)

    def write_test_user_profile(self, x, profile=None):
        """Adds a profile for test user x's role to the run's AWS config, with the SSO profile as
        source (the trust policy only lets the SSO login with session name <email> in, so this only
        works for "me"; other users get a profile that the admin profile can't use either)."""
        profile = profile or f"e2e-{x}"
        role = self.role_arn_of(x)
        with open(self.user_aws_config, "a") as f:
            f.write(f"\n[profile {profile}]\nrole_arn = {role}\nsource_profile = {self.s['SSO_PROFILE']}\n"
                    f"role_session_name = {self.user_email(x)}\nregion = {self.s['REGION']}\n")
        return profile

    # ---------------------------------------------------------------- questions

    def confirm(self, question):
        """Asks a y/n question on the terminal; skipped when INTERACTIVE is False."""
        if not self._settings()["INTERACTIVE"] or not sys.__stdin__.isatty():
            raise SkipExecution(f"not interactive: {question}")
        _console(f"\n  {question} [y/n] ")
        if sys.__stdin__.readline().strip().lower() not in ("y", "yes"):
            raise Failure(f"answered no: {question}")

    # ---------------------------------------------------------------- billing state

    def _state_path(self, run_id):
        return STATE_DIR / f"{run_id}.json"

    def save_billing_state(self, **values):
        STATE_DIR.mkdir(parents=True, exist_ok=True)
        path = self._state_path(self.run_id)
        st = json.loads(path.read_text()) if path.exists() else {"run_id": self.run_id, "phases": []}
        st.update({k: _value(v) for k, v in values.items()})
        st["config"] = self.cfg
        st["sso_email"] = self.sso_email
        path.write_text(json.dumps(st, indent=2) + "\n")
        return str(path)

    def start_billing_phase1(self):
        st = {"run_id": self.run_id, "phase1_at": dt.datetime.now(dt.timezone.utc).isoformat(), "phases": []}
        STATE_DIR.mkdir(parents=True, exist_ok=True)
        self._state_path(self.run_id).write_text(json.dumps(st))
        return self.save_billing_state()

    def load_billing_state(self):
        """Loads the run given by RUN_ID, else the newest unfinished one, and makes it this run."""
        rid = self._settings()["RUN_ID"]
        if rid:
            path = self._state_path(rid)
            if not path.exists():
                raise Failure(f"no billing state {path}")
        else:
            runs = [p for p in STATE_DIR.glob("*.json") if "phase3" not in json.loads(p.read_text()).get("phases", [])]
            if not runs:
                raise SkipExecution("no unfinished billing run: run ./run.sh billing --include phase1 first")
            path = max(runs, key=lambda p: json.loads(p.read_text())["phase1_at"])
        st = json.loads(path.read_text())
        self.run_id, self.cfg, self.sso_email = st["run_id"], st["config"], st.get("sso_email", "")
        BuiltIn().set_global_variable("${RUN}", self.run_id)
        self._publish_id()
        logger.info(f"Billing run {self.run_id}, phase 1 at {st['phase1_at']}", also_console=True)
        return st

    def billing_state(self):
        return json.loads(self._state_path(self.run_id).read_text())

    def hours_since_phase1(self):
        t = dt.datetime.fromisoformat(self.billing_state()["phase1_at"])
        return (dt.datetime.now(dt.timezone.utc) - t).total_seconds() / 3600

    def require_hours_since_phase1(self, hours):
        """Skips with "retry after <time>" when phase 1 was less than hours ago."""
        h = self.hours_since_phase1()
        if h < float(hours):
            at = dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=float(hours) - h)
            raise SkipExecution(f"too early: retry after {at:%Y-%m-%d %H:%M} UTC ({hours} h after phase 1)")

    def billing_caught_up_or_skip(self, ok, what):
        """When ok is false: skips with "retry after <time>", or fails 48 h after phase 1."""
        if _bool(ok):
            return
        h = self.hours_since_phase1()
        if h >= 48:
            raise Failure(f"{what}: still not seen {h:.0f} h after phase 1")
        at = dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=2)
        raise SkipExecution(f"billing not caught up yet ({what}): retry after {at:%Y-%m-%d %H:%M} UTC")

    def mark_phase_done(self, phase):
        st = self.billing_state()
        st["phases"] = sorted(set(st.get("phases", [])) | {phase})
        self._state_path(self.run_id).write_text(json.dumps(st, indent=2) + "\n")

    def delete_billing_state(self):
        self._state_path(self.run_id).unlink(missing_ok=True)

    # ---------------------------------------------------------------- web search (Step 1.8)

    def web_search_region_should_be_supported(self):
        region = self._settings()["REGION"]
        if region not in WEB_SEARCH_REGIONS:
            raise SkipExecution(f"web search isn't offered in {region}: set REGION to one of {', '.join(WEB_SEARCH_REGIONS)}")

    def _agentcore(self):
        return self._session("admin").client("bedrock-agentcore-control")

    def web_search_gateway(self):
        """The setup's gateway (GetGateway output), bedrock-web-search-<id>, or None."""
        ac = self._agentcore()
        name = self.names()["web_search_gateway"]
        for page in ac.get_paginator("list_gateways").paginate():
            for g in page["items"]:
                if g["name"] == name:
                    return ac.get_gateway(gatewayIdentifier=g["gatewayId"])
        return None

    def web_search_target(self, gateway_id):
        ac = self._agentcore()
        for page in ac.get_paginator("list_gateway_targets").paginate(gatewayIdentifier=gateway_id):
            for t in page["items"]:
                if t["name"] == WEB_SEARCH_TARGET:
                    return ac.get_gateway_target(gatewayIdentifier=gateway_id, targetId=t["targetId"])
        return None

    def web_search_should_be_ready(self):
        """The setup's gateway: READY, IAM auth, the setup's gateway role and id tag, and its Web Search
        target READY. Returns the gateway URL."""
        g = self.web_search_gateway()
        n, owner = self.names(), self.cfg["id"]
        if not g:
            raise Failure(f"no gateway {n['web_search_gateway']}")
        problems = []
        if g["status"] != "READY":
            problems.append(f"gateway status {g['status']}")
        if g.get("authorizerType") != "AWS_IAM":
            problems.append(f"authorizer {g.get('authorizerType')}")
        if not g.get("roleArn", "").endswith(f"/{n['web_search_role']}"):
            problems.append(f"role {g.get('roleArn')}")
        tags = self._agentcore().list_tags_for_resource(resourceArn=g["gatewayArn"]).get("tags", {})
        if tags.get(ID_TAG) != owner:
            problems.append(f"tags {tags}, expected {ID_TAG}={owner}")
        t = self.web_search_target(g["gatewayId"])
        if not t:
            problems.append(f"no target {WEB_SEARCH_TARGET}")
        elif t["status"] != "READY":
            problems.append(f"target status {t['status']}")
        if problems:
            raise Failure("web search: " + "; ".join(problems))
        return g["gatewayUrl"]

    def web_search_gateway_should_not_exist(self):
        if self.web_search_gateway():
            raise Failure(f"gateway {self.names()['web_search_gateway']} still exists")

    def role_policy_text(self, role, name="bedrock-invoke"):
        return json.dumps(self._iam().get_role_policy(RoleName=role, PolicyName=name)["PolicyDocument"])

    def _mcp(self, profile, url, method, params):
        """One SigV4-signed MCP JSON-RPC call as profile (user:<x>, sso, or a profile in the run's
        AWS config). Returns (HTTP status, JSON-RPC result or error text)."""
        if profile.startswith("user:"):
            sess = self._user_session(profile[5:])
        elif profile not in ("admin", "sso", "untracked") and self.user_aws_config:
            os.environ["AWS_CONFIG_FILE"] = str(self.user_aws_config)
            try:
                sess = boto3.Session(profile_name=profile)
            finally:
                os.environ.pop("AWS_CONFIG_FILE", None)
        else:
            sess = self._session(profile)
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
        req = AWSRequest(method="POST", url=url, data=body, headers={
            "Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
        SigV4Auth(sess.get_credentials().get_frozen_credentials(), "bedrock-agentcore", self._settings()["REGION"]).add_auth(req)
        try:
            with urllib.request.urlopen(urllib.request.Request(url, data=body.encode(), headers=dict(req.headers),
                                                               method="POST"), timeout=60) as r:
                text = r.read().decode()
                status = r.status
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()
        for line in text.splitlines():
            if line.startswith("data:"):
                text = line[5:].strip()
        d = json.loads(text)
        if "error" in d:
            return status, json.dumps(d["error"])
        return status, d["result"]

    def web_search_should_work(self, profile, url):
        """One real search (maxResults 1, about $0.007) as profile."""
        status, res = self._mcp(profile, url, "tools/call", {"name": WEB_SEARCH_TOOL,
                                                            "arguments": {"query": "Amazon Bedrock", "maxResults": 1}})
        if status != 200 or not isinstance(res, dict) or res.get("isError"):
            raise Failure(f"search as {profile}: HTTP {status}: {res}")
        logger.info(json.dumps(res)[:2000])

    def web_search_should_be_denied(self, profile, url):
        """tools/list as profile is refused (HTTP 401/403)."""
        status, res = self._mcp(profile, url, "tools/list", {})
        if status not in (401, 403):
            raise Failure(f"tools/list as {profile}: HTTP {status}, expected 403: {res}")

    def mcp_proxy_should_list_web_search(self, server_json):
        """Runs the MCP server printed by bedrock claude (uvx mcp-proxy-for-aws ...) over stdio, with
        the run's AWS config, and checks that tools/list has the Web Search tool."""
        server = json.loads(server_json)
        env = dict(os.environ, AWS_CONFIG_FILE=str(self.user_aws_config))
        env.pop("AWS_PROFILE", None)
        msgs = [{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
                    "protocolVersion": "2025-06-18", "capabilities": {},
                    "clientInfo": {"name": "bedrock-e2e", "version": "1"}}},
                {"jsonrpc": "2.0", "method": "notifications/initialized"},
                {"jsonrpc": "2.0", "id": 2, "method": "tools/list"}]
        cmd = [server["command"]] + server["args"]
        logger.info("$ " + " ".join(cmd))
        p = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                             text=True, env=env, cwd=self.cwd)
        try:
            p.stdin.write(json.dumps(msgs[0]) + "\n")
            p.stdin.flush()
            deadline, sent = time.time() + 300, False
            while time.time() < deadline:
                line = p.stdout.readline()
                if not line:
                    break
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                if d.get("id") == 1 and not sent:
                    p.stdin.write(json.dumps(msgs[1]) + "\n" + json.dumps(msgs[2]) + "\n")
                    p.stdin.flush()
                    sent = True
                elif d.get("id") == 2:
                    names = [t["name"] for t in d.get("result", {}).get("tools", [])]
                    if WEB_SEARCH_TOOL not in names:
                        raise Failure(f"tools/list through the proxy: {d}")
                    return names
            raise Failure("no tools/list answer from the proxy")
        finally:
            p.kill()
            _, err = p.communicate(timeout=30)
            logger.info(f"proxy stderr:\n{err[-4000:]}")

    # ---------------------------------------------------------------- small helpers

    @staticmethod
    def json_path(data, path):
        """Gets a value by dotted path; list items by index or by key=value, e.g. users.email=a@x.y.stat"""
        cur = data
        for part in re.findall(r"[^.=]+=[^=]+?(?=\.[a-z_]+(?:\.|$)|$)|[^.]+", path):
            if isinstance(cur, list):
                if "=" in part:
                    k, v = part.split("=", 1)
                    m = [i for i in cur if str(i.get(k)) == v]
                    if not m:
                        raise Failure(f"no item with {k}={v} in {cur}")
                    cur = m[0]
                else:
                    cur = cur[int(part)]
            else:
                if part not in cur:
                    raise Failure(f"no key {part!r} in {list(cur)}")
                cur = cur[part]
        return cur

    @staticmethod
    def should_be_close(got, want, tolerance=0.01):
        if abs(float(got) - float(want)) > float(tolerance):
            raise Failure(f"{got} is not within {tolerance} of {want}")

    @staticmethod
    def copy_file(src, dst):
        shutil.copy(src, dst)
