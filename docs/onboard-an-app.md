# Put an app behind atrium

How to take an internal web app from "reachable inside the cluster" to "reachable only by granted people, through Google login". For the operator who registers the app and the developer who integrates it. It assumes a working install ([install.md](install.md)) and a platform admin in the portal.

The end state: the app has a registered id and host, its namespace and route are wired to Traefik, it validates the `Platform-Assertion` header, and the people who should reach it hold a role.

## 1. Register the app in the portal

A platform admin does this in the portal, in **Initiatives** ([who may do what](reference/api.md#who-administers-what)). The same calls exist in the [API](reference/api.md#endpoints); `portalctl` only grants and revokes.

1. **Initiative.** **New initiative**: a key (immutable; lowercase letters, digits, `-` and `_`) and an optional name. An initiative groups apps that share roles and people. Reuse one if the app belongs to an existing group.
2. **Roles.** In the initiative, **Roles** > **New role**. Role keys are part of the app's contract: they arrive in the `roles` claim and never change, so pick the names your app will check (for example `viewer`, `editor`). Mark a role `manages_access` only for the people who should grant access without being platform admins.
3. **App.** **Apps** > **New app**. The app id (`app_` plus a ULID) is generated; copy it. It is the `aud` your app must check, never the host.
4. **Host.** On the app row, **Add host** (`app.apps.example.com`). A host is a lowercase DNS name; it must be unique across all apps and cannot be the auth host ([host rules](reference/api.md#administration-rules)). ForwardAuth resolves the app by exact match of this host, so it must equal the `Host()` of your route in step 3.

You now have: an app id, the issuer (`https://<PLATFORM_AUTH_HOST>`, for example `https://auth.apps.example.com`) and the JWKS URL, which is the in-cluster address of platform-api's JWKS listener, `https://platform-api-jwks.platform.svc.cluster.local:8083/.well-known/jwks.json` (`http://` if you did not enable [internal TLS](operate.md#internal-tls-for-the-jwks)).

## 2. Prepare the namespace

Label the namespace that hosts the app, or platform-api's NetworkPolicy will not let it fetch the JWKS:

```bash
kubectl label namespace my-app atrium.wysted.dev/app-namespace=true
```

The label prefix is the project's, not your domain. The namespace also needs `NetworkPolicy` objects, because the platform denies traffic by default. Copy the three of [`platform-gitops/base/hello/networkpolicy.yaml`](../platform-gitops/base/hello/networkpolicy.yaml): default deny, ingress only from Traefik on the app port, egress only to DNS and to platform-api on 8083. Add whatever egress your app needs on top. Apps must be reachable only from Traefik: the assertion is the defense if that fails, not a reason to open the port ([assertion](reference/assertion.md#platform-side-guarantees)).

## 3. Route the host through Traefik

ForwardAuth and the removal of client-sent assertions run at the `websecure` entrypoint ([install.md, section 6](install.md#6-traefik)), so the route needs no middleware of its own. Model it on [`platform-gitops/base/hello/hello.yaml`](../platform-gitops/base/hello/hello.yaml):

```yaml
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: my-app
  namespace: my-app
spec:
  entryPoints: [websecure]
  routes:
    - kind: Rule
      match: Host(`app.apps.example.com`)   # exactly the host registered in step 1
      services:
        - {name: my-app, port: 8080}
  tls: {}
```

Rules that matter:

- Use an exact `Host()`. Never `HostRegexp`, a catch-all or a plain `Ingress` without a host: they can match the portal or auth host unnoticed ([ForwardAuth](reference/forwardauth.md#traefik-manifests-and-invariants)).
- The certificate must cover the host (a wildcard such as `*.apps.example.com` does).
- Do not use `/_platform/*` in the app: it is reserved for the login callback and logout ([conventions](reference/forwardauth.md#conventions)).

## 4. Validate the assertion in the app

Traefik adds `Platform-Assertion` to every authorized request. The app must verify it; it must not trust `X-Forwarded-User` or similar headers.

**Go.** Use the SDK (`go get github.com/Wysted/atrium/platform/sdk/go@<commit>`; there are no release tags yet, so pin a commit; the package is `platformauth`):

1. Inject the app id, issuer and JWKS URL by environment. A private CA for an HTTPS JWKS goes in a file; load it with `platformauth.LoadRootCAs` and pass it as `Config.RootCAs` (omit it when the CA is a system root).
2. Build one verifier at start-up and wrap the whole handler:

   ```go
   cfg := platformauth.Config{Issuer: issuer, Audience: appID, JWKSURL: jwksURL}
   if caFile != "" {
       roots, err := platformauth.LoadRootCAs(caFile)
       if err != nil { return err }
       cfg.RootCAs = roots
   }
   verifier, err := platformauth.NewVerifier(cfg)
   if err != nil { return err }
   srv.Handler = verifier.Middleware(mux) // or MiddlewareWithLogger(log, mux)
   ```

3. Read the identity in handlers with `claims, ok := platformauth.ClaimsFrom(r.Context())`. Use `Subject` as the user identifier; `Email` and `Name` are informational. `Roles` holds the role keys of your initiative.
4. Keep `/healthz` for probes: the SDK exempts it from validation, because the kubelet does not go through Traefik.
5. Never log or forward the assertion; the middleware already strips it from the request it hands to your handlers.
6. Sign-out is a plain `POST /_platform/logout` form.

Behavior: a missing or invalid assertion gives 401, unreachable signing keys give 503 ([failure behavior](reference/assertion.md#sdk-failure-behavior)). A complete app is [`platform/examples/hello`](../platform/examples/hello/README.md); its variables are `HELLO_APP_ID`, `HELLO_ISSUER`, `HELLO_JWKS_URL` and `HELLO_JWKS_CA_FILE`.

**Other languages.** There is no SDK; implement the checks in [App-side validation](reference/assertion.md#app-side-validation): `alg` is `ES256` and `typ` is `platform-assertion+jwt` (many libraries do not check `typ`), key selected by `kid` from the fixed JWKS URL (ignore `jku` and `x5u`), `iss` and `aud` exact, `exp` mandatory, health path exempt.

## 5. Grant access

Users are created on their first login with no grants, so everyone gets 403 until granted. A grant needs a user who has signed in once when it names an email.

- **Portal:** initiative > **Grants** > **Grant access**: an email, a robot or a group, a role, an optional expiry. Platform admins and the initiative's managers can do it.
- **Terminal:** `portalctl grant you@example.com my-initiative viewer` ([usage](operate.md#portalctl)).

## 6. Verify

1. Deploy the app and wait for it: `kubectl -n my-app rollout status deploy/my-app`.
2. Open `https://app.apps.example.com` in a private window. Expected: a redirect to `https://auth.apps.example.com/login`, then Google, then back to the app.
3. With a grant you reach the app and `ClaimsFrom` shows your `sub` and roles. Without one you get 403.
4. In the portal, initiative > **Apps** > **Health** on the app row. A healthy app shows no problems. Expected right after deploy: `never_probed` (the portal probes on its interval) and `never_seen` (until an authorized request arrives); `never_probed` clears after the next probe round and `never_seen` when the first authorized request arrives (step 3 causes it). Anything else is a finding:

| Finding | Meaning and fix |
|---|---|
| `unprotected` | The host was served without ForwardAuth: the route is not on the `websecure` entrypoint chain. Fix the route or the entrypoint middlewares |
| `unexpected_redirect` | The host redirects somewhere other than the auth host login. Check the registered host and the app's own redirects |
| `unreachable` | DNS, TLS or timeout from the portal. Check the DNS record, the certificate and that the portal can reach Traefik |
| `no_grants`, `no_manager` | Nobody holds a role yet, or no human holds a `manages_access` role. Grant access (step 5) |
| `never_seen` plus `unprotected` | No route matches the host: Traefik answers 404 |

Other codes (`app_disabled`, `initiative_inactive`, `not_seen_recently`, `probe_stale`, `probe_disabled`): [Health of an app](reference/metrics.md#health-of-an-app).

## Next step

Operating the platform that serves the app: [operate.md](operate.md).
