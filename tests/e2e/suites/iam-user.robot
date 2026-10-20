*** Settings ***
Documentation       A user who signs in with an IAM user's access key instead of SSO: apply tags the IAM
...                 user owner like the personal role (so the budget counts its spend), attaches
...                 bedrock-iam-users-<id> (Step 1.9) and adds it to the pause action; bedrock setup
...                 --source-profile reads the email from the owner tag and writes a working profile;
...                 the access key itself can't call models; a pause denies the IAM user too; and taking
...                 iam_user out of the file releases the IAM user.
...                 Needs only the admin profile: the run creates its own IAM user and access key and
...                 deletes them at the end. Makes one small paid model call.
Resource            ../resources/common.resource
Suite Setup         Start IAM User Run
Suite Teardown      Tear Down IAM User Run
Test Tags           iam-user


*** Variables ***
${PROFILE}      bedrock


*** Test Cases ***
Apply Onboards The IAM User
    [Documentation]    The IAM user gets the owner, product and bedrock-admin:id tags and the setup's IAM
    ...    user policy, the pause action denies it with the role, and the setup command tells the
    ...    user to pass their key profile.
    Iam Users Policy Should Exist
    ${email}=    User Email    iam
    Iam User Should Be Onboarded    ${IAM_USER}    ${email}
    Budget Action Iam Users Should Be    iam    ${IAM_USER}
    Should Contain    ${SETUP_COMMAND}    --source-profile
    Plan Should Show No Changes

Setup With The Access Key Writes A Working Profile
    [Documentation]    setup reads the email from the IAM user's own tags; its doctor assumes the
    ...    personal role through the trust's IAMUser statement (retried while IAM propagates).
    Wait Until Access Key Works    ${KEY_PROFILE}
    Wait Until Keyword Succeeds    3 min    10 s    Setup With Key Profile
    ${email}=    User Email    iam
    ${text}=    User Aws Config Text
    Should Contain    ${text}    role_session_name = ${email}
    Should Contain    ${text}    source_profile = ${KEY_PROFILE}
    Should Contain    ${text}    role_arn = ${ROLE_ARN}

The Personal Role Works
    ${email}=    User Email    iam
    ${role}=    Role Of    iam
    ${arn}=    Identity    ${PROFILE}
    Should Contain    ${arn}    assumed-role/${role}/${email}
    Model Call Should Work    ${PROFILE}

Direct Calls With The Access Key Are Denied
    [Documentation]    Only the personal role, with its budget and pause, may call models.
    Model Call Should Be Denied    ${KEY_PROFILE}    explicit=False

A Pause Denies The IAM User Too
    [Documentation]    pause attaches bedrock-deny to the role and the IAM user, so the access key is
    ...    explicitly denied whatever other policies it has; unpause detaches both.
    ${role}=    Role Of    iam
    ${email}=    User Email    iam
    ${r}=    Admin    pause    ${email}    --reason    e2e test    --json
    Should Be Equal    ${r}[result]    paused
    Deny Policy Should Be Attached    ${role}
    Deny Policy Should Be Attached To Iam User    ${IAM_USER}
    Wait Until Keyword Succeeds    2 min    10 s    Model Call Should Be Denied    ${KEY_PROFILE}
    ${r}=    Admin    unpause    ${email}    --reason    done    --json
    Should Be Equal    ${r}[result]    removed-manual-pause
    Deny Policy Should Not Be Attached    ${role}
    Deny Policy Should Not Be Attached To Iam User    ${IAM_USER}

Removing iam_user Releases The IAM User
    [Documentation]    The IAM user is detached, untagged and taken out of the pause action, but kept
    ...    (it may be used for other things); the policy goes with the last IAM user, and the role
    ...    no longer admits the key.
    Unset User    iam    iam_user
    ${p}=    Plan
    ${i}=    Plan Item    ${p}    IAM user ${IAM_USER}    users
    Should Be Equal    ${i}[op]    delete
    Should Be Equal    ${i}[needs_yes]    ${True}
    Apply
    Iam User Should Be Released    ${IAM_USER}
    Budget Action Iam Users Should Be    iam
    Iam Users Policy Should Not Exist
    Plan Should Show No Changes
    Wait Until Keyword Succeeds    3 min    10 s    Doctor Should Fail To Use The Role


*** Keywords ***
Start IAM User Run
    Start AWS Run
    ${u}=    Create Test Iam User    iam
    Set Suite Variable    ${IAM_USER}    ${u}[0]
    Set Suite Variable    ${KEY_PROFILE}    ${u}[1]
    Add User    iam    iam_user=${IAM_USER}
    ${r}=    Apply
    ${email}=    User Email    iam
    ${cmds}=    Evaluate    [c["command"] for c in $r.json["setup_commands"] if c["email"] == $email]
    Set Suite Variable    ${SETUP_COMMAND}    ${cmds}[0]
    ${arn}=    Role Arn Of    iam
    Set Suite Variable    ${ROLE_ARN}    ${arn}

Tear Down IAM User Run
    Run Keyword And Ignore Error    Tear Down Run
    Delete Test Iam Users

Setup With Key Profile
    ${r}=    Bedrock    setup    --source-profile    ${KEY_PROFILE}    --id    ${ID}    --region    ${REGION}
    ...    --profile-name    ${PROFILE}    --yes    output=result    rc=any
    Should Be Equal As Integers    ${r.rc}    0    setup or its doctor failed:\n${r.stdout}\n${r.stderr}

Doctor Should Fail To Use The Role
    ${d}=    Bedrock    doctor    --profile-name    ${PROFILE}    --json    rc=2
    ${failed}=    Evaluate    [c["name"] for c in $d["checks"] if not c["ok"]]
    Should Contain    ${failed}    permission-set access
