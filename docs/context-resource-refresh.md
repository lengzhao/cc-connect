# Per-conversation context refresh

Claude Code sessions load appended system-prompt files when their long-running
process starts. Automon instructions, Skills, Memory, and Knowledge can change
while that process and conversation remain active, so cc-connect keeps a
content-version checkpoint on every conversation.

Tracked resources:

- files configured by `append_system_prompt_files` (including `AUTOMON.md`);
- discovered `.claude/skills/<skill>/SKILL.md` files;
- `<workspace>/files/memory/**`;
- `<workspace>/files/knowledge/**`.

On the first accepted user turn, cc-connect records the current content hashes.
Before later turns it compares the current hashes with that conversation's
checkpoint. Additions, updates, and deletions produce a hidden prompt notice:

- changed Automon/runtime-instruction files must be re-read before answering;
- changed Skills, Memory, and Knowledge are read only when relevant;
- only paths and timestamps are included, not the full file contents.

The original user message remains unchanged in visible cc-connect history. The
checkpoint advances only after the agent process accepts the prompt; a failed
send keeps the changes pending for the next retry. Checkpoints are persisted in
both legacy JSON and channel-sharded JSONL session stores.

No new configuration is required.
