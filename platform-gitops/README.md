# platform-gitops

Kustomize manifests of the platform for the local k3d cluster `portal`. Everything runs through `./tk`.

```
base/namespaces    platform-db, platform, portal, hello-a, hello-b (Pod Security restricted); `monitoring` lives in overlays/local/namespaces
base/postgres      StatefulSet postgres:18.6 (digest = compose.yaml), initdb.sh, NetworkPolicy
base/migrate       Job `platform-api migrate` as platform_owner
base/platform-api  Deployment x2, Services internal:8081 / jwks:8083 / public:8080, Middlewares, auth-host IngressRoute, NetworkPolicy
base/portal        Deployment x2 (cmd/portal), Service, IngressRoute (passHostHeader), NetworkPolicies
base/hello         reference app (one instance per namespace via overlay)
base/monitoring    NetworkPolicies of the monitoring namespace (default deny; Prometheus, Grafana)
monitoring/        adopter artifacts: alert rules (plain + PrometheusRule), PodMonitors, Grafana dashboard, promtool tests
overlays/local     domain/app-id patches, scripts, seed, traefik values (fake login), monitoring stack values
overlays/local-google  real Google login on top of overlays/local: image tag, client secret and id, env, egress policy, admin bootstrap
```

## Ordering: migrate before platform-api (adopters)

`platform-api` and `portal` refuse to start when the database roles drift from what the binary expects (missing or extra
privileges), so the `migrate` Job (as platform_owner) must complete BEFORE the new Deployments roll out. `base/` has no ordering
mechanism (the `wait-for-db` initContainer only waits for `pg_isready`). `overlays/local/up.sh` does it with `kubectl wait
--for=condition=complete job/migrate` before applying platform-api. With GitOps, add it yourself: Argo CD `PreSync` hook (or
sync-wave `-1`) on the Job, or a Flux `Kustomization` for `base/migrate` that the platform-api one `dependsOn` with
`wait: true`. Delete the finished Job (or set `ttlSecondsAfterFinished`) so each release recreates it.

## Scripts (overlays/local)

| Script | Purpose |
|---|---|
| `up.sh` | secrets (`.tools/secrets/k3d-*`, generated once), image import (`BUILD=1` rebuilds the three images), apply in order (namespaces, DB, portal roles, migrate Job, seed, platform-api, hello-a/b, portal, monitoring), `down-spike.sh`, Traefik helm upgrade |
| `roles.sh` / `roles.sql` | `platform_portal` (NOLOGIN) and the `portal_app` login role (member of `platform_portal` only) as the bootstrap superuser; idempotent, works on an existing volume (initdb.sh only runs once) |
| `seed.sh` / `seed.sql` | dev data as platform_owner; idempotent, also restores grants revoked by the smoke |
| `jwks-tls.sh` | local PKI for the JWKS listener (ECDSA P-256 CA 10 y + leaf 90 days, SANs of `platform-api-jwks`), Secret `platform-api-jwks-tls` and ConfigMap `platform-jwks-ca` per app namespace; idempotent, re-issues the leaf 30 days before expiry; called by `up.sh` before the rollouts |
| `smoke.sh` | end-to-end checks with curl from the host; leaves the cluster as found |
| `down-spike.sh` | deletes spike-authz / spike-app1 / spike-app2 (manifests stay in `spikes/`) |
| `down-monitoring.sh` | removes PodMonitors/rules, the Helm release and the `monitoring` namespace (`DROP_CRDS=1` also the Operator CRDs); the platform keeps running |

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

- Namespace `portal`, labelled `atrium.wysted.dev/app-namespace=true` like any app namespace: platform-api's JWKS listener
  (8083) already accepts it; the internal listener (8081) admits only Traefik. `platform-db` accepts it too (`allow-ingress-platform` selects `platform` and `portal`).
- Database: login role `portal_app`, member only of `platform_portal`; password in `.tools/secrets/k3d-pg-portal-password`, pgpass
  Secret `portal/portal-pgpass` (`fsGroup` + `defaultMode: 0440`, like platform-api). The role is created by `roles.sh`, not by
  the migrations: `platform_owner` has no CREATEROLE and migration 0009 only creates `platform_portal` if it is missing.
- Probe: `PORTAL_PROBE_ADDR=traefik.traefik.svc.cluster.local:443`. The wildcard certificate is the real Let's Encrypt one, so the
  system roots verify it: no `PORTAL_PROBE_CA_FILE` and no `PORTAL_PROBE_INSECURE_TLS`. Local interval 1m (`PORTAL_PROBE_INTERVAL`).
- NetworkPolicies (default deny both ways): ingress only from Traefik to 8080 (kubelet probes on 8081 come from the node and are
  never filtered); egress to DNS, PostgreSQL 5432, platform-api 8083 (JWKS) and Traefik pods on 8443. Policies see the POD port: the
  Service port 443 of Traefik is `websecure` = 8443. The metrics port 9090 is open only to the Prometheus pods of the `monitoring`
  namespace (`allow-ingress-metrics`, same rule in `base/platform-api`). The metrics listener has no Service; the internal listener (8081) is reachable by Traefik only.
- IngressRoute: exact `Host()`, `passHostHeader: true`; ForwardAuth and strip-assertion come from the entrypoint.

## Monitoring

Prometheus and Grafana for the platform, in the `monitoring` namespace of the local cluster, plus the artifacts an adopter
takes to their own stack. The metric catalog and the cardinality rule (no client-supplied label values, no `app_id`) are in
`docs/reference/metrics.md` ("Metrics"); read the rule before adding a metric or a dashboard query.

**Local stack.** `up.sh` installs `kube-prometheus-stack` 92.10.0 (Prometheus Operator v0.94.1, Prometheus v3.15.0, Grafana
13.2.4) as release `monitoring`, with values in `overlays/local/monitoring/values.yaml`. Chosen over the plain prometheus +
grafana charts because PodMonitor and PrometheusRule (what adopters with the Operator apply) can then be tested for real; the
weight is one operator pod (~20 MiB). Everything else is off: Alertmanager, node-exporter, kube-state-metrics, kubelet and
control-plane scrapes, default rules, the admission webhook, Grafana sidecars. Prometheus keeps 2 days in an emptyDir (disposable). Hardening: images pinned by tag and digest, Prometheus reloads by
SIGHUP (`reloadStrategy: ProcessSignal`, so `/-/reload` and `/-/quit` are off), Grafana mounts no ServiceAccount token, the
`monitoring` namespace (Pod Security restricted) is created by the local overlay, not by `base/`.

**Accepted local risk: operator RBAC.** The chart installs the Prometheus Operator with a ClusterRole that can get/list/watch
Secrets cluster-wide (the platform KEK among them) and delete Pods; the chart has no option to make it namespaced
(`prometheusOperator.namespaces` only narrows the namespaces the operator reconciles, it does not shrink the ClusterRole).
Fine for a disposable cluster; in production use RBAC your security team reviews or a Prometheus without an operator (plain
rules file). Prometheus's own ClusterRole is read-only (pods, services, endpoints, nodes metrics).
Alertmanager is disabled and has no receivers: alerts are evaluated and visible in Prometheus (`/alerts`) and Grafana, and go
nowhere. `down-monitoring.sh` tears it down.

**Grafana access.** Day to day, Grafana is an app of the portal behind ForwardAuth: https://grafana.apps.wysted.dev (section
"Grafana behind ForwardAuth" below). The port-forward below is the break-glass path (basic auth is off by default, see there). The toolbox runs in a container, so `./tk` can publish the forwarded port on the
host loopback with `TK_PUBLISH`; leave the command running and open http://127.0.0.1:3000 (user `admin`):

```
TK_PUBLISH=127.0.0.1:3000:3000 ./tk kubectl -n monitoring port-forward --address 0.0.0.0 deploy/monitoring-grafana 3000:3000
TK_PUBLISH=127.0.0.1:9090:9090 ./tk kubectl -n monitoring port-forward --address 0.0.0.0 svc/monitoring-prometheus 9090:9090   # Prometheus
```

While a port-forward runs with `--address 0.0.0.0`, the forwarded port is also reachable from any container on `portal-net`
(the toolbox, the k3d nodes); stop it when you are done. `TK_PUBLISH` only accepts `127.0.0.1:<port>:<port>` (exit 2 otherwise).

The admin password is generated once into `.tools/secrets/k3d-grafana-admin-password` (gitignored, loaded as Secret
`monitoring/grafana-admin`); read it on your own terminal when you log in, never paste it anywhere. The dashboard "Portal platform"
is provisioned read-only from `monitoring/dashboards/portal-platform.json` (folder "Portal"). TODO.md tracks putting Grafana
behind ForwardAuth.

**NetworkPolicies.** `monitoring` is default deny both ways. Prometheus: ingress from Grafana (9090) and itself; egress to
the metrics port 9090 of platform-api and portal pods only, itself, DNS and the Kubernetes API (`up.sh` renders
`overlays/local/monitoring/networkpolicy-apiserver.tpl.yaml` with one /32 per endpoint of the `kubernetes` Service, port 6443). Grafana: egress to Prometheus 9090, DNS and (local overlay) the JWKS 8083; ingress from Traefik on 3000 only (no sidecars, so it never needs the API). On the
other side, `allow-ingress-metrics` in `platform` and `portal` admits only pods labelled `app.kubernetes.io/name=prometheus` from the
`monitoring` namespace to port 9090; the app-namespace label does NOT grant it. Anything that can run a pod with that label in
`monitoring` joins the set: keep the namespace to the stack.

**Adopter artifacts (`monitoring/`).** Nothing there is specific to k3d:

| File | Use |
|---|---|
| `rules/portal-platform.rules.yaml` | alert rules, plain Prometheus format (the source) |
| `operator/prometheusrule.yaml` | the same rules as a `PrometheusRule`, generated by `gen-prometheusrule.sh` |
| `operator/podmonitors.yaml` | scrape of `platform-api` and `portal` on the pod port named `metrics` (`job` = app name) |
| `dashboards/portal-platform.json` | Grafana dashboard (datasource picked by variable); `dashboards/kustomization.yaml` wraps it in a ConfigMap labelled `grafana_dashboard` for a sidecar |
| `rules/tests/portal-platform.test.yaml` | `promtool` unit tests: every rule has a firing and a not-firing case |
| `test-rules.sh` | sync check + `promtool check rules` + `promtool test rules` (pinned `prom/prometheus` through Docker); also `local/lint.sh rules` |

Assumptions for adopters:
- Rules and dashboard expect `job="platform-api"` and `job="portal"` (the PodMonitors set it with `jobLabel`); edit
  `namespaceSelector` in the PodMonitors for your namespaces.
- The per-replica rules (`SigningKeyNotLoaded`, `SigningKeyKEKMismatch`, `DBPoolSaturated`) put `{{ $labels.pod }}` in their text; the
  PodMonitor adds `pod`. A scrape config that does not must relabel it.
- Here Prometheus selects every PodMonitor, ServiceMonitor, ScrapeConfig, Probe and PrometheusRule (selectors `{}`) of the
  namespaces the operator reconciles (`monitoring`, `platform`, `portal`). Anyone who can create those objects there changes what
  is scraped or alerted: restrict creation by RBAC, or label-select in your own installation.
- Reload latency: with `reloadStrategy: ProcessSignal` the config-reloader's first reload check after a Prometheus pod start
  can time out (~3 min) before it starts watching the rule files, so rules applied just after a restart load late; later changes
  take ~40 s. The alternative is the HTTP reload, which needs `--web.enable-lifecycle` (open `/-/quit` and `/-/reload`).
- `ProbeNotWriting` fires when `time() - max(portal_probe_last_write_timestamp_seconds)` exceeds 3 x
  `portal_probe_interval_seconds` (0 = probe disabled, never fires; missing gauges = silent), for 10 minutes. A restart
  resets the timestamp to the process start time, so detection can be delayed by up to 3 intervals (accepted). The portal
  rules assume one portal release per Prometheus (`job="portal"`): two releases sharing one would mask each other.
- After changing the plain rules run `monitoring/gen-prometheusrule.sh`; `test-rules.sh` fails if the generated file is stale.

## Internal TLS for the JWKS (optional, PKI-neutral)

`base/` keeps the JWKS listener on plain HTTP (`:8083`, `http://` URLs): it carries no PKI, so every adopter picks its own.
`overlays/local` turns TLS on with patches only (no base change):

| Where | Patch |
|---|---|
| platform-api | `PLATFORM_JWKS_TLS_CERT_FILE=/etc/platform/jwks-tls/tls.crt`, `PLATFORM_JWKS_TLS_KEY_FILE=.../tls.key`, Secret `platform-api-jwks-tls` mounted with `defaultMode: 0440` (the key must be 0640 or stricter; `fsGroup` already lets the non-root user read it, like the KEK). The listener then serves HTTPS only, TLS 1.3 minimum, and reloads the pair every 30 s |
| portal, hello-a/b | `*_JWKS_URL=https://platform-api-jwks.platform.svc.cluster.local:8083/...`, `*_JWKS_CA_FILE=/etc/<app>/jwks-ca/ca.crt` from ConfigMap `platform-jwks-ca` |

Local CA: `overlays/local/jwks-tls.sh` (run by `up.sh`) keeps `k3d-jwks-ca.*` and `k3d-jwks-tls.*` in `.tools/secrets/` (git-ignored). The CA key
never reaches the cluster. Rotation: delete `k3d-jwks-tls.*` and re-run the script (same CA, new leaf); the kubelet syncs the Secret
volume in about a minute and the listener swaps the pair within 30 s, with no restart and no verifier change. Leaf rotation needs no
restart (reload every 30 s; ~1 min with the Secret volume sync). CA rotation does: verifiers load the CA once at start-up, so the new CA
must be distributed first and the verifiers restarted before the new leaf is served; the script does it in that order (ConfigMaps ->
`rollout restart` portal, hello-a, hello-b + wait -> Secret). With cert-manager/trust-manager, bundle old+new CA during the transition.
The script renews the CA when less than 150 days remain (leaf 90 + renewal window 30 + margin 30), so no leaf outlives its CA.

Adopters: only the [contract](../docs/reference/assertion.md) matters, not the script.
- Server: any issuer that writes a PEM cert (chain allowed) and key (PKCS8/EC/RSA, mode <= 0640) at the two paths: cert-manager (`Certificate`
  with `dnsNames` = the Service names above and `secretName: platform-api-jwks-tls`, which yields `tls.crt`/`tls.key` for the same mount),
  a Vault agent template writing the files, or a manually created `kubernetes.io/tls` Secret. Keep the SAN equal to the name apps use.
- Clients: distribute the issuing CA bundle (PEM) to each app namespace and point `*_JWKS_CA_FILE` (or `Config.RootCAs` in the SDK) at it:
  cert-manager `trust-manager` Bundle, a Vault/CSI mount, or a ConfigMap per namespace. If the CA is public/system-trusted, omit the variable.
- Switch the URLs to `https://` in the same change; an `http` URL logs a warning at verifier start-up.
- Smoke section `16b` checks the SAN, CA verification (and its failure without the CA), TLS-only and TLS 1.3.
- `overlays/local-google` builds on `overlays/local/platform`, so it inherits the TLS patches (the apps keep the CA ConfigMap from `up.sh`).

## Grafana behind ForwardAuth (local overlay)

Grafana is registered as an app of the portal model and reached only through Traefik; it authenticates the request with the
`Platform-Assertion` header that ForwardAuth injects. Nothing in `base/` changes.

| Piece | Where |
|---|---|
| Authorization | `overlays/local/seed.sql`: initiative `observability` (roles `viewer`, `editor`, `admin`, none with `manages_access`), app `grafana` id `app_01J9Z3K7WQ4M8R2T5XV6N0B1HD`, host `grafana.apps.wysted.dev`; `admin@example.com` has `admin`, alice `viewer`, bob only `auditor` (a role Grafana does not map: ForwardAuth lets him in, Grafana refuses), carol nothing |
| Grafana `[auth.jwt]` | `overlays/local/monitoring/values.yaml`: `header_name: Platform-Assertion`, `jwk_set_url` = the HTTPS JWKS, `tls_client_ca` = the `platform-jwks-ca` ConfigMap (created in `monitoring` by `jwks-tls.sh`, mounted at `/etc/grafana/jwks-ca`), `expect_claims` `iss` + `aud` (the app id, never the host), `username_claim: sub` (no `email_claim`: identity is the UUID `sub` only, the email never matches a Grafana user; the built-in admin has `admin_email: break-glass@invalid`), `cache_ttl: 5m`, `auto_sign_up`, `role_attribute_strict: true`, `url_login: false` |
| Roles | `role_attribute_path` takes the `roles` claim and returns one string, highest wins: `admin` -> Admin, `editor` -> Editor, `viewer` -> Viewer; any other role (or none) refuses the login. Org role only: `allow_assign_grafana_admin` is off, nobody becomes a Grafana server admin through the portal |
| Route | `overlays/local/monitoring/grafana-ingressroute.yaml`: `Host(grafana.apps.wysted.dev)` on `websecure` (ForwardAuth and strip-assertion are entrypoint-level) -> Service `monitoring-grafana:80` |
| NetworkPolicies | `overlays/local/monitoring/networkpolicy-grafana.yaml`: ingress to Grafana :3000 from Traefik only; egress to platform-api :8083. `overlays/local/platform/networkpolicy-jwks-grafana.yaml`: platform-api admits :8083 from the Grafana pods (the `monitoring` namespace is NOT labelled as an app namespace) |
| Local login off | `[auth] disable_login_form`, `disable_signout_menu`, `[auth.basic] enabled=false`, anonymous off, `root_url` = the public URL, `cookie_secure` |

Properties worth knowing: Grafana ignores the JOSE `typ` (`platform-assertion+jwt`, see [assertion](../docs/reference/assertion.md)) and cannot require it, which is
acceptable because the platform signing key signs only `Platform-Assertion`; the 60 s clock leeway is fixed (go-jose); the header is
checked on every request, so revocation takes effect on the next request (ForwardAuth answers 403; there is no Grafana-side session
to outlive it). `cache_ttl` (5m) only bounds how long a rotated JWKS key takes to be seen: the platform ([keys](../docs/reference/keys.md)) publishes the `next` key at
least 1 h before it signs and keeps the retired one 1 h, and the emergency window is 5 min (`docs/reference/keys.md`, same as the JWKS `max-age`),
so 5m never rejects a legitimate token. Grafana has no display-name claim in `[auth.jwt]`, so users show as their UUID login with
an empty email. A client-sent `Platform-Assertion` never reaches Grafana: strip-assertion removes it and ForwardAuth re-issues a
genuine one (proven at the auth host and on hello-a, smoke section 7; smoke 19b checks the Grafana-side consequences, an assertion
minted for another app is refused by `aud`, and an unmapped role by `role_attribute_strict`).

**Break-glass admin.** With the form and basic auth off there is no password path. If the platform is down and you need Grafana,
turn the local login back on for as long as you need it and reach Grafana by port-forward (see "Grafana access") with the admin
credentials from `.tools/secrets/k3d-grafana-admin-password`. `GF_AUTH_DISABLE_LOGIN_FORM=false` brings the UI form back,
`GF_AUTH_BASIC_ENABLED=true` is only needed for API calls with basic auth. WARNING: while this is on, the login form is open on
the same Grafana that Traefik serves at `grafana.apps.wysted.dev`, so any logged-in platform user with access to that host can try the
admin password. Keep the window short, with one command each way (a single `set env` rolls the pod once; the revert rolls it again, and the
Grafana database is an emptyDir, so sessions made in the window are lost), and optionally take the route out of Traefik for the
duration (the IngressRoute is local-overlay only and `kubectl apply -k` restores it):

```
./tk kubectl -n monitoring delete ingressroute grafana        # optional: nobody reaches the form through Traefik
./tk kubectl -n monitoring set env deploy/monitoring-grafana -c grafana GF_AUTH_DISABLE_LOGIN_FORM=false GF_AUTH_BASIC_ENABLED=true
# ... port-forward, work ...
./tk kubectl -n monitoring set env deploy/monitoring-grafana -c grafana GF_AUTH_DISABLE_LOGIN_FORM- GF_AUTH_BASIC_ENABLED-   # back to off
./tk kubectl apply -k platform-gitops/overlays/local/monitoring   # restores the IngressRoute (also re-applies the policies)
```

Adopters, mirroring this with their own Grafana and PKI:
- Register in the portal an initiative with role keys equal to what your `role_attribute_path` matches, an app for Grafana, and its
  host (`aud` = the app id). Route the host through the entrypoint that runs ForwardAuth; the Grafana Service must not be reachable
  any other way (NetworkPolicy: ingress from the proxy only; also no LoadBalancer/NodePort), or anyone can send the header.
- `[auth.jwt]` with `jwk_set_url` over HTTPS (mandatory outside dev mode), `tls_client_ca` for a private CA (the CA that issues the JWKS
  certificate, see "Internal TLS for the JWKS"; omit for a public CA), `expect_claims` with the exact `PLATFORM_ISSUER` and app id,
  `role_attribute_strict`, `url_login = false`. `GF_AUTH_JWT_*` env vars work the same as the ini.
- Turn off every other way in (login form, basic auth, anonymous, other `[auth.*]`) and decide your break-glass path.
- Let Grafana reach the JWKS Service (egress) and the JWKS listener admit it (ingress, by pod selector, not by the app-namespace label).

## Notes

- Namespaces that host apps need the label `atrium.wysted.dev/app-namespace=true`: the JWKS listener (8083) accepts them.
- Access log: `accessLog.fields.queryParameters.defaultMode: drop` (Traefik >= 3.7.3) removes the whole query from
  `RequestPath`, so the login `code`, `bind` and `rd` are never logged. Tradeoff: no query at all in access logs (it also
  hides benign parameters). Headers (Cookie, Set-Cookie, Location) are dropped by default; keep them dropped
  explicitly (`accessLog.fields.headers.defaultMode: drop`, as `local/traefik-values.yaml` does): `Authorization`
  now carries API tokens on the portal host, and any header logging would write live secrets.
- API tokens (`docs/reference/forwardauth.md`, "Token path"): the ForwardAuth middleware forwards `Authorization` to
  platform-api, and every route that matches a portal host must list the `strip-authorization` middleware of the
  `portal` namespace, so the token never reaches the portal process. Smoke section 22 checks both in the local cluster.
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
platform-gitops/overlays/local-google/down.sh          # back to the fake login (removes egress policy, client secret and client id)
```

Without `BUILD=1` the script imports the image already on the host and prints a WARN (it never fails) when that image is missing or older than the last commit touching `platform/` (docs and `*.md` excluded, as in `platform/.dockerignore`), or when those sources have uncommitted changes: the tag never changes, so a stale image is otherwise silent.

`local/up.sh` re-applies the fake-login overlay: run `local-google/up.sh` again afterwards. `local/smoke.sh` only works in
fake-login mode (POST /login is 405 under Google). The client secret is read from `.tools/secrets/google-client-secret` and
loaded with `--from-file` into Secret `platform/platform-google-client-secret`, mounted with `fsGroup` + `defaultMode: 0440`
(same policy as the KEK). The (public) client id is read from `.tools/secrets/google-client-id` into ConfigMap
`platform/platform-google-client-id`; `up.sh` fails if either file is missing. The env is read at container start: after changing the id, `./tk kubectl -n platform rollout restart deploy/platform-api`. Env: `PLATFORM_GOOGLE_CLIENT_ID`, `PLATFORM_GOOGLE_CLIENT_SECRET_FILE`, `PLATFORM_ALLOWED_HD=wysted.dev`,
`PLATFORM_FAKE_LOGIN=false` (explicit; `overlays/local` sets `true`, `base/` leaves it unset). The image tag is overridden with the kustomize `images` transformer (base keeps `:dev`).

The portal needs nothing from this overlay (it only validates assertions): `local/up.sh` deploys it and it keeps running; any
active user may open it (reserved initiative `platform`, "any active user" rule). To use its admin API with a real user:
`local-google/admin.sh <email>` (user must have signed in once; `--revoke` removes it; DEVELOPMENT tooling, the portal has no
endpoint to manage platform admins).

Access: users are created on their first login (JIT) with no grants (403 on every app).
Grant and revoke through the portal API with `portalctl` (`platform/api/cmd/portalctl`; build, static Linux binary:
`cd platform/api && ../../gk env CGO_ENABLED=0 go build -o ../../.tools/portalctl/portalctl ./cmd/portalctl`):
`portalctl grant|revoke <email|robot:name> <initiative-key> <role-key>`. Environment: `PORTAL_URL` (https, portal host only,
e.g. `https://portal.apps.wysted.dev`) and `PORTAL_API_TOKEN`. Who can use it: a platform admin (`admin`-scope token), a human
initiative manager (`admin`-scope token, only the initiatives they manage) or a robot holding a `manages_access` role. A grant
by email needs the user to have signed in once. Output is `changed` or `unchanged` plus `grant|revoke done: ...`; exit 0 ok,
1 any runtime or API failure (unknown initiative, role or robot, a grant to a disabled robot, an expired existing grant: "revoke
it first"), 2 usage (bad arguments, invalid `PORTAL_URL`, empty token). An existing grant with a future expiry is `unchanged`
with a warning on stderr. It never follows redirects and honors `SSL_CERT_FILE` for a private CA. Examples:

```
export PORTAL_URL=https://portal.apps.wysted.dev
read -rs PORTAL_API_TOKEN && export PORTAL_API_TOKEN   # paste the token: it stays out of the shell history
.tools/portalctl/portalctl grant you@<your-workspace-domain> demo viewer
.tools/portalctl/portalctl revoke robot:ci-deployer demo editor
```

Bootstrap: `local-google/admin.sh <email>` makes the first platform admin (after one sign-in); that admin grants access
in the portal or creates the token for `portalctl`. Platform-api sees grant changes through NOTIFY, no restart.

Rotating the Google client secret: [docs/runbooks/google-client-secret-rotation.md](docs/runbooks/google-client-secret-rotation.md).

### Egress tradeoff (NetworkPolicy `allow-egress-google`)

Google discovery/token/JWKS need HTTPS to accounts.google.com, oauth2.googleapis.com and www.googleapis.com. A standard
NetworkPolicy cannot match FQDNs, so the policy allows TCP 443 from platform-api pods to `0.0.0.0/0` EXCEPT 10.0.0.0/8
(pods 10.42.0.0/16, services 10.43.0.0/16), 172.16.0.0/12 (k3d node and Docker network 172.28.0.0/16), 192.168.0.0/16,
100.64.0.0/10, 169.254.0.0/16 and 127.0.0.0/8. Consequence: a compromised platform-api could reach ANY public host on 443
(exfiltration), not only Google. Narrowing it needs an egress proxy with an FQDN allowlist or a CNI with L7/FQDN policies
(Cilium, Calico Enterprise); neither is installed (flannel + kube-router). The policy exists only in `local-google`;
the migrate Job is not covered (it only talks to PostgreSQL). IPv4 only (the cluster is single-stack).
