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

- [ ] **Administration panel (next).** Manage initiatives, apps, deployments, roles, groups and grants, and browse the
  audit log. Today this is done with `platform-gitops/overlays/local-google/grant.sh`, `revoke.sh` and SQL.
  Define its contract before writing code (new block in `platform/docs/contract.md`), answering:
  - Who administers what: platform admins (`platform_admins`) vs initiative owners (`initiative_roles.manages_access`),
    and which actions each one can take.
  - Shape: JSON API only, or API + web UI (`platform/web/`, React + Vite), and on which host (the auth host or a
    dedicated admin app behind the same ForwardAuth).
  - Authorization of admin calls: reuse the assertion and AuthZEN-shaped decision, CSRF protection for mutations.
  - Every mutation writes `audit_events` with the acting user; grants by email only for users that already exist.
  - Portal home ("which apps can I open"): AuthZEN `search/resource` over the same model.
- [ ] **Audit for `RevokeUser` / `RevokeApp`.** Only login, logout and session replacement are audited today.
- [ ] **Cookie prefix support in Safari.** `__Host-Http-` is enforced by Chrome 140+ and Firefox 143+; revisit Safari.
