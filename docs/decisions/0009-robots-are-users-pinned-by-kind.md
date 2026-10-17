# 0009. Robots are users pinned by kind

- Status: Accepted
- Date: 2026-10-16

## Context and Problem

Automation needs an identity that can hold grants and appear in the audit and in the assertion `sub`, without being a human, being found by a Google login, or becoming a platform admin.

## Decision Outcome

Robots are rows of `users` with `kind = 'robot'`, so everything keyed by user id works unchanged (grants, `access.Decide`, `adminz`, the audit actor, the assertion `sub`).

`users.kind` is `human` or `robot`; `google_sub` is NOT NULL iff the kind is human, so a Google login can never resolve to a robot. `platform_admins` and `group_members` pin `kind = 'human'` through a composite FK to `users (id, kind)`, so neither can reference a robot. Robots get access only through direct grants, never through groups. A robot may hold `manages_access` but is never an effective manager, so every initiative keeps a human manager. The owner (`owner_user_id`) is informational, not authority; a departing owner never disables their robots automatically, and offboarding checks the owner column.

## Consequences

- `/users/*` and `/robots/*` are filtered by `kind` in SQL and answer 404 for the other family.
- A robot keeps working when its owner is disabled until someone disables it or revokes its tokens.
- Reference: [Humans and robots](../reference/data-model.md#humans-and-robots).
