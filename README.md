# atrium

One entrance to a company's internal apps. atrium is self-hosted: Google Workspace sign-in, per-app authorization kept
in PostgreSQL, and an identity-aware proxy in front of every app through Traefik ForwardAuth. Each request that passes
reaches the app with a short-lived signed JWT whose audience is that app; the app verifies it with a small Go SDK
(`go get github.com/Wysted/atrium/platform/sdk/go`). Its web portal lists each user's apps and lets administrators
manage initiatives (groups of apps), roles, grants, API tokens and areas.

## Status and limits

- Pre-1.0: v0.1.0 is the first tagged release. Configuration, API and SDK may still change between minor versions.
- Google (Workspace or Cloud Identity) is the only supported identity provider today.
- Tested only in a local k3d cluster so far; `TODO.md` lists what is still missing before production.

## Layout

| Path | What it is |
|---|---|
| `platform/api` | Go module with the `platform-api` (login, sessions, ForwardAuth, signing keys, JWKS), `portal` (portal app and its API) and `portalctl` (CLI) commands |
| `platform/web` | Nuxt 4 SPA of the portal, embedded in the `portal` binary |
| `platform/sdk/go` | SDK that apps use to verify the assertion |
| `platform/examples/hello` | minimal app behind the platform, the starting point for a new app |
| `docs` | architecture, reference pages and decision records |
| `platform-gitops` | Kustomize manifests (`base/` for adopters, `overlays/local*` for the local cluster) |
| `local` | k3d cluster, Traefik and cert-manager setup for local development |
| `spikes` | the ForwardAuth spike that validated the design (historical) |

## Documentation

| Document | What it covers |
|---|---|
| `docs/README.md` | index of the documentation by audience |
| `docs/es/` | Spanish copy of the documentation ([docs/es/repo/README.md](docs/es/repo/README.md)) |
| `docs/install.md` | install atrium on your cluster |
| `docs/onboard-an-app.md` | put an internal app behind atrium end to end |
| `docs/operate.md` | portalctl, upgrades, backup, monitoring, hardening and troubleshooting |
| `docs/architecture.md` | components, request flow, trust model and glossary |
| `docs/reference/` | assertion, ForwardAuth, keys, login, configuration, data model, portal API and metrics |
| `docs/runbooks/` | operational runbooks (Google client secret rotation) |
| `platform-gitops/README.md` | Kubernetes manifests, the local cluster, its scripts and the smoke test |
| `platform/examples/hello/README.md` | the minimal app behind the platform |
| `platform/web/COMPONENTS.md` | the portal web UI components |
| `CONTRIBUTING.md` | local development, checks and conventions |
| `SECURITY.md` | how to report a vulnerability |

## License and security

Apache License 2.0, see `LICENSE`. To report a vulnerability, see `SECURITY.md`.
