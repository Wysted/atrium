# 0002. Llaves de firma envueltas con KEK en PostgreSQL

> English version: [docs/decisions/0002-kek-wrapped-signing-keys-in-postgresql.md](../../decisions/0002-kek-wrapped-signing-keys-in-postgresql.md)

- Estado: Aceptada
- Fecha: 2026-10-14

## Contexto y problema

Las aserciones se firman con llaves ES256 que deben ser compartidas por todas las réplicas de platform-api, rotar sin downtime y ser revocables en una emergencia. Guardar las llaves en la base de datos las deja disponibles para cada réplica, pero un dump de la base no debe exponerlas.

## Opciones consideradas

- Llaves privadas selladas con una llave de cifrado (KEK) montada desde un Secret de Kubernetes y guardadas en la tabla `signing_keys` (elegida).
- Un KMS externo: no se hace en v1; el endurecimiento para producción se sigue en [TODO.md](../../../TODO.md).

## Resultado de la decisión

Cifrado de sobre (envelope encryption): la KEK nunca llega a la base de datos, `kid` es el thumbprint de RFC 7638, y las llaves pasan por `next`, `active` (30 días) y `retired` (1 h). Siempre hay una llave `next` publicada, creada justo después de cada promoción, de modo que una revocación de emergencia normalmente promueve una llave que las apps ya conocen. La rotación es automática, una réplica a la vez bajo un advisory lock de PostgreSQL, y los timestamps vienen del reloj de la base de datos. Una llave se promueve solo después de que su llave privada se descifra dentro de la misma transacción. Una réplica que no puede cargar la llave activa deja de firmar (fail closed). Cada llave guarda una huella `kek_id` no reversible de la KEK que la selló, por lo que una réplica con una KEK distinta reporta `KEKMismatch`, falla el readiness y se niega a rotar o revocar en lugar de reemplazar llaves que no puede leer. Las llaves selladas llevan un byte de versión de formato para que la KEK pueda rotarse volviendo a sellar.

## Consecuencias

- La rotación de emergencia deja una ventana acotada: las apps pueden seguir aceptando una llave revocada hasta que expire su caché JWKS (máx. 5 minutos, hasta 20 con stale-if-error, más 30 s para una réplica que perdió la notificación).
- Quien pueda leer el Secret de la KEK y la base de datos puede descifrar todas las llaves.
- Referencia: [Llaves de firma, rotación, JWKS](../reference/keys.md).
