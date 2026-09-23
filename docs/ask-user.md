# Durable decision cards

The host exposes POST `/ask-user` on its existing private Unix socket. The body
is `{project, token, spec}`; `token` comes from `CC_ASK_USER_TOKEN`, injected with
`CC_PROJECT`, `CC_SESSION_KEY` and `CC_DATA_DIR` into a live Agent process. It
resolves a host-recorded origin, not a model-selected session. Agent Runtime
exposes this endpoint through its `ask_user` MCP tool.

`spec` contains title, Markdown, 1–6 `{id,label}` options, `allow_comment`, an
optional exact recipient email/app-scoped open_id and `expires_in_hours` (default
120). The Feishu/Lark adapter renders a form, sends via the existing bot API and
handles `decision:submit` through the existing SDK card-action event dispatcher.
A submitted option and comment are saved before the callback returns success.
The platform-neutral `DecisionPlatform` interface keeps SDK code out of core.

The tool is asynchronous. On pending, the Agent must end the current turn.
Answers are later delivered as a new turn in the SAME session, after it becomes
idle. The original sender identity is retained for processing hooks; the real
answerer's open_id is explicit data, not a replacement JWT identity. Native
AskUserQuestion permission handling is not changed.

State is stored per request in `<session-store-path>.decisions/`, using atomic
rename and fsync. Keep both this directory and Agent session files on persistent
storage for Pod-replacement recovery. This follows the host's single-owner
session model; multiple processes must not write the same store. There is no new
LTS database schema or external callback service.

Duplicate clicks are idempotent. Unknown/stale cards, wrong recipients, unknown
options and expired requests fail closed. Session reset/deletion and a changed
backend session produce `session_unavailable`. A crash during card send produces
`send_unknown`; during Agent handoff it produces `delivery_unknown`. These states
retain the answer and do not automatically replay potentially executed work.
`delivered` acknowledges Agent transport, not business success. A persisted
`answered` request resumes automatically after restart when its session is free.

Each normal native card send has a deterministic Lark UUID. Failed sends surface
an error; no hot retry loop is added. No new reminder, LTS work item, audit or
production data update is performed. Expiry never means approval.
