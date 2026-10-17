# Install atrium on your cluster

How to deploy atrium on a Kubernetes cluster you operate: Traefik with ForwardAuth, PostgreSQL, `platform-api` and the portal, with Google login. The repository ships manifests (`platform-gitops/base/`), not a Helm chart and no published images, so you build the images and write a small kustomize overlay.

> Everything here was exercised only on the maintainer's local k3d cluster. Steps that go beyond what `platform-gitops/` does there are marked **Not verified outside the local cluster**. Examples use `example.com` and `<registry>`.

## 1. What you need

| Need | Detail |
|---|---|
| Kubernetes | A CNI that enforces `NetworkPolicy` (ingress and egress). The security model depends on it ([architecture](architecture.md)). Tested on K3s v1.37 with kube-router |
| Traefik | v3.7.14 (Helm chart 41.7.1) with the `kubernetesCRD` provider (`traefik.io/v1alpha1` `IngressRoute`, `Middleware`). `accessLog.fields.queryParameters` needs >= 3.7.3. It must run in the namespace `traefik` with pods labelled `app.kubernetes.io/name: traefik` (hardcoded in the NetworkPolicies) |
| PostgreSQL | 18 or later (migrations use `uuidv7()`); 18.6 is tested. Direct connections only (`LISTEN`/`NOTIFY`). `base/postgres` is a single-instance dev-grade StatefulSet; for production prefer your own server (section 4) |
| TLS | A certificate valid for the auth host, the portal host and every app host. A wildcard such as `*.apps.example.com` is the simplest; any source works (cert-manager, your CA, a purchased one) |
| DNS | The auth host (`auth.apps.example.com`), the portal host and each app host resolve to Traefik. A wildcard record is the simplest |
| Google | A Google Workspace or Cloud Identity domain and a Cloud project to host the OAuth client (section 5) |
| Registry | A registry your cluster pulls from. There are no published images |
| Tools | `kubectl` with kustomize, `docker`, `openssl`, `psql` |

## 2. Build the images

From the repository root. The build context is `platform/` (a `go.work` spans the modules):

```bash
docker build -f platform/api/Dockerfile        -t <registry>/atrium/platform-api:v1 platform
docker build -f platform/api/Dockerfile.portal -t <registry>/atrium/portal:v1 platform
docker push <registry>/atrium/platform-api:v1
docker push <registry>/atrium/portal:v1
```

The platform-api image also runs the `migrate` subcommand. `base/` uses placeholder names (`portal-platform-api:dev`, `portal-portal:dev`) that your overlay rewrites with an `images:` transformer. `base/` also pulls `postgres:18.6-trixie@sha256:...` for the `wait-for-db` initContainers (only `pg_isready` is used); mirror it if your cluster cannot reach Docker Hub.

To administer grants from a terminal, build the CLI: `cd platform/api && go build -o portalctl ./cmd/portalctl` ([usage](operate.md#portalctl)).

## 3. Credentials

Generate everything first; the next section needs the passwords. Files stay local, never commit them.

```bash
umask 077
openssl rand 32 > kek                                    # 32 bytes, binary
for r in admin owner api portal; do openssl rand -hex 32 | tr -d '\n' > "pw-$r"; done
# pgpass: host:port:database:user:password. The host field must equal PGHOST of the Deployments.
H=postgres.platform-db.svc.cluster.local
printf '%s:5432:platform:platform_owner:%s\n' "$H" "$(<pw-owner)"  > pgpass-owner
printf '%s:5432:platform:platform_api:%s\n'   "$H" "$(<pw-api)"    > pgpass-api
printf '%s:5432:platform:portal_app:%s\n'     "$H" "$(<pw-portal)" > pgpass-portal
```

`pw-admin` is only for Option A below (the bootstrap superuser of `base/postgres`). Once the namespaces exist (section 8) create the Secrets:

```bash
kubectl -n platform create secret generic platform-kek --from-file=kek=kek
kubectl -n platform create secret generic platform-owner-pgpass --from-file=pgpass=pgpass-owner
kubectl -n platform create secret generic platform-api-pgpass   --from-file=pgpass=pgpass-api
kubectl -n portal   create secret generic portal-pgpass         --from-file=pgpass=pgpass-portal
kubectl -n platform create secret generic platform-google-client-secret --from-file=secret=./google-client-secret
# Option A only:
kubectl -n platform-db create secret generic postgres-bootstrap \
  --from-file=admin-password=pw-admin --from-file=owner-password=pw-owner --from-file=api-password=pw-api --from-file=portal-password=pw-portal
```

| Secret | Key | Mounted by |
|---|---|---|
| `platform/platform-kek` | `kek` | platform-api |
| `platform/platform-owner-pgpass` | `pgpass` | migrate Job only |
| `platform/platform-api-pgpass` | `pgpass` | platform-api |
| `portal/portal-pgpass` | `pgpass` | portal |
| `platform/platform-google-client-secret` | `secret` | platform-api |
| `platform-db/postgres-bootstrap` | `admin-password`, `owner-password`, `api-password`, `portal-password` | `base/postgres` (Option A) |

**KEK.** It encrypts the signing keys stored in PostgreSQL ([keys](reference/keys.md)). Generate it once, back it up outside the cluster, and never regenerate it: if it changes, the stored signing keys cannot be unsealed, platform-api reports `KEKMismatch`, fails readiness and refuses to rotate. Files must be regular and at most mode 0640; the manifests mount them with `defaultMode: 0440` plus `fsGroup`. See [TODO.md](../TODO.md) on who can read this Secret. Variables and file rules: [reference/configuration.md](reference/configuration.md).

## 4. Database and roles

PostgreSQL 18 or later is required (the migrations use `uuidv7()`); 18.6 is the tested version. Connections must be direct (`LISTEN`/`NOTIFY`): no transaction-pooling proxy in between. The database is `platform`; five roles ([privileges](reference/data-model.md#database-roles)). Both processes refuse to start if their role is a superuser, owns tables, bypasses RLS or holds privileges beyond the expected set, so create the roles exactly as below.

**Option A: `base/postgres`** (single instance, dev-grade; `storageClassName: local-path` and 2Gi are hardcoded, patch them).

Create `postgres-bootstrap` (section 3). On first start `initdb.sh` creates `platform_owner`, `platform_runtime`, `platform_api`, `platform_portal`, `portal_app` and the database `platform`, using the owner, api and portal passwords of the Secret, which must equal the ones in the pgpass files. `initdb.sh` runs only on an empty data directory: for a volume initialized earlier, apply `overlays/local/roles.sql` once (it expects the psql variable `portal_password`; see Option B for the invocation).

**Option B: your own PostgreSQL.** Run as a superuser. Passwords go through psql variables read from files, never in an argument list:

```bash
{
  printf "\\set owner_password '%s'\n"  "$(<pw-owner)"
  printf "\\set api_password '%s'\n"    "$(<pw-api)"
  printf "\\set portal_password '%s'\n" "$(<pw-portal)"
  cat <<'SQL'
CREATE ROLE platform_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD :'owner_password';
CREATE ROLE platform_runtime NOLOGIN;
CREATE ROLE platform_api LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD :'api_password';
GRANT platform_runtime TO platform_api;
CREATE DATABASE platform OWNER platform_owner;
REVOKE ALL ON DATABASE platform FROM PUBLIC;
GRANT CONNECT ON DATABASE platform TO platform_owner, platform_api;
SQL
} | psql -v ON_ERROR_STOP=1 -q -h <db-host> -U <superuser> -d postgres
# then the portal roles, in the new database:
{ printf "\\set portal_password '%s'\n" "$(<pw-portal)"; cat platform-gitops/overlays/local/roles.sql; } |
  psql -v ON_ERROR_STOP=1 -q -h <db-host> -U <superuser> -d platform
```

(The first block is `base/postgres/initdb.sh`; the second is `roles.sql`, which creates `platform_portal` and `portal_app`, `GRANT platform_portal TO portal_app` and the `CONNECT` grant. `platform_owner` cannot create roles, so both groups must exist before the first migration.) Then patch `base/` for the external server, **Not verified outside the local cluster**:

- `PGHOST` in the migrate Job (initContainer `wait-for-db` and container), in the platform-api and portal Deployments (initContainer and container), and the host field of the three pgpass files.
- `PGSSLMODE` is `disable` in all of them. For `verify-full` also set `PGSSLROOTCERT` and mount the CA (ConfigMap) in each pod.
- NetworkPolicy `allow-egress-dns-db` in `platform` and in `portal` allows egress only to pods `postgres` in namespace `platform-db`: replace that rule with your server's address (`ipBlock`) on port 5432. Drop `base/postgres` and the `platform-db` namespace if unused.

**Migrations.** `base/migrate` is a Job that runs `platform-api migrate` as `platform_owner`. It must complete before `platform-api` and `portal` roll out on every release; `base/` has no ordering mechanism: apply it by hand on first install (section 8); for releases and GitOps ordering see [operate.md](operate.md#upgrade).

## 5. Google OAuth client

In Google Cloud (project of your Workspace organization):

1. Configure the OAuth consent screen as **Internal** (external accounts are rejected, [login](reference/login.md#google-oidc)).
2. Create an OAuth client of type *Web application*.
3. Authorized redirect URI, exactly one: `https://auth.apps.example.com/oauth2/callback` (`https://` + `PLATFORM_AUTH_HOST` + `/oauth2/callback`).
4. Put the client secret in the file used in section 3. The client id is not secret; it goes in `PLATFORM_GOOGLE_CLIENT_ID` (section 7).

`PLATFORM_ALLOWED_HD` is the comma-separated, lowercase list of Workspace domains (the `hd` claim) allowed to sign in. Alias and secondary domains are untested, see [section 11](#11-known-limitations-before-production).

## 6. Traefik

ForwardAuth runs at the entrypoint, so it applies to every route with no opt-out. The contract is in [forwardauth.md](reference/forwardauth.md#traefik-configuration); the repo implements it as follows.

**Middlewares.** `base/platform-api/middlewares.yaml` (namespace `platform`) defines `strip-assertion` and `forwardauth` (address, `authResponseHeaders`, the `authRequestHeaders` allowlist including `Authorization`); their exact content is in [forwardauth.md](reference/forwardauth.md#traefik-configuration). `base/portal/ingressroute.yaml` adds `strip-authorization` so API tokens never reach the portal process; every route matching a portal host must list it.

**Traefik values** (merge into yours; Helm chart values shown):

```yaml
ports:
  websecure:
    http:
      aliasHeadersStrategy: delete
      middlewares:            # order matters
        - platform-strip-assertion@kubernetescrd
        - platform-forwardauth@kubernetescrd
    forwardedHeaders: {trustedIPs: [], insecure: false}   # never trust client X-Forwarded-*
providers:
  kubernetesCRD: {allowCrossNamespace: false}
tlsStore:
  default: {defaultCertificate: {secretName: <your-wildcard-secret>}}   # Secret in the traefik namespace
accessLog:
  enabled: true
  fields:
    headers: {defaultMode: drop}           # Authorization and Cookie must never be logged
    queryParameters: {defaultMode: drop}   # login code, bind and rd travel in the query (Traefik >= 3.7.3)
```

The `ports.websecure.http...` keys map to `entryPoints.websecure.http...` in static configuration and `tlsStore` is dynamic configuration; **Not verified outside the local cluster** (only the Helm chart was used). Source: `local/traefik-values.yaml` and `platform-gitops/overlays/local/traefik/entrypoint-values.yaml`. The Middlewares must exist before Traefik is reconfigured with the entrypoint chain, or every route breaks (section 8).

**Client IP.** Terminate where the real client address reaches Traefik; the `/login` rate limit uses the last `X-Forwarded-For` entry. **Not verified outside the local cluster** (TODO: real client IP).

## 7. Write your overlay

Keep your overlay in a directory of its own, `deploy/prod/<dir>/` (the examples use `../../../platform-gitops/base/...`, so adjust the depth if you place it elsewhere). Each base directory sets its own `namespace:`, so use one overlay directory per base to apply them in order. `kubectl kustomize <dir>` renders it.

`namespaces/kustomization.yaml`: drop the reference-app namespaces. App namespaces keep the label `atrium.wysted.dev/app-namespace: "true"`: it is the project's label prefix (not your domain) and the platform-api NetworkPolicy `allow-ingress-jwks` admits port 8083 only from namespaces carrying it. Add the label to every namespace that hosts an app.

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../../platform-gitops/base/namespaces]
patches:
  - target: {kind: Namespace, name: "hello-.*"}
    patch: |-
      $patch: delete
      apiVersion: v1
      kind: Namespace
      metadata: {name: ignored}
```

`postgres/kustomization.yaml` (Option A only): `resources: [../../../platform-gitops/base/postgres]`, plus a patch for `storageClassName` and size of the StatefulSet `volumeClaimTemplates` and an `images:` entry for `postgres`.

`migrate/kustomization.yaml` (same `images:` for `platform-api` as below, no other patches):

```yaml
resources: [../../../platform-gitops/base/migrate]
images:
  - {name: portal-platform-api, newName: <registry>/atrium/platform-api, newTag: v1}
  - {name: postgres, newName: <registry>/mirror/postgres}   # keeps the pinned tag and digest
```

`platform/kustomization.yaml` (platform-api, Middlewares, auth-host route):

```yaml
resources: [../../../platform-gitops/base/platform-api]
images:
  - {name: portal-platform-api, newName: <registry>/atrium/platform-api, newTag: v1}
  - {name: postgres, newName: <registry>/mirror/postgres}
patches:
  - target: {kind: Deployment, name: platform-api}
    patch: |-
      apiVersion: apps/v1
      kind: Deployment
      metadata: {name: platform-api}
      spec:
        template:
          spec:
            containers:
              - name: platform-api
                env:
                  - {name: PLATFORM_AUTH_HOST, value: auth.apps.example.com}
                  - {name: PLATFORM_ISSUER, value: "https://auth.apps.example.com"}
                  - {name: PLATFORM_PORTAL_APP_ID, value: <portal-app-id>}   # section 9
                  - {name: PLATFORM_GOOGLE_CLIENT_ID, value: <client-id>}
                  - {name: PLATFORM_GOOGLE_CLIENT_SECRET_FILE, value: /etc/platform/google/secret}
                  - {name: PLATFORM_ALLOWED_HD, value: example.com}
                volumeMounts:
                  - {name: google-client-secret, mountPath: /etc/platform/google, readOnly: true}
            volumes:
              - name: google-client-secret
                secret: {secretName: platform-google-client-secret, defaultMode: 0440}
  - target: {kind: IngressRoute, name: platform-api}
    patch: |-
      - {op: replace, path: /spec/routes/0/match, value: "Host(`auth.apps.example.com`)"}
```

`portal/kustomization.yaml`:

```yaml
resources: [../../../platform-gitops/base/portal]
images:
  - {name: portal-portal, newName: <registry>/atrium/portal, newTag: v1}
  - {name: postgres, newName: <registry>/mirror/postgres}
patches:
  - target: {kind: Deployment, name: portal}
    patch: |-
      apiVersion: apps/v1
      kind: Deployment
      metadata: {name: portal}
      spec:
        template:
          spec:
            containers:
              - name: portal
                env:
                  - {name: PORTAL_APP_ID, value: <portal-app-id>}   # same id as PLATFORM_PORTAL_APP_ID
                  - {name: PORTAL_ISSUER, value: "https://auth.apps.example.com"}
  - target: {kind: IngressRoute, name: portal}
    patch: |-
      - {op: replace, path: /spec/routes/0/match, value: "Host(`portal.apps.example.com`)"}
```

Also required, because `base/` denies all traffic by default:

- **Google egress.** `base/platform-api` allows egress only to DNS and PostgreSQL. Add a policy for TCP 443 from platform-api pods, modeled on `overlays/local-google/platform/networkpolicy-egress-google.yaml` (replace its `except` list with your own pod, service and node ranges). It allows any public host on 443; see [section 11](#11-known-limitations-before-production).
- **Cluster assumptions in the NetworkPolicies and probe.** DNS is expected in `kube-system` with `k8s-app=kube-dns`; Traefik in namespace `traefik`, label `app.kubernetes.io/name=traefik`; the portal probe dials `traefik.traefik.svc.cluster.local:443` (`PORTAL_PROBE_ADDR`) and its egress policy allows pod port 8443 (the `websecure` container port). Patch the policies and env if your Traefik differs.
- **External database.** `PGHOST`, `PGSSLMODE` (`disable` in base), the pgpass host and the `postgres` NetworkPolicies are set for the in-cluster `base/postgres`; patch them (and the three Deployments and the Job) if you use another server.
- **Private CA.** The portal integration probe verifies the Traefik certificate against the system roots. If your certificate comes from a private CA, set `PORTAL_PROBE_CA_FILE` in the portal Deployment and mount the CA from a ConfigMap, or integration health stays red ([configuration](reference/configuration.md)).
- Other variables (timeouts, probe interval, JWKS TLS, metrics): [reference/configuration.md](reference/configuration.md).

## 8. Deploy and verify

```bash
kubectl apply -k deploy/prod/namespaces
# create the Secrets (section 3); Option A: kubectl apply -k deploy/prod/postgres; then the roles (section 4)
kubectl -n platform delete job migrate --ignore-not-found
kubectl apply -k deploy/prod/migrate
kubectl -n platform wait --for=condition=complete job/migrate --timeout=180s
# register the portal app (section 9), then:
kubectl apply --dry-run=server -k deploy/prod/platform && kubectl apply -k deploy/prod/platform
kubectl -n platform rollout status deploy/platform-api
kubectl apply -k deploy/prod/portal
kubectl -n portal rollout status deploy/portal
# only now enable the entrypoint chain: upgrade Traefik with the values of section 6
```

Check: `kubectl -n platform get middleware.traefik.io strip-assertion forwardauth` lists both before the Traefik upgrade, and `kubectl -n platform logs deploy/platform-api` shows no `PLATFORM_PORTAL_APP_ID` WARN. A non-ready platform-api usually means a role privilege mismatch (migrate did not finish) or a wrong KEK (readiness reason `kek mismatch`). Then open `https://portal.apps.example.com`: you are redirected to Google, and after sign-in the portal loads. The repo's `overlays/local/smoke.sh` end-to-end suite targets the fake login of the local cluster and is not meant for your install.

## 9. Register the portal and create the first platform admin

**Portal app.** No migration creates the reserved initiative `platform` or the portal app; the local overlay inserts them from `seed.sql` (development data, do not use it). Run once as `platform_owner`. An app id must match `app_` + 26 characters of `[0-9A-HJKMNP-TV-Z]` (a ULID). This generates one: the `00` prefix keeps the leading timestamp characters inside the valid ULID range, and the rest is random hex, which is a subset of the allowed alphabet:

```bash
echo "app_00$(openssl rand -hex 12 | tr a-f A-F)"     # use it as <portal-app-id> in section 7
```

```sql
-- psql -U platform_owner -d platform -v app_id=<portal-app-id> -v host=portal.apps.example.com
BEGIN;
INSERT INTO initiatives (key, name) VALUES ('platform', 'Platform') ON CONFLICT (key) DO NOTHING;
INSERT INTO apps (id, initiative_id, name)
SELECT :'app_id', id, 'portal' FROM initiatives WHERE key = 'platform' ON CONFLICT (id) DO NOTHING;
INSERT INTO deployments (host, app_id) VALUES (:'host', :'app_id') ON CONFLICT (host) DO NOTHING;
COMMIT;
```

**First admin.** There is no production tool: the portal has no endpoint to manage `platform_admins` by design ([API](reference/api.md)), and `overlays/local-google/admin.sh` is development tooling that runs `psql` through `kubectl exec postgres-0`. The equivalent is a manual SQL step as `platform_owner`. `users.email` is not unique, so run the `SELECT` first and continue only if it returns exactly one row. The user must have signed in once (users are created on first login with no grants; the portal itself is open to any allowed active user):

```sql
-- psql -U platform_owner -d platform -v email=you@example.com
-- first check: SELECT id, email FROM users WHERE lower(email) = lower(:'email') AND kind = 'human';  -- must be exactly one row
INSERT INTO platform_admins (user_id)
SELECT id FROM users WHERE lower(email) = lower(:'email') AND kind = 'human' AND google_sub NOT LIKE 'fake:%'
ON CONFLICT DO NOTHING;
```

`admin.sh` also writes one `audit_events` row (`platform_admin.grant`, `actor_id` NULL) in the same transaction; add it if you want the trail. Revoke with `DELETE FROM platform_admins WHERE user_id = ...`. The admin then creates initiatives, apps, groups and API tokens in the portal; `portalctl` grants access with `PORTAL_URL` and `PORTAL_API_TOKEN` ([operate.md](operate.md#portalctl)). To put your own apps behind atrium: [onboard-an-app.md](onboard-an-app.md). Day-two tasks: [operate.md](operate.md).

## 10. Optional

- **Monitoring.** `platform-gitops/monitoring/` has alert rules (plain and `PrometheusRule`), `PodMonitor`s and a Grafana dashboard. Nothing there is specific to k3d; assumptions (job names, `pod` label, namespace selectors) are in [operate.md](operate.md#monitoring), metrics in [metrics.md](reference/metrics.md). `base/monitoring` provides NetworkPolicies for a `monitoring` namespace you create.
- **Internal TLS for the JWKS.** `base/` serves the JWKS over plain HTTP inside the cluster. Any PKI works; see [Internal TLS for the JWKS](operate.md#internal-tls-for-the-jwks) for the env vars and the cert-manager option. Recommended for production, **Not verified outside the local cluster** with your PKI.
- **Grafana behind ForwardAuth:** [operate.md](operate.md#grafana-behind-forwardauth).

## 11. Known limitations before production

Open items are tracked in [TODO.md](../TODO.md): "Before production" (Google `hd` for alias domains, suspended users, KEK in a Secret, real client IP, egress filtering, `NOTIFY` serialization) and "Adopter bootstrap gaps in `base/`" (the manual steps and hardcoded values above). Also note that `base/postgres` is a single instance without backups: plan database HA and backups yourself. Without the Google variables of section 7, platform-api refuses to start (the fake login is off by default), so a forgotten patch fails loudly instead of opening the login.
