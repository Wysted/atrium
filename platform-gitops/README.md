# platform-gitops

Kustomize manifests of the platform for the local k3d cluster `portal`. Everything runs through `./tk`.

```
base/namespaces    platform-db, platform, hello-a, hello-b (Pod Security restricted)
base/postgres      StatefulSet postgres:18.6 (digest = compose.yaml), initdb.sh, NetworkPolicy
base/migrate       Job `platform-api migrate` as platform_owner
base/platform-api  Deployment x2, Services internal:8081 / public:8080, Middlewares, auth-host IngressRoute, NetworkPolicy
base/hello         reference app (one instance per namespace via overlay)
overlays/local     domain/app-id patches, scripts, seed, traefik values (fake login)
overlays/local-google  real Google login on top of overlays/local: image tag, client secret, env, egress policy, grant/revoke
```

## Scripts (overlays/local)

| Script | Purpose |
|---|---|
| `up.sh` | secrets (`.tools/secrets/k3d-*`, generated once), image import (`BUILD=1` rebuilds), apply in order (namespaces, DB, migrate Job, platform-api, hello-a/b), seed, `down-spike.sh`, Traefik helm upgrade |
| `seed.sh` / `seed.sql` | dev data as platform_owner; idempotent, also restores grants revoked by the smoke |
| `smoke.sh` | end-to-end checks with curl from the host; leaves the cluster as found |
| `down-spike.sh` | deletes spike-authz / spike-app1 / spike-app2 (manifests stay in `spikes/`) |

Order matters: the Middlewares must exist before Traefik is upgraded with `traefik/entrypoint-values.yaml`.
Rollback of the edge: `./tk helm upgrade traefik traefik/traefik --version 41.7.1 -n traefik -f local/traefik-values.yaml`.

## Seed

`demo` (viewer, editor) owns hello-a; `other` (viewer) owns hello-b. alice: direct viewer on both. bob: only through group
`demo-editors` (editor on demo): hello-a yes, hello-b no (grants never cross initiatives). carol: no grants.

## Notes

- Namespaces that host apps need the label `portal.wysted.dev/app-namespace=true`: the internal listener (JWKS) accepts them.
- Access log: `accessLog.fields.queryParameters.defaultMode: drop` (Traefik >= 3.7.3) removes the whole query from
  `RequestPath`, so the login `code`, `bind` and `rd` are never logged. Tradeoff: no query at all in access logs (it also
  hides benign parameters). Headers (Cookie, Set-Cookie, Location) are dropped by default.
- Client IP: in k3d Traefik sees `10.42.0.1` (the node's CNI gateway) for every client, because the host port goes through
  the k3d serverlb proxy and ServiceLB SNAT. The `/login` rate limit (last `X-Forwarded-For` entry) therefore behaves as
  one global bucket locally. A real load balancer that preserves the source address is needed for per-client limits.
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
