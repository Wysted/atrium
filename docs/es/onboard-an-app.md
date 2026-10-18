# Poner una app detrás de atrium

> English version: [docs/onboard-an-app.md](../onboard-an-app.md)

Cómo llevar una app web interna de "alcanzable dentro del clúster" a "alcanzable solo por personas autorizadas, mediante login de Google". Para el operador que registra la app y el desarrollador que la integra. Supone una instalación funcional ([install.md](install.md)) y un administrador de la plataforma en el portal.

El estado final: la app tiene un id y un host registrados, su namespace y su ruta están conectados a Traefik, valida el header `Platform-Assertion`, y las personas que deben alcanzarla tienen un rol.

## 1. Registrar la app en el portal

Un administrador de la plataforma lo hace en el portal, en **Initiatives** ([quién puede hacer qué](reference/api.md#quién-administra-qué)). Las mismas llamadas existen en la [API](reference/api.md#endpoints); `portalctl` solo otorga y revoca.

1. **Iniciativa.** **New initiative**: una clave (inmutable; letras minúsculas, dígitos, `-` y `_`) y un nombre opcional. Una iniciativa agrupa apps que comparten roles y personas. Reutiliza una si la app pertenece a un grupo existente.
2. **Roles.** En la iniciativa, **Roles** > **New role**. Las claves de rol forman parte del contrato de la app: llegan en el claim `roles` y nunca cambian, así que elige los nombres que tu app verificará (por ejemplo `viewer`, `editor`). Marca un rol como `manages_access` solo para las personas que deben otorgar acceso sin ser administradores de la plataforma.
3. **App.** **Apps** > **New app**. El id de la app (`app_` más un ULID) se genera; cópialo. Es el `aud` que tu app debe verificar, nunca el host.
4. **Host.** En la fila de la app, **Add host** (`app.apps.example.com`). Un host es un nombre DNS en minúsculas; debe ser único entre todas las apps y no puede ser el host de auth ([reglas de host](reference/api.md#reglas-de-administración)). ForwardAuth resuelve la app por coincidencia exacta de este host, así que debe ser igual al `Host()` de tu ruta en el paso 3.

Ahora tienes: un id de app, el emisor (`https://<PLATFORM_AUTH_HOST>`, por ejemplo `https://auth.apps.example.com`) y la URL del JWKS, que es la dirección dentro del clúster del listener del JWKS de platform-api, `https://platform-api-jwks.platform.svc.cluster.local:8083/.well-known/jwks.json` (`http://` si no habilitaste [TLS interno](operate.md#tls-interno-para-el-jwks)).

## 2. Preparar el namespace

Etiqueta el namespace que aloja la app, o la NetworkPolicy de platform-api no le permitirá obtener el JWKS:

```bash
kubectl label namespace my-app atrium.wysted.dev/app-namespace=true
```

El prefijo de la etiqueta es del proyecto, no tu dominio. El namespace también necesita objetos `NetworkPolicy`, porque la plataforma deniega el tráfico por defecto. Copia las tres de [`platform-gitops/base/hello/networkpolicy.yaml`](../../platform-gitops/base/hello/networkpolicy.yaml): denegar todo por defecto, ingress solo desde Traefik en el puerto de la app, egress solo a DNS y a platform-api en el 8083. Agrega encima el egress que tu app necesite. Las apps deben ser alcanzables solo desde Traefik: la aserción es la defensa si eso falla, no una razón para abrir el puerto ([aserción](reference/assertion.md#garantías-del-lado-de-la-plataforma)).

## 3. Enrutar el host por Traefik

ForwardAuth y la eliminación de aserciones enviadas por el cliente se ejecutan en el entrypoint `websecure` ([install.md, sección 6](install.md#6-traefik)), así que la ruta no necesita un middleware propio. Toma como modelo [`platform-gitops/base/hello/hello.yaml`](../../platform-gitops/base/hello/hello.yaml):

```yaml
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: my-app
  namespace: my-app
spec:
  entryPoints: [websecure]
  routes:
    - kind: Rule
      match: Host(`app.apps.example.com`)   # exactly the host registered in step 1
      services:
        - {name: my-app, port: 8080}
  tls: {}
```

Reglas que importan:

- Usa un `Host()` exacto. Nunca `HostRegexp`, un catch-all o un `Ingress` simple sin host: pueden coincidir con el host del portal o de auth sin que se note ([ForwardAuth](reference/forwardauth.md#manifiestos-de-traefik-e-invariantes)).
- El certificado debe cubrir el host (un wildcard como `*.apps.example.com` lo hace).
- No uses `/_platform/*` en la app: está reservado para el callback de login y el logout ([convenciones](reference/forwardauth.md#convenciones)).

## 4. Validar la aserción en la app

Traefik agrega `Platform-Assertion` a cada solicitud autorizada. La app debe verificarlo; no debe confiar en `X-Forwarded-User` ni en headers similares.

**Go.** Usa el SDK (`go get github.com/Wysted/atrium/platform/sdk/go@<commit>`; aún no hay tags de release, así que fija un commit; el paquete es `platformauth`):

1. Inyecta el id de la app, el emisor y la URL del JWKS por entorno. Una CA privada para un JWKS por HTTPS va en un archivo; cárgala con `platformauth.LoadRootCAs` y pásala como `Config.RootCAs` (omítela cuando la CA es una raíz del sistema).
2. Construye un verificador al iniciar y envuelve todo el handler:

   ```go
   cfg := platformauth.Config{Issuer: issuer, Audience: appID, JWKSURL: jwksURL}
   if caFile != "" {
       roots, err := platformauth.LoadRootCAs(caFile)
       if err != nil { return err }
       cfg.RootCAs = roots
   }
   verifier, err := platformauth.NewVerifier(cfg)
   if err != nil { return err }
   srv.Handler = verifier.Middleware(mux) // or MiddlewareWithLogger(log, mux)
   ```

3. Lee la identidad en los handlers con `claims, ok := platformauth.ClaimsFrom(r.Context())`. Usa `Subject` como identificador del usuario; `Email` y `Name` son informativos. `Roles` contiene las claves de rol de tu iniciativa.
4. Mantén `/healthz` para los probes: el SDK lo exime de la validación, porque el kubelet no pasa por Traefik.
5. Nunca registres ni reenvíes la aserción; el middleware ya la elimina de la solicitud que entrega a tus handlers.
6. El cierre de sesión es un simple formulario `POST /_platform/logout`.

Comportamiento: una aserción ausente o inválida da 401, llaves de firma inalcanzables dan 503 ([comportamiento ante fallas](reference/assertion.md#comportamiento-del-sdk-ante-fallas)). Una app completa es [`platform/examples/hello`](repo/platform/examples/hello/README.md); sus variables son `HELLO_APP_ID`, `HELLO_ISSUER`, `HELLO_JWKS_URL` y `HELLO_JWKS_CA_FILE`.

**Otros lenguajes.** No hay SDK; implementa las verificaciones de [Validación del lado de la app](reference/assertion.md#validación-del-lado-de-la-app): `alg` es `ES256` y `typ` es `platform-assertion+jwt` (muchas bibliotecas no verifican `typ`), llave seleccionada por `kid` desde la URL fija del JWKS (ignora `jku` y `x5u`), `iss` y `aud` exactos, `exp` obligatorio, ruta de salud exenta.

## 5. Otorgar acceso

Los usuarios se crean en su primer login sin grants (asignaciones de rol), así que todos reciben 403 hasta que se les otorgue acceso. Un grant requiere que el usuario haya iniciado sesión una vez cuando nombra un email.

- **Portal:** iniciativa > **Grants** > **Grant access**: un email, un robot o un grupo, un rol, un vencimiento opcional. Los administradores de la plataforma y los gestores de la iniciativa pueden hacerlo.
- **Terminal:** `portalctl grant you@example.com my-initiative viewer` ([uso](operate.md#portalctl)).

## 6. Verificar

1. Despliega la app y espera: `kubectl -n my-app rollout status deploy/my-app`.
2. Abre `https://app.apps.example.com` en una ventana privada. Esperado: una redirección a `https://auth.apps.example.com/login`, luego Google, luego de vuelta a la app.
3. Con un grant llegas a la app y `ClaimsFrom` muestra tu `sub` y tus roles. Sin uno recibes 403.
4. En el portal, iniciativa > **Apps** > **Health** en la fila de la app. Una app saludable no muestra problemas. Esperado justo después del deploy: `never_probed` (el portal hace probe en su intervalo) y `never_seen` (hasta que llegue una solicitud autorizada); `never_probed` desaparece tras la siguiente ronda de probes y `never_seen` cuando llega la primera solicitud autorizada (el paso 3 la provoca). Cualquier otra cosa es un hallazgo:

| Hallazgo | Significado y solución |
|---|---|
| `unprotected` | El host se sirvió sin ForwardAuth: la ruta no está en la cadena del entrypoint `websecure`. Corrige la ruta o los middlewares del entrypoint |
| `unexpected_redirect` | El host redirige a algo distinto del login del host de auth. Revisa el host registrado y las redirecciones propias de la app |
| `unreachable` | DNS, TLS o timeout desde el portal. Revisa el registro DNS, el certificado y que el portal pueda alcanzar Traefik |
| `no_grants`, `no_manager` | Nadie tiene un rol todavía, o ningún humano tiene un rol `manages_access`. Otorga acceso (paso 5) |
| `never_seen` más `unprotected` | Ninguna ruta coincide con el host: Traefik responde 404 |

Otros códigos (`app_disabled`, `initiative_inactive`, `not_seen_recently`, `probe_stale`, `probe_disabled`): [Salud de una app](reference/metrics.md#salud-de-una-app).

## Siguiente paso

Operar la plataforma que sirve la app: [operate.md](operate.md).
