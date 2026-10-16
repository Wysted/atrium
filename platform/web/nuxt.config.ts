import tailwindcss from '@tailwindcss/vite'

// Strict CSP (script-src 'self' 'sha256-...'; style-src 'self'): nothing inline may be added here.
export default defineNuxtConfig({
  compatibilityDate: '2026-10-01',
  ssr: false,
  devtools: { enabled: false },
  telemetry: false,
  modules: ['@nuxt/eslint'],
  css: ['~/assets/css/main.css'],
  vite: {
    plugins: [tailwindcss()],
  },
  experimental: {
    entryImportMap: false,
  },
  app: {
    head: {
      htmlAttrs: { lang: 'en' },
      charset: 'utf-8',
      viewport: 'width=device-width, initial-scale=1',
      title: 'Portal',
    },
  },
})
