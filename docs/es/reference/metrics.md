# Observabilidad

> English version: [docs/reference/metrics.md](../../reference/metrics.md)

Todo lo crítico funciona dentro del portal, sin herramienta externa; las salidas estándar alimentan lo que ejecute el adoptante. El portal es un producto adoptado por muchas empresas, así que nada aquí supone un stack de observabilidad específico.

| Capa | Provisto |
|---|---|
| Auditoría | `audit_events`, navegable en el portal y con alcance por iniciativa para los gestores |
| Salud de integración (por app) | Deployments nunca vistos o que ya no se ven (`last_seen_at`); resultado del último probe; apps sin grants o sin gestor; apps deshabilitadas e iniciativas inactivas |
| Salud de integración (plataforma) | Hosts huérfanos: denegaciones `unknown_host` de un host enrutado por Traefik sin deployment |
| Logs | JSON en stdout (ya es así), para cualquier colector |
| Métricas | `/metrics` en formato Prometheus en un listener de métricas dedicado de cada proceso: decisiones por razón, logins por resultado, consultas de JWKS, estado de las llaves, resultados de probe ([catálogo](#métricas)) |
| Dashboards y alertas | Dashboards de Grafana y reglas de alerta opcionales versionados en `platform-gitops/`; el overlay local agrega Prometheus y Grafana |

## Métricas

Ambos procesos (`platform-api` y `portal`) sirven `GET /metrics` en el formato de texto de Prometheus en un **listener de
métricas dedicado** (`PLATFORM_METRICS_ADDR`, `PORTAL_METRICS_ADDR`; ambos obligatorios y en un puerto distinto del de las demás
direcciones del proceso; mismos timeouts de servidor). Una regla para ambos: `/metrics` nunca se sirve en el listener público, el
interno ni el de JWKS, que responden 404 (las pruebas de cada proceso lo verifican), porque una NetworkPolicy no puede filtrar por ruta.
No hay autenticación: la NetworkPolicy debe permitir solo al namespace de monitoreo alcanzar el puerto de métricas, y
ninguna IngressRoute debe enrutar hacia él. Cada proceso usa su propio registry (nunca el global por defecto) con los colectores del runtime de Go y
del proceso, construido con `github.com/prometheus/client_golang` (fijado en `go.mod`).

**Regla de cardinalidad (estricta).** Ninguna etiqueta toma un valor suministrado por un cliente: ni el Host, la ruta, el user agent,
la query, un email o un id de usuario. Las etiquetas toman un valor de un conjunto cerrado (decisión, razón, resultado, clase
de estado), un patrón de ruta de `ServeMux` (`Request.Pattern`, nunca `URL.Path`; una solicitud rechazada antes del router es
`unmatched`) o un texto fijo. El código que registra un evento limita sus cadenas al conjunto cerrado e informa
cualquier otra cosa como `other`, y una prueba por proceso envía miles de solicitudes con hosts, rutas, métodos y
cookies aleatorios y verifica que el número de series no se mueve y que ninguno de esos valores se expone.
`app_id` **no** es una etiqueta en ningún lado: el conjunto de apps crece con cada empresa que adopta el portal, multiplicaría
todas las demás etiquetas, y la vista por app ya existe (línea de log de decisión, `last_seen_at`, estado del probe).

platform-api (`platform_`):

| Métrica | Tipo | Etiquetas |
|---|---|---|
| `platform_forwardauth_decisions_total` | counter | `decision`, `reason` (los valores de la línea de log de decisión) |
| `platform_forwardauth_decision_duration_seconds` | histogram | `decision` |
| `platform_forwardauth_cache_lookups_total` | counter | `cache` (`decision`, `host`, `negative_host`), `result` (`hit`, `miss`) |
| `platform_logins_total` | counter | `outcome` (un login falso, solo desarrollo, cuenta como `success`): `success`, `provider_refused`, `rejected`, `invalid_flow`, `failed`, `provider_unavailable`, `user_disabled`, `rate_limited`, `invalid_request`, `error` |
| `platform_logouts_total` | counter | `via` (`app`, `auth_host`); solo los logouts que revocaron una sesión |
| `platform_jwks_requests_total` | counter | `listener` (`jwks`, `public`), `status_class` |
| `platform_signing_key_loaded` | gauge | ninguna; 1 cuando esta réplica puede firmar |
| `platform_signing_key_active_age_seconds` | gauge | ninguna; ausente mientras no haya un firmante cargado |
| `platform_signing_key_kek_mismatch` | gauge | ninguna; 1 cuando la KEK no coincide con las llaves activa o siguiente |
| `platform_lastseen_pending_hosts` | gauge | ninguna |
| `platform_lastseen_dropped_total` | counter | ninguna; observaciones de `last_seen_at` descartadas porque la cola estaba llena |
| `platform_lastseen_flush_errors_total` | counter | ninguna; vaciados de `last_seen_at` fallidos (el lote se conserva y se reintenta) |
| `platform_token_use_pending_tokens` | gauge | ninguna; tokens de API esperando el siguiente vaciado de `last_used_at` |
| `platform_token_use_dropped_total` | counter | ninguna; observaciones de `last_used_at` descartadas porque la cola estaba llena |
| `platform_token_use_flush_errors_total` | counter | ninguna; vaciados de `last_used_at` fallidos (el lote se conserva y se reintenta) |
| `platform_unknown_host_drops_total` | counter | `reason` (`invalid`, `budget`, `marker`, `queue`) |
| `platform_db_pool_{acquired,idle,total,max}_connections` | gauge | ninguna |
| `platform_db_pool_{acquires,acquires_waited,acquires_canceled}_total`, `platform_db_pool_acquire_wait_seconds_total` | counter | ninguna |

portal (`portal_`):

| Métrica | Tipo | Etiquetas |
|---|---|---|
| `portal_http_requests_total` | counter | `method` (métodos estándar, si no `other`), `route`, `status_class` |
| `portal_http_request_duration_seconds` | histogram | `route` |
| `portal_contention_responses_total` | counter | ninguna |
| `portal_probe_rounds_total` | counter | `result`: `written`, `inconclusive`, `skipped` (otra réplica tiene el bloqueo), `idle` (nada pendiente), `error` |
| `portal_probe_round_duration_seconds` | histogram | `result` |
| `portal_probe_interval_seconds` | gauge | ninguna; el intervalo de probe configurado (`PORTAL_PROBE_INTERVAL`) en segundos, fijado al iniciar, `0` = probe deshabilitado; se usa para condicionar las alertas del probe |
| `portal_probe_last_write_timestamp_seconds` | gauge | ninguna; tiempo Unix (segundos) de la última ronda de probe con resultado `written`; parte del tiempo de inicio del proceso y se queda ahí mientras el probe está deshabilitado |
| `portal_deployments_by_probe_status` | gauge | `status` (`ok`, `unprotected`, `unexpected`, `unreachable`, `never`) |
| `portal_probe_status_count_errors_total` | counter | ninguna |
| `portal_db_pool_*` | como arriba | ninguna |

Notas:

- `portal_deployments_by_probe_status` es un colector en tiempo de scrape: cuenta `deployments` por `last_probe_status`
  (`never` si aún no hay ninguno) con una caché de 30 s y un timeout de 2 s, en cada réplica y corra o no el probe, de modo que
  cualquier réplica informa el estado de la base de datos. No tiene etiqueta de host. Si el conteo falla, el gauge está ausente en
  ese scrape (un valor desactualizado ocultaría el problema), `portal_probe_status_count_errors_total` crece, y el fallo
  se guarda en caché durante el TTL.
- Los contadores de un vector con combinaciones de etiquetas abiertas (decisiones, solicitudes) aparecen con su primer evento;
  los conjuntos cerrados pequeños (logins, logouts, cachés, rondas de probe) parten en cero.
- Las métricas del pool se leen de `pool.Stat()` en el momento del scrape.
- Los fallos de escritura de auditoría no son una métrica: el portal escribe la fila de auditoría en la misma transacción que el cambio, así que un
  fallo es el fallo de la propia solicitud (`portal_http_requests_total` con `5xx`), y platform-api registra en el log
  los fallos de auditoría de sesión de mejor esfuerzo.
- Las razones de decisión agregadas por la [ruta de tokens](forwardauth.md#ruta-de-token) (`token_no_bearer`, `token_malformed`,
  `token_unknown`, `token_revoked`, `token_expired`, `token_owner_inactive`, `token_scope`) son valores de `reason` en
  `platform_forwardauth_decisions_total`; 401 y el 403 de alcance usan la decisión `unauthenticated` y `deny`.

## `last_seen_at`

- platform-api anota cada decisión autorizada (un 200 con aserción, la app del portal incluida; no denegaciones,
  redirecciones, preflights ni el host de autenticación) en un mapa en memoria `host -> latest time`. Solo llegan ahí los hosts que resolvieron a un
  deployment, así que el mapa está acotado por el número de deployments (y limitado a 10 000 entradas).
  Una solicitud nunca escribe en la base de datos.
- Un flusher en segundo plano corre una vez por minuto y escribe todo el mapa con una sola sentencia, como `platform_runtime`;
  como máximo una escritura por minuto por deployment y réplica. Los tiempos vienen de la réplica que atendió la solicitud,
  limitados al reloj de la base de datos y sin mover nunca la columna hacia atrás. Las filas se bloquean en orden de id con
  `SKIP LOCKED`: un vaciado nunca espera a otra réplica ni a la remoción de un deployment, ni hace deadlock con ellas. La sentencia
  devuelve los hosts que omitió (una fila existente bloqueada por otro); permanecen en memoria, con su tiempo salvo que
  una solicitud más reciente los haya tocado, y los escribe el siguiente tick. Los hosts sin deployment y las filas que ya
  tienen un tiempo más reciente están resueltos, no omitidos.
- Un vaciado fallido se registra en el log y su lote vuelve a la memoria (ganan las observaciones más nuevas), para reintentarse en el siguiente
  tick; nunca afecta una decisión. En el apagado, tras detenerse los listeners, el flusher hace un último vaciado
  de mejor esfuerzo acotado a 5 segundos.
- La sentencia no toca `host` ni `app_id`, así que no emite `access_changed`.

## Probe

- El portal (no platform-api) hace probe de cada host de deployment a través del entrypoint público sin cookies:
  `GET https://<host>/` con `Accept: text/html` y `Sec-Fetch-Mode: navigate` (para que ForwardAuth lo clasifique como
  navegación), sin seguir redirecciones, sin conservar cookies, 10 segundos por host, 8 hosts a la vez. Un host protegido
  responde un 302 al login del host de autenticación. El cuerpo de la respuesta nunca se lee ni se registra.
- Cualquier otra cosa significa que la app es alcanzable sin ForwardAuth (por ejemplo una IngressRoute sin el
  middleware), el fallo más peligroso y uno que los logs de platform-api nunca pueden mostrar. Una verificación equivalente corresponde en el
  CI de gitops.
- Clasificación, almacenada en `deployments.last_probe_status` junto con `last_probe_at`:

| Resultado | Significado |
|---|---|
| `ok` | 302, 303 o 307 (las redirecciones temporales que usa ForwardAuth) cuyo `Location` es `https://<auth host>/login...` (host de autenticación desde `PORTAL_ISSUER`; host, puerto incluido, comparado sin distinguir mayúsculas; ruta exactamente `/login`) |
| `unexpected` | Cualquier otra redirección: 301 o 308, otro host o ruta, `http`, credenciales en la URL, un `Location` relativo (se resuelve contra el host del deployment), o una redirección sin `Location` |
| `unprotected` | Toda respuesta que no es una redirección, sea cual sea su estado (2xx, 4xx, 5xx): el host se sirvió sin ForwardAuth. Un 401, 403 o 503 también cuenta: ForwardAuth responde con un 302 a una navegación sin cookies. Una caída de platform-api se vería igual, que es lo que evita la muestra de control de más abajo |
| `unreachable` | DNS, TCP, TLS (incluido un certificado que no verifica) o timeout: no se puede decir nada del host |

Un host que tiene un deployment pero ningún router de Traefik responde un 404 del propio Traefik, y se clasifica también como
`unprotected`: nada en la respuesta distingue ambos casos. En el reporte de salud la combinación `never_seen`
más `unprotected` apunta a una ruta faltante o incorrecta (nunca llegó tráfico autorizado); `unprotected` en un host
que sí fue visto significa una ruta sin el middleware de ForwardAuth.

- Todo resultado `unprotected` y `unexpected` registra un WARN con host, id de app y estado HTTP.
- Configuración (`PORTAL_PROBE_INTERVAL`, `PORTAL_PROBE_ADDR`, `PORTAL_PROBE_CA_FILE`, `PORTAL_PROBE_INSECURE_TLS`): [configuration.md](configuration.md#portal).
- Ejecutor único: cada réplica corre el bucle, pero una ronda toma un advisory lock de PostgreSQL a nivel de sesión (try, nunca
  espera) en una conexión dedicada y hace probe solo de los deployments nunca probados o probados por última vez hace más de la mitad de un
  intervalo (reloj de la base de datos). Una réplica que despierta mientras otra tiene el bloqueo, o justo después de que
  terminó, no encuentra nada que hacer. Los resultados se escriben con una sola sentencia (mismas reglas de `SKIP LOCKED` que arriba) y no emiten
  `access_changed`. El bloqueo se libera cuando termina la ronda, o por la base de datos si la conexión muere.
- Muestra de control: cada ronda comienza haciendo probe del primer host (por nombre) de la app del portal (`PORTAL_APP_ID`), que
  se sabe que está detrás de ForwardAuth, esté o no pendiente. Si el resultado del control no es `ok`, la ronda es
  no concluyente: una caída de platform-api o Traefik haría que todos los hosts parecieran `unprotected` o `unreachable`. No se
  almacena nada (los estados anteriores permanecen), un WARN registra el estado y host del control, y el siguiente tick reintenta. Si la
  app del portal no tiene deployment, la ronda se omite de la misma forma, con un WARN. Cuando no hay nada pendiente no se hace probe
  del control. El control se vuelve a probar al final de la ronda, y si ya no es `ok` toda la ronda se
  descarta (WARN, nada almacenado): una caída que comenzó durante la ronda contamina los resultados ya medidos.
  Por lo tanto los resultados se escriben una sola vez, tras esa segunda verificación, y el WARN por host `unprotected` o `unexpected`
  se registra entonces; un proceso terminado a la mitad de una ronda (SIGKILL) pierde esa ronda, que el siguiente tick
  repite.
- Una ronda fija una conexión del pool durante toda su duración (el advisory lock), así que `portal serve` sube el tamaño de su
  pool de conexiones a al menos 8 (se conserva un tamaño configurado mayor).
- El portal necesita egress de red hacia `PORTAL_PROBE_ADDR` por HTTPS. Como la dirección de conexión es fija, el portal
  nunca resuelve los nombres de host que los administradores escriben en `deployments` (sin SSRF ciego a través del probe): solo
  viajan como SNI y `Host`.

## Salud de una app

`GET /api/v1/apps/{id}/health` (acción `app.health`, con alcance a la iniciativa de la app: administradores de plataforma y los
gestores de esa iniciativa; cualquier otro recibe 404). Es una lectura: sin fila de auditoría, sin efecto secundario. La respuesta contiene:

- `app`: la app con sus deployments, cada uno con `last_seen_at`, `last_probe_at` y `last_probe_status` (`null`
  significa nunca; los mismos tres campos aparecen en todo listado de apps);
- `initiative_status`;
- `grant_count`: grants no expirados de la iniciativa, sin importar quién los tenga;
- `effective_manager_grants`: grants `manages_access` que son efectivos según la definición de la regla del último gestor
  (misma expresión SQL);
- `problems`: una lista de `{code, host}` (`host` es `null` para hallazgos sobre la app en su conjunto), primero los hallazgos
  a nivel de app y luego cada deployment en orden de host;
- `checked_at`.

| Código | Alcance | Cuándo |
|---|---|---|
| `app_disabled` | app | `apps.status` es `disabled` |
| `initiative_inactive` | app | la iniciativa es `inactive` |
| `no_grants` | app | `grant_count` es 0 |
| `no_manager` | app | `effective_manager_grants` es 0 |
| `unprotected` | host | último probe `unprotected` |
| `unexpected_redirect` | host | último probe `unexpected` |
| `unreachable` | host | último probe `unreachable` |
| `never_probed` | host | aún no hay resultado de probe (probe habilitado) |
| `probe_stale` | host | `last_probe_at` más antiguo que 3 intervalos de probe (probe habilitado): las rondas no se completan, por ejemplo porque la muestra de control sigue fallando |
| `probe_disabled` | app | el portal corre con `PORTAL_PROBE_INTERVAL=0`; reemplaza a `never_probed` y `probe_stale` (un estado almacenado antes se sigue informando) |
| `never_seen` | host | `last_seen_at` es null |
| `not_seen_recently` | host | `last_seen_at` más antiguo que 24 horas |

`never_seen` y `not_seen_recently` no se informan mientras la app está deshabilitada o su iniciativa inactiva: nadie
puede ser autorizado, así que no se espera tráfico. Los hallazgos del probe siempre se informan.

## Hosts huérfanos

Un host huérfano no pertenece a ninguna app, por lo tanto a ninguna iniciativa, así que no forma parte de la salud de ninguna app.
`GET /api/v1/health/orphan-hosts` (acción `orphan_host.list`, solo administradores de plataforma; los demás reciben 403) lista los
hosts con denegaciones `unknown_host` en los últimos 7 días que ahora no tienen deployment (un host que se agregó después
ya no es huérfano), la denegación más reciente primero, como máximo 100: `{hosts: [{host, count, last_denied_at}]}`. `count`
es el número de denegaciones registradas; platform-api registra como máximo una por host cada 10 minutos por réplica ([ForwardAuth](forwardauth.md#clasificación-de-requests-y-reglas-de-borde)),
así que mide cuánto tiempo se ha golpeado a un host, no cuántas solicitudes recibió. La consulta lee `access_denials`, que
el rol `platform_portal` puede consultar con select; no se mantiene un índice adicional: las filas `unknown_host` están acotadas a 30 por minuto por réplica ([ForwardAuth](forwardauth.md#clasificación-de-requests-y-reglas-de-borde)), así que una semana contiene como máximo unas 300 000 por réplica, y el endpoint es solo para administradores.
