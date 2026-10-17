# 0004. Google-only login with an hd allowlist

- Status: Accepted
- Date: 2026-10-14

## Context and Problem

Companies adopting atrium use Google Workspace or Cloud Identity. Login must bind access to a stable identity, work without storing Google credentials, and cut off departed users even though Google sends no RISC events for Workspace users.

## Considered Options

- Google OIDC (authorization code + PKCE) with the consent screen set to Internal (chosen).
- Identity by email: rejected, email is not an identifier.
- Pre-provisioning users by email: rejected, access binds to `google_sub`, which Google never reassigns.
- Syncing Workspace suspensions through the Admin SDK Directory API: later, tracked in [TODO.md](../../TODO.md).

## Decision Outcome

Authorization Code + PKCE (S256) + `state` + `nonce`; the `hd` claim is sent only as a UI hint and enforced against `PLATFORM_ALLOWED_HD` on the ID token (personal accounts carry no `hd`). Identity is `sub`, mapped to `users.google_sub`; users are created just in time on first login. No Google token is stored. The issuer is fixed to `https://accounts.google.com` and never configurable. Revocation is local: a `disabled` flag, sessions deleted on disable, and the 30 s decision cache bound the cutoff.

## Consequences

- Google is the only supported identity provider today.
- Users must log in once (or receive access through a group) before direct grants.
- A user suspended in Google but not disabled in the portal keeps access until the 8 h session cap, and keeps working tokens up to the personal cap.
- Reference: [Google login and sessions](../reference/login.md).
