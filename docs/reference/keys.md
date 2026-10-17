# Signing keys, rotation, JWKS

How platform-api stores, rotates and publishes the ES256 keys that sign assertions. Rationale: [ADR 0002](../decisions/0002-kek-wrapped-signing-keys-in-postgresql.md).

## Storage

- ECDSA P-256 keys (ES256) live in the PostgreSQL table `signing_keys`: `kid`, public JWK, encrypted private key, state, timestamps.
- Private keys use envelope encryption: encrypted with a key-encryption key (KEK) mounted from a Kubernetes Secret; the KEK never reaches the database.
- `kid` is the RFC 7638 JWK thumbprint (base64url SHA-256 of the public key).

## Lifecycle

| State | Signs | In JWKS | Duration |
|---|---|---|---|
| `next` | No | Yes | Created at bootstrap and right after every promotion (so it is normally published for ~30 days); promoted by rotation only if published for at least 1 h |
| `active` | Yes | Yes | 30 days |
| `retired` | No | Yes | 1 h (covers token TTL + leeway + JWKS cache) |
| removed | No | No | Row kept for audit, private key material deleted |

- Rotation is automatic; a single replica rotates at a time, guarded by a PostgreSQL advisory lock.
- Emergency rotation (suspected compromise): the key is removed from the JWKS immediately and a new one is promoted.
  Apps may keep accepting it until their JWKS cache expires (max 5 minutes); lowering `max-age` shrinks that window.
  If the JWKS is unreachable at that moment, stale-if-error ([SDK failure behavior](assertion.md#sdk-failure-behavior)) extends it up to 20 minutes (5 + 15),
  plus up to 30 s for a replica that missed the change notification.

## Failure and consistency rules

- There is always a `next` key, created right after every promotion, so an emergency revocation normally promotes a
  key apps already know. Revocation prefers a `next` published for at least 1 h; during the first hour after a
  promotion it takes the oldest one, and if none can be unsealed it activates a fresh key. Only if no `next` exists is a fresh key activated directly
  (apps then pay one rate-limited JWKS refetch, up to 60 s of rejections).
- A key is promoted only after its private key is unsealed successfully inside the same transaction.
- If a replica cannot load the current active key, it stops signing (fail closed) instead of keeping an old one.
- Rotation timestamps come from PostgreSQL `now()`, never from replica clocks.
- A replica that missed the `signing_keys_changed` notification may sign with a revoked key for up to 30 s
  (periodic reload); this stays inside the 5-minute emergency window above.
- Sealed private keys carry a format version byte, so the KEK can be rotated by re-sealing.
- Every key stores the fingerprint (`kek_id`) of the KEK that sealed it: a domain-separated, non-reversible
  derivation of the KEK, never the KEK itself. A replica whose KEK fingerprint differs from the `active`/`next` keys
  reports `KEKMismatch`, fails readiness, and refuses to rotate or revoke without touching any row.
  Only with a matching fingerprint is an undecryptable `next` treated as corrupt and replaced automatically.

## JWKS endpoint

- `GET /.well-known/jwks.json` with `Cache-Control: max-age=300`; public keys only.
- Apps fetch it from the JWKS listener (`PLATFORM_JWKS_ADDR`) through the in-cluster service URL, never through the public route, and refetch on unknown `kid` (rate limited, see [SDK failure behavior](assertion.md#sdk-failure-behavior)).
- Internal TLS (optional, vendor-neutral): `PLATFORM_JWKS_TLS_CERT_FILE` and `PLATFORM_JWKS_TLS_KEY_FILE` (PEM; both or neither, otherwise a configuration error) make the JWKS listener serve HTTPS only, TLS 1.3 minimum (every in-cluster client, the Go SDK and Grafana, speaks it). The files may be produced by any issuer (cert-manager, Vault agent, a manual Secret); the key file follows the same permission policy as the other secret files (no group write, nothing for others), while the certificate is public material and is read without a permission check (still a regular file, size-limited and PEM-validated). The pair is re-read every 30 s and swapped in when its content changed, so new handshakes present the new certificate without a restart. An invalid initial pair stops start-up; an invalid replacement keeps serving the last good pair and is logged once per distinct error. The certificate must carry the SAN of the in-cluster service name apps use. Verifiers that use a private CA take it from a PEM bundle: `PORTAL_JWKS_CA_FILE` (portal), `HELLO_JWKS_CA_FILE` (example app), `Config.RootCAs` (SDK); without it the system roots apply. An `http` JWKS URL logs a warning at verifier start-up.
