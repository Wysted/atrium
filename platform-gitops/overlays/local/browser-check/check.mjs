// Real-browser smoke of the portal web app through Traefik (run by browser-check.sh inside the pinned Playwright image).
// Output lines are "  PASS: ..." / "  FAIL: ..." (the style of smoke.sh); exit code 1 on any FAIL.
import { chromium } from 'playwright'

const BASE = process.env.PORTAL_URL ?? 'https://portal.apps.wysted.dev'
const AUTH_HOST = process.env.AUTH_HOST ?? 'auth.apps.wysted.dev'
const OUT = process.env.OUT_DIR ?? 'out'
// Fake users of the local seed (overlays/local/seed.sql): alice = viewer on hello-a (demo) and hello-b (other), not an admin;
// admin = platform admin; carol = exists, no grants (no apps, not an admin).
const ALICE = { email: 'alice@example.com', name: 'alice', apps: [{ name: 'hello-a', initiative: 'Demo', host: 'hello-a.apps.wysted.dev' }, { name: 'hello-b', initiative: 'Other', host: 'hello-b.apps.wysted.dev' }] }
const ADMIN = { email: 'admin@example.com', name: 'admin' }
const CAROL = { email: 'carol@example.com', name: 'carol' }
const DATA_TIMEOUT = 15_000
const NOT_FOUND_TEXT = 'Page not found' // app/error.vue for a client route without a page

// Console errors tolerated, by substring. NUXT_E1005 ("Error caught during app initialization") is logged by Nuxt when
// the 404 of a route without a page is raised while the app is still starting (deep link): it is the error page being
// shown, not a defect. Anything else is a failure.
const TOLERATED_CONSOLE = ['NUXT_E1005']

let fails = 0
const pass = (m) => console.log(`  PASS: ${m}`)
const fail = (m) => { fails++; console.log(`  FAIL: ${m}`) }
const check = (label, ok, detail = '') => (ok ? pass(label) : fail(detail ? `${label}: ${detail}` : label))

const browser = await chromium.launch()
let ctx, page
let consoleErrors = []
let failedRequests = []
// A fresh browser context (own cookies) per user; the CSP listener is installed before any page script.
async function newSession() {
  await ctx?.close()
  ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  await ctx.addInitScript(() => {
    window.__csp = []
    document.addEventListener('securitypolicyviolation', (e) => {
      const noQuery = (u) => (u || '').split(/[?#]/)[0]
      window.__csp.push(`${e.violatedDirective} blocked=${noQuery(e.blockedURI) || 'inline'} source=${noQuery(e.sourceFile) || '-'}:${e.lineNumber}`)
    })
  })
  page = await ctx.newPage()
  reset()
  page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()) })
  page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`))
  page.on('requestfailed', (r) => failedRequests.push(`${stripQuery(r.url())} ${r.failure()?.errorText}`))
  // An HTTP error on a request of the portal host counts as a failed request too (the document itself answers 200).
  page.on('response', (r) => {
    if (r.url().startsWith(BASE) && r.status() >= 400) failedRequests.push(`${stripQuery(r.url())} HTTP ${r.status()}`)
  })
}
const stripQuery = (u) => u.split(/[?#]/)[0] // query strings may carry codes/state: never print them
const reset = () => { consoleErrors = []; failedRequests = [] }
const violations = () => page.evaluate(() => window.__csp ?? [])
const bodyText = async () => (await page.locator('body').innerText()).trim()

// The stylesheet applied: html resolves to the design tokens (the UA defaults are a serif font and no token colours).
async function checkStyled(label) {
  const r = await page.evaluate(() => {
    const cs = getComputedStyle(document.documentElement)
    const probe = document.createElement('div')
    probe.style.backgroundColor = 'var(--color-surface)'
    document.body.appendChild(probe)
    const surface = getComputedStyle(probe).backgroundColor
    probe.remove()
    return {
      font: cs.fontFamily.replace(/\s+/g, ' '),
      fontToken: cs.getPropertyValue('--font-sans').trim().replace(/\s+/g, ' '),
      bg: cs.backgroundColor,
      surface,
    }
  })
  check(`${label}: font-family resolves to the --font-sans token`, r.fontToken !== '' && r.font === r.fontToken, JSON.stringify(r))
  check(`${label}: background resolves to the --color-surface token`, r.surface !== 'rgba(0, 0, 0, 0)' && r.bg === r.surface, JSON.stringify(r))
}

async function report(label, { tolerated = [] } = {}) {
  const v = await violations()
  check(`${label}: 0 securitypolicyviolation events`, v.length === 0, JSON.stringify(v))
  check(`${label}: 0 failed requests`, failedRequests.length === 0, JSON.stringify(failedRequests))
  const unexpected = consoleErrors.filter((e) => !tolerated.some((t) => e.includes(t)))
  const ignored = consoleErrors.length - unexpected.length
  check(`${label}: 0 console errors${ignored ? ` (${ignored} tolerated: ${tolerated.join(', ')})` : ''}`, unexpected.length === 0, JSON.stringify(unexpected))
}

async function login(email) {
  await newSession()
  await page.goto(`${BASE}/`)
  check(`${email}: unauthenticated navigation lands on the auth host login form`, new URL(page.url()).host === AUTH_HOST, page.url().split('?')[0])
  await page.fill('input[name=email]', email)
  await Promise.all([page.waitForURL(`${BASE}/**`, { timeout: 30_000 }), page.click('button[type=submit], input[type=submit]')])
  check(`${email}: login ends on the portal host`, new URL(page.url()).origin === BASE, page.url().split('?')[0])
}

// Loads / and waits for the data, not for the network: the greeting (from /me) and the cards or the empty state (from /me/apps).
async function openHome(user, label) {
  reset()
  await page.goto(`${BASE}/`)
  await page.getByRole('heading', { level: 1, name: `Welcome, ${user.name}` }).waitFor({ timeout: DATA_TIMEOUT })
  await page.locator('ul > li h2, :text("No apps yet")').first().waitFor({ timeout: DATA_TIMEOUT })
  check(`${label}: greeting "Welcome, ${user.name}"`, true)
}

const adminButton = () => page.getByRole('link', { name: 'Administration', exact: true })

try {
  console.log('  -- alice: home with her apps')
  await login(ALICE.email)
  await openHome(ALICE, '/ (alice)')
  const text = await bodyText()
  check('/ (alice): the placeholder text is gone', !text.includes('Initiatives portal') && !text.includes('being set up'), JSON.stringify(text.slice(0, 200)))
  for (const app of ALICE.apps) {
    const card = page.locator('li', { has: page.getByRole('heading', { level: 2, name: app.name, exact: true }) })
    check(`/ (alice): card "${app.name}" present once`, (await card.count()) === 1, String(await card.count()))
    check(`/ (alice): card "${app.name}" shows initiative "${app.initiative}"`, (await card.innerText()).includes(app.initiative))
    const href = await card.getByRole('link', { name: app.host }).getAttribute('href')
    check(`/ (alice): card "${app.name}" links to https://${app.host}/`, href === `https://${app.host}/`, String(href))
    check(`/ (alice): card "${app.name}" shows role badge "viewer"`, (await card.innerText()).includes('viewer'))
  }
  check('/ (alice): exactly her 2 apps (no portal card)', (await page.getByRole('heading', { level: 2 }).count()) === ALICE.apps.length, String(await page.getByRole('heading', { level: 2 }).count()))
  check('/ (alice): no "No apps yet"', !(await bodyText()).includes('No apps yet'))
  check('/ (alice): no Administration button (not an admin, manages nothing)', (await adminButton().count()) === 0)
  await page.screenshot({ path: `${OUT}/home.png`, fullPage: true })
  await checkStyled('/ (alice)')
  await report('/ (alice)')

  console.log('  -- a client route without a page renders the error page, styled, under the CSP')
  reset()
  await page.goto(`${BASE}/admin/groups/x`)
  await page.waitForLoadState('networkidle')
  const nf = await bodyText()
  check(`/admin/groups/x renders "${NOT_FOUND_TEXT}"`, nf.includes(NOT_FOUND_TEXT), JSON.stringify(nf.slice(0, 200)))
  await page.screenshot({ path: `${OUT}/not-found.png`, fullPage: true })
  await checkStyled('/admin/groups/x')
  await report('/admin/groups/x', { tolerated: TOLERATED_CONSOLE })

  console.log('  -- carol: no grants -> empty state')
  await login(CAROL.email)
  await openHome(CAROL, '/ (carol)')
  check('/ (carol): empty state "No apps yet"', (await bodyText()).includes('No apps yet'))
  check('/ (carol): no app cards', (await page.locator('li h2').count()) === 0, String(await page.locator('li h2').count()))
  check('/ (carol): no Administration button', (await adminButton().count()) === 0)
  await page.screenshot({ path: `${OUT}/home-empty.png`, fullPage: true })
  await report('/ (carol)')

  console.log('  -- admin: Administration button -> /admin (NuxtLink falls back to a document load: no page matches the route, so E1005 is tolerated here)')
  await login(ADMIN.email)
  await openHome(ADMIN, '/ (admin)')
  await adminButton().waitFor({ timeout: DATA_TIMEOUT })
  check('/ (admin): Administration button visible (platform admin)', true)
  await report('/ (admin)')
  reset()
  const documentRequests = []
  page.on('request', (r) => { if (r.resourceType() === 'document') documentRequests.push(new URL(r.url()).pathname) })
  await adminButton().click()
  await page.getByText(NOT_FOUND_TEXT).first().waitFor({ timeout: DATA_TIMEOUT })
  console.log(`  INFO: document requests after the click: ${JSON.stringify(documentRequests)}`)
  check('Administration click lands on /admin', new URL(page.url()).pathname === '/admin', new URL(page.url()).pathname)
  check(`/admin renders "${NOT_FOUND_TEXT}"`, (await bodyText()).includes(NOT_FOUND_TEXT))
  await page.screenshot({ path: `${OUT}/admin-click.png`, fullPage: true })
  await report('/admin (after the click)', { tolerated: TOLERATED_CONSOLE })
} catch (e) {
  fail(`browser scenario aborted: ${e.message.split('\n')[0]} (url ${page.url().split('?')[0]})`)
  await page.screenshot({ path: `${OUT}/aborted.png` }).catch(() => {})
} finally {
  await browser.close()
}

if (fails > 0) { console.log(`  browser check: ${fails} failure(s); screenshots in out/`); process.exit(1) }
console.log(`  browser check: all scenarios passed; screenshots in out/`)
