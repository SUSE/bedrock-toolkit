*** Settings ***
Documentation       The account setup (Steps 1.1-1.8 but web search): apply, plan, drift, bedrock-admin block,
...                 doctor, models, export, central reporting (fleet init, apply, the daily Lambda report,
...                 fleet report, by month), a second setup in the same account (setups lists both), and uninstall. The run's setup has
...                 its own id (e2e<hex>) and is removed at the end, with its bucket.
Resource            ../resources/common.resource
Suite Setup         Start Account Run
Suite Teardown      Tear Down If Set Up
Test Tags           account


*** Test Cases ***
Apply Creates The Account Setup
    [Documentation]    Suite Setup applied the setup; every account-level name carries the id.
    Bucket Should Exist
    Cost Export Should Exist
    Deny Policy Should Exist
    Role Should Exist    ${N}[budget_actions_role]
    Role Should Exist    ${N}[lambda_role]
    Role Should Exist    ${N}[scheduler_role]
    Lambda Should Exist
    ${env_id}=    Lambda Setting    BEDROCK_ID
    Should Be Equal    ${env_id}    ${ID}
    Schedule Should Exist
    ${daily}=    Daily Schedule Exists
    Should Be True    ${daily}    the daily schedule runs without report: too
    ${p}=    Plan
    ${allowed}=    Allowed Models
    FOR    ${m}    IN    @{allowed}
        Plan Op Should Be    ${p}    model ${m}    ok
    END
    ${tag}=    Plan Item    ${p}    cost allocation tag iamPrincipal/owner
    IF    $tag["op"] == "info" and "can't check" in $tag["status"]
        Should Contain    ${tag}[warnings][0]    management account
    ELSE IF    $tag["status"].startswith("active (")
        Log    not readable from this account, active by evidence: ${tag}[status]
    ELSE
        Cost Allocation Tag Should Be Active
    END

Second Apply Changes Nothing
    ${p}=    Plan
    Plan Should Have No Changes    ${p}
    ${a}=    Apply    rc=any
    Should Be Empty    ${a.json}[applied]    second apply changed: ${a.json}[applied]

Plan Names Exactly The Missing Piece
    Delete Monthly Schedule
    ${p}=    Plan    rc=2
    ${pending}=    Pending Targets    ${p}
    Should Be Equal    ${pending}    ${{ ["schedule " + $N["schedule"]] }}
    Plan Op Should Be    ${p}    schedule ${N}[schedule]    create
    ${out}=    Admin    plan    rc=2
    Should Contain    ${out}    + 1.4 schedule ${N}[schedule]
    Apply    rc=any
    Schedule Should Exist

Doctor Names The Missing Piece
    Delete Monthly Schedule
    ${d}=    Admin    doctor    --json    rc=2
    ${bad}=    Evaluate    [c["name"] for c in $d["checks"] if not c["ok"]]
    Should Contain    ${bad}    Step 1.4 schedule ${N}[schedule]
    Apply    rc=any
    Schedule Should Exist

Doctor Passes
    ${d}=    Admin    doctor    --json    rc=any
    ${bad}=    Evaluate    [c["name"] for c in $d["checks"] if not c["ok"] and c["name"] != "cost export data"]
    Should Be Empty    ${bad}    doctor failed: ${d}
    # Step 1.6 never fails doctor when the tag can't be read: ok, or a warning to ask the org admin.
    ${tag}=    Evaluate    next(c for c in $d["checks"] if c["name"].startswith("Step 1.6 "))
    Should Be True    $tag["ok"]    ${tag}
    IF    $tag.get("warn")
        Should Contain    ${tag}[fix]    management account
    END
    ${names}=    Evaluate    [c["name"] for c in $d["checks"]]
    Should Contain    ${names}    credentials
    Should Contain    ${names}    permissions

Models Shows Where Allowed Models Send Requests
    [Documentation]    models reads each allowed profile's regions from AWS; only allowed rows get them.
    ${rows}=    Admin    models    --filter    claude    --json
    ${listed}=    Evaluate    [r for r in $rows if r["id"] in $MODELS]
    Should Not Be Empty    ${listed}    no inference profile of ${MODELS} in the list
    FOR    ${row}    IN    @{listed}
        Should Be Equal    ${row}[users]    allowed
        IF    $row["id"].startswith("global.")
            Should Be Equal    ${row}[regions]    ${{["any"]}}
        ELSE
            Should Contain    ${row}[regions]    ${REGION}
        END
    END
    ${other}=    Evaluate    [r["id"] for r in $rows if r["users"] != "allowed" and r.get("regions")]
    Should Be Empty    ${other}

Drift Is Shown By Plan And Fixed By Apply
    Change Lambda Setting Directly
    ${p}=    Plan    rc=2
    ${i}=    Plan Op Should Be    ${p}    Lambda ${N}[lambda]    update
    Should Contain    ${i}[details]    settings changed outside the file
    Apply    rc=any
    ${prefix}=    Lambda Setting    SNAPSHOT_PREFIX
    Should Not Contain    ${prefix}    e2e-drift
    ${p}=    Plan
    Plan Op Should Be    ${p}    Lambda ${N}[lambda]    ok

Block Status Reads The Permission Sets
    [Documentation]    block status reads the account's SSO roles: the admin permission set is exempt,
    ...    and BLOCK_TEST_PERMISSION_SET (given the deny once by an org admin) carries it. block policy
    ...    writes the file for the org admin. block apply needs Organizations rights: not tested.
    Admin    block    policy    --admin-permission-set    ${ADMIN_PERMISSION_SET}
    Block Policy File Should Exist
    ${s}=    Admin    block    status    --admin-permission-set    ${ADMIN_PERMISSION_SET}    --json
    Should Be Equal    ${s}[account]    ${ACCOUNT}
    Should Not Be Empty    ${s}[permission_sets]
    ${deny}=    Evaluate    {p["name"]: p["deny"] for p in $s["permission_sets"]}
    ${bad}=    Evaluate    {n: d for n, d in $deny.items() if (d == "exempt") != (n == $ADMIN_PERMISSION_SET) or d not in ("yes", "no", "exempt")}
    Should Be Empty    ${bad}    ${deny}
    IF    $BLOCK_TEST_PERMISSION_SET
        Should Be Equal    ${deny}[${BLOCK_TEST_PERMISSION_SET}]    yes
    END

Export Then Plan Shows No Changes
    ${models}=    Evaluate    [a for m in $MODELS for a in ("--model", m)]
    ${yaml}=    Admin    export    --id    ${ID}    --region    ${REGION}    --product    ${PRODUCT}    @{models}
    ...    config=none
    ${f}=    Write Raw Config    ${yaml}    exported.yaml
    ${p}=    Admin    plan    --json    config=${f}    rc=any
    Plan Should Have No Changes    ${p}

Fleet Init Creates The Central Report Bucket
    [Documentation]    --accounts, so the run needs no AWS Organizations permission.
    Admin    fleet    init    --bucket    ${N}[report_bucket]    --region    ${REGION}    --accounts    ${ACCOUNT}
    ...    --yes    config=none
    ${out}=    Admin    fleet    init    --bucket    ${N}[report_bucket]    --region    ${REGION}
    ...    --accounts    ${ACCOUNT}    --dry-run    config=none
    Should Contain    ${out}    No changes

Fleet Finds The Tagged Bucket Without --bucket
    [Documentation]    Lists and reads the tags of every bucket in the account. With -f the run's file
    ...    gives the region the admin profile lacks. Another run's report bucket in the same account
    ...    makes the choice ambiguous; then the error must list this one.
    ${r}=    Admin    fleet    init    --accounts    ${ACCOUNT}    --dry-run    rc=any    output=result
    Should Contain    ${r.stdout}${r.stderr}    ${N}[report_bucket]
    IF    ${r.rc} == 0    Should Contain    ${r.stdout}    No changes

Fleet Init Refuses The Setup's Cost Export Bucket
    ${r}=    Admin    fleet    init    --bucket    ${N}[bucket]    --region    ${REGION}    --accounts    ${ACCOUNT}
    ...    --dry-run    config=none    rc=any    output=result
    Should Not Be Equal As Integers    ${r.rc}    0

Apply Sends The Central Report
    Set Config    report.bucket    ${N}[report_bucket]
    Set Config    report.region    ${REGION}
    ${r}=    Apply
    ${report}=    Central Report
    Should Be Equal    ${report}[status]    active
    Should Be Equal    ${report}[source]    apply
    Should Be Equal    ${report}[id]    ${ID}
    Should Not Contain    ${report}    people
    Plan Should Show No Changes

The Lambda Sends The Daily Report And Usage
    Invoke Daily Lambda
    ${report}=    Central Report
    Should Be Equal    ${report}[source]    lambda
    Should Be Equal    ${report}[status]    active
    ${u}=    Central Usage
    Should Be Equal    ${u}[month]    ${report}[month]

Fleet Report Shows The Setup
    ${f}=    Admin    fleet    report    --bucket    ${N}[report_bucket]    --region    ${REGION}    --json
    ...    config=none
    ${mine}=    Evaluate    [r for r in $f["reports"] if r["account"] == $ACCOUNT and r["id"] == $ID]
    Length Should Be    ${mine}    1
    Should Be Equal    ${mine}[0][product]    ${PRODUCT}
    Should Not Contain    ${f}    problems
    ${p}=    Admin    fleet    report    --bucket    ${N}[report_bucket]    --region    ${REGION}    --by    product
    ...    --json    config=none
    Should Contain    ${p}[setups]    ${ACCOUNT}/${ID}
    # The month view reads the usage files the daily Lambda wrote to the run's own report bucket.
    ${m}=    Admin    fleet    report    --bucket    ${N}[report_bucket]    --region    ${REGION}    --by    month
    ...    --months    1    --json    config=none
    Length Should Be    ${m}[months]    1
    Should Be Equal As Integers    ${m}[months][0][setups]    1
    Should Be True    ${m}[months][0][partial]

Export Keeps The Report Settings
    ${yaml}=    Admin    export    --id    ${ID}    --region    ${REGION}    --product    ${PRODUCT}    config=none
    Should Contain    ${yaml}    ${N}[report_bucket]

Turning The Report Off Keeps The Daily Schedule
    Unset Config    report
    Apply
    ${env}=    Lambda Setting    REPORT_SETUP
    Should Be True    not $env
    ${exists}=    Daily Schedule Exists
    Should Be True    ${exists}
    Plan Should Show No Changes
    Set Config    report.bucket    ${N}[report_bucket]
    Set Config    report.region    ${REGION}
    Apply

Roles Without An Id Are Left Alone
    ${role}=    Create Unmanaged Role    y
    ${p}=    Plan
    ${email}=    User Email    y
    ${i}=    Plan Op Should Be    ${p}    ${email}    info
    Should Be Equal    ${i}[status]    not managed (role ${role} has no bedrock-admin:id tag)
    Apply    rc=any
    Role Should Exist    ${role}
    [Teardown]    Delete Role    ${role}

Uninstall Refuses While Users Remain
    Add User    a
    Apply    rc=any
    ${r}=    Admin    uninstall    --yes    rc=1    output=result
    Should Contain    ${r.stderr}    personal roles remain
    Lambda Should Exist
    [Teardown]    Run Keywords    Set Users    AND    Apply    rc=any

A Second Setup In The Same Account Is Separate
    [Documentation]    Another id in the same account gets its own resources and personal roles. One person
    ...    can be a user of both. Each setup's plan, Lambda and uninstall only touch their own id.
    ${other}=    Set Variable    ${ID}b
    ${f}=    Write Config With Id    ${other}    x
    Add User    x
    Apply
    Admin    apply    --yes    config=${f}    rc=any
    Deny Policy Should Exist    ${other}
    Lambda Should Exist    ${other}
    Schedule Should Exist    ${other}
    Cost Export Should Exist    ${other}
    Bucket Should Exist    ${other}
    ${r1}=    Role Of    x
    ${r2}=    Role Of    x    ${other}
    Role Tag Should Be    ${r1}    bedrock-admin:id    ${ID}
    Role Tag Should Be    ${r2}    bedrock-admin:id    ${other}
    ${b2}=    Budget Of    x    ${other}
    Budget Limit Should Be    ${b2}    1
    ${roles}=    Personal Roles
    Should Be Equal    ${roles}    ${{ [$r1] }}
    Plan Should Show No Changes
    ${s}=    Admin    setups    --json
    Should Be Equal    ${s}[account]    ${ACCOUNT}
    Should Be Equal    ${s}[current]    ${ID}
    ${mine}=    Evaluate    {x["id"]: x for x in $s["setups"] if x["id"] in ($ID, $other)}
    Should Be Equal As Integers    ${mine}[${other}][users]    1
    Should Be True    ${mine}[${ID}][users] >= 1
    Should Not Be True    ${mine}[${ID}][idle] or ${mine}[${other}][idle]
    Should Be True    ${mine}[${other}][limit_usd] == 1
    # Without a file it lists the profile's account the same way, with no current id.
    ${s}=    Admin    setups    --json    config=none
    Should Not Contain    ${s}    current
    ${ids}=    Evaluate    [x["id"] for x in $s["setups"]]
    Should Contain    ${ids}    ${ID}
    Should Contain    ${ids}    ${other}
    ${r}=    Admin    export    --region    ${REGION}    config=none    rc=1    output=result
    Should Contain    ${r.stderr}    pass --id
    ${result}=    Invoke Monthly Lambda
    ${roles}=    Evaluate    [u["role"] for u in $result["snapshot"]["users"]]
    Should Be Equal    ${roles}    ${{ [$r1] }}
    Admin    uninstall    --yes    --delete-data    config=${f}    rc=1
    Write Config With Id    ${other}    name=${other}.yaml
    Admin    apply    --yes    config=${f}
    Admin    uninstall    --yes    --delete-data    config=${f}
    Lambda Should Not Exist    ${other}
    Deny Policy Should Not Exist    ${other}
    Bucket Should Not Exist    ${other}
    Role Should Not Exist    ${r2}
    Lambda Should Exist
    Role Should Exist    ${r1}
    Plan Should Show No Changes
    [Teardown]    Run Keywords    Remove User    x    AND    Apply    rc=any
    ...    AND    Write Config With Id    ${other}    name=${other}.yaml
    ...    AND    Admin    apply    --yes    config=${f}    rc=any
    ...    AND    Admin    uninstall    --yes    --delete-data    config=${f}    rc=any

Uninstall Keeps The Bucket
    Admin    uninstall    --yes
    Lambda Should Not Exist
    Deny Policy Should Not Exist
    Role Should Not Exist    ${N}[budget_actions_role]
    Cost Export Should Not Exist
    Bucket Should Exist
    ${exists}=    Daily Schedule Exists
    Should Not Be True    ${exists}
    ${report}=    Central Report
    Should Be Equal    ${report}[status]    uninstalled
    Should Be Equal    ${report}[source]    uninstall

Uninstall With Delete Data Removes The Bucket
    Admin    uninstall    --yes    --delete-data
    Bucket Should Not Exist


*** Keywords ***
Start Account Run
    Start Run
    Set Up Account

Tear Down If Set Up
    ${exists}=    Bucket Exists
    IF    ${exists}    Tear Down Run
    ${keep}=    Keep Setup
    IF    not ${keep}    Delete Report Bucket
