# Traefik ForwardAuth con servicio de autorización propio (Go) en K3s: implementación, pitfalls, seguridad de headers y enforcement a nivel de cluster

> Notas de investigación al 2026-10-14. Idioma de las notas: español (términos técnicos en inglés). Idioma de cada fuente indicado como [EN], [DE], [JA], [ZH], [FR], [ES]. Casi todas las fuentes útiles resultaron ser en inglés; las búsquedas en japonés, chino y alemán devolvieron mayoritariamente fuentes en inglés (ver Gaps).

## 1. Contrato de ForwardAuth en Traefik v3: headers, opciones, redirects y respuestas no-2xx

### Takeaway
Traefik hace una subrequest **GET sin body** (por defecto) al `address`, con todos los headers del cliente (salvo hop-by-hop, o filtrados por `authRequestHeaders`) más `X-Forwarded-Method/Proto/Host/Uri/For`; si el auth server responde 2xx se reenvía la request original copiando (delete-then-set) los `authResponseHeaders`; cualquier otro código (incluido 302) se devuelve tal cual al cliente. No hay caché: una llamada al auth server por cada request.

### Cited Findings
- Semántica básica: si el auth server responde 2XX, Traefik reenvía la request original; con cualquier otro código, Traefik devuelve al cliente la respuesta del auth server. [EN] — [Traefik docs ForwardAuth (v3)](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/)
- Headers enviados al auth server: método → `X-Forwarded-Method`; protocolo → `X-Forwarded-Proto`; host → `X-Forwarded-Host`; request URI → `X-Forwarded-Uri`; IP origen → `X-Forwarded-For`. [EN] — [Traefik docs ForwardAuth](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/)
- Opciones y defaults documentados (v3 actual): `address` (requerido); `trustForwardHeader` (unset, **deprecado**, se eliminará en el próximo major); `authResponseHeaders` (`[]`, headers del auth response copiados a la request reenviada, reemplazando los existentes en conflicto); `authResponseHeadersRegex` (`""`, los headers que matchean se eliminan primero; match parcial → usar `^...$`); `authRequestHeaders` (`[]`; si vacío se envían **todos** los headers de la request); `addAuthCookiesToResponse` (`[]`); `forwardBody` (`false`, rompe streaming); `maxBodySize` (`-1` ilimitado, > límite → 401); `maxResponseBodySize` (unset = ilimitado, > límite → 401); `headerField`; `preserveLocationHeader` (`false`); `preserveRequestMethod` (`false`); `authSigninURL` (`""`, URL a la que redirigir cuando el auth server devuelve 401); `tls.ca/cert/key/caSecret/certSecret/insecureSkipVerify`. La página no indica "available since" por opción. [EN] — [Traefik docs ForwardAuth](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/)
- La doc advierte fuertemente fijar `maxBodySize` y `maxResponseBodySize` por riesgo de DoS / agotamiento de memoria. [EN] — [Traefik docs ForwardAuth](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/)
- `trustForwardHeader`: con `true` Traefik confía en todos los `X-Forwarded-*`; el enfoque recomendado es definir `forwardedHeaders.trustedIPs` en el EntryPoint y poner esta opción en `true`, de modo que el EntryPoint elimina los headers forwarded no confiables y los añadidos por otros middlewares (p.ej. `X-Forwarded-Prefix` de StripPrefix) son confiables. Si no se setea, Traefik emite un warning al arrancar y usa comportamiento legacy: elimina `X-Forwarded-For` y `X-Forwarded-Proto` pero deja pasar otros como `X-Forwarded-Prefix`. [EN] — [Traefik docs ForwardAuth](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/)
- Código fuente (`pkg/middlewares/auth/forward.go`, rama master): método por defecto `http.MethodGet`; con `preserveRequestMethod` usa el método original. Sin body salvo `forwardBody` (buffered hasta `maxBodySize`). [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go)
- El cliente HTTP del auth **no sigue redirects** (`CheckRedirect` → `http.ErrUseLastResponse`); un 3xx cae en la rama no-2xx y se devuelve al cliente. [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go)
- Headers a la subrequest: se copian todos los del cliente, se eliminan hop-by-hop (`RemoveConnectionHeaders` + lista `hopHeaders`); si `authRequestHeaders` no está vacío solo se conservan los listados. Con `trustForwardHeader` unset: sobreescribe `X-Forwarded-For` con la IP de `RemoteAddr` y setea incondicionalmente `X-Forwarded-Proto/Method/Host/URI`; no setea `X-Forwarded-Port` ni `X-Forwarded-Prefix`. Con `trustForwardHeader=false` explícito: primero `DeleteXForwardedHeaders`; con `true`: añade a `X-Forwarded-For` previo y setea Proto/Port/Host/URI/Method **solo si ausentes**. `X-Forwarded-Prefix` nunca lo setea este middleware. [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go)
- Respuesta no-2xx: se copian headers de respuesta (incluido `Set-Cookie`, menos hop-by-hop), status code y body (sujeto a `maxResponseBodySize`). `Location`: sin `preserveLocationHeader` se usa `forwardResponse.Location()` (resuelve relativos contra la URL del auth server, por eso la doc dice que se "prefija con el dominio del auth server"); con la opción se pasa el `Location` crudo. [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go); [Traefik docs](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/)
- Respuesta 2xx: para cada nombre en `authResponseHeaders` hace `req.Header.Del(key)` y luego copia los valores del auth response si existen (delete-then-set → un header spoofeado por el cliente con ese nombre se elimina aunque el auth server no lo devuelva). Con `authResponseHeadersRegex` borra todos los headers entrantes que matchean y copia los del auth response que matchean. [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go)
- `addAuthCookiesToResponse`: solo las cookies del auth response cuyos nombres estén en la lista se añaden a la respuesta al cliente; otras cookies del auth (en 2xx) no se envían. [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go)
- `authSigninURL`: si el auth server devuelve exactamente 401, Traefik responde `302 Found` a esa URL (sin copiar headers/body del 401); otros 4xx/5xx no se ven afectados. [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go)
- `headerField`: solo registra el primer valor como `ClientUsername` en el access log; no añade nada a la request al backend. [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go)
- Timeout del cliente auth: `30 * time.Second` fijo (no configurable en ese archivo); usa el contexto de la request entrante. **Sin caché**: cada request dispara una llamada nueva al `address`. [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go)
- Patrón de referencia Authelia: endpoint `/api/authz/forward-auth`, `trustForwardHeader: true`, `maxResponseBodySize: 8192`, `authResponseHeaders: Remote-User, Remote-Groups, Remote-Email, Remote-Name`. [EN] — [Authelia: Traefik integration](https://www.authelia.com/integration/proxies/traefik/)
- Authelia considera `X-Forwarded-Method`, `-Proto`, `-Host`, `-URI` requeridos; `X-Forwarded-For` opcional. Estrategias: header `Authorization` (fallo → 401 + `WWW-Authenticate`) y luego cookie de sesión (fallo → redirect a login). [EN] — [Authelia: Proxy Authorization reference](https://www.authelia.com/reference/guides/proxy-authorization/)
- oauth2-proxy + Traefik: con `/oauth2/auth` el proxy devuelve 401 y Traefik **no** lo convierte en redirect; se requiere un middleware `errors` que capture 401 y redirija a `/oauth2/sign_in?rd=...`/`/oauth2/start?rd=...`, o apuntar ForwardAuth a `/` para obtener 302. Fuentes comunitarias, con consejos contradictorios. [EN] — [Traefik community: migration nginx→traefik](https://community.traefik.io/t/migration-from-nginx-ingress-controller-to-traefik/29753); [Traefik community: oauth2-proxy 401](https://community.traefik.io/t/possible-missing-configuration-oauth2-proxy-traefik/28922); [StackHarbor KB](https://stackharbor.com/en/knowledge-base/traefik-oauth2-proxy-integration/)
- Bug conocido de oauth2-proxy: el parámetro `rd` pierde el path tras login (issue #1297). [EN] — [Traefik community: forgets URL](https://community.traefik.io/t/traefik-oauth2-proxy-forwardauth-middleware-forgets-url/14443)

### Inferences
- Para platform-api: derivar la app de `X-Forwarded-Host` es seguro **solo** si ese header lo fija Traefik (rama `trustForwardHeader` unset/false, o `true` + entrypoint sin trustedIPs que lo borre). Con `trustForwardHeader: true` y `trustedIPs` mal configurados, el cliente podría inyectar `X-Forwarded-Host` (Traefik solo lo setea si está ausente).
- Para el JWT firmado: listarlo en `authResponseHeaders` (p.ej. `X-Platform-Assertion`) garantiza que Traefik borre cualquier valor enviado por el cliente con ese nombre; el backend debe confiar **solo** en ese header y validar firma, `aud` = app/host, `exp` corto.
- Para redirect a login: platform-api puede devolver directamente `302 Location: https://portal.apps.empresa.cl/login?rd=...` con URL absoluta (evita la reescritura relativa); alternativamente devolver 401 y usar `authSigninURL` (pero esa URL es estática, sin `rd` dinámico).
- Usar `authRequestHeaders` (allowlist: `Cookie`, `Authorization`, `Accept`, `X-Requested-With`, `Origin`...) reduce superficie y tamaño de la subrequest.

### Gaps
- La doc no indica en qué versión aparecieron `authSigninURL`, `maxResponseBodySize`, `preserveRequestMethod`; habría que revisar CHANGELOG de traefik.
- No se verificó el comportamiento exacto de `DeleteXForwardedHeaders` (lista de headers que borra) en el código fuente.

## 2. Traefik v3 vs v2, versión en K3s, HelmChartConfig y `forwardedHeaders.trustedIPs`

### Takeaway
K3s actual trae Traefik v3 (desde la línea 1.32; v1.32.10 trae 3.5.1) y se configura con un `HelmChartConfig` llamado `traefik` en `kube-system`; los cambios v2→v3 relevantes son API group `traefik.io`, sintaxis de reglas v3 y eliminación de `tls.caOptional` en ForwardAuth. `trustedIPs` vacío (default) significa que el entrypoint descarta `X-Forwarded-*` del cliente, que es lo correcto con IP pública directa vía ServiceLB.

### Cited Findings
- La doc de K3s indica que K3s "comes with Traefik v3", compatible con Gateway API v1.4; Gateway API deshabilitado por defecto (`providers.kubernetesGateway.enabled`). [EN] — [K3s Networking Services](https://docs.k3s.io/networking/networking-services)
- No editar `/var/lib/rancher/k3s/server/manifests/traefik.yaml` (K3s lo reemplaza al arrancar); crear un `HelmChartConfig` separado en ese directorio. Deshabilitar con `--disable=traefik`. [EN] — [K3s Networking Services](https://docs.k3s.io/networking/networking-services)
- HelmChartConfig debe tener mismo nombre y namespace que el HelmChart (`traefik` en `kube-system`); `valuesContent` se pasa como values file adicional; `spec.set` del HelmChart tiene precedencia sobre `valuesContent`. [EN] — [SUSE/K3s Helm docs](https://documentation.suse.com/cloudnative/k3s/latest/en/helm.html)
- v1.32.10+k3s1 bump de Traefik a 3.5.1. [EN] — [newreleases: k3s v1.32.10+k3s1](https://newreleases.io/project/github/k3s-io/k3s/release/v1.32.10%2Bk3s1)
- Reporte de usuario: upgrade K3s 1.30.11 → 1.32.3 movió Traefik de v2.11.10 a chart 34.2.1 / v3.3.2 (no oficial). [EN] — [Rancher Slack archive](https://slack-archive.rancher.com/t/28506974/hi-all-bit-confused-about-how-to-get-traefik-v3-to-be-deploy)
- Notas v1.33.X: upgrade al chart Traefik v40.x con breaking change en el provider ingress-nginx (renombra value). [EN] — [K3s v1.33.X release notes](https://documentation.suse.com/cloudnative/k3s/latest/en/release-notes-old/v1.33.X.html)
- Blog de enero 2026 afirma que K3s incluye Traefik v2 por defecto: **desactualizado/contradicho** por la doc oficial. [EN] — [oneuptime blog](https://oneuptime.com/blog/post/2026-01-27-k3s-traefik/view); contradicho por [K3s docs](https://docs.k3s.io/networking/networking-services)
- Cambios v2→v3 relevantes: ForwardAuth pierde `tls.caOptional`; API group `traefik.containo.us` eliminado → `traefik.io`; Ingress `networking.k8s.io/v1beta1` no soportado; `PathPrefix` ya no usa regex, reglas con sintaxis Go regexp, `Headers`→`Header`, `HostHeader` eliminado; default rule syntax v3 (configurable con `core.defaultRuleSyntax` o `ruleSyntax` por router / anotación `traefik.ingress.kubernetes.io/router.rulesyntax`); `IPWhiteList`→`IPAllowList`; headers middleware pierde `sslRedirect` etc.; `Content-Type` ya no se autodetecta. [EN] — [Traefik migración v2→v3](https://doc.traefik.io/traefik/migrate/v2-to-v3-details/)
- EntryPoint `forwardedHeaders.trustedIPs` (default ninguno): IPs/CIDR desde las que se confía en `X-Forwarded-*`; `forwardedHeaders.insecure` (default false) solo para tests; `notAppendXForwardedFor` (default false; el chart indica v3.7+); `forwardedHeaders.connection`: lista de headers nombrados en `Connection` que se permiten pasar por la cadena (Traefik por RFC 7230 elimina los headers nombrados en `Connection`). [EN] — [Traefik EntryPoints](https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/); [traefik-helm-chart values.yaml](https://raw.githubusercontent.com/traefik/traefik-helm-chart/master/traefik/values.yaml)
- `proxyProtocol.trustedIPs`: habilita PROXY protocol v1/v2; reemplaza la IP cliente solo para IPs listadas (las del LB); debe configurarse en ambos lados o abre forgery. [EN] — [Traefik EntryPoints](https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/)
- `http.sanitizePath` (default `true`): `/foo/../bar` → `/bar`; desactivarlo "is not safe". [EN] — [Traefik EntryPoints](https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/)
- Authelia recomienda listar solo IPs específicas de proxies en `trustedIPs` (no subredes enteras salvo que solo contengan proxies confiables) y mantener `insecure: false`; por defecto Traefik no confía en otros proxies y elimina headers forwarded potencialmente fabricados. [EN] — [Authelia: Traefik](https://www.authelia.com/integration/proxies/traefik/)
- ServiceLB (Klipper) por defecto corre el LoadBalancer de Traefik en todos los nodos ocupando 80/443; restringible con label `svccontroller.k3s.cattle.io/enablelb=true`; `--disable=servicelb` para usar MetalLB. Known issue: con `externalTrafficPolicy=local` y tráfico vía NAT, no setear `node-external-ip`. [EN] — [K3s Networking Services](https://docs.k3s.io/networking/networking-services)
- Valores del chart: `ports.websecure.http.middlewares: []` (middlewares por defecto del entrypoint), `forwardedHeaders.trustedIPs: []`, `insecure: false`, `proxyProtocol.trustedIPs: []`. [EN] — [traefik-helm-chart values.yaml](https://raw.githubusercontent.com/traefik/traefik-helm-chart/master/traefik/values.yaml)

### Inferences
- Fase "IP pública directa" (ServiceLB): dejar `trustedIPs` vacío → Traefik descarta los `X-Forwarded-*` del cliente. Fase "detrás de LB/VPN": añadir **solo** la IP del LB/reverse proxy a `trustedIPs` (o PROXY protocol si el LB es L4).
- Ejemplo HelmChartConfig (no verificado en cluster; validar ruta de keys contra la versión del chart que trae su K3s):
  ```yaml
  apiVersion: helm.cattle.io/v1
  kind: HelmChartConfig
  metadata: {name: traefik, namespace: kube-system}
  spec:
    valuesContent: |-
      providers:
        kubernetesCRD:
          allowCrossNamespace: false
      ports:
        websecure:
          forwardedHeaders: {trustedIPs: [], insecure: false}
          http:
            middlewares:
              - platform-auth-forwardauth@kubernetescrd
  ```
- Fijar explícitamente `trustForwardHeader` (silencia warning y evita el camino legacy) y planear su eliminación en el próximo major (v4).

### Gaps
- No encontré la tabla oficial "versión K3s → versión Traefik" actual (octubre 2026); verificar con `helm list -n kube-system` o en `/var/lib/rancher/k3s/server/static/charts/` y las release notes de la versión K3s concreta.
- No verificado si el chart cambió la key de middlewares de entrypoint entre `ports.websecure.middlewares` (charts antiguos) y `ports.websecure.http.middlewares` (actual).
- No verificado cómo ServiceLB/Klipper preserva la IP cliente (afecta `X-Forwarded-For` y logs).

## 3. Aplicar el middleware global vs por ruta y enforcement (allowCrossNamespace, entrypoint middlewares, Kyverno)

### Takeaway
La forma más robusta de "nadie se salta el auth" es declarar el ForwardAuth como middleware por defecto del entrypoint `websecure` (`entryPoints.websecure.http.middlewares`), que se antepone a todo router y no tiene opt-out documentado; complementar con Kyverno para validar Ingress/IngressRoute (no existe política pública lista; hay que escribirla).

### Cited Findings
- `entryPoints.<ep>.http.middlewares`: se anteponen a **todos** los routers del entrypoint, en orden de declaración, antes de los middlewares del IngressRoute/Ingress. Formato CRD: `<namespace>-<name>@kubernetescrd` (con `_` si `safeNaming`). La doc no describe forma de opt-out por router. [EN] — [Traefik EntryPoints](https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/)
- Kubernetes CRD provider: `allowCrossNamespace` default `false` (IngressRoutes no pueden referenciar recursos de otros namespaces salvo que se habilite); `allowExternalNameServices` false; `crossProviderNamespaces` (`[]` rechaza toda referencia cross-provider); `namespaces` vacío = todos; `labelSelector`; `ingressClass`. [EN] — [Traefik Kubernetes CRD provider](https://doc.traefik.io/traefik/reference/install-configuration/providers/kubernetes/kubernetes-crd/)
- IngressRoute: middlewares por ruta con `name` y `namespace` opcional. Ingress estándar: anotación `traefik.ingress.kubernetes.io/router.middlewares: <ns>-<name>@kubernetescrd` separados por coma. [EN] — [Traefik IngressRoute reference](https://doc.traefik.io/traefik/reference/routing-configuration/kubernetes/crd/http/ingressroute/); [Traefik Hub Ingress annotations](https://doc.traefik.io/traefik-hub/api-gateway/reference/routing/kubernetes/http/routers/ref-ingress-annotations)
- El middleware no debe aplicarse al Ingress del propio servicio de auth (Authelia). [EN] — [Authelia: Traefik Ingress (K8s)](https://www.authelia.com/integration/kubernetes/traefik-ingress)
- Nombres/namespaces incorrectos causan errores "does not exist" (foro, v2.2). [EN] — [community.containo.us](https://community.containo.us/t/2-2-cant-see-kubernetes-defined-entrypoint-middlewares/6003)
- No se encontró ninguna política Kyverno publicada que exija middlewares Traefik. [EN] — búsqueda sin resultados relevantes; ver [Traefik IngressRoute reference](https://doc.traefik.io/traefik/reference/routing-configuration/kubernetes/crd/http/ingressroute/) para los paths de campos.
- Advisory GHSA-qvj7-gq9c-hp7q (Oct 2026, High): con el provider Ingress-NGINX de Traefik, un router "sibling" de ssl-passthrough sirve un backend protegido por auth **sin auth** — ejemplo de bypass por rutas paralelas. [EN] — [traefik security advisories](https://github.com/traefik/traefik/security/advisories)

### Inferences
- Diseño recomendado: (a) middleware ForwardAuth en `kube-system` (o namespace `platform`) y referenciado a nivel entrypoint `websecure`; (b) el portal/login y `platform-api` deben quedar exentos — como no hay opt-out, la platform-api debe tratar ella misma las rutas públicas (`/login`, `/callback`, `/healthz`) devolviendo 200 para el host del portal, o exponer el login en otro entrypoint; (c) entrypoint `web` solo redirige a `websecure`.
- Kyverno (borrador, no probado): `validate` que deniegue IngressRoute cuyo `spec.entryPoints` contenga algo distinto de `websecure`, o cuyo `spec.routes[].middlewares` referencie namespaces no permitidos; y para `Ingress`, exigir `ingressClassName: traefik` y anotación `traefik.ingress.kubernetes.io/router.entrypoints: websecure`. Además denegar `Middleware` con `spec.forwardAuth` fuera del namespace de plataforma, y prohibir objetos `IngressRouteTCP`/`TLSOption` en namespaces de apps (TCP passthrough salta HTTP middlewares).
- Mantener `allowCrossNamespace: false` si se usa middleware de entrypoint (no lo necesita); si se referencia per-ruta desde namespaces de apps, habría que habilitarlo, ampliando superficie.

### Gaps
- No hay ejemplo oficial de Kyverno para Traefik; la política debe escribirse y testearse (kyverno test / chainsaw).
- No confirmado si un IngressRoute puede declarar un entrypoint inexistente/nuevo para evitar los middlewares por defecto (depende de que el operador solo exponga `web`/`websecure`).

## 4. Pitfalls de seguridad: spoofing de headers, normalización de paths, CVEs de bypass, rendimiento, WebSockets, CORS y SPA (302 vs 401)

### Takeaway
2025–2026 tuvo una racha de CVEs de bypass de ForwardAuth/middlewares en Traefik (X-Forwarded-Prefix spoofing, alias con underscore, StripPrefixRegex + encoded path, `/../` en matchers, h2c Upgrade tunneling, desync de pool en ForwardAuth). Regla: Traefik ≥ última 3.6.x/3.7.x parcheada, auth-service que solo confíe en Host/Method/Uri fijados por Traefik, y no usar `forwardBody`.

### Cited Findings
- **CVE-2024-45410**: un cliente HTTP/1.1 puede marcar `X-Forwarded-Host` etc. como hop-by-hop vía header `Connection` para que se eliminen; fix 2.11.9 / 3.1.3. (CERT-EU lo puntúa 9.8 y menciona RCE; NVD/Mend no — tratar RCE como no confirmado). [EN] — [NVD CVE-2024-45410](https://nvd.nist.gov/vuln/detail/CVE-2024-45410); [CERT-EU 2024-102](https://cert.europa.eu/publications/security-advisories/2024-102/)
- **CVE-2025-32431**: `/../` en el path permite alcanzar un backend expuesto por otro router saltando la cadena de middlewares; fix 2.11.24 / 3.3.6 / 3.4.0-rc2; CVSS 9.1; workaround regla `PathRegexp`. Relacionado **CVE-2025-47952**, fix 2.11.25 / 3.4.1. [EN] — [NVD CVE-2025-32431](https://nvd.nist.gov/vuln/detail/CVE-2025-32431); [SUSE CVE-2025-32431](https://www.suse.com/security/cve/CVE-2025-32431/); [stack.watch Traefik](https://stack.watch/product/traefik/traefik/)
- **CVE-2026-35051 / GHSA-6384-m2mw-rf54** (publicado 2026-04-24): con `trustForwardHeader=false` Traefik no reconstruye `X-Forwarded-Prefix`; un atacante tras un proxy confiable lo spoofea y, si el auth-service decide por prefix, bypass. Afecta 3.0.0-beta1 < 3.6.14 y 3.7.0-ea.1 < 3.7.0-rc.2; fix 3.6.14 / 3.7.0-rc.2 (y 2.11.43). CVSS 3.1 10.0 en GitLab; otras fuentes 7.8 (CVSS v4) — severidad discrepante. [EN] — [GitLab advisory CVE-2026-35051](https://advisories.gitlab.com/golang/github.com/traefik/traefik/v3/CVE-2026-35051/); [vuln.today](https://vuln.today/cve/CVE-2026-35051)
- **CVE-2026-39858 / GHSA-5m6w-wvh7-57vm**: la sanitización solo cubre nombres canónicos (`X-Forwarded-Proto`), no variantes con underscore (`X_Forwarded_Proto`); si el backend de auth trata `_` y `-` como equivalentes, inyección de scheme/host. Fix 2.11.43 / 3.6.14 / 3.7.0-rc.2. [EN] — [GitLab advisory CVE-2026-39858](https://advisories.gitlab.com/golang/github.com/traefik/traefik/v2/CVE-2026-39858/)
- **CVE-2026-40912 / GHSA-6jwx-7vp4-9847** (2026-04-24): `StripPrefixRegex` aplica el regex al path decodificado pero corta el raw path codificado con esa longitud; ForwardAuth ve en `X-Forwarded-Uri` un path que no matchea sus patrones protegidos y aprueba (`/api%2e/admin/secret` → 200). Requiere backend que normalice dot-segments. Fix 2.11.43 / 3.6.14 / 3.7.0-rc.2. [EN] — [GitLab advisory CVE-2026-40912](https://advisories.gitlab.com/golang/github.com/traefik/traefik/CVE-2026-40912/)
- **CVE-2026-71324** (2026-09-04): desync del pool de conexiones con CONNECT HTTP/2-3 → HTTP/1.1; ForwardAuth con `forwardBody` + `preserveRequestMethod` expone su pool de auth-client. Fix 2.11.53 / 3.6.24 / 3.7.9. NVD 9.1; CNA v4 7.0. [EN] — [SUSE CVE-2026-71324](https://www.suse.com/security/cve/CVE-2026-71324/)
- **CVE-2026-88008** (Red Hat BZ 2531489, sept 2026): `Connection: Upgrade, HTTP2-Settings` con token h2c hacia backend que acepta h2c → 101 → túnel crudo; requests HTTP/2 posteriores no pasan por routers, BasicAuth, **ForwardAuth**, IPAllowList, RateLimit ni logging. Fix 2.11.57 / 3.7.13 (rango afectado 3.x ambiguo en la fuente). [EN] — [Red Hat Bugzilla 2531489](https://bugzilla.redhat.com/show_bug.cgi?id=2531489)
- Advisories GitHub sept–oct 2026: GHSA-v67p-phpq-fc8x (sanitización de nombres de headers en entrypoint bypasseada vía **request trailers**, High); GHSA-qvj7-gq9c-hp7q (bypass de auth vía router sibling ssl-passthrough en provider Ingress-NGINX); varias de reutilización de conexiones NTLM/Negotiate. [EN] — [traefik security advisories](https://github.com/traefik/traefik/security/advisories)
- Un feed menciona CVE-2026-33433 con PoC sobre los mismos rangos, sin advisory primario encontrado: **no verificado**. [EN] — [craftedsignal brief](https://feed.craftedsignal.io/briefs/2026-04-traefik-auth-bypass/)
- Rendimiento: ForwardAuth no cachea; una llamada por request, timeout fijo 30 s. [EN] — [traefik forward.go](https://raw.githubusercontent.com/traefik/traefik/master/pkg/middlewares/auth/forward.go)
- WebSockets/streaming: `forwardBody` rompe streaming (doc oficial); tiny-auth advierte que `forwardBody=true` rompe SSE/WebSocket. [EN] — [Traefik docs](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/); [tiny-auth (pkg.go.dev)](https://pkg.go.dev/github.com/nerdneilsfield/tiny-auth)
- CORS preflight: hilo comunitario donde el preflight OPTIONS de una app Vue devuelve 400 con ForwardAuth activo; deshabilitar el middleware en esa ruta o dejar pasar OPTIONS lo resuelve; no hay opción built-in para excluir patrones. Patrón general: los navegadores no envían credenciales en preflight → auth global devuelve 401. [EN] — [Traefik community: OPTIONS through ForwardAuth](https://community.traefik.io/t/options-method-not-passing-through-forwardauth/26478); [auth0 issue #44](https://github.com/auth0/node-oauth2-jwt-bearer/issues/44)
- Traefik headers middleware no responde preflight por sí mismo en algunos setups (pasa al servicio). [EN] — [Traefik community: headers CORS preflight](https://community.traefik.io/t/traefik-headers-middleware-is-not-handling-cors-preflight-request/9266)

### Inferences
- Versión mínima objetivo (oct 2026): 3.7.13+ (o última 3.6.x con todos los backports: ≥3.6.24 y verificar backport de CVE-2026-88008). Suscribirse a GitHub Security Advisories de traefik/traefik.
- Hardening del auth-service Go: decidir solo con `X-Forwarded-Host` (validar contra allowlist `*.apps.empresa.cl` exacta), `X-Forwarded-Method` y `X-Forwarded-Uri` (normalizar con `path.Clean` tras `url.PathUnescape`, rechazar `%2e`, `%2f`, `..`, `;`); **ignorar** `X-Forwarded-Prefix` y cualquier header con underscore; Go `net/http` no canonicaliza `_` a `-`, pero no usar librerías que lo hagan. No usar StripPrefixRegex delante del ForwardAuth.
- Decisión por host (no por path) para la app es más robusta frente a CVEs de path; el control por path debería hacerlo el backend.
- SPA/XHR: devolver 401 (JSON) en vez de 302 cuando `X-Forwarded-Method != GET/HEAD`, o `Accept` no incluye `text/html`, o hay `X-Requested-With: XMLHttpRequest`, o `X-Forwarded-Uri` empieza por `/api`; la SPA intercepta 401 y navega a login. Un 302 a otro dominio en fetch produce error CORS opaco.
- CORS: como frontend y API están en el mismo host (`/api`), no hay preflight cross-origin; si el portal llama APIs de otras apps, devolver 204 para `OPTIONS` con `Origin` permitido en la propia platform-api (sin exigir sesión) o evitar llamadas cross-origin.
- WebSockets: el handshake es un GET con `Upgrade`; ForwardAuth corre una vez en el upgrade (headers hop-by-hop se eliminan de la subrequest), la sesión WS no se re-valida → expiración/revocación debe gestionarla el backend.
- Latencia/caché: platform-api debe resolver en <~2–5 ms: caché en memoria (ristretto/LRU con TTL 15–60 s) por `(session_id, host)`, firma JWT con clave Ed25519 cacheada, pool pgx; la instalación en el mismo nodo/namespace que Traefik reduce RTT. Considerar TTL corto vs revocación.

### Gaps
- No encontré benchmarks publicados de latencia de ForwardAuth ni issue oficial sobre caché.
- No verificado el rango exacto 3.x afectado por CVE-2026-88008 ni si hay backport a 3.6.x.
- No encontré documentación oficial Traefik sobre ForwardAuth + WebSocket específicamente.

## 5. NetworkPolicies en K3s (kube-router) para que los pods de apps solo acepten tráfico de Traefik

### Takeaway
K3s embebe el controlador de network policy de kube-router (con flannel por defecto), por lo que NetworkPolicy estándar se aplica sin instalar nada; patrón: default-deny ingress por namespace de app + allow desde namespace `kube-system` con selector de pods de Traefik, verificando la IP origen real (ServiceLB/hostPort pueden hacer que llegue como IP de nodo).

### Cited Findings
- K3s trae el netpol controller de kube-router y aplica lo expresable en NetworkPolicy (respuesta de foro). [EN] — [discuss.kubernetes.io: NetworkPolicy k3s + Traefik](https://discuss.kubernetes.io/t/networkpolicy-adjustment-for-k3s-and-traefik/11337)
- `--disable-network-policy` se usa al instalar un CNI propio (`--flannel-backend=none`) porque la mayoría de CNIs traen su propio motor de policies. [EN] — [K3s Basic Network Options](https://docs.k3s.io/networking/basic-network-options)
- Ejemplo de política `allow-ingress-from-traefik` (Kaapana) admitiendo tráfico desde el namespace de Traefik. [EN] — [Kaapana network docs](https://kaapana.readthedocs.io/en/master/_sources/user_guide/system/network.rst.txt)
- Hilo comunitario: política que permitía ingress dejó de funcionar con el Traefik por defecto de K3s, causa no aclarada. [EN] — [Traefik community: NetworkPolicy k3s](https://community.traefik.io/t/networkpolicy-adjustment-for-k3s-and-traefik/6438); [Traefik community: whitelist namespaces](https://community.traefik.io/t/in-k3s-how-to-whitelist-specific-namespaces-to-work-with-traefik/29962.md)
- Guía 2026 sobre network policies en K3s. [EN] — [oneuptime: k3s network policies](https://oneuptime.com/blog/post/2026-03-20-k3s-network-policies/view)

### Inferences
- Plantilla por namespace de app (no probada):
  ```yaml
  apiVersion: networking.k8s.io/v1
  kind: NetworkPolicy
  metadata: {name: allow-from-traefik-only}
  spec:
    podSelector: {}
    policyTypes: [Ingress]
    ingress:
      - from:
          - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: kube-system}}
            podSelector: {matchLabels: {app.kubernetes.io/name: traefik}}
  ```
  más un default-deny, y allow intra-namespace frontend→backend si aplica. Para platform-api: permitir solo desde Traefik (ForwardAuth) y desde el portal. Para PostgreSQL: solo desde platform-api.
- Esto además mitiga bypass de ForwardAuth vía acceso directo pod-a-pod (otra app comprometida llamando al backend sin JWT) — el backend igualmente debe validar el JWT.
- Generar las NetworkPolicies desde Kyverno `generate` al crear un namespace de app asegura que ninguna app quede sin ellas.

### Gaps
- No encontré doc oficial K3s que describa explícitamente el netpol controller de kube-router ni sus limitaciones (p.ej. soporte de `endPort`, logging).
- No verificado el label exacto de los pods de Traefik en el chart de K3s (`app.kubernetes.io/name: traefik` es el estándar del chart, confirmar con `kubectl get pods -n kube-system --show-labels`).

## 6. Referencias de implementaciones reales (Go propio, oauth2-proxy, Authelia, Authentik) y fuentes multilingües

### Takeaway
Hay varias implementaciones Go de ForwardAuth para estudiar (clambin/forward-auth, thomseddon/traefik-forward-auth y su fork, tiny-auth); Authelia/Authentik/oauth2-proxy sirven como referencia de contrato (headers requeridos, `authResponseHeaders`, redirects). La literatura no inglesa sobre ForwardAuth con servicio propio es escasa.

### Cited Findings
- clambin/forward-auth (Go): OIDC + reglas de autorización flexibles, v0.4.0 abril 2026. [EN] — [pkg.go.dev clambin/forward-auth](https://pkg.go.dev/github.com/clambin/forward-auth)
- tiny-auth (Go): API key/Basic/Bearer con comparación en tiempo constante; advierte no usar `forwardBody`. [EN] — [pkg.go.dev tiny-auth](https://pkg.go.dev/github.com/nerdneilsfield/tiny-auth)
- ThetaDev/oidc-forward-auth: OIDC simple, usuario en `X-Forwarded-User`, desarrollo detenido ~2020. [EN] — [oidc-forward-auth](https://code.thetadev.de/ThetaDev/oidc-forward-auth)
- developstorm/go-forward-auth: último update sept 2023. [EN] — [awesome.ecosyste.ms](https://awesome.ecosyste.ms/projects/github.com%2Fdevelopstorm%2Fgo-forward-auth)
- thomseddon/traefik-forward-auth y fork toptal (OAuth/OIDC). [EN] — [toptal/traefik-forward-auth](https://github.com/toptal/traefik-forward-auth); [thomseddon docs](https://docsearch.algolia.com/mcp/docs/repo/thomseddon/traefik-forward-auth)
- asaintsever/traefik-forwardauth-authcode-pkce: Authorization Code + PKCE. [EN] — [GitHub README](https://github.com/asaintsever/traefik-forwardauth-authcode-pkce/blob/master/README.md)
- Tutorial con servicio propio en Node/Express basado en cookie JWT que devuelve 401 y se monta en IngressRoute. [EN] — [DEV: Traefik middleware forward authentication](https://dev.to/lenra/traefik-middleware-forward-authentication-1nc1)
- Authentik: `address` → outpost `/outpost.goauthentik.io/auth/traefik`, `trustForwardHeader: true`. [EN] — [Authentik: Traefik](https://docs.goauthentik.io/add-secure-apps/providers/proxy/server_traefik/)
- Tutorial genérico ForwardAuth (2026) con `authRequestHeaders`/`authResponseHeaders`. [EN] — [oneuptime: Traefik forward authentication](https://oneuptime.com/blog/post/2026-01-23-traefik-forward-authentication/view)
- Keycloak + Traefik: backend obtiene identidad vía headers de confianza. [EN] — [Skycloak blog](https://skycloak.io/blog/keycloak-traefik-integration-guide/)
- OAuth2 Proxy como middleware de Traefik (`trustForwardHeader: true`). [EN] — [Medium: Traefik & OAuth2 Proxy](https://medium.com/@nsalexamy/traefik-oauth2-proxy-using-oauth2-proxy-as-authentication-middleware-a3f4ce7e8a8d)
- StackHarbor KB tiene versión en francés del artículo Traefik + oauth2-proxy. [FR] — [StackHarbor FR](https://stackharbor.com/fr/base-de-connaissances/traefik-oauth2-proxy-integration/)
- Documentación K3s Helm en alemán/chino/japonés (traducciones oficiales SUSE). [DE/ZH/JA] — [SUSE K3s Helm (de)](https://documentation.suse.com/cloudnative/k3s/latest/de/add-ons/helm.html); [docs.k3s.io (ja)](https://docs.k3s.io/ja/add-ons/helm)

### Inferences
- Para el diseño de la platform-api, el contrato de Authelia (`X-Forwarded-*` requeridos, 401 para `Authorization` inválido y redirect para cookie) es un buen modelo para distinguir clientes API vs navegador.
- Ninguna referencia hace autorización por grupo por app derivada del host con JWT firmado hacia el backend: el diseño propuesto es más cercano a un IAP (tipo Cloudflare Access / Pomerium con `X-Pomerium-Jwt-Assertion`) que a oauth2-proxy.

### Gaps
- Búsquedas en japonés ("Traefik ForwardAuth 認証 自作 Go"), chino ("Traefik ForwardAuth 鉴权 中间件 自定义 认证服务") y alemán ("Traefik ForwardAuth Middleware eigener Dienst Kubernetes") devolvieron casi solo fuentes en inglés; no se encontraron write-ups nativos de calidad (Qiita/Zenn/Juejin/CSDN) en el presupuesto de búsqueda. No se hicieron búsquedas en español/portugués.
- No se encontró un write-up detallado de un ForwardAuth en Go con PostgreSQL + JWT downstream.
