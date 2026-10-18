# Documentación de atrium

> English version: [docs/README.md](../README.md)

Empieza por [architecture.md](architecture.md) para ver cómo encajan las piezas; usa las páginas de referencia para el comportamiento exacto.

## Adoptantes (operar atrium)

| Documento | Qué cubre |
|---|---|
| [install.md](install.md) | Instalar atrium en tu propio clúster de Kubernetes |
| [operate.md](operate.md) | portalctl, actualizaciones, backup y restauración, monitoreo, endurecimiento (hardening), resolución de problemas |
| [Rotación del secreto de cliente de Google](runbooks/google-client-secret-rotation.md) | Runbook para rotar el secreto del cliente OAuth |
| [architecture.md](architecture.md) | Componentes, flujo de solicitudes, modelo de confianza, baja (offboarding), glosario |
| [reference/configuration.md](reference/configuration.md) | Cada variable de entorno y archivo de secreto de cada binario |
| [reference/login.md](reference/login.md) | Login de Google, sesiones, cookies |
| [reference/metrics.md](reference/metrics.md) | Catálogo de métricas, probe, salud de integración |
| [reference/keys.md](reference/keys.md) | Llaves de firma, rotación, JWKS |
| [reference/api.md](reference/api.md) | API del portal: quién administra qué, tokens, robots, áreas |
| [SECURITY.md](repo/SECURITY.md) | Cómo reportar una vulnerabilidad |

## Desarrolladores de apps (poner una app detrás de atrium)

| Documento | Qué cubre |
|---|---|
| [onboard-an-app.md](onboard-an-app.md) | Registrar una app, enrutar su host, validar la aserción, otorgar acceso |
| [reference/assertion.md](reference/assertion.md) | El JWT `Platform-Assertion` y cómo validarlo |
| [reference/forwardauth.md](reference/forwardauth.md) | Qué hace ForwardAuth con las solicitudes, rutas reservadas, errores |
| [platform/examples/hello/README.md](repo/platform/examples/hello/README.md) | La app de ejemplo: ejecutar y construir |

## Contribuidores

| Documento | Qué cubre |
|---|---|
| [README.md](repo/README.md) | Visión general del proyecto |
| [CONTRIBUTING.md](repo/CONTRIBUTING.md) | Desarrollo local, verificaciones y convenciones |
| [platform-gitops/README.md](repo/platform-gitops/README.md) | Estructura de manifiestos, el clúster k3d local, sus scripts, seed y smoke test |
| [reference/data-model.md](reference/data-model.md) | Esquema de PostgreSQL, restricciones, roles de base de datos |
| [decisions/](decisions/) | Registros de decisiones de arquitectura (ADRs 0001 a 0011) |
| [platform/web/COMPONENTS.md](repo/platform/web/COMPONENTS.md) | Los componentes de la UI web del portal |
