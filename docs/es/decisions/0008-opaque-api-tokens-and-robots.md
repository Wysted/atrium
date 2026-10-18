# 0008. Tokens de API opacos y robots para clientes que no son navegadores

> English version: [docs/decisions/0008-opaque-api-tokens-and-robots.md](../../decisions/0008-opaque-api-tokens-and-robots.md)

- Estado: Aceptada
- Fecha: 2026-10-16

## Contexto y problema

Las CLIs y la automatización necesitan llamar a la API del portal, y contener una credencial filtrada no debe requerir un navegador. El producto es multiempresa, por lo que el diseño no asume ningún sistema de CI, gestor de secretos ni stack del lado del adoptante.

## Opciones consideradas

- ID tokens de Google: descartados. `gcloud auth print-identity-token --audiences` no funciona con cuentas de usuario, y la identidad de Google evitaría la revocación y la auditoría de la plataforma.
- Tokens de acceso personales y de robot, un solo mecanismo (elegida, fase 1).
- Workload identity (emisores OIDC por adoptante, mapeados a robots mediante una política de confianza): solo dirección (fase 2).
- OAuth device flow: no planificado (fase 3); las campañas de phishing de device-code apuntan a administradores.

## Resultado de la decisión

Secretos `ptk_` opacos (32 bytes aleatorios más prefijo y un chequeo de formato CRC-32). Solo se guarda el SHA-256; la búsqueda es por hash, sin caché, por lo que la revocación aplica en el siguiente request en cada réplica. La expiración es obligatoria con un tope por tipo de dueño (30 días personal, 90 días robot), porque un token humano no debe sobrevivir mucho a una suspensión de Google mientras que los robots no se dan de baja (offboarding) a través de Google. El scope (`read`/`admin`) es un techo, nunca un grant: el permiso efectivo es el permiso vigente del dueño intersectado con él. Los tokens se crean solo desde una sesión de navegador, de modo que un token filtrado no puede extenderse a sí mismo, y la revocación por secreto permite a un admin contener una filtración encontrada por cualquier scanner. En un host del portal, `Authorization` toma exclusivamente la ruta de token (sin fallback a cookie); Traefik lo elimina después de ForwardAuth para que el secreto nunca llegue al proceso del portal.

## Consecuencias

- Un secreto bearer funciona para quien lo posea hasta que se revoque o expire; no hay restricción de emisor.
- El rol del portal puede insertar un token con un hash que conoce; el rastro de auditoría es el control para eso.
- Las fases 2 y 3 siguen abiertas: [TODO.md](../../../TODO.md).
- Referencia: [Tokens](../reference/api.md#tokens), [ruta de token](../reference/forwardauth.md#ruta-de-token).
