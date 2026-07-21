# chat-api Stream Answer Parse Fix

Date: 2026-07-21

## Goal

Stop SSE `text_delta` from truncating answers that contain markdown `---` and
from duplicating text when a progress line is replaced by a final answer —
without changing `core.buildCardContent` or other platforms.

## Problem

1. `parseStreamingCardContent` used `LastIndex("\n\n---\n\n")` to isolate the
   answer. Model answers often contain the same horizontal-rule pattern, so
   the stream kept only the final paragraph while `GET /messages` (history)
   still showed the full answer.
2. `textDelta` returned the full current snapshot on non-prefix changes, but
   clients append every `text` field. Progress → clarification therefore
   concatenated into duplicates / garbled joins.

## Decisions

| Decision | Choice |
|----------|--------|
| Scope | `platform/chat-api/` only |
| Answer isolation | Structural strip of thinking + leading tool blocks; remainder is answer |
| Non-prefix deltas | Emit `replace: true` with full text |
| Engine | Unchanged in this hotfix; structured StreamingCard deferred |

## Client contract

| Frame | Client |
|-------|--------|
| `{"text": "<suffix>"}` | `buf += text` |
| `{"text": "<full>", "replace": true}` | `buf = text` |

## Follow-up

Optional `StructuredStreamingCard` in core so chat-api can drop markdown
re-parsing entirely (see architecture discussion: short-term fix A → Engine
evolution later).
