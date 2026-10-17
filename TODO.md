# TODO

Pending work and open questions for atrium. Detailed context lives in
`docs/reference/login.md` ("Limitations"). History of closed items lives in git.

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
  global bucket. Verify the client IP behind the production load balancer. The per-IP rate limit on failed API tokens also waits for it.
- [ ] **Egress filtering.** The `local-google` overlay allows platform-api HTTPS egress to any public address.
  FQDN-level filtering (Google endpoints only) needs an egress proxy or a CNI with L7 policies.
- [ ] **NOTIFY commit serialization.** The `access_changed` and `signing_keys_changed` triggers use NOTIFY, and
  PostgreSQL serializes the commit of every notifying transaction on one cluster-wide lock (waits of up to ~3 s were
  seen under disk load in tests). Admin changes are rare today; measure under the production write rate before
  adding notifying triggers to hot tables.
- [ ] **Enable private vulnerability reporting.** In the GitHub repository, Settings > Code security. `SECURITY.md`
  points reporters to it.
- [ ] **Local TLS for contributors.** Go modules (`github.com/Wysted/atrium/...`, go-gettable) and the namespace label
  prefix (`atrium.wysted.dev/`) were renamed 2026-10-17. Left: the local domain `*.apps.wysted.dev` gets its wildcard
  certificate through the maintainer's Cloudflare token, so a contributor cannot reproduce the local TLS setup. Option:
  a self-signed local CA when no token is present.

## Adopter bootstrap gaps in `base/`

Found while writing `docs/install.md`. Adopters work around each one by hand today.

- [ ] **Fake login is the default in `base/`.** `base/platform-api` sets `PLATFORM_FAKE_LOGIN=true` (the comment
  "Google login arrives in a later part" is stale): the fake login accepts any email, so an adopter who forgets the
  Google patch gets an open login with no error. Default to `false` in `base/` and set `true` in `overlays/local`.
- [ ] **No migration creates the `platform` initiative, the portal app and its deployment.** Without the deployment
  row the portal host does not resolve, so the API cannot create it (chicken and egg). Only `overlays/local/seed.sql`
  (development data) does it; adopters INSERT by hand.
- [ ] **`platform_portal` and `portal_app` exist only in `overlays/local/roles.sql`.** `initdb.sh` creates three
  roles; migration 0009 needs `platform_portal` and `platform_owner` has no `CREATEROLE`.
- [ ] **NetworkPolicy selectors are fixed** to `kube-system`/`k8s-app=kube-dns`, `traefik`/`app.kubernetes.io/name=traefik`,
  the Traefik pod port 8443, `platform-db` and `monitoring`/`prometheus`.
- [ ] **Egress to Google exists only in `overlays/local-google`**, and its `except` list is k3d-specific.
- [ ] **Hardcoded in `base/`:** `PGHOST`, `PGSSLMODE=disable`, `PORTAL_PROBE_ADDR`, `PORTAL_JWKS_URL`, and
  `storageClassName: local-path` with 2Gi.
- [ ] **Migrate-before-platform-api ordering is not enforced in `base/`** (README "Ordering"): needs a PreSync hook or
  `dependsOn` in the adopter's GitOps.

## Next features

- [ ] **`npm audit` in the web build tooling.** High advisories in Nuxt's build tooling (`braces`, `node-forge` via
  nitropack/listhen) have no upstream fix; they never reach the embedded output. Recheck on each Nuxt upgrade.
- [ ] **Non-browser clients, phases 2 and 3.** Phase 1 (personal and robot tokens, `portalctl`) is implemented; see
  `docs/architecture.md` ("Non-browser clients") and `docs/decisions/0008-opaque-api-tokens-and-robots.md`. Open: phase 2 (workload identity) and phase 3 (device flow).
