# 0008. Opaque API tokens and robots for non-browser clients

- Status: Accepted
- Date: 2026-10-16

## Context and Problem

CLIs and automation need to call the portal API, and containing a leaked credential must not require a browser. The product is multi-company, so the design assumes no CI system, secret manager or stack on the adopter's side.

## Considered Options

- Google ID tokens: discarded. `gcloud auth print-identity-token --audiences` does not work with user accounts, and Google identity would bypass the platform's revocation and audit.
- Personal and robot access tokens, one mechanism (chosen, phase 1).
- Workload identity (OIDC issuers per adopter, mapped to robots by a trust policy): direction only (phase 2).
- OAuth device flow: not planned (phase 3); device-code phishing campaigns target admins.

## Decision Outcome

Opaque `ptk_` secrets (32 random bytes plus prefix and a CRC-32 format check). Only the SHA-256 is stored; lookup is by hash, with no cache, so revocation applies on the next request on every replica. Expiration is mandatory with one cap per owner kind (30 days personal, 90 days robot), because a human token must not outlive a Google suspension by much while robots are not offboarded through Google. Scope (`read`/`admin`) is a ceiling, never a grant: the effective permission is the owner's live permission intersected with it. Tokens are created only from a browser session, so a leaked token cannot extend itself, and revocation by secret lets an admin contain a leak found by any scanner. On a portal host, `Authorization` takes the token path exclusively (no cookie fallback); Traefik strips it after ForwardAuth so the secret never reaches the portal process.

## Consequences

- A bearer secret works for anyone holding it until revoked or expired; there is no sender constraint.
- The portal role can insert a token with a hash it knows; the audit trail is the control for that.
- Phases 2 and 3 are open: [TODO.md](../../TODO.md).
- Reference: [Tokens](../reference/api.md#tokens), [token path](../reference/forwardauth.md#token-path).
