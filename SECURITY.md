# Security policy

## Supported versions

| Version | Supported |
|---|---|
| `main` | yes |
| 0.1.x | yes |
| older | no |

The project is pre-1.0: fixes land on `main` and in a new patch release of the latest minor version only. The Go SDK
(`platform/sdk/go`) is versioned with the repository; its tags are `platform/sdk/go/vX.Y.Z`.

## Reporting a vulnerability

Do not open a public issue. Report it privately through GitHub: the repository's **Security** tab,
**Report a vulnerability** (private vulnerability reporting).

Include the affected component (platform-api, portal, SDK, manifests), the commit, steps to reproduce and the impact
you expect. The project has a single maintainer: expect an acknowledgement within 7 days and a fix or a plan within
30 days. Please keep the report private until a fix is published.

## Scope

In scope: authentication and session handling, the ForwardAuth decision, assertion signing and verification (SDK
included), API tokens, authorization data, and the shipped Kubernetes manifests (`platform-gitops/base`).
The local development environment (`local/`, `platform-gitops/overlays/local*`, `spikes/`) is not meant for
production and is out of scope unless the issue also affects `base/`.
