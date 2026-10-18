# atrium

> English version: [README.md](../../../README.md)

Una única entrada a las apps internas de una empresa. atrium es auto-alojado: inicio de sesión con Google Workspace, autorización por app guardada en PostgreSQL y un proxy consciente de la identidad delante de cada app mediante Traefik ForwardAuth. Cada solicitud que pasa llega a la app con un JWT firmado de corta vida cuya audiencia es esa app; la app lo verifica con un pequeño SDK de Go (`go get github.com/Wysted/atrium/platform/sdk/go`). Su portal web lista las apps de cada usuario y permite a los administradores gestionar iniciativas (grupos de apps), roles, grants (asignaciones de rol), tokens de API y áreas.

## Estado y límites

- Pre-1.0: v0.1.0 es el primer release etiquetado. La configuración, la API y el SDK aún pueden cambiar entre versiones menores.
- Google (Workspace o Cloud Identity) es hoy el único proveedor de identidad soportado.
- Probado hasta ahora solo en un cluster k3d local; `TODO.md` lista lo que aún falta antes de producción.

## Estructura

| Ruta | Qué es |
|---|---|
| `platform/api` | Módulo Go con los comandos `platform-api` (login, sesiones, ForwardAuth, llaves de firma, JWKS), `portal` (app del portal y su API) y `portalctl` (CLI) |
| `platform/web` | SPA Nuxt 4 del portal, embebida en el binario `portal` |
| `platform/sdk/go` | SDK que usan las apps para verificar la aserción |
| `platform/examples/hello` | app mínima detrás de la plataforma, el punto de partida para una app nueva |
| `docs` | arquitectura, páginas de referencia y registros de decisiones |
| `platform-gitops` | manifiestos Kustomize (`base/` para adoptantes, `overlays/local*` para el cluster local) |
| `local` | cluster k3d, configuración de Traefik y cert-manager para desarrollo local |
| `spikes` | el spike de ForwardAuth que validó el diseño (histórico) |

## Documentación

| Documento | Qué cubre |
|---|---|
| [`docs/README.md`](../README.md) | índice de la documentación por audiencia |
| [`docs/install.md`](../install.md) | instalar atrium en tu cluster |
| [`docs/onboard-an-app.md`](../onboard-an-app.md) | poner una app interna detrás de atrium de punta a punta |
| [`docs/operate.md`](../operate.md) | portalctl, actualizaciones, backup, monitoreo, endurecimiento y resolución de problemas |
| [`docs/architecture.md`](../architecture.md) | componentes, flujo de solicitudes, modelo de confianza y glosario |
| [`docs/reference/`](../reference/) | aserción, ForwardAuth, llaves, login, configuración, modelo de datos, API del portal y métricas |
| [`docs/runbooks/`](../runbooks/) | runbooks operativos (rotación del client secret de Google) |
| [`platform-gitops/README.md`](platform-gitops/README.md) | manifiestos de Kubernetes, el cluster local, sus scripts y el smoke test |
| [`platform/examples/hello/README.md`](platform/examples/hello/README.md) | la app mínima detrás de la plataforma |
| [`platform/web/COMPONENTS.md`](platform/web/COMPONENTS.md) | los componentes de la UI web del portal |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | desarrollo local, chequeos y convenciones |
| [`SECURITY.md`](SECURITY.md) | cómo reportar una vulnerabilidad |

## Licencia y seguridad

Apache License 2.0, ver [`LICENSE`](../../../LICENSE). Para reportar una vulnerabilidad, ver `SECURITY.md`.
