# Contribuir a atrium

> English version: [CONTRIBUTING.md](../../../CONTRIBUTING.md)

atrium tiene un único mantenedor y no requiere CLA. Los issues y pull requests son bienvenidos. Para reportar una vulnerabilidad, sigue
`SECURITY.md`; no abras un issue público.

## Herramientas

Go, Node y las herramientas del cluster nunca se instalan en el host. El host necesita Docker (con compose), bash y git;
los scripts del cluster local y el smoke también usan `openssl`, `curl` y `python3`. Tres wrappers en la raíz del repo ejecutan
contenedores fijados (`compose.yaml`) con tu uid y el subdirectorio actual como directorio de trabajo:

| Wrapper | Corre en | Úsalo para |
|---|---|---|
| `./gk <cmd>` | contenedor de Go | `go test`, `go run`, `go build` |
| `./nk <cmd>` | contenedor de Node | `npm` en `platform/web` |
| `./tk <cmd>` | contenedor toolbox (`tools/`) | `kubectl`, `helm`, `k3d`, y los scripts `local/*.sh` |

Sin argumentos cada uno abre una shell. `.tools/` es estado local ignorado por git: secretos, KEKs, kubeconfig, cachés de Go, npm y Helm,
y los datos de PostgreSQL. Nunca lo commitees.

## Estructura del repositorio

Ver la tabla en `README.md`. En resumen: el código Go vive en `platform/` (workspace `platform/go.work` con `api`,
`sdk/go` y `examples/hello`), la app web en `platform/web`, los manifiestos de Kubernetes en `platform-gitops`, y
`spikes/` es descartable.

## Chequeos

```bash
local/db-up.sh                                       # local PostgreSQL for the Go tests (needs Docker)
(cd platform/api && ../../gk go test ./...)          # repeat in platform/sdk/go and platform/examples/hello
local/lint.sh                                        # golangci-lint, govulncheck, alert rules (lint|vuln|rules to narrow)
platform-gitops/monitoring/test-rules.sh             # alert rules only (promtool via Docker)
cd platform/web && ../../nk npm ci                   # first time; then, from platform/web:
../../nk npm run generate && ../../nk npm run check:csp
../../nk npm run lint && ../../nk npm run typecheck
```

## Cluster local

Todos los comandos se ejecutan desde la raíz del repo. Los overlays y el smoke se describen en `platform-gitops/README.md`.

```bash
./tk local/cluster-up.sh                             # once: k3d cluster `portal` and Traefik
./tk local/certs-up.sh                               # once: cert-manager and the wildcard certificate (see "Local TLS")
BUILD=1 bash platform-gitops/overlays/local/up.sh    # deploy with the fake login
bash platform-gitops/overlays/local/smoke.sh         # end-to-end checks; section 21 is the browser check
```

Usa `BUILD=1` después de cambiar código: el tag de la imagen nunca cambia, así que sin él el script importa la imagen que ya
esté en el host, que puede estar desactualizada. `SKIP_BROWSER_CHECK=1` omite la sección de Chromium del smoke.

### TLS local

`*.apps.wysted.dev` resuelve a 127.0.0.1 mediante DNS público, por lo que el cluster necesita un certificado wildcard para ese dominio.
`certs-up.sh` elige el emisor:

- Sin `.tools/secrets/cloudflare-token` (el valor por defecto para contribuidores), o `./tk local/certs-up.sh local`: una CA local construida
  por cert-manager (`local/cert-manager/local-ca.yaml`) firma el wildcard. El certificado de la CA (dato público) se
  exporta a `.tools/secrets/local-ca.crt`. `smoke.sh` verifica contra él (`CURL_CA_BUNDLE` y `SSL_CERT_FILE`, nunca
  `curl -k`), `up.sh` lo agrega al probe de integración del portal (`overlays/local/portal-local-ca`) y el chequeo
  de navegador confía en él (Node) o acepta solo la hoja wildcard, verificada contra la CA y fijada por hash SPKI (Chromium
  `--ignore-certificate-errors-spki-list`; la imagen fijada de Playwright no tiene una herramienta para agregar una raíz sin ser root). La CA
  está restringida por nombre a `apps.wysted.dev`.
- Con el token de Cloudflare del mantenedor, el comando sin argumentos emite el wildcard de staging de Let's Encrypt (DNS-01), en el que
  `curl` no confía; `./tk local/certs-up.sh prod` emite el de producción. Un `staging` o `prod` explícito
  sin el token es un error, nunca un fallback. Ambos eliminan `.tools/secrets/local-ca.crt`.

Cambiar de modo vuelve a emitir el certificado; luego vuelve a ejecutar `up.sh`, que elige la variante del portal a partir del archivo de la CA.

Para navegar con un navegador en modo CA local, importa `.tools/secrets/local-ca.crt` como autoridad de confianza (Chrome/Edge:
Settings > Privacy and security > Security > Manage certificates > Authorities; Firefox: Settings > Privacy & Security >
Certificates > View Certificates > Authorities > Import). Cuando termines, destruye la llave de la CA y luego quita la CA del navegador: la llave vive solo en el cluster, por lo que
`k3d cluster delete portal` o `./tk kubectl -n cert-manager delete secret local-ca-tls` la destruye (elimina el
Secret antes de quitarla del navegador, para que nada pueda firmar con una CA en la que todavía confías).

El smoke funciona solo en modo local (login falso). El modo de login real con Google (`overlays/local-google`) necesita tu propio
cliente OAuth en `.tools/secrets/` (ver "Login real con Google" en `platform-gitops/README.md`). Para ejecutar el smoke desde ese modo:

```bash
bash platform-gitops/overlays/local-google/down.sh                # back to the fake login
BUILD=1 bash platform-gitops/overlays/local/up.sh && bash platform-gitops/overlays/local/smoke.sh
BUILD=1 bash platform-gitops/overlays/local-google/up.sh          # return to Google mode
```

## Convenciones

- Conventional Commits. El código, los comentarios, el texto de la UI, la documentación y los mensajes de commit están en inglés.
- La documentación tiene una copia en español en `docs/es/` (mismas rutas; la documentación fuera de `docs/` vive en
  `docs/es/repo/`). Un cambio de documentación actualiza ambos idiomas en el mismo commit.
- Mantén los cambios de código y de documentación en commits separados.
- Nunca commitees secretos ni datos personales: este repositorio es público.
