# Runbook: rotar el secreto del cliente OAuth de Google

> English version: [docs/runbooks/google-client-secret-rotation.md](../../runbooks/google-client-secret-rotation.md)

Alcance: el `client_secret` que platform-api usa para intercambiar el código de autorización en el endpoint de token de
Google (`PLATFORM_GOOGLE_CLIENT_SECRET_FILE`). El id de cliente no cambia. Los comandos usan `kubectl` y los nombres del
Secret y del Deployment de [install.md](../install.md); si tu secreto proviene de un gestor de secretos, reemplaza la
actualización del Secret del paso 3 por él y conserva el resto.

> **Clúster local** (k3d del mantenedor): cada `kubectl` de abajo se ejecuta como `./tk kubectl`, el archivo del secreto es
> `.tools/secrets/google-client-secret`, y `overlays/local-google/up.sh` (con `BUILD=1`) lo carga en el Secret.
> Sin `BUILD=1` ese script actualiza el Secret pero no reinicia, así que ejecuta tú mismo el rollout restart del paso 3.

## Cómo platform-api carga el secreto

- Se monta desde el Secret `platform/platform-google-client-secret`, clave `secret`, en `/etc/platform/google/secret`
  (`fsGroup` + `defaultMode: 0440`, misma política que la KEK; [configuración](../reference/configuration.md#secretos-y-archivos-platform-api)).
- Se lee UNA SOLA VEZ al iniciar (`loadGoogleConfig`, sin espacios en blanco en los extremos). El kubelet actualiza el archivo
  montado tras una actualización del Secret, pero el proceso nunca lo vuelve a leer. **Un `rollout restart` es obligatorio.**
- El inicio solo verifica que el archivo no esté vacío. No llama a Google, así que un secreto incorrecto igual da pods Ready y
  falla en el primer login. **La verificación debe ser un login real.**
- Dos réplicas, actualización gradual: durante el rollout los pods antiguos conservan el secreto antiguo y los nuevos usan el
  nuevo. Por eso ambos secretos de Google deben estar habilitados hasta que el rollout termine.

## Cuándo rotar

- Calendario: al menos una vez al año, o cuando se va la persona que tenía acceso a la consola.
- Sospecha de filtración (secreto en un log, ticket, chat, laptop): usa la [variante por filtración](#variante-por-filtración).
- Instalaciones de uso poco frecuente: consulta el [riesgo de inactividad de 6 meses](#riesgo-de-inactividad-de-6-meses).

## Requisitos previos

- Una cuenta de Google Cloud con permiso para editar el cliente OAuth (Google Auth Platform > Clients).
- Acceso de `kubectl` al clúster, y acceso a Prometheus o Grafana para leer métricas.
- El valor nuevo se muestra SOLO al crearlo (después solo se ven los últimos 4 caracteres), así que descárgalo o cópialo
  de inmediato. Nunca lo pegues en una línea de comandos, ticket o chat.

## Datos de Google (verificados 2026-10-16)

- Un cliente Web tiene como máximo 2 secretos; ambos funcionan hasta que uno se deshabilita. Acciones por secreto: Add, Disable, Enable, Delete
  (Delete solo sobre un secreto deshabilitado, irreversible). Disable surte efecto "en breve" (sin tiempo documentado); Enable lo revierte.
- Los secretos se almacenan con hash; el valor completo se muestra/descarga solo al crearlo.
- No hay API ni comando de `gcloud` compatible para secretos de clientes Web: el paso en la consola es manual.
- Fallas en el endpoint de token: `invalid_client` ("The OAuth client secret is incorrect.") y `deleted_client`. El
  código exacto para un secreto deshabilitado no está documentado; se espera `invalid_client`.
- Fuentes: https://support.google.com/cloud/answer/15549257 ,
  https://support.google.com/cloud/answer/6158849 ,
  https://developers.googleblog.com/en/usability-and-safety-updates-to-google-auth-platform/ ,
  https://developers.google.com/identity/protocols/oauth2/web-server

## Pasos

Si el cliente ya tiene 2 secretos, deshabilita y elimina primero el más antiguo (tras comprobar que no es el desplegado).

1. **Agregar el secreto nuevo.** Consola: Google Auth Platform > Clients > el cliente > "Client secrets" > Add Secret. Descárgalo o
   cópialo ahora.
2. **Guardarlo localmente** (solo en archivo, sin historial del shell). `google-client-secret` es el archivo desde el que creaste el Secret:

   ```bash
   cp -p google-client-secret google-client-secret.old   # rollback copy
   ${EDITOR:-vi} google-client-secret                    # replace the content with the new value
   chmod 600 google-client-secret
   ```

3. **Actualizar el Secret y reiniciar**:

   ```bash
   kubectl -n platform create secret generic platform-google-client-secret \
     --from-file=secret=google-client-secret --dry-run=client -o yaml | kubectl apply -f - >/dev/null
   kubectl -n platform rollout restart deploy/platform-api
   kubectl -n platform rollout status deploy/platform-api --timeout=240s
   ```
4. **Verificar un login real de extremo a extremo.** En una ventana privada del navegador abre una app protegida (por ejemplo
   `https://app.apps.example.com`), inicia sesión con una cuenta de Workspace del dominio permitido y confirma que llegas a la
   app. Repite una vez más (las dos réplicas solo pueden diferir si el rollout está incompleto; en este punto no lo está).
5. **Revisar métricas y logs** (consulta [Detección de fallas](#detección-de-fallas)): `platform_logins_total{outcome="success"}`
   aumentó y no hay `failed`.
6. **Deshabilitar el secreto antiguo.** Consola > "Client secrets" > el secreto ANTIGUO (identifícalo por fecha de creación / últimos 4 caracteres)
   > Disable. NO lo elimines todavía.
7. **Ventana de observación: 24 h** (al menos un día hábil con logins reales). Vigila `outcome="failed"` y líneas de log con
   `invalid_client`. Haz un login real otra vez justo después de deshabilitar y de nuevo al final de la ventana.
8. **Eliminar el secreto antiguo** (ícono de papelera sobre el secreto deshabilitado) una vez que la ventana esté limpia. Luego elimina la copia local:

   ```bash
   shred -u google-client-secret.old 2>/dev/null || rm -f google-client-secret.old
   ```

## Detección de fallas

platform-api responde a un intercambio de código rechazado (cualquier 4xx del endpoint de token, lo que incluye `invalid_client`) con una
página 400 genérica y lo cuenta como `outcome="failed"`. Un 5xx o 429 de Google cuenta como `provider_unavailable` (503).

Logs (JSON, nivel WARN):

```bash
kubectl -n platform logs -l app.kubernetes.io/name=platform-api --since=30m --tail=-1 | rg 'google login failed'
# {"level":"WARN","msg":"google login failed: rejected","error":"authorization code exchange failed: token endpoint answered 401 invalid_client"}
```

El log contiene solo el estado HTTP y el código de error OAuth; nunca el cuerpo de la respuesta ni el secreto. El estado mostrado para
`invalid_client` es el que devuelve Google (típicamente 401); busca el texto `invalid_client`, no el número.
`invalid_grant` en la misma línea es un problema distinto (código vencido o reutilizado, no el secreto).

Métricas (Grafana Explore, datasource de Prometheus; el puerto de métricas solo está abierto a Prometheus):

```promql
sum by (outcome) (increase(platform_logins_total[15m]))
```

Alertas (`platform-gitops/monitoring/rules/portal-platform.rules.yaml`, grupo `portal-platform.login`, severidad warning):

- `LoginsFailing`: al menos 5 `outcome="failed"` en 15 minutos y ningún `success` en la misma ventana, durante 5 minutos. Así se ve
  un secreto de cliente incorrecto, deshabilitado o eliminado: las sesiones que ya existen siguen funcionando (por eso `ForwardAuthDown`
  y `ForwardAuthErrorsHigh` permanecen en silencio) y solo fallan los logins nuevos. Dispara tras aproximadamente 10-15 minutos de logins fallidos,
  así que durante una rotación sigue verificando a mano (panel del dashboard "Logins by outcome", las consultas de arriba); la alerta es la red de
  seguridad para una rotación que nadie estaba vigilando. Las fallas de un solo usuario, o fallas mientras otros usuarios inician sesión, no la disparan.
- `LoginProviderUnavailable`: misma forma para `outcome="provider_unavailable"`, durante 10 minutos. Es Google o el egress hacia él, no
  el secreto.

Una tasa de `failed` persistente con `invalid_client` en los logs tras una rotación significa que el secreto desplegado está deshabilitado, eliminado o
incorrecto: ve a [Rollback](#rollback).

## Rollback

Úsalo cuando el paso 4 o 5 falle, o durante la ventana de observación tras deshabilitar el secreto antiguo.

1. Si el secreto antiguo está deshabilitado: Consola > Enable (revierte la deshabilitación; funciona mientras no esté eliminado).
2. Restaura el valor anterior y reinicia:

   ```bash
   mv google-client-secret.old google-client-secret
   kubectl -n platform create secret generic platform-google-client-secret \
     --from-file=secret=google-client-secret --dry-run=client -o yaml | kubectl apply -f - >/dev/null
   kubectl -n platform rollout restart deploy/platform-api
   kubectl -n platform rollout status deploy/platform-api --timeout=240s
   ```

3. Verifica un login real. Investiga el secreto nuevo (error al copiar, caracteres sobrantes, cliente equivocado) antes de reintentar.

Una vez eliminado el secreto antiguo no hay rollback: agrega otro secreto y repite desde el paso 1.

## Variante por filtración

Objetivo: dejar de aceptar el secreto filtrado lo más rápido posible; las fallas breves de login son aceptables (las sesiones existentes
no se ven afectadas, solo los logins nuevos).

1. Agrega el secreto nuevo (paso 1) y despliégalo (pasos 2-3). Omite la ventana de observación.
2. Verifica un login real (paso 4).
3. Deshabilita de inmediato el secreto filtrado en la consola.
4. Verifica un login real otra vez y revisa `failed` / `invalid_client` durante unos minutos. Las fallas justo después del paso 3 pueden provenir
   solo de pods que aún ejecutan el secreto antiguo; si el rollout se completó en el paso 3 no debería haber ninguna.
5. Si el secreto filtrado ya estaba en el límite de 2 secretos, elimina primero el otro secreto (no filtrado, no desplegado).
6. Elimina el secreto filtrado tras las verificaciones, ejecuta las verificaciones posteriores y averigua dónde ocurrió la filtración.

Si el secreto nuevo no puede desplegarse rápido (clúster caído), deshabilitar primero el filtrado sigue siendo válido: los logins fallan
hasta que se despliegue el secreto nuevo.

## Verificaciones posteriores

- `kubectl -n platform get pods -l app.kubernetes.io/name=platform-api` muestra 2/2 Ready con edades recientes.
- Exactamente el secreto previsto está habilitado en la consola (la lista "Client secrets" muestra una entrada habilitada, los últimos 4
  caracteres coinciden con el nuevo).
- Un login real funciona; `outcome="success"` sube, `failed` se mantiene plano.
- No queda ninguna copia del secreto antiguo ni del nuevo fuera de `google-client-secret` (carpeta de descargas, historial del
  portapapeles, historial del shell).
- Registra la fecha de la rotación (próxima rotación en 12 meses).

## Riesgo de inactividad de 6 meses

Google elimina automáticamente un cliente OAuth tras 6 meses sin uso (los logins o los cambios de configuración cuentan como actividad)
y envía un email a los propietarios 30 días antes. Un cliente eliminado se puede restaurar durante unos 30 días. Para instalaciones de uso poco frecuente, mantén
monitoreado el email de notificación y realiza al menos un login o un cambio de configuración en cada período de 6 meses. Si el
endpoint de token empieza a responder `deleted_client`, restaura primero el cliente en la consola; un secreto nuevo no ayudará.
