# TODO

Pending work and open questions for atrium. Detailed context lives in
`docs/reference/login.md` ("Limitations"). History of closed items lives in git.

## Before production

- [ ] **Google `hd` with secondary domains.** Google does not document which `hd` value an ID token carries for a
  user whose primary address is in a secondary domain. Verified only for a primary-domain user of Cloud Identity Free
  (`wysted.dev`). Email aliases and user alias domains are not a case: Google signs in only with the primary address
  (tested 2026-10-18: an alias is "account not found"). Test with a real secondary-domain user of the target Workspace
  and adjust `PLATFORM_ALLOWED_HD` if needed. Rejections already log `hd` and email.
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

## Adopter bootstrap gaps in `base/`

Found while writing `docs/install.md`.

- [ ] **Cluster-specific defaults in `base/` that every adopter patches** (documented in `docs/install.md`, sections 4
  and 7): NetworkPolicy selectors (`kube-system`/`k8s-app=kube-dns`, `traefik`/`app.kubernetes.io/name=traefik`, Traefik
  pod port 8443, `platform-db`, `monitoring`/`prometheus`), egress to Google (only in `overlays/local-google`, with a
  k3d-specific `except` list), `PGHOST`, `PGSSLMODE=disable`, `PORTAL_PROBE_ADDR`, `PORTAL_JWKS_URL`, and
  `storageClassName: local-path` with 2Gi. Revisit if a second adopter repeats the same patches (a kustomize component
  would then remove real duplication).
- [ ] **Migrate-before-platform-api ordering is not enforced in `base/`** (README "Ordering"). Pods now stay not ready
  with reason `migrations pending` until the Job runs (fail closed); a PreSync hook or `dependsOn` in the adopter's
  GitOps is still needed for zero-downtime ordering.

## Next features

- [ ] **`npm audit` in the web build tooling.** High advisories in Nuxt's build tooling (`braces`, `node-forge` via
  nitropack/listhen) have no upstream fix; they never reach the embedded output. Recheck on each Nuxt upgrade.
- [ ] **Non-browser clients, phases 2 and 3.** Phase 1 (personal and robot tokens, `portalctl`) is implemented; see
  `docs/architecture.md` ("Non-browser clients") and `docs/decisions/0008-opaque-api-tokens-and-robots.md`. Open: phase 2 (workload identity) and phase 3 (device flow).
- [ ] **Apps in another cluster.** Today atrium and its apps share one cluster: `/forwardauth` trusts `X-Forwarded-*`,
  so only the same-cluster Traefik may reach it (NetworkPolicy, which cannot select pods of another cluster). Preferred
  model, no code change: a single ingress in the atrium cluster (its Traefik and internal ForwardAuth) routing over TLS
  to Services of the app cluster (ExternalName or load balancer address), the app cluster accepting traffic only from
  it, and apps validating the assertion with the public JWKS of the auth host (`/.well-known/jwks.json`). Document it
  in `docs/onboard-an-app.md` and test it with two clusters. A Traefik per cluster calling `/forwardauth` across the
  network would need mTLS and IP restriction on the internal listener: a separate security design.
