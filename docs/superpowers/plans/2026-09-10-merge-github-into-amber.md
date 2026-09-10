# Merge GitHub cc-connect into Amber GitLab cc-connect

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cherry-pick/merge 13 unique commits from `github/cc-connect:custom-features` into `gitlab/cc-connect:feat/ambre-feedback-aug25`, resolving all conflicts so both feature sets coexist.

**Architecture:** Add GitHub repo as a remote in the Amber repo, create a dedicated merge branch, run `git merge`, then manually resolve each conflict. The target is the Amber repo — its features (WS reliability, image downscale, thread isolation) take priority in any ambiguous conflict.

**Tech Stack:** Go, git, `go build ./...`, `go test ./...`

---

## Background

- **Common ancestor:** `ace73792` (`fix(chat-api): use project-scoped knowledge storage`)
- **GitHub (source):** 13 unique commits — feishu nexCallback/LTS, `/send` command, skill-guide routing, timer config, context refresh
- **Amber (target):** 22 unique commits — WS reliability, catch-up polling, thread-isolated sessions, image downscale, `HideIntermediateText`, fork SDK

### Files with potential conflicts (touched by both branches)

| File | GitHub changes | Amber changes |
|------|---------------|---------------|
| `core/engine.go` | `/send` command, bridge capabilities | `root_id`/`message_id` injection |
| `core/interfaces.go` | `CardSender`, `InlineButtonSender` additions | `HideIntermediateText` |
| `core/session.go` | session state changes | session state changes |
| `core/context_refresh.go` | Automon resource refresh | context refresh |
| `core/context_refresh_test.go` | tests | tests |
| `core/jsonl_channel_store.go` | store changes | store changes |
| `core/jsonl_channel_store_test.go` | tests | tests |
| `agent/claudecode/claudecode.go` | skill-guide routing, reasoning_effort | image downscale, local path hint |
| `agent/claudecode/session.go` | session mgmt | session mgmt |
| `agent/claudecode/context_resources_test.go` | tests | tests |
| `cmd/cc-connect/main.go` | /send command wiring | main wiring |
| `config/config.go` | timer config | feishu catch-up config |
| `platform/feishu/feishu.go` | nexCallback/LTS, /send delivery | catch-up poller, TCP keepalive |
| `platform/feishu/feishu_test.go` | nexCallback tests | catch-up tests |
| `docs/context-resource-refresh.md` | doc update | doc update |

---

## Task 1: Set up merge branch

**Files:** No code files — git operations only

- [ ] **Step 1: Verify Amber repo is clean**

```bash
cd /Users/dechang.ren/Projects/Amber/group/tech/llm/cc-connect
git status
```
Expected: `nothing to commit, working tree clean`. If not, stash first: `git stash -u`.

- [ ] **Step 2: Add GitHub repo as remote**

```bash
git remote add github /Users/dechang.ren/Projects/github/cc-connect
git remote -v
```
Expected: `github` remote points to the local GitHub repo path.

- [ ] **Step 3: Fetch GitHub commits**

```bash
git fetch github
```
Expected: Fetches `github/custom-features` and other refs.

- [ ] **Step 4: Create merge branch from Amber HEAD**

```bash
git checkout -b merge/github-into-amber
git log --oneline -3
```
Expected: New branch off current Amber HEAD (`79836c5b`).

- [ ] **Step 5: Start the merge**

```bash
git merge github/custom-features --no-commit --no-ff
```
Expected: Either "Automatic merge went well" or "Automatic merge failed; fix conflicts". Conflicts are expected in the overlapping files listed above.

- [ ] **Step 6: Check conflict status**

```bash
git status
```
Note every file listed under "both modified". These need manual resolution in Tasks 2–N.

---

## Task 2: Resolve conflicts in `core/engine.go`

**Files:**
- Modify: `core/engine.go`

- [ ] **Step 1: View conflicts**

```bash
git diff core/engine.go
```
Or open in editor to see `<<<<<<<` / `=======` / `>>>>>>>` markers.

- [ ] **Step 2: Resolve — keep BOTH sets of changes**

**Amber's additions to keep:** `root_id` injection into prompt header, `message_id` injection into prompt header.

**GitHub's additions to keep:** `/send` command handler, `bridge_capabilities` wiring.

Resolution principle: Both are additive — integrate them side by side. If both modify the same function, verify the intent of each change and merge the logic. Amber changes take priority if truly ambiguous.

```bash
# After resolving markers manually:
git diff core/engine.go   # should show clean diff, no <<<< markers
```

- [ ] **Step 3: Stage file**

```bash
git add core/engine.go
```

---

## Task 3: Resolve conflicts in `core/interfaces.go`

**Files:**
- Modify: `core/interfaces.go`

- [ ] **Step 1: View conflicts**

```bash
git diff core/interfaces.go
```

- [ ] **Step 2: Resolve — keep BOTH sets of changes**

**Amber adds:** `HideIntermediateText` field or interface.
**GitHub adds:** `CardSender`, `InlineButtonSender` optional interfaces.

Both are additive interface definitions. Keep all of them.

- [ ] **Step 3: Stage file**

```bash
git add core/interfaces.go
```

---

## Task 4: Resolve conflicts in `core/session.go`

**Files:**
- Modify: `core/session.go`

- [ ] **Step 1: View conflicts**

```bash
git diff core/session.go
```

- [ ] **Step 2: Resolve**

Read what each side changed. Keep both. If the same field/method was changed differently, use Amber's version as the base and layer GitHub's logic on top.

- [ ] **Step 3: Stage file**

```bash
git add core/session.go
```

---

## Task 5: Resolve conflicts in `core/context_refresh.go` and test

**Files:**
- Modify: `core/context_refresh.go`
- Modify: `core/context_refresh_test.go`

- [ ] **Step 1: View conflicts**

```bash
git diff core/context_refresh.go
git diff core/context_refresh_test.go
```

- [ ] **Step 2: Resolve both files**

Both branches likely extended the same functions. Keep both extensions.

- [ ] **Step 3: Stage files**

```bash
git add core/context_refresh.go core/context_refresh_test.go
```

---

## Task 6: Resolve conflicts in `core/jsonl_channel_store.go` and test

**Files:**
- Modify: `core/jsonl_channel_store.go`
- Modify: `core/jsonl_channel_store_test.go`

- [ ] **Step 1: View conflicts**

```bash
git diff core/jsonl_channel_store.go
git diff core/jsonl_channel_store_test.go
```

- [ ] **Step 2: Resolve both files**

Keep both sets of changes.

- [ ] **Step 3: Stage files**

```bash
git add core/jsonl_channel_store.go core/jsonl_channel_store_test.go
```

---

## Task 7: Resolve conflicts in `agent/claudecode/claudecode.go`

**Files:**
- Modify: `agent/claudecode/claudecode.go`

- [ ] **Step 1: View conflicts**

```bash
git diff agent/claudecode/claudecode.go
```

- [ ] **Step 2: Resolve**

**Amber adds:** image downscaling (>2000px), remove local path hint.
**GitHub adds:** skill-guide routing fixes, `reasoning_effort` default for gpt-5.6-terra.

Keep all. These touch different functions — skill-guide routing vs image handling.

- [ ] **Step 3: Stage file**

```bash
git add agent/claudecode/claudecode.go
```

---

## Task 8: Resolve conflicts in `agent/claudecode/session.go` and test

**Files:**
- Modify: `agent/claudecode/session.go`
- Modify: `agent/claudecode/context_resources_test.go`

- [ ] **Step 1: View conflicts**

```bash
git diff agent/claudecode/session.go
git diff agent/claudecode/context_resources_test.go
```

- [ ] **Step 2: Resolve**

Keep both sets of changes.

- [ ] **Step 3: Stage files**

```bash
git add agent/claudecode/session.go agent/claudecode/context_resources_test.go
```

---

## Task 9: Resolve conflicts in `cmd/cc-connect/main.go` and `config/config.go`

**Files:**
- Modify: `cmd/cc-connect/main.go`
- Modify: `config/config.go`

- [ ] **Step 1: View conflicts**

```bash
git diff cmd/cc-connect/main.go
git diff config/config.go
```

- [ ] **Step 2: Resolve `main.go`**

GitHub wires up `/send` command; Amber adds other wiring. Keep both.

- [ ] **Step 3: Resolve `config/config.go`**

GitHub adds timer config switch; Amber adds feishu catch-up config fields. Both are additive struct fields — keep both.

- [ ] **Step 4: Stage files**

```bash
git add cmd/cc-connect/main.go config/config.go
```

---

## Task 10: Resolve conflicts in `platform/feishu/feishu.go` and tests

**Files:**
- Modify: `platform/feishu/feishu.go`
- Modify: `platform/feishu/feishu_test.go`

- [ ] **Step 1: View conflicts**

```bash
git diff platform/feishu/feishu.go
git diff platform/feishu/feishu_test.go
```

- [ ] **Step 2: Resolve `feishu.go`**

**Amber adds:** catch-up poller lifecycle (Start/Stop), TCP keepalive dialer.
**GitHub adds:** nexCallback/LTS HTTP forwarding, work-item card patching, `/send` plain text delivery.

These touch different parts of the feishu platform — keepalive/poller in the WS setup vs nexCallback in a separate handler. Keep all.

- [ ] **Step 3: Resolve `feishu_test.go`**

Both add tests for different features. Keep all tests.

- [ ] **Step 4: Stage files**

```bash
git add platform/feishu/feishu.go platform/feishu/feishu_test.go
```

---

## Task 11: Resolve remaining conflicts and doc files

**Files:**
- Modify: any remaining conflicted files (check `git status`)
- Modify: `docs/context-resource-refresh.md`

- [ ] **Step 1: Check remaining conflicts**

```bash
git status | grep "both modified"
```

- [ ] **Step 2: Resolve any remaining files**

For doc files, merge both sections (Amber's doc + GitHub's doc updates).

- [ ] **Step 3: Stage all remaining resolved files**

```bash
git add docs/context-resource-refresh.md
# and any other remaining files
git status   # should show no "both modified" entries
```

---

## Task 12: Build and test

**Files:** No changes — verification only

- [ ] **Step 1: Build**

```bash
cd /Users/dechang.ren/Projects/Amber/group/tech/llm/cc-connect
go build ./...
```
Expected: No errors. Fix any compilation errors before proceeding.

- [ ] **Step 2: Run tests**

```bash
go test ./...
```
Expected: All tests pass. Fix any test failures before proceeding.

- [ ] **Step 3: Run with race detector**

```bash
go test -race ./...
```
Expected: No race conditions detected.

---

## Task 13: Commit the merge

**Files:** No changes — git operation only

- [ ] **Step 1: Final status check**

```bash
git status
```
Expected: All changes staged, nothing untracked that shouldn't be.

- [ ] **Step 2: Commit**

```bash
git commit -m "$(cat <<'EOF'
chore: merge github/custom-features into feat/ambre-feedback-aug25

Integrates 13 commits from the GitHub fork (nexCallback/LTS, /send command,
skill-guide routing, timer config, context refresh) with Amber's existing
work (WS reliability, catch-up polling, thread isolation, image downscale).

Co-Authored-By: Claude Sonnet 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 3: Verify merge commit**

```bash
git log --oneline -5
git log --oneline ace73792..HEAD | wc -l
```
Expected: commit count > 22 (Amber's 22 + GitHub's 13, minus the merge base = ~35).

- [ ] **Step 4: Clean up GitHub remote**

```bash
git remote remove github
```

---

## Task 14: Final verification

- [ ] **Step 1: Full build + test**

```bash
go build ./... && go test ./...
```
Expected: Clean.

- [ ] **Step 2: Verify key features from both sides compile**

```bash
# GitHub features present
grep -r "nexCallback\|/send\|skill-guide\|reasoning_effort" --include="*.go" -l

# Amber features present  
grep -r "keepalive\|HideIntermediateText\|catchup\|root_id\|downscale" --include="*.go" -l
```
Both greps should return relevant files.

- [ ] **Step 3: Push to GitLab (optional — confirm with user first)**

```bash
# Only after user confirmation:
# git push origin merge/github-into-amber
```
