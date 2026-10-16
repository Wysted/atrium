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
const SMOKE_ROLE = 'smoke-role' // created and deleted on demo by the roles scenario
const LAST_MANAGER_COPY = 'This is the last active manager of the initiative. Grant another manager first.'
const SMOKE_HOST_INPUT = 'Smoke-Host.apps.wysted.dev' // mixed case on purpose: the UI trims and lowercases it
const SMOKE_HOST = 'smoke-host.apps.wysted.dev'
// Fixed copy of the health findings, kept here on purpose (an independent expectation, not imported from the web app): label + explanation.
const PROBLEM_COPY = {
  app_disabled: ['App disabled', 'The app is disabled, so access is denied to everyone. A platform administrator can enable it.'],
  initiative_inactive: ['Initiative inactive', 'The initiative is inactive, so access to all its apps is denied to everyone. A platform administrator can activate it.'],
  no_grants: ['No grants', 'Nobody holds an unexpired grant in this initiative, so no one can reach the app. An initiative manager or a platform administrator can grant access to a user or a group.'],
  no_manager: ['No manager', 'No active user manages access in this initiative, directly or through a group. An initiative manager or a platform administrator can grant a role that manages access.'],
  unprotected: ['Unprotected', 'The last probe was served without a redirect to sign in, so the host may be reachable without ForwardAuth. Check that the route for this host uses the ForwardAuth middleware. If the host was never seen either, the route may be missing or wrong.'],
  unexpected_redirect: ['Unexpected redirect', 'The last probe was redirected somewhere other than the sign-in page (for example a permanent redirect or another host). Check the redirects configured for this host.'],
  unreachable: ['Unreachable', 'The last probe could not connect to the host (DNS, TCP, TLS or timeout). Check that the host resolves, its certificate is valid and the route is up.'],
  never_probed: ['Not probed yet', 'The host has not been probed yet. Probes run periodically: check again after the next round.'],
  probe_stale: ['Probe result is old', 'The last probe result is older than three probe intervals, so probe rounds are not completing. A platform administrator can check the portal probe.'],
  probe_disabled: ['Probe disabled', 'The portal does not probe hosts, so protection by ForwardAuth is not being checked. Any probe result shown may be old.'],
  never_seen: ['Never seen', 'No authorized request has ever reached this host through ForwardAuth. Check that the route sends traffic through the ForwardAuth middleware and that the host is the one users open.'],
  not_seen_recently: ['Not seen recently', 'No authorized request in the last 24 hours. This can be normal for a rarely used app; otherwise check the route and DNS of this host.'],
}
const PROBE_LABEL = { ok: 'Redirects to sign-in', unprotected: 'Unprotected', unexpected: 'Unexpected redirect', unreachable: 'Unreachable' } // null -> 'Not probed'
const probeLabel = (status) => (status === null ? 'Not probed' : PROBE_LABEL[status] ?? 'Unknown result')
const ORPHAN_HOST = process.env.ORPHAN_HOST // unique per run, routed by overlays/local/smoke-orphan.yaml (rendered and applied by browser-check.sh)
if (!ORPHAN_HOST) { console.log('  FAIL: ORPHAN_HOST is not set (browser-check.sh generates it)'); process.exit(1) }
const ORPHAN_EMPTY_COPY = 'No orphan hosts in the last 7 days.'
const squash = (t) => t.replace(/\s+/g, ' ').trim()
const MARKER_APP = 'hello-a (browser check)'
const RESERVED_COPY = 'This host is reserved for the sign-in service.'
const TAKEN_COPY = 'This host is already used by another app.'
const PORTAL_DISABLE_COPY = 'The portal app cannot be disabled.'
const PORTAL_LAST_HOST_COPY = 'The portal app needs at least one host.'
const CURRENT_HOST_TITLE = 'Remove the host you are using?'
const SMOKE_GROUP = 'smoke-group' // created and deleted by the groups scenario
const DEMO_EDITORS = 'demo-editors' // seeded group: one member (bob) and a grant on demo
const GROUP_IN_USE_COPY = 'This group still has members or grants. Remove them first.'
const ALREADY_MEMBER_COPY = 'This user is already a member of the group.'
const NOT_FOUND_TEXT ='Page not found' // app/error.vue for a client route without a page

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
  await page.goto(`${BASE}/admin/nope/x`)
  await page.waitForLoadState('networkidle')
  const nf = await bodyText()
  check(`/admin/nope/x renders "${NOT_FOUND_TEXT}"`, nf.includes(NOT_FOUND_TEXT), JSON.stringify(nf.slice(0, 200)))
  await page.screenshot({ path: `${OUT}/not-found.png`, fullPage: true })
  await checkStyled('/admin/nope/x')
  await report('/admin/nope/x', { tolerated: TOLERATED_CONSOLE })

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

  // ---- 5d-2a: initiative sub-nav, roles, grants. Idempotent: leftovers of an interrupted run are removed first and the
  // roles/grants of demo are compared (through the API) before and after.
  console.log('  -- admin: demo sub-nav, roles (create/delete smoke-role, last-manager 409 on manager)')
  await login(ADMIN.email)
  await page.goto(`${BASE}/admin/initiatives`)
  const aTable = page.getByRole('table')
  await aTable.getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const demoId = ((await aTable.getByRole('link', { name: 'Demo', exact: true }).getAttribute('href')) ?? '').split('/').pop()
  check('demo initiative id captured', !!demoId)

  // API reads/cleanup from inside the page (same-origin, the session cookie): the state is verified through the API, not the UI.
  const apiCall = (method, path) => page.evaluate(async ([m, p]) => {
    const r = await fetch(`/api/v1${p}`, { method: m, headers: { Accept: 'application/json', ...(m === 'GET' ? {} : { 'Content-Type': 'application/json' }) } })
    return { status: r.status, body: r.status === 200 ? await r.json() : null }
  }, [method, path])
  const apiAll = async (path, key) => {
    const items = []
    let cursor = ''
    do {
      const r = await apiCall('GET', `${path}${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`)
      if (r.status !== 200) throw new Error(`GET ${path} answered ${r.status}`)
      items.push(...r.body[key])
      cursor = r.body.next_cursor ?? ''
    } while (cursor)
    return items
  }
  const snapshot = async () => JSON.stringify({
    roles: (await apiAll(`/initiatives/${demoId}/roles`, 'roles')).map((r) => `${r.key}:${r.manages_access}`).sort(),
    grants: (await apiAll(`/initiatives/${demoId}/grants`, 'grants')).map((g) => `${g.user ? g.user.email : `group:${g.group.key}`}|${g.role.key}|${g.expires_at ?? 'never'}`).sort(),
  })
  for (const g of (await apiAll(`/initiatives/${demoId}/grants`, 'grants')).filter((g) => g.user?.email === CAROL.email)) await apiCall('DELETE', `/grants/${g.id}`)
  for (const r of (await apiAll(`/initiatives/${demoId}/roles`, 'roles')).filter((r) => r.key === SMOKE_ROLE)) await apiCall('DELETE', `/roles/${r.id}`)
  const startState = await snapshot()
  console.log(`  (demo roles/grants at start: ${startState})`)
  const managerRole = (await apiAll(`/initiatives/${demoId}/roles`, 'roles')).find((r) => r.key === 'manager')
  check('seed: role "manager" exists on demo', !!managerRole)

  reset()
  const docs = []
  page.on('request', (r) => { if (r.resourceType() === 'document') docs.push(new URL(r.url()).pathname) })
  await aTable.getByRole('link', { name: 'Demo', exact: true }).click()
  await page.getByRole('heading', { level: 1, name: 'Demo', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  await page.evaluate(() => { window.__spa = 'alive' })
  docs.length = 0
  const subnav = page.getByRole('navigation', { name: 'Initiative', exact: true })
  const currentLinks = async () => subnav.locator('[aria-current=page]').allInnerTexts()
  check('sub-nav: links Overview, Grants, Roles, Apps', JSON.stringify(await subnav.getByRole('link').allInnerTexts().then((t) => t.map((s) => s.trim()))) === JSON.stringify(['Overview', 'Grants', 'Roles', 'Apps']))
  check('sub-nav: Overview is current at /admin/initiatives/<id>', JSON.stringify(await currentLinks()) === '["Overview"]', JSON.stringify(await currentLinks()))
  for (const [name, suffix] of [['Grants', '/grants'], ['Roles', '/roles'], ['Overview', '']]) {
    await subnav.getByRole('link', { name, exact: true }).click()
    await page.waitForURL(`${BASE}/admin/initiatives/${demoId}${suffix}`, { timeout: DATA_TIMEOUT })
    await page.getByRole('heading', { level: 2, name: suffix ? name : 'Overview', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check(`sub-nav: ${name} -> ${suffix || '(overview)'} with aria-current on ${name} only`, JSON.stringify(await currentLinks()) === JSON.stringify([name]), JSON.stringify(await currentLinks()))
  }
  check('sub-nav: client-side (no document request, JS context alive)', docs.length === 0 && (await page.evaluate(() => window.__spa)) === 'alive', JSON.stringify(docs))
  await report('sub-nav (admin)')

  reset()
  await subnav.getByRole('link', { name: 'Roles', exact: true }).click()
  const rolesTable = page.getByRole('table')
  const roleRow = (key) => rolesTable.getByRole('row').filter({ has: page.locator('code', { hasText: new RegExp(`^${key}$`) }) })
  await roleRow('manager').waitFor({ timeout: DATA_TIMEOUT })
  for (const k of ['viewer', 'editor', 'manager']) check(`roles: "${k}" listed once`, (await roleRow(k).count()) === 1, String(await roleRow(k).count()))
  check('roles: "manager" has the Manages access badge', (await roleRow('manager').innerText()).includes('Manages access'))
  check('roles: "viewer" and "editor" have no Manages access badge', !(await roleRow('viewer').innerText()).includes('Manages access') && !(await roleRow('editor').innerText()).includes('Manages access'))
  await page.screenshot({ path: `${OUT}/roles.png`, fullPage: true })
  await checkStyled('roles (admin)')
  await report('roles (admin)')

  console.log('  -- admin: create smoke-role, delete it behind a confirmation')
  reset()
  await page.getByRole('button', { name: 'New role', exact: true }).click()
  const rDlg = page.getByRole('dialog')
  await rDlg.waitFor({ timeout: DATA_TIMEOUT })
  check('new role dialog: initial focus is the Key input', await rDlg.getByRole('textbox', { name: /^Key/ }).evaluate((el) => el === document.activeElement))
  check('new role dialog: "Manages access" unchecked by default', !(await rDlg.getByRole('checkbox', { name: /Manages access/ }).isChecked()))
  await rDlg.getByRole('textbox', { name: /^Key/ }).fill(SMOKE_ROLE)
  await rDlg.getByRole('button', { name: 'Create role', exact: true }).click()
  await rDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await roleRow(SMOKE_ROLE).waitFor({ timeout: DATA_TIMEOUT })
  check(`roles: "${SMOKE_ROLE}" appears without management`, (await roleRow(SMOKE_ROLE).innerText()).includes('No management'))
  await report('create role (admin)')
  reset()
  await page.getByRole('button', { name: `Delete role ${SMOKE_ROLE}`, exact: true }).click()
  const dAlert = page.getByRole('alertdialog')
  await dAlert.waitFor({ timeout: DATA_TIMEOUT })
  await dAlert.getByRole('button', { name: 'Delete role', exact: true }).click()
  await dAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await roleRow(SMOKE_ROLE).waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check(`roles: "${SMOKE_ROLE}" gone after the confirmed delete`, (await roleRow(SMOKE_ROLE).count()) === 0)
  await report('delete role (admin)')

  console.log('  -- admin: turning OFF manages_access on "manager" -> last-manager 409, badge stays')
  reset()
  await page.getByRole('button', { name: 'Remove management from role manager', exact: true }).click()
  await dAlert.waitFor({ timeout: DATA_TIMEOUT })
  await dAlert.getByRole('button', { name: 'Remove management', exact: true }).click()
  await dAlert.getByText(LAST_MANAGER_COPY).waitFor({ timeout: DATA_TIMEOUT })
  check('manager toggle: the last-manager copy is shown in the dialog', true)
  const managerPatch = `${BASE}/api/v1/roles/${managerRole?.id} HTTP 409`
  check('manager toggle: the API answered 409 on that role', failedRequests.includes(managerPatch), JSON.stringify(failedRequests))
  await report('manager toggle 409', { tolerated: ['status of 409'], toleratedRequests: [managerPatch] })
  reset()
  await dAlert.getByRole('button', { name: 'Cancel', exact: true }).click()
  await dAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('manager toggle: the badge stays on "manager"', (await roleRow('manager').innerText()).includes('Manages access'))
  check('manager toggle: manages_access still true in the API', (await apiAll(`/initiatives/${demoId}/roles`, 'roles')).some((r) => r.key === 'manager' && r.manages_access))
  await report('manager toggle (after cancel)')

  console.log('  -- dave (manager of demo): grants create/revoke, last-manager 409, roles read-only')
  await login(DAVE.email)
  await page.goto(`${BASE}/admin/initiatives/${demoId}/grants`)
  const gRow = (text) => page.getByRole('table').getByRole('row').filter({ hasText: text })
  await gRow('dave@example.com').first().waitFor({ timeout: DATA_TIMEOUT })
  check('dave grants: sub-nav current is Grants', JSON.stringify(await page.getByRole('navigation', { name: 'Initiative', exact: true }).locator('[aria-current=page]').allInnerTexts()) === '["Grants"]')
  for (const who of ['dave@example.com', 'alice@example.com', 'demo-editors']) check(`dave grants: "${who}" listed`, (await gRow(who).count()) >= 1)
  check('dave grants: carol has no grant', (await gRow(CAROL.email).count()) === 0)
  await report('grants (dave)')

  reset()
  const grantTrigger = page.getByRole('button', { name: 'Grant access', exact: true })
  await grantTrigger.click()
  const gDlg = page.getByRole('dialog')
  await gDlg.waitFor({ timeout: DATA_TIMEOUT })
  check('grant dialog: initial focus is the first field ("Grant to")', await gDlg.getByLabel('Grant to').evaluate((el) => el === document.activeElement))
  await gDlg.getByLabel(/^Role/).locator('option', { hasText: 'viewer' }).waitFor({ state: 'attached', timeout: DATA_TIMEOUT })
  await page.screenshot({ path: `${OUT}/grants-dialog.png`, fullPage: true })
  await report('grant dialog (open)')
  await page.keyboard.press('Escape')
  await gDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('grant dialog: Escape returns focus to the trigger', await grantTrigger.evaluate((el) => el === document.activeElement))
  await report('grant dialog (closed)')

  reset()
  await grantTrigger.click()
  await gDlg.waitFor({ timeout: DATA_TIMEOUT })
  await gDlg.getByLabel(/^Email/).fill(CAROL.email)
  await gDlg.getByLabel(/^Role/).selectOption({ label: 'viewer' })
  await gDlg.getByRole('button', { name: 'Grant access', exact: true }).click()
  await gDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await gRow(CAROL.email).waitFor({ timeout: DATA_TIMEOUT })
  const carolText = await gRow(CAROL.email).innerText()
  check('grants: carol viewer appears with no expiry', carolText.includes('viewer') && carolText.includes('Never'), JSON.stringify(carolText))
  await report('create grant (dave)')
  reset()
  await page.getByRole('button', { name: `Revoke viewer from ${CAROL.email}`, exact: true }).click()
  const gAlert = page.getByRole('alertdialog')
  await gAlert.waitFor({ timeout: DATA_TIMEOUT })
  await gAlert.getByRole('button', { name: 'Revoke', exact: true }).click()
  await gAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await gRow(CAROL.email).waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('grants: carol viewer gone after the confirmed revoke', (await gRow(CAROL.email).count()) === 0)
  await report('revoke grant (dave)')

  reset()
  const daveGrant = (await apiAll(`/initiatives/${demoId}/grants`, 'grants')).find((g) => g.user?.email === DAVE.email && g.role.key === 'manager')
  check('dave own manager grant found through the API', !!daveGrant)
  reset()
  await page.getByRole('button', { name: `Revoke manager from ${DAVE.email}`, exact: true }).click()
  await gAlert.waitFor({ timeout: DATA_TIMEOUT })
  await gAlert.getByRole('button', { name: 'Revoke', exact: true }).click()
  await gAlert.getByText(LAST_MANAGER_COPY).waitFor({ timeout: DATA_TIMEOUT })
  check('dave own revoke: the last-manager copy is shown', true)
  const grantDelete = `${BASE}/api/v1/grants/${daveGrant?.id} HTTP 409`
  check('dave own revoke: the API answered 409 on that grant', failedRequests.includes(grantDelete), JSON.stringify(failedRequests))
  await report('dave own revoke 409', { tolerated: ['status of 409'], toleratedRequests: [grantDelete] })
  reset()
  await gAlert.getByRole('button', { name: 'Cancel', exact: true }).click()
  await gAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('dave own revoke: the grant is still listed', (await gRow(DAVE.email).count()) >= 1)
  await report('dave own revoke (after cancel)')

  reset()
  await page.getByRole('navigation', { name: 'Initiative', exact: true }).getByRole('link', { name: 'Roles', exact: true }).click()
  await page.waitForURL(`${BASE}/admin/initiatives/${demoId}/roles`, { timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 2, name: 'Roles', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByRole('table').getByRole('row').filter({ has: page.locator('code', { hasText: /^manager$/ }) }).waitFor({ timeout: DATA_TIMEOUT })
  for (const k of ['viewer', 'editor', 'manager']) check(`dave roles: "${k}" listed`, (await page.locator('code', { hasText: new RegExp(`^${k}$`) }).count()) === 1)
  check('dave roles: no "New role" button', (await page.getByRole('button', { name: 'New role' }).count()) === 0)
  check('dave roles: no Allow/Remove management toggles', (await page.getByRole('button', { name: /management/i }).count()) === 0)
  check('dave roles: no Delete buttons', (await page.getByRole('button', { name: /^Delete/ }).count()) === 0)
  check('dave roles: read-only notice', (await bodyText()).includes('Only platform administrators change roles'))
  await report('roles (dave)')

  console.log('  -- admin: demo roles and grants equal the starting set')
  await login(ADMIN.email)
  await page.goto(`${BASE}/admin/initiatives`)
  await page.getByRole('table').getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const endState = await snapshot()
  check('demo roles and grants (API) equal the starting set', endState === startState, `${startState} != ${endState}`)
  // ---- 5d-2b: initiative apps and deployments. Idempotent: "New app" is opened and CANCELLED, never submitted (creating an app is
  // not idempotent); the temporary rename and host are undone, and the apps/deployments of demo and platform (through the API)
  // are compared before and after. Leftovers of an interrupted run are removed first.
  console.log('  -- admin: demo Apps (list, New app cancelled, rename round trip, add/remove host, reserved and duplicate hosts)')
  await login(ADMIN.email)
  await page.goto(`${BASE}/admin/initiatives`)
  await page.getByRole('table').getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const platformId = ((await page.getByRole('table').getByRole('link', { name: 'Platform', exact: true }).getAttribute('href')) ?? '').split('/').pop()
  check('platform initiative id captured', !!platformId)
  const apiSend = (method, path, body) => page.evaluate(async ([m, p, b]) => {
    const r = await fetch(`/api/v1${p}`, { method: m, headers: { Accept: 'application/json', 'Content-Type': 'application/json' }, body: JSON.stringify(b) })
    return { status: r.status }
  }, [method, path, body])
  const appsOf = (id) => apiAll(`/initiatives/${id}/apps`, 'apps')
  const appsSnapshot = async () => JSON.stringify(await Promise.all([demoId, platformId].map(async (id) => (await appsOf(id))
    .map((a) => `${a.id}|${a.name}|${a.status}|${a.deployments.map((d) => d.host).sort().join(',')}`).sort())))
  for (const a of await appsOf(demoId)) {
    for (const d of a.deployments.filter((d) => d.host === SMOKE_HOST)) await apiCall('DELETE', `/deployments/${d.id}`)
    if (a.name === MARKER_APP) await apiSend('PATCH', `/apps/${a.id}`, { name: 'hello-a' })
  }
  const appsStart = await appsSnapshot()
  console.log(`  (demo and platform apps at start: ${appsStart})`)
  const helloA = (await appsOf(demoId)).find((a) => a.name === 'hello-a')
  check('seed: demo has the app "hello-a" with its host', !!helloA && helloA.deployments.some((d) => d.host === ALICE.apps[0].host))
  const portalApp = (await appsOf(platformId))[0]
  check('seed: platform has exactly the portal app', (await appsOf(platformId)).length === 1 && !!portalApp)

  const appsMutations = []
  page.on('request', (r) => { if (r.method() !== 'GET' && r.method() !== 'HEAD') appsMutations.push(`${r.method()} ${new URL(r.url()).pathname}`) })
  const appRow = (app) => page.getByRole('table').getByRole('row').filter({ hasText: app.id })
  const appsDlg = () => page.getByRole('dialog')
  const appsAlert = () => page.getByRole('alertdialog')
  const inDialog = () => page.evaluate(() => !!document.activeElement?.closest('[role=dialog]'))
  const openAddHost = async (label) => {
    await appRow(helloA).getByRole('button', { name: `Add a host to app ${label}`, exact: true }).click()
    await appsDlg().waitFor({ timeout: DATA_TIMEOUT })
  }
  const hostPost = `${BASE}/api/v1/apps/${helloA?.id}/deployments`

  reset()
  await page.goto(`${BASE}/admin/initiatives/${demoId}`)
  await page.getByRole('heading', { level: 1, name: 'Demo', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByRole('navigation', { name: 'Initiative', exact: true }).getByRole('link', { name: 'Apps', exact: true }).click()
  await page.waitForURL(`${BASE}/admin/initiatives/${demoId}/apps`, { timeout: DATA_TIMEOUT })
  await appRow(helloA).waitFor({ timeout: DATA_TIMEOUT })
  check('apps: sub-nav current is Apps', JSON.stringify(await page.getByRole('navigation', { name: 'Initiative', exact: true }).locator('[aria-current=page]').allInnerTexts()) === '["Apps"]')
  check('apps: hello-a listed once with status Active', (await appRow(helloA).count()) === 1 && (await appRow(helloA).innerText()).includes('Active'))
  const hostLink = appRow(helloA).getByRole('link', { name: ALICE.apps[0].host })
  check(`apps: hello-a host is a link to https://${ALICE.apps[0].host}/`, (await hostLink.getAttribute('href')) === `https://${ALICE.apps[0].host}/`, String(await hostLink.getAttribute('href')))
  await checkStyled('apps (admin)')
  await report('apps (admin)')

  reset()
  appsMutations.length = 0
  const newApp = page.getByRole('button', { name: 'New app', exact: true })
  await newApp.click()
  await appsDlg().waitFor({ timeout: DATA_TIMEOUT })
  check('new app dialog: focus is inside', await inDialog())
  check('new app dialog: initial focus is the Name input', await appsDlg().getByRole('textbox', { name: /^Name/ }).evaluate((el) => el === document.activeElement))
  await report('new app dialog (open)')
  await appsDlg().getByRole('button', { name: 'Cancel', exact: true }).click() // NEVER submitted
  await appsDlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('new app dialog: Cancel returns focus to the trigger', await newApp.evaluate((el) => el === document.activeElement))
  check('new app dialog: no mutation request', appsMutations.length === 0, JSON.stringify(appsMutations))
  check('new app dialog: the app count is unchanged (API)', (await appsOf(demoId)).length === 1)

  console.log('  -- admin: rename hello-a to a marker and back')
  reset()
  let appRenamed = false
  try {
    await appRow(helloA).getByRole('button', { name: 'Rename app hello-a', exact: true }).click()
    await appsDlg().waitFor({ timeout: DATA_TIMEOUT })
    check('rename dialog: initial focus is the Name input', await appsDlg().getByRole('textbox', { name: /^Name/ }).evaluate((el) => el === document.activeElement))
    await appsDlg().getByRole('textbox', { name: /^Name/ }).fill(MARKER_APP)
    await appsDlg().getByRole('button', { name: 'Save name', exact: true }).click()
    appRenamed = true
    await appsDlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await appRow(helloA).getByText(MARKER_APP, { exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check(`apps rename: "${MARKER_APP}" shown, id kept`, (await appRow(helloA).innerText()).includes(helloA.id))
    check('apps rename: the API has the marker', (await appsOf(demoId)).some((a) => a.id === helloA.id && a.name === MARKER_APP))
    await report('apps rename')
  } finally {
    if (appRenamed) {
      reset()
      await appRow(helloA).getByRole('button', { name: `Rename app ${MARKER_APP}`, exact: true }).click()
      await appsDlg().waitFor({ timeout: DATA_TIMEOUT })
      await appsDlg().getByRole('textbox', { name: /^Name/ }).fill('hello-a')
      await appsDlg().getByRole('button', { name: 'Save name', exact: true }).click()
      await appsDlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
      await appRow(helloA).getByText('hello-a', { exact: true }).waitFor({ timeout: DATA_TIMEOUT })
      check('apps rename: original name "hello-a" restored', (await appsOf(demoId)).some((a) => a.id === helloA.id && a.name === 'hello-a'))
      await report('apps rename back')
    }
  }

  console.log('  -- admin: add host (mixed case -> lowercased), screenshot, remove it behind a confirmation')
  reset()
  let hostAdded = false
  try {
    await openAddHost('hello-a')
    check('add host dialog: focus is inside', await inDialog())
    check('add host dialog: initial focus is the Host input', await appsDlg().getByRole('textbox', { name: /^Host/ }).evaluate((el) => el === document.activeElement))
    await appsDlg().getByRole('textbox', { name: /^Host/ }).fill(`  ${SMOKE_HOST_INPUT}  `)
    await page.screenshot({ path: `${OUT}/apps-add-host-dialog.png`, fullPage: true })
    await report('add host dialog (open)')
    await appsDlg().getByRole('button', { name: 'Add host', exact: true }).click()
    hostAdded = true
    await appsDlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await appRow(helloA).getByRole('link', { name: SMOKE_HOST }).waitFor({ timeout: DATA_TIMEOUT })
    check(`add host: shown lowercased as ${SMOKE_HOST}`, (await appRow(helloA).getByRole('link', { name: SMOKE_HOST }).getAttribute('href')) === `https://${SMOKE_HOST}/`)
    check('add host: the API has the lowercase host only', (await appsOf(demoId)).some((a) => a.id === helloA.id && a.deployments.some((d) => d.host === SMOKE_HOST) && !a.deployments.some((d) => d.host === SMOKE_HOST_INPUT)))
    await report('add host')
    reset()
    await appRow(helloA).getByRole('button', { name: `Remove host ${SMOKE_HOST}`, exact: true }).click()
    await appsAlert().waitFor({ timeout: DATA_TIMEOUT })
    check('remove host: confirm dialog (alertdialog) visible, focus inside', await page.evaluate(() => !!document.activeElement?.closest('[role=alertdialog]')))
    await appsAlert().getByRole('button', { name: 'Remove host', exact: true }).click()
    await appsAlert().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    hostAdded = false
    await appRow(helloA).getByRole('link', { name: SMOKE_HOST }).waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    check(`remove host: ${SMOKE_HOST} gone (list and API)`, !(await appsOf(demoId)).some((a) => a.deployments.some((d) => d.host === SMOKE_HOST)))
    await report('remove host')
  } finally {
    if (hostAdded) for (const a of await appsOf(demoId)) for (const d of a.deployments.filter((d) => d.host === SMOKE_HOST)) await apiCall('DELETE', `/deployments/${d.id}`)
  }

  console.log('  -- admin: reserved auth host -> 422 copy; hello-b host -> 409 duplicate copy; nothing added')
  for (const [label, host, copy, status] of [['auth host', AUTH_HOST, RESERVED_COPY, 422], ['hello-b host', ALICE.apps[1].host, TAKEN_COPY, 409]]) {
    reset()
    await openAddHost('hello-a')
    await appsDlg().getByRole('textbox', { name: /^Host/ }).fill(host)
    await appsDlg().getByRole('button', { name: 'Add host', exact: true }).click()
    await appsDlg().getByText(copy).first().waitFor({ timeout: DATA_TIMEOUT })
    check(`add ${label}: copy "${copy}" shown and the dialog stays open`, await appsDlg().isVisible())
    const failed = `${hostPost} HTTP ${status}`
    check(`add ${label}: the API answered ${status}`, failedRequests.includes(failed), JSON.stringify(failedRequests))
    await report(`add ${label}`, { tolerated: [`status of ${status}`], toleratedRequests: [failed] })
    await appsDlg().getByRole('button', { name: 'Cancel', exact: true }).click()
    await appsDlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    check(`add ${label}: nothing added to hello-a (API)`, (await appsOf(demoId)).every((a) => a.id !== helloA.id || a.deployments.map((d) => d.host).join() === ALICE.apps[0].host))
  }

  console.log('  -- admin: platform Apps (portal app: disable -> 409, last host -> 409)')
  reset()
  await page.goto(`${BASE}/admin/initiatives/${platformId}/apps`)
  await appRow(portalApp).waitFor({ timeout: DATA_TIMEOUT })
  check('platform apps: portal app listed with status Active', (await appRow(portalApp).innerText()).includes('Active'))
  await report('platform apps')
  reset()
  await appRow(portalApp).getByRole('button', { name: /^Disable app /, }).click()
  await appsAlert().waitFor({ timeout: DATA_TIMEOUT })
  await appsAlert().getByRole('button', { name: 'Disable app', exact: true }).click()
  await appsAlert().getByText(PORTAL_DISABLE_COPY).waitFor({ timeout: DATA_TIMEOUT })
  const disable409 = `${BASE}/api/v1/apps/${portalApp.id} HTTP 409`
  check(`portal disable: copy "${PORTAL_DISABLE_COPY}" shown`, true)
  check('portal disable: the API answered 409', failedRequests.includes(disable409), JSON.stringify(failedRequests))
  await report('portal disable 409', { tolerated: ['status of 409'], toleratedRequests: [disable409] })
  await appsAlert().getByRole('button', { name: 'Cancel', exact: true }).click()
  await appsAlert().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('portal disable: status stays Active (list and API)', (await appRow(portalApp).innerText()).includes('Active') && (await appsOf(platformId))[0].status === 'active')
  if (portalApp.deployments.length === 1) {
    const portalHost = portalApp.deployments[0]
    reset()
    await appRow(portalApp).getByRole('button', { name: `Remove host ${portalHost.host}`, exact: true }).click()
    await appsAlert().waitFor({ timeout: DATA_TIMEOUT })
    check('portal last host: the confirm title is "Remove the host you are using?"', (await appsAlert().getByRole('heading', { level: 2 }).innerText()) === CURRENT_HOST_TITLE)
    await appsAlert().getByRole('button', { name: 'Remove host', exact: true }).click()
    await appsAlert().getByText(PORTAL_LAST_HOST_COPY).waitFor({ timeout: DATA_TIMEOUT })
    const remove409 = `${BASE}/api/v1/deployments/${portalHost.id} HTTP 409`
    check(`portal last host: copy "${PORTAL_LAST_HOST_COPY}" shown`, true)
    check('portal last host: the API answered 409', failedRequests.includes(remove409), JSON.stringify(failedRequests))
    await report('portal last host 409', { tolerated: ['status of 409'], toleratedRequests: [remove409] })
    await appsAlert().getByRole('button', { name: 'Cancel', exact: true }).click()
    await appsAlert().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    check('portal last host: the host stays (list and API)', (await appRow(portalApp).getByRole('link', { name: portalHost.host }).count()) === 1 && (await appsOf(platformId))[0].deployments.length === 1)
  } else {
    console.log(`  SKIP: portal remove-host case (the portal app has ${portalApp.deployments.length} hosts, not exactly one; a real portal host is never removed)`)
  }

  console.log('  -- dave (manager of demo): Apps tab is read-only')
  await login(DAVE.email)
  reset()
  await page.goto(`${BASE}/admin/initiatives/${demoId}/apps`)
  await appRow(helloA).waitFor({ timeout: DATA_TIMEOUT })
  check('dave apps: sub-nav current is Apps', JSON.stringify(await page.getByRole('navigation', { name: 'Initiative', exact: true }).locator('[aria-current=page]').allInnerTexts()) === '["Apps"]')
  check('dave apps: hello-a listed with its host link', (await appRow(helloA).getByRole('link', { name: ALICE.apps[0].host }).getAttribute('href')) === `https://${ALICE.apps[0].host}/`)
  for (const name of [/^New app/, /^Rename/, /^Disable/, /^Enable/, /^Add (a )?host/, /^Remove host/]) check(`dave apps: no button ${name}`, (await page.getByRole('button', { name }).count()) === 0)
  check('dave apps: read-only notice', (await bodyText()).includes('Only platform administrators change apps and their hosts'))
  await page.screenshot({ path: `${OUT}/apps-manager.png`, fullPage: true })
  await report('apps (dave)')

  console.log('  -- admin: demo and platform apps/deployments equal the starting set')
  await login(ADMIN.email)
  await page.goto(`${BASE}/admin/initiatives`)
  await page.getByRole('table').getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const appsEnd = await appsSnapshot()
  check('demo and platform apps/deployments (API) equal the starting set', appsEnd === appsStart, `${appsStart} != ${appsEnd}`)
  // ---- 5d-3: groups and members, users, audit. Idempotent: leftovers of an interrupted run (smoke-group, carol disabled) are
  // cleaned first; the groups, the members of demo-editors and carol's status are compared (through the API) before and after.
  console.log('  -- admin: Nav (Initiatives, Groups, Users, Health, Audit); Groups list; leftovers of an interrupted run removed')
  await login(ADMIN.email)
  await page.goto(`${BASE}/admin/initiatives`)
  await page.getByRole('table').getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const adminNav = () => page.getByRole('navigation', { name: 'Administration', exact: true }) // a function: the page changes with each login
  const navLinks = async () => (await adminNav().getByRole('link').allInnerTexts()).map((t) => t.trim())
  check('admin Nav: Initiatives, Groups, Users, Health, Audit', JSON.stringify(await navLinks()) === '["Initiatives","Groups","Users","Health","Audit"]', JSON.stringify(await navLinks()))

  const groupsApi = () => apiAll('/groups', 'groups')
  const membersApi = (id) => apiAll(`/groups/${id}/members`, 'members')
  const userByEmail = async (email) => {
    const r = await apiCall('GET', `/users?email=${encodeURIComponent(email)}`)
    if (r.status !== 200 || r.body.users.length !== 1) throw new Error(`GET /users?email=${email} answered ${r.status} with ${r.body?.users?.length} users`)
    return r.body.users[0]
  }
  for (const g of (await groupsApi()).filter((g) => g.key === SMOKE_GROUP)) {
    for (const m of await membersApi(g.id)) await apiCall('DELETE', `/groups/${g.id}/members/${m.user_id}`)
    await apiCall('DELETE', `/groups/${g.id}`)
  }
  const carolUser = await userByEmail(CAROL.email)
  const adminUser = await userByEmail(ADMIN.email)
  if (carolUser.status !== 'active') await apiSend('PATCH', `/users/${carolUser.id}`, { status: 'active' })
  const demoEditors = (await groupsApi()).find((g) => g.key === DEMO_EDITORS)
  check(`seed: group "${DEMO_EDITORS}" exists`, !!demoEditors)
  const groupsSnapshot = async () => JSON.stringify({
    groups: (await groupsApi()).map((g) => `${g.key}|${g.name}`).sort(),
    demoEditors: (await membersApi(demoEditors.id)).map((m) => `${m.email}|${m.status}`).sort(),
    carol: (await userByEmail(CAROL.email)).status,
  })
  const groupsStart = await groupsSnapshot()
  console.log(`  (groups, demo-editors members and carol at start: ${groupsStart})`)
  check('seed: demo-editors has at least one member', (await membersApi(demoEditors.id)).length >= 1)
  const auditSince = new Date().toISOString() // the audit events of this section are the ones after this instant

  reset()
  await adminNav().getByRole('link', { name: 'Groups', exact: true }).click()
  await page.waitForURL(`${BASE}/admin/groups`, { timeout: DATA_TIMEOUT })
  const gTable = () => page.getByRole('table')
  const groupRow = (key) => gTable().getByRole('row').filter({ has: page.locator('code', { hasText: new RegExp(`^${key}$`) }) })
  await groupRow(DEMO_EDITORS).waitFor({ timeout: DATA_TIMEOUT })
  check('groups: Nav current is Groups', JSON.stringify(await adminNav().locator('[aria-current=page]').allInnerTexts()) === '["Groups"]')
  check('groups: demo-editors listed once with its name', (await groupRow(DEMO_EDITORS).count()) === 1 && (await groupRow(DEMO_EDITORS).innerText()).includes('Demo editors'))
  await page.screenshot({ path: `${OUT}/groups-list.png`, fullPage: true })
  await checkStyled('groups (admin)')
  await report('groups (admin)')

  console.log('  -- admin: create smoke-group, open it, add carol, add again (409 copy), remove carol, back')
  reset()
  await page.getByRole('button', { name: 'New group', exact: true }).click()
  const grpDlg = page.getByRole('dialog')
  await grpDlg.waitFor({ timeout: DATA_TIMEOUT })
  check('new group dialog: initial focus is the Key input', await grpDlg.getByRole('textbox', { name: /^Key/ }).evaluate((el) => el === document.activeElement))
  await grpDlg.getByRole('textbox', { name: /^Key/ }).fill(SMOKE_GROUP)
  await grpDlg.getByRole('textbox', { name: /^Name/ }).fill('Smoke group')
  await grpDlg.getByRole('button', { name: 'Create group', exact: true }).click()
  await grpDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await groupRow(SMOKE_GROUP).waitFor({ timeout: DATA_TIMEOUT })
  check('groups: smoke-group appears with its name', (await groupRow(SMOKE_GROUP).innerText()).includes('Smoke group'))
  const smokeGroup = (await groupsApi()).find((g) => g.key === SMOKE_GROUP)
  check('groups: smoke-group exists in the API', !!smokeGroup)
  await report('create group')

  reset()
  await groupRow(SMOKE_GROUP).getByRole('link', { name: SMOKE_GROUP, exact: true }).click()
  await page.waitForURL(`${BASE}/admin/groups/${smokeGroup.id}`, { timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { name: 'Group members', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByText('This group has no members yet.').waitFor({ timeout: DATA_TIMEOUT })
  check('group detail: Nav current stays Groups', JSON.stringify(await adminNav().locator('[aria-current=page]').allInnerTexts()) === '["Groups"]')
  await report('group detail (empty)')

  const memberRow = (email) => page.getByRole('table').getByRole('row').filter({ hasText: email })
  const addMember = async (email) => {
    await page.getByRole('button', { name: 'Add member', exact: true }).click()
    await grpDlg.waitFor({ timeout: DATA_TIMEOUT })
    await grpDlg.getByRole('textbox', { name: /^Email/ }).fill(email)
    await grpDlg.getByRole('button', { name: 'Add member', exact: true }).click()
  }
  reset()
  await page.getByRole('button', { name: 'Add member', exact: true }).click()
  await grpDlg.waitFor({ timeout: DATA_TIMEOUT })
  check('add member dialog: initial focus is the Email input', await grpDlg.getByRole('textbox', { name: /^Email/ }).evaluate((el) => el === document.activeElement))
  await page.keyboard.press('Escape')
  await grpDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  reset()
  await addMember(CAROL.email)
  await grpDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await memberRow(CAROL.email).waitFor({ timeout: DATA_TIMEOUT })
  check('members: carol listed with status Active', (await memberRow(CAROL.email).innerText()).includes('Active'))
  check('members: carol is the only member (API)', JSON.stringify((await membersApi(smokeGroup.id)).map((m) => m.email)) === JSON.stringify([CAROL.email]))
  await report('add member')

  reset()
  await addMember(CAROL.email)
  await grpDlg.getByText(ALREADY_MEMBER_COPY).first().waitFor({ timeout: DATA_TIMEOUT })
  const dupMember = `${BASE}/api/v1/groups/${smokeGroup.id}/members HTTP 409`
  check(`add member again: copy "${ALREADY_MEMBER_COPY}" shown and the dialog stays open`, await grpDlg.isVisible())
  check('add member again: the API answered 409', failedRequests.includes(dupMember), JSON.stringify(failedRequests))
  await report('add member again 409', { tolerated: ['status of 409'], toleratedRequests: [dupMember] })
  await grpDlg.getByRole('button', { name: 'Cancel', exact: true }).click()
  await grpDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('add member again: still one member (API)', (await membersApi(smokeGroup.id)).length === 1)

  reset()
  await page.getByRole('button', { name: `Remove ${CAROL.email}`, exact: true }).click()
  const mAlert = page.getByRole('alertdialog')
  await mAlert.waitFor({ timeout: DATA_TIMEOUT })
  check('remove member: confirm title names carol', (await mAlert.getByRole('heading', { level: 2 }).innerText()) === `Remove ${CAROL.email}?`)
  await mAlert.getByRole('button', { name: 'Remove', exact: true }).click()
  await mAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await memberRow(CAROL.email).waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('remove member: carol gone (list and API)', (await membersApi(smokeGroup.id)).length === 0)
  await report('remove member')

  console.log('  -- admin: delete demo-editors -> 409 copy, still listed; delete smoke-group behind a confirmation')
  reset()
  await page.getByRole('link', { name: 'All groups', exact: true }).click()
  await page.waitForURL(`${BASE}/admin/groups`, { timeout: DATA_TIMEOUT })
  await groupRow(DEMO_EDITORS).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByRole('button', { name: `Delete group ${DEMO_EDITORS}`, exact: true }).click()
  const gAlert2 = page.getByRole('alertdialog')
  await gAlert2.waitFor({ timeout: DATA_TIMEOUT })
  await gAlert2.getByRole('button', { name: 'Delete group', exact: true }).click()
  await gAlert2.getByText(GROUP_IN_USE_COPY).waitFor({ timeout: DATA_TIMEOUT })
  const inUse409 = `${BASE}/api/v1/groups/${demoEditors.id} HTTP 409`
  check(`delete demo-editors: copy "${GROUP_IN_USE_COPY}" shown in the dialog`, true)
  check('delete demo-editors: the API answered 409', failedRequests.includes(inUse409), JSON.stringify(failedRequests))
  await report('delete demo-editors 409', { tolerated: ['status of 409'], toleratedRequests: [inUse409] })
  await gAlert2.getByRole('button', { name: 'Cancel', exact: true }).click()
  await gAlert2.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('delete demo-editors: still listed after Cancel (list and API)', (await groupRow(DEMO_EDITORS).count()) === 1 && (await groupsApi()).some((g) => g.id === demoEditors.id))

  reset()
  await page.getByRole('button', { name: `Delete group ${SMOKE_GROUP}`, exact: true }).click()
  await gAlert2.waitFor({ timeout: DATA_TIMEOUT })
  check('delete smoke-group: confirm title uses the group name', (await gAlert2.getByRole('heading', { level: 2 }).innerText()) === 'Delete group Smoke group?')
  await gAlert2.getByRole('button', { name: 'Delete group', exact: true }).click()
  await gAlert2.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await groupRow(SMOKE_GROUP).waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('delete smoke-group: gone (list and API)', !(await groupsApi()).some((g) => g.key === SMOKE_GROUP))
  await report('delete group')

  console.log('  -- admin: Users (search by exact email, disable/enable behind confirmations, own row, revoke sessions)')
  reset()
  await adminNav().getByRole('link', { name: 'Users', exact: true }).click()
  await page.waitForURL(`${BASE}/admin/users`, { timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 1, name: 'Users', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  check('users: Nav current is Users', JSON.stringify(await adminNav().locator('[aria-current=page]').allInnerTexts()) === '["Users"]')
  check('users: no results table before the first search', (await page.getByRole('table').count()) === 0)
  await report('users (before search)')
  const search = async (email) => {
    await page.getByRole('textbox', { name: /^Email/ }).fill(email)
    await page.getByRole('button', { name: 'Search', exact: true }).click()
    await page.getByRole('table').getByRole('row').filter({ hasText: email }).waitFor({ timeout: DATA_TIMEOUT }) // the previous results stay until the new ones arrive
  }
  const userRows = () => page.getByRole('table').getByRole('row')
  const statusAlert = page.getByRole('alertdialog')
  const notice = (re) => page.getByText(re).first()
  reset()
  await search(CAROL.email)
  check('users: carol search -> exactly one row', (await userRows().count()) === 2, String(await userRows().count()))
  check('users: carol row shows the email and status Active, no "You" badge', (await userRows().nth(1).innerText()).includes(CAROL.email) && (await userRows().nth(1).innerText()).includes('Active') && !(await userRows().nth(1).innerText()).includes('You'))
  await page.screenshot({ path: `${OUT}/users-results.png`, fullPage: true })
  await checkStyled('users (admin)')
  await report('users search carol')

  let carolDisabled = false
  try {
    reset()
    await page.getByRole('button', { name: `Disable ${CAROL.email}`, exact: true }).click()
    await statusAlert.waitFor({ timeout: DATA_TIMEOUT })
    check('disable carol: confirm title names carol', (await statusAlert.getByRole('heading', { level: 2 }).innerText()) === `Disable ${CAROL.email}?`)
    await statusAlert.getByRole('button', { name: 'Cancel', exact: true }).click()
    await statusAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    check('disable carol: Cancel changes nothing (API)', (await userByEmail(CAROL.email)).status === 'active')
    await page.getByRole('button', { name: `Disable ${CAROL.email}`, exact: true }).click()
    await statusAlert.waitFor({ timeout: DATA_TIMEOUT })
    await statusAlert.getByRole('button', { name: 'Disable', exact: true }).click()
    carolDisabled = true
    await statusAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await userRows().nth(1).getByText('Disabled', { exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check('disable carol: row shows Disabled and offers Enable instead of Disable', (await page.getByRole('button', { name: `Enable ${CAROL.email}`, exact: true }).count()) === 1 && (await page.getByRole('button', { name: `Disable ${CAROL.email}`, exact: true }).count()) === 0)
    check('disable carol: the API says disabled', (await userByEmail(CAROL.email)).status === 'disabled')
    await notice('User disabled and signed out.').waitFor({ timeout: DATA_TIMEOUT })
    await report('disable carol')
    reset()
    await page.getByRole('button', { name: `Enable ${CAROL.email}`, exact: true }).click()
    await statusAlert.waitFor({ timeout: DATA_TIMEOUT })
    check('enable carol: confirm title names carol', (await statusAlert.getByRole('heading', { level: 2 }).innerText()) === `Enable ${CAROL.email}?`)
    await statusAlert.getByRole('button', { name: 'Enable', exact: true }).click()
    await statusAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    carolDisabled = false
    await userRows().nth(1).getByText('Active', { exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check('enable carol: the API says active', (await userByEmail(CAROL.email)).status === 'active')
    await report('enable carol')
  } finally {
    if (carolDisabled) await apiSend('PATCH', `/users/${carolUser.id}`, { status: 'active' })
  }

  reset()
  await page.getByRole('button', { name: `Revoke sessions of ${CAROL.email}`, exact: true }).click()
  await statusAlert.waitFor({ timeout: DATA_TIMEOUT })
  check('revoke carol: confirm title names carol', (await statusAlert.getByRole('heading', { level: 2 }).innerText()) === `Revoke the sessions of ${CAROL.email}?`)
  await statusAlert.getByRole('button', { name: 'Revoke sessions', exact: true }).click()
  await statusAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await notice(/^\d+ sessions? revoked\.$/).waitFor({ timeout: DATA_TIMEOUT })
  check('revoke carol: success notice shown', true)
  await report('revoke carol sessions')

  reset()
  await search(ADMIN.email)
  const ownRow = userRows().nth(1)
  check('users: admin search -> exactly one row', (await userRows().count()) === 2)
  check('users: own row has the "You" badge', (await ownRow.innerText()).includes('You'), JSON.stringify(await ownRow.innerText()))
  check('users: own row has no Disable button', (await page.getByRole('button', { name: `Disable ${ADMIN.email}`, exact: true }).count()) === 0)
  check('users: own row keeps Revoke sessions (NOT clicked)', (await page.getByRole('button', { name: `Revoke sessions of ${ADMIN.email}`, exact: true }).count()) === 1)
  await page.screenshot({ path: `${OUT}/users-own-row.png`, fullPage: true })
  await report('users search admin')

  console.log('  -- admin: Audit (newest first, this section\'s events, details as text, Load more, filter by demo)')
  reset()
  await adminNav().getByRole('link', { name: 'Audit', exact: true }).click()
  await page.waitForURL(`${BASE}/admin/audit`, { timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 1, name: 'Audit', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  const auditRows = () => page.getByRole('table').getByRole('row')
  await auditRows().nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const rowAction = (row) => row.locator('td, th').nth(2).innerText().then((t) => t.trim())
  const rowActions = async () => Promise.all((await auditRows().all()).slice(1).map(rowAction))
  const firstPage = await apiCall('GET', '/audit')
  const fresh = firstPage.body.events.filter((e) => e.occurred_at >= auditSince)
  const EXPECTED_ACTIONS = ['group.create', 'group_member.add', 'group_member.remove', 'group.delete', 'user.disable', 'user.enable', 'user_sessions.revoke']
  check('audit: Nav current is Audit', JSON.stringify(await adminNav().locator('[aria-current=page]').allInnerTexts()) === '["Audit"]')
  check('audit (API): the events since the section started are exactly the 7 actions performed', JSON.stringify(fresh.map((e) => e.action).sort()) === JSON.stringify([...EXPECTED_ACTIONS].sort()), JSON.stringify(fresh.map((e) => e.action)))
  check('audit (API): newest first (occurred_at never increases)', firstPage.body.events.every((e, i, a) => i === 0 || a[i - 1].occurred_at >= e.occurred_at))
  const uiActions = await rowActions()
  check('audit (UI): the top rows are those events in the API order (newest first)', JSON.stringify(uiActions.slice(0, fresh.length)) === JSON.stringify(fresh.map((e) => e.action)), JSON.stringify(uiActions.slice(0, fresh.length + 1)))
  const sg = fresh.find((e) => e.action === 'group.create')
  check('audit: group.create targets the smoke-group id and carries the actor', sg?.target_id === smokeGroup.id && sg?.actor_email === ADMIN.email)
  check('audit: user.disable / user.enable target carol', fresh.filter((e) => e.action.startsWith('user.')).every((e) => e.target_id === carolUser.id))
  check('audit: no event of this section touches the admin user', !fresh.some((e) => e.target_id === adminUser.id))
  check('audit: every <details> is collapsed at load', (await page.locator('details[open]').count()) === 0)

  const createRow = auditRows().filter({ hasText: smokeGroup.id }).filter({ hasText: 'group.create' })
  check('audit: the group.create row of smoke-group is in the table', (await createRow.count()) === 1, String(await createRow.count()))
  await createRow.locator('summary').click()
  const pre = createRow.locator('details pre').first()
  await pre.waitFor({ timeout: DATA_TIMEOUT })
  const preInfo = await pre.evaluate((el) => ({ text: el.textContent, children: el.children.length }))
  let parsed = null
  try { parsed = JSON.parse(preInfo.text) } catch { /* reported below */ }
  check('audit: details JSON renders as text (a <pre> with no child elements, parses as JSON)', preInfo.children === 0 && parsed !== null, JSON.stringify(preInfo).slice(0, 200))
  check('audit: the details text carries the smoke-group key', preInfo.text.includes(SMOKE_GROUP), preInfo.text.slice(0, 200))
  await page.screenshot({ path: `${OUT}/audit-details.png`, fullPage: true })
  await checkStyled('audit (admin)')
  await report('audit (admin)')

  const loadMore = page.getByRole('button', { name: 'Load more', exact: true })
  if (await loadMore.count()) {
    const before = (await auditRows().count()) - 1
    await Promise.all([page.waitForResponse((r) => r.url().includes('/api/v1/audit?cursor=') && r.status() === 200), loadMore.click()])
    await page.waitForFunction((n) => document.querySelectorAll('table tbody tr').length > n, before, { timeout: DATA_TIMEOUT })
    check('audit: "Load more" appends rows', (await auditRows().count()) - 1 > before, `${before} -> ${(await auditRows().count()) - 1}`)
    await report('audit load more')
  } else {
    console.log('  SKIP: audit "Load more" (the log fits one page)')
  }

  reset()
  const filterSelect = page.getByLabel('Initiative', { exact: true })
  await filterSelect.locator('option', { hasText: 'Demo (demo)' }).waitFor({ state: 'attached', timeout: DATA_TIMEOUT })
  check('audit filter: options start with "All initiatives"', (await filterSelect.locator('option').first().innerText()).trim() === 'All initiatives')
  await Promise.all([page.waitForResponse((r) => r.url().includes(`/api/v1/audit?initiative_id=${demoId}`) && r.status() === 200), filterSelect.selectOption({ label: 'Demo (demo)' })])
  await page.waitForFunction(() => !document.querySelector('[aria-busy=true]'), null, { timeout: DATA_TIMEOUT })
  const initiativeCells = async () => Promise.all((await auditRows().all()).slice(1).map((r) => r.locator('td, th').nth(4).innerText().then((t) => t.trim())))
  const cells = await initiativeCells()
  check('audit filter demo: there are rows and every one belongs to Demo', cells.length > 0 && cells.every((c) => c === 'Demo'), JSON.stringify([...new Set(cells)]))
  check('audit filter demo: none of this section\'s group/user events (they have no initiative)', !(await rowActions()).some((a) => EXPECTED_ACTIONS.includes(a)))
  await report('audit filter demo')

  console.log('  -- dave (manager of demo): Nav without Groups/Users, Users refused, Audit scoped to demo')
  await login(DAVE.email)
  reset()
  await page.goto(`${BASE}/admin/initiatives`)
  await page.getByRole('table').getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  check('dave Nav: Initiatives and Audit only', JSON.stringify(await navLinks()) === '["Initiatives","Audit"]', JSON.stringify(await navLinks()))
  await report('nav (dave)')
  for (const [path, label] of [['/admin/users', 'Users'], ['/admin/groups', 'Groups']]) {
    reset()
    await page.goto(`${BASE}${path}`)
    await page.getByText('Platform administrators only').first().waitFor({ timeout: DATA_TIMEOUT })
    check(`dave ${path}: "Platform administrators only"`, true)
    check(`dave ${path}: no search field, no table, no New group button`, (await page.getByRole('textbox').count()) === 0 && (await page.getByRole('table').count()) === 0 && (await page.getByRole('button', { name: 'New group' }).count()) === 0)
    if (path === '/admin/users') await page.screenshot({ path: `${OUT}/users-manager.png`, fullPage: true })
    await report(`${path} (dave)`)
  }
  reset()
  await page.goto(`${BASE}/admin/initiatives`)
  await page.getByRole('navigation', { name: 'Administration', exact: true }).getByRole('link', { name: 'Audit', exact: true }).click()
  await page.waitForURL(`${BASE}/admin/audit`, { timeout: DATA_TIMEOUT })
  await auditRows().nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const dFilter = page.getByLabel('Initiative', { exact: true })
  await dFilter.locator('option', { hasText: 'Demo (demo)' }).waitFor({ state: 'attached', timeout: DATA_TIMEOUT })
  const dOptions = (await dFilter.locator('option').allInnerTexts()).map((t) => t.trim())
  check('dave audit: filter options are "All my initiatives" and Demo only', JSON.stringify(dOptions) === '["All my initiatives","Demo (demo)"]', JSON.stringify(dOptions))
  const dCells = await initiativeCells()
  check('dave audit: there are rows and every one belongs to Demo', dCells.length > 0 && dCells.every((c) => c === 'Demo'), JSON.stringify([...new Set(dCells)]))
  await page.screenshot({ path: `${OUT}/audit-manager.png`, fullPage: true })
  await report('audit (dave)')

  console.log('  -- admin: groups, demo-editors members and carol equal the starting set')
  await login(ADMIN.email)
  await page.goto(`${BASE}/admin/initiatives`)
  await page.getByRole('table').getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const groupsEnd = await groupsSnapshot()
  check('groups, demo-editors members and carol (API) equal the starting set', groupsEnd === groupsStart, `${groupsStart} != ${groupsEnd}`)
  // ---- 5e: integration health. Read-only. The UI must show exactly what GET /apps/{id}/health and GET /health/orphan-hosts return.
  console.log('  -- admin: demo Apps health link, app health page (UI == API), probe badge in the Apps table')
  await login(ADMIN.email)
  reset()
  await page.goto(`${BASE}/admin/initiatives/${demoId}/apps`)
  await appRow(helloA).waitFor({ timeout: DATA_TIMEOUT })
  const apiHealth = async () => (await apiCall('GET', `/apps/${helloA.id}/health`)).body
  let hh = await apiHealth()
  check('apps table: hello-a host shows the probe badge of the API', (await appRow(helloA).innerText()).includes(probeLabel(hh.app.deployments.find((d) => d.host === ALICE.apps[0].host)?.last_probe_status ?? null)), `${probeLabel(hh.app.deployments[0]?.last_probe_status ?? null)} not in ${squash(await appRow(helloA).innerText())}`)
  const probeOf = (hh0) => hh0.app.deployments.find((d) => d.host === ALICE.apps[0].host)?.last_probe_status
  if (probeOf(hh) === 'ok') check('apps table: an ok probe reads "Redirects to sign-in"', (await appRow(helloA).innerText()).includes('Redirects to sign-in'))
  await report('apps table (probe badge)')
  reset()
  const healthLink = appRow(helloA).getByRole('link', { name: /^Health of app hello-a/ })
  check('apps table: hello-a has a "Health" link', (await healthLink.count()) === 1 && squash(await healthLink.innerText()) === 'Health')
  await healthLink.click()
  const healthUrl = `${BASE}/admin/initiatives/${demoId}/apps/${helloA.id}/health`
  await page.waitForURL(healthUrl, { timeout: DATA_TIMEOUT })
  await page.getByRole("heading", { level: 2 }).filter({ hasText: "Health of " }).first().waitFor({ timeout: DATA_TIMEOUT })
  hh = await apiHealth() // the page and this read are a moment apart: compare against the same snapshot below
  const checkedAt = await page.locator('section[aria-labelledby=health-summary]').innerText()
  check('health page: heading names the app (hello-a)', (await page.getByRole("heading", { level: 2 }).filter({ hasText: "Health of " }).first().innerText()).includes('hello-a'))
  check('health page: "Back to apps" link goes to the Apps list', (await page.getByRole('link', { name: 'Back to apps', exact: true }).first().getAttribute('href')) === `/admin/initiatives/${demoId}/apps`)
  check('health page: summary shows the app and initiative status and the grant counts of the API', ['Active'].every((t) => checkedAt.includes(t)) && checkedAt.includes(String(hh.grant_count)) && checkedAt.includes(String(hh.effective_manager_grants)), squash(checkedAt))
  const findings = page.locator('section[aria-labelledby=health-findings]')
  const hostRows = page.locator('section[aria-labelledby=health-hosts] tbody tr')
  const apiAppProblems = hh.problems.filter((p) => p.host === null)
  const apiHostProblems = (host) => hh.problems.filter((p) => p.host === host)
  const expectedItems = (list) => list.map((p) => squash(`${PROBLEM_COPY[p.code]?.[0] ?? '?'} ${PROBLEM_COPY[p.code]?.[1] ?? '?'}`))
  check(`health page: API codes are all known to the check (${JSON.stringify(hh.problems.map((p) => p.code))})`, hh.problems.every((p) => p.code in PROBLEM_COPY))
  if (hh.problems.length === 0) {
    check('health page: API has no problems -> "No findings" in Findings', (await findings.innerText()).includes('No findings'))
  }
  const uiAppItems = (await findings.locator('li').allInnerTexts()).map(squash)
  check('health page: Findings lists exactly the app-level problems of the API, as their fixed sentences, in order', JSON.stringify(uiAppItems) === JSON.stringify(expectedItems(apiAppProblems)), `${JSON.stringify(uiAppItems)} vs ${JSON.stringify(expectedItems(apiAppProblems))}`)
  const apiHostLevel = hh.problems.filter((p) => p.host !== null)
  if (apiHostLevel.length) check('health page: Findings says how many host findings are listed below', (await findings.innerText()).includes(apiHostLevel.length === 1 ? '1 finding about hosts' : `${apiHostLevel.length} findings about hosts`), squash(await findings.innerText()))
  else check('health page: no "findings about hosts" line when the API has none', !(await findings.innerText()).includes('about hosts'))
  check('health page: hosts table has one row per host of the API', (await hostRows.count()) === hh.app.deployments.length, `${await hostRows.count()} vs ${hh.app.deployments.length}`)
  for (const d of hh.app.deployments) {
    const row = hostRows.filter({ hasText: d.host })
    check(`health page: host ${d.host} row exists once`, (await row.count()) === 1)
    const cells = await row.locator('td, th').allInnerTexts().then((c) => c.map(squash))
    check(`health page: ${d.host} Last seen ${d.last_seen_at ? 'is a time' : 'is "Never"'} (as the API)`, d.last_seen_at ? (await row.locator('td, th').nth(1).locator('time').getAttribute('datetime')) === d.last_seen_at : cells[1] === 'Never', JSON.stringify(cells))
    check(`health page: ${d.host} probe badge reads "${probeLabel(d.last_probe_status)}" (API ${d.last_probe_status})`, cells[2]?.startsWith(probeLabel(d.last_probe_status)), JSON.stringify(cells))
    check(`health page: ${d.host} Last probe ${d.last_probe_at ? 'has a time' : 'says "No probe yet"'}`, d.last_probe_at ? (await row.locator('td, th').nth(2).locator('time').getAttribute('datetime')) === d.last_probe_at : cells[2].includes('No probe yet'), JSON.stringify(cells))
    const items = (await row.locator('td, th').nth(3).locator('li').allInnerTexts()).map(squash)
    const mine = apiHostProblems(d.host)
    check(`health page: ${d.host} findings == the API's (${mine.map((p) => p.code).join(', ') || 'none'}), as sentences`, JSON.stringify(items) === JSON.stringify(expectedItems(mine)) && (mine.length > 0 || cells[3] === 'No findings'), `${JSON.stringify(items)} / ${cells[3]}`)
  }
  check('health page: the hosts shown include hello-a\'s host', (await hostRows.filter({ hasText: ALICE.apps[0].host }).count()) === 1)
  const totalShown = uiAppItems.length + (await hostRows.locator('li').count())
  check(`health page: total findings shown (${totalShown}) == total returned by the API (${hh.problems.length}); none extra`, totalShown === hh.problems.length)
  check('health page: host names are text, not links', (await page.locator('section[aria-labelledby=health-hosts] a').count()) === 0)
  await page.screenshot({ path: `${OUT}/app-health.png`, fullPage: true })
  await checkStyled('app health (admin)')
  await report('app health (admin)')
  const stableCheck = await apiHealth()
  if (JSON.stringify(stableCheck.problems) !== JSON.stringify(hh.problems)) console.log('  NOTE: the API problems changed while the page was being checked')

  // Not-found: an app id that does not exist, and the right app under another initiative (route mismatch).
  reset()
  await page.goto(`${BASE}/admin/initiatives/${demoId}/apps/app_00000000000000000000000000/health`)
  await page.getByText('App not found').first().waitFor({ timeout: DATA_TIMEOUT })
  check('health page: unknown app -> "App not found"', await page.getByText('App not found').first().isVisible())
  await report('app health (unknown app)', { tolerated: ['status of 404', 'status of 400'], toleratedRequests: failedRequests.filter((r) => r.includes('/api/v1/apps/')) })

  console.log('  -- orphan hosts: a host no app declares is refused as unknown_host; /admin/health lists it (UI == API)')
  // Traffic through Traefik for a host with a route (smoke-orphan.yaml) and no deployment: ForwardAuth answers 403 and queues an
  // unknown_host denial (contract block 2: written by a background goroutine, at most one row per host per 10 minutes per replica).
  // The host is unique per run, so it must be ABSENT from the API before the denial and PRESENT after: a new denial was recorded.
  const orphanApi = async () => (await apiCall('GET', '/health/orphan-hosts')).body?.hosts ?? null
  const before = await orphanApi()
  check(`orphan: ${ORPHAN_HOST} is not listed by the API before the denial`, Array.isArray(before) && !before.some((h) => h.host === ORPHAN_HOST), JSON.stringify(before?.map((h) => h.host)))
  let orphanStatus = 0
  for (let i = 0; i < 30 && orphanStatus !== 403; i++) {
    orphanStatus = await fetch(`https://${ORPHAN_HOST}/`, { redirect: 'manual', signal: AbortSignal.timeout(5000) }).then((r) => r.status).catch(() => 0)
    if (orphanStatus !== 403) await new Promise((r) => setTimeout(r, 1000))
  }
  check(`orphan: ${ORPHAN_HOST} through Traefik is refused by ForwardAuth (403)`, orphanStatus === 403, String(orphanStatus))
  let apiHosts = null
  for (let i = 0; i < 20; i++) {
    apiHosts = await orphanApi()
    if (apiHosts?.some((h) => h.host === ORPHAN_HOST)) break
    await new Promise((r) => setTimeout(r, 1000))
  }
  check(`orphan: the API lists ${ORPHAN_HOST} (within 20 s)`, !!apiHosts?.some((h) => h.host === ORPHAN_HOST), JSON.stringify(apiHosts?.map((h) => h.host)))
  reset()
  await page.goto(`${BASE}/admin/initiatives`)
  await page.getByRole('table').getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  await adminNav().getByRole('link', { name: 'Health', exact: true }).click()
  await page.waitForURL(`${BASE}/admin/health`, { timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 1, name: 'Health' }).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByRole('table').waitFor({ timeout: DATA_TIMEOUT })
  check('health: Nav current is Health', JSON.stringify(await adminNav().locator('[aria-current=page]').allInnerTexts()) === '["Health"]')
  apiHosts = await orphanApi()
  const orphanRows = page.getByRole('table').locator('tbody tr')
  const uiOrphans = await Promise.all((await orphanRows.all()).map(async (r) => {
    const c = (await r.locator('td, th').allInnerTexts()).map(squash)
    return { host: c[0], count: Number(c[1]), tags: await r.locator('td, th').first().locator('a').count() }
  }))
  if (apiHosts.length === 0) check('orphan: API empty -> the empty state copy', (await bodyText()).includes(ORPHAN_EMPTY_COPY))
  check(`orphan: UI hosts == API hosts, same order (${apiHosts.length})`, JSON.stringify(uiOrphans.map((o) => o.host)) === JSON.stringify(apiHosts.map((h) => h.host)), `${JSON.stringify(uiOrphans.map((o) => o.host))} vs ${JSON.stringify(apiHosts.map((h) => h.host))}`)
  check('orphan: UI counts == API counts', JSON.stringify(uiOrphans.map((o) => o.count)) === JSON.stringify(apiHosts.map((h) => h.count)))
  const mine = apiHosts.find((h) => h.host === ORPHAN_HOST)
  check(`orphan: ${ORPHAN_HOST} shows in the table with count >= 1 and a last denied time`, !!mine && mine.count >= 1 && (await orphanRows.filter({ hasText: ORPHAN_HOST }).locator('time').first().getAttribute('datetime')) === mine.last_denied_at)
  check('orphan: hosts are rendered as text (no links anywhere in the table or in the host cells)', (await page.getByRole('table').locator('a').count()) === 0 && uiOrphans.every((o) => o.tags === 0))
  check(`orphan: ${ORPHAN_HOST} is listed as text in the UI`, (await page.getByRole('table').getByText(ORPHAN_HOST, { exact: true }).count()) === 1)
  check('orphan: no link to the orphan host anywhere on the page', (await page.locator(`a[href*="${ORPHAN_HOST}"]`).count()) === 0)
  await page.screenshot({ path: `${OUT}/orphan-hosts.png`, fullPage: true })
  await checkStyled('health (admin)')
  await report('health (admin)')

  console.log('  -- dave (manager of demo): app health page of hello-a is accessible; /admin/health is for platform admins; Nav has no Health')
  await login(DAVE.email)
  reset()
  await page.goto(`${BASE}/admin/initiatives/${demoId}/apps`)
  await appRow(helloA).waitFor({ timeout: DATA_TIMEOUT })
  check('dave apps: hello-a has the Health link', (await appRow(helloA).getByRole('link', { name: /^Health of app hello-a/ }).count()) === 1)
  await appRow(helloA).getByRole('link', { name: /^Health of app hello-a/ }).click()
  await page.waitForURL(healthUrl, { timeout: DATA_TIMEOUT })
  await page.getByRole("heading", { level: 2 }).filter({ hasText: "Health of " }).first().waitFor({ timeout: DATA_TIMEOUT })
  const daveHealth = await apiHealth()
  const dFindings = page.locator('section[aria-labelledby=health-findings]') // the page changes with each login: locators are rebuilt
  const dHostRows = page.locator('section[aria-labelledby=health-hosts] tbody tr')
  const daveItems = (await dFindings.locator('li').allInnerTexts()).map(squash)
  check('dave health page: renders hello-a with its hosts table', (await dHostRows.count()) === daveHealth.app.deployments.length && (await dHostRows.filter({ hasText: ALICE.apps[0].host }).count()) === 1)
  check('dave health page: findings == the API\'s app-level problems', JSON.stringify(daveItems) === JSON.stringify(expectedItems(daveHealth.problems.filter((p) => p.host === null))))
  check('dave nav: Initiatives and Audit only (no Health)', JSON.stringify(await navLinks()) === '["Initiatives","Audit"]', JSON.stringify(await navLinks()))
  await page.screenshot({ path: `${OUT}/app-health-manager.png`, fullPage: true })
  await report('app health (dave)')
  reset()
  await page.goto(`${BASE}/admin/health`)
  await page.getByText('Platform administrators only').first().waitFor({ timeout: DATA_TIMEOUT })
  check('dave /admin/health: "Platform administrators only"', true)
  check('dave /admin/health: no table and no orphan host in the page', (await page.getByRole('table').count()) === 0 && !(await bodyText()).includes(ORPHAN_HOST))
  check('dave /admin/health: nav has no Health', !(await navLinks()).includes('Health'), JSON.stringify(await navLinks()))
  await page.screenshot({ path: `${OUT}/health-manager.png`, fullPage: true })
  await report('/admin/health (dave)')
  const daveOrphans = await apiCall('GET', '/health/orphan-hosts')
  check('dave: GET /health/orphan-hosts is refused by the API (403)', daveOrphans.status === 403, String(daveOrphans.status))
  reset()
} catch (e) {
  fail(`browser scenario aborted: ${e.message.split('\n')[0]} (url ${page.url().split('?')[0]})`)
  await page.screenshot({ path: `${OUT}/aborted.png` }).catch(() => {})
} finally {
  await browser.close()
}

if (fails > 0) { console.log(`  browser check: ${fails} failure(s); screenshots in out/`); process.exit(1) }
console.log(`  browser check: all scenarios passed; screenshots in out/`)
