# Motores/patrones de autorización centralizada y catálogos/portales de apps internas

Contexto: plataforma de iniciativas (portal + admin + N apps en *.apps.empresa.cl), K3s + Traefik ForwardAuth, platform-api en Go + PostgreSQL como fuente de verdad, control grueso en el borde, fino en cada app vía roles en JWT firmado, AuthZEN como punto de extensión futuro. Fecha de corte: 2026-10-14. Las fuentes están en inglés salvo indicación [ES], [PT], [DE], [JA], [ZH], [KO].

---

## A1. OpenID AuthZEN Authorization API 1.0: forma, endpoints, discovery, implementaciones, librerías Go

### Takeaway
AuthZEN 1.0 es Final (11 ene 2026). Es un contrato JSON/HTTPS pequeño: `{subject, action, resource, context}` -> `{decision, context}`, con endpoints de evaluación individual, por lotes y búsqueda, más discovery en `/.well-known/authzen-configuration`. Cerbos, Topaz, OpenFGA (experimental), Axiomatics, PlainID, Ping y WSO2, entre otros, pasaron la interop. Traefik Hub (comercial) trae un middleware AuthZEN, y en Go existe una librería comunitaria (SCKelemen/authzen). El costo de que platform-api exponga hoy un endpoint compatible es bajo.

### Cited Findings
**Estado**
- La spec es "Authorization API 1.0", Final, publicada el 11 de enero de 2026 en el OpenID AuthZEN WG (Standards Track). Editores: O. Gazitt, D. Brossard y A. Tulshibagwale — [Spec](https://openid.net/specs/authorization-api-1_0.html)
- Votación de aprobación: 81 a favor, 1 en contra y 25 abstenciones (107 de 378 miembros, 28,3 %, sobre el quórum del 20 %). Se anunció el 12 de enero de 2026 y la spec "is not subject to further revision" — [OpenID Foundation](https://openid.net/authorization-api-1-0-final-specification-approved/)
- El WG tiene borradores en curso: un perfil Access Request y un perfil Obligations — [AuthZEN specs page](https://openid.net/wg/authzen/specifications/) (vía resumen de búsqueda; no se verificó el estado de cada borrador)
- Un artículo de dev.to fecha la publicación el 11 de marzo de 2026. Contradice a la spec y a la OIDF, así que se descarta — [dev.to](https://dev.to/kanywst/authzen-authorization-api-10-deep-dive-the-standard-api-that-separates-authorization-decisions-1m2a)

**Forma del request/response**
- `subject` lleva `type` e `id` obligatorios y `properties` opcional. `resource` sigue la misma forma. `action` lleva `name` obligatorio y `properties` opcional. `context` es un objeto opcional con atributos de entorno — [Spec](https://openid.net/specs/authorization-api-1_0.html)
- La respuesta trae `decision` (boolean, obligatorio) y `context` opcional. Un deny se devuelve como HTTP 200 con `{"decision": false}`, no como error HTTP. Los errores usan 400, 401, 403 o 500 — [Spec](https://openid.net/specs/authorization-api-1_0.html)
- Binding HTTPS+JSON: todo va por POST con `Content-Type: application/json` y TLS obligatorio. Se recomienda OAuth 2.0 para autenticar al PEP, aunque el método queda fuera de alcance. `X-Request-ID` es opcional y, si el PEP lo envía, el PDP debe devolverlo — [Spec](https://openid.net/specs/authorization-api-1_0.html)

**Endpoints (rutas por defecto, sobreescribibles vía metadata)**
- Evaluación individual: `POST /access/v1/evaluation` — [Spec](https://openid.net/specs/authorization-api-1_0.html)
- Lote: `POST /access/v1/evaluations`. Recibe un array `evaluations`, y el subject/action/resource/context de nivel superior actúan como defaults que cada ítem puede sobreescribir. La respuesta mantiene el orden. La semántica se elige con `evaluations_semantic`: `execute_all` (por defecto), `deny_on_first_deny` o `permit_on_first_permit` — [Spec](https://openid.net/specs/authorization-api-1_0.html)
- Búsquedas: `/access/v1/search/subject`, `/access/v1/search/resource` y `/access/v1/search/action`. Se omite el id de la entidad buscada y se recibe la lista de entidades permitidas. La paginación usa `page.token`, `page.limit` y `page.next_token`; un `next_token` vacío indica el final, y los parámetros deben ser idénticos entre páginas — [Spec](https://openid.net/specs/authorization-api-1_0.html)
- Discovery: `/.well-known/authzen-configuration`. Campos obligatorios: `policy_decision_point` y `access_evaluation_endpoint`. Opcionales: `access_evaluations_endpoint`, `search_subject_endpoint`, `search_action_endpoint`, `search_resource_endpoint`, `capabilities` (URNs) y `signed_metadata` (JWT) — [Spec](https://openid.net/specs/authorization-api-1_0.html)

**Implementaciones (interop)**
- PDPs con resultados en authzen-interop.net:
  - Todos los escenarios (Todo, Gateway, Search e IdP): Axiomatics, Cerbos, Topaz, PlainID y PingAuthorize.
  - Gateway y/o Search: WSO2, SGNL, EmpowerID, Indykite, Hexa, Rock Solid Knowledge y Permit.
  - Solo algunos escenarios:
    - Open Policy Agent: solo Todo (drafts 00, 01 y 02).
    - OpenFGA: Todo 02 y Gateway 02.
    - Amazon Verified Permissions: Todo 02 y Gateway 02.
    - Aserto: Todo y Gateway.
    - Apache KIE/Kogito.
    - Thales/AuthzForce: Todo 00.
    - 3Edges: Todo 00.

  — [AuthZEN interop results](https://authzen-interop.net/docs/intro)
- Gateways PEP en interop: AWS API Gateway, Envoy (demo de Cerbos), Kong (PlainID), Tyk, Layer7, WSO2 y Zuplo. **Traefik no figura en la lista de interop** — [AuthZEN interop](https://authzen-interop.net/docs/intro)
- IdPs como PEP (escenario search): Auth0, Curity, Duende, EmpowerID, Gluu/Janssen, Keycloak, PingFederate, PingOne y Thales — [AuthZEN interop](https://authzen-interop.net/docs/intro)
- Hubo una demo de interop en vivo en el Gartner IAM Summit con AWS, Cerbos, Aserto, Axiomatics, OpenFGA, Kong y Envoy, entre otros — [OIDF](https://openid.net/authzen-shows-enterprise-readiness-at-gartner-iam-summit/)
- Un issue de octubre de 2026 señala que nada en el repo del WG ejecuta realmente el escenario de certificación. No se encontró un programa de certificación formal publicado — [GitHub issue decern#173](https://github.com/anivar/decern/issues/173)

**Soporte por producto**
- **Cerbos**: PDP v0.48 (diciembre 2025) implementa los endpoints de AuthZEN 1.0 — [Cerbos v0.48](https://www.cerbos.dev/blog/cerbos-pdp-v0-48-open-id-auth-zen-support-improved-query-plans-faster-bundle-loading). El blog de enero de 2026 dice que soporta evaluación y evaluaciones por lote desde hace tiempo — [Cerbos blog](https://www.cerbos.dev/blog/openid-authzen-is-official-cerbos-is-ready)
- **OpenFGA**: el soporte es **experimental**.
  - Activación: `--experimentals=authzen` u `OPENFGA_EXPERIMENTALS=authzen`. El discovery además requiere `--authzen-base-url`.
  - Rutas: los endpoints van bajo `/stores/{store_id}/access/v1/` y el discovery está en `/.well-known/authzen-configuration/{store_id}`.
  - Mapeo de conceptos: subject -> user `type:id`, resource -> object, action -> relation.
  - Mapeo de endpoints: evaluation -> Check, evaluations -> BatchCheck, subject search -> ListUsers, resource search -> ListObjects.
  - La documentación recomienda la API nativa para integración directa. AuthZEN no define escritura de tuplas.

  — [OpenFGA docs](https://openfga.dev/docs/interacting/authzen)
- **Topaz**: endpoints AuthZEN nativos (REST y gRPC) desde Topaz 0.32.54 (febrero 2025) — [Aserto blog](https://www.aserto.com/blog/authzen-gartner-iam-and-native-support-in-topaz). **Alerta de vigencia**: Aserto cerró su SaaS y la compañía el 31 de mayo de 2025. Topaz sigue como open source con mantenedores comunitarios (maintainers@topaz.sh) — [Aserto "The final chapter"](https://www.aserto.com/blog/the-final-chapter-for-aserto)
- **Traefik Hub**: tiene un middleware `plugin: authzen`.
  - Campos: `pdpEndpoint` (URL de discovery, p. ej. OpenFGA por store), `mapping.evaluation` (subject/action/resource/context con expresiones como `$token.sub` o `$request.body.x`), `mapping.evaluations`, `onDenyResponse` y `clientConfig` (`timeoutSeconds`, `maxRetries`).
  - Requiere que un middleware JWT corra antes.
  - Falla cerrado: si el PDP no responde devuelve 500.
  - La documentación no menciona caché.

  — [Traefik Hub docs](https://doc.traefik.io/traefik-hub/api-gateway/guides/authzen-walkthrough). La página está bajo Traefik Hub (producto comercial) y no confirma disponibilidad en Traefik Proxy OSS.
- Curity y Gravitee AM documentan integración AuthZEN — [Curity](https://curity.io/resources/learn/authzen/), [Gravitee AM](https://documentation.gravitee.io/am/4.10/guides/authorization-engines/authzen)

**Librerías Go**
- `github.com/SCKelemen/authzen` declara implementar AuthZEN 1.0 Final sin dependencias externas. El paquete `server` recibe una implementación de PDP y devuelve un `http.Handler` con evaluation, evaluations, search y metadata. Incluye cliente, CLI y un binding gRPC en un submódulo aparte — [pkg.go.dev root](https://pkg.go.dev/github.com/SCKelemen/authzen), [pkg.go.dev server](https://pkg.go.dev/github.com/SCKelemen/authzen/server)
- Una charla de Niko Köbler (Keycloak) trata AuthZEN — [Speakerdeck "Find your AuthZEN"](https://speakerdeck.com/dasniko/find-your-authzen)

### Inferences
- La forma de AuthZEN encaja con el dominio del proyecto: subject `{type:"user", id:sub}`, action `{name:"access"}`, resource `{type:"initiative"|"deployment", id:host}`. Exponer `/access/v1/evaluation` y `/.well-known/authzen-configuration` en platform-api (o en un adaptador) permitiría cambiar después a Cerbos u OpenFGA sin tocar los PEPs.
- `search/resource` corresponde exactamente a "qué apps puede ver este usuario" (el launcher del portal). Es una buena razón para modelar la consulta del portal como resource search.
- Traefik OSS no tiene un middleware AuthZEN confirmado, así que ForwardAuth hacia platform-api sigue siendo el camino OSS. AuthZEN se usaría entre platform-api y un PDP externo, o entre las apps y platform-api.
- SCKelemen/authzen es un proyecto pequeño de un solo autor. Conviene evaluar su madurez antes de depender de él; los tipos del wire son lo bastante simples para implementarlos a mano.

### Gaps
- No se verificó la versión exacta de OpenFGA que introdujo AuthZEN ni si sigue siendo experimental en la última release de octubre de 2026.
- No se confirmó el soporte AuthZEN nativo de OPA como servidor. En la interop OPA aparece solo en Todo, probablemente vía adaptador; no se encontró documentación oficial de OPA sobre AuthZEN.
- No se confirmó si el middleware AuthZEN de Traefik existe en Traefik Proxy OSS ni su versión.
- No se encontraron otros SDKs Go de AuthZEN mantenidos por vendors.

---

## A2. Motores: OPA, Cedar/cedar-go, OpenFGA, SpiceDB, Cerbos, Casbin; ¿basta RBAC en PostgreSQL?

### Takeaway
Hay que elegir por modelo dominante:
- **ReBAC** (relaciones, compartir recursos, jerarquías): OpenFGA o SpiceDB.
- **Políticas/ABAC**: Cerbos (YAML+CEL, el más simple), Cedar (formalmente verificable, cedar-go embebible) u OPA/Rego (generalista, curva más alta).
- **Librería embebida en Go con RBAC por dominio**: Casbin.

Para "grupo -> app -> rol" con pocos roles por app, RBAC en PostgreSQL basta. Un motor se justifica cuando aparecen permisos por recurso individual (explosión de roles), relaciones o herencia, o reglas por atributos o contexto.

### Cited Findings
**Clasificación general**
- OpenFGA y SpiceDB son ReBAC inspirados en Zanzibar. Cerbos, OPA y Cedar son policy/ABAC sin grafo de relaciones incorporado — [sph.sh (2026-03-22)](https://sph.sh/en/posts/external-authorization-management-systems/), [sapl.io comparison](https://sapl.io/guides/comparison/)

**OPA**
- Motor de políticas general en Rego, graduado de CNCF y más fuerte en infraestructura (admission de Kubernetes). Se estima que Rego toma entre 30 y 40 horas de aprendizaje — [sph.sh](https://sph.sh/en/posts/external-authorization-management-systems/)
- El SDK Go (`github.com/open-policy-agent/opa/v1/sdk`) permite embeber OPA con `sdk.New` y `Decision`, y trae decision logs configurables — [pkg.go.dev OPA v1 sdk](https://pkg.go.dev/github.com/open-policy-agent/opa/v1/sdk), [OPA integration docs](https://www.openpolicyagent.org/docs/integration.md)
- Los decision logs incluyen query, input, result, timestamps, decision ID y metadata del bundle, lo que sirve para auditoría — [Orca Security](https://orca.security/resources/blog/what-is-open-policy-agent/)
- **Alerta de vigencia**:
  - En agosto de 2025 Apple contrató a los maintainers core de OPA (Tim Hinrichs, Teemu Koponen, Torin Sandall). OPA sigue en CNCF con gobernanza intacta — [Cloud Native Now](https://cloudnativenow.com/features/apple-buys-styra-brains-opa-remains-open/), [heise [DE]](https://heise.de/-10574287), [OpenSourceForU](https://www.opensourceforu.com/2025/08/apple-acquires-open-policy-agent-developers-while-cncf-retains-control-of-open-source-project/)
  - El futuro de los productos comerciales de Styra (DAS, EOPA) es incierto o estaría pasando a open source, con versiones contradictorias — [Medium](https://medium.com/@mathew_30470/opa-got-acquired-sort-of-whats-next-ea54f392d748); [Oso blog (competidor)](https://www-webflow.osohq.com/post/opa-maintainers-join-apple-oss-community-to-maintain-styra-products)
  - sph.sh afirma que Styra DAS está en sunset — [sph.sh](https://sph.sh/en/posts/external-authorization-management-systems/)

**Cedar / cedar-go**
- Cedar está orientado a ABAC, con verificación formal, y se usa como servicio gestionado en Amazon Verified Permissions — [sph.sh](https://sph.sh/en/posts/external-authorization-management-systems/), [Styra OPA vs Cedar](https://www.styra.com/knowledge-center/opa-vs-cedar-aws-verified-permissions/)
- **cedar-go** (`github.com/cedar-policy/cedar-go`):
  - Implementa el autorizador core, JSON, todos los tipos (incluido datetime/duration, RFC 80) y parsing de schema.
  - **No implementa** validador estable (solo experimental en `x/exp/schema`), formatter, evaluación parcial (solo en `x/exp/batch`), templates de políticas ni CLI.
  - `x/exp` queda fuera de semver.
  - API: `cedar.NewPolicySet()`, `ps.Add`, `cedar.EntityMap`, `cedar.Authorize(ps, entities, req)`.

  — [cedar-go GitHub](https://github.com/cedar-policy/cedar-go)
- [JA] Cedar está acotado a autorización, mientras que OPA es un motor general. Se señala que el tooling y la comunidad de Cedar son menores — [InfoQ JP / resumen de búsqueda](https://www.infoq.com/jp/news/2023/06/open-fine-grained-authorization/) (2023, posiblemente desactualizado)

**OpenFGA**
- ReBAC basado en Zanzibar, proyecto CNCF y motor de Auth0/Okta FGA. Okta FGA declara hasta 100.000 millones de relaciones, 1 millón de RPS y SLA de 99,99 % — [sph.sh](https://sph.sh/en/posts/external-authorization-management-systems/)
- Recomienda un servicio central único al que cada microservicio consulta con `check` en lugar de mantener tablas de roles propias, y menciona prácticas de caché y latencia — [OpenFGA use case](https://openfga.dev/docs/use-cases/microservices-authorization)
- Sus docs dicen que RBAC sirve para modelos planos y pierde fuerza con jerarquía, compartición o multi-tenancy — [OpenFGA concepts](https://openfga.dev/docs/authorization-concepts)

**SpiceDB**
- Zanzibar "más fiel": usa ZedTokens para consistencia. Self-hosted requiere PostgreSQL o CockroachDB más Kubernetes. OpenAI lo usa para los conectores de ChatGPT Enterprise — [sph.sh](https://sph.sh/en/posts/external-authorization-management-systems/)
- Datastores: PostgreSQL, CockroachDB, Spanner, MySQL y memoria. La v1.14 reescribió el datastore de Postgres con grandes mejoras de escritura. Las escrituras afectan más el rendimiento que los nodos — [SpiceDB v1.14.0](https://github.com/authzed/spicedb/releases/tag/v1.14.0), [SpiceDB load testing (dev.to)](https://dev.to/authzed/spicedb-load-testing-guide-5591)
- Modos de consistencia: full es el más lento; minimize-latency es el más rápido pero puede devolver datos levemente desactualizados — [Frontier docs](https://www.mintlify.com/raystack/frontier/configuration/database)

**Cerbos**
- ABAC con YAML + CEL y sin grafo de relaciones. Se despliega como binario único, contenedor o sidecar; Cerbos Hub es la versión gestionada. Curva de aprendizaje baja — [sph.sh](https://sph.sh/en/posts/external-authorization-management-systems/), [Cerbos comparisons](https://www.cerbos.dev/comparisons)
- Soporta AuthZEN 1.0 desde v0.48 (ver A1).

**Casbin (Go)**
- El modelo "RBAC with domains" usa `g = _, _, _`, donde el tercer campo es el dominio o tenant, de modo que un usuario puede tener roles distintos por dominio. Ofrece APIs como `GetAllUsersByDomain` — [Casbin docs](https://casbin.apache.org/docs/rbac-with-domains)
- El gorm-adapter soporta PostgreSQL (`gormadapter.NewAdapterByDB`). El filtered loading por dominio existe en algunos adapters — [Casbin docs](https://casbin.apache.org/docs/rbac-with-domains), [skills.cat (no oficial)](https://skills.cat/skills/meriley/claude-code-skills/implementing-casbin)
- [ZH] Un artículo lo describe como "la librería de control de permisos más potente de Go" — [CSDN/gitcode](https://gitcode.csdn.net/69ca3b020a2f6a37c59b90ab.html)

**Despliegue**
- Central PDP: fuente única y auditoría simple, a cambio de un punto único de falla y un salto de red por decisión.
- Sidecar: evaluación local, a cambio de costo por pod y complejidad de sincronización.
- Embebido (Casbin, Cedar SDK): lo más rápido, pero atado al lenguaje y difícil de gobernar entre servicios.

— [sph.sh](https://sph.sh/en/posts/external-authorization-management-systems/)

**Rendimiento**
- sph.sh afirma que Cedar evalúa entre 28 y 35 veces más rápido que OpenFGA y entre 42 y 80 veces más rápido que Rego, cifras atribuidas a un benchmark de Teleport. Los propios autores advierten que no es una comparación justa (políticas locales vs recorrido de grafo) — [sph.sh](https://sph.sh/en/posts/external-authorization-management-systems/), [openfga-cedar-comparison](https://github.com/openfga/openfga-cedar-comparison)
- Las cifras de costo por 100 millones de decisiones que aparecen en sph.sh **no tienen fuente**; se descartan.

**¿Cuándo basta RBAC?**
- Con unos cinco roles y sin variación por recurso, RBAC basta y FGA es excesivo. Se adopta FGA cuando aparecen roles del tipo "editor-del-proyecto-X" (explosión de roles). La mayoría de los SaaS B2B usan RBAC para lo grueso y FGA para lo fino — [guptadeepak.com (2026)](https://guptadeepak.com/ciam-compass/guides/fine-grained-authorization-fga/)
- [PT] La combinación más común en producción es RBAC para categorías amplias y ReBAC para permisos a nivel de recurso — [OpenReplay PT](https://blog.openreplay.com/pt/aplicativos-modernos-funcoes-permissoes/)
- [DE] Los roles estáticos, la falta de contexto y la escalabilidad son límites de RBAC; OPA se plantea como complemento — [cidaas](https://www.cidaas.com/blog/open-policy-agent/); versión DE de OpenReplay: [OpenReplay DE](https://blog.openreplay.com/de/moderne-apps-rollen-berechtigungen/)

### Inferences
- "Grupo -> iniciativa -> rol" es RBAC con dominio (dominio = iniciativa), equivalente a Casbin RBAC-with-domains, y se resuelve con 2 o 3 joins en Postgres. Para N apps y decenas de roles no hace falta un motor.
- Señales para migrar a un motor:
  - Permisos sobre objetos dentro de cada app (documentos, proyectos), lo que apunta a OpenFGA o SpiceDB por app o compartido.
  - Reglas con atributos como horario, IP, departamento o nivel de clasificación, lo que apunta a Cerbos o Cedar.
  - Necesidad de que equipos no-dev editen políticas versionadas.
- Si se adopta un motor, Cerbos es el de menor fricción: soporta AuthZEN 1.0, YAML y un binario. OpenFGA es la opción si el problema es relacional, y además corre sobre Postgres.
- cedar-go sirve para embeber dentro de platform-api sin servicio extra, pero sin validador estable conviene validar las políticas con el CLI Rust en CI.
- OPA es la opción menos atractiva para un equipo Go pequeño centrado en autorización de apps: curva de Rego e incertidumbre comercial post-Styra, aunque el core es estable en CNCF.

### Gaps
- No hay benchmarks independientes y reproducibles de latencia p99 de Cerbos, OpenFGA o SpiceDB sobre Postgres.
- No se verificaron las releases más recientes (octubre 2026) de cada motor.
- No se encontraron fuentes nativas de Zenn o Qiita [JA] ni de Juejin [ZH]: las búsquedas devolvieron sobre todo material en inglés.

---

## A3. Patrones: PEP/PDP/PIP/PAP, grueso en gateway vs fino en app, caching, policy-as-code vs DB, auditoría

### Takeaway
El patrón consolidado es un PEP en el gateway para control grueso (autenticado + ¿tiene acceso a esta app?) más un PEP en cada servicio para control fino, ambos contra un PDP o modelo de datos único. El gateway solo nunca basta. Conviene un log de decisiones centralizado y caché con invalidación.

### Cited Findings
**Componentes XACML/NIST**
- NIST SP 800-162 define ABAC como la evaluación de atributos de sujeto, objeto, operación y entorno contra políticas — [NIST CSRC](https://csrc.nist.gov/pubs/itlb/2014/03/attribute-based-access-control-abac-definition-and/final)
- El PDP decide permit o deny. El PEP intercepta, consulta al PDP y aplica la decisión — [CyberDefenders (cita NIST)](https://cyberdefenders.org/cybersecurity-glossary/attribute-based-access-control-abac/)
- El PAP es donde se administran las políticas. El PIP obtiene atributos de fuentes externas como HR o el directorio — [Cyberhaven](https://www.cyberhaven.com/infosec-essentials/abac)
- [KO] Se añade un "context handler" que reúne atributos — [itwiki.kr](https://itwiki.kr/w/%EC%86%8D%EC%84%B1_%EA%B8%B0%EB%B0%98_%EC%A0%91%EA%B7%BC%ED%86%B5%EC%A0%9C)

**"Five laws of cloud-native authorization" (CNCF, 2023, autor Omri Gazitt, de Aserto)**
1. Servicio de autorización unificado desplegado cerca de las apps.
2. Checks en tiempo real con capa de caché inteligente: al estilo Zanzibar, datos cacheados en el borde y propagación de cambios.
3. Autorización de grano fino.
4. Políticas como código versionado.
5. Logs de decisiones centralizados para forense y cumplimiento.

— [CNCF blog](https://www.cncf.io/blog/2023/01/19/the-five-laws-of-cloud-native-authorization/)

**Gateway vs servicio**
- [ES] La AWS Prescriptive Guidance para SaaS multi-tenant propone un PDP centralizado (sidecar de OPA) con PEPs tanto en el punto de entrada (gateway) como en cada microservicio — [AWS Guía prescriptiva (es_es)](https://docs.aws.amazon.com/es_es/prescriptive-guidance/latest/saas-multitenant-api-access-authorization/using-opa.html)
- Traefik + OPA: el gateway autoriza según la ruta del microservicio y el rol en el JWT — [Appsecco](https://appsecco.com/blog/microservices-authorization-using-open-policy-agent-and-traefik-api-gateway)
- APISIX + OPA hacen RBAC en el gateway leyendo roles del JWT — [API7](https://api7.ai/blog/rbac-with-api-gateway-opa), [DZone](https://dzone.com/articles/rbac-with-api-gateway-and-open-policy-agentopa)
- El chequeo en el gateway no basta si los servicios o datos son alcanzables por otras rutas: el control fino debe estar en el límite del servicio — [hoop.dev](https://hoop.dev/blog/your-users-are-only-as-safe-as-the-gates-you-build), [arc42 quality](https://quality.arc42.org/approaches/fine-grained-authorization)
- Mia-Platform evitó un servicio RBAC central como punto único de falla y usó un sidecar por servicio en Kubernetes — [Mia-Platform](https://mia-platform.eu/blog/role-based-access-control-rbac/)
- [ZH] SRBAC (Gitee) combina un servicio central de roles y permisos entre sistemas con un plugin APISIX para autorización en el gateway, de modo que cada sistema de negocio no reimplementa permisos ("SRBAC 实现集中的、跨系统的角色权限管理和分配") — [Gitee SRBAC](https://gitee.com/lbzcode/srbac)

**Auditoría y caché**
- Los decision logs de OPA incluyen input, result y decision ID (ver A2) — [Orca](https://orca.security/resources/blog/what-is-open-policy-agent/)
- La consistencia frente a la latencia en la caché de SpiceDB es configurable (ver A2) — [Frontier docs](https://www.mintlify.com/raystack/frontier/configuration/database)
- El middleware AuthZEN de Traefik Hub falla cerrado: un PDP inaccesible devuelve 500 y no un deny — [Traefik Hub docs](https://doc.traefik.io/traefik-hub/api-gateway/guides/authzen-walkthrough)

**Policy-as-code vs DB**
- Policy-as-code permite que seguridad cambie políticas sin tocar el código de la app — [CNCF blog](https://www.cncf.io/blog/2023/01/19/the-five-laws-of-cloud-native-authorization/)
- Backstage advierte que `spec.owner` es solo de presentación y no debe usarse para asignar autorización en runtime. Es un ejemplo de no mezclar metadata de catálogo con authz — [Backstage descriptor format](https://backstage.io/docs/features/software-catalog/descriptor-format/)

### Inferences
- El diseño previsto (ForwardAuth grueso + roles en JWT para lo fino) es el patrón estándar. El JWT actúa como un "PIP precomputado": rápido, pero desactualizado hasta que expira. Conviene un TTL corto (5–15 min) y una revocación por versión de sesión o un `authz_version` en la DB.
- Caché en ForwardAuth: cachear la decisión `(user, host)` en memoria con un TTL de segundos e invalidarla vía NOTIFY de Postgres al cambiar `initiative_access`.
- Fail-closed: si platform-api cae, Traefik ForwardAuth devuelve error, lo que significa disponibilidad total dependiente de platform-api. Hacen falta réplicas y health checks.
- En platform-api, la "regla" grueso de acceso es DB-driven (datos editables por el admin), no policy-as-code. Las reglas estructurales (p. ej. "admin de plataforma ve todo") pueden vivir en código Go con tests.
- Auditoría: separar `audit_events` (cambios administrativos, el PAP) de los decision logs (denegaciones en el borde). Loguear todas las decisiones allow en ForwardAuth genera mucho volumen; lo usual es loguear deny y muestrear allow.

### Gaps
- No se obtuvo el texto primario de NIST SP 800-162 sobre PAP/PIP; las definiciones vienen de fuentes secundarias.
- No se encontraron artículos nativos [JA] de Zenn/Qiita ni [ZH] de Juejin/InfoQ China sobre este patrón. La única fuente [ZH] es Gitee SRBAC.

---

## A4. Modelado de datos RBAC con roles con alcance (plataforma vs por app) en PostgreSQL

### Takeaway
Las asignaciones deben llevar alcance: (sujeto, rol, scope_type, scope_id), con roles de plataforma (scope global) separados de roles por app. Los roles se definen por app (initiative_roles) y la asignación es tenant/app-aware. RLS de Postgres puede añadirse como defensa en profundidad.

### Cited Findings
- La pregunta correcta es "¿tiene el usuario el rol X **en este tenant**?". Aunque los roles sean globales, las asignaciones deben llevar el tenant para evitar fuga de privilegios entre tenants — [WorkOS](https://workos.com/blog/how-to-design-multi-tenant-rbac-saas)
- Modelo de tres niveles: "system" (admins globales que crean tenants), "tenant" (roles y recursos por tenant) y "resource" (recurso dentro del tenant). Permite roles transversales entre tenants — [Aserto blog](https://www.aserto.com/blog/authorization-101-multi-tenant-rbac) (vendor ya cerrado; el contenido conceptual sigue siendo válido)
- Binding de rol tenant-wide (sin scope) o con scope de recurso mediante scope type + scope ID — [AuthSec docs](https://apidocs.authsec.ai/docs/roles-permissions/overview)
- Derivar `tenant_id` del token verificado y nunca del cliente. Añadir RLS como defensa en profundidad, con una política basada en un setting de sesión por request — [claudskills (guía no oficial)](https://claudskills.com/skills/design-authorization-model/SKILL.md)
- Patrón de dos usuarios de base de datos: uno restringido por RLS para operaciones de la organización y otro sin restricción para operaciones cruzadas — [Permiso GitHub](https://github.com/abdec2/permiso)
- Diseño clásico de seis tablas (users, roles, permissions y sus tablas de enlace). Las grandes organizaciones agregan groups, org y positions — [besthub](https://www.besthub.dev/articles/comprehensive-rbac-permission-system-design-models-constraints-and-database-schemas-bad47409b503)
- Role groups que heredan la unión de permisos de sus roles hijos — [HackerNoon](https://sia.hackernoon.com/role-based-access-control-design-for-micro-services-dg1233079)
- Casbin RBAC-with-domains es la misma idea expresada como un modelo de política — [Casbin](https://casbin.apache.org/docs/rbac-with-domains)
- Implementaciones de RBAC sobre Postgres: [Tembo "building RBAC"](https://tembo.io/blog/building-rbac), [SeaORM Pro RBAC](https://www.sea-ql.org/sea-orm-pro/docs/rbac/overview/)

### Inferences
Esquema sugerido, coherente con las tablas ya planificadas:

**Tablas**
- `platform_roles(user_or_group, role)` para admin, auditor y similares. Opcionalmente puede unificarse con `role_assignments(subject_type, subject_id, role_id, scope_type ENUM('platform','initiative'), scope_id NULL)`, con `CHECK ((scope_type='platform') = (scope_id IS NULL))`.
- `initiative_roles(id, initiative_id FK, key, name, UNIQUE(initiative_id,key))`: roles definidos por cada app.
- `initiative_access(initiative_id, subject_type ENUM('user','group'), subject_id, initiative_role_id NULL)`. Un `initiative_role_id` NULL significa acceso sin rol específico. Hay que asegurar con FK compuesta que el rol pertenece a la misma iniciativa: `FOREIGN KEY (initiative_id, initiative_role_id) REFERENCES initiative_roles(initiative_id, id)`.

**Consultas**
- ForwardAuth: `host -> deployment -> initiative`, y luego existe una fila de acceso para el user o alguno de sus grupos.
- Claims del JWT: `roles` filtrados por `initiative_id` (audiencia = app), lo que evita filtrar roles de otras apps.

**Otros**
- Índices: `(subject_type, subject_id)` en access para el portal y `deployments(host) UNIQUE` para ForwardAuth.
- `audit_events` append-only (sin UPDATE/DELETE vía GRANT) con actor, acción, target y diff en JSONB.

### Gaps
- No se encontró una fuente autoritativa única (p. ej. de la documentación de PostgreSQL) con este esquema exacto de roles de plataforma vs por app; el esquema es una inferencia a partir de las fuentes.

---

## B1. Cómo presentan "mis apps" y gestionan el registro las soluciones existentes

### Takeaway
Los productos IAP e IdP (authentik, Pomerium, Pangolin, Teleport, Entra, Okta) filtran el launcher evaluando la misma política de acceso que aplican en el proxy. Los dashboards estáticos (Homepage, Homarr, Heimdall, Dashy) no filtran por usuario. Backstage es un catálogo de software para desarrolladores, no un launcher filtrado por acceso, y es excesivo para este caso.

### Cited Findings
**authentik**
- La visibilidad se controla con bindings (policies, groups o users). Sin bindings, todos tienen acceso. "Policy engine mode" puede ser `All` o `Any` — [authentik docs](https://docs.goauthentik.io/add-secure-apps/applications/manage_apps/)
- La "Application Dashboard" muestra las apps accesibles. Desde 2026.5 existe la opción "Hide from Application Dashboard"; antes se usaba el truco de Launch URL `blank://blank`, que migra automáticamente — [authentik docs](https://docs.goauthentik.io/add-secure-apps/applications/manage_apps/)
- La Launch URL `/application/launch/<slug>/` lleva al usuario directo a la app, pasando antes por login si hace falta. Las apps tienen un "group" opcional para agruparse. Los entitlements por app (desde 2024.12) se gestionan en la UI o por API — [authentik docs](https://docs.goauthentik.io/add-secure-apps/applications/manage_apps/)
- Registro: Admin UI (Applications > New Application, con provider) — [authentik docs](https://docs.goauthentik.io/add-secure-apps/applications/manage_apps/)

**Pomerium**
- El "Routes Portal" está en `/.pomerium/routes` y muestra solo las rutas autorizadas según la política de cada ruta. También expone una API JSON en `/.pomerium/api/v1/routes` con los campos `id`, `name`, `type`, `from`, `description` y `connect_command` — [Pomerium docs](https://www.pomerium.com/docs/capabilities/routes-portal)
- Limitaciones:
  - Solo un subconjunto del Pomerium Policy Language se usa para filtrar: `accept`, `authenticated_user`, `domain`, `email`, `groups` y `user`.
  - `and` solo admite un criterio.
  - El portal no se puede desactivar.
  - La última versión estable documentada es la v0.32.

  — [Pomerium docs](https://www.pomerium.com/docs/capabilities/routes-portal)

**Pangolin**
- El "Resource Launcher" es la home de la organización: muestra los recursos otorgados explícitamente al usuario o a su rol. Los no-admin aterrizan ahí. Permite búsqueda, filtros por sitio o label y agrupación, y un panel de detalle con URL, alias o estado de certificado — [Pangolin docs](https://docs.pangolin.net/manage/resource-launcher)

**Teleport**
- Las apps se etiquetan con labels estáticos o dinámicos y los roles usan `app_labels` en allow/deny. Los valores pueden venir de traits del IdP (`external.*`). Los usuarios acceden por la Web UI — [Teleport RBAC](https://goteleport.com/docs/enroll-resources/application-access/configuration/controls/), [Teleport connecting apps](https://docs.amplify.goteleport.com/docs/enroll-resources/application-access/guides/connecting-apps)
- Registro dinámico de apps como recursos, con permisos CRUD sobre `app` — [Teleport dynamic registration](https://goteleport.com/docs/enroll-resources/application-access/configuration/dynamic-registration/)

**Microsoft Entra My Apps (referencia UX)**
- Una app aparece si "Visible to users" = Yes **y** el usuario o grupo está asignado. Ocultarla no impide el acceso por otros caminos. Las "Collections" agrupan apps y las configura el admin — [Microsoft Learn](https://learn.microsoft.com/en-us/entra/identity/enterprise-apps/myapps-overview), [Hide app](https://learn.microsoft.com/en-us/entra/identity/enterprise-apps/hide-application-from-user-portal), [MS Q&A collections](https://learn.microsoft.com/en-us/answers/questions/1064350/apps-not-displaying-in-a-myapps-collection)
- "Assignment Required?" no afecta la visibilidad en My Apps; lo que la determina es la asignación — [MS Q&A](https://learn.microsoft.com/en-us/answers/questions/1064350/apps-not-displaying-in-a-myapps-collection)

**Homepage (dashboard estático)**
- Descubre servicios por anotaciones en Ingress: `gethomepage.dev/enabled`, `name`, `group`, `icon`, `description`, `widget.*`, `pod-selector`, `weight` e `instance`.
- Las IngressRoute de Traefik requieren `traefik: true` y **obligatoriamente** `gethomepage.dev/href`.
- Las HTTPRoute de Gateway API requieren `gateway: true`.
- No documenta autenticación ni filtrado por usuario.

— [Homepage docs](https://gethomepage.dev/configs/kubernetes/)

**Backstage**
- `catalog-info.yaml` con `apiVersion: backstage.io/v1alpha1`, `kind`, `metadata` (`name`, `title`, `description`, `labels`, `annotations`, `tags`, `links[url,title,icon,type]`) y `spec`. Para Component, `spec` exige `type`, `lifecycle` y `owner`.
- Kinds: Component, API, System, Domain, Resource, Group, User, Location y Template.
- Se ingiere vía entidades Location.
- `owner` no debe usarse para autorización en runtime.

— [Backstage descriptor format](https://backstage.io/docs/features/software-catalog/descriptor-format/)
- Coste y esfuerzo de Backstage:
  - El esfuerzo de setup, mantenimiento y adopción lo hace "sobredimensionado" para equipos pequeños sin equipo de plataforma dedicado — [ayedo [empresa DE, post EN]](https://ayedo.de/en/posts/spotify-backstage-potenziale-und-herausforderungen-interner-developer-plattformen/)
  - El coste real es el personal: hosting de 200 a 500 USD al mes frente a estimaciones de 200 mil a más de 1 millón de USD al año con personas incluidas. Es una cifra de vendor (Roadie) — [Roadie](https://roadie.io/blog/the-true-cost-of-self-hosting-backstage/)
  - Las actualizaciones son dolorosas — [OpsLevel](https://www.opslevel.com/resources/the-pain-of-backstage-upgrades)
  - Costos ocultos — [The New Stack](https://thenewstack.io/the-hidden-costs-of-free-internal-developer-portals/)

### Inferences
- El patrón a copiar es el de authentik, Pomerium y Entra: **el launcher es una consulta sobre la misma tabla de acceso que usa el ForwardAuth** (`initiative_access`), más un flag `visible_in_portal` independiente del acceso. Así se evita que el portal muestre algo que el borde deniega, o al revés.
- La limitación de Pomerium (solo filtra con un subconjunto de criterios) muestra el problema de filtrar el portal cuando la política depende del contexto del request. Con RBAC puro en DB no hay ese problema.
- Backstage es excesivo como launcher para usuarios de negocio: su modelo es de propiedad de software para desarrolladores. Podría complementar la vista técnica (repos, owners) si en el futuro hay un equipo de plataforma.
- Los dashboards estáticos solo aportan ideas de UX (grupos, iconos, widgets de estado) y el patrón de descubrimiento por anotaciones.
- Campos de metadata recomendados por app, a partir de authentik, Pomerium, Backstage y Homepage: slug, name, description, icon, group/categoría, launch URL, visible_in_portal, owner (solo para mostrar), tags y links.

### Gaps
- No se obtuvo documentación de Okta "My Apps/End-User Dashboard", Homarr, Heimdall ni Dashy (las dos últimas no tienen modelo multiusuario conocido; no verificado).
- No se confirmó en la documentación cómo la Web UI de Teleport filtra la lista de apps por `app_labels`.
- Pangolin: no se verificó el modelo de roles en detalle.

---

## B2. Patrones de registro de apps (UI admin vs GitOps vs API desde CI) y sincronización de estado desde Argo CD

### Takeaway
Hay tres patrones: UI/API administrativa (authentik, Entra, Teleport dinámico), declaración junto al despliegue (anotaciones en Ingress/IngressRoute como Homepage, `catalog-info.yaml` como Backstage, recursos dinámicos de Teleport) y notificaciones push desde Argo CD (webhook por trigger) para el estado y la versión. Lo pragmático es combinarlos: la DB es la fuente de verdad del acceso, y GitOps/Argo CD alimenta host, versión y estado.

### Cited Findings
- **Anotaciones en recursos de enrutamiento** (Homepage): `gethomepage.dev/*` sobre Ingress, IngressRoute (exige `href`) o HTTPRoute — [Homepage docs](https://gethomepage.dev/configs/kubernetes/)
- **Archivo en el repo** (Backstage): `catalog-info.yaml` más entidades `Location` que apuntan a otros archivos — [Backstage descriptor format](https://backstage.io/docs/features/software-catalog/descriptor-format/)
- **Registro dinámico vía API o recursos** (Teleport): crear apps como recursos con permisos `create`/`update` y labels — [Teleport dynamic registration](https://goteleport.com/docs/enroll-resources/application-access/configuration/dynamic-registration/)
- **UI admin** (authentik): Applications > New Application, junto con el provider — [authentik docs](https://docs.goauthentik.io/add-secure-apps/applications/manage_apps/)
- **Argo CD notifications, webhook**:
  - El servicio se declara en `argocd-notifications-cm` como `service.webhook.<name>` con `url`, `headers[{name,value}]`, `basicAuth`, `insecureSkipVerify` y opciones de retry (`retryMax`, `retryWaitMin`, `retryWaitMax`).
  - El template `template.<name>` admite `webhook.<name>.method` (GET/POST/PUT/PATCH), `path` y `body`, todos con plantillas.
  - La suscripción se hace con la anotación `notifications.argoproj.io/subscribe.<trigger>.<webhook>: ""` en la Application.
  - Variables disponibles: `app.metadata.name`, `app.spec.source.repoURL`, `app.status.operationState.phase`, `app.status.operationState.operation.sync.revision`, `app.status.sync.status` y `context.argocdUrl`.

  — [Argo CD webhook docs](https://argo-cd.readthedocs.io/en/stable/operator-manual/notifications/services/webhook/)
- Trigger típico: `on-sync-succeeded` con `app.status.operationState.phase in ['Succeeded']`. Para "desplegado y sano" se combina con `app.status.health.status == 'Healthy'` — [OneUptime (2026-02)](https://oneuptime.com/blog/post/2026-02-26-argocd-application-synced-events/view), [OneUptime cheat sheet](https://oneuptime.com/blog/post/2026-02-26-argocd-notification-templates-cheat-sheet/view), [ServiceNow docs](https://www.servicenow.com/docs/r/Recj91eIo1YnsOabQjgskg/AwB2hM2aIpX~41CfhdcfOw) (fuentes secundarias; `app.status.health.status` no aparece en la página oficial del webhook)

### Inferences
Flujo recomendado:
1. El admin crea la iniciativa y sus roles y accesos en la UI: datos de negocio, fuente de verdad en Postgres.
2. El despliegue de cada app (manifiestos en Git y sincronizados por Argo CD) lleva una anotación, p. ej. `platform.empresa.cl/initiative: <slug>`, en el IngressRoute o en la Application.
3. Argo CD notifica por webhook a platform-api (`POST /internal/deployments/sync`) con app, revision, sync y health. platform-api hace upsert de `deployments(host, version, status, last_sync_at)`.
4. Como respaldo frente a webhooks perdidos, se reconcilia periódicamente leyendo la API de Argo CD o los IngressRoute vía la API de Kubernetes. Patrón controller/reconciler: el webhook es un trigger y no la fuente de verdad.

Otras consideraciones:
- Un webhook no autenticado permitiría registrar hosts arbitrarios. Proteger el endpoint con un header secreto (`headers` en el servicio webhook) o con mTLS y NetworkPolicy, y validar que el host pertenezca a `*.apps.empresa.cl` y a una iniciativa existente.
- Una CRD propia (`Initiative`) con un operador sería la opción "más Kubernetes", pero duplica la fuente de verdad con Postgres. Para un equipo pequeño, anotación + webhook + reconciliación es suficiente.

### Gaps
- No se obtuvo una cita oficial de la API REST de Argo CD (`/api/v1/applications`) ni de los generadores de ApplicationSet; quedan como inferencia basada en conocimiento general, sin verificar para la versión vigente.
- No se encontraron implementaciones públicas que registren apps en un portal a partir de notificaciones de Argo CD como patrón documentado.
- No se encontraron fuentes nativas [JA] [ZH] [FR] sobre catálogos y portales de apps internas.
