# Operate atrium

Day-two tasks for the operator of an atrium install ([install.md](install.md)): grant access from a terminal, upgrade, back up, monitor, harden and troubleshoot. Putting an app behind atrium is in [onboard-an-app.md](onboard-an-app.md).

| I want to | Go to |
|---|---|
| Grant or revoke access from a script | [portalctl](#portalctl) |
| Ship a new version | [Upgrade](#upgrade) |
| Not lose the install | [Backup and restore](#backup-and-restore) |
| Alert on platform health | [Monitoring](#monitoring) |
| Rotate the Google client secret | [Runbook](runbooks/google-client-secret-rotation.md) |
| Close the gaps before production | [Production hardening](#production-hardening) |
| Fix a symptom | [Troubleshooting](#troubleshooting) |

## portalctl

`portalctl` grants and revokes initiative access through the portal API. It is a plain HTTP client of the portal host, nothing else. Build a static binary:

```bash
cd platform/api && CGO_ENABLED=0 go build -o portalctl ./cmd/portalctl
```

Usage: `portalctl grant|revoke <email|robot:name> <initiative-key> <role-key>`. The environment holds:

| Variable | Value |
|---|---|
| `PORTAL_URL` | `https://` plus the portal host only, for example `https://portal.apps.example.com` |
| `PORTAL_API_TOKEN` | A personal or robot API token. Never a flag |

Create the token in the portal (**API tokens** on the home page for your own, **Robots** for a robot; the secret is shown once and tokens are created from a browser session) and keep it out of the shell history:

```bash
export PORTAL_URL=https://portal.apps.example.com
read -rs PORTAL_API_TOKEN && export PORTAL_API_TOKEN
portalctl grant you@example.com my-initiative viewer
portalctl revoke robot:ci-deployer my-initiative editor
```

Who can use it: a platform admin (`admin`-scope token), an initiative manager (`admin`-scope token, only for the initiatives they manage) or a robot holding a `manages_access` role ([who administers what](reference/api.md#who-administers-what)). Granting by email needs the user to have signed in once.

Behavior and exit codes:

- It is idempotent. The output is `changed` or `unchanged`, then `grant done: ...` or `revoke done: ...`. An existing grant with a future expiry is `unchanged` and prints a warning on stderr.
- Exit `0` on success; `1` for any runtime or API failure (unknown initiative, role or robot, a grant to a disabled robot, an expired existing grant: revoke it first); `2` for usage (bad arguments, invalid `PORTAL_URL`, empty token).
- It never follows redirects and honors `SSL_CERT_FILE` for a private CA.

Platform-api sees grant changes through `NOTIFY`, with no restart.

## Upgrade

Both `platform-api` and `portal` refuse to start when the database roles drift from what the binary expects, so the schema must be migrated first. Per release:

1. Build and push the new images ([install.md, section 2](install.md#2-build-the-images)) and update the tags in your overlay.
2. Delete the previous `migrate` Job (a Job is immutable), apply the new one and wait for it:

   ```bash
   kubectl -n platform delete job migrate --ignore-not-found
   kubectl apply -k deploy/prod/migrate
   kubectl -n platform wait --for=condition=complete job/migrate --timeout=180s
   ```

3. Roll out `platform-api`, wait for it, then `portal` ([install.md, section 8](install.md#8-deploy-and-verify)).

`base/` has no ordering mechanism; enforce it in your GitOps with an Argo CD `PreSync` hook (or sync-wave `-1`) on the Job, or a Flux `Kustomization` for `migrate` that the `platform-api` one `dependsOn` with `wait: true`. Setting `ttlSecondsAfterFinished` on the Job also recreates it per release. Rolling back a migration is not documented: take a backup before upgrading.

## Backup and restore

All state is in PostgreSQL: initiatives, apps, grants, users, sessions, the audit log and the sealed signing keys. The only thing outside it is the KEK, a Kubernetes Secret.

- **Database.** Back up the `platform` database with your usual PostgreSQL tooling (`base/postgres` is a single instance without backups; plan this yourself). The roles are cluster-level objects and are not part of a database dump: recreate them as in [install.md, section 4](install.md#4-database-and-roles) on the target server before restoring, then run the `migrate` Job.
- **KEK.** Back it up with the database, in a different place, and never regenerate it. The signing keys in `signing_keys` are sealed with it ([keys](reference/keys.md#storage)). A database backup restored with a different KEK holds keys nobody can unseal: platform-api reports a KEK mismatch (readiness reason `kek mismatch`), fails readiness and refuses to rotate or revoke. No procedure in this repository replaces or re-seals the keys without the original KEK. Treat the KEK and the database dump as one unit.
- **Without a database backup** you lose the whole catalog (initiatives, roles, apps, hosts, grants) and the audit trail. Users are recreated on their next login, with no grants.

Restore was not exercised in this repository: test it on a scratch cluster before you depend on it.

## Key rotation

Nothing to do. Signing keys rotate automatically every 30 days, one replica at a time, and are published in the JWKS before they sign. Emergency rotation and the failure rules are in [keys.md](reference/keys.md#lifecycle). Watch the `SigningKeyNotLoaded` and `SigningKeyKEKMismatch` alerts ([Monitoring](#monitoring)). Rotating the KEK itself has no shipped procedure.

## Monitoring

`platform-gitops/monitoring/` holds the artifacts. Nothing there is specific to one cluster. Metric catalog and the cardinality rule: [metrics.md](reference/metrics.md).

| File | Use |
|---|---|
| `rules/portal-platform.rules.yaml` | Alert rules in plain Prometheus format (the source) |
| `operator/prometheusrule.yaml` | The same rules as a `PrometheusRule`, generated by `gen-prometheusrule.sh` |
| `operator/podmonitors.yaml` | Scrape of `platform-api` and `portal` on the pod port named `metrics` (`job` is the app name) |
| `dashboards/portal-platform.json` | Grafana dashboard (datasource picked by variable); `dashboards/kustomization.yaml` wraps it in a ConfigMap labelled `grafana_dashboard` for a sidecar |
| `rules/tests/portal-platform.test.yaml` | `promtool` unit tests: every rule has a firing and a not-firing case |
| `test-rules.sh` | Sync check, `promtool check rules` and `promtool test rules` (pinned `prom/prometheus` through Docker) |

Assumptions to adapt:

- Rules and dashboard expect `job="platform-api"` and `job="portal"` (the PodMonitors set it with `jobLabel`). Edit `namespaceSelector` in the PodMonitors for your namespaces.
- The per-replica rules (`SigningKeyNotLoaded`, `SigningKeyKEKMismatch`, `DBPoolSaturated`) put `{{ $labels.pod }}` in their text. The PodMonitor adds `pod`; a scrape config that does not must relabel it.
- The metrics port (9090) is open only to pods labelled `app.kubernetes.io/name=prometheus` in a namespace named `monitoring` (`allow-ingress-metrics` in `platform` and `portal`); `base/monitoring` has the NetworkPolicies for that namespace. Anything that can run a pod with that label there joins the set: keep the namespace to the stack. Patch the policies if your Prometheus differs.
- If your Prometheus selects every `PodMonitor` and `PrometheusRule` of a namespace, anyone who can create those objects changes what is scraped or alerted. Restrict creation by RBAC or label-select.
- `ProbeNotWriting` fires when `time() - max(portal_probe_last_write_timestamp_seconds)` exceeds 3 times `portal_probe_interval_seconds` (0 means the probe is disabled and it never fires; missing gauges are silent), for 10 minutes. A restart resets the timestamp, so detection can be delayed by up to 3 intervals. The portal rules assume one portal release per Prometheus (`job="portal"`).
- After editing the plain rules run `monitoring/gen-prometheusrule.sh`; `test-rules.sh` fails when the generated file is stale.
- The `LoginsFailing` alert links the [Google client secret runbook](runbooks/google-client-secret-rotation.md).

### Grafana behind ForwardAuth

Grafana can be an app of atrium: it authenticates the request with the `Platform-Assertion` header and needs no login of its own. This is [onboard-an-app.md](onboard-an-app.md) with Grafana's JWT auth as the validator. **Not verified outside the local cluster.**

- Register an initiative whose role keys equal what your `role_attribute_path` matches, an app for Grafana and its host (`aud` is the app id). Route the host through the entrypoint that runs ForwardAuth. The Grafana Service must not be reachable any other way (NetworkPolicy ingress from the proxy only; no LoadBalancer or NodePort), or anyone can send the header.
- Configure `[auth.jwt]` with `header_name = Platform-Assertion`, `jwk_set_url` over HTTPS (mandatory outside dev mode), `tls_client_ca` for a private CA, `expect_claims` with the exact `PLATFORM_ISSUER` and app id, `role_attribute_strict`, and `url_login = false`. `GF_AUTH_JWT_*` environment variables work the same as the ini.
- Turn off every other way in (login form, basic auth, anonymous, other `[auth.*]`) and decide your break-glass path.
- Let Grafana reach the JWKS Service (egress) and the JWKS listener admit it (ingress, by pod selector, not by the app-namespace label).

## Internal TLS for the JWKS

By default `base/` serves the JWKS over plain HTTP inside the cluster; it carries no PKI, so you choose one. TLS is recommended for production and is **not verified outside the local cluster** with your PKI. The contract is in [keys.md](reference/keys.md#jwks-endpoint) and [configuration.md](reference/configuration.md).

- **Server.** Any issuer that writes a PEM certificate (chain allowed) and key (mode 0640 or stricter) at the two paths of `PLATFORM_JWKS_TLS_CERT_FILE` and `PLATFORM_JWKS_TLS_KEY_FILE`: a cert-manager `Certificate` whose `dnsNames` are the JWKS Service names and whose `secretName` yields `tls.crt` and `tls.key` for the mount, a Vault agent template, or a hand-made `kubernetes.io/tls` Secret. The SAN must equal the name apps use. Mount the Secret with `defaultMode: 0440` and a pod `fsGroup`. The listener then serves HTTPS only, TLS 1.3 minimum, and reloads the pair every 30 s, so leaf rotation needs no restart.
- **Clients.** Distribute the issuing CA bundle (PEM) to each app namespace and point the verifier at it: `PORTAL_JWKS_CA_FILE`, `HELLO_JWKS_CA_FILE` or `Config.RootCAs` in the SDK. Options: a cert-manager `trust-manager` Bundle, a Vault or CSI mount, or a ConfigMap per namespace. Omit it when the CA is system-trusted.
- **CA rotation** needs more care than leaf rotation: verifiers load the CA once at start-up, so distribute the new CA first, restart the verifiers, and only then serve the new leaf. Bundle old and new CA during the transition.
- Switch the JWKS URLs to `https://` in the same change. An `http` URL logs a warning at verifier start-up.

## Production hardening

Open items are tracked in [TODO.md](../TODO.md); read both lists before going live.

- **Open items.** Read both lists in [TODO.md](../TODO.md) ("Before production" and "Adopter bootstrap gaps in `base/`"); the manual steps are also in [install.md](install.md#11-known-limitations-before-production).
- **Egress to Google is not in `base/`.** `base/` denies all egress, so without a policy of your own login answers 503 and `LoginProviderUnavailable` fires. Model yours on `overlays/local-google/platform/networkpolicy-egress-google.yaml`, whose `except` list is specific to the local k3d ranges ([install.md](install.md#7-write-your-overlay)). Standard NetworkPolicy cannot match FQDNs, so that policy allows TCP 443 to any address outside your private ranges: a compromised platform-api could reach any public host on 443 (exfiltration), not only Google. Narrowing it needs an egress proxy with an FQDN allowlist or a CNI with L7 or FQDN policies. The `migrate` Job needs only PostgreSQL. The reference policy is IPv4 only.
- **Client IP.** Without a load balancer that preserves the source address, all users share one `/login` rate-limit bucket: [install.md](install.md#6-traefik).
- **KEK.** It is readable by any principal with cluster-wide `secrets get` (for example a Prometheus Operator with default RBAC): audit the ClusterRoles that can read Secrets in `platform`, or keep it in an external KMS.

## Troubleshooting

| Symptom | Cause | Fix | More |
|---|---|---|---|
| `/login` or `/oauth2/callback` answers 503 with `Retry-After` | Google discovery is not ready yet, or Google is unreachable, answered 5xx or 429 at the token endpoint, or redirected. Sessions that already exist keep working | Wait and retry. If it persists, check egress from `platform-api` to `accounts.google.com`, `oauth2.googleapis.com` and `www.googleapis.com`, and Google's status. The `LoginProviderUnavailable` alert fires for this | [login](reference/login.md#configuration-and-availability) |
| A portal API call answers 503 with `Retry-After` | A mutation lost against concurrent transactions on every attempt | Retry; mutations are idempotent by natural key (creating an app is not: check for a duplicate) | [API](reference/api.md#conventions) |
| platform-api not ready: log "this replica was started with the wrong KEK: signing and rotation disabled", readiness reason `kek mismatch`, alert `SigningKeyKEKMismatch` | The KEK mounted differs from the one that sealed the stored signing keys. The replica stops signing and refuses to rotate or revoke | Mount the original KEK back. Never regenerate it | [keys](reference/keys.md#failure-and-consistency-rules), [backup](#backup-and-restore) |
| platform-api or portal refuses to start: "refusing to run: the database role is not the least-privilege role ..." | The role has missing or extra privileges (migrations did not complete, or privileges were changed by hand), or the login role is a superuser, bypasses RLS or owns tables | Run the `migrate` Job to completion before the rollout. Connect with a login role that is a member of the runtime role only, created as in the install guide | [install](install.md#4-database-and-roles), [data model](reference/data-model.md#database-roles) |
| A signed-in user gets 403 on an app | Users are created on first login with no grants, or the grant expired, or the user, app or initiative is disabled or inactive | Grant a role; check the app health in the portal | [onboard](onboard-an-app.md#5-grant-access), [ForwardAuth](reference/forwardauth.md#decision-table) |
| `/login` answers 429 | The per-client rate limit. If it hits many users at once, Traefik sees one client address (load balancer or NAT that hides the source), so all users share one bucket | Honor `Retry-After`. Preserve the client address up to Traefik | [TODO.md](../TODO.md), [login](reference/login.md#rules) |
| platform-api refuses to start with "`PLATFORM_FAKE_LOGIN=false` requires Google login: set `PLATFORM_GOOGLE_CLIENT_ID`, `PLATFORM_GOOGLE_CLIENT_SECRET_FILE` and `PLATFORM_ALLOWED_HD`" | No Google variable is set at all (the fake login is off by default, by design) | Set the three variables together | [configuration](reference/configuration.md#platform-api), [install](install.md#7-write-your-overlay) |
| platform-api refuses to start with "`<VAR>` is required when Google login is configured", "`PLATFORM_GOOGLE_CLIENT_SECRET_FILE`: the file is empty" or another file error | Some Google variables are set but one is missing, empty or unreadable (the secret file must exist, be non-empty and have mode 0640 or stricter) | Fix the named variable or file; all three are required once one is set | [configuration](reference/configuration.md#platform-api) |
| platform-api refuses to start with "`PLATFORM_ISSUER` must be exactly `https://<PLATFORM_AUTH_HOST>`" | The issuer is not `https://` plus the auth host, exactly: scheme case, port, path, query, trailing slash or credentials | Set `PLATFORM_ISSUER` to `https://auth.apps.example.com` for `PLATFORM_AUTH_HOST=auth.apps.example.com`. `PORTAL_ISSUER` and every app's issuer must carry the same value | [configuration](reference/configuration.md#platform-api) |
| Health of an app shows `unprotected`, `unreachable` or `never_seen` | The route is outside the ForwardAuth entrypoint, the portal cannot reach Traefik or its certificate (private CA: set `PORTAL_PROBE_CA_FILE`), or no traffic yet | See the finding table | [onboard](onboard-an-app.md#6-verify), [metrics](reference/metrics.md#health-of-an-app) |
| New logins fail after a rotation of the Google client secret | The deployed secret is wrong, disabled or deleted; `invalid_client` in the logs | Roll back as in the runbook | [runbook](runbooks/google-client-secret-rotation.md#rollback) |

## Runbooks

- [Rotate the Google OAuth client secret](runbooks/google-client-secret-rotation.md)
