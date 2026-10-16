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
- [ ] **Real client IP.** In k3d, Traefik sees every client as `10.42.0.1`, so the `/login` rate limit acts as a
  global bucket. Verify the client IP behind the production load balancer.
- [ ] **Egress filtering.** The `local-google` overlay allows platform-api HTTPS egress to any public address.
  FQDN-level filtering (Google endpoints only) needs an egress proxy or a CNI with L7 policies.
- [ ] **Internal TLS for the JWKS.** Apps fetch the JWKS over in-cluster HTTP; integrity relies on NetworkPolicy.
- [ ] **`client_secret` rotation runbook.** Google allows two active secrets: add, deploy, disable the old one, delete.
- [ ] **Git remote.** The repository only exists on the local disk.

## Next features

- [ ] **Portal app (next).** Home, administration and integration health, defined in contract block 6
  (`platform/docs/contract.md`). Replaces `platform-gitops/overlays/local-google/grant.sh`, `revoke.sh` and SQL.
  Includes the schema changes of block 6, the `app_disabled` decision reason, the `platform_portal` database role,
  `cmd/portal` with the embedded Nuxt 4 SPA (`platform/web/`), `/metrics`, the probe, and the audit of
  `RevokeUser` / `RevokeApp`. Create the frontend subagents when the web work starts.
- [ ] **Non-browser clients for the portal API.** CLI and automation have no way to authenticate yet (block 6 open items).
- [ ] **Flaky test database cleanup.** Under `go test -race ./...` the `DROP DATABASE ... WITH (FORCE)` in
  `internal/testdb` exceeded its 30 s timeout in 2 of ~6 full runs on 2026-10-15 (once across every package at
  the same instant). After the migrated-template change (2026-10-15) 10 consecutive full `-race` runs of
  `platform/api` did not reproduce it and left no `platform_test_*` databases; stale ones (older than 15 min) are now
  swept at the start of each test binary. Root cause not proven: PostgreSQL requests an immediate checkpoint on every
  `DROP DATABASE`, so parallel packages queue on checkpoint I/O (Docker/WSL). Seen again on 2026-10-16 (`TestDecisionTable`
  failing at 31.9 s in a full `-race` run, passing in isolation and in the next runs), so the template did not fix it.
  Next step: capture the failing message (`go test -json`) to tell a drop timeout from a create/connect timeout.
- [ ] **Reduce `platform_runtime`.** Once the portal owns administration, drop its write access to the authorization
  tables except what ForwardAuth and login need.
- [ ] **Cookie prefix support in Safari.** `__Host-Http-` is enforced by Chrome 140+ and Firefox 143+; revisit Safari.
