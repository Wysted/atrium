# TODO

Pending work and open questions for the portal platform. Detailed context lives in
`platform/docs/contract.md` (block 5 "Open items") and in Engram (project `portal`).

## Before production

- [ ] **Google `hd` with alias and secondary domains.** Google does not document which `hd` value an ID token carries
  when the user signs in with an alias or secondary-domain address. Verified only for a primary-domain user of
  Cloud Identity Free (`wysted.dev`). Test with real alias and secondary-domain accounts of the target Workspace,
  decode the ID token, and adjust `PLATFORM_ALLOWED_HD` if needed. Rejections already log `hd` and email.
- [ ] **Suspended Workspace users.** Today access is cut by the local `disabled` flag plus the 8 h session cap.
  A faster cutoff needs the Admin SDK Directory API (`users.get`, `admin.directory.user.readonly`, domain-wide
  delegation granted by a super admin). Decide together with the company's offboarding process.
- [ ] **KEK readable through cluster-wide Secret access.** The KEK is a Kubernetes Secret, so any principal with
  cluster-wide `secrets get` (operators such as the Prometheus Operator, which grants it by default) can read it and
  unseal every signing key. Production should keep the KEK in an external KMS (envelope decryption via the KMS API),
  or at least audit which ClusterRoles can read Secrets in `platform`.
- [ ] **Real client IP.** In k3d, Traefik sees every client as `10.42.0.1`, so the `/login` rate limit acts as a
  global bucket. Verify the client IP behind the production load balancer.
- [ ] **Egress filtering.** The `local-google` overlay allows platform-api HTTPS egress to any public address.
  FQDN-level filtering (Google endpoints only) needs an egress proxy or a CNI with L7 policies.
- [ ] **Internal TLS for the JWKS.** Apps fetch the JWKS over in-cluster HTTP; integrity relies on NetworkPolicy.
- [ ] **`client_secret` rotation runbook.** Google allows two active secrets: add, deploy, disable the old one, delete.
- [ ] **NOTIFY commit serialization.** The `access_changed` and `signing_keys_changed` triggers use NOTIFY, and
  PostgreSQL serializes the commit of every notifying transaction on one cluster-wide lock (waits of up to ~3 s were
  seen under disk load in tests). Admin changes are rare today; measure under the production write rate before
  adding notifying triggers to hot tables.
- [ ] **Git remote.** The repository only exists on the local disk.

## Next features

- [x] **Portal app.** Closed 2026-10-16. Contract block 6: schema, API, integration health, `/metrics`, the probe,
  the k3d deploy and the embedded Nuxt 4 SPA (`platform/web/`: home, administration, integration health) served with
  a startup-computed CSP. The local smoke checks it in a real browser (Playwright, `overlays/local/browser-check/`).
- [ ] **Retire `grant.sh` and `revoke.sh`.** The portal replaces `platform-gitops/overlays/local-google/grant.sh` and
  `revoke.sh`; remove them after granting and revoking once through the portal with a real Google login.
- [ ] **Portal web follow-ups.** Done 2026-10-16: group search (`GET /api/v1/groups?q=`) in the grant dialog and on
  the groups page, and the group page shows the group's name (`GET /api/v1/groups/{id}`). Open by decision: the apps payload
  carries no portal-app flag, so the UI shows Disable and Remove host on the portal app and relies on the 409.
  `npm audit` reports high advisories in Nuxt's build tooling (`braces`, `node-forge` via nitropack/listhen) with no
  upstream fix; they never reach the embedded output. Recheck on each Nuxt upgrade.
- [ ] **Non-browser clients for the portal API.** CLI and automation have no way to authenticate yet (block 6 open items).
- [x] **Flaky test database cleanup.** Closed 2026-10-16. Root cause: every `DROP DATABASE` makes PostgreSQL run an
  immediate checkpoint and wait for it, and the checkpoint's fsync phase covers the files of every live test database
  (hundreds each) across all parallel test binaries. On Docker/WSL storage that phase takes seconds, and a slow one
  lets databases pile up, which slows the next checkpoint (feedback loop) until a `t.Cleanup` drop hits its 30 s
  timeout; every binary stalls at once. Evidence: `pg_stat_activity` showed DROPs in `IPC:CheckpointStart/Done`,
  drops of 7-8 s in normal runs and 16 s under disk load; the same load with `fsync=off` gave checkpoints under 1.6 s.
  Fix: the dev `postgres` service in `compose.yaml` runs with `fsync=off`, `full_page_writes=off`,
  `synchronous_commit=off` (full run 35 s -> 15 s, 20/20 clean runs, no leftover databases); `testdb` errors now state
  step, database and elapsed time. Deferring drops to the end of a binary was tried and is worse (live databases make
  each checkpoint slower). Related, not fixed: `access_changed`/`signing_keys_changed` triggers use NOTIFY, which
  serializes commits cluster-wide on one lock; timing-sensitive app tests (`TestGoogleCallbackProviderUnavailable`)
  can fail when the disk is saturated by other work.
- [x] **Align `deployments.host` CHECK with `hostname.Valid`.** Closed 2026-10-16. Migration 0011 replaces the CHECK
  with `deployments_host_valid` (anchored label regex, no all-digit last label, `octet_length <= 253`), added NOT VALID
  and validated in the same transaction so existing bad rows fail the migration loudly (remediation query in the
  migration). `TestDeploymentsHostCheckMatchesHostnameValid` runs a shared case list through both, as owner and portal.
- [x] **Separate JWKS listener for platform-api.** Closed 2026-10-16. `PLATFORM_JWKS_ADDR` (required) serves only the JWKS; the internal listener answers 404 for it, so `/forwardauth` and `/readyz` stay off the port app namespaces reach. Original note: App namespaces reach platform-api's internal port 8081 for the JWKS,
  and a NetworkPolicy filters by port, not path, so they also reach `/forwardauth` and `/readyz`. Not exploitable
  today (an app holds no valid cookies for other hosts and app sessions are host-bound), but serving the JWKS on its
  own port would keep `/forwardauth` reachable only from Traefik.
- [x] **Probe interval gauge for alert rules.** Closed 2026-10-16. The portal exposes `portal_probe_interval_seconds`
  (0 = disabled) and `portal_probe_last_write_timestamp_seconds`; `ProbeNotWriting` fires when the last write is older
  than 3 intervals and never when the probe is disabled.
- [x] **Reduce `platform_runtime`.** Closed 2026-10-16. Migration 0010 leaves it read-only on the authorization tables
  plus column-level UPDATE where login, sessions and last-seen need it; `CheckRuntimeRole` refuses to start on any drift.
- [x] **Cookie prefix support in Safari.** Closed 2026-10-16: keep `__Host-Http-`. The prefix is defined in
  `draft-ietf-httpbis-layered-cookies`; Chrome 140+ enforces it, Safari has no verified enforcement, and a browser
  without it still applies the `__Host-` rules (the name starts with `__Host-`), so nothing breaks. Firefox skipped the
  HttpOnly check from 144 until 150/151 (CVE-2026-6768), so the prefix is hardening, never the only guarantee: the
  server always sets `Secure; HttpOnly; Path=/` without `Domain`.
- [x] **Grafana behind ForwardAuth.** Closed 2026-10-16, local overlay only. Grafana is the app `grafana` of the initiative
  `observability` (roles viewer/editor/admin map to the org roles), `[auth.jwt]` verifies `Platform-Assertion` through the
  HTTPS JWKS with the local CA, the login form and basic auth are off, and a NetworkPolicy leaves Traefik as the only way in.
  Smoke section 19b and a browser scenario cover it; adopter notes in `platform-gitops/README.md` "Grafana behind ForwardAuth".
  Research that fed it: `[auth.jwt]` selects the key by `kid`, ignores the JOSE `typ` (acceptable: the platform key signs only
  `Platform-Assertion`), has a fixed 60 s leeway, and needs `role_attribute_path` to return one string.
