# 0003. Listeners separados por audiencia

> English version: [docs/decisions/0003-separate-listeners-per-audience.md](../../decisions/0003-separate-listeners-per-audience.md)

- Estado: Aceptada
- Fecha: 2026-10-14

## Contexto y problema

platform-api debe exponer `/forwardauth` solo a Traefik, el JWKS solo a las apps, las rutas de navegador solo a través de la ruta del host de autenticación y `/metrics` solo al monitoreo. Una NetworkPolicy de Kubernetes filtra por puerto, nunca por path.

## Opciones consideradas

- Un listener con enrutamiento por path: un path no puede protegerse con una NetworkPolicy, por lo que Traefik o las apps alcanzarían rutas que no deben.
- Un listener por audiencia (elegida).

## Resultado de la decisión

platform-api sirve cuatro listeners: internal (`/forwardauth`, `/healthz`, `/readyz`), jwks, public (rutas de navegador del host de autenticación) y metrics. Sus direcciones (`PLATFORM_INTERNAL_ADDR`, `PLATFORM_PUBLIC_ADDR`, `PLATFORM_METRICS_ADDR`, `PLATFORM_JWKS_ADDR`) son obligatorias y deben usar puertos distintos. Los namespaces de las apps alcanzan un puerto que no sirve nada más que el JWKS. El portal sigue la misma regla para sus métricas (`/metrics` nunca se sirve en otro listener).

## Consecuencias

- Cuatro puertos y cuatro reglas de NetworkPolicy que mantener; un host comodín entra en conflicto con cualquier host en el mismo puerto, por lo que los puertos deben ser distintos.
- `/metrics` no tiene autenticación propia: la NetworkPolicy debe permitir solo el namespace de monitoreo, y ninguna IngressRoute debe enrutar hacia él.
- Referencia: [Listeners](../reference/forwardauth.md#listeners).
