# Instalar atrium en tu clúster

> English version: [docs/install.md](../install.md)

Cómo desplegar atrium en un clúster de Kubernetes que operas: Traefik con ForwardAuth, PostgreSQL, `platform-api` y el portal, con login de Google. El repositorio incluye manifiestos (`platform-gitops/base/`), no un chart de Helm ni imágenes publicadas, así que construyes las imágenes y escribes un pequeño overlay de kustomize.

> Todo lo descrito aquí se probó únicamente en el clúster k3d local del mantenedor. Los pasos que van más allá de lo que hace `platform-gitops/` allí están marcados como **No verificado fuera del clúster local**. Los ejemplos usan `example.com` y `<registry>`.

## 1. Requisitos

| Requisito | Detalle |
|---|---|
| Kubernetes | Un CNI que aplique `NetworkPolicy` (ingress y egress). El modelo de seguridad depende de ello ([arquitectura](architecture.md)). Probado en K3s v1.37 con kube-router |
| Traefik | v3.7.14 (chart de Helm 41.7.1) con el provider `kubernetesCRD` (`traefik.io/v1alpha1` `IngressRoute`, `Middleware`). `accessLog.fields.queryParameters` requiere >= 3.7.3. Debe ejecutarse en el namespace `traefik` con pods etiquetados `app.kubernetes.io/name: traefik` (fijado en las NetworkPolicies) |
| PostgreSQL | 18 o superior (las migraciones usan `uuidv7()`); 18.6 está probado. Solo conexiones directas (`LISTEN`/`NOTIFY`). `base/postgres` es un StatefulSet de una sola instancia, de nivel desarrollo; para producción es preferible tu propio servidor (sección 4) |
| TLS | Un certificado válido para el host de auth, el host del portal y cada host de app. Un wildcard como `*.apps.example.com` es lo más simple; sirve cualquier origen (cert-manager, tu CA, uno comprado) |
| DNS | El host de auth (`auth.apps.example.com`), el host del portal y cada host de app resuelven a Traefik. Un registro wildcard es lo más simple |
| Google | Un dominio de Google Workspace o Cloud Identity y un proyecto de Cloud que aloje el cliente OAuth (sección 5) |
| Registry | Un registry desde el que tu clúster pueda descargar imágenes. No hay imágenes publicadas |
| Herramientas | `kubectl` con kustomize, `docker`, `openssl`, `psql` |

## 2. Construir las imágenes

Desde la raíz del repositorio. El contexto de build es `platform/` (un `go.work` abarca los módulos):

```bash
docker build -f platform/api/Dockerfile        -t <registry>/atrium/platform-api:v1 platform
docker build -f platform/api/Dockerfile.portal -t <registry>/atrium/portal:v1 platform
docker push <registry>/atrium/platform-api:v1
docker push <registry>/atrium/portal:v1
```

La imagen de platform-api también ejecuta el subcomando `migrate`. `base/` usa nombres de marcador (`portal-platform-api:dev`, `portal-portal:dev`) que tu overlay reescribe con un transformer `images:`. `base/` también descarga `postgres:18.6-trixie@sha256:...` para los initContainers `wait-for-db` (solo se usa `pg_isready`); haz un mirror si tu clúster no alcanza Docker Hub.

Para administrar grants (asignación de rol) desde una terminal, construye la CLI: `cd platform/api && go build -o portalctl ./cmd/portalctl` ([uso](operate.md#portalctl)).

## 3. Credenciales

Genera todo primero; la sección siguiente necesita las contraseñas. Los archivos quedan locales, nunca los subas al repositorio.

```bash
umask 077
openssl rand 32 > kek                                    # 32 bytes, binary
for r in admin owner api portal; do openssl rand -hex 32 | tr -d '\n' > "pw-$r"; done
# pgpass: host:port:database:user:password. The host field must equal PGHOST of the Deployments.
H=postgres.platform-db.svc.cluster.local
printf '%s:5432:platform:platform_owner:%s\n' "$H" "$(<pw-owner)"  > pgpass-owner
printf '%s:5432:platform:platform_api:%s\n'   "$H" "$(<pw-api)"    > pgpass-api
printf '%s:5432:platform:portal_app:%s\n'     "$H" "$(<pw-portal)" > pgpass-portal
```

`pw-admin` es solo para la Opción A más abajo (el superusuario de bootstrap de `base/postgres`). Una vez que existan los namespaces (sección 8), crea los Secrets:

```bash
kubectl -n platform create secret generic platform-kek --from-file=kek=kek
kubectl -n platform create secret generic platform-owner-pgpass --from-file=pgpass=pgpass-owner
kubectl -n platform create secret generic platform-api-pgpass   --from-file=pgpass=pgpass-api
kubectl -n portal   create secret generic portal-pgpass         --from-file=pgpass=pgpass-portal
kubectl -n platform create secret generic platform-google-client-secret --from-file=secret=./google-client-secret
# Option A only:
kubectl -n platform-db create secret generic postgres-bootstrap \
  --from-file=admin-password=pw-admin --from-file=owner-password=pw-owner --from-file=api-password=pw-api --from-file=portal-password=pw-portal
```

| Secret | Clave | Montado por |
|---|---|---|
| `platform/platform-kek` | `kek` | platform-api |
| `platform/platform-owner-pgpass` | `pgpass` | solo el Job migrate |
| `platform/platform-api-pgpass` | `pgpass` | platform-api |
| `portal/portal-pgpass` | `pgpass` | portal |
| `platform/platform-google-client-secret` | `secret` | platform-api |
| `platform-db/postgres-bootstrap` | `admin-password`, `owner-password`, `api-password`, `portal-password` | `base/postgres` (Opción A) |

**KEK.** Cifra las llaves de firma almacenadas en PostgreSQL ([llaves](reference/keys.md)). Genérala una sola vez, respáldala fuera del clúster y nunca la regeneres: si cambia, las llaves de firma almacenadas no se pueden descifrar, platform-api informa `KEKMismatch`, falla el readiness y se niega a rotar. Los archivos deben ser regulares y con modo 0640 como máximo; los manifiestos los montan con `defaultMode: 0440` más `fsGroup`. Consulta [TODO.md](../../TODO.md) sobre quién puede leer este Secret. Variables y reglas de archivos: [reference/configuration.md](reference/configuration.md).

## 4. Base de datos y roles

Se requiere PostgreSQL 18 o superior (las migraciones usan `uuidv7()`); 18.6 es la versión probada. Las conexiones deben ser directas (`LISTEN`/`NOTIFY`): sin un proxy de pooling por transacción en medio. La base de datos es `platform`; cinco roles ([privilegios](reference/data-model.md#roles-de-base-de-datos)). Ambos procesos se niegan a iniciar si su rol es superusuario, es dueño de tablas, omite RLS o tiene privilegios más allá del conjunto esperado, así que crea los roles exactamente como se indica abajo.

**Opción A: `base/postgres`** (una sola instancia, nivel desarrollo; `storageClassName: local-path` y 2Gi están fijados, parchéalos).

Crea `postgres-bootstrap` (sección 3). En el primer inicio `initdb.sh` crea `platform_owner`, `platform_runtime`, `platform_api`, `platform_portal`, `portal_app` y la base de datos `platform`, usando las contraseñas owner, api y portal del Secret, que deben ser iguales a las de los archivos pgpass. `initdb.sh` se ejecuta solo sobre un directorio de datos vacío: para un volumen inicializado antes, aplica `overlays/local/roles.sql` una vez (espera la variable de psql `portal_password`; mira la Opción B para la invocación).

**Opción B: tu propio PostgreSQL.** Ejecuta como superusuario. Las contraseñas pasan por variables de psql leídas desde archivos, nunca en una lista de argumentos:

```bash
{
  printf "\\set owner_password '%s'\n"  "$(<pw-owner)"
  printf "\\set api_password '%s'\n"    "$(<pw-api)"
  printf "\\set portal_password '%s'\n" "$(<pw-portal)"
  cat <<'SQL'
CREATE ROLE platform_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD :'owner_password';
CREATE ROLE platform_runtime NOLOGIN;
CREATE ROLE platform_api LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD :'api_password';
GRANT platform_runtime TO platform_api;
CREATE DATABASE platform OWNER platform_owner;
REVOKE ALL ON DATABASE platform FROM PUBLIC;
GRANT CONNECT ON DATABASE platform TO platform_owner, platform_api;
SQL
} | psql -v ON_ERROR_STOP=1 -q -h <db-host> -U <superuser> -d postgres
# then the portal roles, in the new database:
{ printf "\\set portal_password '%s'\n" "$(<pw-portal)"; cat platform-gitops/overlays/local/roles.sql; } |
  psql -v ON_ERROR_STOP=1 -q -h <db-host> -U <superuser> -d platform
```

(El primer bloque es `base/postgres/initdb.sh`; el segundo es `roles.sql`, que crea `platform_portal` y `portal_app`, `GRANT platform_portal TO portal_app` y el grant de `CONNECT`. `platform_owner` no puede crear roles, así que ambos grupos deben existir antes de la primera migración.) Luego parchea `base/` para el servidor externo, **No verificado fuera del clúster local**:

- `PGHOST` en el Job migrate (initContainer `wait-for-db` y contenedor), en los Deployments de platform-api y portal (initContainer y contenedor), y el campo host de los tres archivos pgpass.
- `PGSSLMODE` es `disable` en todos ellos. Para `verify-full` define también `PGSSLROOTCERT` y monta la CA (ConfigMap) en cada pod.
- La NetworkPolicy `allow-egress-dns-db` en `platform` y en `portal` permite egress solo hacia los pods `postgres` del namespace `platform-db`: reemplaza esa regla por la dirección de tu servidor (`ipBlock`) en el puerto 5432. Elimina `base/postgres` y el namespace `platform-db` si no se usan.

**Migraciones.** `base/migrate` es un Job que ejecuta `platform-api migrate` como `platform_owner`. Debe completarse antes de que `platform-api` y `portal` se desplieguen en cada release; `base/` no tiene un mecanismo de ordenamiento: aplícalo a mano en la primera instalación (sección 8); para releases y orden en GitOps consulta [operate.md](operate.md#actualización).

## 5. Cliente OAuth de Google

En Google Cloud (proyecto de tu organización de Workspace):

1. Configura la pantalla de consentimiento de OAuth como **Internal** (las cuentas externas se rechazan, [login](reference/login.md#google-oidc)).
2. Crea un cliente OAuth de tipo *Web application*.
3. URI de redirección autorizada, exactamente una: `https://auth.apps.example.com/oauth2/callback` (`https://` + `PLATFORM_AUTH_HOST` + `/oauth2/callback`).
4. Coloca el secreto del cliente en el archivo usado en la sección 3. El id de cliente no es secreto; va en `PLATFORM_GOOGLE_CLIENT_ID` (sección 7).

`PLATFORM_ALLOWED_HD` es la lista, separada por comas y en minúsculas, de dominios de Workspace (el claim `hd`) autorizados a iniciar sesión. Los dominios secundarios no están probados (los alias no pueden iniciar sesión), consulta la [sección 11](#11-limitaciones-conocidas-antes-de-producción).

## 6. Traefik

ForwardAuth se ejecuta en el entrypoint, así que se aplica a cada ruta sin posibilidad de exclusión. El contrato está en [forwardauth.md](reference/forwardauth.md#configuración-de-traefik); el repositorio lo implementa como sigue.

**Middlewares.** `base/platform-api/middlewares.yaml` (namespace `platform`) define `strip-assertion` y `forwardauth` (dirección, `authResponseHeaders`, la lista de permitidos `authRequestHeaders` que incluye `Authorization`); su contenido exacto está en [forwardauth.md](reference/forwardauth.md#configuración-de-traefik). `base/portal/ingressroute.yaml` agrega `strip-authorization` para que los tokens de API nunca lleguen al proceso del portal; toda ruta que coincida con un host del portal debe incluirlo.

**Valores de Traefik** (intégralos en los tuyos; se muestran valores del chart de Helm):

```yaml
ports:
  websecure:
    http:
      aliasHeadersStrategy: delete
      middlewares:            # order matters
        - platform-strip-assertion@kubernetescrd
        - platform-forwardauth@kubernetescrd
    forwardedHeaders: {trustedIPs: [], insecure: false}   # never trust client X-Forwarded-*
providers:
  kubernetesCRD: {allowCrossNamespace: false}
tlsStore:
  default: {defaultCertificate: {secretName: <your-wildcard-secret>}}   # Secret in the traefik namespace
accessLog:
  enabled: true
  fields:
    headers: {defaultMode: drop}           # Authorization and Cookie must never be logged
    queryParameters: {defaultMode: drop}   # login code, bind and rd travel in the query (Traefik >= 3.7.3)
```

Las claves `ports.websecure.http...` se corresponden con `entryPoints.websecure.http...` en la configuración estática y `tlsStore` es configuración dinámica; **No verificado fuera del clúster local** (solo se usó el chart de Helm). Fuente: `local/traefik-values.yaml` y `platform-gitops/overlays/local/traefik/entrypoint-values.yaml`. Los Middlewares deben existir antes de reconfigurar Traefik con la cadena del entrypoint, o todas las rutas se rompen (sección 8).

**IP del cliente.** Termina donde la dirección real del cliente llega a Traefik; el límite de tasa de `/login` usa la última entrada de `X-Forwarded-For`. **No verificado fuera del clúster local** (TODO: IP real del cliente).

## 7. Escribir el overlay

Mantén tu overlay en un directorio propio, `deploy/prod/<dir>/` (los ejemplos usan `../../../platform-gitops/base/...`, así que ajusta la profundidad si lo ubicas en otro lugar). Cada directorio base define su propio `namespace:`, así que usa un directorio de overlay por base para aplicarlos en orden. `kubectl kustomize <dir>` lo renderiza.

`namespaces/kustomization.yaml`: elimina los namespaces de las apps de referencia. Los namespaces de apps conservan la etiqueta `atrium.wysted.dev/app-namespace: "true"`: es el prefijo de etiquetas del proyecto (no tu dominio) y la NetworkPolicy `allow-ingress-jwks` de platform-api admite el puerto 8083 solo desde namespaces que la tengan. Agrega la etiqueta a cada namespace que aloje una app.

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../../platform-gitops/base/namespaces]
patches:
  - target: {kind: Namespace, name: "hello-.*"}
    patch: |-
      $patch: delete
      apiVersion: v1
      kind: Namespace
      metadata: {name: ignored}
```

`postgres/kustomization.yaml` (solo Opción A): `resources: [../../../platform-gitops/base/postgres]`, más un parche para `storageClassName` y el tamaño de los `volumeClaimTemplates` del StatefulSet y una entrada `images:` para `postgres`.

`migrate/kustomization.yaml` (el mismo `images:` de `platform-api` que abajo, más las variables de bootstrap de la [sección 9](#9-registrar-el-portal-y-crear-el-primer-administrador-de-la-plataforma)):

```yaml
resources: [../../../platform-gitops/base/migrate]
images:
  - {name: portal-platform-api, newName: <registry>/atrium/platform-api, newTag: v1}
  - {name: postgres, newName: <registry>/mirror/postgres}   # keeps the pinned tag and digest
patches:
  - target: {kind: Job, name: migrate}
    patch: |-
      - op: add
        path: /spec/template/spec/containers/0/env/-
        value: {name: PLATFORM_PORTAL_APP_ID, value: <portal-app-id>}   # same id as in the platform and portal overlays
      - op: add
        path: /spec/template/spec/containers/0/env/-
        value: {name: PLATFORM_PORTAL_HOST, value: portal.apps.example.com}
```

`platform/kustomization.yaml` (platform-api, Middlewares, ruta del host de auth):

```yaml
resources: [../../../platform-gitops/base/platform-api]
images:
  - {name: portal-platform-api, newName: <registry>/atrium/platform-api, newTag: v1}
  - {name: postgres, newName: <registry>/mirror/postgres}
patches:
  - target: {kind: Deployment, name: platform-api}
    patch: |-
      apiVersion: apps/v1
      kind: Deployment
      metadata: {name: platform-api}
      spec:
        template:
          spec:
            containers:
              - name: platform-api
                env:
                  - {name: PLATFORM_AUTH_HOST, value: auth.apps.example.com}
                  - {name: PLATFORM_ISSUER, value: "https://auth.apps.example.com"}
                  - {name: PLATFORM_PORTAL_APP_ID, value: <portal-app-id>}   # section 9
                  - {name: PLATFORM_GOOGLE_CLIENT_ID, value: <client-id>}
                  - {name: PLATFORM_GOOGLE_CLIENT_SECRET_FILE, value: /etc/platform/google/secret}
                  - {name: PLATFORM_ALLOWED_HD, value: example.com}
                volumeMounts:
                  - {name: google-client-secret, mountPath: /etc/platform/google, readOnly: true}
            volumes:
              - name: google-client-secret
                secret: {secretName: platform-google-client-secret, defaultMode: 0440}
  - target: {kind: IngressRoute, name: platform-api}
    patch: |-
      - {op: replace, path: /spec/routes/0/match, value: "Host(`auth.apps.example.com`)"}
```

`portal/kustomization.yaml`:

```yaml
resources: [../../../platform-gitops/base/portal]
images:
  - {name: portal-portal, newName: <registry>/atrium/portal, newTag: v1}
  - {name: postgres, newName: <registry>/mirror/postgres}
patches:
  - target: {kind: Deployment, name: portal}
    patch: |-
      apiVersion: apps/v1
      kind: Deployment
      metadata: {name: portal}
      spec:
        template:
          spec:
            containers:
              - name: portal
                env:
                  - {name: PORTAL_APP_ID, value: <portal-app-id>}   # same id as PLATFORM_PORTAL_APP_ID
                  - {name: PORTAL_ISSUER, value: "https://auth.apps.example.com"}
  - target: {kind: IngressRoute, name: portal}
    patch: |-
      - {op: replace, path: /spec/routes/0/match, value: "Host(`portal.apps.example.com`)"}
```

También es necesario, porque `base/` deniega todo el tráfico por defecto:

- **Egress a Google.** `base/platform-api` permite egress solo a DNS y PostgreSQL. Agrega una policy para TCP 443 desde los pods de platform-api, tomando como modelo `overlays/local-google/platform/networkpolicy-egress-google.yaml` (reemplaza su lista `except` por tus propios rangos de pods, servicios y nodos). Permite cualquier host público en el 443; consulta la [sección 11](#11-limitaciones-conocidas-antes-de-producción).
- **Supuestos del clúster en las NetworkPolicies y el probe.** Se espera DNS en `kube-system` con `k8s-app=kube-dns`; Traefik en el namespace `traefik`, etiqueta `app.kubernetes.io/name=traefik`; el probe del portal conecta a `traefik.traefik.svc.cluster.local:443` (`PORTAL_PROBE_ADDR`) y su policy de egress permite el puerto 8443 del pod (el puerto de contenedor `websecure`). Parchea las policies y las variables de entorno si tu Traefik difiere.
- **Base de datos externa.** `PGHOST`, `PGSSLMODE` (`disable` en base), el host de pgpass y las NetworkPolicies de `postgres` están definidos para el `base/postgres` dentro del clúster; parchéalos (y los tres Deployments y el Job) si usas otro servidor.
- **CA privada.** El probe de integración del portal verifica el certificado de Traefik contra las raíces del sistema. Si tu certificado proviene de una CA privada, define `PORTAL_PROBE_CA_FILE` en el Deployment del portal y monta la CA desde un ConfigMap, o la salud de integración queda en rojo ([configuración](reference/configuration.md)).
- Otras variables (timeouts, intervalo del probe, TLS del JWKS, métricas): [reference/configuration.md](reference/configuration.md).

## 8. Desplegar y verificar

```bash
kubectl apply -k deploy/prod/namespaces
# create the Secrets (section 3); Option A: kubectl apply -k deploy/prod/postgres; then the roles (section 4)
kubectl -n platform delete job migrate --ignore-not-found
kubectl apply -k deploy/prod/migrate
kubectl -n platform wait --for=condition=complete job/migrate --timeout=180s
# the Job also registered the portal app (section 9), then:
kubectl apply --dry-run=server -k deploy/prod/platform && kubectl apply -k deploy/prod/platform
kubectl -n platform rollout status deploy/platform-api
kubectl apply -k deploy/prod/portal
kubectl -n portal rollout status deploy/portal
# only now enable the entrypoint chain: upgrade Traefik with the values of section 6
```

Verificación: `kubectl -n platform get middleware.traefik.io strip-assertion forwardauth` lista ambos antes de la actualización de Traefik, y `kubectl -n platform logs deploy/platform-api` no muestra ningún WARN de `PLATFORM_PORTAL_APP_ID`. Un platform-api no listo suele significar una discrepancia de privilegios de rol (migrate no terminó) o una KEK incorrecta (motivo de readiness `kek mismatch`). Luego abre `https://portal.apps.example.com`: se te redirige a Google y, tras iniciar sesión, el portal carga. La suite de extremo a extremo `overlays/local/smoke.sh` del repositorio apunta al login falso del clúster local y no está pensada para tu instalación.

## 9. Registrar el portal y crear el primer administrador de la plataforma

`platform-api migrate` puede hacer ambas cosas después de aplicar las migraciones, en una sola transacción y de forma idempotente. Crea solo lo que falta, nunca modifica una fila existente y falla con un mensaje claro cuando una fila entra en conflicto (el id de app existe en otra iniciativa, o el host ya está asociado a otra app). Cada fila que crea recibe una fila en `audit_events` con `after.actor = "bootstrap"`. Variables ([referencia](reference/configuration.md#platform-api)):

| Variable | Efecto |
|---|---|
| `PLATFORM_PORTAL_APP_ID` + `PLATFORM_PORTAL_HOST` (ambas o ninguna) | Asegura la iniciativa reservada `platform`, la app del portal con ese id, y un deployment que asocia el host a ella |
| `PLATFORM_BOOTSTRAP_ADMIN_EMAIL` | Otorga el rol de administrador de la plataforma al usuario humano con ese email, solo mientras `platform_admins` esté vacía |

**App del portal.** Un id de app debe coincidir con `app_` + 26 caracteres de `[0-9A-HJKMNP-TV-Z]` (un ULID). Esto genera uno: el prefijo `00` mantiene los caracteres iniciales de timestamp dentro del rango válido de ULID, y el resto es hex aleatorio, que es un subconjunto del alfabeto permitido:

```bash
echo "app_00$(openssl rand -hex 12 | tr a-f A-F)"     # use it as <portal-app-id> in section 7
```

`PLATFORM_PORTAL_HOST` debe ser distinto del host de auth (migrate no puede comprobarlo; la API del portal lo rechaza). Define las dos variables en el overlay de migrate (sección 7) antes de la primera ejecución. Sin el deployment, el host del portal no se resuelve en ForwardAuth, así que nadie podría crearlo mediante la API.

**Primer administrador.** El usuario debe existir, y los usuarios se crean con su primer login. Por tanto: despliega como en la sección 8, inicia sesión una vez en el portal (no recibes grants, pero el portal se abre para cualquier usuario activo autorizado), luego agrega `PLATFORM_BOOTSTRAP_ADMIN_EMAIL` al overlay de migrate y ejecuta el Job de nuevo (`kubectl -n platform delete job migrate`, luego aplícalo). Si el usuario aún no ha iniciado sesión, el Job registra un WARN (`has not signed in yet`) y aun así termina con éxito. Cuando `platform_admins` ya tiene una fila, el paso se omite, de modo que una revocación deliberada nunca se deshace; si varios usuarios comparten el email, el Job falla en lugar de adivinar. Los usuarios de login falso y los robots nunca coinciden. Quita la variable después si lo deseas; es inofensiva.

Revoca con `DELETE FROM platform_admins WHERE user_id = ...` como `platform_owner`. El administrador crea entonces iniciativas, apps, grupos y tokens de API en el portal; `portalctl` otorga acceso con `PORTAL_URL` y `PORTAL_API_TOKEN` ([operate.md](operate.md#portalctl)). Para poner tus propias apps detrás de atrium: [onboard-an-app.md](onboard-an-app.md). Tareas del día a día: [operate.md](operate.md).

## 10. Opcional

- **Monitoreo.** `platform-gitops/monitoring/` incluye reglas de alerta (planas y `PrometheusRule`), `PodMonitor`s y un dashboard de Grafana. Nada de ello es específico de k3d; los supuestos (nombres de job, etiqueta `pod`, selectores de namespace) están en [operate.md](operate.md#monitoreo), las métricas en [metrics.md](reference/metrics.md). `base/monitoring` provee NetworkPolicies para un namespace `monitoring` que tú creas.
- **TLS interno para el JWKS.** `base/` sirve el JWKS por HTTP plano dentro del clúster. Sirve cualquier PKI; consulta [TLS interno para el JWKS](operate.md#tls-interno-para-el-jwks) para las variables de entorno y la opción con cert-manager. Recomendado para producción, **No verificado fuera del clúster local** con tu PKI.
- **Grafana detrás de ForwardAuth:** [operate.md](operate.md#grafana-detrás-de-forwardauth).

## 11. Limitaciones conocidas antes de producción

Los puntos abiertos se siguen en [TODO.md](../../TODO.md): "Before production" (Google `hd` para dominios secundarios, usuarios suspendidos, KEK en un Secret, IP real del cliente, filtrado de egress, serialización de `NOTIFY`) y "Adopter bootstrap gaps in `base/`" (valores fijados arriba). Ten en cuenta también que `base/postgres` es una sola instancia sin backups: planifica por tu cuenta la alta disponibilidad y los backups de la base de datos. Sin las variables de Google de la sección 7, platform-api se niega a iniciar (el login falso está desactivado por defecto), así que un parche olvidado falla de forma visible en lugar de abrir el login.
