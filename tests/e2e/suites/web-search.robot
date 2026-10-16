*** Settings ***
Documentation       Web search (Step 1.8) in the run's own setup (id e2e<hex>): turning web_search on creates
...                 the setup's gateway and its role and gives personal roles the gateway rights, and a role
...                 can search; the pause denies search; turning it off removes the rights, the gateway and
...                 its role. With SSO_PROFILE set, also the user side: bedrock doctor, and the MCP server
...                 printed by bedrock claude run through uvx and mcp-proxy-for-aws, also for a profile in another region.
...                 REGION must be us-east-1, eu-west-1 or ap-northeast-1. A run costs a few cents.
Resource            ../resources/common.resource
Suite Setup         Start Web Search Run
Suite Teardown      Tear Down Run
Test Tags           web-search


*** Variables ***
${PROFILE}      bedrock


*** Test Cases ***
Apply Creates The Gateway And Gives A Personal Role The Rights
    Set Config    web_search.enabled    true
    ${p}=    Plan
    Plan Op Should Be    ${p}    web search gateway ${N}[web_search_gateway]    create
    Plan Op Should Be    ${p}    web search role ${N}[web_search_role]    create
    Apply
    ${role}=    Role Of    a
    ${doc}=    Role Policy Text    ${role}
    Should Contain    ${doc}    bedrock-agentcore:InvokeGateway
    ${url}=    Web Search Should Be Ready
    Set Suite Variable    ${URL}    ${url}
    ${p}=    Plan
    Plan Should Have No Changes    ${p}

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
    ${arn}=    Role Arn Of    me
    Wait Until Sso Can Assume    ${arn}
    Bedrock    setup    --sso-profile    ${SSO_PROFILE}    --id    ${ID}    --region    ${REGION}
    ...    --profile-name    ${PROFILE}    --yes
    ${d}=    Bedrock    doctor    --profile-name    ${PROFILE}    --json
    ${names}=    Evaluate    [c["name"] for c in $d["checks"] if c["ok"]]
    Should Contain    ${names}    web search
    ${out}=    Bedrock    claude    --profile-name    ${PROFILE}    --no-test
    ${json}=    Get Regexp Matches    ${out}    claude mcp add-json web-search '([^']+)'    1
    Should Not Be Empty    ${json}    bedrock claude printed no MCP server:\n${out}
    Should Contain    ${json}[0]    ${URL}
    Mcp Proxy Should List Web Search    ${json}[0]

A User In Another Region Finds The Gateway
    [Documentation]    A profile in a region other than the gateway's: bedrock claude still prints the
    ...    gateway, with the gateway's region in --region.
    Skip If    not $SSO_PROFILE    SSO_PROFILE isn't set
    ${other}=    Set Variable If    $REGION == "eu-central-1"    us-west-2    eu-central-1
    Bedrock    setup    --sso-profile    ${SSO_PROFILE}    --id    ${ID}    --region    ${other}
    ...    --profile-name    ${PROFILE}-other    --yes    rc=any
    ${out}=    Bedrock    claude    --profile-name    ${PROFILE}-other    --no-test    --no-search-test
    ${json}=    Get Regexp Matches    ${out}    claude mcp add-json web-search '([^']+)'    1
    Should Not Be Empty    ${json}    bedrock claude printed no MCP server:\n${out}
    Should Contain    ${json}[0]    ${URL}
    Should Contain    ${json}[0]    "--region","${REGION}"

Turning It Off Removes The Rights And The Gateway
    Set Config    web_search.enabled    false
    ${p}=    Plan
    Plan Op Should Be    ${p}    web search gateway ${N}[web_search_gateway]    delete
    Plan Op Should Be    ${p}    web search role ${N}[web_search_role]    delete
    Apply
    ${role}=    Role Of    a
    ${doc}=    Role Policy Text    ${role}
    Should Not Contain    ${doc}    bedrock-agentcore:InvokeGateway
    Web Search Gateway Should Not Exist
    Role Should Not Exist    ${N}[web_search_role]
    ${p}=    Plan
    Plan Should Have No Changes    ${p}


*** Keywords ***
Start Web Search Run
    Start Run
    Web Search Region Should Be Supported
    Add User    a
    Set Up Account

