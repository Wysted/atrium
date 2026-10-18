# API del portal

> English version: [docs/reference/api.md](../../reference/api.md)

La API JSON del proceso `portal`: quién puede hacer qué, cómo se autorizan y protegen las llamadas, y cada endpoint. La UI web es un cliente de esta API; los permisos siempre los resuelve la API y la UI solo oculta lo que el llamador no puede hacer. Fundamento: [ADR 0006](../decisions/0006-portal-as-a-separate-process-and-database-role.md), [ADR 0007](../decisions/0007-fetch-metadata-instead-of-csrf-tokens.md).

## Convenciones

- `/api/v1/*`, errores como `application/problem+json`. Las URL usan ids, nunca llaves. Los listados usan paginación por keyset con un cursor opaco.
- Cuando una mutación pierde contra transacciones concurrentes (deadlock o fallo de serialización) en cada uno de sus
  pocos intentos, la respuesta es `503` con `Retry-After`, nunca `500`; las mutaciones son idempotentes por llave natural
  (llave de iniciativa, llave de rol, host, grant (asignación de rol)), así que el cliente puede simplemente reintentar; crear una app no tiene ninguna (el id
  se genera), por lo que un reintento tras un fallo ambiguo puede dejar una segunda app.

## Endpoints

| Endpoint | Propósito |
|---|---|
| `GET /api/v1/me` | Perfil y permisos (administrador de plataforma, iniciativas que gestiona), para que la UI oculte acciones |
| `GET /api/v1/me/apps` | Home: apps que el llamador puede abrir, con host, iniciativa y roles |
| `GET, POST /api/v1/initiatives`; `GET, PATCH /api/v1/initiatives/{id}` | Iniciativas (`PATCH`: `status`, `name`; el listado muestra a cada llamador solo sus iniciativas) |
| `GET, POST /api/v1/initiatives/{id}/roles`; `PATCH, DELETE /api/v1/roles/{id}` | Roles (`PATCH`: solo `manages_access`) |
| `GET, POST /api/v1/initiatives/{id}/apps`; `PATCH /api/v1/apps/{id}` | Apps con sus deployments (`PATCH`: `status`, `name`). Cada app lleva un booleano de solo lectura `portal_app` (en las respuestas de listado, creación, patch y health): verdadero para el `PORTAL_APP_ID` configurado dentro de la iniciativa `platform`, la regla de las protecciones contra bloqueo, para que la UI pueda ocultar deshabilitar / quitar el último deployment |
| `POST /api/v1/apps/{id}/deployments`; `DELETE /api/v1/deployments/{id}` | Hosts (se leen a través del listado de apps) |
| `GET, POST /api/v1/initiatives/{id}/grants`; `DELETE /api/v1/grants/{id}` | Grants (sujeto por email, id de grupo o id de robot, rol, `expires_at` opcional en el futuro) |
| `GET, POST /api/v1/groups`; `GET, DELETE /api/v1/groups/{id}`; `GET, POST /api/v1/groups/{id}/members`; `DELETE /api/v1/groups/{id}/members/{user_id}` | Grupos (miembros: solo administradores de plataforma). `GET /groups?q=` filtra por una subcadena de llave o nombre sin distinguir mayúsculas (máximo 64 caracteres, `422` en `q` si no; los comodines son literales) y conserva la paginación por keyset. `GET /groups/{id}` devuelve `id, key, name, created_at` con el mismo acceso que el listado (administradores y gestores; `404` para un id desconocido), nunca los miembros |
| `GET /api/v1/users?email=` (solo humanos); `PATCH /api/v1/users/{id}`; `DELETE /api/v1/users/{id}/sessions` | Usuarios (`status`) y revocación de sesiones |
| `GET, POST /api/v1/me/tokens`; `DELETE /api/v1/me/tokens/{id}` | Tokens personales propios ([Tokens](#tokens)). `POST` devuelve el secreto una sola vez y solo desde una sesión de navegador; los listados nunca devuelven `token_hash` |
| `GET, POST /api/v1/robots`; `GET, PATCH /api/v1/robots/{id}` | Robots (`PATCH`: `name`, `status`, `owner_user_id`). `GET /robots?status=active|disabled` filtra por estado (ausente o vacío = todos; `422` ante cualquier otro valor, campo `status`) y conserva la paginación por keyset. Los administradores de plataforma reciben `id, name, status, owner_user_id, owner_email, owner_name, created_at, last_used_at` (campos del propietario `null` sin propietario); los gestores pueden listar y obtener id, name y status; un id humano o desconocido es 404 |
| `GET, POST /api/v1/robots/{id}/tokens`; `DELETE /api/v1/tokens/{id}` | Tokens de robot (administradores de plataforma; `POST` solo desde una sesión de navegador); `DELETE` revoca cualquier token |
| `POST /api/v1/tokens/revoke`; `DELETE /api/v1/me/tokens/current` | Revocación por secreto: un administrador de plataforma envía `{token}` (un secreto filtrado, venido de cualquier reporte de scanner); la solicitud que lleva un token `admin` revoca ese mismo token (un token `read` recibe 403). Ambos responden 204 |
| `GET /api/v1/audit?initiative_id=&cursor=` | Registro de auditoría, el más reciente primero, paginación por keyset; cada evento lleva `token_id` (el token de API que hizo la solicitud, `null` para una sesión de navegador) |
| `GET /api/v1/apps/{id}/health` | Salud de integración de una app (administradores y gestores de su iniciativa) |
| `GET /api/v1/health/orphan-hosts` | Hosts rechazados como `unknown_host` que no tienen deployment (solo administradores de plataforma) |
| `/api/v1/areas*`; `GET /api/v1/users/{id}/tokens`, `GET /api/v1/users/{id}/robots` | Áreas, sus miembros y supervisores, y los listados que usan los supervisores de área: ver [API de áreas](#api-de-áreas) |

## Quién administra qué

| Acción | Administrador de plataforma | Gestor de iniciativa (tiene un rol con `manages_access`) |
|---|---|---|
| Crear, renombrar, desactivar iniciativas | Sí | No (lee las propias) |
| Apps (crear, renombrar, deshabilitar) y deployments (agregar o quitar hosts) | Sí | No (lista las apps de sus iniciativas, con sus deployments) |
| Roles de la iniciativa (crear, eliminar, `manages_access`) | Sí | No: las llaves de rol son parte del contrato de la app (claim `roles`). Listan los roles de sus iniciativas, necesarios para asignar |
| Grants en la iniciativa (usuario, robot o grupo a rol, `expires_at`), incluidos los roles `manages_access` | Sí | Sí, solo en sus iniciativas |
| Revocar grants en la iniciativa | Sí, salvo el último grant `manages_access` efectivo ([último gestor efectivo](#reglas-de-administración)) | Misma excepción |
| Áreas, sus miembros y supervisores | Sí | No |
| Grupos y miembros | Sí | Solo listar grupos (id, key, name; nunca miembros), para dar acceso a un grupo. Sin cambios: los grupos son globales, un cambio afecta a todas las iniciativas |
| Deshabilitar usuarios, revocar sus sesiones y tokens | Sí (nunca a sí mismos, nunca al último administrador de plataforma activo) | No |
| Robots (crear, renombrar, deshabilitar, propietario) y sus tokens | Sí (creación de tokens solo desde una sesión de navegador) | No (listan id, name y status, para asignar) |
| Tokens personales propios (crear, listar, revocar) | Sí (cualquier usuario activo; alcance `admin` solo con permisos de administración en la emisión) | Igual |
| Revocar cualquier token | Sí | No |
| `platform_admins` | No: solo CLI o SQL (bootstrap) | No |
| Registro de auditoría | Todo | Solo eventos de sus iniciativas |
| Salud de integración de una app | Sí | Sí, para las apps de sus iniciativas |
| Hosts huérfanos (hosts sin deployment) | Sí | No (no pertenecen a ninguna iniciativa) |

Los supervisores de área son una tercera capacidad, fuera de esta tabla: solo desde una sesión de navegador,
administran los tokens y robots de las personas de sus áreas, nunca el acceso ([Supervisores de área](#supervisores-de-área)).
Supervisar no es un permiso de administración en la emisión de tokens.

## Reglas de administración

Reglas de administración, aplicadas dentro de la transacción de la mutación:

- **Último gestor efectivo.** Un grant `manages_access` es *efectivo* cuando no ha expirado y su sujeto tiene al
   menos un usuario **humano** activo: el usuario del propio grant es un humano activo, o el grupo tiene al menos un
   miembro activo (los grupos solo contienen humanos). Un grant de robot nunca cuenta, de modo que cada iniciativa
   conserva un gestor humano. Un usuario deshabilitado no es gestor (el portal lo rechaza), así que los grants que
   solo tienen usuarios deshabilitados no mantienen una iniciativa gestionada. Revocar un grant `manages_access`
   efectivo falla con 409 si no queda otro efectivo en la iniciativa; revocar un grant que no es efectivo siempre está
   permitido. Aplica también a los administradores de plataforma: una iniciativa se da de baja desactivándola, nunca
   quitando a sus gestores (un administrador que necesita reemplazar a los gestores asigna primero y revoca después).
   Se aplica solo en la revocación: quitar a un miembro de un grupo o deshabilitar a un usuario aún puede dejar una
   iniciativa sin gestor efectivo, y un administrador de plataforma lo repara.
- **Protecciones contra bloqueo.** Deshabilitarse a uno mismo falla con 409. Deshabilitar a un usuario nunca deja cero
   administradores de plataforma activos (409; dos administradores que se deshabilitan mutuamente al mismo tiempo no
   pueden tener éxito ambos). El mismo tipo de protección resguarda la entrada al portal: la iniciativa `platform` no
   se puede desactivar (409; su llave es inmutable como toda llave), la app del portal (`PORTAL_APP_ID` dentro de la
   iniciativa `platform`, la regla del evaluador de ForwardAuth, de modo que un id mal escrito que apunte a otra parte
   no protege nada) no se puede deshabilitar (409) y su último deployment no se puede quitar (409). Quitar un
   deployment bloquea la fila de la app (`FOR NO KEY UPDATE`) antes de contar, así que dos remociones de los dos
   últimos deployments no pueden tener éxito ambas.
- **Llaves inmutables.** La llave de una iniciativa, la llave de un rol (es parte del claim `roles`), el id de una app
   y la iniciativa de una app nunca cambian tras la creación: un patch que los nombre se rechaza (400, campo
   desconocido). Las llaves siguen el esquema (`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`; como máximo 63 caracteres para
   iniciativas y 32 para roles); el id de una app lo genera el servidor (`app_` más un ULID).
- **Rol `manages_access`.** Solo los administradores de plataforma lo activan o desactivan. Desactivarlo quita los grants del rol a los
   gestores, así que sigue la regla del último gestor exactamente como una revocación: 409 cuando el rol tiene un grant
   de gestor efectivo y no quedaría efectivo ningún grant de otro rol; un rol sin grant efectivo siempre puede
   desactivarse. Un rol se elimina solo sin grants (409).
- **Entradas.** Los nombres vacíos son válidos (el valor por defecto del esquema es `''`; la UI muestra la llave). Para
   las iniciativas, la validación del cuerpo precede a la verificación de existencia, así que un `PATCH` inválido sobre
   una iniciativa inexistente responde 422; los roles y las apps se cargan primero para autorizar la llamada, así que un
   rol o app inexistente responde 404 antes de validar su cuerpo.
- **Hosts.** El host de un deployment es un nombre DNS en minúsculas: como máximo 253 bytes, etiquetas de 1 a 63
   caracteres de `[a-z0-9-]` que no empiezan ni terminan con guion, sin punto final, sin comodín, sin puerto, y una
   última etiqueta que no sea solo dígitos (sin direcciones IP); los nombres internacionalizados llegan en su forma
   `xn--`, cuyas etiquetas no se validan como punycode (la búsqueda es una coincidencia exacta sobre
   `X-Forwarded-Host`, así que uno mal formado es inofensivo). El host de autenticación (el host de
   `PORTAL_ISSUER`) se rechaza (422, `reserved`), y un host que ya existe en cualquier app, incluidos los propios del
   portal, es un 409 (el índice único es la protección).
   El esquema aplica la misma regla (`deployments_host_valid`, migración 0011), así que SQL no puede almacenar un host
   que ForwardAuth rechazaría. Los adoptantes con filas antiguas deben corregirlas primero: la migración 0011 falla ante
   cualquier fila que viole la regla (la consulta que las encuentra está en el comentario de la migración).
- **Orden de bloqueos.** La revocación de un grant bloquea la fila de la iniciativa (`FOR NO KEY UPDATE`) y luego los
   grants `manages_access` de la iniciativa ordenados por id; lo mismo hace un cambio del `manages_access` de un rol, y un
   `PATCH` de iniciativa toma primero la fila de la iniciativa. Cualquier ruta que actualice o elimine una fila de rol
   bloquea primero la iniciativa, la eliminación de rol incluida: su verificación de llave foránea toma bloqueos
   `FOR KEY SHARE` sobre los grants de la iniciativa, que se cruzarían con el `FOR UPDATE` de un cambio concurrente de
   `manages_access` y provocarían un deadlock. El rol se lee de nuevo tras el bloqueo (desaparecido: 404), y todo
   `UPDATE` de catálogo que no coincide con ninguna fila responde 404, nunca un éxito silencioso. La fila de la app
   (`FOR NO KEY UPDATE`: `PATCH` de app, remoción de deployment) es una hoja: no se bloquea nada mientras se la
   mantiene. La creación de un grant toma primero la fila de la iniciativa `FOR SHARE` y vuelve a leer los permisos del
   llamador, así que un gestor revocado hace un momento no puede terminar un POST en vuelo. Deshabilitar a un usuario
   bloquea a los administradores de plataforma activos ordenados por id (`FOR NO KEY UPDATE`) y luego al objetivo.
   Ninguna ruta toma bloqueos explícitos de ambas familias. Los inserts que referencian `users` (grants, miembros de
   grupo) toman bloqueos `KEY SHARE` implícitos a través de sus llaves foráneas, que nunca entran en conflicto con
   `FOR NO KEY UPDATE`: esa compatibilidad es lo que descarta un ciclo, así que los bloqueos sobre `users` nunca deben
   elevarse a `FOR UPDATE`.
- **Aceptado por diseño.** Los `before`/`after` de la auditoría conservan emails (el registro de seguridad debe seguir
   siendo legible después de que un usuario cambie), y asignar por email permite a un gestor saber que un email es un
   usuario activo único (inherente a nombrar personas por email).

Un gestor puede nombrar a otro gestor: ya controlan todos los grants de la iniciativa, así que no agrega privilegio.
Los roles, grupos y hosts son las vías de escalamiento, y quedan en manos de los administradores de plataforma.

## Autorización de las llamadas del portal

1. La identidad viene solo de la aserción verificada (`sub`). Nada sobre permisos viaja en la aserción:
   estaría desactualizado durante toda su vida útil.
2. En cada solicitud el portal carga los permisos del llamador desde la base de datos: administrador de plataforma o no, y las
   iniciativas donde existe un grant no expirado con `manages_access` (directamente o a través de un grupo). También las áreas que supervisan, vacías bajo un token ([Supervisores de área](#supervisores-de-área), solo sesión de navegador;
   `portal.subject()` y `Service.Me` aplican la compuerta de tokens).
3. Una función pura `adminz.Decide(subject, action, resource)` (con forma AuthZEN, como `access.Decide`) responde cada
   llamada, con pruebas de tabla. Las acciones se nombran `<resource>.<verb>`: `grant.create`, `app.disable`, `audit.list`.
4. La verificación de permisos y la mutación corren en la misma transacción. La regla del "último gestor" toma un bloqueo de fila sobre
   los grants `manages_access` de la iniciativa dentro de esa transacción, así que dos gestores que se revocan
   mutuamente al mismo tiempo no pueden dejar la iniciativa sin uno.
5. 403 no lleva detalle sobre otros recursos. Un recurso de una iniciativa que el llamador no gestiona responde 404,
   para no confirmar su existencia.

## Protección de las solicitudes

Toda app vive bajo el mismo dominio registrable, así que `hello.apps.<domain>` y `portal.apps.<domain>` son same-site:
las cookies `SameSite=Lax` viajan entre ellos. La defensa es de mismo origen, mediante Fetch Metadata. No se usan
tokens CSRF; esto sigue el diseño de `http.CrossOriginProtection` de Go y se aparta deliberadamente de la hoja de
referencia de OWASP, que todavía sugiere tokens como capa adicional.

Orden de los middleware:

1. Recuperación (recover) y registro de solicitudes.
2. Aislamiento de lectura para `/api/*`: rechazar cualquier valor de `Sec-Fetch-Site` distinto de `same-origin` y `none` (`same-site`,
   `cross-site` y valores desconocidos) antes de autenticar, y enviar `Vary: Sec-Fetch-Dest, Sec-Fetch-Mode,
   Sec-Fetch-Site`. Un header ausente se permite (cliente que no es navegador, misma decisión que Go).
3. `http.CrossOriginProtection` para métodos no seguros. Verificado en Go 1.27.2: `same-origin` y `none` pasan, cualquier otro
   `Sec-Fetch-Site` se rechaza; sin él, `Origin` debe coincidir con `Host` (no se compara el esquema, así que HSTS importa);
   sin ambos, la solicitud pasa.
4. Las mutaciones aceptan solo `Content-Type: application/json` (analizado con `mime.ParseMediaType`, parámetros permitidos),
   de lo contrario 415. Los handlers nunca llaman a `ParseForm` ni a `FormValue`.

Headers de respuesta en toda respuesta del portal. La Content-Security-Policy de abajo es la de la UI web; las respuestas
de la API (`/api/*`, documentos problem) llevan en su lugar `default-src 'none'; frame-ancestors 'none'`, y el handler de
la UI es el único lugar que la reemplaza por la política completa:

- `Content-Security-Policy: default-src 'none'; script-src 'self' 'sha256-<inline>'; style-src 'self';
  img-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self' https://auth.apps.<domain>;
  frame-ancestors 'none'; base-uri 'none'`. Al iniciar, el servidor calcula el SHA-256 de cada script inline del HTML
  embebido (solo queda `window.__NUXT__`, y cambia con cada build). Calcular el hash de lo que haya es seguro:
  el HTML es un artefacto de build dentro del binario y, sin SSR, ningún dato de usuario llega nunca a él. `form-action`
  incluye el host de autenticación porque Chrome lo aplica al 303 que sigue al POST de logout.
- `Strict-Transport-Security` con `includeSubDomains`, `X-Frame-Options: DENY`, `Cross-Origin-Opener-Policy: same-origin`,
  `Cross-Origin-Resource-Policy: same-origin`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`.
- Nunca ningún header `Access-Control-*`.

Reglas:

- Ningún `GET`/`HEAD`/`OPTIONS` cambia estado.
- Un futuro WebSocket valida `Origin` explícitamente: su handshake es un GET, que `CrossOriginProtection` omite.
- El portal no lee ninguna cookie (Traefik convierte la cookie de la app en la aserción). Cualquier cookie futura debe usar
  el prefijo `__Host-`: los sitios hermanos pueden plantar cookies en el dominio padre.
- `r.Host` nunca se reescribe desde `X-Forwarded-Host`; Traefik mantiene `passHostHeader: true` para la ruta del portal.

Pruebas requeridas: POST con `Sec-Fetch-Site: same-site` o `cross-site` -> 403; POST con un `Origin` hermano y sin
`Sec-Fetch-Site` -> 403; `text/plain` -> 415; `application/json; charset=utf-8` -> aceptado; GET `/api/*` con
`same-site` -> 403; ninguna respuesta lleva `Access-Control-*`; el hash de la CSP coincide con el HTML embebido.

## Auditoría

1. Toda mutación escribe una fila de `audit_events` en la misma transacción: si el insert falla, la mutación se revierte.
   (La auditoría de login y logout sigue siendo de mejor esfuerzo: un login no debe fallar por la auditoría.)
2. Cada fila lleva el usuario que actúa, `action` (los mismos nombres que `adminz`), el objetivo, `before`/`after` e `initiative_id`
   cuando el objetivo pertenece a una (NULL para grupos, usuarios y otros objetivos globales).
3. Las eliminaciones conservan el `before` completo: revocar un grant elimina la fila, quitar un deployment elimina el host.
4. Revocar sesiones y deshabilitar usuarios desde el portal también se auditan, y también `RevokeUser`/`RevokeApp`
   de platform-api.
5. Las mutaciones hechas con token registran además el token (`token_id`), ver [Tokens](#auditoría-de-acciones-de-tokens-y-robots).
   Las acciones de supervisor registran además el área (`area_id`), ver [Supervisores de área](#auditoría-de-acciones-de-áreas).

### Auditoría de acciones de tokens y robots

Cada mutación hecha con un token registra al usuario o robot que actúa en `actor_id` y al token en la columna
`audit_events.token_id uuid NULL` (sin FK, como el resto de la tabla), con un índice como el de `initiative_id` si
filtrar lo necesita. Una columna, no jsonb: es filtrable, y `actor_id` por sí solo no puede decir cuál de los tokens del
propietario actuó.
Acciones: `token.create` (`after`: name, scope, `expires_at`, propietario; nunca el hash), `token.revoke`, `robot.create`,
`robot.update` (name, status, propietario). Deshabilitar a un usuario o robot registra los ids de los tokens revocados en la
fila `user.update` / `robot.update` (ids, no una cantidad: cada uno se puede rastrear). Las lecturas y los usos simples no se auditan
(`last_used_at` es el rastro). Los listados de auditoría, los grants y los documentos de usuario muestran a un robot por su nombre (el email de un robot es null).

### Auditoría de acciones de áreas

Las seis acciones de administración de áreas de abajo escriben `area_id` NULL. Acciones `area.create`, `area.delete` (objetivo `area`, `before`/`after`: key y name), `area_member.add`,
`area_member.remove`, `area_supervisor.add`, `area_supervisor.remove` (objetivo `area`, documento: id del área, id del usuario y
email, como los miembros de grupo). Las acciones de supervisor conservan sus nombres (`token.revoke`, `robot.update`) y fijan
`audit_events.area_id`: la capacidad, para que un lector distinga "como supervisor del área X" de "como propietario" o "como administrador
de plataforma". Se fija solo cuando el permiso vino de la capacidad de supervisor (razón de decisión `area_supervisor`),
nunca para un administrador de plataforma, que pasa `scopeArea` antes de que se miren las áreas supervisadas. `initiative_id` queda NULL en todas ellas (objetivos globales); los gestores nunca ven estos eventos. Los supervisores no tienen
vista de auditoría en este diseño.

## Home

`GET /api/v1/me/apps` es una sola consulta SQL que aplica exactamente las reglas de `access.Decide`: usuario activo, app activa,
iniciativa activa, grant no expirado (directo o a través de un grupo). Invariante, verificado por una prueba sobre los datos semilla:
un deployment aparece en el home si y solo si `/forwardauth` respondería 200 para ese usuario. Internamente tiene la
forma de una búsqueda de recursos AuthZEN (sujeto + acción -> recursos); no se expone ningún endpoint público AuthZEN hasta que un
consumidor externo lo necesite.

La única excepción a la invariante es la propia app del portal: `/forwardauth` deja entrar a todo usuario activo, pero el home
nunca la lista (es donde el usuario ya está, no una de "sus" apps). La prueba verifica la excepción explícitamente.

## Tokens

Los tokens de acceso personales (humanos) y los tokens de robot son un solo mecanismo, para CLI y automatización que llaman a esta API. Tabla y formato del secreto: [modelo de datos](data-model.md#tokens-de-api). Cómo se autentican: [ruta de tokens de ForwardAuth](forwardauth.md#ruta-de-token). Fundamento: [ADR 0008](../decisions/0008-opaque-api-tokens-and-robots.md).

### Reglas de tokens

1. El secreto se muestra una sola vez, en la respuesta de creación; nunca es recuperable y ningún endpoint devuelve `token_hash`.
2. La expiración es obligatoria, con un tope por tipo de propietario: `PORTAL_TOKEN_MAX_TTL_PERSONAL` (por defecto `720h`, 30 días) y
   `PORTAL_TOKEN_MAX_TTL_ROBOT` (por defecto `2160h`, 90 días). Un token humano no debe sobrevivir mucho a una suspensión de Google:
   el control de baja (offboarding) es la bandera local `disabled` más el tope de sesión de 8 h ([login](login.md#limitaciones)), y un usuario suspendido pero
   no deshabilitado conserva un token funcional hasta el tope. Los robots no se dan de baja a través de Google, así que pueden
   permitirse un tope más largo. La rotación sigue siendo rutina (crear el nuevo token, cambiar, revocar el viejo; varios tokens por
   propietario se superponen, como máximo 25 sin expirar ni revocar). Subir un tope es decisión del adoptante. Una solicitud sin
   `expires_in` recibe el tope del tipo de propietario; una mayor es un 422.
3. El alcance (scope) es un techo, nunca una concesión. `read` permite solo `GET` y `HEAD`; `admin` levanta la restricción de método. En ambos
   casos el permiso efectivo es la intersección de los permisos vivos del propietario (resueltos desde la base de datos en
   cada solicitud, como en [Autorización de las llamadas del portal](#autorización-de-las-llamadas-del-portal)) y el alcance.
4. Los permisos se verifican en la emisión y en cada uso. En la emisión, `admin` se acepta solo si el propietario del token es
   en ese momento un administrador de plataforma o tiene `manages_access` en al menos una iniciativa; de lo contrario 422. Un token nunca
   lleva más de lo que su propietario puede hacer en la emisión, y lo que puede hacer después se reduce con el propietario (grant revocado,
   usuario o robot deshabilitado).
5. Deshabilitar a un usuario o robot revoca todos sus tokens (`revoked_at`) en la misma transacción, igual que sus sesiones;
   rehabilitarlo no los trae de vuelta. ForwardAuth además verifica `status` en cada solicitud. La revocación vive en la
   ruta de la API (`SetUserStatus` retorna antes cuando el estado ya coincide): un usuario deshabilitado por SQL manual, sin
   revocación, y luego rehabilitado a través de la API recupera sus tokens aún válidos. La forma soportada de deshabilitar es
   la API; tras una deshabilitación manual, revocar los tokens a mano.
6. La tarea de mantenimiento (el job existente en platform-api) elimina los tokens 30 días después de `expires_at` o `revoked_at`, para que los propietarios
   sigan viendo los tokens muertos recientemente. Llama a `purge_dead_api_tokens(grace interval)`, una función SECURITY DEFINER con un
   `search_path` fijado que solo elimina filas pasado el período de gracia y rechaza una gracia menor a 7 días (mismo patrón
   que `drop_expired_access_denials_partitions`, migración 0008); el rol runtime no tiene DELETE sobre la tabla. La auditoría conserva name, scope y expiración en `before`/`after`.

### Emisión y revocación

1. Los tokens se crean solo desde una sesión de navegador: una solicitud autenticada por un token (aserción con `tid`) nunca puede
   crear un token ni un token de robot, de modo que un token filtrado no puede extenderse a sí mismo. Lo verifica el portal, no el cliente.
2. Un humano crea tokens solo para sí mismo (`POST /api/v1/me/tokens`). Los tokens de robot los crean solo los administradores de
   plataforma, desde una sesión (`POST /api/v1/robots/{id}/tokens`).
3. El propietario revoca sus propios tokens; un administrador de plataforma revoca cualquier token (y un supervisor de
   área los tokens de su alcance, solo desde una sesión de navegador, [Supervisores de área](#supervisores-de-área)). Un token puede revocar tokens (propios, o cualquiera si
   su propietario es un administrador de plataforma y el alcance es `admin`): la contención no debe requerir un navegador.
   Revocación por secreto, para contener una filtración sin conocer el id del token:
   - `POST /api/v1/tokens/revoke` con `{token}`, solo administradores de plataforma (sesión o token `admin`): el portal verifica el
     formato, calcula el hash del secreto y revoca el token vivo que coincide. Responde 204 coincida o no algún token,
     así el endpoint nunca confirma que un secreto existe; una revocación es visible en la auditoría y en la lista de tokens.
     Funciona con cualquier scanner de secretos (GitHub, GitLab o uno interno): un administrador pega lo que contiene el reporte.
   - `DELETE /api/v1/me/tokens/current` revoca el token que autentica la solicitud (espíritu de RFC 7009). Es un
     `DELETE`, así que necesita un token `admin` como cualquier otra mutación: ninguna ruta está exenta de la restricción de `read`
     (ForwardAuth nunca trata rutas de forma especial). Un token `read` filtrado lo contiene su propietario (revocar por id) o un
     administrador de plataforma (revocar por secreto). Una solicitud sin token (sesión de navegador) responde 404.
   - Sin endpoint público para programas de socios de scanners: son específicos de cada proveedor y requerirían una ruta fuera de
     ForwardAuth. Un adoptante que necesite uno lo agrega más adelante.
4. La creación corre en una transacción que toma `LockUser(owner)` (`FOR NO KEY UPDATE`, el bloqueo que toma `SetUserStatus`)
   como su primer bloqueo, vuelve a leer el `status` del propietario bajo él, cuenta los tokens sin expirar ni revocar contra el
   límite de 25 y luego inserta. El bloqueo es necesario porque la llave foránea del insert toma solo `KEY SHARE` sobre
   el usuario, que no entra en conflicto con una deshabilitación concurrente: sin él, un token podría insertarse después de que la
   deshabilitación revocara los demás, y dos creaciones paralelas podrían pasar el conteo juntas. Es un bloqueo hoja (ningún otro
   bloqueo de usuario se mantiene con él), así que no puede unirse a los órdenes de bloqueo de [Reglas de administración](#reglas-de-administración). `LockUser` solo ve humanos, así que los tokens de
   robot toman el mismo bloqueo (`FOR NO KEY UPDATE`) a través del repositorio de robots, que solo ve robots.
5. Toda creación y revocación se audita ([Auditoría](#auditoría-de-acciones-de-tokens-y-robots)). La creación no tiene llave natural, así que un reintento tras un fallo ambiguo
   puede dejar un segundo token; el cliente lista y revoca el sobrante.

### Configuración

Los topes de vida de los tokens personales y de robot (`PORTAL_TOKEN_MAX_TTL_PERSONAL`, por defecto `720h`, y `PORTAL_TOKEN_MAX_TTL_ROBOT`, por defecto `2160h`; el de robot aplica al endpoint de tokens de robot) están en [configuration.md](configuration.md#portal).

### API de tokens personales

`POST /api/v1/me/tokens` recibe `{name, scope, expires_in}` (`expires_in` en segundos,
opcional) y responde 201 con el token y su `secret`; el límite de 25 tokens vivos responde 409; un alcance `admin`
que el propietario no puede tener en ese momento, un `expires_in` por encima del tope o no positivo, y un nombre o alcance inválido responden 422;
una solicitud autenticada por un token responde 403. Los listados van del más reciente al más antiguo con el `cursor` de keyset e incluyen los tokens
muertos recientemente (`last_used_at`, `revoked_at`). Revocar un token que ya está revocado responde 204 y no escribe fila de
auditoría; un token que no es del llamador responde 404 (`DELETE /api/v1/tokens/{id}` también, salvo que el llamador sea un administrador de
plataforma, o un supervisor de área solo desde una sesión de navegador cuando el token está en su alcance).
Los nombres de acción de auditoría son `token.create` y `token.revoke`; `adminz` usa `token.create_own`, `token.list_own`,
`token.revoke_own`, `token.revoke_supervised` (`DELETE /tokens/{id}` de otro propietario; los administradores de plataforma lo pasan) y `token.revoke_any` (solo revocación por secreto). `POST /api/v1/tokens/revoke`
responde 422 para un `token` vacío o mal formado (la verificación de formato no revela nada sobre la existencia, y corre después de la
verificación de autorización y antes de la búsqueda por hash), 403 para un llamador que no es administrador de plataforma, y 204 para todo secreto bien formado, coincida o no;
solo una coincidencia escribe una fila de auditoría (`token.revoke`, el id del token y su propietario, nunca el secreto).

### Protección de las solicitudes con tokens

Una solicitud Bearer no lleva credencial ambiente: el navegador nunca agrega `Authorization` por sí mismo, y para agregarlo desde una
página de otro origen necesita un preflight de CORS, que el portal nunca aprueba (sin `Access-Control-*`). Fetch Metadata
queda como está: los clientes que no son navegador no envían `Sec-Fetch-Site`, y un header ausente se permite ([Protección de las solicitudes](#protección-de-las-solicitudes), orden de middleware
2 y 3); un navegador siempre lo envía, así que una solicitud cross-site originada en un navegador se rechaza lleve lo que lleve.
Como la ruta de tokens ignora las cookies, una solicitud no puede mezclar la cookie de una víctima con el token de un atacante. Las mutaciones mantienen
la regla de solo JSON (415). El portal además aplica el alcance desde el claim `scope` en un middleware posterior a la autenticación y anterior a las
rutas: si `tid` está presente, `scope` debe ser `read` o `admin` (cualquier otra cosa: 403, falla cerrada), y `read` significa exactamente
`GET` y `HEAD`. No reutiliza `isSafeMethod` (`guard.go` de portalhttp), que también acepta `OPTIONS`. Defensa en profundidad
por si ForwardAuth y el portal alguna vez discrepan.

## Robots

Reglas del esquema de robots (kind, pins, grants): [modelo de datos](data-model.md#humanos-y-robots).

`GET /api/v1/robots` lista por id con el `cursor` de keyset: los administradores de plataforma reciben `id,
name, status, owner_user_id, owner_email, owner_name, created_at, last_used_at` (el uso más reciente de cualquiera de sus tokens), los gestores reciben solo `id, name,
status`, cualquier otro 403. `POST /api/v1/robots` recibe `{name, owner_user_id?}` y responde 201; un nombre duplicado
(sin distinguir mayúsculas, entre robots) es 409, un nombre inválido o un propietario que no es un humano activo es 422. `PATCH
/api/v1/robots/{id}` recibe cualquiera de `name`, `status` (`active`/`disabled`) y `owner_user_id` (`""` lo borra; JSON `null`
se lee como ausente); un patch vacío es 422, un patch que no cambia nada no escribe fila de auditoría, deshabilitar revoca los tokens vivos
(ids en el `after` de la fila `robot.update`) y un id humano es 404. `POST /api/v1/robots/{id}/tokens` recibe el cuerpo de
el token personal (`expires_in` limitado por `PORTAL_TOKEN_MAX_TTL_ROBOT`) y responde 201 con el secreto, solo desde una sesión de
navegador (403 de lo contrario) y solo para administradores de plataforma (403); un id desconocido o humano es 404, un robot deshabilitado 409, los 25
tokens vivos 409, y el alcance `admin` para un robot sin un grant `manages_access` vigente 422 (`not_allowed`).
`GET /api/v1/robots/{id}/tokens` los lista (administradores, y supervisores de área solo desde una sesión de
navegador para robots de su alcance: `robot_token.list` es de alcance de área, cualquier otro recibe 404). El sujeto del grant admite `robot_id` (exactamente uno de `email`,
`group_id`, `robot_id`, de lo contrario 422 sobre los tres campos); un robot desconocido, humano o deshabilitado es 422 `unknown` sobre
`robot_id`. Acciones de `adminz`: `robot.list`, `robot.create`, `robot.update`, `robot_token.create`, `robot_token.list`.
La pertenencia a grupos es por email, que un robot no tiene, así que los endpoints de miembros no pueden recibir un id de robot.
`DELETE /api/v1/tokens/{id}`, la revocación por secreto y `DELETE /api/v1/me/tokens/current` (el propio token del robot) sirven
los tokens de propiedad de un robot sin cambios.

La salida de un propietario nunca deshabilita automáticamente a sus robots.

## Supervisores de área

Un supervisor de área administra los robots y tokens de API de las personas de un área, de modo que la baja (offboarding) de la automatización de una persona no espere a un administrador de plataforma. Los supervisores administran las credenciales de las personas y nunca el acceso; son una capacidad aparte, no el gestor de iniciativa. Tablas: [modelo de datos](data-model.md#áreas). Fundamento: [ADR 0010](../decisions/0010-local-areas-with-admin-assigned-supervisors.md).

Los cambios de área no afectan las decisiones de acceso: sin `NOTIFY`, y platform-api nunca lee estas tablas.

### Alcance

1. Una persona es **supervisable** cuando es miembro humano de un área, no es administrador de plataforma y no supervisa
   ningún área. Los miembros deshabilitados siguen siendo supervisables: dar de baja los robots de una persona que se fue es el uso principal. "Administrador de plataforma"
   y "supervisa un área" se leen de las filas de `platform_admins` y `area_supervisors` sea cual sea el estado de la persona
   (falla cerrada): un supervisor o administrador deshabilitado sigue sin ser supervisable.
2. Un supervisor alcanza a una persona supervisable si y solo si el supervisor está activo y supervisa el área de esa persona.
   A través de esa persona alcanza:
   - los tokens personales de la persona;
   - los robots cuyo `owner_user_id` es esa persona, y los tokens de esos robots.

   Un robot sin propietario, o de alguien que no es supervisable, lo alcanzan solo los administradores de plataforma.
3. Sobre lo que alcanza, un supervisor puede: listar metadatos (nunca secretos ni hashes, que el portal de todos modos no puede leer),
   revocar tokens y cambiar el `owner_user_id` de un robot. El nuevo propietario debe ser una persona supervisable activa de un área
   que el actor supervisa (cualquiera de ellas, no necesariamente la misma área); borrar el propietario no está permitido (422
   `not_allowed`: sacaría al robot del alcance de todo supervisor).
4. Nunca: grants, roles, grupos, acceso a iniciativas o apps (ni siquiera listar los grants de un robot), deshabilitar a un usuario o
   robot, renombrar un robot, revocar sesiones, crear tokens para otra persona, actuar como la persona.
5. Las protecciones se derivan de la regla 1. Como un supervisor nunca es supervisable, nadie alcanza sus propios tokens o robots
   por esta vía, los supervisores no pueden administrarse entre sí (sin cadena: supervisar un área no da nada sobre
   otro supervisor ni sobre las áreas que supervisan) y los administradores de plataforma quedan fuera de todo alcance. El predicado
   depende solo del objetivo, no del actor, así que el repositorio resuelve "el área supervisable de esta persona"
   y `adminz.Decide` la compara con las áreas supervisadas del llamador.
6. El alcance se evalúa en cada solicitud dentro de la transacción del caso de uso, desde la base de datos, nunca en caché.
7. Fuera de alcance responde 404 ([Autorización de las llamadas del portal](#autorización-de-las-llamadas-del-portal), regla 5): un supervisor no se entera de nada sobre personas,
   tokens o robots fuera de sus áreas.
8. La capacidad de supervisor es solo de sesión de navegador: bajo un token (`tid`), `Supervised` está vacío, así que toda
   acción con alcance de área responde como para cualquier otro llamador (404). Lecturas incluidas: un token `read` filtrado de un supervisor
   no debe listar los tokens o robots de otras personas. Supervisar no es un permiso administrativo en la emisión de tokens
   (la regla 4 de [Reglas de tokens](#reglas-de-tokens) no cambia), así que un supervisor no obtiene de ello ningún token `admin`.

`owner_user_id` sigue sin ser autoridad de acceso (ForwardAuth y los grants lo ignoran), pero ubica a un robot en el
área de su propietario: fijarlo decide qué supervisores administran el robot.

### Autorización (`adminz`)

- `Subject` gana `Supervised` (ids de áreas), cargado por la misma consulta de permisos que `Managed`; `Resource` gana
  `AreaID`, el área supervisable de la persona objetivo (vacío si no hay), resuelta desde el almacenamiento por el caso de uso.
- La regla 8 de [Alcance](#alcance) se implementa en dos lugares: `portal.subject(ctx, tx, id)` construye el sujeto sin
  `Supervised` cuando `TokenFrom(ctx) != ""` (todo caso de uso obtiene su sujeto ahí, así que ningún handler puede
  olvidarlo), y `Service.Me`, que no usa `subject()`, vacía `supervised_areas` bajo un token.
- Nuevo alcance de regla `scopeArea`: permitido a los administradores de plataforma sea cual sea `AreaID` (un administrador alcanza a toda persona,
  pertenezca o no a un área), razón `platform_admin`; a un supervisor si y solo si `AreaID` no está vacío y es uno
  de `Supervised`, razón `area_supervisor`; a cualquier otro `not_found`. La razón es lo que le dice al caso de uso que el
  permiso vino de la capacidad de supervisor ([auditoría de acciones de áreas](#auditoría-de-acciones-de-áreas)).
- Acciones:

| Acción | Alcance | Usada por |
|---|---|---|
| `area.create`, `area.delete`, `area.list`, `area_member.add`, `area_member.remove`, `area_supervisor.add`, `area_supervisor.remove`, `area_supervisor.list` | global (administradores) | Administración de áreas (`GET /areas/{id}` usa `area.list`) |
| `area_member.list` | área (`AreaID` = el área misma) | `GET /areas/{id}/members` |
| `user_token.list`, `user_robot.list` | área | `GET /users/{id}/tokens`, `GET /users/{id}/robots` |
| `robot_token.list` | área (antes global) | `GET /robots/{id}/tokens` |
| `token.revoke_supervised` | área | `DELETE /tokens/{id}` sobre un token que no es del llamador |
| `robot.reassign` | área | `PATCH /robots/{id}` solo con `owner_user_id`, por un llamador que no es administrador de plataforma; `AreaID` = el área del propietario actual (el nuevo propietario es una verificación de campo del caso de uso) |

`token.revoke_any` sigue siendo global (administradores) y sigue autorizando la revocación por secreto: un supervisor nunca revoca por
secreto (les permitiría revocar tokens fuera de su alcance, y el endpoint no debe decir de quién era el token que coincidió).

### Casos de uso

- **Revocar** (`DELETE /api/v1/tokens/{id}`): token propio -> `token.revoke_own`; de lo contrario
  `token.revoke_supervised` con `AreaID` = área supervisable de la persona del token (el propietario de un token personal,
  el propietario del robot para un token de robot), que los administradores de plataforma pasan de todos modos. Sin bloqueo: la revocación es un
  único `UPDATE ... WHERE revoked_at IS NULL`. La fila de auditoría es `token.revoke`, con `area_id` fijado solo cuando la
  razón de la decisión es `area_supervisor` (nunca para un administrador de plataforma, ni siquiera uno que también supervise el área).
- **Reasignar** (`PATCH /api/v1/robots/{id}`): un administrador de plataforma conserva la ruta actual. Cualquier otro: el patch puede
  llevar solo `owner_user_id` (403 de lo contrario), el alcance se verifica una vez antes del bloqueo (un llamador sin alcance no bloquea
  nada) y de nuevo tras `LockRobot` desde la fila bloqueada, de modo que dos reasignaciones concurrentes (o un patch de administrador) del
  mismo robot corren una tras otra y la segunda se autoriza contra el propietario que escribió la primera.
  `adminz` ve solo el área del propietario actual; el nuevo propietario es una validación del caso de uso, en la misma
  transacción: activo, supervisable, y con un área en los `Supervised` del llamador; de lo contrario 422 `unknown` sobre
  `owner_user_id` (el código existente, que no dice qué condición falló). La fila de auditoría es `robot.update`
  (propietario `before`/`after`) con `area_id` = el área del propietario anterior (razón `area_supervisor`).
- **Listados**: `GET /users/{id}/tokens`, `GET /users/{id}/robots`, `GET /robots/{id}/tokens` y
  `GET /areas/{id}/members` responden los mismos documentos que sus equivalentes de administrador (tokens como en `/me/tokens`;
  robots con campos de propietario y `last_used_at`). La lista de miembros de un supervisor contiene solo miembros supervisables (exactamente
  las personas sobre las que puede actuar); los administradores de plataforma ven a todos los miembros.

Carreras, bajo READ COMMITTED. Las mutaciones de administrador sobre áreas (quitar un miembro, quitar un supervisor, convertir a alguien en
administrador de plataforma o en supervisor) no leen nada que escriba una acción de supervisor (tokens, `owner_user_id`), así que una acción de supervisor
que leyó su alcance justo antes de que se confirmara una remoción concurrente termina en el estado del orden serial
"acción, luego remoción": lo mismo que si el administrador hubiera quitado un segundo después. A diferencia de la creación de grants (que toma la
iniciativa `FOR SHARE` y vuelve a leer los permisos, así que un gestor revocado no puede terminar un POST en vuelo), no se bloquea ninguna
fila de área: una acción de supervisor nunca extiende el acceso de nadie (la revocación quita, `owner_user_id` no concede
nada), así que la ventana en vuelo es inofensiva y la auditoría la registra. Un nuevo propietario deshabilitado concurrentemente es el
mismo caso que la ruta de administrador (`checkRobotOwner` lee sin bloqueo): el robot sigue funcionando, como con cualquier propietario
deshabilitado. Las rutas de supervisor no toman bloqueos nuevos (`LockRobot` es el bloqueo hoja existente), así que no pueden unirse a los órdenes
de bloqueo de [Reglas de administración](#reglas-de-administración). Las mutaciones de área no toman bloqueo explícito sobre `users`; sus llaves foráneas toman `KEY SHARE`,
compatible con `FOR NO KEY UPDATE`. Eliminar un área no toma bloqueo (necesitaría `UPDATE` sobre `areas`, que el
rol del portal no tiene): un `DELETE` simple, protegido por `ON DELETE RESTRICT`. `RESTRICT` lanza 23001
(`restrict_violation`), no el 23503 de `NO ACTION` y de los inserts: un 23001 sobre `area_members_area_id_fkey` o
`area_supervisors_area_id_fkey` se mapea a 409 en el delete (`portal.ErrInUse`), y un 23503 sobre las mismas restricciones a
404 al agregar un miembro o supervisor a un área eliminada concurrentemente (las cuatro llaves foráneas de las dos tablas se unen
a `referenceFields`, repositorio del portal).

### `/me`

`GET /api/v1/me` gana `permissions.supervised_areas`: `[{id, key, name}]` ordenado por key, como `managed_initiatives`.
La UI muestra las pantallas de supervisor si y solo si no está vacío. Bajo un token siempre es `[]` (regla 8 de [Alcance](#alcance)).

### API de áreas

| Endpoint | Propósito |
|---|---|
| `GET, POST /api/v1/areas`; `GET, DELETE /api/v1/areas/{id}` | Áreas (administradores de plataforma). `POST` recibe `{key, name}`; una llave duplicada es 409. `DELETE` con miembros o supervisores es 409. Sin `PATCH`: la llave es inmutable y renombrar es una extensión posterior, como con los grupos |
| `GET, POST /api/v1/areas/{id}/members`; `DELETE /api/v1/areas/{id}/members/{user_id}` | Miembros. `POST {email}` resuelve exactamente un humano activo (422 de lo contrario); una persona que ya está en un área (esta u otra) es 409: mover a alguien es quitar y luego agregar; un área desconocida (eliminada concurrentemente incluida) es 404. `GET`: administradores de plataforma, todos los miembros; supervisores del área, solo miembros supervisables. Cada miembro: `user_id, email, name, status, added_at` |
| `GET, POST /api/v1/areas/{id}/supervisors`; `DELETE /api/v1/areas/{id}/supervisors/{user_id}` | Supervisores (administradores de plataforma). `POST {email}` como para los miembros; un duplicado es 409 |
| `GET /api/v1/users/{id}/tokens` | Los tokens de un humano, el más reciente primero, `cursor` de keyset, tokens muertos recientemente incluidos (administradores de plataforma: cualquier humano; supervisores: personas supervisables de sus áreas). Un id de robot es 404, como el resto de `/users/*` |
| `GET /api/v1/users/{id}/robots` | Robots de propiedad de un humano, por id, `cursor` de keyset, documento de administrador (mismo acceso que la línea anterior) |

Ampliados para supervisores, siempre desde una sesión de navegador y dentro de su alcance: `GET /api/v1/robots/{id}/tokens`,
`DELETE /api/v1/tokens/{id}`, `PATCH /api/v1/robots/{id}` (solo `owner_user_id`); y `GET /api/v1/me`
(`supervised_areas`, `[]` bajo un token), `GET /api/v1/audit` (cada evento lleva `area_id`). `GET /api/v1/robots` y
`GET /api/v1/robots/{id}` no cambian: un supervisor alcanza a los robots a través de sus personas.

Para llamadores que no son administradores de plataforma ni supervisores en alcance, `GET /robots/{id}/tokens`, `GET /areas/{id}/members`
y `PATCH /robots/{id}` solo con `owner_user_id` responden 404, porque el alcance de área oculta lo que rechaza. Un
`PATCH /robots/{id}` de un no administrador que lleva cualquier otro campo, o ninguno, es 403 antes de buscar nada.
`GET /areas/{id}/supervisors` sigue siendo solo para administradores (403).

Reutilizar estos endpoints mantiene una sola revocación y un solo patch de robot, de modo que las reglas de tokens y robots (bloqueo, patch sin efecto
sin auditoría, 204 en un token ya revocado) valen para los supervisores sin una segunda implementación. Los dos nuevos
listados `/users/{id}/*` cierran también una brecha para los administradores de plataforma: podían revocar cualquier token pero no tenían forma de listar los
tokens de un humano.



## Content-Security-Policy y la UI web

Las páginas y componentes de la UI web deben ajustarse a esta política:

- El portal envía `default-src 'none'; style-src 'self'` (sin `style-src-elem` / `style-src-attr` / `'unsafe-inline'`;
  `platform/api/internal/webui/csp.go`), así que tanto los elementos `<style>` como los atributos `style=` en el markup están bloqueados, mientras que
  las escrituras CSSOM (`el.style.x = ...`, `setProperty`) están permitidas.
- Reka UI (reka-ui 2.11.0, fijado) es compatible: la app web importa solo `DialogRoot`, `DialogPortal`, `DialogOverlay`,
  `DialogContent`, `DialogTitle`, `DialogDescription`, `DialogClose` (`UiDialog`) y `AlertDialogRoot`,
  `AlertDialogPortal`, `AlertDialogOverlay`, `AlertDialogContent`, `AlertDialogTitle`, `AlertDialogDescription`,
  `AlertDialogCancel` (`UiConfirmDialog`). Su código fuente e importaciones transitivas (Primitive, Teleport, Presence,
  DismissableLayer, FocusScope, `useBodyScrollLock`, `bodyPointerEvents`, `aria-hidden`) nunca crean un elemento `<style>`
  ni fijan un atributo `style`; solo usan CSSOM (`body.style.overflow` / `paddingRight` / `marginRight` /
  `pointerEvents`, `--scrollbar-width` sobre `<html>`, `animationFillMode` en Presence) y bindings `:style` de objeto de Vue
  (`pointer-events`), todos permitidos.
- Los únicos creadores de `<style>` en la biblioteca son Select, Combobox, ScrollArea y Splitter (no se usan; eslint
  `no-restricted-imports` los prohíbe). El bundle generado tiene un solo `createElement('style')`, la ruta
  `defineCustomElement` de Vue, que la app no usa.
- Regla a mantener: nunca enlazar `:style` a un string (Vue escribe `style.cssText`, que esta política bloquea); los bindings de objeto
  están bien.
- Cobertura: `platform-gitops/overlays/local/browser-check/check.mjs` escucha `securitypolicyviolation` en cada página
  y verifica 0 eventos; abre `UiDialog` (crear iniciativa) y `UiConfirmDialog` (desactivar, revocar grant, quitar
  host) y verifica el bloqueo de scroll de Reka. Un Dialog abierto desde las pantallas de grupos, usuarios, roles, robots y tokens (mismos
  componentes, misma ruta de código) no se ejercita ahí.
