# Contributing to atrium

atrium has a single maintainer and no CLA. Issues and pull requests are welcome. To report a vulnerability, follow
`SECURITY.md`; do not open a public issue.

## Tooling

Go, Node and the cluster tools are never installed on the host. The host needs Docker (with compose), bash and git;
the local cluster scripts and the smoke also use `openssl`, `curl` and `python3`. Three wrappers in the repo root run
pinned containers (`compose.yaml`) with your uid and the current subdirectory as the working directory:

| Wrapper | Runs in | Use for |
|---|---|---|
| `./gk <cmd>` | Go container | `go test`, `go run`, `go build` |
| `./nk <cmd>` | Node container | `npm` in `platform/web` |
| `./tk <cmd>` | toolbox container (`tools/`) | `kubectl`, `helm`, `k3d`, and the `local/*.sh` scripts |

With no arguments each opens a shell. `.tools/` is git-ignored local state: secrets, KEKs, kubeconfig, Go, npm and Helm
caches, and the PostgreSQL data. Never commit it.

## Repository layout

See the table in `README.md`. In short: Go code lives in `platform/` (workspace `platform/go.work` with `api`,
`sdk/go` and `examples/hello`), the web app in `platform/web`, Kubernetes manifests in `platform-gitops`, and
`spikes/` is disposable.

## Checks

```bash
local/db-up.sh                                       # local PostgreSQL for the Go tests (needs Docker)
(cd platform/api && ../../gk go test ./...)          # repeat in platform/sdk/go and platform/examples/hello
local/lint.sh                                        # golangci-lint, govulncheck, alert rules (lint|vuln|rules to narrow)
platform-gitops/monitoring/test-rules.sh             # alert rules only (promtool via Docker)
cd platform/web && ../../nk npm ci                   # first time; then, from platform/web:
../../nk npm run generate && ../../nk npm run check:csp
../../nk npm run lint && ../../nk npm run typecheck
```

## Local cluster

All commands run from the repo root. The overlays and the smoke are described in `platform-gitops/README.md`.

```bash
./tk local/cluster-up.sh                             # once: k3d cluster `portal` and Traefik
./tk local/certs-up.sh prod                          # once: cert-manager and the wildcard certificate (see below)
BUILD=1 bash platform-gitops/overlays/local/up.sh    # deploy with the fake login
bash platform-gitops/overlays/local/smoke.sh         # end-to-end checks; section 21 is the browser check
```

Use `BUILD=1` after changing code: the image tag never changes, so without it the script imports whatever image is
already on the host, which may be stale. `SKIP_BROWSER_CHECK=1` skips the Chromium section of the smoke.
`certs-up.sh` without `prod` issues a Let's Encrypt staging certificate, which the smoke's `curl` does not trust.

The smoke works only in local (fake login) mode. The real Google login mode (`overlays/local-google`) needs your own
OAuth client in `.tools/secrets/` (see "Real Google login" in `platform-gitops/README.md`). To run the smoke from it:

```bash
bash platform-gitops/overlays/local-google/down.sh                # back to the fake login
BUILD=1 bash platform-gitops/overlays/local/up.sh && bash platform-gitops/overlays/local/smoke.sh
BUILD=1 bash platform-gitops/overlays/local-google/up.sh          # return to Google mode
```

Known limitation: the local cluster serves `*.apps.wysted.dev`, which resolves to 127.0.0.1 through public DNS, with a
wildcard certificate issued by cert-manager through a DNS-01 challenge with the maintainer's Cloudflare token.
`local/certs-up.sh` exits with an error if `.tools/secrets/cloudflare-token` is missing, and there is no fallback, so
outside contributors cannot run the local cluster's TLS or the smoke yet (tracked in `TODO.md`). The Go and web checks
do not need it.

## Conventions

- Conventional Commits. Code, comments, UI text, docs and commit messages are in English.
- Keep code and docs changes in separate commits.
- Never commit secrets or personal data: this repository is public.
