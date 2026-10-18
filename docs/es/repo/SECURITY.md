# Política de seguridad

> English version: [SECURITY.md](../../../SECURITY.md)

## Versiones soportadas

| Versión | Soportada |
|---|---|
| `main` | sí |
| 0.1.x | sí |
| anteriores | no |

El proyecto es pre-1.0: las correcciones llegan a `main` y a un nuevo patch release de la última versión menor únicamente. El SDK de Go (`platform/sdk/go`) se versiona junto con el repositorio; sus tags son `platform/sdk/go/vX.Y.Z`.

## Reportar una vulnerabilidad

No abras un issue público. Repórtala de forma privada a través de GitHub: la pestaña **Security** del repositorio, **Report a vulnerability** (reporte privado de vulnerabilidades).

Incluye el componente afectado (platform-api, portal, SDK, manifiestos), el commit, los pasos para reproducir y el impacto que esperas. El proyecto tiene un único mantenedor: espera un acuse de recibo dentro de 7 días y una corrección o un plan dentro de 30 días. Por favor mantén el reporte privado hasta que se publique una corrección.

## Alcance

Dentro del alcance: autenticación y manejo de sesiones, la decisión de ForwardAuth, la firma y verificación de aserciones (SDK incluido), tokens de API, datos de autorización y los manifiestos de Kubernetes distribuidos (`platform-gitops/base`).
El entorno de desarrollo local (`local/`, `platform-gitops/overlays/local*`, `spikes/`) no está pensado para producción y queda fuera del alcance, salvo que el problema también afecte a `base/`.
