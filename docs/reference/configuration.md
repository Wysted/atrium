# Configuration reference

Every environment variable each binary reads, with its default, validation and effect. The code is the source of truth, and a test (`platform/api/cmd/platform-api/configdoc_test.go`) fails when a quoted `PLATFORM_`, `PORTAL_` or `HELLO_` name in a non-test Go file under `platform/` has no table row here, or a table row names a variable no such file contains. It checks names only, not defaults or validation.

Conventions, valid for every table:

- platform-api, portal and hello trim surrounding whitespace and treat an empty value as unset. portalctl reads `PORTAL_URL` and `PORTAL_API_TOKEN` as they are, with no trimming.
- A **duration** is a Go duration (`45m`, `720h`); a **bool** is one of `1`, `t`, `T`, `TRUE`, `true`, `True`, `0`, `f`, `F`, `FALSE`, `false`, `False` (`yes` and `on` are rejected); a **listen address** is `host:port` (`:8080`).
- `platform-api serve` and `portal serve` check at start-up and any problem stops the process. Variable syntax, the Google secret file and the probe CA file are validated together in one pass that reports every problem at once. The KEK, the JWKS TLS pair, `PORTAL_JWKS_CA_FILE` and the database fail one at a time afterwards. There are no defaults other than the ones listed.
- "Manifest" columns say what `platform-gitops/base/` sets, which is a deployment choice and not a code default.
- Database connection: both servers (and `platform-api migrate`) read the standard libpq variables (`PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGSSLMODE`, `PGPASSFILE`, ...) through pgx. `PGSSLMODE` defaults to `prefer` in pgx; `platform-gitops/base/` sets `disable` for the in-cluster database, so with an external database set it explicitly (for example `verify-full`). Roles and privileges: [data-model.md, "Database roles"](data-model.md#database-roles).

## Go system roots

`SSL_CERT_FILE` and `SSL_CERT_DIR` are read by the Go runtime, not by atrium. They set the system roots that platform-api uses to reach the Google endpoints, that portal uses for an `https` `PORTAL_JWKS_URL` when `PORTAL_JWKS_CA_FILE` is unset, that hello uses likewise without `HELLO_JWKS_CA_FILE`, and that portalctl uses for `PORTAL_URL`. `PORTAL_PROBE_CA_FILE` adds to them.

## platform-api

Subcommands: `platform-api migrate` (applies migrations as the owner role, then the optional bootstrap; reads the `PG*` variables and the two variables marked "migrate only" below and `PLATFORM_PORTAL_APP_ID`) and `platform-api serve` (everything below). Behavior: [forwardauth.md](forwardauth.md), [login.md](login.md), [keys.md](keys.md).

| Variable | Required | Default | Format and validation | Effect |
|---|---|---|---|---|
| `PLATFORM_INTERNAL_ADDR` | yes | none | listen address | Listener Traefik ForwardAuth and kubelet probes use. Never exposed by a route. Manifest: `:8081` |
| `PLATFORM_PUBLIC_ADDR` | yes | none | listen address | Auth host listener (login, callbacks, JWKS copy). The only one a route may point to. Manifest: `:8080` |
| `PLATFORM_METRICS_ADDR` | yes | none | listen address | `/metrics` alone, so a NetworkPolicy can filter by port. Manifest: `:9090` |
| `PLATFORM_JWKS_ADDR` | yes | none | listen address | JWKS alone, the only port app namespaces reach. Manifest: `:8083` |
| `PLATFORM_JWKS_TLS_CERT_FILE` | no | unset (plain HTTP, logged as a WARN) | PEM path; must be set together with the key | Switches the JWKS listener to HTTPS (TLS 1.3 minimum) |
| `PLATFORM_JWKS_TLS_KEY_FILE` | no | unset | PEM path; must be set together with the certificate | Private key of the pair above |
| `PLATFORM_AUTH_HOST` | yes | none | lowercase host: letters, digits, `.` and `-`; no scheme, port or path | Host of the auth listener |
| `PLATFORM_ISSUER` | yes | none | exactly `https://<PLATFORM_AUTH_HOST>`, nothing else (no port, path, query, fragment or credentials) | The `iss` claim of every assertion |
| `PLATFORM_KEK_FILE` | yes | none | path of the KEK file (see below) | Key-encryption key that seals the signing keys |
| `PLATFORM_IDLE_TIMEOUT` | no | `45m` | duration within `30m` to `60m` | Central session idle timeout |
| `PLATFORM_FAKE_LOGIN` | no | `false` | bool | Development login that accepts any email. Needs no Google variables; ignored with a WARN when Google is configured (the two never coexist). Manifest: only `overlays/local` sets `true` |
| `PLATFORM_PORTAL_APP_ID` | no | unset (no portal app) | `app_` plus 26 characters of Crockford base32 (`^app_[0-9A-HJKMNP-TV-Z]{26}$`) | Id of the portal app, the one app any active user may open ([api.md](api.md)). Must be an app of the `platform` initiative or the exception stays inactive (WARN at start-up). `migrate` also reads it (with `PLATFORM_PORTAL_HOST`) to create the app |
| `PLATFORM_PORTAL_HOST` | no (`migrate` only) | unset | valid lowercase host name (no scheme, port or path); set together with `PLATFORM_PORTAL_APP_ID` or not at all | Read by `migrate`, not `serve`. Host of the portal: `migrate` creates, if missing, the `platform` initiative, the portal app and a deployment of this host ([install.md](../install.md#9-register-the-portal-and-create-the-first-platform-admin)). Never modifies existing rows; a host already mapped to another app, or an app id living in another initiative, fails the Job |
| `PLATFORM_BOOTSTRAP_ADMIN_EMAIL` | no (`migrate` only) | unset | email address, lowercased | Read by `migrate`, not `serve`. Grants platform admin to the one human, non-fake user with that email, only when `platform_admins` is empty (a WARN and success if the user has not signed in yet; an error if several match) |
| `PLATFORM_GOOGLE_CLIENT_ID` | with Google | unset | non-empty, no whitespace | OAuth client id (not secret) |
| `PLATFORM_GOOGLE_CLIENT_SECRET_FILE` | with Google | unset | path of a secret file, non-empty, at most 4 KiB | OAuth client secret |
| `PLATFORM_ALLOWED_HD` | with Google | unset | comma-separated domains, each with a dot, host characters only, no empty entries; lowercased, duplicates collapse | Allowlist of Google `hd` values |
| `PLATFORM_ALLOW_PRIVILEGED_DB_LOGIN` | no | `false` | bool | Development only. Without it, start-up refuses a database login role that is superuser, bypasses RLS, owns tables or can `CREATE` in `public`. With `true` the refusal becomes a WARN. Never in production |

Rules across variables:

- The four listener addresses must be `host:port` and use different ports (a wildcard host conflicts with any host on the same port). The TLS files are not listeners.
- Login mode: the client id, client secret file and `PLATFORM_ALLOWED_HD` are all required as soon as one is set. With none set, `PLATFORM_FAKE_LOGIN=true` is required, otherwise start-up fails. See [login.md](login.md).
- The database user is whatever `PGUSER` says; `serve` assumes the runtime role as a startup parameter (see Database roles).

### Secrets and files (platform-api)

| File | Variable | Format | Reloaded at runtime |
|---|---|---|---|
| KEK | `PLATFORM_KEK_FILE` | exactly 32 raw bytes (not hex, not base64, no trailing newline), not all zeros. Generate with `openssl rand 32 > kek` | No, read once at start-up |
| Google client secret | `PLATFORM_GOOGLE_CLIENT_SECRET_FILE` | the secret as text; a trailing newline is trimmed | No, read once at start-up; rotation needs a restart ([runbook](../runbooks/google-client-secret-rotation.md)) |
| JWKS TLS certificate | `PLATFORM_JWKS_TLS_CERT_FILE` | PEM (leaf first, chain allowed), at most 256 KiB | Yes, compared every 30 s; a valid replacement is served to new handshakes without a restart |
| JWKS TLS key | `PLATFORM_JWKS_TLS_KEY_FILE` | PEM, at most 256 KiB | Yes, together with the certificate. An invalid initial pair stops start-up; an invalid replacement keeps the last good pair and is logged once per distinct error |
| pgpass | `PGPASSFILE` | libpq format: lines of `host:port:database:user:password`, wildcards (`*`) allowed | No. pgx reads it once when the pool config is parsed, and only if no password is set otherwise; new connections do not reread it |

File policy for the KEK, the Google secret and the TLS key: a regular file, mode `0640` or stricter (group write and any access for others are rejected, not warned about). In Kubernetes mount the Secret with `defaultMode: 0440` and a pod `fsGroup`. The TLS certificate is public and has no permission check. Neither the KEK nor any secret value is logged.

Mounted by `platform-gitops/base/`:

| Secret (key) | Mount path | Used by |
|---|---|---|
| `platform-kek` (`kek`) | `/etc/platform/kek/kek` | platform-api, via `PLATFORM_KEK_FILE` |
| `platform-api-pgpass` (`pgpass`) | `/etc/platform/pgpass/pgpass` | platform-api, via `PGPASSFILE` (login role `platform_api`) |
| `platform-owner-pgpass` (`pgpass`) | `/etc/platform/pgpass/pgpass` | the migrate Job, via `PGPASSFILE` (login role `platform_owner`) |

Added by overlays in this repo (names are examples): `platform-api-jwks-tls` (`tls.crt`, `tls.key`) for the JWKS TLS pair, and `platform-google-client-secret` (`secret`) plus a ConfigMap with the client id for Google login.

## portal

Subcommand: `portal serve`. Behavior: [api.md](api.md), [metrics.md](metrics.md).

| Variable | Required | Default | Format and validation | Effect |
|---|---|---|---|---|
| `PORTAL_PUBLIC_ADDR` | yes | none | listen address | Listener of the portal route. Manifest: `:8080` |
| `PORTAL_INTERNAL_ADDR` | yes | none | listen address | Probes listener. Manifest: `:8081` |
| `PORTAL_METRICS_ADDR` | yes | none | listen address | `/metrics` alone. Manifest: `:9090` |
| `PORTAL_APP_ID` | yes | none | `^app_[0-9A-HJKMNP-TV-Z]{26}$` | The portal's own app id: the `aud` its assertions must carry. Same value as `PLATFORM_PORTAL_APP_ID`. Manifest: a placeholder the overlay replaces |
| `PORTAL_ISSUER` | yes | none | `https://<auth host>[:port]` with nothing else; a port must be decimal 1 to 65535 | Exact `iss` match, and the auth origin used for CSP and probe checks, for example `https://auth.apps.example.com`. Manifest: `https://auth.invalid`, replaced by the overlay |
| `PORTAL_JWKS_URL` | yes | none | absolute `http` or `https` URL, no credentials, query or fragment | In-cluster URL of platform-api's JWKS listener. Plain `http` is accepted with a WARN: the keys then rest on the NetworkPolicy. Manifest: `http://platform-api-jwks.platform.svc.cluster.local:8083/.well-known/jwks.json` |
| `PORTAL_JWKS_CA_FILE` | no | unset (system roots) | PEM path; only valid with an `https` `PORTAL_JWKS_URL` | CA bundle for a JWKS signed by a private CA. When set it **replaces** the system roots for the JWKS client (the SDK trust store) |
| `PORTAL_ALLOW_PRIVILEGED_DB_LOGIN` | no | `false` | bool | Same development-only escape hatch as in platform-api, for the `platform_portal` role |
| `PORTAL_TOKEN_MAX_TTL_PERSONAL` | no | `720h` | duration above zero | Cap and default lifetime of a personal API token |
| `PORTAL_TOKEN_MAX_TTL_ROBOT` | no | `2160h` | duration above zero | Cap and default lifetime of a robot API token |
| `PORTAL_PROBE_INTERVAL` | no | `5m` | duration: `0` disables the probe, otherwise `1m` to `24h` | Time between probe rounds (each wait is the interval plus or minus 10 percent). The overlay of this repo sets `1m` |
| `PORTAL_PROBE_ADDR` | when the probe is enabled | none | `host:port`, host non-empty, port 1 to 65535 | Where the probe connects, whatever the deployment host is (the ingress service). TLS server name and `Host` stay the deployment host. Manifest: `traefik.traefik.svc.cluster.local:443`. Not read when `PORTAL_PROBE_INTERVAL=0` |
| `PORTAL_PROBE_CA_FILE` | no | unset | PEM path with at least one certificate, at most 1 MiB | CA bundle **added to** the system roots to verify the ingress certificate |
| `PORTAL_PROBE_INSECURE_TLS` | no | `false` | bool | Development only: skips certificate verification of the probe; logged at Error level on every start |

Rules across variables:

- The three listener addresses must use different ports.
- With `PORTAL_PROBE_INTERVAL=0` the other `PORTAL_PROBE_*` variables are not read or validated.
- The database pool is raised to at least 8 connections whatever `pgx` would pick; the size has no variable.

### Secrets and files (portal)

| File | Variable | Format | Reloaded at runtime |
|---|---|---|---|
| pgpass | `PGPASSFILE` | libpq format, as above (login role `portal_app`, member only of `platform_portal`) | No, same as above |
| JWKS CA bundle | `PORTAL_JWKS_CA_FILE` | PEM certificates; a file without a valid certificate stops start-up | No, read once at start-up |
| Probe CA bundle | `PORTAL_PROBE_CA_FILE` | PEM certificates | No, read once at start-up |

CA bundles are public material: no permission check (a ConfigMap mount is fine).

Mounted by `platform-gitops/base/`: `portal-pgpass` (`pgpass`) at `/etc/portal/pgpass/pgpass`. The overlay of this repo also mounts the ConfigMap `platform-jwks-ca` (`ca.crt`) at `/etc/portal/jwks-ca/` for `PORTAL_JWKS_CA_FILE`.

## portalctl

CLI client of the portal API (`portalctl grant|revoke <email|robot:name> <initiative-key> <role-key>`). Both variables are required; the token is never a flag.

| Variable | Required | Default | Format and validation | Effect |
|---|---|---|---|---|
| `PORTAL_URL` | yes | none | `https://host[:port]` with an optional trailing `/`; no credentials, path, query or fragment | The portal host; any other scheme is refused and redirects are never followed |
| `PORTAL_API_TOKEN` | yes | none | a personal or robot API token | Sent as a bearer token to `PORTAL_URL` only |

A bad value exits with status 2 and a usage message. The private CA of the portal host is trusted through the Go system roots (`SSL_CERT_FILE`, `SSL_CERT_DIR`).

## hello (example app)

`platform/examples/hello`, the minimal app behind the platform ([README](../../platform/examples/hello/README.md)). It shows the minimum an app needs to validate assertions.

| Variable | Required | Default | Format and validation | Effect |
|---|---|---|---|---|
| `HELLO_LISTEN_ADDR` | yes | none | listen address, for example `:8080` | Where the app listens |
| `HELLO_APP_ID` | yes | none | the stable app id | The `aud` the assertion must carry |
| `HELLO_ISSUER` | yes | none | the platform issuer, for example `https://auth.apps.example.com` | Exact `iss` match |
| `HELLO_JWKS_URL` | yes | none | in-cluster JWKS URL (https recommended) | Where the verifier fetches keys; plain `http` logs a WARN |
| `HELLO_JWKS_CA_FILE` | no | unset (system roots) | PEM path; only valid with an `https` `HELLO_JWKS_URL` | CA bundle for a JWKS signed by a private CA; replaces the system roots. Read once at start-up |

Missing required variables are reported together.
