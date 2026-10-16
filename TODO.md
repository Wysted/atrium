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
- [ ] **Portal web follow-ups.** The group picker of the grant dialog has no search (pages of a native select);
  `GET /api/v1/groups/{id}` would let the group page show its name; the apps payload carries no portal-app flag, so the
  UI shows Disable and Remove host on the portal app and relies on the 409. `npm audit` reports high advisories in
  Nuxt's build tooling (`braces`, `node-forge` via nitropack/listhen) with no upstream fix; they never reach the
  embedded output. Recheck on each Nuxt upgrade.
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
- [ ] **Align `deployments.host` CHECK with `hostname.Valid`.** ForwardAuth rejects hosts failing `hostname.Valid` before
  the lookup, and `Valid` is stricter than the schema CHECK (`a..b`, a numeric last label). A host inserted by SQL
  that passes the CHECK but not `Valid` would never resolve. Add a matching CHECK in a new migration.
- [ ] **Separate JWKS listener for platform-api.** App namespaces reach platform-api's internal port 8081 for the JWKS,
  and a NetworkPolicy filters by port, not path, so they also reach `/forwardauth` and `/readyz`. Not exploitable
  today (an app holds no valid cookies for other hosts and app sessions are host-bound), but serving the JWKS on its
  own port would keep `/forwardauth` reachable only from Traefik.
- [x] **Probe interval gauge for alert rules.** Closed 2026-10-16. The portal exposes `portal_probe_interval_seconds`
  (0 = disabled) and `portal_probe_last_write_timestamp_seconds`; `ProbeNotWriting` fires when the last write is older
  than 3 intervals and never when the probe is disabled.
- [x] **Reduce `platform_runtime`.** Closed 2026-10-16. Migration 0010 leaves it read-only on the authorization tables
  plus column-level UPDATE where login, sessions and last-seen need it; `CheckRuntimeRole` refuses to start on any drift.
- [ ] **Cookie prefix support in Safari.** `__Host-Http-` is enforced by Chrome 140+ and Firefox 143+; revisit Safari.
- [ ] **Grafana behind ForwardAuth (auth.jwt validating `Platform-Assertion`: verify ES256 and `typ` support).** Grafana is
  only reachable by port-forward with a generated admin password. To publish it through Traefik as a regular app, Grafana's
  `auth.jwt` must validate the `Platform-Assertion` (header_name, JWKS URL of platform-api, ES256 signature) and its `typ`
  header; verify both are supported before relying on it, and keep the admin login off the public path.
