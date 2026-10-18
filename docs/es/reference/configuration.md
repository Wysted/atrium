# Referencia de configuración

> English version: [docs/reference/configuration.md](../../reference/configuration.md)

Cada variable de entorno que lee cada binario, con su valor por defecto, validación y efecto. El código es la fuente de verdad, y un test (`platform/api/cmd/platform-api/configdoc_test.go`) falla cuando un nombre `PLATFORM_`, `PORTAL_` o `HELLO_` entre comillas en un archivo Go que no sea de test bajo `platform/` no tiene fila de tabla aquí, o cuando una fila de tabla nombra una variable que ningún archivo así contiene. Verifica solo nombres, no valores por defecto ni validación.

Convenciones, válidas para cada tabla:

- platform-api, portal y hello recortan los espacios en blanco circundantes y tratan un valor vacío como no definido. portalctl lee `PORTAL_URL` y `PORTAL_API_TOKEN` tal cual, sin recortar.
- Una **duración** es una duración de Go (`45m`, `720h`); un **bool** es uno de `1`, `t`, `T`, `TRUE`, `true`, `True`, `0`, `f`, `F`, `FALSE`, `false`, `False` (`yes` y `on` se rechazan); una **dirección de escucha** es `host:port` (`:8080`).
- `platform-api serve` y `portal serve` verifican al arrancar y cualquier problema detiene el proceso. La sintaxis de las variables, el archivo de secreto de Google y el archivo de CA del probe se validan juntos en una sola pasada que informa todos los problemas a la vez. La KEK, el par TLS del JWKS, `PORTAL_JWKS_CA_FILE` y la base de datos fallan de a uno después. No hay valores por defecto aparte de los listados.
- Las columnas "Manifest" indican lo que define `platform-gitops/base/`, que es una elección de deployment y no un valor por defecto del código.
- Conexión a la base de datos: ambos servidores (y `platform-api migrate`) leen las variables estándar de libpq (`PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGSSLMODE`, `PGPASSFILE`, ...) a través de pgx. `PGSSLMODE` por defecto es `prefer` en pgx; `platform-gitops/base/` define `disable` para la base de datos dentro del clúster, así que con una base de datos externa defínalo explícitamente (por ejemplo `verify-full`). Roles y privilegios: [data-model.md, "Roles de base de datos"](data-model.md#roles-de-base-de-datos).

## Raíces del sistema de Go

`SSL_CERT_FILE` y `SSL_CERT_DIR` las lee el runtime de Go, no atrium. Definen las raíces del sistema que usa platform-api para alcanzar los endpoints de Google, que usa portal para un `PORTAL_JWKS_URL` `https` cuando `PORTAL_JWKS_CA_FILE` no está definido, que usa hello de igual modo sin `HELLO_JWKS_CA_FILE`, y que usa portalctl para `PORTAL_URL`. `PORTAL_PROBE_CA_FILE` se suma a ellas.

## platform-api

Subcomandos: `platform-api migrate` (aplica las migraciones con el rol dueño, luego el bootstrap opcional; lee las variables `PG*`, las dos variables marcadas "solo migrate" más abajo y `PLATFORM_PORTAL_APP_ID`) y `platform-api serve` (todo lo demás). Comportamiento: [forwardauth.md](forwardauth.md), [login.md](login.md), [keys.md](keys.md).

| Variable | Obligatoria | Valor por defecto | Formato y validación | Efecto |
|---|---|---|---|---|
| `PLATFORM_INTERNAL_ADDR` | sí | ninguno | dirección de escucha | Listener que usan Traefik ForwardAuth y los probes de kubelet. Nunca se expone mediante una ruta. Manifest: `:8081` |
| `PLATFORM_PUBLIC_ADDR` | sí | ninguno | dirección de escucha | Listener del host de autenticación (login, callbacks, copia del JWKS). El único al que una ruta puede apuntar. Manifest: `:8080` |
| `PLATFORM_METRICS_ADDR` | sí | ninguno | dirección de escucha | `/metrics` solo, para que una NetworkPolicy pueda filtrar por puerto. Manifest: `:9090` |
| `PLATFORM_JWKS_ADDR` | sí | ninguno | dirección de escucha | Solo el JWKS, el único puerto que alcanzan los namespaces de apps. Manifest: `:8083` |
| `PLATFORM_JWKS_TLS_CERT_FILE` | no | sin definir (HTTP plano, registrado como WARN) | ruta PEM; debe definirse junto con la llave | Cambia el listener del JWKS a HTTPS (TLS 1.3 como mínimo) |
| `PLATFORM_JWKS_TLS_KEY_FILE` | no | sin definir | ruta PEM; debe definirse junto con el certificado | Llave privada del par anterior |
| `PLATFORM_AUTH_HOST` | sí | ninguno | host en minúsculas: letras, dígitos, `.` y `-`; sin esquema, puerto ni ruta | Host del listener de autenticación |
| `PLATFORM_ISSUER` | sí | ninguno | exactamente `https://<PLATFORM_AUTH_HOST>`, nada más (sin puerto, ruta, query, fragmento ni credenciales) | El claim `iss` de cada aserción |
| `PLATFORM_KEK_FILE` | sí | ninguno | ruta del archivo de la KEK (ver abajo) | Key-encryption key que sella las llaves de firma |
| `PLATFORM_IDLE_TIMEOUT` | no | `45m` | duración entre `30m` y `60m` | Timeout de inactividad de la sesión central |
| `PLATFORM_FAKE_LOGIN` | no | `false` | bool | Login de desarrollo que acepta cualquier email. No necesita variables de Google; se ignora con un WARN cuando Google está configurado (los dos nunca coexisten). Manifest: solo `overlays/local` define `true` |
| `PLATFORM_PORTAL_APP_ID` | no | sin definir (sin app del portal) | `app_` más 26 caracteres de Crockford base32 (`^app_[0-9A-HJKMNP-TV-Z]{26}$`) | Id de la app del portal, la única app que cualquier usuario activo puede abrir ([api.md](api.md)). Debe ser una app de la iniciativa `platform` o la excepción queda inactiva (WARN al arrancar). `migrate` también lo lee (con `PLATFORM_PORTAL_HOST`) para crear la app |
| `PLATFORM_PORTAL_HOST` | no (solo `migrate`) | sin definir | nombre de host válido en minúsculas (sin esquema, puerto ni ruta); definido junto con `PLATFORM_PORTAL_APP_ID` o ninguno de los dos | Lo lee `migrate`, no `serve`. Host del portal: `migrate` crea, si faltan, la iniciativa `platform`, la app del portal y un deployment de este host ([install.md](../install.md#9-registrar-el-portal-y-crear-el-primer-administrador-de-la-plataforma)). Nunca modifica filas existentes; un host ya mapeado a otra app, o un app id que vive en otra iniciativa, hace fallar el Job |
| `PLATFORM_BOOTSTRAP_ADMIN_EMAIL` | no (solo `migrate`) | sin definir | dirección de email, en minúsculas | Lo lee `migrate`, no `serve`. Otorga administrador de plataforma al único usuario humano, no falso, con ese email, solo cuando `platform_admins` está vacío (un WARN y éxito si el usuario aún no inició sesión; un error si coinciden varios) |
| `PLATFORM_GOOGLE_CLIENT_ID` | con Google | sin definir | no vacío, sin espacios en blanco | Id de cliente OAuth (no es secreto) |
| `PLATFORM_GOOGLE_CLIENT_SECRET_FILE` | con Google | sin definir | ruta de un archivo de secreto, no vacío, de a lo sumo 4 KiB | Secreto de cliente OAuth |
| `PLATFORM_ALLOWED_HD` | con Google | sin definir | dominios separados por comas, cada uno con un punto, solo caracteres de host, sin entradas vacías; en minúsculas, los duplicados se colapsan | Lista de permitidos de valores `hd` de Google |
| `PLATFORM_ALLOW_PRIVILEGED_DB_LOGIN` | no | `false` | bool | Solo desarrollo. Sin él, el arranque rechaza un rol de login de base de datos que sea superusuario, que eluda RLS, que sea dueño de tablas o que pueda hacer `CREATE` en `public`. Con `true` el rechazo pasa a ser un WARN. Nunca en producción |

Reglas entre variables:

- Las cuatro direcciones de listener deben ser `host:port` y usar puertos distintos (un host comodín entra en conflicto con cualquier host en el mismo puerto). Los archivos TLS no son listeners.
- Modo de login: el id de cliente, el archivo de secreto de cliente y `PLATFORM_ALLOWED_HD` son todos obligatorios en cuanto se define uno. Sin ninguno, se requiere `PLATFORM_FAKE_LOGIN=true`, de lo contrario el arranque falla. Ver [login.md](login.md).
- El usuario de la base de datos es el que indique `PGUSER`; `serve` asume el rol de runtime como parámetro de arranque (ver Roles de base de datos).

### Secretos y archivos (platform-api)

| Archivo | Variable | Formato | Recargado en runtime |
|---|---|---|---|
| KEK | `PLATFORM_KEK_FILE` | exactamente 32 bytes crudos (no hex, no base64, sin salto de línea final), no todos ceros. Generar con `openssl rand 32 > kek` | No, se lee una vez al arrancar |
| Secreto de cliente de Google | `PLATFORM_GOOGLE_CLIENT_SECRET_FILE` | el secreto como texto; se recorta un salto de línea final | No, se lee una vez al arrancar; la rotación requiere un reinicio ([runbook](../runbooks/google-client-secret-rotation.md)) |
| Certificado TLS del JWKS | `PLATFORM_JWKS_TLS_CERT_FILE` | PEM (hoja primero, se permite cadena), de a lo sumo 256 KiB | Sí, comparado cada 30 s; un reemplazo válido se sirve a los nuevos handshakes sin reinicio |
| Llave TLS del JWKS | `PLATFORM_JWKS_TLS_KEY_FILE` | PEM, de a lo sumo 256 KiB | Sí, junto con el certificado. Un par inicial inválido detiene el arranque; un reemplazo inválido conserva el último par bueno y se registra una vez por error distinto |
| pgpass | `PGPASSFILE` | formato libpq: líneas de `host:port:database:user:password`, se permiten comodines (`*`) | No. pgx lo lee una vez al parsear la configuración del pool, y solo si no hay una contraseña definida de otro modo; las nuevas conexiones no lo releen |

Política de archivos para la KEK, el secreto de Google y la llave TLS: un archivo regular, modo `0640` o más estricto (la escritura de grupo y cualquier acceso para otros se rechazan, no solo se advierten). En Kubernetes monte el Secret con `defaultMode: 0440` y un `fsGroup` del pod. El certificado TLS es público y no tiene verificación de permisos. Ni la KEK ni ningún valor de secreto se registra en logs.

Montado por `platform-gitops/base/`:

| Secret (llave) | Ruta de montaje | Usado por |
|---|---|---|
| `platform-kek` (`kek`) | `/etc/platform/kek/kek` | platform-api, mediante `PLATFORM_KEK_FILE` |
| `platform-api-pgpass` (`pgpass`) | `/etc/platform/pgpass/pgpass` | platform-api, mediante `PGPASSFILE` (rol de login `platform_api`) |
| `platform-owner-pgpass` (`pgpass`) | `/etc/platform/pgpass/pgpass` | el Job de migrate, mediante `PGPASSFILE` (rol de login `platform_owner`) |

Agregados por overlays en este repositorio (los nombres son ejemplos): `platform-api-jwks-tls` (`tls.crt`, `tls.key`) para el par TLS del JWKS, y `platform-google-client-secret` (`secret`) más un ConfigMap con el id de cliente para el login con Google.

## portal

Subcomando: `portal serve`. Comportamiento: [api.md](api.md), [metrics.md](metrics.md).

| Variable | Obligatoria | Valor por defecto | Formato y validación | Efecto |
|---|---|---|---|---|
| `PORTAL_PUBLIC_ADDR` | sí | ninguno | dirección de escucha | Listener de la ruta del portal. Manifest: `:8080` |
| `PORTAL_INTERNAL_ADDR` | sí | ninguno | dirección de escucha | Listener de probes. Manifest: `:8081` |
| `PORTAL_METRICS_ADDR` | sí | ninguno | dirección de escucha | `/metrics` solo. Manifest: `:9090` |
| `PORTAL_APP_ID` | sí | ninguno | `^app_[0-9A-HJKMNP-TV-Z]{26}$` | El app id propio del portal: el `aud` que deben llevar sus aserciones. Mismo valor que `PLATFORM_PORTAL_APP_ID`. Manifest: un placeholder que el overlay reemplaza |
| `PORTAL_ISSUER` | sí | ninguno | `https://<auth host>[:port]` sin nada más; un puerto debe ser decimal de 1 a 65535 | Coincidencia exacta de `iss`, y el origen de autenticación usado para CSP y las verificaciones del probe, por ejemplo `https://auth.apps.example.com`. Manifest: `https://auth.invalid`, reemplazado por el overlay |
| `PORTAL_JWKS_URL` | sí | ninguno | URL `http` o `https` absoluta, sin credenciales, query ni fragmento | URL dentro del clúster del listener JWKS de platform-api. `http` plano se acepta con un WARN: las llaves descansan entonces en la NetworkPolicy. Manifest: `http://platform-api-jwks.platform.svc.cluster.local:8083/.well-known/jwks.json` |
| `PORTAL_JWKS_CA_FILE` | no | sin definir (raíces del sistema) | ruta PEM; solo válida con un `PORTAL_JWKS_URL` `https` | Bundle de CA para un JWKS firmado por una CA privada. Cuando se define **reemplaza** las raíces del sistema para el cliente del JWKS (el almacén de confianza del SDK) |
| `PORTAL_ALLOW_PRIVILEGED_DB_LOGIN` | no | `false` | bool | La misma válvula de escape solo para desarrollo que en platform-api, para el rol `platform_portal` |
| `PORTAL_TOKEN_MAX_TTL_PERSONAL` | no | `720h` | duración mayor que cero | Tope y vida por defecto de un token de API personal |
| `PORTAL_TOKEN_MAX_TTL_ROBOT` | no | `2160h` | duración mayor que cero | Tope y vida por defecto de un token de API de robot |
| `PORTAL_PROBE_INTERVAL` | no | `5m` | duración: `0` deshabilita el probe, de lo contrario `1m` a `24h` | Tiempo entre rondas del probe (cada espera es el intervalo más o menos 10 por ciento). El overlay de este repositorio define `1m` |
| `PORTAL_PROBE_ADDR` | cuando el probe está habilitado | ninguno | `host:port`, host no vacío, puerto 1 a 65535 | Adónde se conecta el probe, sin importar cuál sea el host del deployment (el servicio de ingress). El nombre de servidor TLS y `Host` siguen siendo el host del deployment. Manifest: `traefik.traefik.svc.cluster.local:443`. No se lee cuando `PORTAL_PROBE_INTERVAL=0` |
| `PORTAL_PROBE_CA_FILE` | no | sin definir | ruta PEM con al menos un certificado, de a lo sumo 1 MiB | Bundle de CA **agregado a** las raíces del sistema para verificar el certificado del ingress |
| `PORTAL_PROBE_INSECURE_TLS` | no | `false` | bool | Solo desarrollo: omite la verificación del certificado del probe; se registra a nivel Error en cada arranque |

Reglas entre variables:

- Las tres direcciones de listener deben usar puertos distintos.
- Con `PORTAL_PROBE_INTERVAL=0` las demás variables `PORTAL_PROBE_*` no se leen ni se validan.
- El pool de la base de datos se eleva a al menos 8 conexiones, sin importar lo que elegiría `pgx`; el tamaño no tiene variable.

### Secretos y archivos (portal)

| Archivo | Variable | Formato | Recargado en runtime |
|---|---|---|---|
| pgpass | `PGPASSFILE` | formato libpq, como arriba (rol de login `portal_app`, miembro solo de `platform_portal`) | No, igual que arriba |
| Bundle de CA del JWKS | `PORTAL_JWKS_CA_FILE` | certificados PEM; un archivo sin un certificado válido detiene el arranque | No, se lee una vez al arrancar |
| Bundle de CA del probe | `PORTAL_PROBE_CA_FILE` | certificados PEM | No, se lee una vez al arrancar |

Los bundles de CA son material público: sin verificación de permisos (un montaje de ConfigMap está bien).

Montado por `platform-gitops/base/`: `portal-pgpass` (`pgpass`) en `/etc/portal/pgpass/pgpass`. El overlay de este repositorio también monta el ConfigMap `platform-jwks-ca` (`ca.crt`) en `/etc/portal/jwks-ca/` para `PORTAL_JWKS_CA_FILE`.

## portalctl

Cliente CLI de la API del portal (`portalctl grant|revoke <email|robot:name> <initiative-key> <role-key>`). Ambas variables son obligatorias; el token nunca es un flag.

| Variable | Obligatoria | Valor por defecto | Formato y validación | Efecto |
|---|---|---|---|---|
| `PORTAL_URL` | sí | ninguno | `https://host[:port]` con un `/` final opcional; sin credenciales, ruta, query ni fragmento | El host del portal; cualquier otro esquema se rechaza y las redirecciones nunca se siguen |
| `PORTAL_API_TOKEN` | sí | ninguno | un token de API personal o de robot | Se envía como token bearer solo a `PORTAL_URL` |

Un valor incorrecto sale con estado 2 y un mensaje de uso. La CA privada del host del portal es de confianza a través de las raíces del sistema de Go (`SSL_CERT_FILE`, `SSL_CERT_DIR`).

## hello (app de ejemplo)

`platform/examples/hello`, la app mínima detrás de la plataforma ([README](../repo/platform/examples/hello/README.md)). Muestra lo mínimo que una app necesita para validar aserciones.

| Variable | Obligatoria | Valor por defecto | Formato y validación | Efecto |
|---|---|---|---|---|
| `HELLO_LISTEN_ADDR` | sí | ninguno | dirección de escucha, por ejemplo `:8080` | Dónde escucha la app |
| `HELLO_APP_ID` | sí | ninguno | el app id estable | El `aud` que debe llevar la aserción |
| `HELLO_ISSUER` | sí | ninguno | el emisor de la plataforma, por ejemplo `https://auth.apps.example.com` | Coincidencia exacta de `iss` |
| `HELLO_JWKS_URL` | sí | ninguno | URL del JWKS dentro del clúster (se recomienda https) | Dónde el verificador obtiene las llaves; `http` plano registra un WARN |
| `HELLO_JWKS_CA_FILE` | no | sin definir (raíces del sistema) | ruta PEM; solo válida con un `HELLO_JWKS_URL` `https` | Bundle de CA para un JWKS firmado por una CA privada; reemplaza las raíces del sistema. Se lee una vez al arrancar |

Las variables obligatorias faltantes se informan juntas.
