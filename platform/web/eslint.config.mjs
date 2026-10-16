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
)
