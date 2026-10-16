# Component catalog

Source of truth for the portal UI components. Read it before creating or changing a component and update it in the same change.

## Rules

1. Layers
   - `app/components/base/` (prefix `Base`): own primitives, no third-party UI. Layout, text, button, link, icon.
   - `app/components/ui/`: composed widgets built on Reka UI (Dialog, Menu, Select). TODO.
   - `app/components/features/<domain>/`: domain components that know about the API types. TODO.
2. Tokens only. Styling comes from `app/assets/css/tokens.css` through semantic utilities (`bg-surface`, `text-text-muted`, `border-border`, `bg-primary`, `rounded-md`, `shadow-sm`, `p-4`). No raw palette values, no arbitrary values (`bg-[#fff]`, `p-[13px]`). The one allowed exception: an arbitrary value that only references declared tokens through `var(--...)` (e.g. `BaseGrid` columns using `--container-grid-*`); never rely on Tailwind's default theme variables. An adopter re-themes by overriding that one file (colors, radius, type scale, shadows; the dark theme reuses the same names).
3. Pages compose, they do not markup. `app/pages/**` may not use raw HTML elements (div, span, p, h1-h6, a, button, input, select, textarea, table, ul, ol, li, img, form, label, section, header, footer, nav, main); `vue/no-restricted-html-elements` fails lint. Layouts and components may use them.
4. Tailwind classes must be literal strings. Props map to classes through lookup tables, never by string concatenation (`'gap-' + n` is not detected by the scanner).
5. CSP (`script-src 'self' 'sha256-...'; style-src 'self'`):
   - no `<script>`/`<style>` inline, no `useHead` with inline scripts/styles, no `style=` attributes in markup;
   - `:style` bindings are allowed (CSSOM) but prefer token classes;
   - `v-html` is banned (`vue/no-v-html` error);
   - no CDN, web fonts or external origins; icons are inline SVG without `<style>`;
   - any Reka UI component must be verified against `style-src 'self'` before adoption.
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
- Props: `to?: string` (internal route, NuxtLink), `href?: string` (plain `<a>`; must be a trusted URL, never user-controlled, until a safe-href helper exists), `target?: '_self'|'_blank'` (`_blank` adds `rel="noopener noreferrer"`), `type?: 'button'|'submit'|'reset' = 'button'`, `variant?: 'primary'|'secondary'|'ghost'|'danger' = 'primary'`, `size?: 'sm'|'md'|'lg' = 'md'`, `disabled?: boolean`, `loading?: boolean`.
- Slots: default (text, optionally with a `BaseIcon`).
- Events: `click` (declared emit, so a parent `@click` never reaches the root element directly); emitted only while active, never when `disabled` or `loading`.
- Example: `<BaseButton variant="danger" type="submit" :loading="saving">Delete</BaseButton>`
- A11y: renders `<button type="button">` by default (never an accidental submit). `loading` shows a decorative spinning `loader` icon (`motion-safe:animate-spin`), sets `aria-busy` and blocks activation (the `<button>` keeps native focus and uses `aria-disabled` plus a click handler that prevents default, so it will not submit forms); hover styles apply only while actionable; the `disabled` prop uses the native attribute on buttons. Disabled/loading links render without `href` plus `aria-disabled`. Icon-only buttons need an `aria-label` on the button.

### BaseLink
- Purpose: inline text link.
- Props: `to?: string` (internal, NuxtLink), `href?: string` (external, `<a>`; must be a trusted URL, never user-controlled, until a safe-href helper exists), `target?: '_self'|'_blank'` (`_blank` adds `rel="noopener noreferrer"`).
- Slots: default.
- Example: `<BaseLink href="/docs/" target="_blank">Docs</BaseLink>`
- A11y: link text must make sense out of context; announce new-tab behavior in the text when it matters.

### BaseIcon
- Purpose: inline SVG from a curated set (`check`, `loader`, `x`, `plus`, `minus`, `chevron-down`, `chevron-right`, `external-link`, `alert-triangle`, `info`, `search`). Add a name in `app/types/ui.ts` and the path table in the component.
- Props: `name: IconName` (required), `label?: string`, `size?: 'sm'|'md'|'lg' = 'md'`. Color follows `currentColor`.
- Slots: none.
- Example: `<BaseIcon name="alert-triangle" label="Warning" />` (meaningful) or `<BaseIcon name="check" />` (decorative).
- A11y: without `label` the icon is `aria-hidden`; with `label` it is `role="img"` and `aria-label`. An icon that is the only content of a control must carry the label. In dev (`import.meta.dev`) it warns on an unknown `name`, an empty `label`, and a `role="img"`/`aria-label` attribute without the `label` prop.

## Error page

### app/error.vue
- Purpose: the app-wide error page (client-side 404 and runtime errors). Required under the strict CSP: Nuxt's default error page injects an inline `<style>` and `<script>`, both blocked, leaving the page unstyled.
- Props: `error: NuxtError` (supplied by Nuxt).
- Composition: `NuxtLayout` + `BaseStack`, `BaseText`, `BaseHeading`, `BaseCluster`, `BaseButton` only. Title via `useHead({ title })`.
- Copy: 404 shows "Page not found"; any other status shows "Something went wrong". Only the status code and fixed copy are shown; `error.message` is rendered in dev only (`import.meta.dev`), never the stack.
- Action: "Go to home" calls `clearError({ redirect: '/' })`.
- Note: `NUXT_E1005` ("Error caught during app initialization") in the console on a direct visit to an unknown route is Nuxt's own `app:error` diagnostic for the 404 raised during startup; it is expected and not a CSP issue.

## TODO (priority order)

1. Card, Badge, Input, Field (label + hint + error), Select. TODO
2. Table, EmptyState, PageHeader, Dialog (Reka UI), Menu (Reka UI). TODO
3. Features (`app/components/features/<domain>/`), driven by the API types from `platform/docs/contract.md`. TODO
