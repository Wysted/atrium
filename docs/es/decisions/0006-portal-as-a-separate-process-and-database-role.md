# 0006. Portal como proceso separado con su propio rol de base de datos

> English version: [docs/decisions/0006-portal-as-a-separate-process-and-database-role.md](../../decisions/0006-portal-as-a-separate-process-and-database-role.md)

- Estado: Aceptada
- Fecha: 2026-10-15

## Contexto y problema

La administración necesita una UI web y una API, y necesita acceso de escritura a las tablas de autorización. Servir esa UI desde el host de autenticación permitiría que un XSS allí comprometa el login de todas las apps, y darle esas escrituras a platform-api ampliaría lo que puede hacer un compromiso de la ruta de login.

## Opciones consideradas

- Servir la UI de administración desde platform-api en el host de autenticación: descartada (radio de impacto de un XSS).
- Un proceso `portal` separado, registrado como una app ordinaria, con su propio host y rol de base de datos (elegida).

## Resultado de la decisión

El portal es su propio Deployment (`cmd/portal`, mismo módulo Go) en su propio host detrás del mismo ForwardAuth. Nunca posee la KEK ni ninguna llave de firma. platform-api corre como `platform_runtime` con solo lo que ForwardAuth y el login necesitan; el portal corre como `platform_portal`. Ambos roles se verifican al arrancar contra un conjunto esperado de privilegios, en ambas direcciones, y el proceso se niega a arrancar si hay desvío. Los permisos del portal se resuelven desde la base de datos en cada request (nunca viajan en la aserción, que quedaría desactualizada durante su vida) mediante una función pura `adminz.Decide`, en la misma transacción que la mutación. Un recurso no autorizado de otra iniciativa responde 404 para no confirmar su existencia.

## Consecuencias

- El Job de migración debe completarse antes de desplegar platform-api y el portal; de lo contrario los pods nuevos ven grants antiguos y se niegan a arrancar (los pods viejos siguen sirviendo).
- Una nueva columna escribible necesita un grant explícito y una entrada en el conjunto esperado.
- Referencia: [API del portal](../reference/api.md), [Roles de base de datos](../reference/data-model.md#roles-de-base-de-datos).
