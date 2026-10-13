*** Settings ***
Documentation       Shared keywords. The library (lib/BedrockE2E.py) runs the CLIs and reads AWS directly.
Library             Collections
Library             OperatingSystem
Library             String
Library             ../lib/BedrockE2E.py


*** Keywords ***
Start AWS Run
    [Documentation]    Starts a run that uses the admin profile and applies its own setup (id ${ID}).
    ...    Pass an id to use it instead of the run id. Tear Down Run removes the setup again.
    [Arguments]    ${id}=${EMPTY}
    Start Run
    IF    $id    Use Id    ${id}
    Set Up Account

Set Up Account
    [Documentation]    Applies the run's config. Fails on conflicts (for example a leftover setup
    ...    of the same id, made outside bedrock-admin).
    ${r}=    Apply    rc=any
    Should Be True    ${r.rc} in (0, 2)    apply failed:\n${r.stdout}\n${r.stderr}
    ${conflicts}=    Evaluate    [i["target"] + ": " + i["status"] for i in ($r.json.get("pending") or []) if i["op"] == "conflict"]
    Should Be Empty    ${conflicts}    apply has conflicts: ${conflicts}

Tear Down Run
    [Documentation]    Offboards the run's users and uninstalls its setup with the bucket
    ...    (KEEP_SETUP=True keeps everything, to look at it after a failure).
    ${keep}=    Keep Setup
    IF    ${keep}
        Log    KEEP_SETUP: setup ${ID} left in place; remove it with bedrock-admin uninstall --delete-data    console=True
        RETURN
    END
    Set Users
    Run Keyword And Ignore Error    Admin    apply    --yes
    Admin    uninstall    --yes    --delete-data

Apply
    [Documentation]    apply --yes --json; returns the result (rc, stdout, stderr, json).
    [Arguments]    @{args}    ${rc}=0    ${config}=${EMPTY}
    ${r}=    Admin    apply    --yes    --json    @{args}    rc=${rc}    config=${config}    output=result
    RETURN    ${r}

Plan
    [Arguments]    @{args}    ${rc}=any
    ${p}=    Admin    plan    --json    @{args}    rc=${rc}
    RETURN    ${p}

User Plan Op Should Be
    [Arguments]    ${x}    ${op}
    ${p}=    Plan
    ${email}=    User Email    ${x}
    ${item}=    Plan Op Should Be    ${p}    ${email}    ${op}
    RETURN    ${item}

Plan Should Show No Changes
    ${p}=    Plan    rc=0
    Plan Should Have No Changes    ${p}

User Should Be Set Up
    [Documentation]    Role, budget and pause action exist, with the given threshold and alerts.
    [Arguments]    ${x}    ${limit}    ${pause_at}=100    @{alerts}
    ${role}=    Role Of    ${x}
    ${budget}=    Budget Of    ${x}
    ${email}=    User Email    ${x}
    Role Should Exist    ${role}
    Role Tag Should Be    ${role}    owner    ${email}
    Role Tag Should Be    ${role}    bedrock-admin:id    ${ID}
    Role Should Have Trust For    ${role}    ${email}
    Budget Limit Should Be    ${budget}    ${limit}
    Budget Alerts Should Be    ${budget}    @{alerts}
    Budget Action Should Be    ${budget}    threshold=${pause_at}    status=STANDBY
    Deny Policy Should Not Be Attached    ${role}

User Should Be Gone
    [Arguments]    ${x}
    ${role}=    Role Of    ${x}
    ${budget}=    Budget Of    ${x}
    Role Should Not Exist    ${role}
    Budget Should Not Exist    ${budget}

Text Should Contain Lines
    [Arguments]    ${text}    @{lines}
    FOR    ${l}    IN    @{lines}
        Should Contain    ${text}    ${l}
    END
