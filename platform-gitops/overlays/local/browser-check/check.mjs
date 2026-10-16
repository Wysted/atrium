// Real-browser smoke of the portal web app through Traefik (run by browser-check.sh inside the pinned Playwright image).
// Output lines are "  PASS: ..." / "  FAIL: ..." (the style of smoke.sh); exit code 1 on any FAIL.
import { chromium } from 'playwright'

const BASE = process.env.PORTAL_URL ?? 'https://portal.apps.wysted.dev'
const AUTH_HOST = process.env.AUTH_HOST ?? 'auth.apps.wysted.dev'
const OUT = process.env.OUT_DIR ?? 'out'
// Fake users of the local seed (overlays/local/seed.sql; dave is below): alice = viewer on hello-a (demo) and hello-b (other), not an admin;
// admin = platform admin; carol = exists, no grants (no apps, not an admin).
const ALICE = { email: 'alice@example.com', name: 'alice', apps: [{ name: 'hello-a', initiative: 'Demo', host: 'hello-a.apps.wysted.dev' }, { name: 'hello-b', initiative: 'Other', host: 'hello-b.apps.wysted.dev' }] }
const ADMIN = { email: 'admin@example.com', name: 'admin' }
const CAROL = { email: 'carol@example.com', name: 'carol' }
const DATA_TIMEOUT = 15_000
const NO_ACCESS_TEXT = 'You do not have access to administration'
// Initiatives of the local seed (seed.sql): key -> name. The rename round trip uses demo and restores its name.
const SEED_INITIATIVES = [{ key: 'platform', name: 'Platform' }, { key: 'demo', name: 'Demo' }, { key: 'other', name: 'Other' }]
const RENAMED = SEED_INITIATIVES[1]
const MARKER = 'Demo (browser check)'
// dave = manager of demo only (role "manager" with manages_access); not a platform admin; his only app is hello-a.
const DAVE = { email: 'dave@example.com', name: 'dave' }
const NOT_FOUND_TEXT = 'Page not found' // app/error.vue for a client route without a page

// Console errors tolerated, by substring, ONLY in the unknown-route scenario. NUXT_E1005 ("Error caught during app
// initialization") is logged by Nuxt when the 404 of a route without a page is raised while the app is still starting
// (deep link): it is the error page being shown, not a defect. Anything else is a failure.
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

// tolerated: console substrings; toleratedRequests: exact "<path> HTTP <status>" entries (only the scenario that
// provokes the API 404 on purpose passes them; the browser also logs it as a "Failed to load resource" console error).
async function report(label, { tolerated = [], toleratedRequests = [] } = {}) {
  const v = await violations()
  check(`${label}: 0 securitypolicyviolation events`, v.length === 0, JSON.stringify(v))
  const badRequests = failedRequests.filter((r) => !toleratedRequests.includes(r))
  check(`${label}: 0 failed requests${failedRequests.length > badRequests.length ? ` (${failedRequests.length - badRequests.length} tolerated: ${toleratedRequests.join(', ')})` : ''}`, badRequests.length === 0, JSON.stringify(badRequests))
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

  console.log('  -- alice (not an admin, manages nothing): direct visit to /admin')
  await login(ALICE.email)
  reset()
  await page.goto(`${BASE}/admin`)
  await page.getByText(NO_ACCESS_TEXT).first().waitFor({ timeout: DATA_TIMEOUT })
  check(`/admin (alice): "${NO_ACCESS_TEXT}"`, true)
  check('/admin (alice): link back home', (await page.getByRole('link', { name: 'Go to home' }).getAttribute('href')) === '/')
  check('/admin (alice): no "New initiative" control', (await page.getByRole('button', { name: 'New initiative' }).count()) === 0)
  await page.screenshot({ path: `${OUT}/admin-no-access.png`, fullPage: true })
  await checkStyled('/admin (alice)')
  await report('/admin (alice)')

  console.log('  -- admin: Administration button -> /admin/initiatives (client-side navigation)')
  await login(ADMIN.email)
  await openHome(ADMIN, '/ (admin)')
  await adminButton().waitFor({ timeout: DATA_TIMEOUT })
  check('/ (admin): Administration button visible (platform admin)', true)
  await report('/ (admin)')
  reset()
  const documentRequests = []
  page.on('request', (r) => { if (r.resourceType() === 'document') documentRequests.push(new URL(r.url()).pathname) })
  await page.evaluate(() => { window.__spa = 'alive' }) // lost on any document load
  await adminButton().click()
  await page.waitForURL(`${BASE}/admin/initiatives`, { timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 1, name: 'Initiatives' }).waitFor({ timeout: DATA_TIMEOUT })
  check('Administration click: no document request (client-side)', documentRequests.length === 0, JSON.stringify(documentRequests))
  check('Administration click: the JS context survived (no reload)', (await page.evaluate(() => window.__spa)) === 'alive')
  check('Administration click lands on /admin/initiatives (via the /admin redirect)', new URL(page.url()).pathname === '/admin/initiatives', new URL(page.url()).pathname)

  const table = page.getByRole('table')
  await table.getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const otherHref = await table.getByRole('link', { name: 'Other', exact: true }).getAttribute('href') // /admin/initiatives/<id>, for dave's out-of-scope visit
  for (const i of SEED_INITIATIVES) {
    const row = table.getByRole('row').filter({ has: page.locator('code', { hasText: new RegExp(`^${i.key}$`) }) })
    check(`list: initiative "${i.key}" present once`, (await row.count()) === 1, String(await row.count()))
    if (await row.count() === 1) {
      const t = await row.innerText()
      check(`list: "${i.key}" shows name "${i.name}" and status Active`, t.includes(i.name) && t.includes('Active'), JSON.stringify(t))
    }
  }
  await page.screenshot({ path: `${OUT}/admin-list.png`, fullPage: true })
  await checkStyled('/admin/initiatives')
  await report('/admin/initiatives')

  console.log('  -- admin: "New initiative" dialog (Reka), no mutation')
  reset()
  const mutations = []
  page.on('request', (r) => { if (r.method() !== 'GET' && r.method() !== 'HEAD') mutations.push(`${r.method()} ${new URL(r.url()).pathname}`) })
  const trigger = page.getByRole('button', { name: 'New initiative' })
  await trigger.click()
  const dialog = page.getByRole('dialog')
  await dialog.waitFor({ timeout: DATA_TIMEOUT })
  check('create dialog: role=dialog visible', await dialog.isVisible())
  check('create dialog: focus is inside', await page.evaluate(() => !!document.activeElement?.closest('[role=dialog]')))
  check('create dialog: initial focus is the Key input (not Close)', await dialog.getByRole('textbox', { name: /^Key/ }).evaluate((el) => el === document.activeElement))
  const bodyLock = await page.evaluate(() => ({ overflow: document.body.style.overflow, hidden: [...document.body.children].filter((e) => e.getAttribute('aria-hidden') === 'true').length }))
  check('create dialog: Reka scroll lock and aria-hidden on siblings applied (CSSOM)', bodyLock.overflow === 'hidden' && bodyLock.hidden > 0, JSON.stringify(bodyLock))
  await page.screenshot({ path: `${OUT}/admin-create-dialog.png`, fullPage: true })
  await report('create dialog (open)')
  await page.keyboard.press('Escape')
  await dialog.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('create dialog: Escape closes it', true)
  check('create dialog: focus returns to the trigger', await trigger.evaluate((el) => el === document.activeElement))
  check('create dialog: scroll lock released', await page.evaluate(() => document.body.style.overflow !== 'hidden'))
  check('create dialog: no mutation request', mutations.length === 0, JSON.stringify(mutations))
  await report('create dialog (closed)')

  console.log(`  -- admin: detail of "${RENAMED.key}": rename round trip, deactivate dialog cancelled`)
  reset()
  await table.getByRole('link', { name: RENAMED.name, exact: true }).click()
  await page.getByRole('heading', { level: 1, name: RENAMED.name, exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  check('detail: client-side navigation from the list', documentRequests.length === 0, JSON.stringify(documentRequests))
  const keyInput = page.getByLabel('Key', { exact: true })
  check(`detail: key "${RENAMED.key}" is read-only`, (await keyInput.inputValue()) === RENAMED.key && (await keyInput.getAttribute('readonly')) !== null)
  await page.screenshot({ path: `${OUT}/admin-detail.png`, fullPage: true })
  const nameInput = page.getByLabel('Name', { exact: true })
  let renamed = false
  try {
    await nameInput.fill(MARKER)
    await page.getByRole('button', { name: 'Save name' }).click()
    renamed = true
    await page.getByRole('heading', { level: 1, name: MARKER, exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check(`rename: the new name "${MARKER}" is shown`, true)
    check('rename: PATCH sent', mutations.some((m) => m.startsWith('PATCH ')), JSON.stringify(mutations))
  } finally {
    if (renamed) {
      await nameInput.fill(RENAMED.name)
      await page.getByRole('button', { name: 'Save name' }).click()
      await page.getByRole('heading', { level: 1, name: RENAMED.name, exact: true }).waitFor({ timeout: DATA_TIMEOUT })
      check(`rename: original name "${RENAMED.name}" restored`, true)
    }
  }
  mutations.length = 0
  const deactivate = page.getByRole('button', { name: 'Deactivate initiative' })
  await deactivate.click()
  const confirmDlg = page.getByRole('alertdialog')
  await confirmDlg.waitFor({ timeout: DATA_TIMEOUT })
  check('deactivate: confirm dialog (alertdialog) visible', await confirmDlg.isVisible())
  check('deactivate: focus inside the dialog', await page.evaluate(() => !!document.activeElement?.closest('[role=alertdialog]')))
  await page.screenshot({ path: `${OUT}/admin-confirm-dialog.png`, fullPage: true })
  await report('detail (confirm dialog open)')
  await confirmDlg.getByRole('button', { name: 'Cancel' }).click()
  await confirmDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('deactivate: Cancel closes it, focus returns to the trigger', await deactivate.evaluate((el) => el === document.activeElement))
  check('deactivate: no mutation request after Cancel', mutations.length === 0, JSON.stringify(mutations))
  check('deactivate: status still Active', (await bodyText()).includes('Active'))
  await report(`detail "${RENAMED.key}"`)

  console.log('  -- admin: the platform initiative cannot be deactivated')
  reset()
  await page.getByRole('link', { name: 'Back to initiatives' }).click()
  await table.getByRole('link', { name: 'Platform', exact: true }).click()
  await page.getByRole('heading', { level: 1, name: 'Platform', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByText('The platform initiative cannot be deactivated').waitFor({ timeout: DATA_TIMEOUT })
  check('platform detail: no Deactivate action', (await page.getByRole('button', { name: /Deactivate/ }).count()) === 0)
  await report('platform detail')

  console.log('  -- dave (manager of demo only): Administration, scoped read-only views')
  await login(DAVE.email)
  await openHome(DAVE, '/ (dave)')
  await adminButton().waitFor({ timeout: DATA_TIMEOUT })
  check('/ (dave): Administration button visible (manages demo)', true)
  check('/ (dave): exactly his app hello-a (no hello-b, no portal)', (await page.getByRole('heading', { level: 2 }).count()) === 1 && (await page.getByRole('heading', { level: 2, name: 'hello-a', exact: true }).count()) === 1)
  await report('/ (dave)')
  reset()
  await adminButton().click()
  await page.waitForURL(`${BASE}/admin/initiatives`, { timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 1, name: 'Initiatives' }).waitFor({ timeout: DATA_TIMEOUT })
  const dTable = page.getByRole('table')
  await dTable.getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  check('dave list: exactly one data row', (await dTable.getByRole('row').count()) === 2, String(await dTable.getByRole('row').count()))
  check('dave list: it is demo', (await dTable.getByRole('row').nth(1).locator('code').innerText()) === 'demo')
  for (const k of ['platform', 'other']) check(`dave list: "${k}" absent`, (await dTable.locator('code', { hasText: new RegExp(`^${k}$`) }).count()) === 0)
  check('dave list: no "New initiative" button', (await page.getByRole('button', { name: 'New initiative' }).count()) === 0)
  await page.screenshot({ path: `${OUT}/manager-list.png`, fullPage: true })
  await report('/admin/initiatives (dave)')
  reset()
  await dTable.getByRole('link', { name: 'Demo', exact: true }).click()
  await page.getByRole('heading', { level: 1, name: 'Demo', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  const dKey = page.getByLabel('Key', { exact: true })
  const dName = page.getByLabel('Name', { exact: true })
  check('dave detail: key "demo" read-only', (await dKey.inputValue()) === 'demo' && (await dKey.getAttribute('readonly')) !== null)
  check('dave detail: name "Demo" read-only', (await dName.inputValue()) === 'Demo' && (await dName.getAttribute('readonly')) !== null)
  check('dave detail: no "Save name" (no rename form)', (await page.getByRole('button', { name: 'Save name' }).count()) === 0)
  check('dave detail: no Deactivate/Activate action', (await page.getByRole('button', { name: /Deactivate|Activate/ }).count()) === 0)
  check('dave detail: status Active shown', (await bodyText()).includes('Active'))
  await page.screenshot({ path: `${OUT}/manager-detail.png`, fullPage: true })
  await report('detail demo (dave)')

  console.log("  -- dave: direct visit to another initiative's id -> \"Initiative not found\" (API 404)")
  const otherId = (otherHref ?? '').split('/').pop()
  check('other initiative id captured from the admin session', !!otherId, String(otherHref))
  reset()
  await page.goto(`${BASE}/admin/initiatives/${otherId}`)
  await page.getByText('Initiative not found').first().waitFor({ timeout: DATA_TIMEOUT })
  check('dave -> other: "Initiative not found"', true)
  check('dave -> other: the API answered 404', failedRequests.some((r) => r === `${BASE}/api/v1/initiatives/${otherId} HTTP 404`), JSON.stringify(failedRequests))
  await page.screenshot({ path: `${OUT}/manager-not-found.png`, fullPage: true })
  await checkStyled('/admin/initiatives/<other> (dave)')
  await report('/admin/initiatives/<other> (dave)', { tolerated: ['status of 404'], toleratedRequests: [`${BASE}/api/v1/initiatives/${otherId} HTTP 404`] })
} catch (e) {
  fail(`browser scenario aborted: ${e.message.split('\n')[0]} (url ${page.url().split('?')[0]})`)
  await page.screenshot({ path: `${OUT}/aborted.png` }).catch(() => {})
} finally {
  await browser.close()
}

if (fails > 0) { console.log(`  browser check: ${fails} failure(s); screenshots in out/`); process.exit(1) }
console.log(`  browser check: all scenarios passed; screenshots in out/`)
