# atrium

One entrance to a company's internal apps. atrium is self-hosted: Google Workspace sign-in, per-app authorization kept
in PostgreSQL, and an identity-aware proxy in front of every app through Traefik ForwardAuth. Each request that passes
reaches the app with a short-lived signed JWT whose audience is that app; the app verifies it with a small Go SDK
(`go get github.com/Wysted/atrium/platform/sdk/go`). Its web portal lists each user's apps and lets administrators
manage initiatives (groups of apps), roles, grants, API tokens and areas.

Status: pre-release. It runs end to end in a local k3d cluster; see `TODO.md` for what is still missing before production.

## Layout

| Path | What it is |
|---|---|
| `platform/api` | Go module with the `platform-api` (login, sessions, ForwardAuth, signing keys, JWKS), `portal` (portal app and its API) and `portalctl` (CLI) commands |
| `platform/web` | Nuxt 4 SPA of the portal, embedded in the `portal` binary |
| `platform/sdk/go` | SDK that apps use to verify the assertion |
| `platform/examples/hello` | minimal app behind the platform, the starting point for a new app |
| `platform/docs/contract.md` | the contract: assertion, ForwardAuth, keys, data model, login, portal API |
| `platform-gitops` | Kustomize manifests (`base/` for adopters, `overlays/local*` for the local cluster) and runbooks |
| `local` | k3d cluster, Traefik and cert-manager setup for local development |
| `spikes`, `reports`, `research_notes` | the ForwardAuth spike and the research behind the design |

## Local development

Nothing is installed on the host: Go, Node and the cluster tools run in Docker through `./gk`, `./nk` and `./tk`
(`compose.yaml`). The local cluster, its scripts and the smoke test are described in `platform-gitops/README.md`.

## License and security

Apache License 2.0, see `LICENSE`. To report a vulnerability, see `SECURITY.md`.
