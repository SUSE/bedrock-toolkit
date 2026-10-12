*** Settings ***
Documentation       What a user does: bedrock setup, calls through the personal role, bedrock doctor and usage, the
...                 Step 1.7 block on direct SSO calls, and a manual pause seen from the user's side.
...                 Needs SSO_PROFILE signed in (aws sso login) in the same account. The runner is added as
...                 a user named <run>-me; the user CLI writes to a copy of ~/.aws/config, never the real one.
Resource            ../resources/common.resource
Suite Setup         Start User Access Run
Suite Teardown      Remove Run Users
Test Tags           user-access


*** Variables ***
${PROFILE}      bedrock


*** Test Cases ***
Setup Writes A Working Profile
    ${r}=    Bedrock    setup    --sso-profile    ${SSO_PROFILE}    --role-arn    ${ROLE_ARN}    --region    ${REGION}
    ...    --profile-name    ${PROFILE}    output=result    rc=any
    Should Be Equal As Integers    ${r.rc}    0    setup or its doctor failed:\n${r.stdout}\n${r.stderr}
    ${text}=    User Aws Config Text
    Should Contain    ${text}    [profile ${PROFILE}]
    Should Contain    ${text}    role_session_name = ${SSO_EMAIL}
    Should Contain    ${text}    source_profile = ${SSO_PROFILE}

The Personal Role Works
    ${arn}=    Identity    ${PROFILE}
    ${role}=    Role Of    me
    Should Contain    ${arn}    assumed-role/${role}/${SSO_EMAIL}
    FOR    ${m}    IN    @{MODELS}
        Model Call Should Work    ${PROFILE}    ${m}
    END

Only Listed Models Can Be Called
    [Documentation]    models: is an allow-list: a Claude model the file doesn't list is denied by IAM
    ...    (no Allow, so not an explicit deny). A denied call costs nothing.
    ${m}=    Unlisted Model
    Skip If    not $m    MODELS lists every model this test could use
    Model Call Should Be Denied    ${PROFILE}    ${m}    explicit=False

Doctor Passes
    ${d}=    Bedrock    doctor    --profile-name    ${PROFILE}    --json
    Should Be True    $d["ok"]

Usage Reads The User's Own Budget
    [Documentation]    The personal role may read its own budget and tags.
    ${u}=    Bedrock    usage    --profile-name    ${PROFILE}    --json
    Should Be Equal    ${u["state"]}    active
    Should Be True    $u["limit_usd"] > 0

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
    Should Contain    ${fixes}    paused
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
    Apply
    ${role}=    Role Of    me
    Set Suite Variable    ${ROLE_ARN}    arn:aws:iam::${ACCOUNT}:role/bedrock-users/${role}
    Wait Until Sso Can Assume    ${ROLE_ARN}
    ${ps}=    Get Sso Permission Set
    Set Suite Variable    ${SSO_PERMISSION_SET}    ${ps}
