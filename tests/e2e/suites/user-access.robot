*** Settings ***
Documentation       What a user does: bedrock setup, calls through the personal role, bedrock doctor, usage and models, the
...                 Step 1.7 block on direct SSO calls, and a manual pause seen from the user's side.
...                 Needs SSO_PROFILE signed in (aws sso login) in the same account. The runner is added as
...                 a user of the run's own setup (id e2e<hex>); the user CLI writes to a copy of ~/.aws/config,
...                 never the real one.
Resource            ../resources/common.resource
Suite Setup         Start User Access Run
Suite Teardown      Tear Down Run
Test Tags           user-access


*** Variables ***
${PROFILE}      bedrock


*** Test Cases ***
Setup Writes A Working Profile
    [Documentation]    bedrock setup --id finds the personal role from the SSO email, as the setup
    ...    command apply prints for a setup that isn't the default id.
    Should Be Equal    ${SETUP_COMMAND}    bedrock setup --id ${ID}
    ${r}=    Bedrock    setup    --source-profile    ${SSO_PROFILE}    --id    ${ID}    --region    ${REGION}
    ...    --profile-name    ${PROFILE}    output=result    rc=any
    Should Be Equal As Integers    ${r.rc}    0    setup or its doctor failed:\n${r.stdout}\n${r.stderr}
    ${text}=    User Aws Config Text
    Should Contain    ${text}    [profile ${PROFILE}]
    Should Contain    ${text}    role_session_name = ${SSO_EMAIL}
    Should Contain    ${text}    source_profile = ${SSO_PROFILE}
    Should Contain    ${text}    role_arn = ${ROLE_ARN}

The Personal Role Works
    ${arn}=    Identity    ${PROFILE}
    ${role}=    Role Of    me
    Should Contain    ${arn}    assumed-role/${role}/${SSO_EMAIL}
    FOR    ${m}    IN    @{MODELS}
        Model Call Should Work    ${PROFILE}    ${m}
    END

Baseline Models Need Not Be Listed
    [Documentation]    A set models: always allows the built-in baseline models (models.toml) too.
    ${m}=    Baseline Only Model
    Skip If    not $m    MODELS lists every baseline model
    Model Call Should Work    ${PROFILE}    ${m}

Only Listed Models Can Be Called
    [Documentation]    models: is an allow-list: a Claude model neither the file nor the baseline lists is
    ...    denied by IAM (no Allow, so not an explicit deny). A denied call costs nothing.
    ${m}=    Unlisted Model
    Skip If    not $m    MODELS or the baseline lists every model this test could use
    Model Call Should Be Denied    ${PROFILE}    ${m}    explicit=False

Doctor Passes
    ${d}=    Bedrock    doctor    --profile-name    ${PROFILE}    --json
    Should Be True    $d["ok"]

Usage Reads The User's Own Budget
    [Documentation]    The personal role may read its own budget and tags.
    ${u}=    Bedrock    usage    --profile-name    ${PROFILE}    --json
    Should Be Equal    ${u["state"]}    active
    Should Be True    $u["limit_usd"] > 0

Models Lists The Role's Allowed Models
    [Documentation]    bedrock models reads the personal role's own policy: the run's models: and the
    ...    region's baseline models, without other geographies' profiles.
    ${m}=    Bedrock    models    --profile-name    ${PROFILE}    --json
    Should Be Equal    ${m["source"]}    role
    Should Not Be True    $m["any_claude"]
    ${allowed}=    Allowed Models
    Lists Should Be Equal    ${m["models"]}    ${allowed}    ignore_order=True

Direct SSO Calls Are Denied
    [Documentation]    Step 1.7. Skipped with a note when the org admin hasn't applied the deny to the
    ...    runner's permission set in the test account.
    ${denied}=    Model Call Is Denied    sso
    Skip If    not ${denied}    Step 1.7 isn't applied to permission set ${SSO_PERMISSION_SET}: direct SSO calls work
    Model Call Should Be Denied    sso    explicit=True

A Manual Pause Denies Calls And Unpause Restores Them
    Admin    pause    ${SSO_EMAIL}    --reason    e2e user-access
    Wait Until Keyword Succeeds    3 min    10 s    Model Call Should Be Denied    ${PROFILE}    explicit=True
    ${d}=    Bedrock    doctor    --profile-name    ${PROFILE}    --json    rc=2
    ${fixes}=    Evaluate    " ".join(c.get("fix", "") for c in $d["checks"] if not c["ok"])
    Should Contain    ${fixes}    paused    ignore_case=True
    ${u}=    Bedrock    usage    --profile-name    ${PROFILE}    --json
    Should Be Equal    ${u["state"]}    paused
    Should Contain    ${u["detail"]}    e2e user-access
    Admin    unpause    ${SSO_EMAIL}
    Wait Until Keyword Succeeds    3 min    10 s    Model Call Should Work    ${PROFILE}
    [Teardown]    Admin    unpause    ${SSO_EMAIL}    rc=any


*** Keywords ***
Start User Access Run
    Start AWS Run
    Start SSO
    Add User    me
    ${r}=    Apply
    ${cmds}=    Evaluate    [c["command"] for c in $r.json["setup_commands"] if c["email"] == $SSO_EMAIL]
    Set Suite Variable    ${SETUP_COMMAND}    ${cmds}[0]
    ${arn}=    Role Arn Of    me
    Set Suite Variable    ${ROLE_ARN}    ${arn}
    Wait Until Sso Can Assume    ${ROLE_ARN}
    ${ps}=    Get Sso Permission Set
    Set Suite Variable    ${SSO_PERMISSION_SET}    ${ps}
