# 0006. Portal as a separate process with its own database role

- Status: Accepted
- Date: 2026-10-15

## Context and Problem

Administration needs a web UI and an API, and administration needs write access to the authorization tables. Serving that UI from the auth host would let an XSS there compromise the login of every app, and giving platform-api those writes would widen what a compromise of the login path can do.

## Considered Options

- Serve the admin UI from platform-api on the auth host: rejected (XSS blast radius).
- A separate `portal` process, registered as an ordinary app, with its own host and database role (chosen).

## Decision Outcome

The portal is its own Deployment (`cmd/portal`, same Go module) on its own host behind the same ForwardAuth. It never holds the KEK or any signing key. platform-api runs as `platform_runtime` with only what ForwardAuth and login need; the portal runs as `platform_portal`. Both roles are checked at start-up against an expected set of privileges, in both directions, and the process refuses to start on drift. Portal permissions are resolved from the database on every request (never carried in the assertion, which would be stale for its lifetime) by a pure function `adminz.Decide`, in the same transaction as the mutation. An unauthorized resource of another initiative answers 404 so its existence is not confirmed.

## Consequences

- The migrate Job must complete before platform-api and the portal roll out; otherwise new pods see old grants and refuse to start (the old pods keep serving).
- A new writable column needs an explicit grant and an entry in the expected set.
- Reference: [Portal API](../reference/api.md), [Database roles](../reference/data-model.md#database-roles).
