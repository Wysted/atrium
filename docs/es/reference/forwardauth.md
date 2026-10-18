# ForwardAuth

> English version: [docs/reference/forwardauth.md](../../reference/forwardauth.md)

El contrato entre Traefik y platform-api: cómo se autoriza cada solicitud y qué recibe la app. Por qué está construido así: [ADR 0001](../decisions/0001-per-app-assertion-with-opaque-ids.md), [ADR 0003](../decisions/0003-separate-listeners-per-audience.md).

## Configuración de Traefik

Se adjunta al entrypoint `websecure` (se aplica a cada router, sin posibilidad de exclusión), en este orden:

1. `strip-assertion` (middleware `headers`): elimina cualquier `Platform-Assertion` enviado por el cliente.
2. `forwardauth`: llama a `platform-api /forwardauth` con
   - `authResponseHeaders: [Platform-Assertion]`
   - `authRequestHeaders` como lista de permitidos: `Cookie`, `Accept`, `Sec-Fetch-Mode`, `Sec-Fetch-Site`, `Origin`,
     `Access-Control-Request-Method`, `Access-Control-Request-Headers`, `X-Requested-With`, `User-Agent`,
     `Authorization` (`Sec-Fetch-Site` y `Origin` se necesitan para la verificación CSRF del logout; `Authorization` se lee solo para
     hosts del portal y llega a platform-api para todos los hosts, ver [Ruta de token](#ruta-de-token))
   - `maxResponseBodySize` acotado
   - los `X-Forwarded-*` del cliente nunca son de confianza (entrypoint `forwardedHeaders` sin `trustedIPs`)

Traefik siempre llama a `/forwardauth` con GET; el método original llega solo en `X-Forwarded-Method`.

### Comportamiento de Traefik del que depende este contrato

Traefik v3.7.14:

1. Con `authRequestHeaders` como lista de permitidos, `X-Forwarded-Method/Host/Uri/Proto/For/Port` siguen llegando a platform-api
   (los provee Traefik); los headers fuera de la lista (personalizados) no. Un `X-Forwarded-Host` enviado por el cliente es reemplazado
   por el host real.
2. `strip-assertion` (`customRequestHeaders` con valor vacío) elimina una aserción enviada por el cliente en el host de autenticación y
   en solicitudes de preflight; las solicitudes autorizadas siguen recibiendo la aserción emitida por platform-api.
3. `maxResponseBodySize` está en bytes; una respuesta de autenticación mayor se rechaza con 401.
4. `trustForwardHeader` está deprecado en v3.7 (se elimina en el próximo major); permanece explícitamente en `false` y se revisa
   en la actualización al major de Traefik.
5. `Authorization` en `authRequestHeaders` llega a platform-api y nada más cambia para las apps: una app detrás de
   ForwardAuth sigue recibiendo el `Authorization` que se le envió, detrás de una sesión válida; sin una, la solicitud es un 401
   como antes.
6. En un 401/403 de ForwardAuth, Traefik copia el `WWW-Authenticate` de la respuesta de autenticación al cliente
   (`Bearer error="invalid_token"`, `Bearer` solo, `insufficient_scope`), sin redirección y sin `Set-Cookie`.

## Listeners

platform-api sirve cuatro listeners para que `/forwardauth` nunca sea alcanzable por una ruta pública ni desde namespaces de apps:

| Listener | Rutas | Alcanzable desde |
|---|---|---|
| internal | `/forwardauth`, `/healthz`, `/readyz` (responde 404 para el JWKS) | Traefik (ForwardAuth) y kubelet (probes) mediante NetworkPolicy; los namespaces de apps no pueden alcanzarlo |
| jwks | solo `GET /.well-known/jwks.json` (`PLATFORM_JWKS_ADDR`, obligatorio; solo HTTPS con TLS 1.3 cuando `PLATFORM_JWKS_TLS_CERT_FILE` y `PLATFORM_JWKS_TLS_KEY_FILE` están definidos, HTTP plano en caso contrario) | namespaces de apps y el namespace del portal mediante NetworkPolicy |
| public | rutas de navegador del host de autenticación: `/login`, `/oauth2/callback`, `/logout`, página de sesión cerrada, `/.well-known/jwks.json` | IngressRoute de Traefik solo para el host de autenticación |
| metrics | solo `/metrics` (`PLATFORM_METRICS_ADDR`, obligatorio) | solo el namespace de monitoreo, mediante NetworkPolicy |

Una NetworkPolicy filtra por puerto, nunca por ruta, por lo que `/metrics` y el JWKS tienen cada uno un listener propio: ninguno puede compartir uno que alcancen Traefik o los namespaces de apps, y los namespaces de apps alcanzan un puerto que no sirve nada más que el JWKS. Las cuatro direcciones (`PLATFORM_INTERNAL_ADDR`, `PLATFORM_PUBLIC_ADDR`, `PLATFORM_METRICS_ADDR`, `PLATFORM_JWKS_ADDR`) son obligatorias y deben usar puertos distintos (un host comodín entra en conflicto con cualquier host en el mismo puerto).

`/readyz` falla cuando la base de datos no es alcanzable, cuando una migración embebida no está aplicada (`migrations pending`, o `migrations check failed` si la verificación misma da error; el portal se comporta igual), cuando no hay un firmante cargado, o ante `KEKMismatch`.

## Entradas que lee platform-api

`X-Forwarded-Host`, `X-Forwarded-Method`, `X-Forwarded-Uri`, `Cookie`, `Accept`, `Sec-Fetch-Mode`,
`Origin`, `Access-Control-Request-Method`, y `Authorization` solo cuando el host es un host del portal (nunca se registra). Se ignoran: `X-Forwarded-Prefix`, `X-Forwarded-Port`, `X-Forwarded-Proto`.
La app se resuelve solo desde `X-Forwarded-Host` (exacto, en minúsculas) contra `deployments.host`.

## Tabla de decisión

| Caso | Respuesta |
|---|---|
| El host es el host de autenticación | 200, sin aserción |
| Host que no está en deployments | 403 (una fila `unknown_host` va a `access_denials`, ver [Clasificación de solicitudes y reglas de borde](#clasificación-de-solicitudes-y-reglas-de-borde)) |
| Preflight de CORS (`OPTIONS` + `Origin` + `Access-Control-Request-Method`) | 200, sin aserción (CORS es responsabilidad de la app) |
| Host del portal con un header `Authorization` (cualquier valor; la fila de preflight anterior tiene prioridad) | Solo ruta de token, cookie ignorada (una cookie válida con un token inválido es 401), nunca una redirección: token `Bearer` válido y dueño activo -> 200 + `Platform-Assertion` (`roles` vacío, claims de token); token malformado, desconocido, vencido o revocado, dueño inactivo -> 401 problem+json con `WWW-Authenticate: Bearer error="invalid_token"`; método no permitido por el scope del token -> 403. Cualquier otro host ignora `Authorization` |
| `/_platform/callback?code=` con un código de un solo uso válido ligado a este host y a la cookie `__Host-Http-platform_bind` del navegador | 302 al `rd` guardado + `Set-Cookie: __Host-Http-platform_app`, `Referrer-Policy: no-referrer` |
| Cookie de app ausente o inválida, navegación (`GET`/`HEAD` + señales HTML) | 302 a `https://auth.apps.<domain>/login?rd=<original url>&bind=<hash>` + `Set-Cookie: __Host-Http-platform_bind` (valor aleatorio de corta vida) |
| Cookie de app ausente o inválida, API/XHR | 401 |
| El host pertenece a la app del portal (`PLATFORM_PORTAL_APP_ID`, ver [Componentes y procesos](../architecture.md#componentes-y-procesos)) | 200 + `Platform-Assertion` con `roles` vacío para cualquier usuario activo, mientras la app esté activa y su iniciativa esté activa |
| Autenticado, sin acceso a la app (sin grant, usuario deshabilitado, app deshabilitada, iniciativa inactiva) | 403 (HTML para navegación, JSON para API) |
| Autorizado | 200 + `Platform-Assertion` |
| Falla de platform-api o de la base de datos | 503; fail closed, nunca 2xx |

## Clasificación de solicitudes y reglas de borde

- Navegación vs API: solo `GET`/`HEAD` pueden ser navegación. Cuando `Sec-Fetch-Mode` está presente, decide por sí solo
  (`navigate` = navegación, cualquier otro = API, sin importar `Accept`); sin él, un `Accept` HTML significa navegación.
  Cualquier otro método, incluido un `POST` de formulario, es API (401): una solicitud no idempotente nunca se redirige.
- La cookie `__Host-Http-platform_bind` se reutiliza (y se renueva su vida) cuando ya existe una bien formada,
  de modo que pestañas paralelas o llamadas de API no sobrescriban el vínculo de las otras. Las respuestas 401 de API también la establecen, con
  `login_url` apuntando a `https://<host>/`.
- Un callback inválido responde 403 y conserva la cookie de bind: borrarla permitiría que el enlace de un atacante cancelara el
  login en curso de la víctima. El código se quema en ambos casos.
- `access_denials` registra un denial cuando se evalúa la decisión (cache miss), no en cada 403 repetido.
- Un host sin deployment (un host huérfano: Traefik lo enrutó aquí pero ninguna app lo posee) se registra como un
  denial `unknown_host` sin usuario. El host es una entrada hostil (viene de la red), por lo que el registro está
  acotado en cada paso y nunca forma parte de la respuesta: el host debe ser un nombre DNS válido según las mismas reglas que
  `deployments.host` (minúsculas, etiquetas de 1 a 63, a lo sumo 253 bytes; cualquier otra cosa nunca se registra); un host se
  registra a lo sumo una vez cada 10 minutos por réplica (caché de marcadores de a lo sumo 10 000 hosts); cada réplica tiene un token
  bucket de 30 filas por minuto con ráfaga de 30 para todos los hosts en conjunto; los hosts aceptados pasan por una cola acotada
  (128) hacia un único escritor en segundo plano, por lo que ForwardAuth nunca espera a la base de datos por ellos. Fuera de presupuesto, con la caché de
  marcadores llena o la cola llena, el host se descarta y se contabiliza (un WARN resume los descartes una vez por minuto).
  El límite real es entonces 30 filas por minuto por réplica sostenido (43 200 por día), sin importar lo que envíe
  un atacante. El portal lee estas filas ([hosts huérfanos](metrics.md#hosts-huérfanos)).
- La resolución host-a-app y las decisiones se cachean por 30 s y ambas cachés se vacían con `access_changed`.
  Un host que no es un nombre DNS válido (mismas reglas que `deployments.host`) se rechaza con el mismo 403 antes de cualquier
  búsqueda: sin consulta, sin slot de caché. Los hosts válidos pero sin deployment se cachean aparte (a lo sumo 10 000,
  mismo TTL, también vaciados por `access_changed`), de modo que una avalancha de nombres aleatorios nunca pueda desalojar a un host conocido.
  El estado de sesión nunca se cachea.

## Convenciones

- Rutas reservadas en cada host de app: `/_platform/*` (`callback`, `logout`). Las apps no deben usarlas. `logout` acepta solo POST ([login](login.md#reglas)).
- Los errores JSON usan RFC 9457 `application/problem+json` con un miembro adicional `login_url`, para que los frontends de las apps sepan adónde enviar al usuario en un 401.
- Cada respuesta de `/forwardauth` lleva `Cache-Control: no-store`.
- Una línea de log de decisión por solicitud: decisión, motivo, host, app id, `sid`, `jti`. Nunca tokens ni cookies.

## Ruta de token

Permite que clientes que no son navegadores (CLI, automatización) llamen a la API del portal con un token personal o de robot. Solo platform-api decide. Modelo de tokens y ciclo de vida: [API](api.md#tokens); claims en la aserción: [aserción](assertion.md#claims-añadidos-para-tokens-de-api-y-robots). Fundamento: [ADR 0008](../decisions/0008-opaque-api-tokens-and-robots.md).

1. Traefik agrega `Authorization` a `authRequestHeaders`. Esto llega a platform-api para **todos** los hosts, por lo que platform-api
   lo lee solo cuando la app resuelta cumple la misma regla de portal que el Evaluator (`Facts.PortalApp`: app id igual
   a `PLATFORM_PORTAL_APP_ID` **y** iniciativa `platform`, no solo el app id) y lo ignora, y nunca lo registra,
   en cualquier otro host. Como `Cookie`, llega solo al listener interno, que solo Traefik alcanza (NetworkPolicy). Las apps detrás de ForwardAuth conservan su propio `Authorization` (Grafana, por ejemplo): ForwardAuth nunca
   cambia la solicitud que llega a la app.
2. En un host del portal, cualquier header `Authorization` toma exclusivamente la ruta de token; la cookie no se consulta y no
   hay fallback a ella. Forma aceptada: un header, esquema `Bearer` (sin distinguir mayúsculas), un token. Los tokens nunca se
   aceptan en un query string, una cookie ni otro header.
3. Orden: verificación de formato (prefijo, longitud, checksum) -> una consulta por `token_hash` unida con el dueño, sin caché:
   `revoked_at IS NULL AND expires_at > now()` y dueño con `status = 'active'` -> las reglas del portal de la [tabla de decisión](#tabla-de-decisión) (app e
   iniciativa activas; cualquier dueño activo; `roles` vacío) -> scope: con `read`, un método distinto de `GET`/`HEAD`
   (`X-Forwarded-Method`) responde 403. La fila del token y el `status` del dueño se leen en vivo; si las reglas de app e
   iniciativa reutilizan el Evaluator, pueden venir de su caché de decisiones de 30 s (vaciada por `access_changed`).
4. Fallas (RFC 6750): un token malformado, desconocido, vencido o revocado, o un dueño inactivo (humano o robot), responden
   401 `application/problem+json` con `WWW-Authenticate: Bearer error="invalid_token"`, el mismo cuerpo para toda causa
   (sin oráculo; el motivo va al log). Un header `Authorization` con otro esquema, o `Bearer` sin
   credenciales, responde 401 con `WWW-Authenticate: Bearer` y sin `error`. Nunca una redirección, sin `Set-Cookie`, sin
   `login_url`. Violación de scope: 403 con `WWW-Authenticate: Bearer error="insufficient_scope"`. Falla de la base de datos:
   503 (fail closed).
5. Éxito: 200 + `Platform-Assertion` ([claims](assertion.md#claims-añadidos-para-tokens-de-api-y-robots)). `last_used_at` se escribe con throttling (una vez por minuto por token por
   réplica, como `deployments.last_seen_at`) y en modo best effort, mediante una cola acotada: ForwardAuth nunca espera por ello.
6. La línea de log de decisión lleva decisión, motivo, host, app id, `tid` y `jti`; nunca el header `Authorization` ni el token.
7. Traefik: en la ruta del portal, un middleware `headers` `strip-authorization` elimina `Authorization` después de ForwardAuth
   (`customRequestHeaders` con valor vacío, como `strip-assertion`), de modo que el secreto nunca llega al proceso del portal,
   a sus logs ni a sus panics. El portal nunca lee `Authorization`. El middleware vive en el namespace `portal` y
   las rutas lo referencian localmente por nombre (`allowCrossNamespace` es false: uno en `platform` rompería la ruta
   del portal). Invariante: todo IngressRoute que coincide con un host del portal lo referencia; el smoke check los lista y
   falla si a alguno le falta. El access log sigue descartando headers (`fields.headers.defaultMode: drop`); esa configuración
   protege el secreto y no debe relajarse.
8. Los preflights de CORS (`OPTIONS` + `Origin` + `Access-Control-Request-Method`) siguen respondiendo 200 sin aserción antes de
   cualquier manejo de tokens; el portal nunca responde CORS.

### Decisiones de implementación

Donde las reglas anteriores dejaron una elección:

- El hecho del portal se lee del Evaluator (`Decision.PortalApp`, establecido siempre que el host es conocido) a través de la caché
  de decisiones bajo un sujeto nil-UUID, solo cuando una solicitud lleva `Authorization`; en un host que no es el del portal el
  header no se vuelve a leer y la ruta de cookie corre como antes. Las reglas de app e iniciativa de la regla 3 vienen de
  esa misma caché (`authorize` para el dueño); la fila del token y el `status` del dueño nunca.
- La búsqueda selecciona la fila por hash y la base de datos calcula `revoked`, `expired` (contra `now()`) y `owner active`
  como flags, en lugar de filtrarlos en el `WHERE`: la misma consulta indexada única, y el log puede distinguir desconocido,
  revocado, vencido y dueño inactivo. La respuesta no puede.
- Varios headers `Authorization`, un valor vacío, otro esquema y `Bearer` sin credenciales responden todos
  `WWW-Authenticate: Bearer` (motivo `token_no_bearer`); un valor Bearer con formato incorrecto (incluidos dos tokens en
  un header) responde `error="invalid_token"` (`token_malformed`), antes de cualquier consulta. Todos los 401 comparten un cuerpo.
- App o iniciativa inactiva (o cualquier otro deny de las reglas del portal) responde 403 problem+json sin challenge;
  se escribe una fila en `access_denials` para ello como con las sesiones (una vez por cache miss de decisión; `user_id` = el dueño del token, que puede ser un robot, y `sid` NULL), de modo que los denials de tokens se pueden consultar como los de sesión (ninguna pantalla del portal lista aún denials por usuario; la salud de integración solo lee `unknown_host`). La verificación de scope es un techo del token, no una regla de acceso, y no registra nada. Un scope que no es
  `read` ni `admin` falla cerrado como `insufficient_scope`.
- Motivos agregados al log de decisión y a `platform_forwardauth_decisions_total`: `token_no_bearer`, `token_malformed`,
  `token_unknown`, `token_revoked`, `token_expired`, `token_owner_inactive`, `token_scope` (el 401 y el 403 de scope usan
  la decisión `unauthenticated` y `deny`); la línea gana `tid` (vacío para sesiones).
- `last_used_at` pasa por `internal/tokenuse` (un conjunto acotado de a lo sumo 10 000 tokens por réplica vaciado una vez por
  minuto por un único escritor, con un vaciado final al apagar; las observaciones más allá del límite se descartan y se contabilizan, ver
  la tabla de métricas). La sentencia bloquea filas con `SKIP LOCKED`, limita el tiempo al reloj de la base de datos y nunca mueve
  la columna hacia atrás; un token cuya fila está bloqueada se mantiene en memoria y se reintenta en el siguiente tick (uno revocado
  simplemente no se escribe). Tanto esto como `lastseen` son usuarios delgados de `internal/touchset`.

### Manifiestos de Traefik e invariantes

`strip-authorization` está en `platform-gitops/base/portal/ingressroute.yaml` junto a la ruta que lo referencia. El
smoke check lista todo IngressRoute que coincide con un host del portal y falla si a alguno le falta el middleware; también lista
los objetos `Ingress` estándar y falla ante cualquier ruta pública sin `Host(`, con `HostRegexp`, o un Ingress sin regla de
host, porque esos podrían coincidir con el host del portal sin ser notados. Una solicitud con un Bearer en una navegación es un 401, nunca
una redirección de login.

### Limitaciones

Un rate limit sobre tokens fallidos por IP de cliente espera a la IP real del cliente (ver [TODO.md](../../../TODO.md): en k3d todo cliente aparece como `10.42.0.1`).
