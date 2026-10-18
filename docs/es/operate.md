# Operar atrium

> English version: [docs/operate.md](../operate.md)

Tareas del día a día para el operador de una instalación de atrium ([install.md](install.md)): otorgar acceso desde una terminal, actualizar, respaldar, monitorear, endurecer (hardening) y resolver problemas. Poner una app detrás de atrium está en [onboard-an-app.md](onboard-an-app.md).

| Quiero | Ir a |
|---|---|
| Otorgar o revocar acceso desde un script | [portalctl](#portalctl) |
| Publicar una nueva versión | [Actualización](#actualización) |
| No perder la instalación | [Backup y restauración](#backup-y-restauración) |
| Alertar sobre la salud de la plataforma | [Monitoreo](#monitoreo) |
| Rotar el secreto del cliente de Google | [Runbook](runbooks/google-client-secret-rotation.md) |
| Cerrar las brechas antes de producción | [Endurecimiento para producción](#endurecimiento-para-producción-hardening) |
| Resolver un síntoma | [Resolución de problemas](#resolución-de-problemas) |

## portalctl

`portalctl` otorga y revoca acceso a iniciativas mediante la API del portal. Es un simple cliente HTTP del host del portal, nada más. Construye un binario estático:

```bash
cd platform/api && CGO_ENABLED=0 go build -o portalctl ./cmd/portalctl
```

Uso: `portalctl grant|revoke <email|robot:name> <initiative-key> <role-key>`. El entorno contiene:

| Variable | Valor |
|---|---|
| `PORTAL_URL` | `https://` más solo el host del portal, por ejemplo `https://portal.apps.example.com` |
| `PORTAL_API_TOKEN` | Un token de API personal o de robot. Nunca un flag |

Crea el token en el portal (**API tokens** en la página de inicio para el tuyo, **Robots** para un robot; el secreto se muestra una sola vez y los tokens se crean desde una sesión de navegador) y mantenlo fuera del historial del shell:

```bash
export PORTAL_URL=https://portal.apps.example.com
read -rs PORTAL_API_TOKEN && export PORTAL_API_TOKEN
portalctl grant you@example.com my-initiative viewer
portalctl revoke robot:ci-deployer my-initiative editor
```

Quién puede usarlo: un administrador de la plataforma (token con alcance `admin`), un gestor de iniciativa (token con alcance `admin`, solo para las iniciativas que gestiona) o un robot con un rol `manages_access` ([quién administra qué](reference/api.md#quién-administra-qué)). Otorgar por email requiere que el usuario haya iniciado sesión una vez.

Comportamiento y códigos de salida:

- Es idempotente. La salida es `changed` o `unchanged`, luego `grant done: ...` o `revoke done: ...`. Un grant (asignación de rol) existente con vencimiento futuro es `unchanged` e imprime una advertencia en stderr.
- Salida `0` en caso de éxito; `1` ante cualquier falla de ejecución o de la API (iniciativa, rol o robot desconocido, un grant a un robot deshabilitado, un grant existente vencido: revócalo primero); `2` por uso incorrecto (argumentos inválidos, `PORTAL_URL` inválida, token vacío).
- Nunca sigue redirecciones y respeta `SSL_CERT_FILE` para una CA privada.

Platform-api ve los cambios de grants mediante `NOTIFY`, sin reiniciar.

## Actualización

Tanto `platform-api` como `portal` se niegan a iniciar cuando los roles de la base de datos se desvían de lo que el binario espera, así que primero se debe migrar el esquema. Ambos también informan no listo (motivo `migrations pending`) mientras alguna migración incluida en su binario no esté aplicada, y quedan listos por sí solos cuando el Job la aplica; las migraciones más nuevas que el binario nunca bloquean. Por release:

1. Construye y sube las imágenes nuevas ([install.md, sección 2](install.md#2-construir-las-imágenes)) y actualiza los tags en tu overlay.
2. Elimina el Job `migrate` anterior (un Job es inmutable), aplica el nuevo y espéralo:

   ```bash
   kubectl -n platform delete job migrate --ignore-not-found
   kubectl apply -k deploy/prod/migrate
   kubectl -n platform wait --for=condition=complete job/migrate --timeout=180s
   ```

3. Despliega `platform-api`, espéralo y luego `portal` ([install.md, sección 8](install.md#8-desplegar-y-verificar)).

`base/` no tiene un mecanismo de ordenamiento; aplícalo en tu GitOps con un hook `PreSync` de Argo CD (o sync-wave `-1`) en el Job, o un `Kustomization` de Flux para `migrate` del que el de `platform-api` dependa con `dependsOn` y `wait: true`. Definir `ttlSecondsAfterFinished` en el Job también lo recrea en cada release. Revertir una migración no está documentado: haz un backup antes de actualizar.

## Backup y restauración

Todo el estado está en PostgreSQL: iniciativas, apps, grants, usuarios, sesiones, el registro de auditoría y las llaves de firma selladas. Lo único fuera de ella es la KEK, un Secret de Kubernetes.

- **Base de datos.** Respalda la base de datos `platform` con tus herramientas habituales de PostgreSQL (`base/postgres` es una sola instancia sin backups; planifícalo por tu cuenta). Los roles son objetos a nivel de clúster y no forman parte de un dump de la base de datos: recréalos como en [install.md, sección 4](install.md#4-base-de-datos-y-roles) en el servidor de destino antes de restaurar, y luego ejecuta el Job `migrate`.
- **KEK.** Respáldala junto con la base de datos, en un lugar distinto, y nunca la regeneres. Las llaves de firma de `signing_keys` están selladas con ella ([llaves](reference/keys.md#almacenamiento)). Un backup de la base de datos restaurado con una KEK distinta contiene llaves que nadie puede descifrar: platform-api informa una discrepancia de KEK (motivo de readiness `kek mismatch`), falla el readiness y se niega a rotar o revocar. Ningún procedimiento de este repositorio reemplaza o vuelve a sellar las llaves sin la KEK original. Trata la KEK y el dump de la base de datos como una sola unidad.
- **Sin un backup de la base de datos** pierdes todo el catálogo (iniciativas, roles, apps, hosts, grants) y la traza de auditoría. Los usuarios se recrean en su siguiente login, sin grants.

La restauración no se ejercitó en este repositorio: pruébala en un clúster de pruebas antes de depender de ella.

## Rotación de llaves

No hay nada que hacer. Las llaves de firma rotan automáticamente cada 30 días, una réplica a la vez, y se publican en el JWKS antes de firmar. La rotación de emergencia y las reglas de falla están en [keys.md](reference/keys.md#ciclo-de-vida). Vigila las alertas `SigningKeyNotLoaded` y `SigningKeyKEKMismatch` ([Monitoreo](#monitoreo)). La rotación de la KEK misma no tiene un procedimiento incluido.

## Monitoreo

`platform-gitops/monitoring/` contiene los artefactos. Nada de ello es específico de un clúster. Catálogo de métricas y la regla de cardinalidad: [metrics.md](reference/metrics.md).

| Archivo | Uso |
|---|---|
| `rules/portal-platform.rules.yaml` | Reglas de alerta en formato Prometheus plano (la fuente) |
| `operator/prometheusrule.yaml` | Las mismas reglas como un `PrometheusRule`, generado por `gen-prometheusrule.sh` |
| `operator/podmonitors.yaml` | Scrape de `platform-api` y `portal` en el puerto de pod llamado `metrics` (`job` es el nombre de la app) |
| `dashboards/portal-platform.json` | Dashboard de Grafana (datasource elegido por variable); `dashboards/kustomization.yaml` lo envuelve en un ConfigMap con la etiqueta `grafana_dashboard` para un sidecar |
| `rules/tests/portal-platform.test.yaml` | Pruebas unitarias de `promtool`: cada regla tiene un caso que dispara y uno que no |
| `test-rules.sh` | Verificación de sincronía, `promtool check rules` y `promtool test rules` (`prom/prometheus` fijado, mediante Docker) |

Supuestos a adaptar:

- Las reglas y el dashboard esperan `job="platform-api"` y `job="portal"` (los PodMonitors lo definen con `jobLabel`). Edita `namespaceSelector` en los PodMonitors para tus namespaces.
- Las reglas por réplica (`SigningKeyNotLoaded`, `SigningKeyKEKMismatch`, `DBPoolSaturated`) incluyen `{{ $labels.pod }}` en su texto. El PodMonitor agrega `pod`; una configuración de scrape que no lo haga debe aplicar relabel.
- El puerto de métricas (9090) está abierto solo a los pods etiquetados `app.kubernetes.io/name=prometheus` en un namespace llamado `monitoring` (`allow-ingress-metrics` en `platform` y `portal`); `base/monitoring` tiene las NetworkPolicies para ese namespace. Cualquier cosa que pueda ejecutar un pod con esa etiqueta allí se une al conjunto: limita el namespace al stack. Parchea las policies si tu Prometheus difiere.
- Si tu Prometheus selecciona todos los `PodMonitor` y `PrometheusRule` de un namespace, cualquiera que pueda crear esos objetos cambia lo que se scrapea o alerta. Restringe la creación por RBAC o por selección de etiquetas.
- `ProbeNotWriting` dispara cuando `time() - max(portal_probe_last_write_timestamp_seconds)` supera 3 veces `portal_probe_interval_seconds` (0 significa que el probe está deshabilitado y nunca dispara; las gauges ausentes son silenciosas), durante 10 minutos. Un reinicio restablece el timestamp, así que la detección puede retrasarse hasta 3 intervalos. Las reglas del portal asumen un release del portal por Prometheus (`job="portal"`).
- Tras editar las reglas planas ejecuta `monitoring/gen-prometheusrule.sh`; `test-rules.sh` falla cuando el archivo generado está desactualizado.
- La alerta `LoginsFailing` enlaza al [runbook del secreto del cliente de Google](runbooks/google-client-secret-rotation.md).

### Grafana detrás de ForwardAuth

Grafana puede ser una app de atrium: autentica la solicitud con el header `Platform-Assertion` y no necesita un login propio. Esto es [onboard-an-app.md](onboard-an-app.md) con la autenticación JWT de Grafana como validador. **No verificado fuera del clúster local.**

- Registra una iniciativa cuyas llaves de rol sean iguales a lo que coincida tu `role_attribute_path`, una app para Grafana y su host (`aud` es el id de la app). Enruta el host por el entrypoint que ejecuta ForwardAuth. El Service de Grafana no debe ser alcanzable de ninguna otra forma (ingress de NetworkPolicy solo desde el proxy; sin LoadBalancer ni NodePort), o cualquiera puede enviar el header.
- Configura `[auth.jwt]` con `header_name = Platform-Assertion`, `jwk_set_url` sobre HTTPS (obligatorio fuera del modo dev), `tls_client_ca` para una CA privada, `expect_claims` con el `PLATFORM_ISSUER` exacto y el id de la app, `role_attribute_strict` y `url_login = false`. Las variables de entorno `GF_AUTH_JWT_*` funcionan igual que el ini.
- Desactiva cualquier otra vía de entrada (formulario de login, basic auth, anónimo, otros `[auth.*]`) y decide tu vía de emergencia (break-glass).
- Permite que Grafana alcance el Service del JWKS (egress) y que el listener del JWKS lo admita (ingress, por selector de pod, no por la etiqueta de namespace de apps).

## TLS interno para el JWKS

Por defecto `base/` sirve el JWKS por HTTP plano dentro del clúster; no incluye PKI, así que tú eliges una. TLS se recomienda para producción y **no está verificado fuera del clúster local** con tu PKI. El contrato está en [keys.md](reference/keys.md#endpoint-jwks) y [configuration.md](reference/configuration.md).

- **Servidor.** Cualquier emisor que escriba un certificado PEM (se permite cadena) y una llave (modo 0640 o más estricto) en las dos rutas de `PLATFORM_JWKS_TLS_CERT_FILE` y `PLATFORM_JWKS_TLS_KEY_FILE`: un `Certificate` de cert-manager cuyos `dnsNames` sean los nombres del Service del JWKS y cuyo `secretName` entregue `tls.crt` y `tls.key` para el montaje, una plantilla de Vault agent, o un Secret `kubernetes.io/tls` hecho a mano. El SAN debe ser igual al nombre que usan las apps. Monta el Secret con `defaultMode: 0440` y un `fsGroup` del pod. El listener entonces sirve solo HTTPS, TLS 1.3 como mínimo, y recarga el par cada 30 s, así que la rotación de la hoja no requiere reinicio.
- **Clientes.** Distribuye el bundle de la CA emisora (PEM) a cada namespace de app y apunta el verificador a él: `PORTAL_JWKS_CA_FILE`, `HELLO_JWKS_CA_FILE` o `Config.RootCAs` en el SDK. Opciones: un Bundle de `trust-manager` de cert-manager, un montaje de Vault o CSI, o un ConfigMap por namespace. Omítelo cuando la CA es de confianza del sistema.
- **La rotación de la CA** requiere más cuidado que la de la hoja: los verificadores cargan la CA una sola vez al iniciar, así que distribuye primero la nueva CA, reinicia los verificadores y solo entonces sirve la nueva hoja. Combina la CA antigua y la nueva en un bundle durante la transición.
- Cambia las URLs del JWKS a `https://` en el mismo cambio. Una URL `http` registra una advertencia al iniciar el verificador.

## Endurecimiento para producción (hardening)

Los puntos abiertos se siguen en [TODO.md](../../TODO.md); lee ambas listas antes de salir a producción.

- **Puntos abiertos.** Lee ambas listas en [TODO.md](../../TODO.md) ("Before production" y "Adopter bootstrap gaps in `base/`"); los pasos manuales también están en [install.md](install.md#11-limitaciones-conocidas-antes-de-producción).
- **El egress a Google no está en `base/`.** `base/` deniega todo el egress, así que sin una policy propia el login responde 503 y dispara `LoginProviderUnavailable`. Toma como modelo `overlays/local-google/platform/networkpolicy-egress-google.yaml`, cuya lista `except` es específica de los rangos locales de k3d ([install.md](install.md#7-escribir-el-overlay)). La NetworkPolicy estándar no puede coincidir con FQDNs, así que esa policy permite TCP 443 a cualquier dirección fuera de tus rangos privados: un platform-api comprometido podría alcanzar cualquier host público en el 443 (exfiltración), no solo Google. Acotarla requiere un proxy de egress con una lista de FQDN permitidos o un CNI con policies L7 o de FQDN. El Job `migrate` solo necesita PostgreSQL. La policy de referencia es solo IPv4.
- **IP del cliente.** Sin un balanceador de carga que preserve la dirección de origen, todos los usuarios comparten un único bucket del límite de tasa de `/login`: [install.md](install.md#6-traefik).
- **KEK.** Es legible por cualquier principal con `secrets get` a nivel de todo el clúster (por ejemplo un Prometheus Operator con RBAC por defecto): audita los ClusterRoles que pueden leer Secrets en `platform`, o mantenla en un KMS externo.

## Resolución de problemas

| Síntoma | Causa | Solución | Más |
|---|---|---|---|
| `/login` o `/oauth2/callback` responde 503 con `Retry-After` | El discovery de Google aún no está listo, o Google es inalcanzable, respondió 5xx o 429 en el endpoint de token, o redirigió. Las sesiones que ya existen siguen funcionando | Espera y reintenta. Si persiste, revisa el egress de `platform-api` hacia `accounts.google.com`, `oauth2.googleapis.com` y `www.googleapis.com`, y el estado de Google. La alerta `LoginProviderUnavailable` dispara por esto | [login](reference/login.md#configuración-y-disponibilidad) |
| Una llamada a la API del portal responde 503 con `Retry-After` | Una mutación perdió frente a transacciones concurrentes en todos los intentos | Reintenta; las mutaciones son idempotentes por clave natural (crear una app no lo es: revisa si hay un duplicado) | [API](reference/api.md#convenciones) |
| platform-api no listo: el log dice "this replica was started with the wrong KEK: signing and rotation disabled", motivo de readiness `kek mismatch`, alerta `SigningKeyKEKMismatch` | La KEK montada difiere de la que selló las llaves de firma almacenadas. La réplica deja de firmar y se niega a rotar o revocar | Vuelve a montar la KEK original. Nunca la regeneres | [llaves](reference/keys.md#reglas-de-falla-y-consistencia), [backup](#backup-y-restauración) |
| platform-api o portal no listo: el log dice el motivo `migrations pending` (o `migrations check failed`) | La imagen incluye migraciones que la base de datos no ha aplicado: el Job `migrate` no se ha ejecutado o no ha terminado para este release. `migrations check failed` significa que la consulta de verificación misma falló | Ejecuta el Job `migrate` hasta completarlo; los pods quedan listos sin reiniciar | [actualización](#actualización) |
| platform-api o portal se niega a iniciar: "refusing to run: the database role is not the least-privilege role ..." | Al rol le faltan o le sobran privilegios (las migraciones no se completaron, o los privilegios se cambiaron a mano), o el rol de login es superusuario, omite RLS o es dueño de tablas | Ejecuta el Job `migrate` hasta completarlo antes del despliegue. Conéctate con un rol de login que sea miembro solo del rol runtime, creado como en la guía de instalación | [instalación](install.md#4-base-de-datos-y-roles), [modelo de datos](reference/data-model.md#roles-de-base-de-datos) |
| Un usuario con sesión iniciada recibe 403 en una app | Los usuarios se crean en el primer login sin grants, o el grant venció, o el usuario, la app o la iniciativa está deshabilitado o inactivo | Otorga un rol; revisa la salud de la app en el portal | [onboard](onboard-an-app.md#5-otorgar-acceso), [ForwardAuth](reference/forwardauth.md#tabla-de-decisión) |
| `/login` responde 429 | El límite de tasa por cliente. Si afecta a muchos usuarios a la vez, Traefik ve una sola dirección de cliente (balanceador de carga o NAT que oculta el origen), así que todos los usuarios comparten un bucket | Respeta `Retry-After`. Preserva la dirección del cliente hasta Traefik | [TODO.md](../../TODO.md), [login](reference/login.md#reglas) |
| platform-api se niega a iniciar con "`PLATFORM_FAKE_LOGIN=false` requires Google login: set `PLATFORM_GOOGLE_CLIENT_ID`, `PLATFORM_GOOGLE_CLIENT_SECRET_FILE` and `PLATFORM_ALLOWED_HD`" | No hay ninguna variable de Google definida (el login falso está desactivado por defecto, por diseño) | Define las tres variables juntas | [configuración](reference/configuration.md#platform-api), [instalación](install.md#7-escribir-el-overlay) |
| platform-api se niega a iniciar con "`<VAR>` is required when Google login is configured", "`PLATFORM_GOOGLE_CLIENT_SECRET_FILE`: the file is empty" u otro error de archivo | Algunas variables de Google están definidas pero falta una, está vacía o es ilegible (el archivo del secreto debe existir, no estar vacío y tener modo 0640 o más estricto) | Corrige la variable o archivo indicado; las tres son obligatorias una vez que se define una | [configuración](reference/configuration.md#platform-api) |
| platform-api se niega a iniciar con "`PLATFORM_ISSUER` must be exactly `https://<PLATFORM_AUTH_HOST>`" | El emisor no es exactamente `https://` más el host de auth: mayúsculas en el esquema, puerto, ruta, query, barra final o credenciales | Define `PLATFORM_ISSUER` como `https://auth.apps.example.com` para `PLATFORM_AUTH_HOST=auth.apps.example.com`. `PORTAL_ISSUER` y el emisor de cada app deben llevar el mismo valor | [configuración](reference/configuration.md#platform-api) |
| La salud de una app muestra `unprotected`, `unreachable` o `never_seen` | La ruta está fuera del entrypoint de ForwardAuth, el portal no alcanza Traefik o su certificado (CA privada: define `PORTAL_PROBE_CA_FILE`), o aún no hay tráfico | Consulta la tabla de hallazgos | [onboard](onboard-an-app.md#6-verificar), [métricas](reference/metrics.md#salud-de-una-app) |
| Los nuevos logins fallan tras una rotación del secreto del cliente de Google | El secreto desplegado es incorrecto, está deshabilitado o eliminado; `invalid_client` en los logs | Revierte como indica el runbook | [runbook](runbooks/google-client-secret-rotation.md#rollback) |

## Runbooks

- [Rotar el secreto del cliente OAuth de Google](runbooks/google-client-secret-rotation.md)
