# Slack Setup Guide

This guide walks you through connecting **cc-connect** to Slack, so you can chat with your local Claude Code via a Slack bot.

## Prerequisites

- A Slack workspace account (with permission to create apps)
- A machine that can run cc-connect (no public IP needed)
- Claude Code installed and configured

> 💡 **Advantage**: Uses Socket Mode (WebSocket) — no public IP, no domain, no reverse proxy needed.

---

## Step 1: Create a Slack App

### 1.1 Open the Slack API Console

Go to [Slack API](https://api.slack.com/apps) and sign in with your Slack account.

### 1.2 Create a New App

1. Click "Create New App"
2. Select "From scratch"
3. Fill in the app details:

| Field | Suggested Value |
|-------|----------------|
| App Name | `cc-connect` |
| Development Slack Workspace | Select your workspace |

4. Click "Create App"

---

## Step 2: Configure Bot User

### 2.1 Go to App Home

In the left sidebar, click "App Home".

### 2.2 Set Bot Info

1. Click "Edit" to configure the bot display name
2. Fill in:

| Field | Suggested Value |
|-------|----------------|
| Display Name (Bot Name) | `cc-connect` |
| Default Username | `cc_connect` |

### 2.3 Always Show Bot Online

Toggle on "Always Show My Bot as Online".

---

## Step 3: Configure Permissions (OAuth Scopes)

### 3.1 Go to OAuth & Permissions

In the left sidebar, click "OAuth & Permissions".

### 3.2 Add Bot Token Scopes

Under "Scopes" → "Bot Token Scopes", add:

| Scope | Purpose |
|-------|---------|
| `app_mentions:read` | Read @mention messages |
| `chat:write` | Send messages |
| `im:history` | Read DM history |
| `im:read` | Read DM list |
| `im:write` | Send DMs |
| `channels:history` | Read channel messages (optional) |
| `groups:history` | Read private channel messages (optional) |
| `users:read` | Get user info |
| `users:read.email` | Get user email (optional; only needed when `include_user_email = true`) |

---

## Step 4: Enable Socket Mode

### 4.1 Go to Socket Mode Settings

In the left sidebar, click "Socket Mode".

### 4.2 Enable Socket Mode

1. Toggle on "Enable Socket Mode"
2. Click "Generate Token and Enter Socket Mode"

### 4.3 Generate App-Level Token

1. Enter a token name (e.g. `cc-connect-socket-token`)
2. Add the following scope:
   - `connections:write` — establish WebSocket connections
3. Click "Generate"

### 4.4 Save the Token

The system will generate an App-Level Token (format: `xapp-xxxxxxx...`). Save it immediately.

> ⚠️ The token is only shown once — copy it now!

---

## Step 5: Configure Event Subscriptions

### 5.1 Go to Event Subscriptions

In the left sidebar, click "Event Subscriptions".

### 5.2 Enable Events

1. Toggle on "Enable Events"
2. Since we're using Socket Mode, no Request URL is needed

### 5.3 Subscribe to Bot Events

Under "Subscribe to bot events", add:

| Event | Purpose |
|-------|---------|
| `app_mention` | Triggered when the bot is @mentioned |
| `message.im` | Triggered when a DM is received |
| `message.channels` | Channel thread follow-ups (recommended with default `thread_reply_without_mention`) |
| `message.groups` | Same for private channels (optional) |

### 5.4 Save Changes

Click "Save Changes".

---

## Step 6: Install App to Workspace

### 6.1 Install the App

In the left sidebar, click "Install App" → "Install to Workspace".

### 6.2 Authorize

Review the permissions and click "Allow".

### 6.3 Get the Bot Token

After installation, you'll see:

```
Bot User OAuth Token: xoxb-xxxxxxx...
```

> ⚠️ Save this token — you'll need it for configuration.

---

## Step 7: Configure cc-connect

Add both tokens to your `config.toml`:

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"

[[projects.platforms]]
type = "slack"

[projects.platforms.options]
bot_token = "xoxb-xxxxxxx..."
app_token = "xapp-xxxxxxx..."
# Optional: override Slack Web API base URL for proxy/custom deployments.
# Accepts either "https://slack.example.com" or "https://slack.example.com/api/".
api_url = "https://slack.example.com/api/"
# inject_mentioned_users = true  # default: inject refs for inbound <@USER_ID> mentions
# include_user_email = false     # default: false; requires users:read.email when enabled
# group_reply_all = false        # default: channel messages require @mention (via app_mention event)
# require_mention = true         # alias: require_mention = false is the same as group_reply_all = true
# thread_reply_without_mention = true  # default: follow-ups in a bot-engaged thread need no @mention
# thread_active_ttl_hours = 72         # default: active threads survive restarts for 72 hours
# dedup_ttl_seconds = 60               # default: inbound dedup window for duplicate Slack events
# state_dir = ""                       # optional override for thread state storage directory
```

By default, the bot responds to **DMs**, **@mentions in channels**, and **follow-up messages in the same thread** after an `@mention` started the conversation. Set `thread_reply_without_mention = false` to require `@mention` on every channel message.

To respond to every top-level channel message without `@mention`, set `group_reply_all = true` (or `require_mention = false`) and subscribe to `message.channels` in your Slack app.

When `thread_reply_without_mention` is enabled (default), also subscribe to `message.channels` and `message.groups` (private channels) so thread follow-ups are delivered.

Active thread state is persisted under `<data_dir>/slack/<project>/state.json` and restored on restart while still within `thread_active_ttl_hours` (default 72h). Inbound duplicate events (`app_mention` + `message` for the same post) are deduplicated in memory for `dedup_ttl_seconds` (default 60s).

### Token Reference

| Token | Prefix | Purpose |
|-------|--------|---------|
| Bot Token | `xoxb-` | Bot API authentication |
| App Token | `xapp-` | Socket Mode connection |

---

## Step 8: Start cc-connect

### 8.1 Launch

```bash
cc-connect
# Or specify a config file
cc-connect -config /path/to/config.toml
```

### 8.2 Verify Connection

You should see logs like:

```
level=INFO msg="slack: connected"
level=INFO msg="platform started" project=my-project platform=slack
level=INFO msg="cc-connect is running" projects=1
```

---

## Step 9: Start Chatting

### 9.1 Direct Message

1. Search for your bot name in Slack
2. Open a DM conversation
3. Send a message

### 9.2 Channel Usage

1. Add the bot to a channel (`/invite @cc_connect`)
2. @mention the bot: `@cc_connect help me analyze the code`
3. The bot will respond

---

## Usage Example

```
User: @cc_connect Help me analyze the current project structure

cc-connect: 🤔 Thinking...
cc-connect: 🔧 Tool: Bash(ls -la)
cc-connect: Here's the project structure...
```

---

## Interactive Command UI

Commands such as `/help`, `/list`, `/model`, `/mode`, and `/lang` render as **Block Kit** messages with buttons and dropdowns instead of plain text. Clicking a button updates the original message in place (same behavior as Feishu interactive cards).

Requirements:

- **Interactivity** must be enabled in your Slack app settings (included in `docs/slack-app-manifest.json`)
- Socket Mode must be running so `block_actions` callbacks reach cc-connect

Delete-mode multi-select uses toggle buttons on Slack (Feishu uses native checkbox forms).

Platform UI strings (loading toasts, ask-question confirmations) follow the user's language. Detection priority: explicit `/lang`, natural-language message content, session cache, then Slack `users.info` `locale` (client language setting, not display name). Supported locales match the engine (`en`, `zh`, `zh-TW`, `ja`, `es`).

## Slack Mentions and User Context

Slack stores user mentions in message text as `<@USER_ID>`. cc-connect keeps that original text so outgoing replies can mention the same person with Slack's native syntax, and by default injects a compact reference for every mentioned user:

```text
[cc-connect slack_mention id=U123ABC name="Hory Zhao"]
```

When project-level `inject_sender = true` is enabled, the sender is also injected through the common sender header:

```text
[cc-connect sender_id=U999 sender_name="Rock" platform=slack chat_id=C123]
```

When `inject_timestamp = true` is enabled, each agent message also includes the current time in the user's timezone (Slack profile timezone, or `default_timezone` fallback):

```text
[cc-connect timestamp="2026-06-10T15:30:00+08:00" timezone="Asia/Shanghai" sender_id=U999 ...]
hello
```

The agent should mention Slack users with `<@USER_ID>` when replying. If your organization provides a tool that maps email/name to Slack user ID, expose that tool to the agent and let it return the ID to use in `<@...>`.

Email injection is disabled by default. To include emails for the sender and mentioned users, set:

```toml
[projects.platforms.options]
include_user_email = true
```

Then add the Slack bot scope `users:read.email` and reinstall/re-authorize the Slack app. If Slack does not return an email, cc-connect omits the `email` field and continues normally.

Hooks receive the same information in structured form. HTTP hooks get the sender email as top-level `user_email`; command hooks and custom exec commands get `CC_HOOK_USER_EMAIL`. Mentioned users are exposed through hook context as `ctx.slack_mentions` (or `CC_HOOK_CTX_JSON` for command hooks and custom exec commands):

```json
{
  "user_email": "rock@example.com",
  "ctx": {
    "slack_mentions": [
      {
        "id": "U123ABC",
        "name": "Hory Zhao",
        "email": "hory.zhao@example.com"
      }
    ]
  }
}
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                       Slack Cloud                            │
│                                                              │
│   User Message ──→ Slack API ──→ Socket Mode Gateway         │
│                                       │                      │
└───────────────────────────────────────┼──────────────────────┘
                                        │
                                        │ WebSocket (no public IP needed)
                                        ▼
┌─────────────────────────────────────────────────────────────┐
│                    Your Local Machine                         │
│                                                              │
│   cc-connect ◄──► Claude Code CLI ◄──► Your Project Code    │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## Socket Mode vs Webhook

| Feature | Socket Mode | Webhook |
|---------|-------------|---------|
| Public IP | ❌ Not needed | ✅ Required |
| Domain | ❌ Not needed | ✅ Required |
| HTTPS cert | ❌ Not needed | ✅ Required |
| Reverse proxy | ❌ Not needed | ✅ Required |
| Connection | WebSocket | HTTP callback |
| Complexity | Simple | More complex |
| Best for | Local dev, private network | Production |

---

## FAQ

### Q: Socket Mode connection fails?

Check the following:
1. Is the App Token correct? (starts with `xapp-`)
2. Does the App Token have `connections:write` scope?
3. Is Socket Mode enabled in the app settings?

### Q: Bot doesn't respond to messages?

Check the following:
1. Is the Bot Token correct? (starts with `xoxb-`)
2. Are event subscriptions configured correctly?
3. Are the required scopes added?

### Q: Changes to permissions don't take effect?

**⚠️ Important**: After modifying scopes or events, you must reinstall the app!

1. Go to "Install App"
2. Click "Reinstall to Workspace"

### Q: Bot doesn't respond in DMs?

Make sure you've subscribed to the `message.im` event.

### Q: Bot doesn't respond in channels?

Make sure:
1. You've subscribed to the `app_mention` event
2. The bot has been added to the channel
3. You @mentioned the bot in your message

---

## References

- [Slack API Documentation](https://api.slack.com/)
- [Slack App Building Guide](https://api.slack.com/start/building)
- [Socket Mode Documentation](https://api.slack.com/apis/connections/socket)
- [Bot Token Scopes](https://api.slack.com/scopes)
- [Event Types](https://api.slack.com/events)

---

## See Also

- [Feishu Setup](./feishu.md)
- [DingTalk Setup](./dingtalk.md)
- [Weibo Setup](./weibo.md)
- [Telegram Setup](./telegram.md)
- [Discord Setup](./discord.md)
- [Back to README](../README.md)
