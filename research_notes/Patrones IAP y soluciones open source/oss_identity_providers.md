# IdP / IAM open source (Authentik, Zitadel, Keycloak, Ory, Dex, Casdoor/Logto): cómo resuelven el acceso a apps, los grupos y la auditoría en apps internas

> Contexto: plataforma de iniciativas (portal + admin + N apps en *.apps.empresa.cl, K3s + Traefik). Plan actual: platform-api en Go como Traefik ForwardAuth, que hace login OIDC directo contra Google, guarda grupos y acceso por app en PostgreSQL propio y emite JWT firmados por app. Fecha de corte: octubre 2026. Idioma de cada fuente marcado como [EN], [ES], [DE], [JA], [ZH]. Las fuentes de terceros que son proveedores (Cerbos, SuperTokens, WZ-IT, Cloudzy, Skycloak, MojoAuth) se marcan como "vendor" porque tienen sesgo.

## 1. Federación con Google como IdP upstream e importación/sincronización de grupos de Google Workspace

### Takeaway
Ninguno de los IdP "grandes" (Authentik, Zitadel, Keycloak) trae de forma nativa y open source la importación de grupos de Google Workspace hacia el IdP. Google no emite el claim `groups` en OIDC, así que siempre hace falta la Admin SDK Directory API con una service account con domain-wide delegation. **Dex es el único que lo hace de forma nativa y OSS** en su conector de Google. En Authentik, la integración con Google Workspace es Enterprise y sincroniza hacia afuera (de Authentik a Google), no al revés.

### Cited Findings
- **Limitación de Google (aplica a todos):** Google no incluye la pertenencia a grupos en los tokens OIDC. Para obtenerla hay que consultar la Admin SDK con una service account que impersona a un admin de Workspace. — [Dex Google connector docs [EN]](https://dexidp.io/docs/connectors/google/); también [SUSE Observability "Google via Keycloak" [EN]](https://documentation.suse.com/cloudnative/suse-observability/latest/en/suse-cloud-observability/keycloak-sso/google.html)
- **Domain-wide delegation:** solo un super admin de Workspace puede autorizarla (Admin console > Security > API controls > Domain-wide delegation). Google advierte que la app accede a los datos de todos los usuarios, así que conviene limitar los scopes al mínimo. — [Google Workspace Admin Help [EN]](https://knowledge.workspace.google.com/admin/apps/control-api-access-with-domain-wide-delegation)
- **Dex (Go, OSS):** el conector `google` acepta `serviceAccountFilePath`, `domainToAdminEmail` (mapea un dominio al admin que se impersona; admite el comodín `*`), `groups` (lista blanca de grupos que pueden entrar) y `hostedDomains`. El único scope que se delega es `https://www.googleapis.com/auth/admin.directory.group.readonly`, y la doc advierte que delegar scopes adicionales rompe el acceso. El admin impersonado necesita al menos el rol "Groups Reader". Por defecto fuerza `prompt=consent`. — [Dex Google connector [EN]](https://dexidp.io/docs/connectors/google/)
- **Authentik:** el "Google Workspace provider" empuja usuarios y grupos **desde Authentik hacia Google Workspace**, con Authentik como fuente de verdad. Hace sync directo cuando hay cambios y un full sync cada 4 horas. Es feature **Enterprise**. — [authentik docs: Google Workspace provider [EN]](https://docs.goauthentik.io/add-secure-apps/providers/gws); "Google Workspace integration" figura en la lista de features Enterprise: [authentik Enterprise features [EN]](https://docs.goauthentik.io/enterprise/enterprise-features/)
- **Authentik:** "External OAuth and SAML sources" también aparece como feature Enterprise. — [authentik Enterprise features [EN]](https://docs.goauthentik.io/enterprise/enterprise-features/) (Ver Gaps: no confirmé si el source "Google" OAuth básico sigue siendo OSS.)
- **Keycloak:** los mappers de identity provider solo trabajan sobre claims que ya vienen en el token. En el foro oficial no hay forma estándar de traer grupos de Google, y la respuesta remite a una propuesta de mapper "Advanced claim to group" (KEYCLOAK-19283). — [Keycloak forum thread [EN]](https://forum.keycloak.org/t/how-to-add-roles-group-to-user-based-on-their-google-groups-via-identity-provider/13691)
- **Keycloak:** la guía de SUSE propone un mapper de IdP **custom** que llame a la Admin SDK con domain-wide delegation. Como alternativas lista un rol hardcodeado para todos los usuarios de Google o la asignación manual de roles. — [SUSE Observability "Google via Keycloak" [EN]](https://documentation.suse.com/cloudnative/suse-observability/latest/en/suse-cloud-observability/keycloak-sso/google.html)
- **Zitadel:** trae un IdP Google integrado (OAuth/OIDC). Para grupos de Workspace, el foro oficial remite a las Actions de post-authentication. El patrón documentado para Entra ID es guardar los grupos en metadata del usuario y emitirlos como claim. No encontré receta oficial para Google. — [Zitadel forum "Using Google Workspace groups" [EN]](https://questions.zitadel.com/m/1413514005150175252); [Zitadel Actions external-authentication [EN]](https://zitadel.com/docs/apis/actions/external-authentication#post-authentication)
- **Zitadel + Google:** un hilo del foro reporta que, con Google como IdP externo, los logins siguientes pueden seguir redirigiendo a Google, y que es el comportamiento intencional. — [Zitadel questions [EN]](https://questions.zitadel.com/m/1413514005150175252) (resumen del buscador; verificar)
- **Casdoor:** listados de terceros mencionan Google Workspace entre sus integraciones, sin detalle. — [Railway Casdoor template [EN]](https://railway.com/deploy/casdoor-sso.md) (secundaria)

### Inferences
- El plan actual (Go + Google OIDC directo + grupos propios en PostgreSQL) está al mismo nivel que Authentik, Keycloak o Zitadel en este punto: con cualquiera de ellos habría que escribir código (mapper SPI en Java, Action en JS de Zitadel o policy/property mapping en Python de Authentik) que llame a la Directory API. **Si el requisito es reutilizar los grupos de Google Workspace, lo más barato es llamar a la Directory API desde el platform-api** (scope `admin.directory.group.readonly`, igual que Dex) o poner Dex delante.
- Mantener los grupos en PostgreSQL propio (no en Google) evita la domain-wide delegation, que es un permiso muy amplio y requiere un super admin.

### Gaps
- No confirmé si el source OAuth "Google" de Authentik sigue siendo OSS después de que "External OAuth and SAML sources" pasara a la lista Enterprise. Puede referirse a sources genéricos o a algo distinto. Revisar `docs.goauthentik.io/users-sources/sources/social-logins/google/`.
- No verifiqué el flag `fetchTransitiveGroupMembership` de Dex: la página consultada no lo mencionaba.
- Ory Kratos: no busqué cómo trata los grupos de Google (su OIDC "generic" + Jsonnet mapper tampoco recibe grupos de Google, por la misma limitación).

## 2. Modelado de "aplicaciones" y acceso por app

### Takeaway
Cada IdP tiene un modelo explícito de "aplicación + quién puede entrar". En Authentik es Application + Provider + bindings (policy, grupo o usuario). En Zitadel es Project + Roles + Role Assignments, con el flag "Check Role Assignment on Authentication". En Keycloak son clients + client roles + grupos. En Ory es Keto (Zanzibar) con relation tuples. El modelo de Authentik es el más cercano al del platform-api propuesto: app ↔ grupos con acceso ↔ un ForwardAuth que decide.

### Cited Findings
- **Authentik: Application + Provider.** Una application se crea junto con un provider (el protocolo de SSO). Además se pueden adjuntar backchannel providers (SCIM, LDAP, Google Workspace, Entra ID, SSF) para sincronizar directorios. — [authentik Manage applications [EN]](https://docs.goauthentik.io/add-secure-apps/applications/manage_apps/)
- **Authentik: bindings.** Una application **sin bindings es accesible para todos los usuarios**. Se pueden vincular policies, grupos o usuarios, y el "Policy engine mode" decide si hay que pasar todos (All) o al menos uno (Any). — [authentik Manage applications [EN]](https://docs.goauthentik.io/add-secure-apps/applications/manage_apps/)
- **Authentik: entitlements (desde 2024.12).** Sirven para la autorización dentro de una app, tienen scope de una sola application y admiten bindings de usuarios y grupos, pero no de policies. Se consultan desde property mappings o policies con `user.app_entitlements()` y `user.app_entitlements_attributes()`. — [authentik Manage applications [EN]](https://docs.goauthentik.io/add-secure-apps/applications/manage_apps/)
- **Zitadel: Project y Roles.** Los roles se definen en el proyecto con una key (la que aparece en el token), un display name y un "group" opcional. Una "Role Assignment" (antes "User Grant" o "Authorization") vincula un usuario con uno o más roles. — [Zitadel Roles docs [EN]](https://zitadel.com/docs/guides/manage/console/roles)
- **Zitadel: settings de proyecto.**
  - "Assert Roles on Authentication": devuelve los roles en userinfo y, si se configura en la app, en los tokens.
  - "Check Role Assignment on Authentication": **solo pueden hacer login los usuarios con al menos un rol en el proyecto**.
  - "Check for Project on Authentication": falla si la organización del usuario no tiene grant del proyecto.

  — [Zitadel Projects docs [EN]](https://zitadel.com/docs/guides/manage/console/projects)
- **Zitadel: formato del claim de roles.** El recomendado es `urn:zitadel:iam:org:project:{projectId}:roles` y el legacy, `urn:zitadel:iam:org:project:roles`. La estructura es `{rol: {orgId: orgDomain}}`. Se piden con los scopes `urn:zitadel:iam:org:project:id:{projectId}:aud` y `urn:zitadel:iam:org:projects:roles`. Los roles de administrador (IAM_OWNER) no van en los tokens. — [Zitadel Retrieve user roles [EN]](https://zitadel.com/docs/guides/integrate/retrieve-user-roles)
- **Keycloak:** se lo describe con la federación LDAP/AD, fine-grained authorization y multi-realm más profundos de los tres. — [Cerbos (vendor) [EN]](https://www.cerbos.dev/blog/authentik-vs-keycloak-selfhosted-idp-comparison); [computingforgeeks vía buscador [EN]](https://computingforgeeks.com/?p=173202)
- **Ory Keto + Oathkeeper:** Oathkeeper expone la Access Control Decision API (`/decisions`), que replica la lógica del proxy sin reenviar la request y devuelve 200, 401 o 403. Un usuario del foro valora delegar en Keto toda la autorización desde Oathkeeper. — [Ory Oathkeeper Traefik guide [EN]](https://www.ory.com/docs/oathkeeper/guides/traefik-proxy-integration); [gruchalski.com [EN]](https://gruchalski.com/posts/2021-05-20-figuring-out-oathkeeper/); [archive.ory.sh [EN]](https://archive.ory.sh/t/10120051/also-is-it-possible-to-couple-the-oathkeeper-decision-api-wi)
- **Casdoor:** está construido sobre Casbin y trae un motor de autorización (RBAC, ABAC y ACL) con editor de policies y organizaciones, grupos y roles multi-tenant. — [Railway Casdoor guide (secundaria) [EN]](https://railway.com/deploy/casdoor-sso.md)
- Un resumen de fuentes japonesas y de vendors señala que **la autorización por recurso es débil tanto en Keycloak como en Zitadel** y que conviene sumar un motor de autorización aparte. — [Cerbos "Keycloak vs Zitadel" (vendor de autorización) [EN]](https://www.cerbos.dev/blog/keycloak-vs-zitadel)

### Inferences
- Patrones probados que conviene copiar en el schema PostgreSQL propio:
  1. **Una app sin bindings es pública: hay que cerrarla con un deny-by-default.** El default de Authentik es abierto y es una trampa conocida.
  2. **Bindings app ↔ grupo/usuario/policy con modo any/all** (Authentik).
  3. **Separar "acceso a la app" (gate en el ForwardAuth) de "roles dentro de la app"** (entitlements de Authentik, roles de proyecto de Zitadel) y emitir estos últimos en un claim por app con namespace, como `urn:zitadel:iam:org:project:{id}:roles`.
  4. Equivalente a "Check Role Assignment on Authentication": el ForwardAuth deniega si el usuario no tiene ningún rol en la app.
- Las relation tuples de Keto o Casbin solo valen la pena si aparece autorización por recurso (por ejemplo, "editor de la iniciativa X"). Para un gate app-level alcanza con tablas relacionales.

### Gaps
- No obtuve la doc oficial de clients y client roles de Keycloak ni de "Authorization Services". Los datos de Keycloak en esta sección vienen de vendors.
- No verifiqué el detalle del authorizer `remote_json` o `keto` de Oathkeeper.

## 3. Portal/launcher para usuarios y admin UI con administración delegada

### Takeaway
Authentik trae el launcher más útil: el "Application Dashboard" (antes "My applications/Library"), con launch URLs. La "Applications" del account console de Keycloak es más bien una lista de apps a las que el usuario tiene acceso, no un portal. En Zitadel no encontré un launcher de usuario. Para administración delegada, Keycloak tiene FGAP v2 (26.2) y Zitadel tiene project grants entre organizaciones.

### Cited Findings
- **Authentik:** las applications aparecen en el "Application Dashboard" del usuario salvo que estén ocultas. "Hide from Application Dashboard" las saca del dashboard **sin restringir el acceso**; antes de 2026.5 esto se hacía con la launch URL `blank://blank`. La launch URL `https://authentik.company/application/launch/<slug>/` redirige directo, autenticando primero si hace falta. — [authentik Manage applications [EN]](https://docs.goauthentik.io/add-secure-apps/applications/manage_apps/)
- **Keycloak:** el account console permite gestionar el perfil, el 2FA, los IdP vinculados y la actividad de dispositivos. El menú "Applications" muestra a qué aplicaciones puede acceder el usuario (en el setup por defecto, solo el Account Console). — [Red Hat build of Keycloak 26.0 – Account Console [EN]](https://docs.redhat.com/en/documentation/red_hat_build_of_keycloak/26.0/html/server_administration_guide/account-service)
- **Keycloak FGAP v2 (26.2, abril 2025):** permite definir permisos de administración sobre usuarios, grupos, clients y roles sin roles admin amplios. Cada scope se asigna de forma explícita (nada se hereda implícitamente), se habilita por realm y tiene una sección "Permissions" en el Admin Console. **No soporta permisos de token exchange**, para eso hace falta FGAP v1. — [Keycloak 26.2.0 release notes [EN]](https://www.keycloak.org/docs/26.2.5/release_notes/index.html); [Keycloak blog FGAP 26.2 [EN]](https://www.keycloak.org/2025/05/fgap-kc-26-2.html); [heise [DE]](https://heise.de/-10352601); [FOLIO Jira spike [EN]](https://folio-org.atlassian.net/wiki/spaces/FOLIJET/pages/1373962243/KEYCLOAK-80+-+Spike+Investigate+Keycloak+s+Fine+Grained+Admin+Permissions+FGAP+v2)
- **Zitadel project grants:** al conceder un proyecto a otra organización, esta gestiona las role assignments de sus propios usuarios, restringida al subconjunto de roles concedido. — [Zitadel Projects docs [EN]](https://zitadel.com/docs/guides/manage/console/projects)
- **Zitadel v4 (31-jul-2025):** la UI de login por defecto pasó a Next.js y la API v1 quedó mayormente deprecada en favor de la v2 (Connect RPC). Hay una major cada 3 meses (no retrocompatibles) y una minor cada 2 semanas. — [Zenn, AI Shift "ZITADELはv4でこう変わる" [JA]](https://zenn.dev/aishift/articles/0902be43dcdf0c)

### Inferences
- Para "una app de admin por iniciativa", el patrón de Zitadel (project grant con subconjunto de roles) y FGAP v2 de Keycloak (permisos explícitos por grupo o client) son el modelo a imitar: un **"app owner" que solo administra los bindings de su app**. En el platform-api basta con una tabla `app_admins(app_id, user_id|group_id)` y validarla en los endpoints de admin.
- El launcher del portal propio es comparable al Application Dashboard de Authentik. Conviene copiar dos ideas: launch URL por slug y separar "visible en el portal" de "acceso permitido".

### Gaps
- No verifiqué si Zitadel trae un launcher de apps para el usuario final. No hay evidencia, así que asumo que no.
- No encontré los nombres exactos de los roles de manager de Zitadel (PROJECT_OWNER, PROJECT_GRANT_OWNER, ORG_OWNER) en la doc consultada.

## 4. Proxy/forward-auth integrado y propagación de identidad (headers, JWT)

### Takeaway
Authentik es el único de los cuatro grandes con un forward-auth integrado y documentado para Traefik: el proxy provider con outpost embebido o separado, que inyecta headers `X-authentik-*` y un JWT. Keycloak delega en oauth2-proxy (Gatekeeper/Louketo murió en 2020). Zitadel no tiene modo proxy. Ory usa Oathkeeper (`/decisions`).

### Cited Findings
- **Authentik + Traefik:**
  - El middleware `forwardAuth` apunta a `/outpost.goauthentik.io/auth/traefik` (puerto 9000) con `trustForwardHeader`.
  - Headers propagados: `X-authentik-username`, `-groups`, `-entitlements`, `-email`, `-name`, `-uid`, **`X-authentik-jwt`** y metadatos (`X-authentik-meta-jwks`, `-outpost`, `-provider`, `-app`, `-version`).
  - Hay un modo single-app, que necesita una ruta `PathPrefix(/outpost.goauthentik.io/)` por host, y un modo "domain-level".
  - En K8s se usa un `Middleware` CRD `traefik.io/v1alpha1` (Traefik 3) referenciado cross-namespace.
  - Con el outpost embebido, el host del outpost puede ser el mismo que el de authentik.

  — [authentik proxy provider – Traefik [EN]](https://docs.goauthentik.io/add-secure-apps/providers/proxy/server_traefik/)
- **Authentik:** la versión de la instancia y la de los outposts deben coincidir. — [authentik 2025.10 release notes [EN]](https://docs.goauthentik.io/releases/2025.10/)
- **Keycloak:** en agosto de 2020 el equipo de Keycloak discontinuó Louketo (ex keycloak-gatekeeper) y recomendó **OAuth2 Proxy**. Llegó a "end of line" el 21-nov-2020. Hay un fork comunitario (gogatekeeper). CVE-2020-14359: bypass con headers en minúscula en todas las versiones de Gatekeeper. — [Keycloak blog "Sunsetting Louketo" [EN]](https://www.keycloak.org/2020/08/sunsetting-louketo-project.adoc.html); [Keycloak forum [EN]](https://forum.keycloak.org/t/keycloak-gatekeeper-beyond-version-10/8106); [CVE-2020-14359 [EN]](https://vulert.com/vuln-db/CVE-2020-14359)
- **Zitadel:** una comparativa la lista sin "Proxy mode" (frente a Authentik, que sí lo tiene) y sin LDAP server propio. — [WZ-IT (vendor) [EN]](https://wz-it.com/en/blog/authentik-vs-zitadel-identity-provider-comparison/); [computingforgeeks vía buscador [EN]](https://computingforgeeks.com/?p=173202)
- **Ory Oathkeeper + Traefik:** ForwardAuth hacia `http://oathkeeper:4456/decisions`, con `authResponseHeaders` para los headers que setea Oathkeeper. Un ejemplo de K8s usa `X-Id-Token` y `Authorization`. Hay usuarios que reportan errores 500 y 404 al integrarlo. — [Ory Oathkeeper Traefik guide [EN]](https://www.ory.com/docs/oathkeeper/guides/traefik-proxy-integration); [archive.ory.sh [EN]](https://archive.ory.sh/t/18470188/hello-everyone-we-are-trying-to-deploy-oathkeeper-using-helm); [archive.ory.sh [EN]](https://archive.ory.sh/t/51042/hi-all-i-m-having-a-play-around-trying-to-get-oathkeeper-wor)
- **Authelia** (referencia): no es un IdP completo, sino SSO+MFA delante de un reverse proxy. Según Cerbos, su provider OIDC estaba documentado como beta. — [Cerbos (vendor) [EN]](https://www.cerbos.dev/blog/best-keycloak-alternatives.md)

### Inferences
- El diseño propuesto (ForwardAuth propio que emite un JWT por app) **es el mismo patrón que el proxy provider de Authentik**: headers de identidad + `X-authentik-jwt` + JWKS publicado. Conviene replicar:
  - Publicar el JWKS y propagar un header con la app evaluada, como `X-authentik-meta-app`.
  - En Traefik, eliminar o sobrescribir los headers de identidad entrantes para evitar spoofing. La CVE de Gatekeeper fue justamente un bypass por headers.
  - Mantener alineadas las versiones del emisor y del verificador.
- Si se adopta Keycloak o Zitadel, igual hay que desplegar un forward-auth aparte (oauth2-proxy o el propio platform-api como relying party), así que el componente Go no desaparece.

### Gaps
- No verifiqué el estado actual de oauth2-proxy (releases 2026) ni su soporte de `allowed_groups` con claims de Keycloak o Zitadel.

## 5. Auditoría y event logs: logins, decisiones de acceso, retención y export

### Takeaway
- Authentik guarda eventos en DB con retención configurable (365 días por defecto), los escribe en los logs del contenedor y envía notificaciones por webhook. El export CSV y la auditoría "enhanced" son Enterprise.
- Keycloak tiene user events y admin events con expiración separada, desactivados por defecto en DB, y un SPI de event listeners.
- Zitadel es event-sourced: guarda el audit trail sin límite y limita por `auditLogRetention` lo que expone la Events API.

### Cited Findings
- **Authentik:**
  - "Event retention" se configura en System > Settings, con **365 días por defecto**.
  - Cada evento se escribe en los logs del contenedor (nivel info). Si se reenvían los logs, recomiendan una retención interna corta (p. ej. `days=1`).
  - Las notificaciones salen por transporte local, email o webhook.
  - Antes de guardar, se limpian contraseñas y credenciales de los eventos.

  — [authentik Events [EN]](https://docs.goauthentik.io/sys-mgmt/events/)
- **Authentik Enterprise:** "Enhanced audit logging", "Event maps and charts", "CSV data exports" y "Object Lifecycle Management". — [authentik Enterprise features [EN]](https://docs.goauthentik.io/enterprise/enterprise-features/)
- **Keycloak:**
  - Hay login/user events y admin events. Por defecto **no se guardan** ni se muestran en el Admin Console (los errores sí van al log).
  - Se activan en Realm settings > Events, con un campo Expiration.
  - Existe un Event Listener SPI con listeners para logs y email.

  — [Red Hat build of Keycloak 24 – Auditing [EN]](https://docs.redhat.com/en/documentation/red_hat_build_of_keycloak/24.0/html/server_administration_guide/configuring_auditing_to_track_events)
- **Keycloak:**
  - La expiración se expresa en segundos (604800 = 7 días).
  - Los admin events tienen su propio toggle y su propia expiración.
  - El listener `jboss-logging` por defecto solo escribe errores al log.

  — [Skycloak (vendor) [EN]](https://skycloak.io/blog/keycloak-auditing-event-logging-complete-guide/)
- **Zitadel:**
  - Trae un audit trail integrado "por tiempo ilimitado" en el eventstore.
  - El límite `auditLogRetention` acota por antigüedad lo que devuelven la Events API y los endpoints de historial (`0s` = todo). Se configura global, por instancia nueva o vía system API.
  - Usar la Event API requiere los roles IAM_OWNER o IAM_OWNER_VIEWER.

  — [Zitadel Usage Control [EN]](https://zitadel.com/docs/self-hosting/manage/usage_control); [Zitadel Audit trail [EN]](https://zitadel.com/docs/concepts/features/audit-trail); [Zitadel Event API [EN]](https://zitadel.com/docs/guides/integrate/event-api)
- **Zitadel:** se lo describe con "event-sourced audit logs" de serie. — [Cerbos (vendor) [EN]](https://www.cerbos.dev/blog/keycloak-vs-zitadel)

### Inferences
- Ningún IdP registra de forma nativa "decisión de acceso por request" a nivel ForwardAuth. Lo que registran son logins, autorizaciones de application (`authorize_application` en Authentik) y cambios admin. **Si el platform-api registra cada decisión allow/deny por app, queda por encima de lo que dan los IdP en auditoría de acceso.** Patrón recomendado:
  - Tabla de eventos append-only en PostgreSQL con retención configurable (365 días como default razonable, siguiendo a Authentik).
  - Emisión simultánea a stdout en JSON para Loki o similar.
  - Webhooks de notificación opcionales.
- Separar "user events" de "admin events" con retenciones distintas (modelo Keycloak).

### Gaps
- No encontré el valor por defecto de `auditLogRetention` en Zitadel self-hosted. Un dato de "9 meses" en el foro no está verificado.
- No verifiqué el export nativo de eventos de Keycloak (más allá de la Admin REST API).

## 6. Operación en Kubernetes: Helm, operator, base de datos, footprint, HA y upgrades

### Takeaway
- **Authentik:** desde 2025.10 solo PostgreSQL (eliminó Redis). Es Python y tiene server + worker.
- **Zitadel:** binario Go + login Next.js separado, PostgreSQL 14–18, exige HTTP/2/h2c de punta a punta, majors cada 3 meses.
- **Keycloak:** JVM, unos 1.25 GB por pod según su propia guía de sizing, Infinispan y PostgreSQL, operator oficial.

### Cited Findings
- **Authentik 2025.10:**
  - Elimina Redis: caching, outpost embebido y WebSockets pasan a Postgres (las tasks ya habían migrado en 2025.8).
  - Uso aproximado de **+50% de conexiones a Postgres**.
  - Requiere TLS 1.3 o EMS si Postgres exige TLS.
  - El scope mapping OAuth por defecto pasa a poner `email_verified=false`.
  - Después de migrar se pueden borrar los PVC de Redis.

  — [authentik 2025.10 release notes [EN]](https://docs.goauthentik.io/releases/2025.10/); [blog "We removed Redis" [EN]](https://goauthentik.io/blog/2025-11-13-we-removed-redis/); [mirror versión 2026.8 de las notas [EN]](https://version-2026-8.goauthentik.io/releases/2025.10/); guía en español que lo confirma: [jacar.es [ES]](https://jacar.es/como-instalar-authentik-sso/)
- **Authentik footprint:** imagen de unos 500 MB y unos 300 MB de RAM en idle. — [WZ-IT (vendor) [DE/EN]](https://wz-it.com/en/blog/authentik-vs-zitadel-identity-provider-comparison/). Mínimo para Docker Compose: 2 vCPU y 2 GB de RAM. — [computingforgeeks vía buscador [EN]](https://computingforgeeks.com/?p=173202). Arquitectura: web server + worker en background. — [computingforgeeks [EN]](https://computingforgeeks.com/?p=173202)
- **Zitadel en K8s:**
  - Chart `zitadel/zitadel` desde `https://charts.zitadel.com`. El subchart de PostgreSQL es solo para quickstart.
  - **El login UI es un deployment Next.js separado** (`zitadel-login`, puerto 8080, con ingress propio).
  - Soporta PostgreSQL 14–18 (la 18 requiere v4.11.0 o superior).
  - **La consola requiere HTTP/2 de punta a punta**: el ingress debe hacer h2c hacia los pods.
  - El ejemplo usa `replicaCount: 2` y una PDB.
  - Los jobs `zitadel-init` y `zitadel-setup` deben completarse antes de que el deployment esté Ready.

  — [Zitadel Kubernetes deploy [EN]](https://zitadel.com/docs/self-hosting/deploy/kubernetes)
- **Zitadel footprint:** unos 100 MB de RAM en idle. — [WZ-IT (vendor) [EN]](https://wz-it.com/en/blog/authentik-vs-zitadel-identity-provider-comparison/)
- **Zitadel upgrades:**
  - La v3 terminó el soporte de CockroachDB y cambió la licencia. La v4 fue un upgrade "relativamente fácil".
  - Service Ping viene **habilitado por defecto**: envía system ID, versión, dominio y fecha de creación. Se desactiva con `ServicePing.Enabled=false`.
  - `LoginV2.Required=false` mantiene el login v1.

  — [Zenn AI Shift v4 [JA]](https://zenn.dev/aishift/articles/0902be43dcdf0c); v3: [Zenn AI Shift v3 [JA]](https://zenn.dev/aishift/articles/abb11f61817880)
- **Keycloak sizing oficial:**
  - **1250 MB por pod** (incluye caches de realm y 10.000 sesiones cacheadas). Heap = 70% del límite y non-heap ≈ 300 MB, lo que da un límite de unos 1360 MB.
  - CPU: 1 vCPU por 15 logins con password/s, 120 client-credentials/s o 120 refresh/s, más un 150% de headroom.
  - Las sesiones se guardan en DB por defecto.
  - DB: 1400 IOPS de escritura y 0.35–0.7 vCPU por cada 100 req/s.

  — [Keycloak HA – memory & CPU sizing [EN]](https://www.keycloak.org/high-availability/multi-cluster/concepts-memory-and-cpu-sizing)
- **Keycloak:** se lo describe como un único proceso Java, con upgrades y tuning laboriosos. — [computingforgeeks [EN]](https://computingforgeeks.com/?p=173202); [Cerbos (vendor) [EN]](https://www.cerbos.dev/blog/keycloak-vs-zitadel). Usuarios chinos lo describen como pesado, lento al arrancar, con mucha memoria y ciclos de soporte cortos. — [V2EX "公司内网认证系统调研" (2025-05) [ZH]](https://www.v2ex.com/t/1133490); [CSDN [ZH]](https://blog.csdn.net/zhengliebin/article/details/163608868)
- **Keycloak** tiene Kubernetes Operator y versión con soporte comercial de Red Hat. — [ayedo [DE/EN]](https://ayedo.de/en/posts/authentik-vs-keycloak/) (secundaria, vía buscador)
- **Casdoor:** binario Go único que sirve la consola React. **Necesita Redis para sesiones compartidas si corre con más de una réplica.** — [Railway Casdoor guide (secundaria) [EN]](https://railway.com/deploy/casdoor-sso.md)
- **Ory:** Ory recomienda los builds OSS para desarrollo y testing y para despliegues donde "un downtime ocasional en upgrades sea aceptable y no se requiera parcheo de CVE". Las migraciones zero-downtime y los parches de CVE con SLA son parte de la OEL. — [Ory OEL docs [EN]](https://www.ory.com/docs/self-hosted/oel)

### Inferences
- En un K3s pequeño, el orden de footprint (ver también la tabla de la sección 8) es aproximadamente: Dex/Zitadel (Go) < Authentik (Python, server+worker, solo Postgres desde 2025.10) < Keycloak (JVM, unos 1.3 GB por pod). El platform-api propio en Go queda en el orden de decenas de MB.
- Zitadel exige h2c en Traefik, lo cual es posible pero hay que configurarlo explícitamente (`scheme: h2c` en el Service/IngressRoute).
- Las majors trimestrales no retrocompatibles de Zitadel implican una carga de upgrade recurrente.

### Gaps
- No verifiqué los requisitos de recursos del Helm chart de Authentik ni el soporte de HA multi-réplica del worker.
- No tengo datos oficiales de RAM de Zitadel (solo el dato de un vendor) ni de Dex.

## 7. Stack, licencia, features enterprise y salud del proyecto (2026)

### Takeaway
Las licencias se separan así:
- **Apache-2.0:** Keycloak (CNCF incubating), Dex, Ory OSS y Casdoor.
- **MIT + carpeta enterprise:** Authentik.
- **AGPL-3.0 desde Zitadel v3 (marzo 2025):** Zitadel.
- **MPL-2.0:** Logto.

El modelo open-core recorta features relevantes para este caso: en Authentik, la integración con Google Workspace, el export CSV de auditoría y los sources externos son Enterprise. En Ory, los parches de CVE con SLA y las migraciones zero-downtime están en la OEL.

### Cited Findings
- **Authentik:** el LICENSE raíz deja todo bajo MIT salvo `authentik/enterprise/`, que tiene licencia propia. — [authentik LICENSE [EN]](https://github.com/goauthentik/authentik/blob/main/LICENSE). Stack Python + TypeScript. — [WZ-IT (vendor) [EN]](https://wz-it.com/en/blog/authentik-vs-zitadel-identity-provider-comparison/)
- **Authentik Enterprise:**
  - Lista: Agent accounts, Scheduled user offboarding, Google Workspace integration, Microsoft Entra ID integration, External OAuth and SAML sources, SSF provider, OAuth auth for SCIM, WS-Federation, PAM, Password history, client certificate auth, RADIUS EAP-TLS, Account Lockdown, Enhanced audit logging, Event maps/charts, CSV exports, Object Lifecycle Management, Fleet/Chrome connectors, Local device login y Advanced device compliance.
  - Usa las mismas imágenes y la licencia se valida localmente (funciona air-gapped).

  — [authentik Enterprise features [EN]](https://docs.goauthentik.io/enterprise/enterprise-features/); [authentik Enterprise [EN]](https://docs.goauthentik.io/enterprise/)
- **Zitadel:**
  - Pasa de Apache-2.0 a **AGPL-3.0** con la v3 (31-mar-2025), sin efecto retroactivo.
  - Las SDKs y herramientas siguen en Apache-2.0.
  - Hay licencia comercial para quien no pueda cumplir la AGPL.

  — [Zitadel blog "Moving to AGPL 3.0" [EN]](https://zitadel.com/blog/apache-to-agpl); [Zitadel license FAQ [EN]](https://zitadel.com/license-faq); [anuncio v3 [EN]](https://zitadel.com/v2). Stack Go.
- **Fuentes desactualizadas:** algunos comparadores todavía listan a Zitadel como Apache-2.0. Hay conflicto entre sitios y la licencia vigente es AGPL-3.0. — [openalternative [EN]](https://openalternative.co/compare/keycloak/vs/zitadel) vs. otros comparadores (señalado en el resumen de búsqueda japonesa). La comunidad japonesa considera que el impacto de la AGPL es limitado. — [Qiita [JA]](https://qiita.com/naka_kyon/items/7831bcf97343886d443c)
- **Zitadel salud:**
  - v3 en marzo 2025 y v4 el 31-jul-2025.
  - Majors cada 3 meses y minors cada 2 semanas.
  - En v4.11 o superior soporta PG18.

  — [Zenn [JA]](https://zenn.dev/aishift/articles/0902be43dcdf0c); [Zitadel K8s docs [EN]](https://zitadel.com/docs/self-hosting/deploy/kubernetes)
- **Keycloak:**
  - Apache-2.0, proyecto **CNCF incubating desde el 10-abr-2023**, Java/Quarkus con extensiones SPI.
  - Va por la serie 26.x: la 26.2 es de abril 2025 y Red Hat documenta la 26.4.

  — [Qiita "Keycloakとは" [JA]](https://qiita.com/wadahiro/items/bc0d7a5ee2c3c12093d0); [openalternative [EN]](https://openalternative.co/compare/keycloak/vs/zitadel); [Keycloak 26.2.0 released [EN]](https://www.keycloak.org/2025/04/keycloak-2620-released); [Red Hat build of Keycloak 26.4 [EN]](https://docs.redhat.com/de/documentation/red_hat_build_of_keycloak/26.4/html/server_administration_guide/account-service)
- **Keycloak salud:** hay KeyConf Prague 2026 y KeycloakCon Europe 2027 anunciados en keycloak.org. — [keycloak.org (banner) [EN]](https://www.keycloak.org/2020/08/sunsetting-louketo-project.adoc.html)
- **Ory:**
  - Los builds OSS siguen en **Apache-2.0**.
  - La **Ory Enterprise License (OEL)** es comercial e incluye features que no están en OSS: ROPC en Hydra, multi-tenancy/organizations en Kratos, parches de CVE con plazos y soporte con SLA.
  - Versionado nuevo (25.4.0).
  - Hay fricción comunitaria por las SDK self-hosted que se actualizan tarde.

  — [Ory OEL docs [EN]](https://www.ory.com/docs/self-hosted/oel); [Ory Enterprise License [EN]](https://www.ory.com/ory-enterprise-license); [Ory changelog v25.4.0 [EN]](https://changelog.ory.com/announcements/ann_Dne2mookJyqgU); [archive.ory.sh [EN]](https://archive.ory.sh/t/25972487/how-are-we-to-take-ory-entreprise-licensing-seriously-if-we-); falta de UI self-hosted: [archive.ory.sh [EN]](https://archive.ory.sh/t/29717195/is-there-really-no-front-end-for-self-hosted-ory-i-found-htt)
- **Casdoor:**
  - Apache-2.0, Go + React.
  - Protocolos: OAuth2/OIDC, SAML, CAS, LDAP, SCIM, WebAuthn y RADIUS.
  - En China se lo critica como "más bien un proyecto personal", con APIs y formato de errores inconsistentes. El dato de "3x QPS de Keycloak" no tiene fuente fiable.

  — [pkg.go.dev casdoor [EN]](https://pkg.go.dev/github.com/casdoor/casdoor@v1.691.0); [CSDN [ZH]](https://blog.csdn.net/zhengliebin/article/details/163608868); [V2EX [ZH]](https://www.v2ex.com/t/870546)
- **Logto:** TypeScript, **MPL-2.0**, con edición OSS self-hosted y Cloud. Un revisor advierte que el copyleft de la MPL puede preocupar a procurement. — [Logto blog OSS vs Cloud [EN]](https://blog.logto.io/logto-oss-vs-logto-cloud); [guptadeepak [EN]](https://guptadeepak.com/ciam-compass/vendors/logto/)
- **Popularidad:** según un comparador, Keycloak tiene unas 35k estrellas en GitHub y Authentik unas 22k (cifras que cambian). — [resumen de comparadores [EN]](https://openalternative.co/compare/authentik/vs/keycloak)
- **Experiencias de comunidad (V2EX, 2025-05 a 2026-03):**
  - Keycloak se usa como hub con WeCom upstream y un provider custom, pero otros lo llaman "inflado y difícil de personalizar".
  - Se recomiendan Authentik (buena UI en chino), Casdoor, Logto y el stack Ory.
  - Zitadel no aparece en el hilo.

  — [V2EX t/1133490 [ZH]](https://www.v2ex.com/t/1133490)
- **Consenso de las comparativas (DE/JA/ZH/EN):**
  - Authentik es el default para homelab, herramientas internas y equipos chicos (proxy, flows, LDAP/RADIUS).
  - Zitadel se orienta a SaaS/B2B multi-tenant con foco en APIs.
  - Keycloak se orienta a enterprise con LDAP/AD, multi-realm y soporte de Red Hat.

  — [Cloudzy [EN/JA]](https://cloudzy.com/ja/blog/authentik-vs-zitadel-vs-keycloak/); [WZ-IT [EN]](https://wz-it.com/en/blog/authentik-vs-zitadel-identity-provider-comparison/); [Cerbos (vendor) [EN]](https://www.cerbos.dev/blog/authentik-vs-keycloak-selfhosted-idp-comparison)

### Inferences
- Para una empresa que solo usa el IdP internamente, la AGPL de Zitadel normalmente no obliga a nada si no se modifica. Lo que sí hay que vigilar son los parches propios.
- El open-core de Authentik afecta justamente lo que este proyecto necesita (Google Workspace y export de auditoría). Si se adoptara Authentik, probablemente terminaría siendo Enterprise o sumando código propio.

### Gaps
- No obtuve el precio por usuario de Authentik Enterprise: la página remite a ventas.
- No verifiqué actividad de releases de Dex en 2026 ni el estado de los proyectos Ory OSS en 2026 más allá de la v25.4.

## 8. Trade-off: IdP completo vs. autorización en PostgreSQL propio detrás de ForwardAuth propio

### Takeaway
Para el alcance actual (solo usuarios de Google Workspace, apps web propias detrás de Traefik, gate a nivel de app y portal propio), **el diseño propio en Go es razonable y equivale al patrón "proxy provider + application bindings" de Authentik**, sin su open-core ni su peso operativo. Un IdP se vuelve necesario cuando aparecen:
- apps que necesitan OIDC/SAML nativo (SaaS de terceros, Grafana, ArgoCD, etc.);
- clientes machine-to-machine (client credentials);
- usuarios no-Google (externos o proveedores);
- MFA y passkeys propios, independientes de Google;
- LDAP o RADIUS para herramientas legacy;
- requisitos de auditoría o compliance formales.

### Cited Findings
- Hay voces que advierten que un IdP self-hosted es infraestructura crítica: hay que planificar backups, acceso de emergencia y upgrades. — [resumen de comparativas DE [DE]](https://wz-it.com/en/blog/authentik-vs-zitadel-identity-provider-comparison/); [Cloudzy [EN]](https://cloudzy.com/blog/authentik-vs-zitadel-vs-keycloak/)
- Authentik aporta además un LDAP server, RADIUS, Kerberos y proxy mode. Zitadel no tiene proxy ni LDAP server. — [WZ-IT (vendor) [EN]](https://wz-it.com/en/blog/authentik-vs-zitadel-identity-provider-comparison/)
- El costo de JVM de Keycloak está cuantificado oficialmente: unos 1.25 GB por pod y 1 vCPU por cada 15 logins/s. — [Keycloak sizing [EN]](https://www.keycloak.org/high-availability/multi-cluster/concepts-memory-and-cpu-sizing)
- La autorización por recurso es débil en los IdP y se sugiere un motor aparte. — [Cerbos (vendor) [EN]](https://www.cerbos.dev/blog/keycloak-vs-zitadel)
- Dex resuelve la federación con Google + grupos de Workspace de forma OSS y liviana, y emite OIDC hacia las apps. — [Dex Google connector [EN]](https://dexidp.io/docs/connectors/google/)

### Inferences
**Matriz resumen (para el writer):**

| | Grupos Google WS | Acceso por app | Launcher | Forward-auth Traefik | Auditoría | Stack / DB | Licencia |
|---|---|---|---|---|---|---|---|
| **Platform-api propio** | Directory API (código propio) | Tablas propias | Portal propio | Es el propio | Lo que se diseñe | Go / PG | Propia |
| **Authentik** | Solo outbound y Enterprise | App + bindings + entitlements | Sí (App Dashboard) | Sí (outpost, `X-authentik-*` + JWT) | DB, 365 d, webhook; CSV Enterprise | Python / PG (sin Redis desde 2025.10) | MIT + enterprise |
| **Zitadel** | Action custom | Project / roles / role assignments, claim URN | No evidenciado | No | Eventstore + Events API | Go + Next.js / PG 14–18, h2c | AGPL-3.0 (v3+) |
| **Keycloak** | Mapper SPI custom | Clients / roles / grupos, FGAP v2 | Lista en account console | Vía oauth2-proxy | User/admin events con expiración, SPI | Java / PG, ~1.3 GB/pod | Apache-2.0, CNCF |
| **Ory** | No nativo | Keto (Zanzibar) | No (sin UI OSS) | Oathkeeper `/decisions` | No evaluado | Go / PG, 3–4 servicios | Apache-2.0 + OEL |
| **Dex** | **Nativo OSS** | No (solo federación) | No | No (va con oauth2-proxy) | Mínima | Go | Apache-2.0 |
| **Casdoor** | Listado, no verificado | Casbin RBAC/ABAC | Sí (consola) | No verificado | No verificado | Go / DB + Redis para HA | Apache-2.0 |

- **Recomendación derivada:**
  1. Mantener el platform-api como ForwardAuth y PDP (punto de decisión de acceso).
  2. Diseñar el schema de modo que **el emisor de identidad sea intercambiable**: tratar Google como "un upstream" y no acoplar `sub` al ID de Google.
  3. Exponer un JWKS y claims por app con namespace (al estilo Zitadel).
  4. Dejar la puerta abierta a poner Dex o Zitadel delante cuando aparezca un disparador (apps con OIDC nativo, M2M, usuarios externos o MFA propio). Así el platform-api pasa a ser relying party de ese IdP y conserva el modelo de apps, bindings y auditoría.
- Hacer M2M (client credentials) o emitir OIDC estándar a terceros desde código propio implica reimplementar un servidor OAuth2/OIDC. Ese es el punto de corte más claro para adoptar un IdP: Zitadel o Keycloak para un IdP completo, Hydra si se quiere solo el servidor OAuth2.

### Gaps
- No hay benchmarks independientes de footprint (Authentik y Zitadel solo tienen datos de vendors).
- No encontré artículos en portugués ni en francés específicos sobre el tema. Las búsquedas en español devolvieron casi solo fuentes en inglés (salvo jacar.es).
- No evalué Pomerium ni Authelia en profundidad, aunque son alternativas IAP directas al ForwardAuth propio.
