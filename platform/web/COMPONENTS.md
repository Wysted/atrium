# Component catalog

Source of truth for the portal UI components. Read it before creating or changing a component and update it in the same change.

## Rules

1. Layers
   - `app/components/base/` (prefix `Base`): own primitives, no third-party UI. Layout, text, button, link, icon.
   - `app/components/ui/` (prefix `Ui`): composed widgets built on Reka UI (`reka-ui` pinned at 2.11.0, imported explicitly, no Nuxt module). Today: Dialog and AlertDialog. Do NOT use Reka's Select, Combobox or Splitter: they render or create a `<style>` element (CSP). A Select, if needed, is a native `<select>` in `base/`. Menu/Popover are unaudited.
   - `app/components/features/<domain>/`: domain components that know about the API types (`app/types/api.ts`). Nuxt names them with the path prefix: `features/home/AppCard.vue` is `<FeaturesHomeAppCard>`.
2. Tokens only. Styling comes from `app/assets/css/tokens.css` through semantic utilities (`bg-surface`, `text-text-muted`, `border-border`, `bg-primary`, `rounded-md`, `shadow-sm`, `p-4`). No raw palette values, no arbitrary values (`bg-[#fff]`, `p-[13px]`). The one allowed exception: an arbitrary value that only references declared tokens through `var(--...)` (e.g. `BaseGrid` columns using `--container-grid-*`); never rely on Tailwind's default theme variables. An adopter re-themes by overriding that one file (colors, radius, type scale, shadows; the dark theme reuses the same names).
3. Pages compose, they do not markup. `app/pages/**` may not use raw HTML elements (div, span, p, h1-h6, a, button, input, select, textarea, table, ul, ol, li, img, form, label, section, header, footer, nav, main); `vue/no-restricted-html-elements` fails lint. Layouts and components may use them.
4. Tailwind classes must be literal strings. Props map to classes through lookup tables, never by string concatenation (`'gap-' + n` is not detected by the scanner).
5. CSP (`script-src 'self' 'sha256-...'; style-src 'self'`):
   - no `<script>`/`<style>` inline, no `useHead` with inline scripts/styles, no `style=` attributes in markup;
   - `:style` bindings are allowed (CSSOM) but prefer token classes;
   - `v-html` is banned (`vue/no-v-html` error);
   - no CDN, web fonts or external origins; icons are inline SVG without `<style>`;
   - any Reka UI component must be verified against `style-src 'self'` before adoption (read its source in `node_modules/reka-ui/dist/<Component>/`: look for `as: "style"`, `createElement("style")`, `setAttribute("style")`).
   `npm run generate && npm run check:csp` enforces the HTML side.
6. Accessibility: semantic elements, visible focus ring (`outline-focus` token), labelled controls, icon-only controls require `label`.
7. Shared prop types live in `app/types/ui.ts` (`Space` is the spacing scale: 0, 1, 2, 3, 4, 6, 8, 12 times `--spacing`).

## Layout primitives

All accept `as` (tag name, default `div`) and a default slot.

### BaseBox
- Purpose: generic container with optional padding, surface, border, radius, shadow.
- Props: `as?: string`, `padding?: Space = 0`, `surface?: 'none'|'base'|'raised' = 'none'`, `bordered?: boolean`, `rounded?: 'none'|'sm'|'md'|'lg' = 'none'`, `shadow?: 'none'|'sm'|'md' = 'none'`.
- Slots: default.
- Example: `<BaseBox as="section" surface="raised" bordered rounded="md" :padding="4">...</BaseBox>`
- A11y: use `as` for landmarks (`header`, `main`, `nav`, `section`); a `section` needs a heading.

### BaseStack
- Purpose: vertical flow with uniform gap.
- Props: `as?`, `gap?: Space = 4`, `align?: 'start'|'center'|'end'|'stretch'|'baseline' = 'stretch'`.
- Slots: default.
- Example: `<BaseStack :gap="6"><BaseHeading :level="1">Title</BaseHeading><BaseText>Body</BaseText></BaseStack>`
- A11y: use `as="ul"` only with list-item children (a `BaseBox as="li"`).

### BaseCluster
- Purpose: horizontal wrapping row (actions, tags, toolbars).
- Props: `as?`, `gap?: Space = 2`, `align? = 'center'`, `justify?: 'start'|'center'|'end'|'between' = 'start'`, `wrap?: boolean = true`.
- Slots: default.
- Example: `<BaseCluster justify="between"><BaseText>Left</BaseText><BaseButton>Right</BaseButton></BaseCluster>`
- A11y: visual order equals DOM order.

### BaseGrid
- Purpose: responsive auto-fill grid; columns collapse by minimum width.
- Props: `as?`, `gap?: Space = 4`, `minColumn?: 'sm'|'md'|'lg' = 'md'` (16rem / 18rem / 20rem via the `--container-grid-sm|md|lg` tokens).
- Slots: default.
- Example: `<BaseGrid min-column="md"><BaseBox>...</BaseBox><BaseBox>...</BaseBox></BaseGrid>`
- A11y: DOM order is the reading order.

### BaseContainer
- Purpose: centered max-width wrapper with side gutters (`--spacing-gutter`).
- Props: `as?`, `size?: 'page'|'reading' = 'page'` (`--container-page` 72rem, `--container-reading` 44rem).
- Slots: default.
- Example: `<BaseContainer size="reading">...</BaseContainer>`

## Text

### BaseText
- Purpose: body text with tone, size and weight.
- Props: `as?: string = 'p'`, `size?: 'xs'|'sm'|'base'|'lg'|'xl'|'2xl'|'3xl'|'4xl' = 'base'`, `tone?: 'default'|'muted'|'primary'|'danger'|'warning'|'success' = 'default'`, `weight?: 'normal'|'medium'|'semibold'|'bold' = 'normal'`.
- Slots: default.
- Example: `<BaseText tone="danger" size="sm">Something failed.</BaseText>`
- A11y: do not rely on tone alone to convey state; add words or an icon with a label.

### BaseHeading
- Purpose: semantic heading with the visual size decoupled from the level.
- Props: `level: 1|2|3|4|5|6` (required, picks `h1`..`h6`), `size?: TextSize` (defaults per level: 3xl, 2xl, xl, lg, base, sm).
- Slots: default.
- Example: `<BaseHeading :level="2" size="lg">Section</BaseHeading>`
- A11y: do not skip levels; one `h1` per page.

## Controls

### BaseButton
- Purpose: action or navigation styled as a button.
- Props: `to?: string` (internal route, NuxtLink), `href?: string` (plain `<a>`; must be a trusted URL, never user-controlled: it is not sanitized. Built-from-data hrefs go through a validating helper such as `hostUrl()`, see `FeaturesHomeAppCard`), `target?: '_self'|'_blank'` (`_blank` adds `rel="noopener noreferrer"`), `type?: 'button'|'submit'|'reset' = 'button'`, `variant?: 'primary'|'secondary'|'ghost'|'danger' = 'primary'`, `size?: 'sm'|'md'|'lg' = 'md'`, `disabled?: boolean`, `loading?: boolean`.
- Slots: default (text, optionally with a `BaseIcon`).
- Events: `click` (declared emit, so a parent `@click` never reaches the root element directly); emitted only while active, never when `disabled` or `loading`.
- Example: `<BaseButton variant="danger" type="submit" :loading="saving">Delete</BaseButton>`
- A11y: renders `<button type="button">` by default (never an accidental submit). `loading` shows a decorative spinning `loader` icon (`motion-safe:animate-spin`), sets `aria-busy` and blocks activation (the `<button>` keeps native focus and uses `aria-disabled` plus a click handler that prevents default, so it will not submit forms); hover styles apply only while actionable; the `disabled` prop uses the native attribute on buttons. Disabled/loading links render without `href` plus `aria-disabled`. Icon-only buttons need an `aria-label` on the button.

### BaseLink
- Purpose: inline text link.
- Props: `to?: string` (internal, NuxtLink), `href?: string` (external, `<a>`; must be a trusted URL, never user-controlled: it is not sanitized. Built-from-data hrefs go through a validating helper such as `hostUrl()`, see `FeaturesHomeAppCard`), `target?: '_self'|'_blank'` (`_blank` adds `rel="noopener noreferrer"`).
- Slots: default.
- Example: `<BaseLink href="/docs/" target="_blank">Docs</BaseLink>`
- A11y: link text must make sense out of context; announce new-tab behavior in the text when it matters.

### BaseIcon
- Purpose: inline SVG from a curated set (`check`, `loader`, `x`, `plus`, `minus`, `chevron-down`, `chevron-right`, `external-link`, `alert-triangle`, `info`, `search`). Add a name in `app/types/ui.ts` and the path table in the component.
- Props: `name: IconName` (required), `label?: string`, `size?: 'sm'|'md'|'lg' = 'md'`. Color follows `currentColor`.
- Slots: none.
- Example: `<BaseIcon name="alert-triangle" label="Warning" />` (meaningful) or `<BaseIcon name="check" />` (decorative).
- A11y: without `label` the icon is `aria-hidden`; with `label` it is `role="img"` and `aria-label`. An icon that is the only content of a control must carry the label. In dev (`import.meta.dev`) it warns on an unknown `name`, an empty `label`, and a `role="img"`/`aria-label` attribute without the `label` prop.

## Forms and data

### BaseField
- Purpose: label + control + hint + error, wired for assistive tech.
- Props: `label: string` (required), `hint?: string`, `error?: string`, `required?: boolean` (adds a visual `*`, `aria-required` on the control).
- Slots: default, with slot props `id`, `aria-describedby` (hint/error ids, only rendered ones), `aria-invalid`, `aria-required`. Bind them: `<BaseField label="Key" :error="e" v-slot="field"><BaseInput v-bind="field" v-model="key" /></BaseField>`.
- A11y: the `label` is a real `<label for>`; the error is text under the control (not colour-only). Move focus to the first invalid control on submit (`BaseInput.focus()`).

### BaseInput
- Purpose: single-line text input.
- Props: `modelValue` (`v-model`, string), `type?: 'text'|'email'|'search'|'datetime-local' = 'text'` (`datetime-local`: wall-clock value `YYYY-MM-DDTHH:mm`; `localInputToRfc3339()` in `app/utils/datetime.ts` adds the user's offset), `readonly?`, `disabled?`. Other attributes (`id`, `aria-*`, `autocomplete`...) fall through to the `<input>`.
- Exposes `focus()`. Invalid look comes from `aria-invalid`.
- A11y: never use without a `BaseField` (or another label). Focus ring via the `focus` token.

### BaseSelect
- Purpose: native `<select>` (Reka's Select injects `<style>`, banned by the CSP).
- Props: `modelValue` (`v-model`, string), `options: SelectOption[]` (`{ value, label, disabled? }`, in `app/types/ui.ts`), `placeholder?: string` (adds a disabled first option with the empty value), `disabled?`. Other attributes (`id`, `aria-*`) fall through to the `<select>`.
- Exposes `focus()`. Invalid look comes from `aria-invalid`.
- Example: `<BaseField label="Role" v-slot="field"><BaseSelect v-bind="field" v-model="roleId" :options="roleOptions" placeholder="Choose a role" /></BaseField>`
- A11y: never use without a `BaseField` (or another label); the browser provides keyboard and the platform picker.

### BaseCheckbox
- Purpose: native checkbox with its visible label (default slot) and optional hint.
- Props: `modelValue` (`v-model`, boolean), `hint?: string` (linked with `aria-describedby`). Other attributes (`disabled`, `name`) fall through to the `<input>`.
- Example: `<BaseCheckbox v-model="managesAccess" hint="Holders can grant access.">Manages access</BaseCheckbox>`
- A11y: the label is a real `<label for>`; it carries its own text, so it does not go inside a `BaseField`.

### BaseTable
- Purpose: semantic data table.
- Props: `caption: string` (required, names the table), `captionHidden?: boolean` (visually hidden caption), `columns: TableColumn[]` (`{ key, label, align?: 'start'|'end', hideLabel? }`, in `app/types/ui.ts`), `rows: T[]`, `rowKey: (row) => string`, `rowHeader?: string` (column key rendered as `<th scope="row">`), `emptyText?: string = 'Nothing to show.'`.
- Slots: `cell` with `{ row, column }`; the default cell renders `row[column.key]`.
- Example: `<BaseTable caption="Initiatives" :columns="cols" :rows="rows" :row-key="r => r.id"><template #cell="{ row, column }">...</template></BaseTable>`
- A11y: `<caption>`, `<th scope>`; empty state is a real row. The table sits in a horizontally scrollable `role="region"` (focusable, labelled by the caption), so a wide table scrolls inside itself, not the page.

## UI (Reka UI)

Style behavior of what is used (reka-ui 2.11.0, from its source; the browser check belongs to the orchestrator): Dialog/AlertDialog set `overflow`, `padding-right`, `margin-right` and `pointer-events` on `document.body`, `--scrollbar-width` on `<html>`, bind `pointer-events` in a `:style` on the content and the overlay, and Presence may set `animation-fill-mode`; all via CSSOM (allowed by `style-src 'self'`). No `<style>` element, no `style=` in markup, no focus-guard spans (those are Select/Menu/Popover/Combobox). Both portal to `<body>`.

### UiDialog
- Purpose: modal dialog (Reka `Dialog`): overlay, title, description, close button, body slot.
- Props: `open` (`v-model:open`, required), `title: string`, `description: string` (both required: they name the dialog), `busy?: boolean` (blocks Escape, outside click and the close button while a request runs).
- Props also: `initialFocus?: { focus: () => void }` (e.g. a `BaseInput` template ref; receives focus on open via `@open-auto-focus.prevent`; default Reka focus when omitted), `persistent?: boolean` (Escape and outside clicks do not close it, e.g. while a one-time secret is shown; the X and in-dialog buttons still do).
- Slots: default (body, usually a form).
- A11y: Reka traps focus; focus returns to the opener through `useFocusReturn` (`app/composables/useFocusReturn.ts`: `document.activeElement`, else the last clicked button/link, so it works in Safari where a click does not focus the button; it falls back to Reka's restore if the opener is gone). Closes on Escape, marks the rest `aria-hidden`.

### UiConfirmDialog
- Purpose: confirmation of a destructive or hard-to-undo action (Reka `AlertDialog`, `role="alertdialog"`): no outside-click dismissal, "Cancel" gets focus first; focus returns to the opener via `useFocusReturn` like `UiDialog`.
- Props: `open` (`v-model:open`), `title`, `description`, `confirmLabel` (required), `cancelLabel?: string = 'Cancel'`, `variant?: 'primary'|'danger' = 'danger'`, `busy?: boolean`, `error?: string` (fixed message of the last failed attempt, `role="alert"`).
- Events: `confirm`. It does not close itself: the parent runs the request, shows `error`, and sets `open` to false on success.
- Slots: default (extra content above the buttons).

## Error page

### app/error.vue
- Purpose: the app-wide error page (client-side 404 and runtime errors). Required under the strict CSP: Nuxt's default error page injects an inline `<style>` and `<script>`, both blocked, leaving the page unstyled.
- Props: `error: NuxtError` (supplied by Nuxt).
- Composition: `NuxtLayout` + `BaseStack`, `BaseText`, `BaseHeading`, `BaseCluster`, `BaseButton` only. Title via `useHead({ title })`.
- Copy: 404 shows "Page not found"; any other status shows "Something went wrong". Only the status code and fixed copy are shown; `error.message` is rendered in dev only (`import.meta.dev`), never the stack.
- Action: "Go to home" calls `clearError({ redirect: '/' })`.
- Note: `NUXT_E1005` ("Error caught during app initialization") in the console on a direct visit to an unknown route is Nuxt's own `app:error` diagnostic for the 404 raised during startup; it is expected and not a CSP issue.

## Surfaces and feedback

### BaseCard
- Purpose: raised, bordered, rounded surface with a small shadow that groups content (a `BaseBox` preset).
- Props: `as?: string = 'div'`, `padding?: Space = 4`.
- Slots: default.
- Example: `<BaseCard as="li"><BaseText>...</BaseText></BaseCard>`
- A11y: use `as="li"` only inside a list; give a cluster of cards a heading.

### BaseBadge
- Purpose: small label for a role or status.
- Props: `tone?: 'neutral'|'primary'|'success'|'warning'|'danger' = 'neutral'`.
- Slots: default (text).
- Example: `<BaseBadge tone="primary">editor</BaseBadge>`
- A11y: tone is decoration; the text must carry the meaning. Text/border colors use the semantic tokens.

### BaseSkeleton
- Purpose: decorative loading placeholder (`motion-safe:animate-pulse`).
- Props: `height?: 'sm'|'md'|'lg' = 'md'`, `width?: 'half'|'full' = 'full'`.
- Slots: none.
- Example: `<BaseSkeleton height="lg" width="half" />`
- A11y: `aria-hidden`. The loading region must say it is busy: `aria-busy="true"` plus visually hidden text (`class="sr-only"` on a `BaseText`), see `FeaturesHomeAppSkeleton`.

### BaseEmptyState
- Purpose: "nothing here" or "could not load" block with an optional action.
- Props: `title: string`, `description?: string`, `icon?: IconName = 'info'`, `level?: HeadingLevel = 2`.
- Slots: default (actions, e.g. a `BaseButton`).
- Example: `<BaseEmptyState title="No apps yet" description="..."><BaseButton>Retry</BaseButton></BaseEmptyState>`
- A11y: renders a `section` with a heading; pick `level` so headings do not skip levels. The icon is decorative.

### BasePageHeader
- Purpose: page title block: `h1`, optional description and an actions slot.
- Props: `title: string`, `description?: string`. The `h1` has `tabindex="-1"`; the component exposes `focusTitle()` to move focus there (template ref).
- Slots: default (page actions, right aligned, wraps on small screens).
- Example: `<BasePageHeader title="Welcome" description="..."><BaseButton to="/admin">Administration</BaseButton></BasePageHeader>`
- A11y: owns the single `h1` of the page; rendered in a `header` element.

## API layer

- `app/types/api.ts`: payload types mirroring `platform/api/internal/portalhttp` (`MeResponse`, `HomeResponse`, `HomeApp`, `InitiativeRef`, `Initiative`, `InitiativeList`, `CreateInitiativeBody`, `PatchInitiativeBody`, `Role`, `RoleList`, `CreateRoleBody`, `PatchRoleBody`, `Grant`, `GrantRole`, `GrantUser`, `GrantGroup`, `GrantList`, `CreateGrantBody`, `Group`, `GroupList`, `ApiToken`, `TokenList`, `CreatedToken`, `CreateTokenBody`, `TokenScope`, `Robot`, `RobotRef`, `RobotList`, `RobotRefList`, `CreateRobotBody`, `PatchRobotBody`, `CreateGroupBody`, `GroupMember`, `GroupMemberList`, `AddMemberBody`, `AdminUser`, `AdminUserList`, `PatchUserBody`, `RevokeSessionsResult`, `AuditEvent`, `AuditList`, `UserStatus`, `App`, `AppStatus`, `AppList`, `Deployment`, `CreateAppBody`, `PatchAppBody`, `CreateDeploymentBody`, `ProbeStatus`, `ReceivedProbeStatus`, `HealthProblemCode`, `HealthProblem`, `AppHealth`, `OrphanHost`, `OrphanHostList`) and the RFC 9457 `Problem`.
- `app/composables/useApi.ts`: `apiGet<T>(path)` (same-origin `$fetch` under `/api/v1`, no tokens or CSRF headers, no retries), `ApiError` (`status`, `kind`, `problem`, `reloading`) and `useApiData<T>(key, path)` (client-side `useAsyncData`, returns `data`, `error`, `pending`, `refresh`, `refreshQuiet()` (re-fetch that keeps the previous state on failure, resolves to a boolean), `set(value)` (replace the cached value, e.g. with a mutation response); failures come back as `error`, never thrown). Error kinds: `unauthorized` (401), `forbidden` (403), `not_found` (404), `conflict` (409), `invalid` (400/415/422), `unavailable` (503), `server` (other 5xx), `network`, `unknown`. Response bodies are never logged and `ApiError.message` is fixed text.
- Mutations: `apiSend(method, path, body?)` / `useApi().post|patch|del` (`POST`/`PATCH`/`DELETE`, `Content-Type: application/json` always sent, DELETE without a body included, same-origin, no CSRF headers: CrossOriginProtection plus the JSON content type are the guards; no retries). `ApiError.retryAfter` holds a numeric `Retry-After` (503), capped at 120 s. 413 maps to kind `invalid`.
- `app/utils/mutationError.ts`: `describeMutationError(error, { conflict, notFound?, fields?, fieldCopy? })` -> `{ message, fields, retryable }`, fixed English copy only (server `detail` is never shown). 422 `errors[]` -> per declared field via `CODE_COPY` (`required`, `invalid`, `must_be_in_the_future`, `no_unique_active_user`, `unknown`, `exactly_one_required`, `reserved`, `at_least_one_required`, `not_allowed`, `too_large`; unknown codes fall back to `invalid`; first error per field wins; errors on fields the form does not render fold into "Check the values and try again."), 400/413/415 -> "The request was not accepted. Reload the page and try again.", 409 -> the per-action `conflict` copy, 403 -> "You do not have permission to do this.", 404 -> "This item no longer exists.", 503 -> "The portal is busy. Try again in N seconds." (retryable), network/5xx retryable, 401 -> reload policy below.
- `app/composables/useAdminAccess.ts`: `/me` (shared `me` key) as `isPlatformAdmin`, `isManager`, `canAdminister`, `managesInitiative(id)` (platform admin, or `managed_initiatives` includes the id), `revalidate()` (quiet background re-read; the admin layout calls it on mount). The UI only hides; the API enforces.
- `app/composables/useGroupSearch.ts`: `useGroupSearch(delay = 300)` -> `input` (bind to the box), `query` (debounced, trimmed, capped at `GROUP_SEARCH_MAX` = 64), `path()` (`/groups` or `/groups?q=...`, via `groupsPath()` in `app/utils/group.ts`; pass it as the `usePagedList` getter and `reload()` when `query` changes).
- `app/composables/usePagedList.ts`: `usePagedList<Page, Item>(path, select, { immediate? })` (`path` may be a getter, e.g. reading a filter, and may carry a query string) keyset list (opaque cursor echoed back; `immediate: false` skips the load on mount, the caller runs `reload()`): `items`, `loading`, `loadingMore`, `error`, `hasMore`, `reload()`, `loadMore()`; a newer request wins over an older one.
- `app/utils/initiative.ts`: key/name validation mirroring Go (key `^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`, <= 63; name <= 200 bytes), `initiativeLabel()` (name, else key), `PLATFORM_INITIATIVE_KEY`.
- `app/composables/useInitiativeContext.ts`: `provideInitiativeContext()` / `useInitiativeContext()` -> `{ initiative, set }`; the `/admin/initiatives/[id]` parent route provides the loaded initiative to its nested pages.
- `app/utils/role.ts`: role key validation (<= 32, same pattern as initiative keys), `ROLE_FIELD_COPY`, `LAST_MANAGER_COPY` (fixed 409 copy for revoking a manager grant or turning `manages_access` off).
- `app/utils/datetime.ts`: `formatTimestamp()` (locale), `isPast()`, `localInputToRfc3339()` (a `datetime-local` value to RFC 3339 with the user's offset).
- 401 policy: the session is gone, so the page reloads once (ForwardAuth answers the navigation with a 302 to login). `login_url` is not followed. Loop guard: at most one reload per 30 s (`sessionStorage`), plus a module flag so every 401 after the first in the same page load also reports `reloading` (no flash of the reload message); beyond that `ApiError.reloading` is false and the UI asks the user to reload.
- `app/utils/app.ts`: app/host rules and fixed copy. `validateAppName()` (<= 200 bytes, no NUL), `appLabel()` (name, else id), `normalizeHost()` (trim + ASCII-only lowercase: Go `hostname.Valid` rejects uppercase, and `toLowerCase()` would map the Kelvin sign to `k`), `validateHost()` (via `isHostname()`), `isAmbiguousFailure()` (network, 503, other 5xx or unknown after sending), `APP_FIELD_COPY`, `HOST_TAKEN_COPY`, `APP_CREATE_AMBIGUOUS_COPY`, `PORTAL_APP_DISABLE_COPY`, `PORTAL_APP_LAST_HOST_COPY`.
- `app/utils/group.ts`: group key/name validation mirroring Go (key like initiatives, <= 63; name <= 200 bytes), `GROUP_FIELD_COPY`, `groupLabel()`.
- `app/utils/email.ts`: `validateEmail()` and `EMAIL_FIELD_COPY` (422 `email.*`, incl. `no_unique_active_user`), shared by add member and user lookup.
- `app/utils/user.ts`: `SELF_DISABLE_COPY` ("You cannot disable your own account.") and `LAST_ADMIN_COPY` ("This is the last active platform admin."), the 409 copy of `PATCH /users/{id}`.
- `app/utils/datetime.ts` also has `formatRelative()` ("3 hours ago", day/minute granularity, "just now" under a minute).
- `app/utils/health.ts`: `PROBLEM_COPY` / `problemCopy(code)` (fixed label, explanation and tone per health finding code, unknown codes fall back to `UNKNOWN_PROBLEM_COPY`) and `probeStatusCopy(status)` (badge label/tone of a stored probe result; `null` = "Not probed", unknown value = "Unknown result"; `HealthProblem.code` and `Deployment.last_probe_status` accept unknown strings via `(string & {})`; every sentence names who can act).
- `app/utils/token.ts`: token rules and copy: `TOKEN_NAME_MAX` (64), `TOKEN_LIMIT` (25), `EXPIRY_OPTIONS` (1, 7, 30 days, longest allowed) / `ROBOT_EXPIRY_OPTIONS` (adds 90 days) / `EXPIRY_DEFAULT` (7 days) / `EXPIRY_MAXIMUM` (omit `expires_in`, the server applies its cap) / `TOKEN_UNCERTAIN_COPY`, `SCOPE_OPTIONS`, `TOKEN_FIELD_COPY` (422 `name`, `scope` incl. `not_allowed`, `expires_in` incl. `too_large`), `TOKEN_LIMIT_COPY` (the 409), `tokenState()` (active / expired / revoked), `validateTokenName()`.
- `app/utils/robot.ts`: `validateRobotName()`, `ROBOT_FIELD_COPY`, `ROBOT_NAME_TAKEN_COPY`, `robotOwnerLabel()` (owner email, name as fallback; reuses the token name rules). `app/utils/user.ts` also has `subjectLabel()` (email, else name: a robot has no email).
- `app/utils/hostname.ts`: `isHostname()` (mirrors Go `hostname.Valid`: lowercase DNS labels, <= 253 chars, no trailing dot, last label not all digits) and `hostUrl()` (`https://<host>/` or null).

## Features

### features/home
- `FeaturesHomeAppCard` (`AppCard.vue`): props `app: HomeApp`. A `BaseCard as="li"` with name, initiative, host links (`hostUrl()`-validated, new tab, visually hidden "(opens in a new tab)") and role badges. The external-link icon is inside the `<a>`. When no host passes validation it shows a muted "No address available" note.
- `FeaturesHomeAppSkeleton` (`AppSkeleton.vue`): grid of three card skeletons. It carries no live region of its own: the page wraps the results in a persistent `aria-live="polite"` container with `aria-busy` while loading.
- `FeaturesHomeLoadError` (`LoadError.vue`): props `error: ApiError`, `retrying?: boolean` ("Try again" shows `BaseButton loading`), emits `retry`. Container has `role="alert"`. Fixed copy per error kind, never server detail.
- `app/pages/index.vue`: greeting from `/me` (name, else email), "Administration" button to `/admin` when `permissions.platform_admin` or `managed_initiatives` is non-empty (the UI only hides), loading, error, "No apps yet" and the grid. The error state depends only on `/me/apps`; if `/me` fails while apps load, a muted "Some details could not be loaded." notice with a `/me`-only retry shows instead. After a successful retry focus moves to the page h1. The Administration button navigates client-side to `/admin`, which redirects to `/admin/initiatives`.

### features/admin
- Layout `app/layouts/admin.vue`: header with home link and `FeaturesAdminNav`; resolves `/me` and gates the page: skeleton while loading, `FeaturesHomeLoadError` on failure, `FeaturesAdminNoAccess` for a user who is neither platform admin nor manager, otherwise the slot (so admin pages never fire calls for anyone else). Pages opt in with `definePageMeta({ layout: 'admin' })`.
- `FeaturesAdminNav` (`Nav.vue`): props `isPlatformAdmin`, `isManager`. `<nav aria-label="Administration">`, `aria-current` on the current section. Initiatives (admin and managers), Groups and Users (platform admin only), Audit (admin and managers).
- `FeaturesAdminNoAccess` (`NoAccess.vue`): fixed "You do not have access to administration" state with a link home.
- `app/pages/admin/index.vue`: client-side redirect (route middleware, no flash) to `/admin/initiatives`.

### features/initiatives
- `FeaturesInitiativesTable` (`Table.vue`): props `initiatives: Initiative[]`, `caption`. Columns name (key when empty, link to the detail), key, status.
- `FeaturesInitiativesStatusBadge` (`StatusBadge.vue`): prop `status`.
- `FeaturesInitiativesCreateDialog` (`CreateDialog.vue`): `v-model:open`, emits `created(initiative)` (the list page navigates to `/admin/initiatives/<id>`). Key input focused on open. Client validation like Go; 409 shows "An initiative with this key already exists. If you just retried, it may have been created by your earlier attempt: check the list." on the key field.
- `FeaturesInitiativesNameForm` (`NameForm.vue`): props `initiative`, `editable`; key always read-only; PATCH name; emits `saved(initiative)` (the PATCH response; the page sets it as its state, no re-fetch).
- `FeaturesInitiativesStatusPanel` (`StatusPanel.vue`): props `initiative`, `canChange`; one button (activate/deactivate) behind `UiConfirmDialog`, hidden for the `platform` initiative (409 still handled); emits `changed(initiative)` (the PATCH response).
- Pages: `/admin/initiatives` (keyset list with "Load more", "New initiative" for platform admins) and the nested `/admin/initiatives/[id]` (parent route `[id].vue`: loads the initiative once, h1, `FeaturesInitiativesSubNav`, `<NuxtPage />`; 404 -> "Initiative not found") with children `index` (overview + status; managers see it read-only), `grants` and `roles`. Each child is a thin page over a panel.
- `FeaturesInitiativesSubNav` (`SubNav.vue`): prop `initiativeId`. `<nav aria-label="Initiative">` with plain links Overview / Grants / Roles / Apps (not a tabs widget), `aria-current="page"` on the current one.

### features/roles
- `FeaturesRolesPanel` (`Panel.vue`): props `initiativeId`, `canEdit` (platform admin). Keyset list; with `canEdit`: "New role", toggle `manages_access` (ON goes straight through, OFF asks for confirmation; 409 = `LAST_MANAGER_COPY`) and delete behind `UiConfirmDialog` (409 = "Remove this role's grants first."). Managers see it read-only. The API returns no grant count per role, so none is shown.
- `FeaturesRolesTable` (`Table.vue`): props `roles`, `canEdit`, `caption`, `busyId?`; emits `toggle(role)`, `remove(role)`.
- `FeaturesRolesCreateDialog` (`CreateDialog.vue`): props `initiativeId`, `v-model:open`, emits `created(role)`. Key validated like Go (<= 32), `BaseCheckbox` for `manages_access`; 409 = key taken.

### features/grants
- `FeaturesGrantsPanel` (`Panel.vue`): props `initiativeId`, `canEdit` (platform admin or manager of the initiative). Keyset list, "Grant access", revoke behind `UiConfirmDialog` (409 = `LAST_MANAGER_COPY`). Removed grants leave the list locally; a creation reloads it.
- `FeaturesGrantsTable` (`Table.vue`): props `grants`, `canRevoke`, `caption`; emits `revoke(grant)`. Subject (user name/email or group), role (+ "Manages access" badge), expiry with an "Expired" badge (the API lists expired grants), created date.
- `FeaturesGrantsCreateDialog` (`CreateDialog.vue`): props `initiativeId`, `v-model:open`, emits `created(grant)`. Subject type select (user by email / group), role select (all pages of this initiative's roles), optional `datetime-local` expiry validated in the future, 422 per field (`email`, `group_id`, `role_id`, `expires_at`), 409 = subject already holds the role.
- `FeaturesGrantsGroupSelect` (`GroupSelect.vue`): `v-model` group id; a "Search groups" `BaseInput` (debounced 300 ms via `useGroupSearch`, trimmed, max 64) over a native select fed by `GET /groups?q=`, with "Load more groups" for the current query (the same `q` on every page). The polite live region announces "N groups found" / "No groups found" after each (re)load. The chosen group is remembered and stays in the select even when a later search does not return it. Fetches on mount, so mount it only when needed.
- `FeaturesHomeLoadError` now takes optional `subject` (title: "Could not load {subject}", default "your apps") and `forbiddenDescription`.

### features/apps
- `FeaturesAppsPanel` (`Panel.vue`): props `initiativeId`, `canEdit` (platform admin). Keyset list of apps with their hosts; managers get it read-only. With `canEdit`: "New app", rename, disable (behind `UiConfirmDialog`) / enable (direct), add host, remove host (behind `UiConfirmDialog`). The apps payload has no initiative key and no portal flag, so the portal app cannot be recognised: Disable and Remove are always offered and the 409s show "The portal app cannot be disabled." / "The portal app needs at least one host.". Creating an app is not idempotent: no retry on an ambiguous failure; the list is reloaded and "The app may have been created. The list was reloaded; if it is missing, reload again in a moment before trying again." stays visible next to a "Reload list" button. Removing the host in use (`isCurrentHost`: matches `location.host` or `location.hostname`, lowercase) shows a stronger danger confirmation. A 404 (remove host, toggle) or a 409 when enabling closes the dialog, reloads the list quietly (`reload(true)`) and shows a fixed notice. A created app is appended and focused when the list has no further page, otherwise the notice points to "Load more".
- `FeaturesAppsTable` (`Table.vue`): props `apps`, `canEdit`, `caption`, `busyId?`; emits `rename(app)`, `toggle(app)`, `addHost(app)`, `removeHost(app, deployment)`. Hosts are `hostUrl()`-validated external links (plain text when the check fails); the name cell is a focus target `#app-<id>`.
- `FeaturesAppsStatusBadge` (`StatusBadge.vue`): prop `status`.
- `FeaturesAppsCreateDialog` (`CreateDialog.vue`): props `initiativeId`, `v-model:open`, emits `created(app)` and `uncertain`. Submit blocked while pending; optional name.
- `FeaturesAppsRenameDialog` (`RenameDialog.vue`): props `app`, `v-model:open`, emits `saved(app)` (the PATCH response).
- `FeaturesAppsAddHostDialog` (`AddHostDialog.vue`): props `app`, `v-model:open`, emits `added(deployment)`. The host is trimmed and lowercased on submit, then validated like Go; a polite live preview "Will be added as <normalized>" shows when it differs from the typed value; 422 `reserved` = "This host is reserved for the sign-in service."; 409 = "This host is already used by another app. If you just retried, ..." (adding is idempotent by host, so the 409 may be our own earlier attempt).
- Page `/admin/initiatives/[id]/apps` (`apps/index.vue`; platform admin edits, managers read). The table shows a `FeaturesHealthProbeBadge` per host and a "Health" link per app to `/admin/initiatives/[id]/apps/[appId]/health`. `apps.vue` became `apps/index.vue` so the `apps/` folder does not turn it into a parent route.

### features/health
- `FeaturesHealthAppPanel` (`AppPanel.vue`): props `appId`, `initiativeId`; `/admin/initiatives/[id]/apps/[appId]/health` (platform admin and managers of the initiative). `GET /apps/{id}/health`: summary (app and initiative status, grant count, effective manager grants, checked-at), findings (app-level) and a hosts table. Verdicts and thresholds are the server's (`problems[]`); the client computes none. 404, or an app of another initiative than the route's, shows "App not found". "Refresh" re-reads quietly and announces "Refreshed" in a polite live region (sr-only). Sits under the parent route's h1, so its heading is an h2.
- `FeaturesHealthHostsTable` (`HostsTable.vue`): props `deployments`, `problems`, `caption`. Per host: last seen (relative + absolute, "Never"), last probe (badge + time, "No probe yet") and the findings for that host; a host without findings reads "No findings" (not "healthy").
- `FeaturesHealthProblemList` (`ProblemList.vue`): prop `problems`. Badge with the label and a fixed sentence per code (what it means and what to check, wording from contract.md, generic, no adopter tooling).
- `FeaturesHealthProbeBadge` (`ProbeBadge.vue`): prop `status: ReceivedProbeStatus | null` (Redirects to sign-in / Unprotected / Unexpected redirect / Unreachable (danger) / Not probed; any other value reads "Unknown result", neutral). In the Apps table the probe age (relative `last_probe_at`) sits next to the badge.
- `FeaturesHealthTimestamp` (`Timestamp.vue`): props `value: string | null`, `emptyText`. Relative text over a `<time>` with the locale absolute time.
- `FeaturesHealthOrphanHostsPanel` (`OrphanHostsPanel.vue`) and `FeaturesHealthOrphanHostsTable` (`OrphanHostsTable.vue`, prop `hosts`, `caption`): `/admin/health` (platform admin only; the page does not mount the panel for anyone else). `GET /health/orphan-hosts` (7 days, at most 100, no paging). Explains what an orphan host is and what the count means; hosts are plain text (`break-all`). No "add to app" shortcut (it needs an app picker).
- Nav: "Health" (platform admin only) between Users and Audit.

### features/groups (platform admin only)
- `FeaturesGroupsPanel` (`Panel.vue`): `/admin/groups`. Search box "Search groups" (debounced `?q=`, "No groups match your search." when empty, count announced in a polite live region). Keyset list, "New group", delete behind `UiConfirmDialog` (says groups are global; 409 = "This group still has members or grants. Remove them first."). Managers never reach it (they only list groups in the grant dialog).
- `FeaturesGroupsTable` (`Table.vue`): props `groups`, `caption`, `busyId?`, `emptyText?`; emits `remove(group)`. The key links to `/admin/groups/<id>`.
- `FeaturesGroupsCreateDialog` (`CreateDialog.vue`): `v-model:open`, emits `created(group)`. Key/name validated like Go; 409 = key taken, on the key field.
- `FeaturesGroupsMembersPanel` (`MembersPanel.vue`): prop `groupId`; `/admin/groups/[id]`. Keyset member list, add by email, remove behind `UiConfirmDialog`; loads `GET /groups/{id}` for the h1 (name, else key; "Group" until loaded or if that call fails) and keeps "Group members" as an h2; a 404 on the group or the list = "Group not found".
- `FeaturesGroupsMembersTable` (`MembersTable.vue`): props `members`, `caption`; emits `remove(member)`. Disabled members are listed with a badge.
- `FeaturesGroupsAddMemberDialog` (`AddMemberDialog.vue`): props `groupId`, `v-model:open`, emits `added(member)`. 422 `email` (`invalid`, `no_unique_active_user`) on the field; 409 = already a member.

### features/users (platform admin only)
- `FeaturesUsersPanel` (`Panel.vue`): `/admin/users`. Lookup by exact email (`GET /users?email=` returns a list, at most 50, unpaged: the email is not unique). Disable/Enable behind `UiConfirmDialog` (disabling also ends the user's sessions); 409 on the caller's own row = `SELF_DISABLE_COPY`, otherwise `LAST_ADMIN_COPY`. "Revoke sessions" behind `UiConfirmDialog`; on your own account it warns and, on success, reloads the page (ForwardAuth then redirects to sign in).
- `FeaturesUsersTable` (`Table.vue`): props `users`, `selfId`, `caption`, `busyId?`; emits `setStatus(user)`, `revokeSessions(user)`. "Disable" is hidden for `selfId`; a "You" badge marks the row.
- `FeaturesUsersStatusBadge` (`StatusBadge.vue`): prop `status` (`UserStatus`).

### features/audit (platform admin and managers)
- `FeaturesAuditPanel` (`Panel.vue`): `/admin/audit`. Newest first, keyset "Load more". Initiative filter = native `BaseSelect` over `GET /initiatives` ("All initiatives" / "All my initiatives", "Load more initiatives"); changing it reloads. Without a filter the API scopes a manager to their initiatives; a filter on someone else's initiative is a 404 ("Initiative not found").
- `FeaturesAuditTable` (`Table.vue`): props `events`, `caption`, `initiativeLabels` (id to label; the id is shown when not loaded). Columns when, actor (email; a robot shows its name and "Robot"; "Unknown" when both are null), action, target type/id, initiative, changes.
- `FeaturesAuditChanges` (`Changes.vue`): props `before`, `after` (arbitrary JSON), optional `action`, `occurredAt` (sr-only suffix of "Show changes"). Native `<details>` collapsed by default; the JSON is computed and the `<pre>` rendered only while open (`@toggle`), capped at 64 KB per side with a visible "Truncated" note, text interpolation only (never `v-html`). Audit and Users panels announce "N events" / "N users found" in a polite sr-only live region after (re)load.

### features/tokens (every signed-in user)
- `FeaturesTokensPanel` (`Panel.vue`): props `path` (list + create), `revokeBase` (`DELETE {revokeBase}/{id}`), `offerAdmin`, `canCreate`, `owner` ("you" / "the robot ci"), `expiryOptions?`. Keyset list, "New token", revoke behind `UiConfirmDialog`. Used by `/tokens` (`/me/tokens`) and by the robot detail (`/robots/{id}/tokens`, revoked at `/tokens/{id}`).
- `FeaturesTokensTable` (`Table.vue`): props `tokens`, `caption`; emits `revoke(token)`. Name, scope, state badge (Active / Expired / Revoked), created, expires, last used; Revoke only on live tokens.
- `FeaturesTokensCreateDialog` (`CreateDialog.vue`): props `path`, `offerAdmin`, `owner`, `expiryOptions?`, `v-model:open`; emits `created` (no payload: the secret never leaves the dialog) and `uncertain`. Name, scope (admin only when `offerAdmin`; the API decides: 422 `scope.not_allowed`), expiry list (`EXPIRY_OPTIONS`; 422 `too_large` when over the server cap), 409 = `TOKEN_LIMIT_COPY`. On 201 the dialog switches to the secret view: read-only field whose hint (aria-describedby) says it is shown only once, "Copy secret" (polite status "Secret copied to the clipboard."; a failed copy is a danger `role=alert`). While the secret is shown the dialog is `persistent` (no Escape or outside click): only "Done" or the X close it. The secret exists only in a local ref, wiped when the dialog closes or unmounts; never in a store, storage, URL or log. Creation is not idempotent: after an ambiguous failure (network, 503, 5xx) the dialog closes and emits `uncertain`; the panel reloads the list and shows "The result is unknown. Check the list before creating another token.". Definitive 4xx stay in the form.
- Page `/tokens` (default layout; linked from the home as "API tokens"): the caller's own tokens; the admin scope is offered when `/me` says platform admin or manager.

### features/robots (platform admin only)
- `FeaturesRobotsPanel` (`Panel.vue`): `/admin/robots`. Keyset list (a create with an unknown result reloads it and shows an alert), "New robot", Edit, Disable / Enable behind `UiConfirmDialog` (disabling says it revokes all tokens and enabling does not restore them). Also mounts `FeaturesRobotsRevokeSecret`.
- `FeaturesRobotsTable` (`Table.vue`): props `robots`, `selfId`, `caption`, `busyId?`; emits `edit(robot)`, `setStatus(robot)`. The owner column reads "You", the owner's email (name as fallback) or "None". "Tokens" links to `/admin/robots/<id>`.
- `FeaturesRobotsFormDialog` (`FormDialog.vue`): props `robot` (null = create), `v-model:open`; emits `saved(robot)` and `uncertain` (create only, after an ambiguous failure). Name; owner = keep / set by email / none. The email is resolved with `GET /users?email=` to exactly one active person, sent as `owner_user_id` (`""` clears). Edit sends only changed fields. 409 = name taken.
- `FeaturesRobotsDetailPanel` (`DetailPanel.vue`): prop `robotId`; `/admin/robots/[id]`. `GET /robots/{id}`; 404 = "Robot not found", 403 = load error. Disabled robot: no "New token". Admin scope always offered (API refuses with 422).
- `FeaturesRobotsRevokeSecret` (`RevokeSecret.vue`): `POST /tokens/revoke {token}`; the answer is 204 for any well-formed value, so the copy never confirms existence; 422 = malformed. The field is cleared after every submit (retryable errors say "Paste the token again.") and the value is never echoed. Plain text on purpose: a password manager would offer to save a leaked secret.
- Nav: "Robots" (platform admin only) between Users and Health.
- Grants: the create dialog has a "Robot" subject (`robot_id`, native select over the active robots of `GET /robots`, which managers may list). A robot grant arrives as `user` with an empty `email`: tables use `subjectLabel()` (email, else name) with a muted "Robot" line; the revoke confirmation reads "robot <name>". Audit: the actor column shows `actor_email`, else `actor_name` plus "Robot", else "Unknown". A token-made event shows a muted "via token <first 8 chars of token_id>" line (no link).

## TODO (priority order)

1. Menu (Reka UI), only if a screen needs it; audit its source for `<style>` first.
