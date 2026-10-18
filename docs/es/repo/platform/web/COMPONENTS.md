# Catálogo de componentes

> English version: [platform/web/COMPONENTS.md](../../../../../platform/web/COMPONENTS.md)

Fuente de verdad de los componentes de la UI del portal. Léelo antes de crear o cambiar un componente y actualízalo en el mismo cambio.

## Reglas

1. Capas
   - `app/components/base/` (prefijo `Base`): primitivas propias, sin UI de terceros. Layout, texto, botón, enlace, ícono.
   - `app/components/ui/` (prefijo `Ui`): widgets compuestos construidos sobre Reka UI (`reka-ui` fijado en 2.11.0, importado explícitamente, sin módulo de Nuxt). Hoy: Dialog y AlertDialog. NO uses Select, Combobox ni Splitter de Reka: renderizan o crean un elemento `<style>` (CSP). Un Select, si hace falta, es un `<select>` nativo en `base/`. Menu/Popover no están auditados.
   - `app/components/features/<domain>/`: componentes de dominio que conocen los tipos de la API (`app/types/api.ts`). Nuxt los nombra con el prefijo de la ruta: `features/home/AppCard.vue` es `<FeaturesHomeAppCard>`.
2. Solo tokens. El estilo viene de `app/assets/css/tokens.css` mediante utilidades semánticas (`bg-surface`, `text-text-muted`, `border-border`, `bg-primary`, `rounded-md`, `shadow-sm`, `p-4`). Sin valores crudos de paleta, sin valores arbitrarios (`bg-[#fff]`, `p-[13px]`). La única excepción permitida: un valor arbitrario que solo referencia tokens declarados mediante `var(--...)` (p. ej. las columnas de `BaseGrid` usando `--container-grid-*`); nunca depender de las variables del tema por defecto de Tailwind. Un adoptante cambia el tema sobrescribiendo ese único archivo (colores, radio, escala tipográfica, sombras; el tema oscuro reutiliza los mismos nombres).
3. Las páginas componen, no hacen markup. `app/pages/**` no puede usar elementos HTML crudos (div, span, p, h1-h6, a, button, input, select, textarea, table, ul, ol, li, img, form, label, section, header, footer, nav, main); `vue/no-restricted-html-elements` hace fallar el lint. Los layouts y componentes pueden usarlos.
4. Las clases de Tailwind deben ser strings literales. Las props se mapean a clases mediante tablas de búsqueda, nunca por concatenación de strings (`'gap-' + n` no lo detecta el scanner).
5. CSP (`script-src 'self' 'sha256-...'; style-src 'self'`):
   - sin `<script>`/`<style>` inline, sin `useHead` con scripts/estilos inline, sin atributos `style=` en el markup;
   - los bindings `:style` están permitidos (CSSOM) pero se prefieren las clases de tokens;
   - `v-html` está prohibido (error de `vue/no-v-html`);
   - sin CDN, web fonts ni orígenes externos; los íconos son SVG inline sin `<style>`;
   - todo componente de Reka UI debe verificarse contra `style-src 'self'` antes de adoptarlo (lee su código en `node_modules/reka-ui/dist/<Component>/`: busca `as: "style"`, `createElement("style")`, `setAttribute("style")`).
   `npm run generate && npm run check:csp` hace cumplir el lado HTML.
6. Accesibilidad: elementos semánticos, anillo de foco visible (token `outline-focus`), controles con etiqueta, los controles solo-ícono requieren `label`.
7. Los tipos de props compartidos viven en `app/types/ui.ts` (`Space` es la escala de espaciado: 0, 1, 2, 3, 4, 6, 8, 12 veces `--spacing`).

## Primitivas de layout

Todas aceptan `as` (nombre del tag, por defecto `div`) y un slot por defecto.

### BaseBox
- Propósito: contenedor genérico con padding, superficie, borde, radio y sombra opcionales.
- Props: `as?: string`, `padding?: Space = 0`, `surface?: 'none'|'base'|'raised' = 'none'`, `bordered?: boolean`, `rounded?: 'none'|'sm'|'md'|'lg' = 'none'`, `shadow?: 'none'|'sm'|'md' = 'none'`.
- Slots: default.
- Ejemplo: `<BaseBox as="section" surface="raised" bordered rounded="md" :padding="4">...</BaseBox>`
- A11y: usa `as` para landmarks (`header`, `main`, `nav`, `section`); un `section` necesita un heading.

### BaseStack
- Propósito: flujo vertical con gap uniforme.
- Props: `as?`, `gap?: Space = 4`, `align?: 'start'|'center'|'end'|'stretch'|'baseline' = 'stretch'`.
- Slots: default.
- Ejemplo: `<BaseStack :gap="6"><BaseHeading :level="1">Title</BaseHeading><BaseText>Body</BaseText></BaseStack>`
- A11y: usa `as="ul"` solo con hijos que sean items de lista (un `BaseBox as="li"`).

### BaseCluster
- Propósito: fila horizontal con wrap (acciones, tags, barras de herramientas).
- Props: `as?`, `gap?: Space = 2`, `align? = 'center'`, `justify?: 'start'|'center'|'end'|'between' = 'start'`, `wrap?: boolean = true`.
- Slots: default.
- Ejemplo: `<BaseCluster justify="between"><BaseText>Left</BaseText><BaseButton>Right</BaseButton></BaseCluster>`
- A11y: el orden visual es igual al orden del DOM.

### BaseGrid
- Propósito: grid responsivo auto-fill; las columnas colapsan por ancho mínimo.
- Props: `as?`, `gap?: Space = 4`, `minColumn?: 'sm'|'md'|'lg' = 'md'` (16rem / 18rem / 20rem mediante los tokens `--container-grid-sm|md|lg`).
- Slots: default.
- Ejemplo: `<BaseGrid min-column="md"><BaseBox>...</BaseBox><BaseBox>...</BaseBox></BaseGrid>`
- A11y: el orden del DOM es el orden de lectura.

### BaseContainer
- Propósito: wrapper centrado de ancho máximo con gutters laterales (`--spacing-gutter`).
- Props: `as?`, `size?: 'page'|'reading' = 'page'` (`--container-page` 72rem, `--container-reading` 44rem).
- Slots: default.
- Ejemplo: `<BaseContainer size="reading">...</BaseContainer>`

## Texto

### BaseText
- Propósito: texto de cuerpo con tono, tamaño y peso.
- Props: `as?: string = 'p'`, `size?: 'xs'|'sm'|'base'|'lg'|'xl'|'2xl'|'3xl'|'4xl' = 'base'`, `tone?: 'default'|'muted'|'primary'|'danger'|'warning'|'success' = 'default'`, `weight?: 'normal'|'medium'|'semibold'|'bold' = 'normal'`.
- Slots: default.
- Ejemplo: `<BaseText tone="danger" size="sm">Something failed.</BaseText>`
- A11y: no dependas solo del tono para comunicar un estado; agrega palabras o un ícono con label.

### BaseHeading
- Propósito: heading semántico con el tamaño visual desacoplado del nivel.
- Props: `level: 1|2|3|4|5|6` (obligatoria, elige `h1`..`h6`), `size?: TextSize` (por defecto según el nivel: 3xl, 2xl, xl, lg, base, sm).
- Slots: default.
- Ejemplo: `<BaseHeading :level="2" size="lg">Section</BaseHeading>`
- A11y: no saltes niveles; un `h1` por página.

## Controles

### BaseButton
- Propósito: acción o navegación con estilo de botón.
- Props: `to?: string` (ruta interna, NuxtLink), `href?: string` (`<a>` simple; debe ser una URL de confianza, nunca controlada por el usuario: no se sanitiza. Los href construidos a partir de datos pasan por un helper validador como `hostUrl()`, ver `FeaturesHomeAppCard`), `target?: '_self'|'_blank'` (`_blank` agrega `rel="noopener noreferrer"`), `type?: 'button'|'submit'|'reset' = 'button'`, `variant?: 'primary'|'secondary'|'ghost'|'danger' = 'primary'`, `size?: 'sm'|'md'|'lg' = 'md'`, `disabled?: boolean`, `loading?: boolean`.
- Slots: default (texto, opcionalmente con un `BaseIcon`).
- Eventos: `click` (emit declarado, por lo que un `@click` del padre nunca llega directamente al elemento raíz); se emite solo mientras está activo, nunca con `disabled` o `loading`.
- Ejemplo: `<BaseButton variant="danger" type="submit" :loading="saving">Delete</BaseButton>`
- A11y: renderiza `<button type="button">` por defecto (nunca un submit accidental). `loading` muestra un ícono `loader` giratorio decorativo (`motion-safe:animate-spin`), establece `aria-busy` y bloquea la activación (el `<button>` conserva el foco nativo y usa `aria-disabled` más un handler de click que previene el default, por lo que no enviará formularios); los estilos de hover aplican solo mientras es accionable; la prop `disabled` usa el atributo nativo en los botones. Los enlaces disabled/loading se renderizan sin `href` más `aria-disabled`. Los botones solo-ícono necesitan un `aria-label` en el botón.

### BaseLink
- Propósito: enlace de texto en línea.
- Props: `to?: string` (interno, NuxtLink), `href?: string` (externo, `<a>`; debe ser una URL de confianza, nunca controlada por el usuario: no se sanitiza. Los href construidos a partir de datos pasan por un helper validador como `hostUrl()`, ver `FeaturesHomeAppCard`), `target?: '_self'|'_blank'` (`_blank` agrega `rel="noopener noreferrer"`).
- Slots: default.
- Ejemplo: `<BaseLink href="/docs/" target="_blank">Docs</BaseLink>`
- A11y: el texto del enlace debe tener sentido fuera de contexto; anuncia en el texto el comportamiento de nueva pestaña cuando importe.

### BaseIcon
- Propósito: SVG en línea de un conjunto curado (`check`, `loader`, `x`, `plus`, `minus`, `chevron-down`, `chevron-right`, `external-link`, `alert-triangle`, `info`, `search`). Agrega un nombre en `app/types/ui.ts` y en la tabla de paths del componente.
- Props: `name: IconName` (obligatoria), `label?: string`, `size?: 'sm'|'md'|'lg' = 'md'`. El color sigue a `currentColor`.
- Slots: ninguno.
- Ejemplo: `<BaseIcon name="alert-triangle" label="Warning" />` (con significado) o `<BaseIcon name="check" />` (decorativo).
- A11y: sin `label` el ícono es `aria-hidden`; con `label` es `role="img"` y `aria-label`. Un ícono que es el único contenido de un control debe llevar el label. En dev (`import.meta.dev`) avisa ante un `name` desconocido, un `label` vacío y un atributo `role="img"`/`aria-label` sin la prop `label`.

## Formularios y datos

### BaseField
- Propósito: label + control + hint + error, conectados para tecnologías de asistencia.
- Props: `label: string` (obligatoria), `hint?: string`, `error?: string`, `required?: boolean` (agrega un `*` visual, `aria-required` en el control).
- Slots: default, con slot props `id`, `aria-describedby` (ids de hint/error, solo los renderizados), `aria-invalid`, `aria-required`. Enlázalos: `<BaseField label="Key" :error="e" v-slot="field"><BaseInput v-bind="field" v-model="key" /></BaseField>`.
- A11y: el `label` es un `<label for>` real; el error es texto bajo el control (no solo color). Mueve el foco al primer control inválido al enviar (`BaseInput.focus()`).

### BaseInput
- Propósito: input de texto de una línea.
- Props: `modelValue` (`v-model`, string), `type?: 'text'|'email'|'search'|'datetime-local' = 'text'` (`datetime-local`: valor de hora local `YYYY-MM-DDTHH:mm`; `localInputToRfc3339()` en `app/utils/datetime.ts` agrega el offset del usuario), `readonly?`, `disabled?`. Otros atributos (`id`, `aria-*`, `autocomplete`...) pasan al `<input>`.
- Expone `focus()`. El aspecto inválido proviene de `aria-invalid`.
- A11y: nunca lo uses sin un `BaseField` (u otro label). Anillo de foco mediante el token `focus`.

### BaseSelect
- Propósito: `<select>` nativo (el Select de Reka inyecta `<style>`, prohibido por la CSP).
- Props: `modelValue` (`v-model`, string), `options: SelectOption[]` (`{ value, label, disabled? }`, en `app/types/ui.ts`), `placeholder?: string` (agrega una primera opción deshabilitada con valor vacío), `disabled?`. Otros atributos (`id`, `aria-*`) pasan al `<select>`.
- Expone `focus()`. El aspecto inválido proviene de `aria-invalid`.
- Ejemplo: `<BaseField label="Role" v-slot="field"><BaseSelect v-bind="field" v-model="roleId" :options="roleOptions" placeholder="Choose a role" /></BaseField>`
- A11y: nunca lo uses sin un `BaseField` (u otro label); el navegador provee el teclado y el selector de la plataforma.

### BaseCheckbox
- Propósito: checkbox nativo con su label visible (slot por defecto) y hint opcional.
- Props: `modelValue` (`v-model`, boolean), `hint?: string` (vinculado con `aria-describedby`). Otros atributos (`disabled`, `name`) pasan al `<input>`.
- Ejemplo: `<BaseCheckbox v-model="managesAccess" hint="Holders can grant access.">Manages access</BaseCheckbox>`
- A11y: el label es un `<label for>` real; lleva su propio texto, por lo que no va dentro de un `BaseField`.

### BaseTable
- Propósito: tabla de datos semántica.
- Props: `caption: string` (obligatoria, nombra la tabla), `captionHidden?: boolean` (caption oculto visualmente), `columns: TableColumn[]` (`{ key, label, align?: 'start'|'end', hideLabel? }`, en `app/types/ui.ts`), `rows: T[]`, `rowKey: (row) => string`, `rowHeader?: string` (clave de columna renderizada como `<th scope="row">`), `emptyText?: string = 'Nothing to show.'`.
- Slots: `cell` con `{ row, column }`; la celda por defecto renderiza `row[column.key]`.
- Ejemplo: `<BaseTable caption="Initiatives" :columns="cols" :rows="rows" :row-key="r => r.id"><template #cell="{ row, column }">...</template></BaseTable>`
- A11y: `<caption>`, `<th scope>`; el estado vacío es una fila real. La tabla está en un `role="region"` con scroll horizontal (enfocable, etiquetado por el caption), por lo que una tabla ancha hace scroll dentro de sí misma, no de la página.

## UI (Reka UI)

Comportamiento de estilos de lo que se usa (reka-ui 2.11.0, según su código; la verificación en navegador le corresponde al orquestador): Dialog/AlertDialog establecen `overflow`, `padding-right`, `margin-right` y `pointer-events` en `document.body`, `--scrollbar-width` en `<html>`, enlazan `pointer-events` en un `:style` en el contenido y el overlay, y Presence puede establecer `animation-fill-mode`; todo mediante CSSOM (permitido por `style-src 'self'`). Sin elemento `<style>`, sin `style=` en el markup, sin spans focus-guard (esos son de Select/Menu/Popover/Combobox). Ambos se portalean a `<body>`.

### UiDialog
- Propósito: diálogo modal (Reka `Dialog`): overlay, título, descripción, botón de cerrar, slot de cuerpo.
- Props: `open` (`v-model:open`, obligatoria), `title: string`, `description: string` (ambas obligatorias: nombran el diálogo), `busy?: boolean` (bloquea Escape, el click fuera y el botón de cerrar mientras corre una solicitud).
- También props: `initialFocus?: { focus: () => void }` (p. ej. un template ref de `BaseInput`; recibe el foco al abrir mediante `@open-auto-focus.prevent`; foco por defecto de Reka si se omite), `persistent?: boolean` (Escape y los clicks fuera no lo cierran, p. ej. mientras se muestra un secreto de un solo uso; la X y los botones dentro del diálogo sí).
- Slots: default (cuerpo, normalmente un formulario).
- A11y: Reka atrapa el foco; el foco vuelve al elemento que lo abrió mediante `useFocusReturn` (`app/composables/useFocusReturn.ts`: `document.activeElement`, si no el último botón/enlace clickeado, por lo que funciona en Safari donde un click no enfoca el botón; recurre a la restauración de Reka si el opener ya no existe). Cierra con Escape, marca el resto como `aria-hidden`.

### UiConfirmDialog
- Propósito: confirmación de una acción destructiva o difícil de deshacer (Reka `AlertDialog`, `role="alertdialog"`): sin cierre por click fuera, "Cancel" recibe el foco primero; el foco vuelve al opener mediante `useFocusReturn` como `UiDialog`.
- Props: `open` (`v-model:open`), `title`, `description`, `confirmLabel` (obligatoria), `cancelLabel?: string = 'Cancel'`, `variant?: 'primary'|'danger' = 'danger'`, `busy?: boolean`, `error?: string` (mensaje fijo del último intento fallido, `role="alert"`).
- Eventos: `confirm`. No se cierra solo: el padre ejecuta la solicitud, muestra `error` y pone `open` en false si tiene éxito.
- Slots: default (contenido extra sobre los botones).

## Página de error

### app/error.vue
- Propósito: la página de error de toda la app (404 del lado del cliente y errores de runtime). Necesaria bajo la CSP estricta: la página de error por defecto de Nuxt inyecta un `<style>` y un `<script>` inline, ambos bloqueados, dejando la página sin estilo.
- Props: `error: NuxtError` (provista por Nuxt).
- Composición: `NuxtLayout` + `BaseStack`, `BaseText`, `BaseHeading`, `BaseCluster`, `BaseButton` únicamente. Título mediante `useHead({ title })`.
- Copy: 404 muestra "Page not found"; cualquier otro status muestra "Something went wrong". Solo se muestran el código de estado y el copy fijo; `error.message` se renderiza solo en dev (`import.meta.dev`), nunca el stack.
- Acción: "Go to home" llama a `clearError({ redirect: '/' })`.
- Nota: `NUXT_E1005` ("Error caught during app initialization") en la consola al visitar directamente una ruta desconocida es el diagnóstico `app:error` propio de Nuxt para el 404 levantado durante el arranque; es esperado y no es un problema de CSP.

## Superficies y feedback

### BaseCard
- Propósito: superficie elevada, con borde y redondeada con una sombra pequeña que agrupa contenido (un preset de `BaseBox`).
- Props: `as?: string = 'div'`, `padding?: Space = 4`.
- Slots: default.
- Ejemplo: `<BaseCard as="li"><BaseText>...</BaseText></BaseCard>`
- A11y: usa `as="li"` solo dentro de una lista; dale un heading a un conjunto de cards.

### BaseBadge
- Propósito: etiqueta pequeña para un rol o estado.
- Props: `tone?: 'neutral'|'primary'|'success'|'warning'|'danger' = 'neutral'`.
- Slots: default (texto).
- Ejemplo: `<BaseBadge tone="primary">editor</BaseBadge>`
- A11y: el tono es decoración; el texto debe llevar el significado. Los colores de texto/borde usan los tokens semánticos.

### BaseSkeleton
- Propósito: placeholder de carga decorativo (`motion-safe:animate-pulse`).
- Props: `height?: 'sm'|'md'|'lg' = 'md'`, `width?: 'half'|'full' = 'full'`.
- Slots: ninguno.
- Ejemplo: `<BaseSkeleton height="lg" width="half" />`
- A11y: `aria-hidden`. La región de carga debe indicar que está ocupada: `aria-busy="true"` más texto oculto visualmente (`class="sr-only"` en un `BaseText`), ver `FeaturesHomeAppSkeleton`.

### BaseEmptyState
- Propósito: bloque "no hay nada aquí" o "no se pudo cargar" con una acción opcional.
- Props: `title: string`, `description?: string`, `icon?: IconName = 'info'`, `level?: HeadingLevel = 2`.
- Slots: default (acciones, p. ej. un `BaseButton`).
- Ejemplo: `<BaseEmptyState title="No apps yet" description="..."><BaseButton>Retry</BaseButton></BaseEmptyState>`
- A11y: renderiza un `section` con un heading; elige `level` para que los headings no salten niveles. El ícono es decorativo.

### BasePageHeader
- Propósito: bloque de título de página: `h1`, descripción opcional y un slot de acciones.
- Props: `title: string`, `description?: string`. El `h1` tiene `tabindex="-1"`; el componente expone `focusTitle()` para mover el foco allí (template ref).
- Slots: default (acciones de la página, alineadas a la derecha, con wrap en pantallas chicas).
- Ejemplo: `<BasePageHeader title="Welcome" description="..."><BaseButton to="/admin">Administration</BaseButton></BasePageHeader>`
- A11y: posee el único `h1` de la página; se renderiza en un elemento `header`.

## Capa de API

- `app/types/api.ts`: tipos de payload que reflejan `platform/api/internal/portalhttp` (`MeResponse`, `HomeResponse`, `HomeApp`, `InitiativeRef`, `Initiative`, `InitiativeList`, `CreateInitiativeBody`, `PatchInitiativeBody`, `Role`, `RoleList`, `CreateRoleBody`, `PatchRoleBody`, `Grant`, `GrantRole`, `GrantUser`, `GrantGroup`, `GrantList`, `CreateGrantBody`, `Group`, `GroupList`, `ApiToken`, `TokenList`, `CreatedToken`, `CreateTokenBody`, `TokenScope`, `Robot`, `RobotRef`, `RobotList`, `RobotRefList`, `CreateRobotBody`, `PatchRobotBody`, `CreateGroupBody`, `Area`, `AreaList`, `AreaPersonKind`, `AreaPersonList`, `SupervisedArea`, `GroupMember`, `GroupMemberList`, `AddMemberBody`, `AdminUser`, `AdminUserList`, `PatchUserBody`, `RevokeSessionsResult`, `AuditEvent`, `AuditList`, `UserStatus`, `App`, `AppStatus`, `AppList`, `Deployment`, `CreateAppBody`, `PatchAppBody`, `CreateDeploymentBody`, `ProbeStatus`, `ReceivedProbeStatus`, `HealthProblemCode`, `HealthProblem`, `AppHealth`, `OrphanHost`, `OrphanHostList`) y el `Problem` de RFC 9457.
- `app/composables/useApi.ts`: `apiGet<T>(path)` (`$fetch` same-origin bajo `/api/v1`, sin tokens ni headers CSRF, sin reintentos), `ApiError` (`status`, `kind`, `problem`, `reloading`) y `useApiData<T>(key, path)` (`useAsyncData` del lado del cliente, devuelve `data`, `error`, `pending`, `refresh`, `refreshQuiet()` (re-fetch que conserva el estado anterior si falla, resuelve a un boolean), `set(value)` (reemplaza el valor en caché, p. ej. con la respuesta de una mutación); las fallas vuelven como `error`, nunca se lanzan). Tipos de error: `unauthorized` (401), `forbidden` (403), `not_found` (404), `conflict` (409), `invalid` (400/415/422), `unavailable` (503), `server` (otros 5xx), `network`, `unknown`. Los cuerpos de respuesta nunca se registran y `ApiError.message` es texto fijo.
- Mutaciones: `apiSend(method, path, body?)` / `useApi().post|patch|del` (`POST`/`PATCH`/`DELETE`, `Content-Type: application/json` siempre enviado, DELETE sin cuerpo incluido, same-origin, sin headers CSRF: CrossOriginProtection más el content type JSON son las protecciones; sin reintentos). `ApiError.retryAfter` guarda un `Retry-After` numérico (503), limitado a 120 s. 413 se mapea al tipo `invalid`.
- `app/utils/mutationError.ts`: `describeMutationError(error, { conflict, notFound?, fields?, fieldCopy? })` -> `{ message, fields, retryable }`, solo copy fijo en inglés (el `detail` del servidor nunca se muestra). 422 `errors[]` -> por campo declarado mediante `CODE_COPY` (`required`, `invalid`, `must_be_in_the_future`, `no_unique_active_user`, `unknown`, `exactly_one_required`, `reserved`, `at_least_one_required`, `not_allowed`, `too_large`; los códigos desconocidos recurren a `invalid`; gana el primer error por campo; los errores en campos que el formulario no renderiza se pliegan en "Check the values and try again."), 400/413/415 -> "The request was not accepted. Reload the page and try again.", 409 -> el copy `conflict` de cada acción, 403 -> "You do not have permission to do this.", 404 -> "This item no longer exists.", 503 -> "The portal is busy. Try again in N seconds." (reintentable), network/5xx reintentables, 401 -> política de recarga más abajo.
- `app/composables/useAdminAccess.ts`: `/me` (clave `me` compartida) como `isPlatformAdmin`, `isManager`, `canAdminister`, `managesInitiative(id)` (admin de la plataforma, o `managed_initiatives` incluye el id), `supervisedAreas` / `isSupervisor` (`permissions.supervised_areas` de `/me`, `[]` bajo tokens), `revalidate()` (relectura silenciosa en segundo plano; el layout admin la llama al montar). La UI solo oculta; la API hace cumplir.
- `app/composables/useGroupSearch.ts`: `useGroupSearch(delay = 300)` -> `input` (enlazar a la caja), `query` (con debounce, recortada, limitada a `GROUP_SEARCH_MAX` = 64), `path()` (`/groups` o `/groups?q=...`, mediante `groupsPath()` en `app/utils/group.ts`; pásalo como getter de `usePagedList` y llama `reload()` cuando `query` cambie).
- `app/composables/usePagedList.ts`: `usePagedList<Page, Item>(path, select, { immediate? })` (`path` puede ser un getter, p. ej. que lee un filtro, y puede llevar un query string) lista keyset (cursor opaco devuelto tal cual; `immediate: false` omite la carga al montar, el llamador ejecuta `reload()`): `items`, `loading`, `loadingMore`, `error`, `hasMore`, `reload()`, `loadMore()`; una solicitud más nueva gana sobre una más vieja.
- `app/utils/initiative.ts`: validación de key/nombre que refleja Go (key `^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`, <= 63; nombre <= 200 bytes), `initiativeLabel()` (nombre, si no key), `PLATFORM_INITIATIVE_KEY`.
- `app/composables/useInitiativeContext.ts`: `provideInitiativeContext()` / `useInitiativeContext()` -> `{ initiative, set }`; la ruta padre `/admin/initiatives/[id]` provee la iniciativa cargada a sus páginas anidadas.
- `app/utils/role.ts`: validación de la key del rol (<= 32, mismo patrón que las keys de iniciativa), `ROLE_FIELD_COPY`, `LAST_MANAGER_COPY` (copy fijo del 409 al revocar un grant de manager o desactivar `manages_access`).
- `app/utils/datetime.ts`: `formatTimestamp()` (locale), `isPast()`, `localInputToRfc3339()` (un valor `datetime-local` a RFC 3339 con el offset del usuario).
- Política de 401: la sesión se perdió, por lo que la página se recarga una vez (ForwardAuth responde a la navegación con un 302 al login). `login_url` no se sigue. Protección contra bucles: como máximo una recarga cada 30 s (`sessionStorage`), más un flag de módulo para que todo 401 posterior al primero en la misma carga de página también reporte `reloading` (sin flash del mensaje de recarga); más allá de eso `ApiError.reloading` es false y la UI le pide al usuario recargar.
- `app/utils/app.ts`: reglas de app/host y copy fijo. `validateAppName()` (<= 200 bytes, sin NUL), `appLabel()` (nombre, si no id), `normalizeHost()` (trim + minúsculas solo ASCII: `hostname.Valid` de Go rechaza mayúsculas, y `toLowerCase()` mapearía el símbolo Kelvin a `k`), `validateHost()` (mediante `isHostname()`), `isAmbiguousFailure()` (network, 503, otros 5xx o desconocido tras enviar), `APP_FIELD_COPY`, `HOST_TAKEN_COPY`, `APP_CREATE_AMBIGUOUS_COPY`, `PORTAL_APP_DISABLE_COPY`, `PORTAL_APP_LAST_HOST_COPY`.
- `app/utils/group.ts`: validación de key/nombre de grupo que refleja Go (key como las iniciativas, <= 63; nombre <= 200 bytes), `GROUP_FIELD_COPY`, `groupLabel()`.
- `app/utils/email.ts`: `validateEmail()` y `EMAIL_FIELD_COPY` (422 `email.*`, incl. `no_unique_active_user`), compartidos por agregar miembro y búsqueda de usuario.
- `app/utils/user.ts`: `SELF_DISABLE_COPY` ("You cannot disable your own account.") y `LAST_ADMIN_COPY` ("This is the last active platform admin."), el copy del 409 de `PATCH /users/{id}`.
- `app/utils/datetime.ts` también tiene `formatRelative()` ("3 hours ago", granularidad de día/minuto, "just now" bajo un minuto).
- `app/utils/health.ts`: `PROBLEM_COPY` / `problemCopy(code)` (label fijo, explicación y tono por código de hallazgo de health, los códigos desconocidos recurren a `UNKNOWN_PROBLEM_COPY`) y `probeStatusCopy(status)` (label/tono del badge de un resultado de probe guardado; `null` = "Not probed", valor desconocido = "Unknown result"; `HealthProblem.code` y `Deployment.last_probe_status` aceptan strings desconocidos mediante `(string & {})`; cada oración nombra quién puede actuar).
- `app/utils/token.ts`: reglas y copy de tokens: `TOKEN_NAME_MAX` (64), `TOKEN_LIMIT` (25), `EXPIRY_OPTIONS` (1, 7, 30 días, el más largo permitido) / `ROBOT_EXPIRY_OPTIONS` (agrega 90 días) / `EXPIRY_DEFAULT` (7 días) / `EXPIRY_MAXIMUM` (omite `expires_in`, el servidor aplica su tope) / `TOKEN_UNCERTAIN_COPY`, `SCOPE_OPTIONS`, `TOKEN_FIELD_COPY` (422 `name`, `scope` incl. `not_allowed`, `expires_in` incl. `too_large`), `TOKEN_LIMIT_COPY` (el 409), `tokenState()` (active / expired / revoked), `validateTokenName()`.
- `app/utils/robot.ts`: `validateRobotName()`, `ROBOT_FIELD_COPY`, `ROBOT_NAME_TAKEN_COPY`, `robotOwnerLabel()` (email del dueño, nombre como respaldo; reutiliza las reglas de nombre de token). `app/utils/user.ts` también tiene `subjectLabel()` (email, si no nombre: un robot no tiene email).
- `app/utils/hostname.ts`: `isHostname()` (refleja `hostname.Valid` de Go: labels DNS en minúsculas, <= 253 caracteres, sin punto final, la última label no es solo dígitos) y `hostUrl()` (`https://<host>/` o null).

## Features

### features/home
- `FeaturesHomeAppCard` (`AppCard.vue`): props `app: HomeApp`. Un `BaseCard as="li"` con nombre, iniciativa, enlaces de host (validados con `hostUrl()`, nueva pestaña, "(opens in a new tab)" oculto visualmente) y badges de rol. El ícono de enlace externo está dentro del `<a>`. Cuando ningún host pasa la validación muestra una nota atenuada "No address available".
- `FeaturesHomeAppSkeleton` (`AppSkeleton.vue`): grid de tres skeletons de card. No lleva una región live propia: la página envuelve los resultados en un contenedor `aria-live="polite"` persistente con `aria-busy` mientras carga.
- `FeaturesHomeLoadError` (`LoadError.vue`): props `error: ApiError`, `retrying?: boolean` ("Try again" muestra `BaseButton loading`), emite `retry`. El contenedor tiene `role="alert"`. Copy fijo por tipo de error, nunca el detalle del servidor.
- `app/pages/index.vue`: saludo desde `/me` (nombre, si no email), botón "Administration" hacia `/admin` cuando `permissions.platform_admin` o `managed_initiatives` no está vacío (la UI solo oculta), carga, error, "No apps yet" y el grid. El estado de error depende solo de `/me/apps`; si `/me` falla mientras las apps cargan, en su lugar se muestra un aviso atenuado "Some details could not be loaded." con un reintento solo de `/me`. Tras un reintento exitoso el foco se mueve al h1 de la página. El botón Administration navega del lado del cliente a `/admin`, que redirige a `/admin/initiatives`.

### features/admin
- Layout `app/layouts/admin.vue`: header con enlace a home y `FeaturesAdminNav`; resuelve `/me` y controla la página: skeleton mientras carga, `FeaturesHomeLoadError` si falla, `FeaturesAdminNoAccess` para un usuario que no es admin de la plataforma ni manager, y en otro caso el slot (así las páginas admin nunca disparan llamadas para nadie más). Las páginas se suscriben con `definePageMeta({ layout: 'admin' })`.
- `FeaturesAdminNav` (`Nav.vue`): props `isPlatformAdmin`, `isManager`. `<nav aria-label="Administration">`, `aria-current` en la sección actual. Initiatives (admin y managers), Groups, Areas y Users (solo admin de la plataforma), "My areas" (prop `isSupervisor`, enlaza a `/areas`, fuera del layout admin), Audit (admin y managers).
- `FeaturesAdminCreateKeyDialog` (`CreateKeyDialog.vue`): props `endpoint`, `title`, `description`, `submitLabel`, `conflict`; `v-model:open`, emite `created`. Compartido por grupos y áreas: key/nombre validados como Go, POST a `endpoint`, 409 = `conflict` en el campo key.
- `FeaturesAdminAddPersonDialog` (`AddPersonDialog.vue`): props `endpoint`, `title`, `description`, `submitLabel`, `conflict`, `notFound`; `v-model:open`, emite `added` y `not-found` (404). Compartido por miembros de grupo y miembros/supervisores de área: 422 `email` en el campo, 409 = `conflict`.
- `FeaturesAdminPagedBlock` (`PagedBlock.vue`): props `list` (un resultado de `usePagedList`), `subject`, `forbiddenDescription`; slot por defecto = la tabla. Skeletons de carga ("Loading <subject>"), error de carga, "Could not load more <subject>.", "Load more".
- `useRemoveFlow` (composable): estado de eliminar-tras-confirmación (`removing`, `open`, `busy`, `error`, `notice`, `ask`, `confirm`); 404 = aviso `gone` + recarga, otras fallas en el diálogo (409 = `conflict`).
- `FeaturesAdminNoAccess` (`NoAccess.vue`): estado fijo "You do not have access to administration" con un enlace a home.
- `app/pages/admin/index.vue`: redirección del lado del cliente (route middleware, sin flash) a `/admin/initiatives`.

### features/initiatives
- `FeaturesInitiativesTable` (`Table.vue`): props `initiatives: Initiative[]`, `caption`. Columnas nombre (key cuando está vacío, enlace al detalle), key, estado.
- `FeaturesInitiativesStatusBadge` (`StatusBadge.vue`): prop `status`.
- `FeaturesInitiativesCreateDialog` (`CreateDialog.vue`): `v-model:open`, emite `created(initiative)` (la página de lista navega a `/admin/initiatives/<id>`). El input de key recibe el foco al abrir. Validación del cliente como Go; 409 muestra "An initiative with this key already exists. If you just retried, it may have been created by your earlier attempt: check the list." en el campo key.
- `FeaturesInitiativesNameForm` (`NameForm.vue`): props `initiative`, `editable`; la key siempre es de solo lectura; PATCH del nombre; emite `saved(initiative)` (la respuesta del PATCH; la página la establece como su estado, sin re-fetch).
- `FeaturesInitiativesStatusPanel` (`StatusPanel.vue`): props `initiative`, `canChange`; un botón (activar/desactivar) tras `UiConfirmDialog`, oculto para la iniciativa `platform` (el 409 igual se maneja); emite `changed(initiative)` (la respuesta del PATCH).
- Páginas: `/admin/initiatives` (lista keyset con "Load more", "New initiative" para admins de la plataforma) y la anidada `/admin/initiatives/[id]` (ruta padre `[id].vue`: carga la iniciativa una vez, h1, `FeaturesInitiativesSubNav`, `<NuxtPage />`; 404 -> "Initiative not found") con hijas `index` (resumen + estado; los managers lo ven de solo lectura), `grants` y `roles`. Cada hija es una página delgada sobre un panel.
- `FeaturesInitiativesSubNav` (`SubNav.vue`): prop `initiativeId`. `<nav aria-label="Initiative">` con enlaces simples Overview / Grants / Roles / Apps (no un widget de tabs), `aria-current="page"` en el actual.

### features/roles
- `FeaturesRolesPanel` (`Panel.vue`): props `initiativeId`, `canEdit` (admin de la plataforma). Lista keyset; con `canEdit`: "New role", alternar `manages_access` (ON pasa directo, OFF pide confirmación; 409 = `LAST_MANAGER_COPY`) y eliminar tras `UiConfirmDialog` (409 = "Remove this role's grants first."). Los managers lo ven de solo lectura. La API no devuelve conteo de grants por rol, por lo que no se muestra ninguno.
- `FeaturesRolesTable` (`Table.vue`): props `roles`, `canEdit`, `caption`, `busyId?`; emite `toggle(role)`, `remove(role)`.
- `FeaturesRolesCreateDialog` (`CreateDialog.vue`): props `initiativeId`, `v-model:open`, emite `created(role)`. Key validada como Go (<= 32), `BaseCheckbox` para `manages_access`; 409 = key en uso.

### features/grants
- `FeaturesGrantsPanel` (`Panel.vue`): props `initiativeId`, `canEdit` (admin de la plataforma o manager de la iniciativa). Lista keyset, "Grant access", revocar tras `UiConfirmDialog` (409 = `LAST_MANAGER_COPY`). Los grants eliminados salen de la lista localmente; una creación la recarga.
- `FeaturesGrantsTable` (`Table.vue`): props `grants`, `canRevoke`, `caption`; emite `revoke(grant)`. Sujeto (nombre/email de usuario o grupo), rol (+ badge "Manages access"), expiración con un badge "Expired" (la API lista los grants expirados), fecha de creación.
- `FeaturesGrantsCreateDialog` (`CreateDialog.vue`): props `initiativeId`, `v-model:open`, emite `created(grant)`. Select de tipo de sujeto (usuario por email / grupo), select de rol (todas las páginas de los roles de esta iniciativa), expiración `datetime-local` opcional validada a futuro, 422 por campo (`email`, `group_id`, `role_id`, `expires_at`), 409 = el sujeto ya tiene el rol.
- `FeaturesGrantsGroupSelect` (`GroupSelect.vue`): `v-model` id de grupo; un `BaseInput` "Search groups" (debounce de 300 ms mediante `useGroupSearch`, recortado, máx. 64) sobre un select nativo alimentado por `GET /groups?q=`, con "Load more groups" para la consulta actual (el mismo `q` en cada página). La región live polite anuncia "N groups found" / "No groups found" tras cada (re)carga. El grupo elegido se recuerda y permanece en el select aunque una búsqueda posterior no lo devuelva. Hace fetch al montar, así que móntalo solo cuando haga falta.
- `FeaturesHomeLoadError` ahora acepta `subject` opcional (título: "Could not load {subject}", por defecto "your apps") y `forbiddenDescription`.

### features/apps
- `FeaturesAppsPanel` (`Panel.vue`): props `initiativeId`, `canEdit` (admin de la plataforma). Lista keyset de apps con sus hosts; los managers la tienen de solo lectura. Con `canEdit`: "New app", renombrar, deshabilitar (tras `UiConfirmDialog`) / habilitar (directo), agregar host, quitar host (tras `UiConfirmDialog`). `App.portal_app` marca la app propia del portal: su botón Disable y el botón de quitar de su último host no se renderizan (la API respondería 409); los 409 igual se manejan con "The portal app cannot be disabled." / "The portal app needs at least one host.". Crear una app no es idempotente: sin reintento ante una falla ambigua; la lista se recarga y "The app may have been created. The list was reloaded; if it is missing, reload again in a moment before trying again." permanece visible junto a un botón "Reload list". Quitar el host en uso (`isCurrentHost`: coincide con `location.host` o `location.hostname`, en minúsculas) muestra una confirmación de peligro más fuerte. Un 404 (quitar host, alternar) o un 409 al habilitar cierra el diálogo, recarga la lista silenciosamente (`reload(true)`) y muestra un aviso fijo. Una app creada se agrega al final y recibe el foco cuando la lista no tiene más páginas; en otro caso el aviso apunta a "Load more".
- `FeaturesAppsTable` (`Table.vue`): props `apps`, `canEdit`, `caption`, `busyId?`; emite `rename(app)`, `toggle(app)`, `addHost(app)`, `removeHost(app, deployment)`. Los hosts son enlaces externos validados con `hostUrl()` (texto plano cuando la verificación falla); la celda de nombre es un destino de foco `#app-<id>`.
- `FeaturesAppsStatusBadge` (`StatusBadge.vue`): prop `status`.
- `FeaturesAppsCreateDialog` (`CreateDialog.vue`): props `initiativeId`, `v-model:open`, emite `created(app)` y `uncertain`. Envío bloqueado mientras está pendiente; nombre opcional.
- `FeaturesAppsRenameDialog` (`RenameDialog.vue`): props `app`, `v-model:open`, emite `saved(app)` (la respuesta del PATCH).
- `FeaturesAppsAddHostDialog` (`AddHostDialog.vue`): props `app`, `v-model:open`, emite `added(deployment)`. El host se recorta y se pasa a minúsculas al enviar, luego se valida como Go; una vista previa live polite "Will be added as <normalized>" aparece cuando difiere del valor tipeado; 422 `reserved` = "This host is reserved for the sign-in service."; 409 = "This host is already used by another app. If you just retried, ..." (agregar es idempotente por host, por lo que el 409 puede ser nuestro propio intento anterior).
- Página `/admin/initiatives/[id]/apps` (`apps/index.vue`; el admin de la plataforma edita, los managers leen). La tabla muestra un `FeaturesHealthProbeBadge` por host y un enlace "Health" por app a `/admin/initiatives/[id]/apps/[appId]/health`. `apps.vue` pasó a ser `apps/index.vue` para que la carpeta `apps/` no la convierta en una ruta padre.

### features/health
- `FeaturesHealthAppPanel` (`AppPanel.vue`): props `appId`, `initiativeId`; `/admin/initiatives/[id]/apps/[appId]/health` (admin de la plataforma y managers de la iniciativa). `GET /apps/{id}/health`: resumen (estado de la app y la iniciativa, conteo de grants, grants de manager efectivos, checked-at), hallazgos (a nivel de app) y una tabla de hosts. Los veredictos y umbrales son del servidor (`problems[]`); el cliente no calcula ninguno. 404, o una app de otra iniciativa distinta a la de la ruta, muestra "App not found". "Refresh" relee silenciosamente y anuncia "Refreshed" en una región live polite (sr-only). Va bajo el h1 de la ruta padre, por lo que su heading es un h2.
- `FeaturesHealthHostsTable` (`HostsTable.vue`): props `deployments`, `problems`, `caption`. Por host: last seen (relativo + absoluto, "Never"), último probe (badge + hora, "No probe yet") y los hallazgos de ese host; un host sin hallazgos dice "No findings" (no "healthy").
- `FeaturesHealthProblemList` (`ProblemList.vue`): prop `problems`. Badge con el label y una oración fija por código (qué significa y qué revisar, redacción de docs/reference/metrics.md, genérica, sin herramientas del adoptante).
- `FeaturesHealthProbeBadge` (`ProbeBadge.vue`): prop `status: ReceivedProbeStatus | null` (Redirects to sign-in / Unprotected / Unexpected redirect / Unreachable (danger) / Not probed; cualquier otro valor dice "Unknown result", neutral). En la tabla Apps la antigüedad del probe (`last_probe_at` relativo) va junto al badge.
- `FeaturesHealthTimestamp` (`Timestamp.vue`): props `value: string | null`, `emptyText`. Texto relativo sobre un `<time>` con la hora absoluta según el locale.
- `FeaturesHealthOrphanHostsPanel` (`OrphanHostsPanel.vue`) y `FeaturesHealthOrphanHostsTable` (`OrphanHostsTable.vue`, prop `hosts`, `caption`): `/admin/health` (solo admin de la plataforma; la página no monta el panel para nadie más). `GET /health/orphan-hosts` (7 días, como máximo 100, sin paginación). Explica qué es un host huérfano y qué significa el conteo; los hosts son texto plano (`break-all`). Sin atajo "add to app" (necesita un selector de app).
- Nav: "Health" (solo admin de la plataforma) entre Users y Audit.

### features/groups (solo admin de la plataforma)
- `FeaturesGroupsPanel` (`Panel.vue`): `/admin/groups`. Caja de búsqueda "Search groups" (`?q=` con debounce, "No groups match your search." cuando está vacío, conteo anunciado en una región live polite). Lista keyset, "New group", eliminar tras `UiConfirmDialog` (dice que los grupos son globales; 409 = "This group still has members or grants. Remove them first."). Los managers nunca llegan a él (solo listan grupos en el diálogo de grants).
- `FeaturesGroupsTable` (`Table.vue`): props `groups`, `caption`, `busyId?`, `emptyText?`; emite `remove(group)`. La key enlaza a `/admin/groups/<id>`.
- `FeaturesGroupsMembersPanel` (`MembersPanel.vue`): prop `groupId`; `/admin/groups/[id]`. Lista keyset de miembros, agregar por email, quitar tras `UiConfirmDialog`; carga `GET /groups/{id}` para el h1 (nombre, si no key; "Group" hasta que cargue o si esa llamada falla) y mantiene "Group members" como h2; un 404 en el grupo o la lista = "Group not found".
- `FeaturesGroupsMembersTable` (`MembersTable.vue`): props `members`, `caption`, `emptyText?`, `personLabel?` (encabezado de columna, por defecto "Member"; también usado por las pantallas de áreas); emite `remove(member)`. Los miembros deshabilitados se listan con un badge.

### features/areas (solo admin de la plataforma)
Las keys y nombres siguen las reglas de grupo (se reutiliza `utils/group.ts`: `validateGroupKey`, `GROUP_FIELD_COPY`, `groupLabel`). Sin caja de búsqueda.
- `FeaturesAreasPanel` (`Panel.vue`): `/admin/areas`. Lista keyset, "New area", eliminar tras `UiConfirmDialog` (409 = "This area still has members or supervisors. Remove them first, then delete the area."). El copy del encabezado explica qué puede hacer un supervisor.
- `FeaturesAreasTable` (`Table.vue`): props `areas`, `caption`; emite `remove(area)`. La key enlaza a `/admin/areas/<id>`.
- `FeaturesAreasDetailPanel` (`DetailPanel.vue`): prop `areaId`; `/admin/areas/[id]`. h1 = nombre, si no key ("Area" hasta que cargue); muestra la key; "Area not found" ante un 404 del área o de una lista; dos `PeoplePanel`.
- `FeaturesAreasPeoplePanel` (`PeoplePanel.vue`): props `areaId`, `kind` (`members` | `supervisors`); emite `not-found`. h2 "Members" / "Supervisors" con copy de ayuda, botón de agregar, lista keyset (reutiliza `FeaturesGroupsMembersTable`), quitar tras `UiConfirmDialog`.

### features/supervision (supervisores de área; layout por defecto, no el gate de admin)
Páginas `/areas`, `/areas/[id]`, `/areas/[id]/people/[userId]` (sin PII en la URL: el email de la persona se recuerda en `useState('supervision-people')` por la lista de miembros y se lee de vuelta como texto a mostrar, recurriendo a "this person"). Home muestra "My areas" y el Nav admin muestra "My areas" cuando `supervised_areas` no está vacío. Un supervisor nunca crea tokens, cambia accesos ni deshabilita a nadie; la API responde 404 fuera de alcance.
- `FeaturesSupervisionGate` (`Gate.vue`): prop `areaId?`; resuelve `/me` (skeleton, error de carga, "You do not supervise any area", "Area not found" cuando el id no está en `supervised_areas`); slot prop `area`.
- `FeaturesSupervisionAreasPanel` (`AreasPanel.vue`): prop `areas`; copy simple sobre lo que un supervisor puede y no puede hacer, tabla con un enlace "People" por área.
- `FeaturesSupervisionMembersPanel` (`MembersPanel.vue`): prop `areaId`; `GET /areas/{id}/members` (solo miembros supervisables) con `FeaturesAdminPagedBlock`; por persona un enlace "Tokens and robots"; guarda `{user_id: email}` en `useState('supervision-people')`.
- `FeaturesSupervisionPersonPanel` (`PersonPanel.vue`): props `userId`, `areaId`, `label`; cuando la lista de robots responde 404 todo el panel pasa a "This person is not in your areas" con un enlace de vuelta al área; si no, `FeaturesTokensPanel` en `/users/{id}/tokens` (revocar en `/tokens/{id}`, sin crear) más `FeaturesSupervisionRobotsPanel`.
- `FeaturesSupervisionRobotsPanel` (`RobotsPanel.vue`): props `userId`, `label`; emite `not-found`; `GET /users/{id}/robots`; por robot "Tokens" (el `FeaturesTokensPanel` de un robot se muestra debajo, desde `/robots/{id}/tokens`) y "Change owner".
- `FeaturesSupervisionOwnerDialog` (`OwnerDialog.vue`): props `robot`, `v-model:open`; emite `saved(robot)` y `gone` (PATCH 404: el diálogo se cierra, la lista de robots se recarga silenciosamente con "This robot is no longer in your scope."). Select nativo sobre las personas activas de todas las áreas supervisadas (miembros leídos al abrir con `limit=200`, 20 páginas por área como máximo, aparece una pista cuando se trunca; una carga más nueva o cerrar el diálogo descarta los resultados obsoletos), `PATCH /robots/{id}` `{owner_user_id}`; 422 `owner_user_id` (unknown / not_allowed) en el campo.

### features/users (solo admin de la plataforma)
- `FeaturesUsersPanel` (`Panel.vue`): `/admin/users`. Búsqueda por email exacto (`GET /users?email=` devuelve una lista, como máximo 50, sin paginar: el email no es único). Disable/Enable tras `UiConfirmDialog` (deshabilitar también termina las sesiones del usuario); 409 en la fila propia del que llama = `SELF_DISABLE_COPY`, en otro caso `LAST_ADMIN_COPY`. "Revoke sessions" tras `UiConfirmDialog`; en tu propia cuenta advierte y, si tiene éxito, recarga la página (ForwardAuth entonces redirige a iniciar sesión).
- `FeaturesUsersTable` (`Table.vue`): props `users`, `selfId`, `caption`, `busyId?`; emite `setStatus(user)`, `revokeSessions(user)`. "Disable" está oculto para `selfId`; un badge "You" marca la fila.
- `FeaturesUsersStatusBadge` (`StatusBadge.vue`): prop `status` (`UserStatus`).

### features/audit (admin de la plataforma y managers)
- `FeaturesAuditPanel` (`Panel.vue`): `/admin/audit`. Más nuevo primero, "Load more" keyset. Filtro de iniciativa = `BaseSelect` nativo sobre `GET /initiatives` ("All initiatives" / "All my initiatives", "Load more initiatives"); cambiarlo recarga. Sin filtro la API limita a un manager a sus iniciativas; un filtro sobre la iniciativa de otra persona es un 404 ("Initiative not found").
- `FeaturesAuditTable` (`Table.vue`): props `events`, `caption`, `initiativeLabels` (id a label; se muestra el id cuando no está cargado). Columnas cuándo, actor (email; un robot muestra su nombre y "Robot"; "Unknown" cuando ambos son null), acción, tipo/id del objetivo, iniciativa, cambios.
- `FeaturesAuditChanges` (`Changes.vue`): props `before`, `after` (JSON arbitrario), opcionales `action`, `occurredAt` (sufijo sr-only de "Show changes"). `<details>` nativo colapsado por defecto; el JSON se calcula y el `<pre>` se renderiza solo mientras está abierto (`@toggle`), limitado a 64 KB por lado con una nota visible "Truncated", solo interpolación de texto (nunca `v-html`). Los paneles Audit y Users anuncian "N events" / "N users found" en una región live polite sr-only tras (re)cargar.

### features/tokens (todo usuario con sesión iniciada)
- `FeaturesTokensPanel` (`Panel.vue`): props `path` (lista + crear), `revokeBase` (`DELETE {revokeBase}/{id}`), `offerAdmin`, `canCreate`, `owner` ("you" / "the robot ci"), `expiryOptions?`, `heading?` / `headingLevel?` (por defecto "Tokens", h2). El caption de la tabla es el heading cuando se da. Con `canCreate` en false (supervisores, y robots deshabilitados en el detalle de robot admin) el texto de revocar omite "create a new token". Lista keyset, "New token", revocar tras `UiConfirmDialog`. Usado por `/tokens` (`/me/tokens`) y por el detalle de robot (`/robots/{id}/tokens`, revocado en `/tokens/{id}`).
- `FeaturesTokensTable` (`Table.vue`): props `tokens`, `caption`; emite `revoke(token)`. Nombre, scope, badge de estado (Active / Expired / Revoked), creado, expira, último uso; Revoke solo en tokens vivos.
- `FeaturesTokensCreateDialog` (`CreateDialog.vue`): props `path`, `offerAdmin`, `owner`, `expiryOptions?`, `v-model:open`; emite `created` (sin payload: el secreto nunca sale del diálogo) y `uncertain`. Nombre, scope (admin solo cuando `offerAdmin`; la API decide: 422 `scope.not_allowed`), lista de expiración (`EXPIRY_OPTIONS`; 422 `too_large` cuando excede el tope del servidor), 409 = `TOKEN_LIMIT_COPY`. Con un 201 el diálogo cambia a la vista del secreto: campo de solo lectura cuya pista (aria-describedby) dice que se muestra solo una vez, "Copy secret" (estado polite "Secret copied to the clipboard."; una copia fallida es un `role=alert` de peligro). Mientras se muestra el secreto el diálogo es `persistent` (sin Escape ni click fuera): solo "Done" o la X lo cierran. El secreto existe únicamente en un ref local, borrado cuando el diálogo se cierra o se desmonta; nunca en un store, storage, URL ni log. La creación no es idempotente: tras una falla ambigua (network, 503, 5xx) el diálogo se cierra y emite `uncertain`; el panel recarga la lista y muestra "The result is unknown. Check the list before creating another token.". Los 4xx definitivos permanecen en el formulario.
- Página `/tokens` (layout por defecto; enlazada desde home como "API tokens"): los tokens propios del que llama; el scope admin se ofrece cuando `/me` dice admin de la plataforma o manager.

### features/robots (solo admin de la plataforma)
- `FeaturesRobotsPanel` (`Panel.vue`): `/admin/robots`. Filtro de estado (`BaseSelect` nativo con label: All / Active / Disabled, enviado como `?status=`; cambiarlo recarga desde la primera página). Lista keyset (una creación con resultado desconocido la recarga y muestra una alerta), "New robot", Edit, Disable / Enable tras `UiConfirmDialog` (deshabilitar dice que revoca todos los tokens y habilitar no los restaura). También monta `FeaturesRobotsRevokeSecret`.
- `FeaturesRobotsTable` (`Table.vue`): props `robots`, `selfId`, `caption`, `busyId?`, `emptyText?`; emite `edit(robot)`, `setStatus(robot)`. La columna de dueño dice "You", el email del dueño (nombre como respaldo) o "None". "Tokens" enlaza a `/admin/robots/<id>`.
- `FeaturesRobotsFormDialog` (`FormDialog.vue`): props `robot` (null = crear), `v-model:open`; emite `saved(robot)` y `uncertain` (solo al crear, tras una falla ambigua). Nombre; dueño = mantener / establecer por email / ninguno. El email se resuelve con `GET /users?email=` a exactamente una persona activa, enviado como `owner_user_id` (`""` lo limpia). Editar envía solo los campos cambiados. 409 = nombre en uso.
- `FeaturesRobotsDetailPanel` (`DetailPanel.vue`): prop `robotId`; `/admin/robots/[id]`. `GET /robots/{id}`; 404 = "Robot not found", 403 = error de carga. Robot deshabilitado: sin "New token". El scope admin siempre se ofrece (la API rechaza con 422).
- `FeaturesRobotsRevokeSecret` (`RevokeSecret.vue`): `POST /tokens/revoke {token}`; la respuesta es 204 para cualquier valor bien formado, por lo que el copy nunca confirma la existencia; 422 = mal formado. El campo se limpia tras cada envío (los errores reintentables dicen "Paste the token again.") y el valor nunca se repite. Texto plano a propósito: un gestor de contraseñas ofrecería guardar un secreto filtrado.
- Nav: "Robots" (solo admin de la plataforma) entre Users y Health.
- Grants: el diálogo de creación tiene un sujeto "Robot" (`robot_id`, select nativo sobre los robots activos de `GET /robots`, que los managers pueden listar). Un grant de robot llega como `user` con un `email` vacío: las tablas usan `subjectLabel()` (email, si no nombre) con una línea atenuada "Robot"; la confirmación de revocar dice "robot <name>". Audit: la columna actor muestra `actor_email`, si no `actor_name` más "Robot", si no "Unknown". Un evento hecho con token muestra una línea atenuada "via token <first 8 chars of token_id>" (sin enlace).

## TODO (por orden de prioridad)

1. Menu (Reka UI), solo si una pantalla lo necesita; audita primero su código en busca de `<style>`.
