# platform-gitops

Kustomize manifests of the platform for the local k3d cluster `portal`. Everything runs through `./tk`.

```
base/namespaces    platform-db, platform, hello-a, hello-b (Pod Security restricted)
base/postgres      StatefulSet postgres:18.6 (digest = compose.yaml), initdb.sh, NetworkPolicy
base/migrate       Job `platform-api migrate` as platform_owner
base/platform-api  Deployment x2, Services internal:8081 / public:8080, Middlewares, auth-host IngressRoute, NetworkPolicy
base/hello         reference app (one instance per namespace via overlay)
overlays/local     domain/app-id patches, scripts, seed, traefik values
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
