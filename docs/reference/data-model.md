# Data model

PostgreSQL schema for authorization, tokens, robots, areas and audit. Session, login and key tables are in [login](login.md#session-tables) and [keys](keys.md#storage). Why robots and areas are modelled this way: [ADR 0009](../decisions/0009-robots-are-users-pinned-by-kind.md), [ADR 0010](../decisions/0010-local-areas-with-admin-assigned-supervisors.md).

RBAC with domains: a role only exists inside an initiative.

```
users --< group_members >-- groups
  |                           |
  +---------+       +---------+
            v       v
         initiative_access   (subject: user XOR group)
            |  composite FK (initiative_id, role_id)
            v
  initiative_roles >-- initiatives --< apps (aud) --< deployments (host)
```

## Tables

| Table | Purpose and key constraints |
|---|---|
| `users` | Humans are created just-in-time on first login; robots by platform admins ([API](api.md#robots)). `id` uuid = JWT `sub`; `kind` human/robot, `UNIQUE (id, kind)`; `google_sub` UNIQUE, NOT NULL iff `kind = 'human'` (CHECK; robots have none); `owner_user_id` (robots, informational); `email`, `name` informational; `status` active/disabled |
| `groups`, `group_members` | Internal groups; PK `(group_id, user_id)`; members are humans only (`kind` pinned by composite FK). Google Workspace group sync is a later extension |
| `areas`, `area_members`, `area_supervisors` | Migration 0014: areas for the supervisors of people's credentials; `area_members` PK `(user_id)`, one area per person; members and supervisors are humans only (`kind` pinned by composite FK). Read by the portal only ([model](#areas)) |
| `initiatives` | `key` UNIQUE, `status` |
| `initiative_roles` | `UNIQUE (initiative_id, key)` and `UNIQUE (initiative_id, id)` (target of the composite FK); `manages_access` flag for delegated administration |
| `apps` | `id` = `app_<ulid>` (the `aud`); belongs to one initiative; `status` active/disabled |
| `deployments` | `host` UNIQUE, lowercase; maps a host to an app (an app may have several hosts); `last_seen_at` and last probe result |
| `initiative_access` | Grant: initiative + role + exactly one of user/group (CHECK), optional `expires_at`, `granted_by`, `granted_at`; FK `(initiative_id, role_id)` -> `initiative_roles (initiative_id, id)` |
| `platform_admins` | Platform-wide administrators, separate from initiative roles; humans only (`kind` pinned by composite FK) |
| `api_tokens` | Personal and robot access tokens: `user_id`, `token_hash` UNIQUE (SHA-256), `scope` read/admin, `expires_at` NOT NULL, `last_used_at`, `revoked_at` |
| `audit_events` | Admin changes (actor, action, target, before/after jsonb, nullable `initiative_id` for scoping, nullable `token_id` for token-made changes and nullable `area_id` for supervisor-made changes, migration 0014). Append-only: the app role has INSERT and SELECT only |
| `access_denials` | Deny decisions, partitioned by month; retention 365 days by dropping partitions |

## Rules

1. Decision: allow iff the user is active, the host has a deployment, the app is active, the app's initiative is active,
   and a non-expired grant exists for the user directly or through a group. `roles` = union of the granted role keys
   for that initiative. The only exception is the portal app ([ForwardAuth](forwardauth.md#decision-table)): any active user, empty `roles`.
2. Fail closed by construction: no deployment or no grant means deny. Nothing is open by default.
3. The composite FK makes it impossible to grant a role of initiative A on initiative B.
4. No pre-provisioning by email: access binds to `google_sub`, which Google never reassigns. Users log in once
   (or receive access through a group) before direct grants.

## Conventions

- PostgreSQL >= 18 (`uuidv7()` built in); `timestamptz` everywhere; hosts and keys forced lowercase with CHECK.
- Access changes emit `NOTIFY`; platform-api keeps a decision cache (TTL 30 s) invalidated by `LISTEN`.

## Changes made for the portal

| Change | Why |
|---|---|
| `audit_events.initiative_id uuid NULL` (no FK, like the rest of the table) + index `(initiative_id, occurred_at)` | Managers filter by column instead of parsing jsonb |
| `apps.status` active/disabled; new decision reason `app_disabled` | Deactivate instead of delete |
| `deployments.last_seen_at`, `last_probe_at`, `last_probe_status` | Integration health |
| Role `platform_portal` for the portal process (see [Database roles](#database-roles)) | Least privilege for the admin process |
| Migration 0014: `areas`, `area_members`, `area_supervisors` (`platform_portal`: `SELECT, INSERT, DELETE`; nothing for `platform_runtime`) and `audit_events.area_id` (written only by supervisor actions) | Area supervisors |

Deactivate instead of delete: initiatives through `status` (already exists); apps through `status`; a deployment is
deleted (nothing references it, the audit keeps it); a role is deleted only without grants (the FK already blocks
it); a group is deleted only when it has no members and no grants. Grants by email resolve to exactly one active
human user: zero or several matches fail (`users.email` is not unique, `google_sub` is the identity).

## Humans and robots

1. A robot is a row in `users` with `kind = 'robot'`; humans have `kind = 'human'` (default, so existing rows stay valid).
   Everything keyed by user id works unchanged: grants, `access.Decide`, `adminz`, the audit actor and the assertion `sub`.
2. `users.google_sub` is nullable. `CHECK ((kind = 'human') = (google_sub IS NOT NULL))`: a robot has no
   `google_sub`, so a Google login can never resolve to it, and a human always has one (seed users keep their `fake:` value).
   Robots have `email = ''` (`CHECK (kind = 'human' OR email = '')`), a `name` of 1 to 64 characters (`CHECK (kind = 'human'
   OR char_length(name) BETWEEN 1 AND 64)`; humans keep the default `''`) unique among robots (case-insensitive), `status`,
   and an optional `owner_user_id` (FK `users`, `ON DELETE SET NULL`): the accountable human, informational, not authority
   (still no access authority, but it places the robot in its owner's area for area supervisors, see
   [Area supervisors](api.md#area-supervisors)). The FK cannot say "active human", so the API checks it on create and on change (422 otherwise). A robot does not
   stop working when its owner is disabled; offboarding checks the owner column (the robot list shows it).
3. Robots are global like users. Only platform admins create, rename, disable or change the owner of a robot, and issue
   or revoke its tokens (area supervisors may also change the owner and revoke the tokens, from a browser session
   only and within their scope, see [Area supervisors](api.md#area-supervisors)). Humans are still created only by login. The two families never mix in the API: `/users/*`
   (`GET`, `PATCH` status, `DELETE .../sessions`) answers 404 for a robot id and `/robots/*` answers 404 for a human id,
   filtered by `kind` in the SQL, not after loading the row. Disabling revokes the target's tokens in one repository
   function shared by both paths, in the same transaction; it records the revoked token ids. The last-admin guard
   counts humans only (robots are never in `platform_admins`, so it already does).
4. Robots get access only through direct grants, never through groups: their access stays explicit and reviewable.
   The grant subject gains `robot_id` (exactly one of `email`, `group_id`, `robot_id`); an email resolves to exactly one
   active **human** (`FindActiveByEmail` filters `kind = 'human'`), and `robot_id` must name an active robot (422
   otherwise). Robots have no email, so they cannot be found by `GET /users?email=`.
5. Robots are never platform admins. The schema guarantees it, so the permission query needs no filter, with the same
   pattern as the composite FK of grants: `users` gets `UNIQUE (id, kind)`, and `platform_admins` and `group_members`
   get a column `kind text NOT NULL DEFAULT 'human' CHECK (kind = 'human')` with a composite FK `(user_id, kind)`
   to `users (id, kind)` (cascade kept). Neither table can reference a robot. The composite FK replaces the single-column
   one under the same constraint name (`group_members_user_id_fkey`, `platform_admins_user_id_fkey`), so the
   `referenceFields` mapping of the portal repository (violated FK -> request field `user_id`) stays correct.
   `sessions.user_id` gets no pin: no login path produces a session for an id without `google_sub`, and the role that writes
   sessions could forge one for a human just as well, so the pin would add a column and no protection.
6. A robot may hold a role with `manages_access` (this is what lets automation such as `portalctl` manage grants), but it is never an
   *effective* manager ([last effective manager rule](api.md#administration-rules)): an initiative always keeps a human manager. The human-only
   filter goes once into `effectiveGrantExpr` (portal repository), only in the direct-grant branch
   (`initiative_access.user_id` can hold a robot; the group branch needs none, because the composite foreign key of
   `group_members` already guarantees human members), which feeds the
   last-manager rule and the integration health counts alike.

## API tokens

Table `api_tokens`. The secret is `ptk_` + 43 characters of base64url (32 random bytes from `crypto/rand`) +
8 hex digits of the CRC-32 of the preceding text: 55 characters. The prefix lets secret scanners find leaks; the checksum
is only a format check (it is not secret, and the hash covers the whole string), so malformed values are rejected before
any database query.

| Column | Meaning |
|---|---|
| `id` | uuid (`tid` claim, audit target) |
| `user_id` | FK `users`, `ON DELETE CASCADE`: owner, human or robot |
| `name` | Label chosen by the creator, 1 to 64 characters |
| `token_hash` | SHA-256 of the whole secret, `bytea`, unique index. 256 bits of entropy make a slow hash pointless; lookup is by hash, so no secret is ever compared and there is no timing oracle |
| `scope` | `read` or `admin` (CHECK) |
| `created_by`, `created_at` | Acting user (equals `user_id` for personal tokens; a platform admin for robot tokens) |
| `expires_at` | NOT NULL, `CHECK (expires_at > created_at)`; the cap is configuration |
| `last_used_at` | Best-effort |
| `revoked_at` | NULL while valid |

## Areas

| Table | Purpose and key constraints |
|---|---|
| `areas` | `id` uuid, `key` UNIQUE and immutable (same rule as group keys, at most 63 characters, CHECK), `name` (default `''`), `created_at`. No `status`: an area grants nothing by itself, so there is nothing to deactivate; it is deleted only when it has no members and no supervisors |
| `area_members` | `area_id` FK `areas` (`ON DELETE RESTRICT`), `user_id`, `added_at`; **PK `(user_id)`**: one area per person. Humans only: column `kind text NOT NULL DEFAULT 'human' CHECK (kind = 'human')` and composite FK `(user_id, kind)` -> `users (id, kind)` `ON DELETE CASCADE` (the pattern of `group_members`, migration 0012). Index `(area_id, user_id)` |
| `area_supervisors` | `area_id` FK `areas` (`ON DELETE RESTRICT`), `user_id`, `added_at`; PK `(area_id, user_id)` (an area may have several supervisors, a person may supervise several areas). Humans only, same `kind` pin and composite FK. Index `(user_id)` (the permission query) |
| `audit_events.area_id` | `uuid NULL`, no FK and no index (nothing filters by it yet), like `token_id`: the area through which a supervisor acted. NULL for every other event |

A supervisor may also be a member of an area, the one they supervise included. Only platform admins create and delete
areas and manage their members and supervisors. No hierarchy (an area has no parent), no sync from the IdP.

Area changes do not affect access decisions: no `NOTIFY`, and platform-api never reads these tables.

## Database roles

### `platform_runtime` privileges

The portal owns administration, so platform-api runs with the minimum that ForwardAuth and login need (migration 0010).
Anything else is refused at start-up: `CheckRuntimeRole` compares the privileges the role holds, table-wide and per
column, with the expected set in both directions (an extra grant or a missing one stops the process).

- Read only: `group_members`, `initiatives`, `initiative_roles`, `apps`, `initiative_access`.
- No access: `groups`, `platform_admins`, `schema_migrations`. EXECUTE on the SECURITY DEFINER function
  `applied_migrations()` (migration 0015) is the only view of `schema_migrations`: it returns the applied version names
  for the readiness check. `platform_portal` holds the same EXECUTE; PUBLIC does not.
- `users`: SELECT, INSERT of `google_sub`, `email`, `name` only (the columns of the login upsert, so this role cannot create a robot), and UPDATE of `email`, `name`, `updated_at` only (JIT login upsert); `status` and `google_sub` are never writable.
- `sessions`: SELECT, INSERT, DELETE, UPDATE of `last_seen_at`. `app_sessions`: SELECT, INSERT, DELETE.
- `login_codes`, `oauth_flows`: SELECT, INSERT, DELETE, UPDATE of `consumed_at`.
- `api_tokens`: SELECT and UPDATE of `last_used_at` only, plus EXECUTE on the SECURITY DEFINER purge function
  `purge_dead_api_tokens` (no DELETE on the table); it never creates or revokes tokens.
- `deployments`: SELECT and UPDATE of `last_seen_at` only (the `FOR NO KEY UPDATE SKIP LOCKED` row lock needs UPDATE on at
  least one column); `host` and `app_id` are read-only.
- `signing_keys`, `audit_events` (append-only), `access_denials` (append-only) and the partition functions are unchanged.

What the check covers (the portal role, `CheckPortalRole`, uses the same mechanism against its own grant set from migration 0009):

- Every privilege of PostgreSQL 18 on tables, partitioned tables, views, materialized views, foreign tables (SELECT,
  INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER, MAINTAIN) and sequences (USAGE, SELECT, UPDATE) of schema `public`,
  each also probed `WITH GRANT OPTION`. A relation that is not in the expected set must carry nothing.
- A partition is held to its top-level parent: a direct grant on a leaf beyond the parent's set is drift (a leaf with
  less is normal, partitions get no ACL of their own).
- Membership of the effective role in any predefined `pg_*` role, direct or inherited, is refused.

Operational notes:

- The check runs once per pool, at start-up. Drift introduced afterwards is detected on the next restart.
- Table-level SELECT and INSERT also cover columns added later, while UPDATE is column-level here: a new column needs an
  explicit `GRANT UPDATE (col)` (and an entry in `runtimeGrants`) to be writable.
- Deployment ordering: the migrate Job must complete before platform-api (and the portal) roll out. Otherwise new pods
  see the old grants and refuse to start, or stay not ready with reason `migrations pending` (fail closed); the old pods
  keep serving.

### `platform_portal` privileges

`platform_portal` (portal process):

- Authorization tables: CRUD, except `users`.
- `users`: SELECT; `INSERT (kind, name, email, owner_user_id, status)` without `google_sub`, which is why the portal can only
  create robots (the CHECK rejects a human without `google_sub`); `UPDATE (name, owner_user_id, status, updated_at)`. Humans
  are created just-in-time by platform-api and `google_sub` is never writable.
- `api_tokens`: `INSERT`, `SELECT (id, user_id, name, scope, created_by, created_at, expires_at, last_used_at, revoked_at)`
  (never `token_hash`) and `UPDATE (revoked_at)`.
- EXECUTE on `revoke_api_token_by_hash(p_hash bytea) RETURNS uuid` (migration 0013), the only way the portal touches
  `token_hash`: a `SECURITY DEFINER` function with a pinned `search_path` that rejects a hash that is not 32 bytes and
  revokes the live token with that hash (not revoked, not expired), returning its id or NULL when nothing matched;
  `platform_runtime` and PUBLIC cannot execute it. Function privileges are outside `CheckPortalRole` (it compares table,
  column and sequence privileges only), like `purge_dead_api_tokens`: a test pins them.
- `audit_events`: SELECT and INSERT (the table-wide `INSERT` covers `area_id`); `access_denials`: SELECT.
- `sessions` and `app_sessions`: DELETE plus SELECT without the token hashes.
- `areas`, `area_members`, `area_supervisors`: `SELECT, INSERT, DELETE` (no `UPDATE`: keys are immutable and there is no rename).
- Nothing on `signing_keys`, `oauth_flows` or `login_codes`.

`platform_runtime` has nothing on the area tables; `runtimeGrants` has no entry for them (an unlisted table must carry no
privilege, so `CheckRuntimeRole` already enforces it). The expected sets of `CheckPortalRole` include the three area tables.

Migrations that change privileges follow the deployment rule of [`platform_runtime` privileges](#platform_runtime-privileges): the migrate
Job runs first, and new pods fail the start-up check against old grants (fail closed). `ALTER TABLE audit_events` takes
`ACCESS EXCLUSIVE` on a table platform-api writes (login audit, `RevokeUser`): migration 0014 sets `lock_timeout` like 0012
and is retried rather than queueing every writer behind it.
