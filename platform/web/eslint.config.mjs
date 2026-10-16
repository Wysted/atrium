// @ts-check
import withNuxt from './.nuxt/eslint.config.mjs'

const restrictedElements = [
  'div', 'span', 'p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'a', 'button', 'input', 'select',
  'textarea', 'table', 'ul', 'ol', 'li', 'img', 'form', 'label', 'section', 'header', 'footer',
  'nav', 'main',
].map(element => ({
  element,
  message: `Raw <${element}> is not allowed in pages. Compose base/ui/features components (see platform/web/COMPONENTS.md).`,
}))

// reka-ui is not auto-imported (no Nuxt module registers it), so explicit imports are the only way in and this
// rule catches every use. These families inject focus guards, <style> or inline styles that were not audited
// against style-src 'self'. Audit one (see the Reka section of COMPONENTS.md), then lift its pattern here.
const rekaMessage = 'Unaudited Reka UI family under the strict CSP (style-src \'self\'). Audit it first and see platform/web/COMPONENTS.md; for a select use the native <select> in base/.'
const rekaBanned = '^(Select|Combobox|ScrollArea|Splitter|Viewport|Menu|Popover|DropdownMenu|ContextMenu|Tooltip|HoverCard)'

export default withNuxt(
  {
    // v-html is banned everywhere (CSP and XSS).
    files: ['**/*.vue'],
    rules: {
      'vue/no-v-html': 'error',
    },
  },
  {
    // Pages compose components; they never use raw HTML elements.
    files: ['app/pages/**/*.vue'],
    rules: {
      'vue/no-restricted-html-elements': ['error', ...restrictedElements],
    },
  },
  {
    // Unaudited Reka UI components are banned until checked against the CSP.
    files: ['app/**/*.{vue,ts}'],
    rules: {
      'no-restricted-imports': ['error', {
        patterns: [
          { group: ['reka-ui'], importNamePattern: rekaBanned, message: rekaMessage },
          { regex: '^reka-ui/', message: rekaMessage },
        ],
      }],
    },
  },
)
