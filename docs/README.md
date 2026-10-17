# atrium documentation

Start with [architecture.md](architecture.md) to see how the pieces fit; use the reference pages for exact behavior.

## Adopters (operating atrium)

| Document | What it covers |
|---|---|
| [architecture.md](architecture.md) | Components, request flow, trust model, offboarding, glossary |
| [platform-gitops/README.md](../platform-gitops/README.md) | Kubernetes manifests, overlays, the smoke test and runbooks |
| [Google client secret rotation](../platform-gitops/docs/runbooks/google-client-secret-rotation.md) | Runbook for rotating the OAuth client secret |
| [reference/login.md](reference/login.md) | Google login, sessions, configuration variables |
| [reference/metrics.md](reference/metrics.md) | Metrics catalog, probe, integration health |
| [reference/keys.md](reference/keys.md) | Signing keys, rotation, JWKS |
| [reference/api.md](reference/api.md) | Portal API: who administers what, tokens, robots, areas |
| [SECURITY.md](../SECURITY.md) | How to report a vulnerability |

## App developers (putting an app behind atrium)

| Document | What it covers |
|---|---|
| [reference/assertion.md](reference/assertion.md) | The `Platform-Assertion` JWT and how to validate it |
| [reference/forwardauth.md](reference/forwardauth.md) | What ForwardAuth does to requests, reserved paths, errors |
| [platform/examples/hello/README.md](../platform/examples/hello/README.md) | Minimal app behind the platform |

## Contributors

| Document | What it covers |
|---|---|
| [CONTRIBUTING.md](../CONTRIBUTING.md) | Local development, checks and conventions |
| [reference/data-model.md](reference/data-model.md) | PostgreSQL schema, constraints, database roles |
| [decisions/](decisions/) | Architecture decision records (ADRs 0001 to 0011) |
| [platform/web/COMPONENTS.md](../platform/web/COMPONENTS.md) | The portal web UI components |
