*** Settings ***
Documentation       Web search (Step 1.8) for personal roles: with web_search on, apply gives a role the gateway
...                 rights and it can search; the pause denies search; turning web_search off removes the
...                 rights. With SSO_PROFILE set, also the user side: bedrock doctor, and the MCP server printed
...                 by bedrock claude run through uvx and mcp-proxy-for-aws.
...                 Uses its own id (e2e-<run>) and the gateway of the account setup, so the account setup must
...                 have web_search on (in REGION: us-east-1, eu-west-1 or ap-northeast-1). The gateway itself
...                 (create, update, delete) belongs to the account setup's id and isn't changed here.
...                 A run costs a few cents.
Resource            ../resources/common.resource
Suite Setup         Start Web Search Run
Suite Teardown      Remove Run Users
Test Tags           web-search


*** Variables ***
${PROFILE}      bedrock


*** Test Cases ***
Apply Gives A Personal Role The Gateway Rights
    Set Config    web_search.enabled    true
    ${p}=    Plan
    ${gw}=    Plan Op Should Be    ${p}    web search gateway    info
    Should Contain    ${gw}[status]    managed by ${OWNER}
    Apply
    ${role}=    Role Of    a
    ${doc}=    Role Policy Text    ${role}
    Should Contain    ${doc}    bedrock-agentcore:InvokeGateway
    ${url}=    Web Search Should Be Ready    ${OWNER}
    Should Be Equal    ${url}    ${URL}

A Personal Role Can Search
    Let Admin Assume    a
    Wait Until Keyword Succeeds    3 min    15 s    Web Search Should Work    user:a    ${URL}

The Pause Denies Search
    ${email}=    User Email    a
    Admin    pause    ${email}    --reason    e2e web-search
    Wait Until Keyword Succeeds    3 min    10 s    Web Search Should Be Denied    user:a    ${URL}
    Admin    unpause    ${email}
    Wait Until Keyword Succeeds    3 min    15 s    Web Search Should Work    user:a    ${URL}
    [Teardown]    Admin    unpause    ${email}    rc=any

The User CLI Finds And Uses Web Search
    [Documentation]    bedrock setup, bedrock doctor (uvx and tools/list), then the MCP server that
    ...    bedrock claude prints, run as Claude Code would run it.
    Skip If    not $SSO_PROFILE    SSO_PROFILE isn't set
    Start SSO
    Add User    me
    Apply
    ${role}=    Role Of    me
    ${arn}=    Set Variable    arn:aws:iam::${ACCOUNT}:role/bedrock-users/${role}
    Wait Until Sso Can Assume    ${arn}
    Bedrock    setup    --sso-profile    ${SSO_PROFILE}    --role-arn    ${arn}    --region    ${REGION}
    ...    --profile-name    ${PROFILE}    --yes
    ${d}=    Bedrock    doctor    --profile-name    ${PROFILE}    --json
    ${names}=    Evaluate    [c["name"] for c in $d["checks"] if c["ok"]]
    Should Contain    ${names}    web search
    ${out}=    Bedrock    claude    --profile-name    ${PROFILE}    --no-test
    ${json}=    Get Regexp Matches    ${out}    claude mcp add-json web-search '([^']+)'    1
    Should Not Be Empty    ${json}    bedrock claude printed no MCP server:\n${out}
    Should Contain    ${json}[0]    ${URL}
    Mcp Proxy Should List Web Search    ${json}[0]

Turning It Off Removes The Rights But Not The Gateway
    Set Config    web_search.enabled    false
    Apply
    ${role}=    Role Of    a
    ${doc}=    Role Policy Text    ${role}
    Should Not Contain    ${doc}    bedrock-agentcore:InvokeGateway
    Wait Until Keyword Succeeds    3 min    10 s    Web Search Should Be Denied    user:a    ${URL}
    ${url}=    Web Search Should Be Ready    ${OWNER}
    Should Be Equal    ${url}    ${URL}


*** Keywords ***
Start Web Search Run
    Start Run
    Web Search Region Should Be Supported
    ${owner}=    Account Resource Owner
    IF    not $owner
        Fail    The account isn't set up: run ./run.sh account first
    END
    Set Suite Variable    ${OWNER}    ${owner}
    ${url}=    Web Search Should Be Ready    ${owner}
    Set Suite Variable    ${URL}    ${url}
    Log    Account setup and web search gateway managed by id ${owner}    console=True
    Add User    a
