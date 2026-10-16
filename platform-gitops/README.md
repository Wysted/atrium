# platform-gitops

Kustomize manifests of the platform for the local k3d cluster `portal`. Everything runs through `./tk`.

```
base/namespaces    platform-db, platform, portal, hello-a, hello-b (Pod Security restricted)
base/postgres      StatefulSet postgres:18.6 (digest = compose.yaml), initdb.sh, NetworkPolicy
base/migrate       Job `platform-api migrate` as platform_owner
base/platform-api  Deployment x2, Services internal:8081 / public:8080, Middlewares, auth-host IngressRoute, NetworkPolicy
base/portal        Deployment x2 (cmd/portal), Service, IngressRoute (passHostHeader), NetworkPolicies
base/hello         reference app (one instance per namespace via overlay)
overlays/local     domain/app-id patches, scripts, seed, traefik values (fake login)
overlays/local-google  real Google login on top of overlays/local: image tag, client secret, env, egress policy, grant/revoke
```

## Scripts (overlays/local)

| Script | Purpose |
|---|---|
| `up.sh` | secrets (`.tools/secrets/k3d-*`, generated once), image import (`BUILD=1` rebuilds the three images), apply in order (namespaces, DB, portal roles, migrate Job, seed, platform-api, hello-a/b, portal), `down-spike.sh`, Traefik helm upgrade |
| `roles.sh` / `roles.sql` | `platform_portal` (NOLOGIN) and the `portal_app` login role (member of `platform_portal` only) as the bootstrap superuser; idempotent, works on an existing volume (initdb.sh only runs once) |
| `seed.sh` / `seed.sql` | dev data as platform_owner; idempotent, also restores grants revoked by the smoke |
| `smoke.sh` | end-to-end checks with curl from the host; leaves the cluster as found |
| `down-spike.sh` | deletes spike-authz / spike-app1 / spike-app2 (manifests stay in `spikes/`) |

Order matters: the Middlewares must exist before Traefik is upgraded with `traefik/entrypoint-values.yaml`.
Rollback of the edge: `./tk helm upgrade traefik traefik/traefik --version 41.7.1 -n traefik -f local/traefik-values.yaml`.

## Seed

`demo` (viewer, editor) owns hello-a; `other` (viewer) owns hello-b. alice: direct viewer on both. bob: only through group
`demo-editors` (editor on demo): hello-a yes, hello-b no (grants never cross initiatives). carol: no grants.

The reserved initiative `platform` owns the portal app (fixed id `app_01HQ3PD7RTA5M2XKWV9N8B4CYE`, host
`portal.apps.wysted.dev`); the same id is `PORTAL_APP_ID` (overlays/local/portal) and `PLATFORM_PORTAL_APP_ID` (overlays/local/platform).
`admin@example.com` (fake login) is a PLATFORM ADMIN so the smoke can exercise the admin API: development only, it lives in
the local overlay's `seed.sql`, never in base.

## Portal (base/portal)

- Namespace `portal`, labelled `portal.wysted.dev/app-namespace=true` like any app namespace: platform-api's internal listener
  (JWKS) already accepts it. `platform-db` accepts it too (`allow-ingress-platform` selects `platform` and `portal`).
- Database: login role `portal_app`, member only of `platform_portal`; password in `.tools/secrets/k3d-pg-portal-password`, pgpass
  Secret `portal/portal-pgpass` (`fsGroup` + `defaultMode: 0440`, like platform-api). The role is created by `roles.sh`, not by
  the migrations: `platform_owner` has no CREATEROLE and migration 0009 only creates `platform_portal` if it is missing.
- Probe: `PORTAL_PROBE_ADDR=traefik.traefik.svc.cluster.local:443`. The wildcard certificate is the real Let's Encrypt one, so the
  system roots verify it: no `PORTAL_PROBE_CA_FILE` and no `PORTAL_PROBE_INSECURE_TLS`. Local interval 1m (`PORTAL_PROBE_INTERVAL`).
- NetworkPolicies (default deny both ways): ingress only from Traefik to 8080 (kubelet probes on 8081 come from the node and are
  never filtered); egress to DNS, PostgreSQL 5432, platform-api 8081 (JWKS) and Traefik pods on 8443. Policies see the POD port: the
  Service port 443 of Traefik is `websecure` = 8443. The metrics port 9090 of platform-api and portal has no allow rule (monitoring
  gets its own in part 4b). The internal and metrics listeners have no Service.
- IngressRoute: exact `Host()`, `passHostHeader: true`; ForwardAuth and strip-assertion come from the entrypoint.

## Notes

- Namespaces that host apps need the label `portal.wysted.dev/app-namespace=true`: the internal listener (JWKS) accepts them.
- Access log: `accessLog.fields.queryParameters.defaultMode: drop` (Traefik >= 3.7.3) removes the whole query from
  `RequestPath`, so the login `code`, `bind` and `rd` are never logged. Tradeoff: no query at all in access logs (it also
  hides benign parameters). Headers (Cookie, Set-Cookie, Location) are dropped by default.
- Client IP: in k3d Traefik sees `10.42.0.1` (the node's CNI gateway) for every client, because the host port goes through
  the k3d serverlb proxy and ServiceLB SNAT. The `/login` rate limit (last `X-Forwarded-For` entry) therefore behaves as
  one global bucket locally. A real load balancer that preserves the source address is needed for per-client limits.
- Smoke and the `/login` rate limit: every login of a run (and of back-to-back runs) shares one bucket per platform-api replica
  (burst 30, one token per 2 s) because Traefik sees all clients as `10.42.0.1`. The smoke never raises the limit: `login()` waits
  out a 429 using `Retry-After` (bounded retries). At the end `smoke.sh` lists every failed check with its section.
- platform-api and portal have a `preStop` native sleep (5 s, distroless images have no `sleep` binary): the Service rules reach
  Traefik after SIGTERM, and without it a deleted replica answered "connection refused" to ForwardAuth (Traefik 500 once seen
  in smoke section 12). `terminationGracePeriodSeconds` 30 covers 5 s + the 20 s shutdown budget.
- A fresh pod is refused for 1-2 s when it connects to a destination protected by an ingress NetworkPolicy (kube-router
  has not yet synced it into the allow set): pods that talk to PostgreSQL wait with an initContainer (`pg_isready`).

## Real Google login (overlays/local-google)

Layers on `overlays/local` (which must have been deployed once with `local/up.sh`). Switching:

```
BUILD=1 platform-gitops/overlays/local-google/up.sh   # build portal-platform-api:dev-google, import, secret, migrate, rollout
platform-gitops/overlays/local-google/down.sh          # back to the fake login (removes egress policy and client secret)
```

`local/up.sh` re-applies the fake-login overlay: run `local-google/up.sh` again afterwards. `local/smoke.sh` only works in
fake-login mode (POST /login is 405 under Google). The client secret is read from `.tools/secrets/google-client-secret` and
loaded with `--from-file` into Secret `platform/platform-google-client-secret`, mounted with `fsGroup` + `defaultMode: 0440`
(same policy as the KEK). Env: `PLATFORM_GOOGLE_CLIENT_ID`, `PLATFORM_GOOGLE_CLIENT_SECRET_FILE`, `PLATFORM_ALLOWED_HD=wysted.dev`,
`PLATFORM_FAKE_LOGIN=false`. The image tag is overridden with the kustomize `images` transformer (base keeps `:dev`).

The portal needs nothing from this overlay (it only validates assertions): `local/up.sh` deploys it and it keeps running; any
active user may open it (reserved initiative `platform`, "any active user" rule). To use its admin API with a real user:
`local-google/admin.sh <email>` (user must have signed in once; `--revoke` removes it; DEVELOPMENT tooling, the portal has no
endpoint to manage platform admins).

Access: users are created on their first login (JIT) with no grants (403 on every app).
`local-google/grant.sh <email> <initiative-key> <role-key>` (as platform_owner via the postgres pod) inserts a direct
`initiative_access` row for an EXISTING user, idempotently, with an `audit_events` row (`access.grant`, `after.actor = "cli"`).
`revoke.sh` (or `grant.sh --revoke`) removes it. Platform-api sees the change through NOTIFY, no restart. Example:
`grant.sh you@example.com demo viewer` (demo = hello-a).

### Egress tradeoff (NetworkPolicy `allow-egress-google`)

Google discovery/token/JWKS need HTTPS to accounts.google.com, oauth2.googleapis.com and www.googleapis.com. A standard
NetworkPolicy cannot match FQDNs, so the policy allows TCP 443 from platform-api pods to `0.0.0.0/0` EXCEPT 10.0.0.0/8
(pods 10.42.0.0/16, services 10.43.0.0/16), 172.16.0.0/12 (k3d node and Docker network 172.28.0.0/16), 192.168.0.0/16,
100.64.0.0/10, 169.254.0.0/16 and 127.0.0.0/8. Consequence: a compromised platform-api could reach ANY public host on 443
(exfiltration), not only Google. Narrowing it needs an egress proxy with an FQDN allowlist or a CNI with L7/FQDN policies
(Cilium, Calico Enterprise); neither is installed (flannel + kube-router). The policy exists only in `local-google`;
the migrate Job is not covered (it only talks to PostgreSQL). IPv4 only (the cluster is single-stack).
