# platform-gitops

> English version: [platform-gitops/README.md](../../../../platform-gitops/README.md)

Manifiestos Kustomize de la plataforma y del cluster k3d local `portal`, para contribuidores. Todo se ejecuta mediante `./tk`. Para instalar atrium en tu propio cluster ver [docs/install.md](../../install.md); para operarlo, [docs/operate.md](../../operate.md).

```
base/namespaces    platform-db, platform, portal, hello-a, hello-b (Pod Security restricted); `monitoring` lives in overlays/local/namespaces
base/postgres      StatefulSet postgres:18.6 (digest = compose.yaml), initdb.sh, NetworkPolicy
base/migrate       Job `platform-api migrate` as platform_owner
base/platform-api  Deployment x2, Services internal:8081 / jwks:8083 / public:8080, Middlewares, auth-host IngressRoute, NetworkPolicy
base/portal        Deployment x2 (cmd/portal), Service, IngressRoute (passHostHeader), NetworkPolicies
base/hello         reference app (one instance per namespace via overlay)
base/monitoring    NetworkPolicies of the monitoring namespace (default deny; Prometheus, Grafana)
monitoring/        alert rules (plain + PrometheusRule), PodMonitors, Grafana dashboard, promtool tests (adopters: docs/operate.md)
overlays/local     domain/app-id patches, scripts, seed, traefik values (fake login), monitoring stack values
overlays/local-google  real Google login on top of overlays/local: image tag, client secret and id, env, egress policy, admin bootstrap
```

## Orden: migrate antes de platform-api

`platform-api` y `portal` se niegan a arrancar cuando los roles de la base de datos se desvían de lo que el binario espera, por lo que el Job `migrate` debe completarse ANTES de que los nuevos Deployments se desplieguen. `base/` no tiene un mecanismo de orden (el initContainer `wait-for-db` solo espera a `pg_isready`); `overlays/local/up.sh` lo hace con `kubectl wait --for=condition=complete job/migrate` antes de aplicar platform-api. Para el orden con GitOps ver [docs/operate.md](../../operate.md#actualización) y [docs/install.md](../../install.md#4-base-de-datos-y-roles).

## Scripts (overlays/local)

| Script | Propósito |
|---|---|
| `up.sh` | secretos (`.tools/secrets/k3d-*`, generados una vez), importación de imágenes (`BUILD=1` reconstruye las tres imágenes), aplica en orden (namespaces, DB, roles del portal, Job migrate, seed, platform-api, hello-a/b, portal, monitoring), `down-spike.sh`, helm upgrade de Traefik |
| `roles.sh` / `roles.sql` | `platform_portal` (NOLOGIN) y el rol de login `portal_app` (miembro solo de `platform_portal`) como el superusuario de bootstrap; idempotente; `base/postgres/initdb.sh` ahora los crea en un volumen nuevo, por lo que esto es para volúmenes inicializados antes de eso |
| `seed.sh` / `seed.sql` | datos de desarrollo como platform_owner; idempotente, también restaura los grants revocados por el smoke |
| `jwks-tls.sh` | PKI local para el listener JWKS (CA ECDSA P-256 de 10 años + hoja de 90 días, SANs de `platform-api-jwks`), Secret `platform-api-jwks-tls` y ConfigMap `platform-jwks-ca` por namespace de app; idempotente, vuelve a emitir la hoja 30 días antes de que expire; lo invoca `up.sh` antes de los rollouts |
| `smoke.sh` | chequeos de punta a punta con curl desde el host (confiando en `.tools/secrets/local-ca.crt` cuando existe); deja el cluster como lo encontró |
| `down-spike.sh` | elimina spike-authz / spike-app1 / spike-app2 (los manifiestos quedan en `spikes/`) |
| `down-monitoring.sh` | elimina PodMonitors/reglas, el release de Helm y el namespace `monitoring` (`DROP_CRDS=1` también las CRDs del Operator); la plataforma sigue corriendo |

El orden importa: los Middlewares deben existir antes de actualizar Traefik con `traefik/entrypoint-values.yaml`.
Rollback del edge: `./tk helm upgrade traefik traefik/traefik --version 41.7.1 -n traefik -f local/traefik-values.yaml`.

## Seed

`demo` (viewer, editor) es dueño de hello-a; `other` (viewer) es dueño de hello-b. alice: viewer directo en ambas. bob: solo a través del grupo
`demo-editors` (editor en demo): hello-a sí, hello-b no (los grants nunca cruzan iniciativas). carol: sin grants.

La iniciativa reservada `platform` es dueña de la app del portal (id fijo `app_01HQ3PD7RTA5M2XKWV9N8B4CYE`, host
`portal.apps.wysted.dev`); el mismo id es `PORTAL_APP_ID` (overlays/local/portal) y `PLATFORM_PORTAL_APP_ID` (overlays/local/platform).
`admin@example.com` (login falso) es ADMIN DE LA PLATAFORMA para que el smoke pueda ejercitar la API de administración: solo desarrollo, vive en
el `seed.sql` del overlay local, nunca en base.

## Portal (base/portal)

- Namespace `portal`, etiquetado `atrium.wysted.dev/app-namespace=true` como cualquier namespace de app: el listener JWKS de platform-api
  (8083) ya lo acepta; el listener internal (8081) admite solo a Traefik. `platform-db` también lo acepta (`allow-ingress-platform` selecciona `platform` y `portal`).
- Base de datos: rol de login `portal_app`, miembro solo de `platform_portal`; contraseña en `.tools/secrets/k3d-pg-portal-password`, Secret pgpass
  `portal/portal-pgpass` (`fsGroup` + `defaultMode: 0440`, como platform-api). El rol lo crea `base/postgres/initdb.sh` (clave `portal-password` de `postgres-bootstrap`; `roles.sh` cubre volúmenes antiguos), no las
  migraciones: `platform_owner` no tiene CREATEROLE y la migración 0009 solo crea `platform_portal` si falta.
- Probe: `PORTAL_PROBE_ADDR=traefik.traefik.svc.cluster.local:443`. Con el wildcard de Let's Encrypt las
  raíces del sistema lo verifican: sin `PORTAL_PROBE_CA_FILE` ni `PORTAL_PROBE_INSECURE_TLS`. En modo CA local (`local/certs-up.sh` sin token de Cloudflare, ver CONTRIBUTING.md) `up.sh` aplica en su lugar `overlays/local/portal-local-ca`: ConfigMap `portal/local-tls-ca` desde `.tools/secrets/local-ca.crt`, montado y configurado como `PORTAL_PROBE_CA_FILE` (se agrega a las raíces del sistema). Intervalo local de 1m (`PORTAL_PROBE_INTERVAL`).
- NetworkPolicies (default deny en ambos sentidos): ingress solo desde Traefik al 8080 (los probes del kubelet en 8081 vienen del nodo y
  nunca se filtran); egress a DNS, PostgreSQL 5432, platform-api 8083 (JWKS) y los pods de Traefik en 8443. Las políticas ven el puerto del POD: el
  puerto 443 del Service de Traefik es `websecure` = 8443. El puerto de métricas 9090 está abierto solo a los pods de Prometheus del namespace
  `monitoring` (`allow-ingress-metrics`, misma regla en `base/platform-api`). El listener de métricas no tiene Service; el listener internal (8081) es alcanzable solo por Traefik.
- IngressRoute: `Host()` exacto, `passHostHeader: true`; ForwardAuth y strip-assertion vienen del entrypoint.

## Notas sobre el cluster local

- Access log: `accessLog.fields.queryParameters.defaultMode: drop` (Traefik >= 3.7.3) elimina toda la query de `RequestPath`, por lo que el `code`, `bind` y `rd` del login nunca se registran. Contrapartida: ninguna query en los access logs (también oculta parámetros benignos). Los headers (Cookie, Set-Cookie, Location) se descartan por defecto; mantenlos descartados explícitamente (`accessLog.fields.headers.defaultMode: drop`, como hace `local/traefik-values.yaml`): `Authorization` ahora lleva tokens de API en el host del portal, y cualquier registro de headers escribiría secretos vivos.
- Tokens de API ([forwardauth](../../reference/forwardauth.md#ruta-de-token)): toda ruta que coincida con un host del portal debe listar el middleware `strip-authorization` del namespace `portal`. La sección 22 del smoke verifica ambos en el cluster local.
- IP del cliente: en k3d Traefik ve `10.42.0.1` (el gateway CNI del nodo) para todos los clientes, porque el puerto del host pasa por el proxy serverlb de k3d y el SNAT de ServiceLB. El rate limit de `/login` (última entrada de `X-Forwarded-For`) se comporta por lo tanto como un único bucket global en local. Se necesita un load balancer real que preserve la dirección de origen para tener límites por cliente.
- Smoke y el rate limit de `/login`: todos los logins de una corrida (y de corridas consecutivas) comparten un bucket por réplica de platform-api (burst 30, un token cada 2 s) porque Traefik ve a todos los clientes como `10.42.0.1`. El smoke nunca sube el límite: `login()` espera un 429 usando `Retry-After` (reintentos acotados). Al final `smoke.sh` lista cada chequeo fallido con su sección.
- platform-api y portal tienen un `preStop` con sleep nativo (5 s, las imágenes distroless no tienen binario `sleep`): las reglas del Service llegan a Traefik después de SIGTERM, y sin él una réplica eliminada respondía "connection refused" a ForwardAuth (un 500 de Traefik visto una vez en la sección 12 del smoke). `terminationGracePeriodSeconds` 30 cubre 5 s + el presupuesto de shutdown de 20 s.
- Un pod nuevo es rechazado durante 1-2 s cuando se conecta a un destino protegido por una NetworkPolicy de ingress (kube-router todavía no la sincronizó en el conjunto de permitidos): los pods que hablan con PostgreSQL esperan con un initContainer (`pg_isready`).

## Monitoreo

Prometheus y Grafana para la plataforma, en el namespace `monitoring` del cluster local, más los artefactos que un adoptante
lleva a su propio stack ([docs/operate.md](../../operate.md#monitoreo)). El catálogo de métricas y la regla de cardinalidad (sin valores de label provistos por el cliente, sin `app_id`) están en
`docs/reference/metrics.md` ("Metrics"); lee la regla antes de agregar una métrica o una consulta de dashboard.

**Stack local.** `up.sh` instala `kube-prometheus-stack` 92.10.0 (Prometheus Operator v0.94.1, Prometheus v3.15.0, Grafana
13.2.4) como release `monitoring`, con valores en `overlays/local/monitoring/values.yaml`. Elegido sobre los charts simples de prometheus +
grafana porque así PodMonitor y PrometheusRule (lo que aplican los adoptantes con el Operator) pueden probarse de verdad; el
costo es un pod de operator (~20 MiB). Todo lo demás está desactivado: Alertmanager, node-exporter, kube-state-metrics, scrapes de kubelet y
control-plane, reglas por defecto, el admission webhook, sidecars de Grafana. Prometheus guarda 2 días en un emptyDir (descartable). Endurecimiento: imágenes fijadas por tag y digest, Prometheus recarga por
SIGHUP (`reloadStrategy: ProcessSignal`, por lo que `/-/reload` y `/-/quit` están desactivados), Grafana no monta token de ServiceAccount, el
namespace `monitoring` (Pod Security restricted) lo crea el overlay local, no `base/`.

**Riesgo local aceptado: RBAC del operator.** El chart instala el Prometheus Operator con un ClusterRole que puede get/list/watch
Secrets en todo el cluster (entre ellos la KEK de la plataforma) y eliminar Pods; el chart no tiene opción para limitarlo a un namespace
(`prometheusOperator.namespaces` solo reduce los namespaces que el operator reconcilia, no reduce el ClusterRole).
Aceptable para un cluster descartable; en producción usa un RBAC que tu equipo de seguridad revise o un Prometheus sin operator (archivo de
reglas simple). El ClusterRole del propio Prometheus es de solo lectura (pods, services, endpoints, métricas de nodes).
Alertmanager está desactivado y sin receptores: las alertas se evalúan y son visibles en Prometheus (`/alerts`) y Grafana, y no van
a ninguna parte. `down-monitoring.sh` lo desmonta.

**Acceso a Grafana.** En el día a día, Grafana es una app del portal detrás de ForwardAuth: https://grafana.apps.wysted.dev (sección
"Grafana detrás de ForwardAuth" más abajo). El port-forward de abajo es la vía de emergencia (la autenticación basic está desactivada por defecto, ver allí). El toolbox corre en un contenedor, por lo que `./tk` puede publicar el puerto reenviado en el
loopback del host con `TK_PUBLISH`; deja el comando corriendo y abre http://127.0.0.1:3000 (usuario `admin`):

```
TK_PUBLISH=127.0.0.1:3000:3000 ./tk kubectl -n monitoring port-forward --address 0.0.0.0 deploy/monitoring-grafana 3000:3000
TK_PUBLISH=127.0.0.1:9090:9090 ./tk kubectl -n monitoring port-forward --address 0.0.0.0 svc/monitoring-prometheus 9090:9090   # Prometheus
```

Mientras un port-forward corre con `--address 0.0.0.0`, el puerto reenviado también es alcanzable desde cualquier contenedor en `portal-net`
(el toolbox, los nodos k3d); detenlo cuando termines. `TK_PUBLISH` solo acepta `127.0.0.1:<port>:<port>` (exit 2 en caso contrario).

La contraseña de admin se genera una vez en `.tools/secrets/k3d-grafana-admin-password` (ignorado por git, cargado como Secret
`monitoring/grafana-admin`); léela en tu propia terminal cuando inicies sesión, nunca la pegues en ningún lado. El dashboard "Portal platform"
se provisiona de solo lectura desde `monitoring/dashboards/portal-platform.json` (carpeta "Portal"). TODO.md registra poner Grafana
detrás de ForwardAuth.

**NetworkPolicies.** `monitoring` es default deny en ambos sentidos. Prometheus: ingress desde Grafana (9090) y desde sí mismo; egress solo
al puerto de métricas 9090 de los pods de platform-api y portal, a sí mismo, DNS y la API de Kubernetes (`up.sh` renderiza
`overlays/local/monitoring/networkpolicy-apiserver.tpl.yaml` con un /32 por endpoint del Service `kubernetes`, puerto 6443). Grafana: egress a Prometheus 9090, DNS y (overlay local) el JWKS 8083; ingress solo desde Traefik en 3000 (sin sidecars, por lo que nunca necesita la API). Del
otro lado, `allow-ingress-metrics` en `platform` y `portal` admite solo pods etiquetados `app.kubernetes.io/name=prometheus` del
namespace `monitoring` al puerto 9090; la etiqueta de namespace de app NO lo concede. Cualquier cosa que pueda correr un pod con esa etiqueta en
`monitoring` se une al conjunto: limita el namespace al stack.

**Artefactos para adoptantes (`monitoring/`)** (archivos, supuestos, notas de alertas): [docs/operate.md](../../operate.md#monitoreo). Hechos locales que difieren:
- Aquí Prometheus selecciona todo PodMonitor, ServiceMonitor, ScrapeConfig, Probe y PrometheusRule (selectores `{}`) de los namespaces que el operator reconcilia (`monitoring`, `platform`, `portal`).
- Latencia de recarga: con `reloadStrategy: ProcessSignal` el primer chequeo de recarga del config-reloader tras el arranque de un pod de Prometheus puede agotar el tiempo (~3 min) antes de empezar a observar los archivos de reglas, por lo que las reglas aplicadas justo después de un reinicio cargan tarde; los cambios posteriores tardan ~40 s. La alternativa es la recarga HTTP, que necesita `--web.enable-lifecycle` (abre `/-/quit` y `/-/reload`).
- `monitoring/test-rules.sh` también lo ejecuta `local/lint.sh rules`.

## TLS interno para el JWKS (overlay local)

`base/` mantiene el listener JWKS en HTTP plano (`:8083`, URLs `http://`). `overlays/local` activa TLS solo con patches (sin cambios en base). Adoptantes: [docs/operate.md](../../operate.md#tls-interno-para-el-jwks).

| Dónde | Patch |
|---|---|
| platform-api | `PLATFORM_JWKS_TLS_CERT_FILE=/etc/platform/jwks-tls/tls.crt`, `PLATFORM_JWKS_TLS_KEY_FILE=.../tls.key`, Secret `platform-api-jwks-tls` montado con `defaultMode: 0440` (la llave debe ser 0640 o más estricta; `fsGroup` ya deja que el usuario no root la lea, como la KEK). El listener entonces sirve solo HTTPS, TLS 1.3 como mínimo, y recarga el par cada 30 s |
| portal, hello-a/b | `*_JWKS_URL=https://platform-api-jwks.platform.svc.cluster.local:8083/...`, `*_JWKS_CA_FILE=/etc/<app>/jwks-ca/ca.crt` desde el ConfigMap `platform-jwks-ca` |

CA local: `overlays/local/jwks-tls.sh` (lo ejecuta `up.sh`) mantiene `k3d-jwks-ca.*` y `k3d-jwks-tls.*` en `.tools/secrets/` (ignorado por git). La llave de la CA
nunca llega al cluster. Rotación: elimina `k3d-jwks-tls.*` y vuelve a ejecutar el script (misma CA, hoja nueva); el kubelet sincroniza el volumen del Secret
en aproximadamente un minuto y el listener cambia el par en 30 s, sin reinicio ni cambio en los verificadores. La rotación de la hoja no necesita
reinicio (recarga cada 30 s; ~1 min con la sincronización del volumen del Secret). La rotación de la CA sí: los verificadores cargan la CA una sola vez al arrancar, por lo que la nueva CA
debe distribuirse primero y los verificadores reiniciarse antes de que se sirva la hoja nueva; el script lo hace en ese orden (ConfigMaps ->
`rollout restart` de portal, hello-a, hello-b + espera -> Secret). Con cert-manager/trust-manager, agrupa la CA vieja y la nueva durante la transición.
El script renueva la CA cuando quedan menos de 150 días (hoja 90 + ventana de renovación 30 + margen 30), por lo que ninguna hoja sobrevive a su CA.

Cluster local:

- La sección `16b` del smoke verifica el SAN, la verificación de la CA (y su falla sin la CA), solo-TLS y TLS 1.3.
- `overlays/local-google` se construye sobre `overlays/local/platform`, por lo que hereda los patches de TLS (las apps conservan el ConfigMap de la CA de `up.sh`).

## Grafana detrás de ForwardAuth (overlay local)

Grafana está registrada como una app del modelo del portal y se alcanza solo a través de Traefik; autentica el request con el
header `Platform-Assertion` que ForwardAuth inyecta. Nada en `base/` cambia.

| Pieza | Dónde |
|---|---|
| Autorización | `overlays/local/seed.sql`: iniciativa `observability` (roles `viewer`, `editor`, `admin`, ninguno con `manages_access`), app `grafana` id `app_01J9Z3K7WQ4M8R2T5XV6N0B1HD`, host `grafana.apps.wysted.dev`; `admin@example.com` tiene `admin`, alice `viewer`, bob solo `auditor` (un rol que Grafana no mapea: ForwardAuth lo deja pasar, Grafana lo rechaza), carol nada |
| `[auth.jwt]` de Grafana | `overlays/local/monitoring/values.yaml`: `header_name: Platform-Assertion`, `jwk_set_url` = el JWKS HTTPS, `tls_client_ca` = el ConfigMap `platform-jwks-ca` (creado en `monitoring` por `jwks-tls.sh`, montado en `/etc/grafana/jwks-ca`), `expect_claims` `iss` + `aud` (el id de la app, nunca el host), `username_claim: sub` (sin `email_claim`: la identidad es solo el `sub` UUID, el email nunca coincide con un usuario de Grafana; el admin integrado tiene `admin_email: break-glass@invalid`), `cache_ttl: 5m`, `auto_sign_up`, `role_attribute_strict: true`, `url_login: false` |
| Roles | `role_attribute_path` toma el claim `roles` y devuelve un string, gana el más alto: `admin` -> Admin, `editor` -> Editor, `viewer` -> Viewer; cualquier otro rol (o ninguno) rechaza el login. Solo rol de org: `allow_assign_grafana_admin` está desactivado, nadie se vuelve admin del servidor Grafana a través del portal |
| Ruta | `overlays/local/monitoring/grafana-ingressroute.yaml`: `Host(grafana.apps.wysted.dev)` en `websecure` (ForwardAuth y strip-assertion son a nivel de entrypoint) -> Service `monitoring-grafana:80` |
| NetworkPolicies | `overlays/local/monitoring/networkpolicy-grafana.yaml`: ingress a Grafana :3000 solo desde Traefik; egress a platform-api :8083. `overlays/local/platform/networkpolicy-jwks-grafana.yaml`: platform-api admite :8083 desde los pods de Grafana (el namespace `monitoring` NO está etiquetado como namespace de app) |
| Login local desactivado | `[auth] disable_login_form`, `disable_signout_menu`, `[auth.basic] enabled=false`, anónimo desactivado, `root_url` = la URL pública, `cookie_secure` |

Propiedades que conviene conocer: Grafana ignora el `typ` JOSE (`platform-assertion+jwt`, ver [aserción](../../reference/assertion.md)) y no puede exigirlo, lo cual es
aceptable porque la llave de firma de la plataforma firma solo `Platform-Assertion`; la tolerancia de reloj de 60 s es fija (go-jose); el header se
verifica en cada request, por lo que la revocación surte efecto en el siguiente request (ForwardAuth responde 403; no hay sesión del lado de Grafana
que la sobreviva). `cache_ttl` (5m) solo acota cuánto tarda en verse una llave JWKS rotada: la plataforma ([llaves](../../reference/keys.md)) publica la llave `next` al
menos 1 h antes de firmar y conserva la retirada 1 h, y la ventana de emergencia es de 5 min (`docs/reference/keys.md`, igual que el `max-age` del JWKS),
por lo que 5m nunca rechaza un token legítimo. Grafana no tiene claim de nombre visible en `[auth.jwt]`, por lo que los usuarios aparecen con su login UUID y
un email vacío. Un `Platform-Assertion` enviado por el cliente nunca llega a Grafana: strip-assertion lo elimina y ForwardAuth vuelve a emitir uno
genuino (probado en el host de autenticación y en hello-a, sección 7 del smoke; el smoke 19b verifica las consecuencias del lado de Grafana: una aserción
emitida para otra app es rechazada por `aud`, y un rol sin mapeo por `role_attribute_strict`).

**Admin de emergencia (break-glass).** Con el formulario y la autenticación basic desactivados no hay vía por contraseña. Si la plataforma está caída y necesitas Grafana,
vuelve a activar el login local por el tiempo que lo necesites y accede a Grafana por port-forward (ver "Acceso a Grafana") con las credenciales
de admin de `.tools/secrets/k3d-grafana-admin-password`. `GF_AUTH_DISABLE_LOGIN_FORM=false` devuelve el formulario de la UI,
`GF_AUTH_BASIC_ENABLED=true` solo se necesita para llamadas a la API con autenticación basic. ADVERTENCIA: mientras esto esté activo, el formulario de login está abierto en
la misma Grafana que Traefik sirve en `grafana.apps.wysted.dev`, por lo que cualquier usuario de la plataforma con sesión iniciada y acceso a ese host puede probar la
contraseña de admin. Mantén la ventana corta, con un comando para cada sentido (un solo `set env` despliega el pod una vez; la reversión lo despliega otra vez, y la
base de datos de Grafana es un emptyDir, por lo que las sesiones creadas en la ventana se pierden), y opcionalmente saca la ruta de Traefik durante ese
tiempo (la IngressRoute es solo del overlay local y `kubectl apply -k` la restaura):

```
./tk kubectl -n monitoring delete ingressroute grafana        # optional: nobody reaches the form through Traefik
./tk kubectl -n monitoring set env deploy/monitoring-grafana -c grafana GF_AUTH_DISABLE_LOGIN_FORM=false GF_AUTH_BASIC_ENABLED=true
# ... port-forward, work ...
./tk kubectl -n monitoring set env deploy/monitoring-grafana -c grafana GF_AUTH_DISABLE_LOGIN_FORM- GF_AUTH_BASIC_ENABLED-   # back to off
./tk kubectl apply -k platform-gitops/overlays/local/monitoring   # restores the IngressRoute (also re-applies the policies)
```

Adoptantes que replican esto con su propia Grafana y PKI: [docs/operate.md](../../operate.md#grafana-detrás-de-forwardauth).

## Login real con Google (overlays/local-google)

Se apila sobre `overlays/local` (que debe haberse desplegado una vez con `local/up.sh`). Para cambiar:

```
BUILD=1 platform-gitops/overlays/local-google/up.sh   # build portal-platform-api:dev-google, import, secret, migrate, rollout
platform-gitops/overlays/local-google/down.sh          # back to the fake login (removes egress policy, client secret and client id)
```

Sin `BUILD=1` el script importa la imagen que ya está en el host e imprime un WARN (nunca falla) cuando esa imagen falta o es más antigua que el último commit que toca `platform/` (docs y `*.md` excluidos, como en `platform/.dockerignore`), o cuando esas fuentes tienen cambios sin commitear: el tag nunca cambia, por lo que de otro modo una imagen desactualizada pasa en silencio.

`local/up.sh` vuelve a aplicar el overlay de login falso: ejecuta `local-google/up.sh` otra vez después. `local/smoke.sh` solo funciona en
modo login falso (POST /login es 405 bajo Google). El client secret se lee de `.tools/secrets/google-client-secret` y se
carga con `--from-file` en el Secret `platform/platform-google-client-secret`, montado con `fsGroup` + `defaultMode: 0440`
(misma política que la KEK). El client id (público) se lee de `.tools/secrets/google-client-id` en el ConfigMap
`platform/platform-google-client-id`; `up.sh` falla si falta cualquiera de los dos archivos. El env se lee al arrancar el contenedor: tras cambiar el id, `./tk kubectl -n platform rollout restart deploy/platform-api`. Env: `PLATFORM_GOOGLE_CLIENT_ID`, `PLATFORM_GOOGLE_CLIENT_SECRET_FILE`, `PLATFORM_ALLOWED_HD=wysted.dev`,
`PLATFORM_FAKE_LOGIN=false` (explícito; `overlays/local` lo pone en `true`, `base/` lo deja sin definir). El tag de la imagen se sobreescribe con el transformer `images` de kustomize (base conserva `:dev`).

El portal no necesita nada de este overlay (solo valida aserciones): `local/up.sh` lo despliega y sigue corriendo; cualquier
usuario activo puede abrirlo (iniciativa reservada `platform`, regla "cualquier usuario activo"). Para usar su API de administración con un usuario real:
`local-google/admin.sh <email>` (el usuario debe haber iniciado sesión una vez; `--revoke` lo quita; herramienta de DESARROLLO, el portal no tiene
endpoint para gestionar admins de la plataforma).

Acceso: los usuarios se crean en su primer login (JIT) sin grants (403 en cada app). Otorga y revoca con `portalctl` ([uso](../../operate.md#portalctl)); compila el binario estático de Linux en el directorio del toolbox ignorado por git:
`cd platform/api && ../../gk env CGO_ENABLED=0 go build -o ../../.tools/portalctl/portalctl ./cmd/portalctl`. Localmente `PORTAL_URL` es `https://portal.apps.wysted.dev`, y
`.tools/portalctl/portalctl grant you@<your-workspace-domain> demo viewer` otorga la iniciativa sembrada `demo`.

Bootstrap: `local-google/admin.sh <email>` crea el primer admin de la plataforma (tras un inicio de sesión); ese admin otorga acceso
en el portal o crea el token para `portalctl`.

Rotación del client secret de Google: [docs/runbooks/google-client-secret-rotation.md](../../runbooks/google-client-secret-rotation.md) (sus comandos del cluster local pasan por `./tk`).

### Trade-off de egress (NetworkPolicy `allow-egress-google`)

El discovery/token/JWKS de Google necesita HTTPS hacia accounts.google.com, oauth2.googleapis.com y www.googleapis.com. La política permite TCP 443 desde los pods de platform-api a `0.0.0.0/0` EXCEPTO 10.0.0.0/8
(pods 10.42.0.0/16, services 10.43.0.0/16), 172.16.0.0/12 (nodo k3d y red Docker 172.28.0.0/16), 192.168.0.0/16,
100.64.0.0/10, 169.254.0.0/16 y 127.0.0.0/8. No hay instalado ni un proxy de egress ni un CNI con soporte de FQDN (flannel + kube-router). La política existe solo en `local-google`;
el Job migrate no está cubierto (solo habla con PostgreSQL). Solo IPv4 (el cluster es single-stack). El trade-off general: [docs/operate.md](../../operate.md#endurecimiento-para-producción-hardening).
