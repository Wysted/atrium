# atrium documentation

Start with [architecture.md](architecture.md) to see how the pieces fit; use the reference pages for exact behavior.

Español: [es/README.md](es/README.md).

## Adopters (operating atrium)

| Document | What it covers |
|---|---|
| [install.md](install.md) | Install atrium on your own Kubernetes cluster |
| [operate.md](operate.md) | portalctl, upgrades, backup and restore, monitoring, hardening, troubleshooting |
| [Google client secret rotation](runbooks/google-client-secret-rotation.md) | Runbook for rotating the OAuth client secret |
| [architecture.md](architecture.md) | Components, request flow, trust model, offboarding, glossary |
| [reference/configuration.md](reference/configuration.md) | Every environment variable and secret file of each binary |
| [reference/login.md](reference/login.md) | Google login, sessions, cookies |
| [reference/metrics.md](reference/metrics.md) | Metrics catalog, probe, integration health |
| [reference/keys.md](reference/keys.md) | Signing keys, rotation, JWKS |
| [reference/api.md](reference/api.md) | Portal API: who administers what, tokens, robots, areas |
| [SECURITY.md](../SECURITY.md) | How to report a vulnerability |

## App developers (putting an app behind atrium)

| Document | What it covers |
|---|---|
| [onboard-an-app.md](onboard-an-app.md) | Register an app, route its host, validate the assertion, grant access |
| [reference/assertion.md](reference/assertion.md) | The `Platform-Assertion` JWT and how to validate it |
| [reference/forwardauth.md](reference/forwardauth.md) | What ForwardAuth does to requests, reserved paths, errors |
| [platform/examples/hello/README.md](../platform/examples/hello/README.md) | The example app: run and build |

## Contributors

| Document | What it covers |
|---|---|
| [CONTRIBUTING.md](../CONTRIBUTING.md) | Local development, checks and conventions |
| [platform-gitops/README.md](../platform-gitops/README.md) | Manifest layout, the local k3d cluster, its scripts, seed and smoke test |
| [reference/data-model.md](reference/data-model.md) | PostgreSQL schema, constraints, database roles |
| [decisions/](decisions/) | Architecture decision records (ADRs 0001 to 0011) |
| [platform/web/COMPONENTS.md](../platform/web/COMPONENTS.md) | The portal web UI components |
