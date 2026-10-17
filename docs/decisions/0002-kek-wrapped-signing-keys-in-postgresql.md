# 0002. KEK-wrapped signing keys in PostgreSQL

- Status: Accepted
- Date: 2026-10-14

## Context and Problem

Assertions are signed with ES256 keys that must be shared by all platform-api replicas, rotate without downtime and be revocable in an emergency. Storing the keys in the database makes them available to every replica, but a database dump must not expose them.

## Considered Options

- Private keys sealed with a key-encryption key (KEK) mounted from a Kubernetes Secret and stored in the `signing_keys` table (chosen).
- An external KMS: not done in v1; production hardening tracked in [TODO.md](../../TODO.md).

## Decision Outcome

Envelope encryption: the KEK never reaches the database, `kid` is the RFC 7638 thumbprint, and keys move through `next`, `active` (30 days) and `retired` (1 h). There is always a published `next` key, created right after every promotion, so an emergency revocation normally promotes a key apps already know. Rotation is automatic, one replica at a time under a PostgreSQL advisory lock, and timestamps come from the database clock. A key is promoted only after its private key is unsealed inside the same transaction. A replica that cannot load the active key stops signing (fail closed). Each key stores a non-reversible `kek_id` fingerprint of the KEK that sealed it, so a replica with a different KEK reports `KEKMismatch`, fails readiness and refuses to rotate or revoke instead of replacing keys it cannot read. Sealed keys carry a format version byte so the KEK can be rotated by re-sealing.

## Consequences

- Emergency rotation leaves a bounded window: apps may keep accepting a revoked key until their JWKS cache expires (max 5 minutes, up to 20 with stale-if-error, plus 30 s for a replica that missed the notification).
- Anyone who can read the KEK Secret and the database can unseal every key.
- Reference: [Signing keys, rotation, JWKS](../reference/keys.md).
