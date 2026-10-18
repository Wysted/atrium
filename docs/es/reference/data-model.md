# Modelo de datos

> English version: [docs/reference/data-model.md](../../reference/data-model.md)

Esquema de PostgreSQL para autorización, tokens, robots, áreas y auditoría. Las tablas de sesión, login y llaves están en [login](login.md#tablas-de-sesión) y [keys](keys.md#almacenamiento). Por qué los robots y las áreas se modelan así: [ADR 0009](../decisions/0009-robots-are-users-pinned-by-kind.md), [ADR 0010](../decisions/0010-local-areas-with-admin-assigned-supervisors.md).

RBAC con dominios: un rol solo existe dentro de una iniciativa.

```
users --< group_members >-- groups
  |                           |
  +---------+       +---------+
            v       v
         initiative_access   (subject: user XOR group)
            |  composite FK (initiative_id, role_id)
            v
  initiative_roles >-- initiatives --< apps (aud) --< deployments (host)
```

## Tablas

| Tabla | Propósito y restricciones clave |
|---|---|
| `users` | Los humanos se crean just-in-time en el primer login; los robots, por los administradores de plataforma ([API](api.md#robots)). `id` uuid = `sub` del JWT; `kind` human/robot, `UNIQUE (id, kind)`; `google_sub` UNIQUE, NOT NULL si y solo si `kind = 'human'` (CHECK; los robots no tienen); `owner_user_id` (robots, informativo); `email`, `name` informativos; `status` active/disabled |
| `groups`, `group_members` | Grupos internos; PK `(group_id, user_id)`; los miembros son solo humanos (`kind` fijado por FK compuesta). La sincronización de grupos de Google Workspace es una extensión posterior |
| `areas`, `area_members`, `area_supervisors` | Migración 0014: áreas para los supervisores de las credenciales de las personas; `area_members` PK `(user_id)`, un área por persona; los miembros y supervisores son solo humanos (`kind` fijado por FK compuesta). Lo lee solo el portal ([modelo](#áreas)) |
| `initiatives` | `key` UNIQUE, `status` |
| `initiative_roles` | `UNIQUE (initiative_id, key)` y `UNIQUE (initiative_id, id)` (destino de la FK compuesta); bandera `manages_access` para la administración delegada |
| `apps` | `id` = `app_<ulid>` (el `aud`); pertenece a una iniciativa; `status` active/disabled |
| `deployments` | `host` UNIQUE, en minúsculas; asocia un host a una app (una app puede tener varios hosts); `last_seen_at` y resultado del último probe |
| `initiative_access` | Grant: iniciativa + rol + exactamente uno de usuario/grupo (CHECK), `expires_at` opcional, `granted_by`, `granted_at`; FK `(initiative_id, role_id)` -> `initiative_roles (initiative_id, id)` |
| `platform_admins` | Administradores de toda la plataforma, separados de los roles de iniciativa; solo humanos (`kind` fijado por FK compuesta) |
| `api_tokens` | Tokens de acceso personales y de robot: `user_id`, `token_hash` UNIQUE (SHA-256), `scope` read/admin, `expires_at` NOT NULL, `last_used_at`, `revoked_at` |
| `audit_events` | Cambios de administración (actor, action, target, before/after jsonb, `initiative_id` nullable para el alcance, `token_id` nullable para cambios hechos con token y `area_id` nullable para cambios hechos por supervisores, migración 0014). Solo anexar: el rol de la app tiene solo INSERT y SELECT |
| `access_denials` | Decisiones de denegación, particionadas por mes; retención de 365 días eliminando particiones |

## Reglas

1. Decisión: permitir si y solo si el usuario está activo, el host tiene un deployment, la app está activa, la iniciativa de la app está activa
   y existe un grant no expirado para el usuario directamente o a través de un grupo. `roles` = unión de las llaves de rol otorgadas
   para esa iniciativa. La única excepción es la app del portal ([ForwardAuth](forwardauth.md#tabla-de-decisión)): cualquier usuario activo, `roles` vacío.
2. Falla cerrada por construcción: sin deployment o sin grant significa denegar. Nada está abierto por defecto.
3. La FK compuesta hace imposible otorgar un rol de la iniciativa A sobre la iniciativa B.
4. Sin pre-aprovisionamiento por email: el acceso se vincula a `google_sub`, que Google nunca reasigna. Los usuarios inician sesión una vez
   (o reciben acceso a través de un grupo) antes de los grants directos.

## Convenciones

- PostgreSQL >= 18 (`uuidv7()` integrado); `timestamptz` en todas partes; hosts y llaves forzados a minúsculas con CHECK.
- Los cambios de acceso emiten `NOTIFY`; platform-api mantiene una caché de decisiones (TTL 30 s) invalidada por `LISTEN`.

## Cambios hechos para el portal

| Cambio | Por qué |
|---|---|
| `audit_events.initiative_id uuid NULL` (sin FK, como el resto de la tabla) + índice `(initiative_id, occurred_at)` | Los gestores filtran por columna en lugar de analizar jsonb |
| `apps.status` active/disabled; nueva razón de decisión `app_disabled` | Desactivar en lugar de eliminar |
| `deployments.last_seen_at`, `last_probe_at`, `last_probe_status` | Salud de integración |
| Rol `platform_portal` para el proceso del portal (ver [Roles de base de datos](#roles-de-base-de-datos)) | Mínimo privilegio para el proceso de administración |
| Migración 0014: `areas`, `area_members`, `area_supervisors` (`platform_portal`: `SELECT, INSERT, DELETE`; nada para `platform_runtime`) y `audit_events.area_id` (escrito solo por acciones de supervisor) | Supervisores de área |

Desactivar en lugar de eliminar: las iniciativas mediante `status` (ya existe); las apps mediante `status`; un deployment se
elimina (nada lo referencia, la auditoría lo conserva); un rol se elimina solo sin grants (la FK ya lo
bloquea); un grupo se elimina solo cuando no tiene miembros ni grants. Los grants por email se resuelven a exactamente un usuario
humano activo: cero o varias coincidencias fallan (`users.email` no es único, `google_sub` es la identidad).

## Humanos y robots

1. Un robot es una fila en `users` con `kind = 'robot'`; los humanos tienen `kind = 'human'` (por defecto, para que las filas existentes sigan siendo válidas).
   Todo lo que se indexa por id de usuario funciona sin cambios: grants, `access.Decide`, `adminz`, el actor de auditoría y el `sub` de la aserción.
2. `users.google_sub` admite nulos. `CHECK ((kind = 'human') = (google_sub IS NOT NULL))`: un robot no tiene
   `google_sub`, así que un login de Google nunca puede resolver a él, y un humano siempre lo tiene (los usuarios semilla conservan su valor `fake:`).
   Los robots tienen `email = ''` (`CHECK (kind = 'human' OR email = '')`), un `name` de 1 a 64 caracteres (`CHECK (kind = 'human'
   OR char_length(name) BETWEEN 1 AND 64)`; los humanos conservan el valor por defecto `''`) único entre robots (sin distinguir mayúsculas), `status`,
   y un `owner_user_id` opcional (FK `users`, `ON DELETE SET NULL`): el humano responsable, informativo, no autoridad
   (sigue sin ser autoridad de acceso, pero ubica al robot en el área de su propietario para los supervisores de área, ver
   [Supervisores de área](api.md#supervisores-de-área)). La FK no puede decir "humano activo", así que la API lo verifica al crear y al cambiar (422 de lo contrario). Un robot no
   deja de funcionar cuando su propietario se deshabilita; la baja (offboarding) revisa la columna del propietario (la lista de robots la muestra).
3. Los robots son globales como los usuarios. Solo los administradores de plataforma crean, renombran, deshabilitan o cambian el propietario de un robot, y emiten
   o revocan sus tokens (los supervisores de área también pueden cambiar el propietario y revocar los tokens, solo desde una sesión de navegador
   y dentro de su alcance, ver [Supervisores de área](api.md#supervisores-de-área)). Los humanos se siguen creando solo por login. Las dos familias nunca se mezclan en la API: `/users/*`
   (`GET`, `PATCH` status, `DELETE .../sessions`) responde 404 para un id de robot y `/robots/*` responde 404 para un id humano,
   filtrado por `kind` en el SQL, no después de cargar la fila. Deshabilitar revoca los tokens del objetivo en una función de repositorio
   compartida por ambas rutas, en la misma transacción; registra los ids de los tokens revocados. La protección del último administrador
   cuenta solo humanos (los robots nunca están en `platform_admins`, así que ya es así).
4. Los robots obtienen acceso solo mediante grants directos, nunca mediante grupos: su acceso permanece explícito y revisable.
   El sujeto del grant gana `robot_id` (exactamente uno de `email`, `group_id`, `robot_id`); un email se resuelve a exactamente un
   **humano** activo (`FindActiveByEmail` filtra `kind = 'human'`), y `robot_id` debe nombrar a un robot activo (422
   de lo contrario). Los robots no tienen email, así que no se pueden encontrar con `GET /users?email=`.
5. Los robots nunca son administradores de plataforma. El esquema lo garantiza, así que la consulta de permisos no necesita filtro, con el mismo
   patrón que la FK compuesta de los grants: `users` obtiene `UNIQUE (id, kind)`, y `platform_admins` y `group_members`
   obtienen una columna `kind text NOT NULL DEFAULT 'human' CHECK (kind = 'human')` con una FK compuesta `(user_id, kind)`
   hacia `users (id, kind)` (cascada conservada). Ninguna de las dos tablas puede referenciar a un robot. La FK compuesta reemplaza a la de una sola columna
   bajo el mismo nombre de restricción (`group_members_user_id_fkey`, `platform_admins_user_id_fkey`), de modo que el
   mapeo `referenceFields` del repositorio del portal (FK violada -> campo de solicitud `user_id`) sigue siendo correcto.
   `sessions.user_id` no recibe pin: ninguna ruta de login produce una sesión para un id sin `google_sub`, y el rol que escribe
   sesiones podría falsificar una para un humano igual de bien, así que el pin agregaría una columna y ninguna protección.
6. Un robot puede tener un rol con `manages_access` (esto es lo que permite a automatización como `portalctl` gestionar grants), pero nunca es un
   gestor *efectivo* ([regla del último gestor efectivo](api.md#reglas-de-administración)): una iniciativa siempre conserva un gestor humano. El filtro
   solo-humanos va una vez en `effectiveGrantExpr` (repositorio del portal), solo en la rama de grant directo
   (`initiative_access.user_id` puede contener un robot; la rama de grupo no necesita ninguno, porque la llave foránea compuesta de
   `group_members` ya garantiza miembros humanos), que alimenta
   tanto la regla del último gestor como los conteos de salud de integración.

## Tokens de API

Tabla `api_tokens`. El secreto es `ptk_` + 43 caracteres de base64url (32 bytes aleatorios de `crypto/rand`) +
8 dígitos hexadecimales del CRC-32 del texto precedente: 55 caracteres. El prefijo permite a los scanners de secretos encontrar filtraciones; el checksum
es solo una verificación de formato (no es secreto, y el hash cubre toda la cadena), así que los valores mal formados se rechazan antes de
cualquier consulta a la base de datos.

| Columna | Significado |
|---|---|
| `id` | uuid (claim `tid`, objetivo de auditoría) |
| `user_id` | FK `users`, `ON DELETE CASCADE`: propietario, humano o robot |
| `name` | Etiqueta elegida por el creador, de 1 a 64 caracteres |
| `token_hash` | SHA-256 de todo el secreto, `bytea`, índice único. 256 bits de entropía hacen inútil un hash lento; la búsqueda es por hash, así que nunca se compara un secreto y no hay oráculo de tiempo |
| `scope` | `read` o `admin` (CHECK) |
| `created_by`, `created_at` | Usuario que actúa (igual a `user_id` para tokens personales; un administrador de plataforma para tokens de robot) |
| `expires_at` | NOT NULL, `CHECK (expires_at > created_at)`; el tope es configuración |
| `last_used_at` | Mejor esfuerzo |
| `revoked_at` | NULL mientras es válido |

## Áreas

| Tabla | Propósito y restricciones clave |
|---|---|
| `areas` | `id` uuid, `key` UNIQUE e inmutable (misma regla que las llaves de grupo, máximo 63 caracteres, CHECK), `name` (por defecto `''`), `created_at`. Sin `status`: un área no concede nada por sí misma, así que no hay nada que desactivar; se elimina solo cuando no tiene miembros ni supervisores |
| `area_members` | `area_id` FK `areas` (`ON DELETE RESTRICT`), `user_id`, `added_at`; **PK `(user_id)`**: un área por persona. Solo humanos: columna `kind text NOT NULL DEFAULT 'human' CHECK (kind = 'human')` y FK compuesta `(user_id, kind)` -> `users (id, kind)` `ON DELETE CASCADE` (el patrón de `group_members`, migración 0012). Índice `(area_id, user_id)` |
| `area_supervisors` | `area_id` FK `areas` (`ON DELETE RESTRICT`), `user_id`, `added_at`; PK `(area_id, user_id)` (un área puede tener varios supervisores, una persona puede supervisar varias áreas). Solo humanos, mismo pin de `kind` y FK compuesta. Índice `(user_id)` (la consulta de permisos) |
| `audit_events.area_id` | `uuid NULL`, sin FK ni índice (nada filtra por él todavía), como `token_id`: el área a través de la cual actuó un supervisor. NULL para todo otro evento |

Un supervisor también puede ser miembro de un área, incluida la que supervisa. Solo los administradores de plataforma crean y eliminan
áreas y gestionan sus miembros y supervisores. Sin jerarquía (un área no tiene padre), sin sincronización desde el IdP.

Los cambios de área no afectan las decisiones de acceso: sin `NOTIFY`, y platform-api nunca lee estas tablas.

## Roles de base de datos

### Privilegios de `platform_runtime`

El portal es dueño de la administración, así que platform-api corre con el mínimo que necesitan ForwardAuth y el login (migración 0010).
Todo lo demás se rechaza al iniciar: `CheckRuntimeRole` compara los privilegios que tiene el rol, a nivel de tabla y por
columna, con el conjunto esperado en ambos sentidos (un grant de más o uno faltante detiene el proceso).

- Solo lectura: `group_members`, `initiatives`, `initiative_roles`, `apps`, `initiative_access`.
- Sin acceso: `groups`, `platform_admins`, `schema_migrations`. EXECUTE sobre la función SECURITY DEFINER
  `applied_migrations()` (migración 0015) es la única vista de `schema_migrations`: devuelve los nombres de versión aplicados
  para la verificación de readiness. `platform_portal` tiene el mismo EXECUTE; PUBLIC no.
- `users`: SELECT, INSERT solo de `google_sub`, `email`, `name` (las columnas del upsert de login, así que este rol no puede crear un robot), y UPDATE solo de `email`, `name`, `updated_at` (upsert JIT del login); `status` y `google_sub` nunca son escribibles.
- `sessions`: SELECT, INSERT, DELETE, UPDATE de `last_seen_at`. `app_sessions`: SELECT, INSERT, DELETE.
- `login_codes`, `oauth_flows`: SELECT, INSERT, DELETE, UPDATE de `consumed_at`.
- `api_tokens`: SELECT y UPDATE solo de `last_used_at`, más EXECUTE sobre la función de purga SECURITY DEFINER
  `purge_dead_api_tokens` (sin DELETE sobre la tabla); nunca crea ni revoca tokens.
- `deployments`: SELECT y UPDATE solo de `last_seen_at` (el bloqueo de fila `FOR NO KEY UPDATE SKIP LOCKED` necesita UPDATE sobre al
  menos una columna); `host` y `app_id` son de solo lectura.
- `signing_keys`, `audit_events` (solo anexar), `access_denials` (solo anexar) y las funciones de partición no cambian.

Qué cubre la verificación (el rol del portal, `CheckPortalRole`, usa el mismo mecanismo contra su propio conjunto de grants de la migración 0009):

- Todo privilegio de PostgreSQL 18 sobre tablas, tablas particionadas, vistas, vistas materializadas, tablas foráneas (SELECT,
  INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER, MAINTAIN) y secuencias (USAGE, SELECT, UPDATE) del esquema `public`,
  cada uno sondeado también `WITH GRANT OPTION`. Una relación que no está en el conjunto esperado no debe tener nada.
- Una partición se mide contra su padre de nivel superior: un grant directo sobre una hoja más allá del conjunto del padre es deriva (una hoja con
  menos es normal, las particiones no tienen ACL propia).
- Se rechaza la pertenencia del rol efectivo a cualquier rol predefinido `pg_*`, directa o heredada.

Notas operativas:

- La verificación corre una vez por pool, al iniciar. La deriva introducida después se detecta en el siguiente reinicio.
- SELECT e INSERT a nivel de tabla cubren también las columnas agregadas después, mientras que UPDATE es a nivel de columna aquí: una columna nueva necesita un
  `GRANT UPDATE (col)` explícito (y una entrada en `runtimeGrants`) para ser escribible.
- Orden de despliegue: el Job de migración debe completarse antes de que platform-api (y el portal) se desplieguen. De lo contrario los pods nuevos
  ven los grants antiguos y se niegan a iniciar, o quedan no listos con la razón `migrations pending` (falla cerrada); los pods antiguos
  siguen sirviendo.

### Privilegios de `platform_portal`

`platform_portal` (proceso del portal):

- Tablas de autorización: CRUD, excepto `users`.
- `users`: SELECT; `INSERT (kind, name, email, owner_user_id, status)` sin `google_sub`, por lo que el portal solo puede
  crear robots (el CHECK rechaza un humano sin `google_sub`); `UPDATE (name, owner_user_id, status, updated_at)`. Los humanos
  los crea just-in-time platform-api y `google_sub` nunca es escribible.
- `api_tokens`: `INSERT`, `SELECT (id, user_id, name, scope, created_by, created_at, expires_at, last_used_at, revoked_at)`
  (nunca `token_hash`) y `UPDATE (revoked_at)`.
- EXECUTE sobre `revoke_api_token_by_hash(p_hash bytea) RETURNS uuid` (migración 0013), la única forma en que el portal toca
  `token_hash`: una función `SECURITY DEFINER` con un `search_path` fijado que rechaza un hash que no tiene 32 bytes y
  revoca el token vivo con ese hash (no revocado, no expirado), devolviendo su id o NULL cuando nada coincidió;
  `platform_runtime` y PUBLIC no pueden ejecutarla. Los privilegios de función quedan fuera de `CheckPortalRole` (compara solo privilegios de tabla,
  columna y secuencia), como `purge_dead_api_tokens`: una prueba los fija.
- `audit_events`: SELECT e INSERT (el `INSERT` a nivel de tabla cubre `area_id`); `access_denials`: SELECT.
- `sessions` y `app_sessions`: DELETE más SELECT sin los hashes de token.
- `areas`, `area_members`, `area_supervisors`: `SELECT, INSERT, DELETE` (sin `UPDATE`: las llaves son inmutables y no hay renombrado).
- Nada sobre `signing_keys`, `oauth_flows` ni `login_codes`.

`platform_runtime` no tiene nada sobre las tablas de áreas; `runtimeGrants` no tiene entrada para ellas (una tabla no listada no debe tener
ningún privilegio, así que `CheckRuntimeRole` ya lo exige). Los conjuntos esperados de `CheckPortalRole` incluyen las tres tablas de áreas.

Las migraciones que cambian privilegios siguen la regla de despliegue de [privilegios de `platform_runtime`](#privilegios-de-platform_runtime): el Job de
migración corre primero, y los pods nuevos fallan la verificación de inicio contra los grants antiguos (falla cerrada). `ALTER TABLE audit_events` toma
`ACCESS EXCLUSIVE` sobre una tabla en la que escribe platform-api (auditoría de login, `RevokeUser`): la migración 0014 fija `lock_timeout` como la 0012
y se reintenta en lugar de encolar a todos los escritores detrás de ella.
