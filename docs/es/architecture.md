# Arquitectura

> English version: [docs/architecture.md](../architecture.md)

atrium es una única entrada a las apps internas de una empresa: inicio de sesión con Google Workspace, autorización por app guardada en PostgreSQL y un proxy consciente de la identidad frente a cada app mediante Traefik ForwardAuth. Cada solicitud que pasa llega a la app con un JWT firmado de corta vida cuya audiencia es esa app. Esta página explica cómo encajan las piezas y por qué se puede confiar en los límites; el comportamiento exacto está en la [referencia](README.md). Las decisiones de diseño individuales están en [decisions/](decisions/).

## Componentes y procesos

| Componente | Rol |
|---|---|
| Traefik | Entrypoint `websecure`. Ejecuta `strip-assertion` y luego ForwardAuth en cada router, sin posibilidad de exclusión |
| `platform-api` | Autoriza cada solicitud (`/forwardauth`), ejecuta el login con Google y las sesiones, es dueño de las llaves de firma y publica el JWKS. Guarda la KEK. Cuatro listeners (internal, jwks, public, metrics): [Listeners](reference/forwardauth.md#listeners) |
| `portal` (`cmd/portal`) | La app del portal: el inicio ("qué apps puedo abrir") para todo usuario, más las pantallas de administración y la API JSON que las sostiene. Deployment, host y rol de base de datos propios; nunca guarda la KEK ni ninguna llave de firma |
| `portalctl` | CLI en Go (`platform/api/cmd/portalctl`) que habla con la API del portal con un token y solo con el host del portal. Resuelve llaves y nombres de robots listando (la API no filtra por llave), trata un 409 al crear como `unchanged` solo cuando el grant directo existente está vigente (uno vencido es un error: la llave única ignora `expires_at`) y rechaza cualquier redirección. Uso: [operate.md](operate.md#portalctl) |
| `sdk/go` | La biblioteca que usan las apps para validar la aserción una sola vez: [JWT de aserción](reference/assertion.md#validación-del-lado-de-la-app) |
| UI web (`platform/web`) | Nuxt 4 con `ssr: false` + `nuxi generate` (y `experimental.entryImportMap: false`), embebida en el binario `portal` con `embed.FS`, con fallback de SPA a `200.html`. Sin runtime de Node en producción |
| PostgreSQL | Datos de autorización, sesiones, llaves, auditoría: [Modelo de datos](reference/data-model.md) |
| Apps | Alcanzables solo desde Traefik (NetworkPolicy); cada una verifica la aserción |

### Forma del portal

- Una app web, `portal`, en su propio host detrás del mismo ForwardAuth. El host de autenticación nunca sirve esta UI: un XSS ahí
  comprometería el login de todas las apps.
- Un proceso separado de platform-api: mismo módulo Go, binario `cmd/portal`, con Deployment, host y rol de base de datos propios.
- Es una app como cualquier otra: registrada en `apps`/`deployments` bajo la iniciativa reservada `platform`, y
  valida la aserción con `sdk/go` (`aud` = su app id). `PLATFORM_PORTAL_APP_ID` le dice a platform-api qué app
  recibe la regla de "cualquier usuario activo" ([tabla de decisión](reference/forwardauth.md#tabla-de-decisión)); se usa el id en lugar de
  un host porque los hosts pueden cambiar y ser varios. La regla se aplica solo a una app de la iniciativa reservada `platform`:
  la decisión lo verifica cada vez (fail closed), y platform-api registra un WARN al arrancar si el id apunta a otro lado.
  El proceso del portal hace la misma verificación sobre su propio `PORTAL_APP_ID` (WARN, nunca hace fallar el arranque); `PORTAL_APP_ID`
  (portal) y `PLATFORM_PORTAL_APP_ID` (platform-api) deben tener el mismo id. `access_changed` lo emite
  `deployments` solo en INSERT, DELETE o un cambio de host o app_id; las columnas de telemetría no lo emiten.
- Primero una API JSON (`/api/v1/*`), con la UI web encima: [API del portal](reference/api.md). La CLI es `portalctl`; reemplazó a los
  scripts de shell anteriores para otorgar y revocar grants.
- Las herramientas de Go (build, test, vet, lint) siempre corren en modo workspace (`platform/go.work`): `cmd/portal` importa
  `sdk/go` sin `require`/`replace`, igual que `examples/hello`. Un build de `platform/api` fuera del
  workspace no resuelve el SDK.

Fundamento: [ADR 0003](decisions/0003-separate-listeners-per-audience.md), [ADR 0006](decisions/0006-portal-as-a-separate-process-and-database-role.md).

## Flujo de una solicitud

1. Un cliente llega a Traefik en `websecure`. `strip-assertion` elimina cualquier `Platform-Assertion` enviado por el cliente, luego
   ForwardAuth llama a `platform-api /forwardauth` (siempre con GET; el método original llega en `X-Forwarded-Method`).
2. platform-api resuelve la app desde `X-Forwarded-Host` contra `deployments.host` y decide ([tabla de decisión](reference/forwardauth.md#tabla-de-decisión)):
   permite solo si el usuario está activo, la app y su iniciativa están activas y existe un grant no vencido, directo o a través de un grupo.
   Cualquier otra cosa es un deny; una falla de platform-api o de la base de datos es un 503, nunca un 2xx.
3. Al permitir, platform-api responde 200 con `Platform-Assertion`, un JWT ES256 para esa única app (`aud`), válido 90 s, y
   Traefik lo reenvía a la app. La app lo valida con `sdk/go`, usando las llaves del JWKS ([llaves](reference/keys.md)).
4. Primera visita, sin cookie de app válida: una navegación recibe un 302 al login en el host de autenticación
   (`/login?rd=<original url>&bind=<hash>`, más la cookie de corta vida `__Host-Http-platform_bind`); una llamada de API recibe un 401
   con `login_url`.
5. El host de autenticación ejecuta el flujo Google OIDC ([login](reference/login.md#google-oidc)) y crea la sesión central
   (`__Host-Http-platform_session`). Luego emite un `login_code` de un solo uso ligado a la sesión, la app, el host destino
   y el hash de `bind`.
6. El navegador vuelve al host de la app en `/_platform/callback?code=`. ForwardAuth acepta el código solo del navegador
   que tiene la cookie de bind correspondiente, y responde un 302 a la URL original con la cookie de app `__Host-Http-platform_app`.
   Desde entonces se aplica el paso 3.
7. Los clientes que no son navegadores (CLI, automatización) envían `Authorization: Bearer <token>` al host del portal; el mismo ForwardAuth
   valida el token y emite una aserción con claims de token ([ruta de token](reference/forwardauth.md#ruta-de-token)).

## Modelo de confianza

| Límite | Qué lo hace cumplir |
|---|---|
| `/forwardauth` nunca es alcanzable desde una ruta pública ni desde namespaces de apps | Listener interno separado, alcanzable desde Traefik y kubelet mediante NetworkPolicy ([listeners](reference/forwardauth.md#listeners)) |
| Las apps solo reciben tráfico que pasó por ForwardAuth | La NetworkPolicy deja que solo Traefik las alcance; la aserción firmada es la defensa si eso falla. El probe verifica el caso opuesto ([métricas](reference/metrics.md#probe)) |
| Un cliente no puede falsificar una aserción | Cualquier `Platform-Assertion` enviado por el cliente se elimina antes de que corra ForwardAuth; la aserción está firmada con ES256 y ligada a una sola app |
| Las llaves de firma no se filtran por la base de datos | Las llaves privadas se sellan con una KEK que nunca llega a la base de datos; solo platform-api la guarda |
| Un proceso comprometido no puede hacer más que su rol | `platform_runtime` (platform-api) y `platform_portal` (portal) tienen privilegios mínimos; ambos se verifican contra un conjunto esperado al arrancar y el proceso se niega a iniciar si hay desviación ([privilegios](reference/data-model.md#privilegios-de-platform_runtime), [roles](reference/data-model.md#roles-de-base-de-datos)) |
| Nada está abierto por defecto | Sin deployment o sin grant significa deny; los permisos se resuelven desde la base de datos en cada llamada al portal, nunca viajan en la aserción |
| Las fallas cierran | Una falla de platform-api o de la base de datos es un 503; una réplica que no puede cargar la llave activa deja de firmar; un desajuste de KEK hace fallar la readiness |

### Clientes que no son navegadores

La CLI y la automatización llaman a la API del portal con tokens. El producto es multiempresa, por lo que el diseño no asume ningún sistema de CI,
gestor de secretos ni stack del lado del adoptante. Fundamento: [ADR 0008](decisions/0008-opaque-api-tokens-and-robots.md).

| Fase | Mecanismo | Estado |
|---|---|---|
| 1 | Tokens de acceso personal (humanos) y tokens de robot, un solo mecanismo | Implementada (tokens personales, tokens de robot, `portalctl`) |
| 2 | Identidad de workload: emisores OIDC configurables por adoptante (service accounts de Kubernetes, CI de GitHub o GitLab), mapeados a robots por una política de confianza sobre claims combinados (emisor + audiencia + subject + repositorio/namespace); sin secretos de larga vida | Solo dirección; reutiliza robots, scopes y la ruta de token de la fase 1 |
| 3 | Device flow de OAuth, solo si los tokens personales resultan molestos. Las campañas de phishing de device code (estilo Storm-2372) apuntan a administradores, por lo que requeriría una página de verificación que muestre el cliente y el scope solicitado, vida corta del código y ningún scope `admin` sin una sesión de navegador | No planificada |

Las fases 2 y 3 se siguen en [TODO.md](../../TODO.md). Los ID tokens de Google se descartaron
(`gcloud auth print-identity-token --audiences` no funciona con cuentas de usuario, y la identidad de Google evitaría la
revocación y la auditoría de la plataforma).

Modelo de amenazas de los tokens de API y los robots:

| Amenaza | Mitigación | Riesgo residual |
|---|---|---|
| Token filtrado en logs, salida de CI o historial del shell | Prefijo para escáneres de secretos; vencimiento (tope de 30 días personal, 90 robot); `last_used_at` y revocación; Traefik elimina `Authorization` antes del portal, platform-api nunca lo registra; query strings rechazados | Un secreto bearer funciona para quien lo tenga hasta que se revoque o venza; sin restricción de emisor (DPoP, mTLS). Los logs de Traefik o del lado del cliente fuera de la plataforma son responsabilidad del adoptante |
| Volcado de la base de datos | Solo se guarda el SHA-256 de secretos de 256 bits; `platform_portal` no puede leer los hashes | Ninguno para el secreto; un volcado igualmente expone metadatos de tokens (nombres, dueños) |
| Token reenviado al portal por una app hermana que lo recibió por error | Los tokens no se aceptan en ningún otro host; las apps no pueden leer los headers de la ruta del portal (Traefik los elimina); los clientes hablan solo con el host del portal | Una app hermana maliciosa que obtenga el secreto puede reutilizarlo contra el portal hasta el vencimiento o la revocación: el token no tiene vínculo de audiencia más allá de la regla de host |
| Token enviado a otros hosts de apps | Platform-api lo ignora ahí; el secreto queda expuesto a esa app, que podría reutilizarlo contra el portal | Hasta el vencimiento o la revocación. La CLI rechaza cualquier host que no sea el del portal; un `Authorization` extraviado en otro lado es un error del cliente |
| Escalada de privilegios al emitir | Solo sesión de navegador; los dueños solo para sí mismos; `admin` solo si el dueño es admin o manager en ese momento; tokens de robot solo por administradores de plataforma; el scope nunca agrega permisos | Ninguno más allá de los derechos propios del dueño al emitir |
| Deriva de privilegios tras la emisión (grant revocado, dueño deshabilitado) | Los permisos se resuelven en vivo en cada solicitud desde el dueño; deshabilitar revoca los tokens; ForwardAuth verifica el `status` del dueño en cada solicitud | La fila del token no tiene caché, pero las reglas de app e iniciativa pueden venir de la caché de decisiones de 30 s si se reutiliza el Evaluator |
| Brecha de baja (offboarding): usuario suspendido en Google pero no deshabilitado en el portal | Tope personal de 30 días; la baja deshabilita al usuario en el portal, lo que revoca sesiones y tokens de inmediato ([limitaciones del login](reference/login.md#limitaciones)) | Un usuario suspendido, no deshabilitado, conserva un token personal funcional hasta 30 días; cerrarlo requiere la verificación con la Directory API ([limitaciones del login](reference/login.md#limitaciones)) |
| El `Authorization` de cada app llega a platform-api | Agregado a `authRequestHeaders`; se lee solo para la app del portal, nunca se registra; el listener interno es alcanzable solo desde Traefik (NetworkPolicy), la misma exposición que `Cookie` hoy | Un compromiso de platform-api ve los headers `Authorization` de las apps, como ya ve las cookies |
| Robot usado como administrador encubierto | Los robots nunca son administradores de plataforma (restricción del esquema), nunca están en grupos, nunca son managers efectivos; solo los administradores de plataforma los crean; la creación y los grants quedan auditados | Un administrador de plataforma aún puede otorgar a un robot `manages_access` en una iniciativa junto con un manager humano; se revisa mediante la auditoría y la lista de robots |
| Un token filtrado emite tokens nuevos | Emisión solo desde una sesión de navegador (aserción sin `tid`) | Solo contención: un token `admin` filtrado de un administrador aún puede cambiar grants, deshabilitar usuarios y revocar tokens hasta ser revocado |
| CSRF o confused deputy a través del navegador | Bearer no es ambiental; el preflight nunca se aprueba; Fetch Metadata y solo JSON sin cambios; la ruta de token ignora las cookies | Ninguno identificado |
| DoS con valores Bearer aleatorios | Verificación de formato y checksum antes de la base de datos; de lo contrario una consulta indexada, no más pesada que la consulta de sesión sin caché existente; los rate limits de Traefik son herramienta del adoptante | Una avalancha de tokens aleatorios bien formados cuesta una consulta indexada cada uno; misma clase que una avalancha de cookies aleatorias |
| Latencia de revocación | Sin caché: la siguiente solicitud tras la revocación falla en todas las réplicas | Una solicitud que ya pasó ForwardAuth termina; su aserción vive como máximo 90 s y nunca es visible para el cliente |
| Phishing a un administrador para que cree un token | La creación requiere una sesión de navegador en el host del portal y muestra el scope y el vencimiento; auditada | Un administrador engañado puede entregar un token a un atacante; mitigado solo por el vencimiento y la revisión. El device flow (fase 3) ampliaría esta superficie |
| Interno que crea un robot | Solo administradores de plataforma; cada creación de robot, grant y token queda auditada; los robots se listan con dueño y último uso | Un administrador de plataforma malicioso ya tiene confianza para otorgar grants; la auditoría es el control |
| Proceso del portal comprometido | No puede leer `token_hash` ni escribir `platform_admins`; `platform_runtime` no puede insertar tokens ni eliminarlos, y no puede crear un robot | El rol del portal puede insertar un token con un hash que conoce para cualquier usuario, equivalente a lo que ya permiten sus escrituras de grants; ese rol puede eludir el rastro de auditoría |

### Supervisores de área

Fundamento: [ADR 0010](decisions/0010-local-areas-with-admin-assigned-supervisors.md). Modelo de amenazas:

| Amenaza | Mitigación | Riesgo residual |
|---|---|---|
| El supervisor escala a acceso | Sin acción sobre grants, roles, grupos ni apps; el alcance son metadatos de credenciales, revocación y `owner_user_id` | Ninguno para el acceso |
| El supervisor administra a un par, a un administrador o a sí mismo | Los supervisores y los administradores de plataforma nunca son supervisables (un predicado SQL sobre el destino, evaluado en cada solicitud, con tests) | Ninguno identificado |
| Privilegio por efecto secundario (alguien agregado a un grupo de acceso cae bajo un supervisor) | Las áreas son tablas propias, gestionadas solo por administradores de plataforma | Un error de un administrador de plataforma al agregar un miembro; auditado |
| El supervisor interrumpe la automatización (revoca los tokens de un robot de deploy) | Solo robots que pertenecen a sus personas; auditado con `area_id` | Capacidad intencional: un administrador que asigna el dueño de un robot compartido lo entrega a los supervisores de ese dueño |
| Robot movido fuera de alcance | Reasignación solo a personas supervisables de las áreas del actor; se rechaza borrar el dueño | Un supervisor de dos áreas puede mover un robot entre ellas, de modo que los otros supervisores del área destino lo ganan |
| Token filtrado de un supervisor | La capacidad de supervisor es solo de sesión de navegador: bajo un token `Supervised` está vacío (un solo lugar, `portal.subject`), y supervisar nunca permite un token `admin` | Ninguno más allá de lo que el dueño del token puede hacer sin la capacidad |
| Un supervisor removido termina una acción en curso | La siguiente solicitud relee el alcance; sin caché | Una solicitud que ya está en su transacción hace commit; nunca extiende el acceso; auditado |

## Casos de uso

- **Abrir una app.** `GET /api/v1/me/apps` lista las apps que el solicitante puede abrir con las reglas exactas de la decisión
  de ForwardAuth: un deployment aparece en el inicio si y solo si `/forwardauth` respondería 200 para ese usuario (la app
  del portal nunca se lista).
- **Dar acceso.** Un administrador de plataforma o un manager de iniciativa otorga un rol en una iniciativa a un usuario (por email), un grupo
  o un robot ([quién administra qué](reference/api.md#quién-administra-qué)). El acceso se liga a `google_sub`, por lo que un usuario inicia
  sesión una vez, o recibe acceso a través de un grupo, antes de los grants directos.
- **Automatizar.** Un administrador de plataforma crea un robot y le emite un token; al robot se le otorgan roles directamente, nunca a través de
  grupos. Un humano usa un token personal con la CLI. Los tokens vencen (30 días personal, 90 días robot por defecto) y se
  revocan al deshabilitar ([Tokens](reference/api.md#tokens)).
- **Contener un token filtrado.** El dueño lo revoca por id; un administrador de plataforma revoca por id o por secreto. Un token puede
  revocar tokens (los de su propio dueño, o cualquiera si el dueño es administrador de plataforma y el scope es `admin`), por lo que la contención
  no requiere un navegador.
- **Dar de baja a una persona.** Ver [Baja, de extremo a extremo](#baja-de-extremo-a-extremo).

### Baja, de extremo a extremo

1. Una persona se va. Su supervisor revoca los tokens personales de la persona (inmediato, sin caché) y reasigna cada uno de
   sus robots a otro miembro, o revoca los tokens del robot cuando nadie lo asume (un robot se autentica
   solo con tokens, por lo que eso lo detiene; deshabilitar queda en manos de los administradores de plataforma).
2. Un administrador de plataforma deshabilita al usuario cuando RR. HH. o TI lo indica (sesiones y tokens restantes revocados, regla 5 de
   [Reglas de tokens](reference/api.md#reglas-de-tokens)). Deshabilitar no quita a la persona de su área: sigue siendo supervisable, así que los robots que aún le
   pertenecen permanecen en el alcance de su supervisor. Un administrador de plataforma quita a la persona del área cuando no quede nada.
3. El corte de sesión no es responsabilidad del supervisor: una sesión de navegador termina con la deshabilitación o con el tope de 8 h ([login](reference/login.md#reglas)).
4. La baja de un supervisor es una tarea de administrador de plataforma. Deshabilitado, es un sujeto inactivo y no puede hacer nada; sus
   filas de `area_supervisors` permanecen hasta que un administrador de plataforma las quite, y mientras permanezcan la persona sigue
   siendo no supervisable (regla 1), por lo que sus propios tokens y robots solo son alcanzables por administradores de plataforma. Un área sin supervisor activo recae en los administradores de plataforma; no hay
   regla de "último supervisor", porque los administradores de plataforma siempre alcanzan a toda persona.
5. Sin notificación: la plataforma no tiene un canal de notificación. El dueño anterior y el nuevo se enteran de una reasignación
   por la auditoría (administradores de plataforma) y la lista de robots; no se envía nada.

## Glosario

| Término | Significado |
|---|---|
| Iniciativa | La unidad que agrupa apps; los roles, grants y managers viven dentro de ella. Tiene una `key` inmutable y un `status` (active/inactive). La iniciativa reservada `platform` contiene la app del portal y no se puede desactivar |
| Rol | Un rol con nombre que solo existe dentro de una iniciativa (`initiative_roles`). Su llave es parte del contrato de la app: las llaves de los roles que un usuario tiene en la iniciativa de la app se entregan en el claim `roles`. El flag `manages_access` hace que quienes lo tienen sean managers de la iniciativa |
| Grant | Una fila de `initiative_access`: una iniciativa, un rol y exactamente un sujeto (un usuario, un grupo o un robot), con un `expires_at` opcional. Los robots solo reciben grants directos |
| Grupo | Un grupo interno de humanos, global entre iniciativas, usable como sujeto de un grant. La sincronización de grupos de Google Workspace es una extensión posterior |
| Área | Un contenedor definido en el portal que agrupa personas (un área por persona) para los supervisores de sus credenciales. No otorga nada por sí misma y no es un grupo |
| Supervisor de área | Un humano que administra los robots y tokens de API de las personas de un área (nunca el acceso), solo desde una sesión de navegador |
| Administrador de plataforma | Un humano en `platform_admins`, separado de los roles de iniciativa, con administración a nivel de toda la plataforma. Se crea solo por CLI o SQL |
| Manager (manager de iniciativa) | Un usuario que tiene un rol con `manages_access` en una iniciativa; administra los grants de esa iniciativa |
| Robot | Una identidad no humana: una fila en `users` con `kind = 'robot'`, sin email, sin login de Google; creada por administradores de plataforma y autenticada solo con tokens |
| Token personal / token de robot | Un token opaco de acceso a la API `ptk_` con scope `read` o `admin` y vencimiento obligatorio, propiedad de un humano o de un robot respectivamente |
| App | Un servicio detrás de ForwardAuth, con un id opaco e inmutable (`app_<ulid>`) que es el `aud` de la aserción; pertenece a una iniciativa |
| Deployment / host | Un deployment mapea un host en minúsculas a una app; una app puede tener varios. ForwardAuth resuelve la app solo desde el host |
| Audiencia (`aud`) | El id de app para el que se emite la aserción; una app acepta solo la suya |
| Aserción | El JWT ES256 en `Platform-Assertion` que indica quién es el usuario para una app: [JWT de aserción](reference/assertion.md) |
| KEK | La key-encryption key, montada desde un Secret de Kubernetes, que sella las llaves de firma privadas; nunca se guarda en la base de datos |
| JWKS | Las llaves de firma públicas, servidas en `/.well-known/jwks.json` en un listener dedicado, usadas por las apps para verificar aserciones |
| App del portal | La app nombrada por `PLATFORM_PORTAL_APP_ID` dentro de la iniciativa `platform`: cualquier usuario activo puede abrirla, y aloja el inicio, la administración y la API |
