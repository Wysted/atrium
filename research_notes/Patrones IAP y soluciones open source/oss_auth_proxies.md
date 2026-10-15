# Proxies de autenticación / IAP / forward-auth open source: comparación por solución (estado a 2026-10-14)

Contexto: plataforma interna con portal, admin y N apps en `*.apps.empresa.cl`, sobre K3s con Traefik. El diseño propuesto es un platform-api en Go que actúa como ForwardAuth: login con Google OIDC, autorización desde grupos en PostgreSQL (grupo -> app) y JWT firmado con `aud` por app.
Soluciones cubiertas: Pomerium Core, oauth2-proxy, Authelia, traefik-forward-auth (thomseddon y forks), Tinyauth, Pangolin, Teleport Application Access (Community) y Ory Oathkeeper.
Idioma de las fuentes: todas en inglés salvo donde se indica [ES], [DE], [FR], [ZH] o [JA].

---

## 1. Autenticación: IdPs soportados, Google OIDC y capacidad de ser su propio IdP

### Takeaway
Pomerium, oauth2-proxy, traefik-forward-auth y Tinyauth hacen login con Google sin pagar. Pangolin Community solo admite IdPs "globales" OIDC genéricos; el conector dedicado de Google requiere Enterprise. Teleport Community solo trae SSO con GitHub. Authelia no puede delegar el login en Google porque solo usa backends file/LDAP y la función de Relying Party está "en planificación". Oathkeeper no tiene login propio. Authelia y Tinyauth (OpenID Certified) pueden actuar como IdP OIDC.

### Cited Findings
- **Pomerium**: se autentica contra un IdP OIDC. El guide de Traefik dice "Pomerium authenticates users via Identity Provider" — [Pomerium Traefik guide](https://pomerium.com/guides/traefik-ingress)
- **oauth2-proxy**: tiene un provider Google dedicado con flags `--google-admin-email`, `--google-group`, `--google-service-account-json` y `--google-use-application-default-credentials` — [oauth2-proxy Google provider](https://oauth2-proxy.github.io/oauth2-proxy/configuration/providers/google)
- **oauth2-proxy**: AlternativeTo lo describe como "standalone reverse proxy or a middleware component" para OAuth2/OIDC [DE search] — [AlternativeTo](https://www.alternativeto.net/software/tinyauth/)
- **Authelia**: el roadmap oficial pone "OpenID Connect 1.0 Relying Party" en la etapa *Planning* ("still in the very early planning stages"). En cambio, "OpenID Connect 1.0 Provider" está en *Active* — [Authelia roadmap](https://www.authelia.com/roadmap/prologue/introduction/)
- **Authelia**: un espejo antiguo de su documentación decía que el proyecto "does not intend to support" el rol de Relying Party para Google y GitHub. Es una postura histórica que el roadmap actual matiza — [mirror antiguo (baja confiabilidad)](https://gitea.unej.ac.id/techies/authelia/commit/1eef78ff7b760168020b557879359773fa9ea8fe)
- **Authelia**: es un "OpenID Certified OpenID Connect provider" sin SAML — [sumguy.com (blog)](https://sumguy.com/tinyauth-vs-pocket-id-vs-authelia/)
- **traefik-forward-auth (thomseddon)**: "Supports multiple providers including Google and OpenID Connect", además de un modo Generic OAuth2 — [thomseddon README](https://github.com/thomseddon/traefik-forward-auth)
- **traefik-forward-auth (fork ItalyPaleAle, v4)**: soporta Google, Microsoft Entra ID, GitHub, OIDC genérico y SSO vía Tailscale Whois — [ItalyPaleAle README](https://github.com/ItalyPaleAle/traefik-forward-auth)
- **traefik-forward-auth (fork mesosphere)**: es una "partial rewrite to support generic OIDC Providers" con discovery — [mesosphere README](https://github.com/mesosphere/traefik-forward-auth)
- **Tinyauth**: la documentación tiene guías para Google OAuth, GitHub OAuth, Pocket ID, OIDC genérico y LDAP — [Tinyauth headers/docs](https://tinyauth.app/docs/reference/headers)
- **Tinyauth**: es además un OpenID Provider: "As of 2026-06-25, Tinyauth v5.1.0 is OpenID Certified™ for Basic OP" — [Tinyauth GitHub](https://github.com/tinyauthapp/tinyauth)
- **Pangolin**: soporta OIDC genérico (Authentik, Keycloak, Okta), Google Workspace y Azure Entra ID. Sin embargo, "Global identity providers are the only supported method in Pangolin Community", mientras que Google, Entra ID y los IdPs por organización son solo Cloud o Enterprise — [Pangolin IdP docs](https://docs.pangolin.net/manage/identity-providers/add-an-idp)
- **Pangolin**: también ofrece identidades propias ("built-in Pangolin identities") — [Pangolin GitHub](https://github.com/fosrl/pangolin)
- **Teleport Community**: su único conector SSO es GitHub. Google Workspace, Entra, Okta, OIDC y SAML son Enterprise — [Teleport feature matrix](https://goteleport.com/docs/feature-matrix/)
- **Teleport**: la autenticación OIDC/SAML/AD está limitada a "Enterprise (Cloud) and Enterprise (Self-Hosted)" — [Teleport FAQ](https://goteleport.com/docs/faq/)
- **Ory Oathkeeper**: su autenticación valida credenciales existentes (bearer token, basic auth, cookies) y "can work in isolation" o junto a Hydra y Keto. No incluye UI de login — [Oathkeeper docs](https://www.ory.com/docs/oathkeeper/)

### Inferences
- Para Google OIDC sin pagar, las opciones viables son Pomerium Core, oauth2-proxy, traefik-forward-auth (cualquier fork), Tinyauth y Pangolin Community con Google configurado como OIDC genérico global. Esto último es plausible porque Google es un OP OIDC estándar, pero no está verificado.
- Teleport Community queda descartado si el login debe ser con Google.
- Authelia solo sirve con Google si se coloca otro IdP delante (por ejemplo LDAP sincronizado), lo que lo hace mal candidato.

### Gaps
- No verifiqué que Pangolin Community acepte Google como "OIDC genérico global". Lo más probable es que sí, pero falta confirmarlo en la documentación.
- No revisé la lista completa de providers de oauth2-proxy (Keycloak, Entra, GitLab, etc.). De memoria es extensa, pero no está citada.

---

## 2. Autorización por app/ruta y por grupo: lenguaje de políticas, dónde viven, cambios dinámicos y decisión externa

### Takeaway
Pomerium tiene el lenguaje más rico (PPL), pero el criterio `groups` es Enterprise-only. Authelia ofrece reglas YAML ordenadas por dominio/recurso/sujeto. oauth2-proxy autoriza por grupo con query params en `/oauth2/auth`. Tinyauth define ACLs por app con labels de Docker. thomseddon solo restringe por email o dominio. El único que delega la decisión en un servicio externo de forma nativa es Oathkeeper (`remote_json`). Por eso, el patrón "PostgreSQL decide" no viene de serie en casi ninguno.

### Cited Findings
- **Pomerium PPL**: usa acciones `allow`/`deny` con `and`/`or`/`not`/`nor`, y "deny always takes precedence over allow" — [Pomerium PPL](https://www.pomerium.com/docs/internals/ppl)
- **Pomerium PPL, criterios por edición**:
  - Core: `accept`, `authenticated_user`, `claim`, `client_certificate`, `cors_preflight`, `device`, `domain`, `email`, `http_method`, `http_path`, `mcp_tool`, `pomerium_routes`, `reject`, `source_ip`, `user`
  - Zero/Enterprise: `date`, `day_of_week`, `time_of_day`
  - Enterprise-only: `groups`, `record` ("groups data is only available after a successful directory sync")
  - Rego custom es Enterprise
  — [Pomerium PPL](https://www.pomerium.com/docs/internals/ppl)
- **Pomerium (pricing)**: ABAC con PPL está en todos los planes. Rego es Enterprise-only — [Pomerium pricing](https://www.pomerium.com/pricing)
- **Pomerium Ingress Controller**: provisiona rutas dinámicamente desde recursos Ingress, con la política en annotations. La configuración global vive en el CRD `ingress.pomerium.io/Pomerium` — [Pomerium K8s reference](https://www.pomerium.com/docs/deploy/k8s/reference.md); [Pomerium K8s install](https://www.pomerium.com/docs/k8s/install)
- **oauth2-proxy**: `/oauth2/auth` responde 202 o 401 y acepta los query params `allowed_groups`, `allowed_email_domains` y `allowed_emails` (listas separadas por coma). Así se puede definir una regla por ruta o middleware. Si el usuario no está en el grupo, el proxy devuelve 403 — [oauth2-proxy endpoints](https://oauth2-proxy.github.io/oauth2-proxy/features/endpoints)
- **Authelia**: `access_control` tiene `default_policy` y `rules`. Cada regla admite `domain`/`domain_regex`, `policy` (`deny`/`bypass`/`one_factor`/`two_factor`), `subject` (`user:`, `group:`, `oauth2:client:`, con OR de AND), `methods`, `networks`, `resources` (regex) y `query`. Se aplica la primera regla que coincide y la documentación recomienda `default_policy: deny`. Para probar reglas existe `authelia access-control check-policy` — [Authelia access control](https://www.authelia.com/configuration/security/access-control/)
- **Authelia, recarga**: una fuente secundaria indica que `authentication_backend.file.watch = true` recarga el archivo de usuarios, y que sin ello se lee una sola vez al arrancar — [commit hyperhive (secundario)](https://git.berlin.ccc.de/vinzenz/hyperhive/commit/63fc54edc52f758965f71b79be4e859046fc3bbe)
- **Tinyauth**: las ACLs se declaran por app con `tinyauth.apps.[app].[key]` (labels de Docker) o `TINYAUTH_APPS_[APP]_[KEY]` (variables de entorno). Las claves disponibles son `users.allow/block`, `oauth.whitelist`, `oauth.groups`, `ldap.groups`, `path.allow/block` e `ip.allow/block/bypass`
  - Las labels son dinámicas en runtime. Las variables de entorno requieren reinicio.
  - `oauth.groups` no funciona con Google ni GitHub.
  - Política por defecto `allow` (configurable a `deny`).
  - Una app no configurada queda "unrestricted" para cualquier usuario autenticado.
  — [Tinyauth access controls](https://tinyauth.app/docs/guides/access-controls)
- **traefik-forward-auth (thomseddon)**: las reglas `rule.<name>.action|rule|provider|whitelist|domains` usan matchers de Traefik (`Host`, `PathPrefix`…). Solo hay whitelist de emails o dominios, sin soporte de grupos — [thomseddon README](https://github.com/thomseddon/traefik-forward-auth)
- **traefik-forward-auth (mesosphere)**: implementa RBAC opcional "within Kubernetes by using ClusterRole and ClusterRoleBinding", con `nonResourceURLs` como URLs y comodines `*`/`**` — [mesosphere README](https://github.com/mesosphere/traefik-forward-auth)
- **traefik-forward-auth (ItalyPaleAle)**: tiene una página "Authorization conditions" en su documentación, pero no pude leerla (HTTP 503) — [ItalyPaleAle README](https://github.com/ItalyPaleAle/traefik-forward-auth)
- **Pangolin**: control de acceso por usuario y rol a nivel de recurso ("Grant users access to specific resources, not entire networks") — [Pangolin GitHub](https://github.com/fosrl/pangolin)
- **Pangolin**: el auto-provisioning de usuarios con asignación de roles está disponible. Múltiples roles por usuario requiere Cloud o Enterprise — [Pangolin IdP docs](https://docs.pangolin.net/manage/identity-providers/add-an-idp)
- **Teleport**: las apps se etiquetan con labels (`apps[0].labels.env=dev`) y el rol `access` da acceso. El RBAC se basa en roles con labels — [Teleport app access getting started](https://goteleport.com/docs/enroll-resources/application-access/getting-started/)
- **Teleport Community**: las Access Requests solo se pueden hacer "through CLI" — [Teleport feature matrix](https://goteleport.com/docs/feature-matrix/)
- **Oathkeeper, authorizers**: `allow`, `deny`, `keto_engine_acp_ory`, `remote` y `remote_json`. `remote_json` hace POST JSON a un endpoint externo con un payload en Go template: 200 permite y 403 deniega — [Oathkeeper authz](https://www.ory.com/docs/oathkeeper/pipeline/authz)
- **Oathkeeper, almacenamiento de access rules**: filesystem, variable de entorno o HTTP(S) remoto, configurado con `ACCESS_RULES_REPOSITORIES` — [Oathkeeper access rules](https://www.ory.com/docs/oathkeeper/api-access-rules)
- **Oathkeeper, recarga de reglas remotas**: un foro de la comunidad reporta que "reload from HTTP is not implemented" (no confirmado oficialmente) — [Ory forum archive](https://archive.ory.sh/t/26930442/if-i-put-my-oathkeeper-rules-in-an-http-url-how-can-i-ask-oa)
- **Oathkeeper Maester**: es un controlador K8s que observa CRs `rules.oathkeeper.ory.sh/v1alpha1` y escribe un ConfigMap. Lo mantiene la comunidad, "not actively maintained by Ory core maintainers" — [oathkeeper-maester pkg.go.dev](https://pkg.go.dev/github.com/ory/oathkeeper-maester)

### Inferences
- **Lenguaje de reglas**: el modelo de Authelia (primera coincidencia, `default_policy: deny`, sujetos OR-de-AND por `group:`) y el de PPL (deny gana sobre allow) son patrones probados que conviene copiar en el modelo de datos de PostgreSQL. Por ejemplo: tabla `app_access_rule(app_id, subject_type, subject_value, effect)` con deny prioritario y un default deny.
- **Consulta a nuestra base de datos**: el `remote_json` de Oathkeeper es exactamente el patrón "un servicio externo decide". Si se reusara Oathkeeper, el platform-api solo expondría el endpoint de decisión. Aun así, Oathkeeper no hace login.
- **Lección de Tinyauth**: su fallo abierto ("si la app no está configurada, cualquier autenticado pasa") es un anti-patrón que conviene evitar explícitamente con default deny por subdominio.

### Gaps
- No verifiqué la sintaxis exacta de las annotations de política del Pomerium Ingress Controller (`ingress.pomerium.io/policy`).
- No verifiqué si Pomerium Core recarga `config.yaml` en caliente.
- No pude leer las "Authorization conditions" del fork ItalyPaleAle (HTTP 503).
- Ninguna fuente documenta un hot reload oficial de `access_control` en Authelia.

---

## 3. Propagación de identidad al upstream: headers planos vs JWT firmado (claims, JWKS, aud por ruta)

### Takeaway
Solo Pomerium, Teleport y Oathkeeper emiten su propio JWT firmado con JWKS publicado. Ese es el modelo que queremos. oauth2-proxy, Authelia, Tinyauth, traefik-forward-auth y Pangolin envían headers planos (`X-Auth-Request-*`, `Remote-*`, `X-Forwarded-User`), que obligan a confiar en la red. Algunos pueden reenviar el id_token crudo del IdP, que no es un token con `aud` por app.

### Cited Findings
- **Pomerium**: envía el header `X-Pomerium-Jwt-Assertion` (RFC 7519)
  - Claims: `exp`, `iat`, `sub`, `aud`, `iss`, `email`, `groups` ("if available") y `name`.
  - "The original ID token from your IdP is never forwarded": Pomerium emite un token nuevo.
  - JWKS en `https://<dominio-ruta>/.well-known/pomerium/jwks.json` con selección por `kid`.
  - Firma con clave EC configurable en `SIGNING_KEY`.
  - El verificador debe validar `aud` e `iss` = dominio del servicio, además de `exp`.
  — [Pomerium identity verification](https://www.pomerium.com/docs/capabilities/getting-users-identity)
- **Teleport**: envía el header `Teleport-Jwt-Assertion`, firmado por la CA de Teleport
  - Claims: `aud` (dirección de la app), `iss`, `sub`, `username`, `roles`, `traits`, `nbf` y `exp`.
  - JWKS en `https://[cluster]/.well-known/jwks.json`.
  - Se puede inyectar con `rewrite.headers` como `Authorization: Bearer {{internal.jwt}}`.
  - `jwt_claims` admite `roles-and-traits`, `roles`, `traits` o `none`.
  — [Teleport JWT](https://goteleport.com/docs/enroll-resources/application-access/jwt/introduction/)
- **Oathkeeper**: el mutator `id_token` genera un JWT firmado (OIDC ID Token)
  - Opciones: `jwks_url` (archivo o URL), `issuer_url` y `ttl` (por defecto 1 minuto).
  - Claims con Go templates, incluido `aud` por regla.
  - Existen además mutators `header` y `cookie`, y `hydrator`, que enriquece la sesión llamando a una API externa.
  — [Oathkeeper mutators](https://www.ory.com/docs/oathkeeper/pipeline/mutator)
- **Authelia**: envía `Remote-User`, `Remote-Groups`, `Remote-Name` y `Remote-Email` como headers planos
  - El proxy "must ensure only Authelia is setting these headers".
  - En Kubernetes las IPs de pod no son estáticas y la documentación sugiere un service mesh.
  - No se menciona ningún JWT firmado hacia el upstream.
  — [Authelia trusted header SSO](https://www.authelia.com/integration/trusted-header-sso/introduction/)
- **oauth2-proxy**: `/oauth2/auth` documenta solo códigos de estado, sin headers — [oauth2-proxy endpoints](https://oauth2-proxy.github.io/oauth2-proxy/features/endpoints)
- **oauth2-proxy con Traefik**: guías comunitarias configuran `authResponseHeaders` con `X-Auth-Request-User`, `X-Auth-Request-Email` y `X-Auth-Request-Groups`. Algunas usan `X-Forwarded-Groups` — [stackharbor (EN/FR)](https://stackharbor.com/en/knowledge-base/traefik-oauth2-proxy-integration/); [Traefik community](https://community.traefik.io/t/solved-forwardauth-with-oauth2-proxy/27718)
- **Tinyauth**: envía `Remote-User`, `Remote-Email`, `Remote-Name`, `Remote-Groups` (solo con OIDC que tenga claim `groups`) y `Remote-Sub`, sin JWT — [Tinyauth headers](https://tinyauth.app/docs/reference/headers)
- **traefik-forward-auth (thomseddon)**: envía la identidad en `X-Forwarded-User` vía `authResponseHeaders` — [thomseddon README](https://github.com/thomseddon/traefik-forward-auth)
- **traefik-forward-auth (mesosphere)**: "The raw id-token received from OIDC provider can optionally be passed upstream via a custom header" — [mesosphere README](https://github.com/mesosphere/traefik-forward-auth)
- **Pangolin**: puede enviar la identidad en headers personalizados, entre ellos `Remote-User`. Solo funciona con métodos que aportan identidad (SSO), no con PIN — [Pangolin forwarded headers](https://docs.pangolin.net/manage/access-control/forwarded-headers)
- **Pangolin (Badger)**: una release note antigua dice que "now always set Remote-* headers" — [badger releases](https://github.com/fosrl/badger/releases)

### Inferences
- El diseño "JWT con `aud` por app + JWKS" coincide con Pomerium (aud = dominio de la ruta), Teleport (aud = URI de la app) y Oathkeeper (aud en el template de claims). Conviene copiar estos detalles:
  - Un header dedicado (`X-<Plataforma>-Jwt-Assertion`) que no reemplace el `Authorization` de la app.
  - Nunca reenviar el id_token de Google.
  - TTL corto, de alrededor de 1 minuto como en Oathkeeper.
  - JWKS en `/.well-known/...` con `kid` para rotación.
  - Claims mínimos: `sub`, `email`, `name`, `groups` y, opcionalmente, `roles`.
- Si se usaran headers planos al estilo Authelia o oauth2-proxy, cada app tendría que confiar en la red. En K3s esto obliga a NetworkPolicies, por la advertencia de Authelia sobre IPs de pod.

### Gaps
- No verifiqué con fuente primaria los flags de oauth2-proxy para inyectar headers (`--set-xauthrequest`, `--pass-access-token`, `--set-authorization-header`): la página de integración devolvió 404. De memoria, oauth2-proxy reenvía el id_token o access token del IdP y no firma un JWT propio. Hay que confirmarlo.
- No verifiqué si Pomerium permite configurar el `aud` por ruta de forma explícita o si siempre es el host.

---

## 4. Sesión y SSO entre subdominios: cookie domain, dominio central de auth y logout

### Takeaway
El patrón dominante es tener un host de auth central (callback único registrado en Google) más una cookie emitida en el dominio padre (`.apps.empresa.cl`). Los diseños stateless con cookie firmada (thomseddon, oauth2-proxy en modo cookie) no pueden revocar sesiones en el servidor. Authelia y oauth2-proxy con Redis sí pueden.

### Cited Findings
- **traefik-forward-auth (thomseddon)**: tiene dos modos
  - Overlay: callback `/_oauth` en cada dominio, que obliga a registrar cada dominio en el IdP.
  - Auth Host (`auth-host`): "useful when dealing with a large number of subdomains", con un solo host registrado.
  
  `cookie-domain` emite la cookie en el dominio padre. El logout en `/_oauth/logout` solo borra la cookie: "a copied cookie stays valid until it expires" (stateless, `lifetime` por defecto 12 h) — [thomseddon README](https://github.com/thomseddon/traefik-forward-auth)
- **Authelia**: `session.cookies` es una lista de dominios, cada uno con `domain` y `authelia_url` obligatorios. El dominio debe ser sufijo del host de `authelia_url` y no puede ser un public suffix. Los timers por defecto son:
  - `inactivity`: 5 min
  - `expiration`: 1 h
  - `remember_me`: 1 mes
  
  Proveedores de sesión: memoria (stateful, por defecto), Redis y Redis Sentinel. "the stateful providers are not recommended in High Availability scenarios like Kubernetes" — [Authelia session](https://www.authelia.com/configuration/session/introduction/)
- **oauth2-proxy, almacenamiento de sesión**:
  - Cookie (por defecto, stateless): sin locking y con JWT grande.
  - Redis: el navegador solo recibe un ticket `{CookieName}-{ticketID}.{secret}` de 128+128 bits y la sesión se cifra en Redis con `SETEX`.
  - Soporta Sentinel y Cluster.
  — [oauth2-proxy session storage](https://oauth2-proxy.github.io/oauth2-proxy/configuration/session_storage)
- **oauth2-proxy, logout**: `/oauth2/sign_out` "only removes oauth2-proxy's own cookies". El logout en el IdP se hace con `rd=` (URL-encoded), el dominio destino debe estar en `--whitelist-domain` y admite `{id_token}` para id_token_hint — [oauth2-proxy endpoints](https://oauth2-proxy.github.io/oauth2-proxy/features/endpoints)
- **oauth2-proxy con Traefik**: hace falta un middleware `errors` que capture el 401 y redirija a `/oauth2/start?rd={url}`, porque Traefik no convierte el 401 en redirect como hace nginx — [Traefik community](https://community.traefik.io/t/solved-forwardauth-with-oauth2-proxy/27718); [stackharbor FR](https://stackharbor.com/fr/base-de-connaissances/traefik-oauth2-proxy-integration/)
- **Teleport**: cada app se sirve en `https://<app>.<teleport-domain>`, lo que exige DNS y certificado wildcard (`*.teleport.example.com`) — [Teleport app access getting started](https://goteleport.com/docs/enroll-resources/application-access/getting-started/)
- **Pomerium**: hay un portal de rutas en el dominio de Pomerium (`/.pomerium/routes`). Un usuario no autenticado es redirigido al IdP — [Pomerium routes portal](https://main.docs.pomerium.com/docs/capabilities/routes-portal)
- **Tinyauth**: asocia cada request a una app según el subdominio y depende de que el proxy fije `X-Forwarded-Host` — [Tinyauth access controls](https://tinyauth.app/docs/guides/access-controls)

### Inferences
- **Diseño recomendado para `*.apps.empresa.cl`**:
  - Host central `auth.apps.empresa.cl` como único redirect URI en Google.
  - Cookie de sesión opaca en `.apps.empresa.cl`, con id de sesión respaldado en PostgreSQL o Redis para permitir revocación real.
  - JWT por app emitido en cada forward-auth.
  
  Es la combinación de "auth host" de thomseddon con el ticket de Redis de oauth2-proxy.
- **Logout**: debe invalidar la sesión en el servidor, cosa que los diseños stateless no logran, y opcionalmente redirigir al logout de Google.

### Gaps
- No verifiqué la estrategia de cookies de Pomerium Core: cookie en el dominio de autenticación, más un "programmatic" o cookie por ruta.
- No revisé cómo maneja Teleport la cookie de sesión por subdominio de app.
- No revisé el modelo de sesión de Pangolin.

---

## 5. Grupos de Google Workspace (Directory API / service account): ¿OSS o pago?

### Takeaway
oauth2-proxy es el único de la lista que obtiene grupos de Google Workspace en su versión 100% OSS: usa el Admin SDK con un service account y delegación de dominio. En Pomerium, los grupos (directory sync) son Enterprise. En Tinyauth no funcionan con Google. En Pangolin, el conector Google es Enterprise. En Teleport, Google Workspace es Enterprise. Google OIDC no entrega un claim `groups` por sí solo, así que guardar los grupos en PostgreSQL (nuestro diseño) evita depender de esto.

### Cited Findings
- **oauth2-proxy**: usa el Admin SDK impersonando a `--google-admin-email`, con domain-wide delegation y scope `https://www.googleapis.com/auth/admin.directory.group.member.readonly`
  - Credenciales: JSON de service account o ADC/Workload Identity (`--google-use-application-default-credentials`, `--google-target-principal`).
  - La membresía se verifica en el login y en cada refresh (alrededor de 1 h). La documentación recomienda `cookie-refresh` de 1 h.
  — [oauth2-proxy Google provider](https://oauth2-proxy.github.io/oauth2-proxy/configuration/providers/google)
- **oauth2-proxy (documentación de forks v3.x)**: "Create or choose an existing email group and set that email to the google-group flag" [búsqueda JA, fuentes EN] — [BTLzdravtech fork pkg.go.dev](https://pkg.go.dev/github.com/BTLzdravtech/oauth2-proxy)
- **Pomerium**: "Directory Sync is a Pomerium Enterprise feature" y permite importar grupos del IdP — [Pomerium directory sync](https://pomerium.com/docs/capabilities/directory-sync); [Pomerium Enterprise](https://www.pomerium.com/docs/deploy/enterprise.md)
- **Pomerium (pricing)**: directory sync de usuarios y grupos solo en Enterprise. Zero Business ($7–9/usuario/mes) no lo incluye — [Pomerium pricing](https://www.pomerium.com/pricing)
- **Pomerium PPL**: el criterio `groups` es Enterprise-only — [Pomerium PPL](https://www.pomerium.com/docs/internals/ppl)
- **Tinyauth**: `oauth.groups` funciona con OIDC custom que tenga scope `groups`, "but not Google or GitHub" — [Tinyauth access controls](https://tinyauth.app/docs/guides/access-controls)
- **Pangolin**: el conector Google Workspace es solo Cloud o Enterprise — [Pangolin IdP docs](https://docs.pangolin.net/manage/identity-providers/add-an-idp)
- **Pangolin**: puede "sync users and roles from an IdP" — [Pangolin GitHub](https://github.com/fosrl/pangolin)
- **Teleport**: Google Workspace aparece entre los conectores SSO de Enterprise — [Teleport feature matrix](https://goteleport.com/docs/feature-matrix/)

### Inferences
- **Patrón OSS a copiar**: el de oauth2-proxy, es decir, service account con domain-wide delegation, scope `admin.directory.group.member.readonly` y revalidación en cada refresh (~1 h). Si en el futuro queremos sincronizar grupos de Google en lugar de gestionarlos en PostgreSQL, se puede implementar en Go con `google.golang.org/api/admin/directory/v1`.
- **Fuente de verdad**: mantener los grupos en PostgreSQL desacopla la plataforma del tier pago de cualquier IAP.

### Gaps
- No verifiqué si Authelia u Oathkeeper tienen alguna integración con Google Groups. Lo esperable es que no, porque Authelia no se integra con Google y Oathkeeper no hace login.

---

## 6. Portal / catálogo de apps / dashboard para usuarios

### Takeaway
Pomerium (`/.pomerium/routes`, con una API JSON), Pangolin (Resource Launcher) y Teleport (pestaña Applications con "Launch") traen un catálogo filtrado por permisos. oauth2-proxy, Authelia, Tinyauth, traefik-forward-auth y Oathkeeper no tienen catálogo de apps.

### Cited Findings
- **Pomerium**: el Routes Portal en `/.pomerium/routes` muestra las rutas autorizadas para el usuario. La misma información se expone como JSON en `/.pomerium/api/v1/routes`, y para TCP/SSH incluye `connect_command` — [Pomerium routes portal](https://main.docs.pomerium.com/docs/capabilities/routes-portal); [v0.32 docs](https://0-32-0.docs.pomerium.com/docs/capabilities/routes-portal)
- **Pomerium**: la página de pricing no menciona el routes portal, y no confirmé si está en Core o solo en las versiones pagas — [Pomerium pricing](https://www.pomerium.com/pricing)
- **Pangolin**: ofrece una "personalized home page (Resource Launcher)" donde los usuarios encuentran sus recursos — [Pangolin GitHub](https://github.com/fosrl/pangolin)
- **Teleport**: en la pestaña Applications, el usuario ve "All available applications" y abre cada una con **Launch** — [Teleport app access getting started](https://goteleport.com/docs/enroll-resources/application-access/getting-started/)

### Inferences
- **Copiar el patrón de Pomerium**: un endpoint JSON con las apps autorizadas para el usuario actual, que el portal React/SPA consume, además de una página HTML. Encaja directamente con nuestro portal y la tabla grupo -> app de PostgreSQL.

### Gaps
- La documentación no aclara si el Routes Portal está disponible en Pomerium Core open source. Es probable, porque la documentación vNext lo trata como capacidad general, pero no está verificado.

---

## 7. Auditoría de decisiones de acceso

### Takeaway
Pomerium Core registra cada decisión ("authorize check", con allow/deny y su motivo) en logs estructurados. Teleport Community trae audit log estructurado con exportación a SIEM. El resto (oauth2-proxy, Authelia, Tinyauth, traefik-forward-auth y Oathkeeper) solo deja logs de aplicación; no encontré auditoría de decisiones documentada como función.

### Cited Findings
- **Pomerium**: por cada request emite un log con mensaje `authorize check`. Campos: `allow`, `allow-why-true/false`, `deny`, `deny-why-false`, `user`, `email`, `session-id`, `request-id` y `check-request-id`
  - Son configurables con `authorize_log_fields` (`authorizeLogFields` en K8s).
  - La documentación advierte no loguear `body` ni `query` por la información sensible que contienen.
  — [Pomerium authorize log fields](https://www.pomerium.com/docs/reference/authorize-log-fields)
- **Pomerium Zero/Enterprise**: incluyen logs de acceso y autorización y logs de auditoría administrativa. La grabación de sesiones SSH es solo Enterprise — [Pomerium pricing](https://www.pomerium.com/pricing)
- **Teleport Community**: incluye Structured Audit Logs, Export to SIEM, grabación de sesiones con playback y eventos a nivel de protocolo — [Teleport feature matrix](https://goteleport.com/docs/feature-matrix/)

### Inferences
- **Patrón a copiar de Pomerium**: un log estructurado por decisión, con un request-id correlacionable y la razón (`allow-why`/`deny-why`), más un allowlist de campos que excluya body y query. En nuestro caso se puede escribir de forma asíncrona a una tabla `access_decision_log` en PostgreSQL o enviar a Loki.

### Gaps
- No encontré documentación de auditoría de decisiones de Authelia, oauth2-proxy, Tinyauth, Pangolin ni Oathkeeper. Pangolin probablemente tenga logs en el dashboard, pero no está verificado.

---

## 8. Operación en Kubernetes: ForwardAuth de Traefik vs reverse proxy propio, Helm, CRDs, footprint, HA y estado

### Takeaway
Los que se integran limpiamente como ForwardAuth de Traefik son oauth2-proxy (con middleware `errors`), Authelia, Tinyauth, traefik-forward-auth y Oathkeeper (decisions API). Pomerium descontinuó forward-auth en v0.21 y hoy exige ser el proxy o ingress, reemplazando a Traefik para las rutas protegidas. Pangolin trae su propio Traefik con el plugin Badger y túneles WireGuard (Newt). Teleport es su propio proxy con un agente. El estado se guarda en Redis (oauth2-proxy, Authelia), en PostgreSQL (Pomerium databroker) o en nada (thomseddon).

### Cited Findings
- **Pomerium**: "Starting v0.21.0, Pomerium will no longer support Forward Auth", porque va en contra de los principios BeyondCorp. El guide de Traefik que todavía describe forward-auth está desactualizado — [Pomerium v0.21 announcement](https://pomerium.com/blog/announcing-pomerium-v0-21); [Pomerium Traefik guide (desactualizado)](https://pomerium.com/guides/traefik-ingress)
- **Pomerium Ingress Controller**: se instala con kustomize (CRDs + RBAC)
  - Requiere PostgreSQL 11+ para más de una réplica.
  - El CRD `Pomerium` tiene una sección `storage` (en memoria si no se configura).
  - El databroker guarda también la configuración.
  — [Pomerium K8s install](https://www.pomerium.com/docs/k8s/install); [Pomerium databroker](https://0-27-0.docs.pomerium.com/docs/reference/databroker); [Pomerium architecture](https://www.pomerium.com/docs/internals/architecture.md)
- **Pomerium Ingress Controller (repo)**: 37 estrellas, release v0.33.4 del 2026-10-02 — [pomerium/ingress-controller](https://github.com/pomerium/ingress-controller)
- **Pomerium**: existe un ejemplo antiguo de Traefik 2.2 + Pomerium 0.7.5 en ambos modos, obsoleto [búsqueda FR] — [8gears example](https://github.com/8gears/Traefik-with-Pomerium-Forward-Auth-and-Proxy-on-Kubernetes-with-Helm)
- **Pomerium**: hay una guía de despliegue GitOps con Flux de marzo de 2026 — [oneuptime](https://oneuptime.com/blog/post/2026-03-13-deploy-pomerium-access-proxy-with-flux-cd/markdown)
- **oauth2-proxy con Traefik**: el ForwardAuth contra `/oauth2/auth` devuelve 401 sin redirect, y la solución es un middleware `errors` hacia `/oauth2/start` — [Traefik community](https://community.traefik.io/t/solved-forwardauth-with-oauth2-proxy/27718)
- **oauth2-proxy, HA**: Redis Sentinel o Cluster — [oauth2-proxy session storage](https://oauth2-proxy.github.io/oauth2-proxy/configuration/session_storage)
- **Authelia**: tiene guías para Traefik (incluida Kubernetes), Envoy y NGINX — [Authelia supported proxies](https://www.authelia.com/overview/prologue/supported-proxies/)
- **Authelia, Helm**: el chart oficial está en "beta" y puede tener breaking changes (charts.authelia.com) — [Authelia Helm chart](https://authelia.com/integration/kubernetes/chart)
- **Authelia, HA**: en Kubernetes se recomiendan proveedores de sesión stateless (Redis/Sentinel) — [Authelia session](https://www.authelia.com/configuration/session/introduction/)
- **Authelia, footprint**: alrededor de 20–25 MB de RAM según sus desarrolladores [búsqueda ZH; cifra no verificada en fuente primaria] — [Help Net Security](https://www.helpnetsecurity.com/?p=301387)
- **Tinyauth**: "supports all the popular proxies like Traefik, Nginx and Caddy" — [Tinyauth GitHub](https://github.com/tinyauthapp/tinyauth)
- **Tinyauth**: las ACLs dinámicas dependen de labels de Docker y requieren montar el socket. La documentación de ACL no cubre Kubernetes — [Tinyauth access controls](https://tinyauth.app/docs/guides/access-controls)
- **Tinyauth, seguridad**: no debe ser accesible directamente, porque confía en `X-Forwarded-Host` — [Tinyauth access controls](https://tinyauth.app/docs/guides/access-controls)
- **traefik-forward-auth (thomseddon)**: stateless con cookie firmada por `secret`. Varias instancias en el mismo dominio pueden chocar en cookies si no comparten nombre o secret — [thomseddon README](https://github.com/thomseddon/traefik-forward-auth)
- **Pangolin**: el instalador 1.14.0+ añade Badger a todas las rutas de Traefik — [Pangolin forwarded headers](https://docs.pangolin.net/manage/access-control/forwarded-headers)
- **Pangolin (Badger)**: las versiones v1.6.0 y v1.6.1 reportan fallos de verificación de integridad con el plugin catalog de Traefik. Se recomendaba v1.5.0 (puede estar desactualizado) — [badger releases](https://github.com/fosrl/badger/releases)
- **Pangolin, Helm de Newt** (conector de sitio):
  - Chart 1.4.0, app 1.12.3, K8s >=1.30.14, en charts.fossorial.io o OCI `ghcr.io/fosrl/helm-charts/newt`.
  - Ofrece multi-instancia, métricas y NetworkPolicy.
  - Existe un "Pangolin Kubernetes Controller" aparte.
  — [Pangolin Newt Helm](https://docs.pangolin.net/self-host/manual/kubernetes/newt/helm); [Pangolin K8s Helm](https://docs.pangolin.net/self-host/manual/kubernetes/helm)
- **Teleport**: el agente para K8s es el chart `teleport/teleport-kube-agent` con `roles=app`, `proxyAddr`, `authToken` y `apps[].name/uri` — [Teleport app access getting started](https://goteleport.com/docs/enroll-resources/application-access/getting-started/)
- **Teleport Community**: no soporta "multi-region failover" — [Teleport feature matrix](https://goteleport.com/docs/feature-matrix/)
- **Oathkeeper**: funciona como reverse proxy o como "Access Control Decision API", donde 200 permite y cualquier otro código deniega. Se puede conectar a gateways como PDP — [Oathkeeper docs](https://www.ory.com/docs/oathkeeper/)
- **Oathkeeper Maester**: Helm chart con un CRD Rule, mantenido por la comunidad — [Oathkeeper-maester Helm chart](https://k8s.ory.sh/helm/oathkeeper-maester.html); [pkg.go.dev](https://pkg.go.dev/github.com/ory/oathkeeper-maester)
- **Traefik ForwardAuth (afecta a todos los que lo usan, incluido nuestro diseño)**: CVE-2026-54764, publicado el 2026-07-06
  - ForwardAuth deriva `X-Forwarded-Port` de la request original aun con `trustForwardHeader: false`.
  - Un atacante puede falsear `X-Forwarded-Proto` por HTTP y saltarse checks de autorización basados en puerto.
  - Corregido en Traefik v2.11.51, v3.6.22 y v3.7.6. CVSS 4.0: 6.9.
  — [Vulert](https://vulert.com/vuln-db/CVE-2026-54764); [INCIBE [ES]](https://www.incibe.es/incibe-cert/alerta-temprana/vulnerabilidades/cve-2026-54764); [Debian tracker](https://security-tracker.debian.org/tracker/CVE-2026-54764)

### Inferences
- **Con K3s + Traefik ya instalado**: reusar Pomerium implica que Pomerium sea el proxy de las apps (Ingress Controller propio o Traefik delante enviando todo a Pomerium). Agrega un salto y cambia la arquitectura de ingress.
- **Encaje natural con Traefik ForwardAuth**: oauth2-proxy, Authelia, Tinyauth, traefik-forward-auth, Oathkeeper y nuestro platform-api.
- **Por la CVE**: la versión de Traefik de K3s debe estar parcheada (>= v3.6.22 o v3.7.6). El platform-api no debe tomar decisiones basadas en `X-Forwarded-Proto` o `X-Forwarded-Port`, y debe decidir por `X-Forwarded-Host` con un allowlist estricto de hosts.

### Gaps
- No obtuve cifras de footprint (RAM/CPU) de Pomerium, oauth2-proxy, Tinyauth, Oathkeeper ni Teleport.
- No verifiqué el Helm chart oficial de oauth2-proxy (existe `oauth2-proxy/manifests`, pero no lo consulté).
- No verifiqué el backend de estado de Teleport (etcd, DynamoDB, Postgres, Firestore).

---

## 9. Licencias, separación OSS/Enterprise y salud del proyecto (a 2026-10-14)

### Takeaway
- **Licencias permisivas**: Pomerium Core (Apache-2.0), oauth2-proxy (MIT), Authelia (Apache-2.0), thomseddon/ItalyPaleAle (MIT) y Oathkeeper (Apache-2.0).
- **AGPL-3.0**: Tinyauth y Pangolin CE.
- **Teleport**: el código es AGPL-3.0, pero los binarios Community tienen una licencia comercial gratuita solo para empresas con menos de 100 empleados y menos de USD 10M de ingresos.
- **Pangolin**: dual license. La edición Enterprise usa la Fossorial Commercial License, gratuita para empresas con menos de USD 100K de ingresos.
- **thomseddon**: sin release desde mayo de 2024.

### Cited Findings
Datos de la API de GitHub consultada el 2026-10-14:

| Proyecto | Estrellas | Licencia | Última release |
|---|---|---|---|
| [pomerium/pomerium](https://github.com/pomerium/pomerium) | 5.043 | Apache-2.0 | v0.33.4 (2026-10-02) |
| [oauth2-proxy/oauth2-proxy](https://github.com/oauth2-proxy/oauth2-proxy) | 15.180 | MIT | v7.15.5 (2026-10-01) |
| [authelia/authelia](https://github.com/authelia/authelia) | 29.263 | Apache-2.0 | v4.39.28 (2026-09-17) |
| [thomseddon/traefik-forward-auth](https://github.com/thomseddon/traefik-forward-auth) | 2.391 | MIT | v2.3.0 (2024-05-06) |
| [ItalyPaleAle/traefik-forward-auth](https://github.com/ItalyPaleAle/traefik-forward-auth) | 123 | MIT | sin release en la API |
| [mesosphere/traefik-forward-auth](https://github.com/mesosphere/traefik-forward-auth) | 253 | no clara | sin releases |
| [jordemort/traefik-forward-auth](https://github.com/jordemort/traefik-forward-auth) | 42 | n/d | sin releases |
| [tinyauthapp/tinyauth](https://github.com/tinyauthapp/tinyauth) | 8.376 | AGPL-3.0 | v5.2.0 (2026-09-07) |
| [fosrl/pangolin](https://github.com/fosrl/pangolin) | 23.087 | dual (NOASSERTION en API) | 1.24.0 (2026-09-30) |
| [gravitational/teleport](https://github.com/gravitational/teleport) | 20.989 | AGPL-3.0 (código) | v18.10.0 (2026-07-09) |
| [ory/oathkeeper](https://github.com/ory/oathkeeper) | 3.609 | Apache-2.0 | v26.2.0 (2026-03-20) |

- **Actividad reciente**:
  - Pomerium, oauth2-proxy, Authelia, Tinyauth y Pangolin tenían push el 2026-10-13 o 2026-10-14.
  - Thomseddon: último push el 2026-04-03.
  - ItalyPaleAle: push el 2026-10-12, sigue activo.
  - Mesosphere: push el 2026-09-16; Releases y Packages aparecen vacíos.
  - Jordemort: último push el 2023-10-26, efectivamente abandonado.
  
  — [ItalyPaleAle](https://github.com/ItalyPaleAle/traefik-forward-auth); [mesosphere](https://github.com/mesosphere/traefik-forward-auth); [jordemort](https://github.com/jordemort/traefik-forward-auth)
- **Tinyauth**: el repo se movió de `steveiliop56/tinyauth` a `tinyauthapp/tinyauth` (la API devuelve 301) — [tinyauthapp/tinyauth](https://github.com/tinyauthapp/tinyauth)
- **Tinyauth, avisos del README**: "Tinyauth is in active development and configuration may change often". Licencia AGPL-3.0 — [Tinyauth GitHub](https://github.com/tinyauthapp/tinyauth)
- **Tinyauth, conflicto de licencia**: un blog [DE search] afirma GPL-3.0 sin tier pago, mientras que el repo dice AGPL-3.0. Prevalece el repo — [sumguy.com](https://sumguy.com/tinyauth-vs-pocket-id-vs-authelia/)
- **Pangolin, licencia**: "dual licensed under the AGPL-3 and the Fossorial Commercial License". La CE es AGPL-3. La EE es open-core y "Free for personal and hobbyist use, and for businesses making less than $100K USD gross annual revenue" — [Pangolin GitHub](https://github.com/fosrl/pangolin)
- **Pangolin, cambio de licencia**: anunciado el 23 de octubre de 2025. Las empresas que no califican deben comprar una licencia o quedarse en CE — [Linuxiac](https://linuxiac.com/pangolin-reverse-proxy-moves-to-dual-licensing-with-new-enterprise-edition/)
- **Pangolin, fork AGPL**: un fork (ThetaDev) elimina el código propietario y reimplementa funciones bajo AGPL. SourceForge lista "MIT" por error [búsqueda ES] — [ThetaDev fork](https://code.thetadev.de/ThetaDev/pangolin/src/branch/oss/README.md); [SourceForge](https://sourceforge.net/mirror/pangolin/)
- **Teleport, licencia**: los binarios Community tienen "Free usage for companies with <100 employees and <US$10M annual revenue". El código fuente es AGPL-3.0 — [Teleport feature matrix](https://goteleport.com/docs/feature-matrix/)
- **Teleport, funciones fuera de Community**: SSO más allá de GitHub, Device Trust, session locks, IP restrictions, Identity Governance, revisiones JIT de access requests y multi-region failover — [Teleport feature matrix](https://goteleport.com/docs/feature-matrix/)
- **Teleport, repositorio público** (observación propia vía API de GitHub): el último commit es del 2026-08-25, con mensaje "Linux desktop access documentation (#490)" y trailer `Export-Source-Commit`. Sugiere que el repo público se alimenta por exportación desde otra fuente; no encontré confirmación oficial. Las releases públicas más recientes son v18.10.0 (2026-07-09) y v18.9.2 (2026-06-29) — [gravitational/teleport commits](https://github.com/gravitational/teleport/commits)
- **Pomerium, planes**:
  - Zero Personal: gratis, 10 rutas y 5 políticas.
  - Zero Business: USD 9/usuario/mes mensual o 7 anual, 100 rutas, sin directory sync.
  - Enterprise: precio a medida, con directory sync, device identity, Rego y SSH recording.
  — [Pomerium pricing](https://www.pomerium.com/pricing)
- **Oathkeeper Maester**: no lo mantienen los core maintainers de Ory — [oathkeeper-maester](https://pkg.go.dev/github.com/ory/oathkeeper-maester)
- **Oathkeeper, versionado**: pasó a versiones de calendario (v26.2.0, 2026-03-20) — [ory/oathkeeper](https://github.com/ory/oathkeeper)

### Inferences
- **Para una empresa (CIC)**:
  - Teleport Community probablemente no es usable legalmente si la empresa supera 100 empleados o USD 10M; habría que compilar desde el código AGPL o pagar.
  - Pangolin EE requiere licencia si se superan USD 100K de ingresos, y la CE pierde Google, IdPs por organización y múltiples roles por usuario.
  - Pomerium Core es libre, pero los grupos son de pago.
- **Lo plenamente libre y activo**: oauth2-proxy, Authelia, Oathkeeper y Pomerium Core sin grupos.
- **AGPL (Tinyauth, Pangolin CE)**: es aceptable para uso interno sin modificar. Si se modifica y se expone por red, obliga a publicar el código.
- **traefik-forward-auth**: thomseddon está en mantenimiento mínimo. El fork vivo más útil es ItalyPaleAle (v4, MIT).

### Gaps
- No verifiqué el número de maintainers activos por proyecto.
- No verifiqué el estado de Teleport v19 ni si hubo releases posteriores a v18.10.0 fuera de GitHub.
- No confirmé la explicación oficial del trailer `Export-Source-Commit` en el repo de Teleport.

---

## 10. Encaje con nuestro caso: reusar tal cual, reusar parcialmente o copiar ideas

### Takeaway
Ninguna solución cubre a la vez, de forma OSS y sobre Traefik ForwardAuth, nuestros tres requisitos: Google OIDC, autorización desde PostgreSQL y JWT firmado con `aud` por app. Lo más razonable es construir el platform-api propio copiando decisiones probadas: el JWT con JWKS de Pomerium/Teleport, las reglas deny-first de Authelia/PPL, el host central con cookie en el dominio padre de thomseddon, Redis o PostgreSQL para sesiones revocables como oauth2-proxy, el Admin SDK de oauth2-proxy para grupos de Google y el `remote_json`/`id_token` de Oathkeeper.

### Cited Findings
- **Pomerium Core**: tiene el JWT firmado con `aud` + JWKS que queremos — [Pomerium identity](https://www.pomerium.com/docs/capabilities/getting-users-identity). En contra: no hace forward-auth desde v0.21 — [v0.21](https://pomerium.com/blog/announcing-pomerium-v0-21), y los grupos son Enterprise — [PPL](https://www.pomerium.com/docs/internals/ppl)
- **oauth2-proxy**: soporta Google con grupos OSS — [Google provider](https://oauth2-proxy.github.io/oauth2-proxy/configuration/providers/google) y autorización por grupo con query params por middleware — [endpoints](https://oauth2-proxy.github.io/oauth2-proxy/features/endpoints). No hay evidencia de que emita un JWT propio con `aud` por app (ver Gaps de la sección 3).
- **Authelia**: no hace login con Google — [roadmap](https://www.authelia.com/roadmap/prologue/introduction/) y propaga identidad con headers planos — [trusted headers](https://www.authelia.com/integration/trusted-header-sso/introduction/)
- **Oathkeeper**: `remote_json` delega la decisión a un servicio externo — [authz](https://www.ory.com/docs/oathkeeper/pipeline/authz) y `id_token` firma un JWT con `aud` configurable — [mutators](https://www.ory.com/docs/oathkeeper/pipeline/mutator). No tiene login — [docs](https://www.ory.com/docs/oathkeeper/)
- **Teleport**: emite JWT por app y tiene catálogo — [JWT](https://goteleport.com/docs/enroll-resources/application-access/jwt/introduction/). Pero Google SSO es Enterprise y la licencia limita a empresas pequeñas — [feature matrix](https://goteleport.com/docs/feature-matrix/)
- **Pangolin**: tiene dashboard y RBAC por recurso, pero Google es EE, la licencia es dual y trae su propio Traefik con Badger — [IdP](https://docs.pangolin.net/manage/identity-providers/add-an-idp); [GitHub](https://github.com/fosrl/pangolin)
- **Tinyauth**: no tiene grupos con Google, sus ACLs se basan en labels de Docker y la configuración "may change often" — [ACL](https://tinyauth.app/docs/guides/access-controls); [GitHub](https://github.com/tinyauthapp/tinyauth)

### Inferences

**Veredicto por solución:**

| Solución | Veredicto | Razón principal |
|---|---|---|
| Pomerium Core | Reusar parcialmente o copiar ideas | Solo si se acepta reemplazar Traefik ForwardAuth por Pomerium como proxy y llevar los grupos a PPL vía `claim/`, lo que requiere un IdP intermedio que emita el claim `groups`. Copiar: header `X-...-Jwt-Assertion`, JWKS en `/.well-known/...`, `aud` = host de la app, logs de decisión con `allow-why`/`deny-why` y portal JSON de rutas. |
| oauth2-proxy | Reusar parcialmente como "login gateway" | Viable para Google + grupos de Workspace y como middleware por app con `allowed_groups`. Faltaría el JWT por app (lo emitiría nuestro servicio) y la autorización desde PostgreSQL. Copiar: sesión en Redis por ticket, revalidación de grupos en cada refresh y `rd` en el logout. |
| Authelia | Copiar ideas | Su modelo de reglas y sesiones multi-dominio con Redis sirve de referencia. No reusar por la falta de Google. |
| traefik-forward-auth (thomseddon/ItalyPaleAle) | Copiar ideas | Es la referencia más cercana en simplicidad a nuestro platform-api: auth-host central, `cookie-domain` y reglas por matcher de Traefik. Evitar su sesión stateless no revocable. |
| Tinyauth | No reusar; copiar el set de headers `Remote-*` | Evitar su fallo abierto ante apps no configuradas. |
| Pangolin | No reusar en K3s | Es otra arquitectura (túneles WireGuard + Traefik propio) con licencia restrictiva para empresas. Copiar la idea del Resource Launcher. |
| Teleport Community | No reusar | Licencia y SSO limitados. Copiar el formato de claims (`roles`, `traits`) y el modo `jwt_claims`. |
| Ory Oathkeeper | Reusar parcialmente | Opción real si se quiere un PEP genérico: Traefik ForwardAuth -> Oathkeeper decisions -> `remote_json` al platform-api -> `id_token` con `aud` por regla. Requiere de todos modos un servicio de login con Google (oauth2-proxy o propio), por lo que agrega piezas sin eliminar el platform-api. |

**Checklist de diseño para el platform-api**, derivado de lo anterior:
1. Default deny por host desconocido (anti-patrón de Tinyauth).
2. Deny gana sobre allow (PPL).
3. JWT nuevo por request con `aud` = host de la app, TTL corto, `kid` y JWKS publicado (Pomerium/Teleport/Oathkeeper).
4. Nunca reenviar el token de Google (Pomerium).
5. Un único redirect URI con cookie en `.apps.empresa.cl` (thomseddon auth-host).
6. Sesión opaca revocable en el servidor (oauth2-proxy Redis ticket).
7. Middleware `errors`/redirect para el 401 en Traefik, o devolver 302 directamente desde el ForwardAuth.
8. Ignorar `X-Forwarded-Proto/Port` del cliente y parchear Traefik (CVE-2026-54764).
9. Log estructurado por decisión con su razón (Pomerium).
10. Endpoint JSON de "mis apps" para el portal (Pomerium routes API).

### Gaps
- **Fuentes multilingües escasas**: las búsquedas en japonés (Zenn/Qiita), chino y francés no devolvieron experiencias de primera mano específicas. Solo aparecieron documentación y guías en inglés, un artículo en español sobre la licencia de Pangolin (vía Linuxiac) y un blog en inglés hallado con búsqueda en alemán. No hay "experience reports" fiables en esos idiomas.
- No se hicieron pruebas de footprint ni de latencia.
