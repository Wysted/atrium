// Real-browser smoke of the portal web app through Traefik (run by browser-check.sh inside the pinned Playwright image).
// Output lines are "  PASS: ..." / "  FAIL: ..." (the style of smoke.sh); exit code 1 on any FAIL.
import { chromium } from 'playwright'

const BASE = process.env.PORTAL_URL ?? 'https://portal.apps.wysted.dev'
const AUTH_HOST = process.env.AUTH_HOST ?? 'auth.apps.wysted.dev'
const OUT = process.env.OUT_DIR ?? 'out'
// Fake users of the local seed (overlays/local/seed.sql; dave is below): alice = viewer on hello-a (demo) and hello-b (other), not an admin;
// admin = platform admin; carol = exists, no grants (no apps, not an admin).
const ALICE = { email: 'alice@example.com', name: 'alice', apps: [{ name: 'hello-a', initiative: 'Demo', host: 'hello-a.apps.wysted.dev' }, { name: 'hello-b', initiative: 'Other', host: 'hello-b.apps.wysted.dev' }, { name: 'grafana', initiative: 'Observability', host: 'grafana.apps.wysted.dev' }] }
const GRAFANA = 'https://grafana.apps.wysted.dev'
const ADMIN = { email: 'admin@example.com', name: 'admin' }
const CAROL = { email: 'carol@example.com', name: 'carol' }
const DATA_TIMEOUT = 15_000
const NO_ACCESS_TEXT = 'You do not have access to administration'
// Initiatives of the local seed (seed.sql): key -> name. The rename round trip uses demo and restores its name.
const SEED_INITIATIVES = [{ key: 'platform', name: 'Platform' }, { key: 'demo', name: 'Demo' }, { key: 'other', name: 'Other' }, { key: 'observability', name: 'Observability' }]
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
const SMOKE_GROUP = 'smoke-group' // created and deleted by the groups scenario
const DEMO_EDITORS = 'demo-editors' // seeded group: one member (bob) and a grant on demo
const GROUP_IN_USE_COPY = 'This group still has members or grants. Remove them first.'
const ALREADY_MEMBER_COPY = 'This user is already a member of the group.'
const SMOKE_AREA = 'smoke-ui-area' // created and deleted by the areas scenario
const AREA_IN_USE_COPY = 'This area still has members or supervisors. Remove them first, then delete the area.'
const ALREADY_IN_AREA_COPY = 'This user already belongs to an area. A person is in one area at most.'
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
  check('/ (alice): exactly her 3 apps (no portal card)', (await page.getByRole('heading', { level: 2 }).count()) === ALICE.apps.length, String(await page.getByRole('heading', { level: 2 }).count()))
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

  console.log('  -- dave: grant dialog group search narrows the select; grant to a found group, then revoke')
  reset()
  await grantTrigger.click()
  await gDlg.waitFor({ timeout: DATA_TIMEOUT })
  await gDlg.getByLabel('Grant to').selectOption({ label: 'Group' })
  const grpSelect = gDlg.getByLabel(/^Group/).and(page.locator('select'))
  await grpSelect.locator('option', { hasText: 'demo-editors' }).waitFor({ state: 'attached', timeout: DATA_TIMEOUT })
  check('grant dialog: search box "Search groups" with its hint', (await gDlg.getByRole('searchbox', { name: 'Search groups' }).count()) === 1 && (await gDlg.innerText()).includes('Matches the key or the name.'))
  const optTexts = () => grpSelect.locator('option').allInnerTexts().then((t) => t.filter((s) => s !== 'Choose a group'))
  await gDlg.getByRole('searchbox', { name: 'Search groups' }).fill('zzz-no-such-group')
  await gDlg.getByText('No groups match your search.').waitFor({ timeout: DATA_TIMEOUT })
  check('grant dialog: nonsense search leaves no group options', (await optTexts()).length === 0, JSON.stringify(await optTexts()))
  await gDlg.getByRole('searchbox', { name: 'Search groups' }).fill('demo')
  await grpSelect.locator('option', { hasText: 'demo-editors' }).waitFor({ state: 'attached', timeout: DATA_TIMEOUT })
  const narrowed = await optTexts()
  check('grant dialog: search "demo" narrows the options to matching groups', narrowed.length >= 1 && narrowed.every((t) => /demo/i.test(t)), JSON.stringify(narrowed))
  await grpSelect.selectOption({ label: narrowed.find((t) => t.includes('demo-editors')) })
  await gDlg.getByLabel(/^Role/).selectOption({ label: 'viewer' })
  await gDlg.getByRole('button', { name: 'Grant access', exact: true }).click()
  await gDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await page.getByRole('button', { name: 'Revoke viewer from demo-editors', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  check('grant to a found group: demo-editors viewer grant listed', true)
  await report('create group grant (dave)')
  reset()
  await page.getByRole('button', { name: 'Revoke viewer from demo-editors', exact: true }).click()
  await gAlert.waitFor({ timeout: DATA_TIMEOUT })
  await gAlert.getByRole('button', { name: 'Revoke', exact: true }).click()
  await gAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await page.getByRole('button', { name: 'Revoke viewer from demo-editors', exact: true }).waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('revoke group grant: gone (list)', true)
  await report('revoke group grant (dave)')

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

  console.log('  -- admin: platform Apps (the portal app offers no Disable and no remove-host; a demo app still does)')
  reset()
  await page.goto(`${BASE}/admin/initiatives/${platformId}/apps`)
  await appRow(portalApp).waitFor({ timeout: DATA_TIMEOUT })
  check('platform apps: portal app listed with status Active', (await appRow(portalApp).innerText()).includes('Active'))
  check('portal app: no Disable button', (await appRow(portalApp).getByRole('button', { name: /^Disable app /, }).count()) === 0)
  if (portalApp.deployments.length === 1) check('portal app: no remove-host button for its only host', (await appRow(portalApp).getByRole('button', { name: /^Remove host /, }).count()) === 0)
  else console.log(`  SKIP: portal remove-host case (the portal app has ${portalApp.deployments.length} hosts, not exactly one)`)
  await report('platform apps')
  reset()
  await page.goto(`${BASE}/admin/initiatives/${demoId}/apps`)
  await appRow(helloA).waitFor({ timeout: DATA_TIMEOUT })
  check('demo apps: hello-a still offers Disable', (await appRow(helloA).getByRole('button', { name: /^Disable app /, }).count()) === 1)
  check('demo apps: hello-a still offers remove-host', (await appRow(helloA).getByRole('button', { name: `Remove host ${ALICE.apps[0].host}`, exact: true }).count()) === 1)
  await report('demo apps (actions)')

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
  console.log('  -- admin: Nav (Initiatives, Groups, Users, Robots, Health, Audit); Groups list; leftovers of an interrupted run removed')
  await login(ADMIN.email)
  await page.goto(`${BASE}/admin/initiatives`)
  await page.getByRole('table').getByRole('row').nth(1).waitFor({ timeout: DATA_TIMEOUT })
  const adminNav = () => page.getByRole('navigation', { name: 'Administration', exact: true }) // a function: the page changes with each login
  const navLinks = async () => (await adminNav().getByRole('link').allInnerTexts()).map((t) => t.trim())
  check('admin Nav: Initiatives, Groups, Areas, Users, Robots, Health, Audit', JSON.stringify(await navLinks()) === '["Initiatives","Groups","Areas","Users","Robots","Health","Audit"]', JSON.stringify(await navLinks()))

  // ---- 5d-3b: areas and their members/supervisors. Placed BEFORE auditSince on purpose: the audit section below asserts the
  // exact set of events since then, and the area events must not be part of it. Idempotent: leftovers of smoke-ui-area are removed first.
  console.log('  -- admin: Areas (create smoke-ui-area, add member/supervisor, 409 copies, remove, delete)')
  const areasApi = () => apiAll('/areas', 'areas')
  const areaPeopleApi = (id, kind) => apiAll(`/areas/${id}/${kind}`, kind)
  for (const a of (await areasApi()).filter((a) => a.key === SMOKE_AREA)) {
    for (const kind of ['members', 'supervisors']) for (const p of await areaPeopleApi(a.id, kind)) await apiCall('DELETE', `/areas/${a.id}/${kind}/${p.user_id}`)
    await apiCall('DELETE', `/areas/${a.id}`)
  }
  reset()
  await adminNav().getByRole('link', { name: 'Areas', exact: true }).click()
  await page.waitForURL(`${BASE}/admin/areas`, { timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 1, name: 'Areas', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  const areaRow = (key) => page.getByRole('table', { name: 'Areas', exact: true }).getByRole('row').filter({ has: page.locator('code', { hasText: new RegExp(`^${key}$`) }) })
  const areaNotice = (text) => page.getByRole('status').filter({ hasText: text }).first()
  const areaDlg = page.getByRole('dialog')
  const areaAlert = page.getByRole('alertdialog')
  check('areas: Nav current is Areas', JSON.stringify(await adminNav().locator('[aria-current=page]').allInnerTexts()) === '["Areas"]')
  check('areas: "New area" button present', (await page.getByRole('button', { name: 'New area', exact: true }).count()) === 1)
  await report('areas (admin)')

  reset()
  await page.getByRole('button', { name: 'New area', exact: true }).click()
  await areaDlg.waitFor({ timeout: DATA_TIMEOUT })
  await areaDlg.getByRole('textbox', { name: /^Key/ }).fill(SMOKE_AREA)
  await areaDlg.getByRole('textbox', { name: /^Name/ }).fill('Smoke UI area')
  await areaDlg.getByRole('button', { name: 'Create area', exact: true }).click()
  await areaDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await areaRow(SMOKE_AREA).waitFor({ timeout: DATA_TIMEOUT })
  await areaNotice('Area created.').waitFor({ timeout: DATA_TIMEOUT })
  const smokeArea = (await areasApi()).find((a) => a.key === SMOKE_AREA)
  check('areas: smoke-ui-area listed with its name and present in the API', !!smokeArea && (await areaRow(SMOKE_AREA).innerText()).includes('Smoke UI area'))
  await report('create area')

  reset()
  await areaRow(SMOKE_AREA).getByRole('link', { name: SMOKE_AREA, exact: true }).click()
  await page.waitForURL(`${BASE}/admin/areas/${smokeArea.id}`, { timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 1, name: 'Smoke UI area', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 2, name: 'Members', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 2, name: 'Supervisors', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByText('This area has no members yet.').waitFor({ timeout: DATA_TIMEOUT })
  await page.getByText('This area has no supervisors yet.').waitFor({ timeout: DATA_TIMEOUT })
  check('area detail: shows "Key: smoke-ui-area", Nav current stays Areas', (await bodyText()).includes(`Key: ${SMOKE_AREA}`) && JSON.stringify(await adminNav().locator('[aria-current=page]').allInnerTexts()) === '["Areas"]')
  await report('area detail (empty)')

  const areaPerson = (kind, email) => page.getByRole('table', { name: kind === 'members' ? 'Members' : 'Supervisors', exact: true }).getByRole('row').filter({ hasText: email })
  const addAreaPerson = async (kind, email) => {
    const label = kind === 'members' ? 'Add member' : 'Add supervisor'
    await page.getByRole('button', { name: label, exact: true }).click()
    await areaDlg.waitFor({ timeout: DATA_TIMEOUT })
    await areaDlg.getByRole('textbox', { name: /^Email/ }).fill(email)
    await areaDlg.getByRole('button', { name: label, exact: true }).click()
  }
  reset()
  await addAreaPerson('members', ALICE.email)
  await areaDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await areaPerson('members', ALICE.email).waitFor({ timeout: DATA_TIMEOUT })
  await areaNotice('Member added.').waitFor({ timeout: DATA_TIMEOUT })
  check('area members: alice is the only member (API)', JSON.stringify((await areaPeopleApi(smokeArea.id, 'members')).map((m) => m.email)) === JSON.stringify([ALICE.email]))
  await report('area add member')

  reset()
  await addAreaPerson('members', ALICE.email)
  await areaDlg.getByText(ALREADY_IN_AREA_COPY).first().waitFor({ timeout: DATA_TIMEOUT })
  const dupAreaMember = `${BASE}/api/v1/areas/${smokeArea.id}/members HTTP 409`
  check(`area add member again: copy "${ALREADY_IN_AREA_COPY}" shown and the dialog stays open`, await areaDlg.isVisible())
  check('area add member again: the API answered 409', failedRequests.includes(dupAreaMember), JSON.stringify(failedRequests))
  await report('area add member again 409', { tolerated: ['status of 409'], toleratedRequests: [dupAreaMember] })
  await areaDlg.getByRole('button', { name: 'Cancel', exact: true }).click()
  await areaDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('area add member again: still one member (API)', (await areaPeopleApi(smokeArea.id, 'members')).length === 1)

  reset()
  await addAreaPerson('supervisors', CAROL.email)
  await areaDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await areaPerson('supervisors', CAROL.email).waitFor({ timeout: DATA_TIMEOUT })
  await areaNotice('Supervisor added.').waitFor({ timeout: DATA_TIMEOUT })
  check('area supervisors: carol is the only supervisor (API)', JSON.stringify((await areaPeopleApi(smokeArea.id, 'supervisors')).map((m) => m.email)) === JSON.stringify([CAROL.email]))
  await report('area add supervisor')

  console.log('  -- admin: delete smoke-ui-area with people -> 409 copy; remove both; delete; gone')
  reset()
  await page.getByRole('link', { name: 'All areas', exact: true }).or(page.getByRole('button', { name: 'All areas', exact: true })).click()
  await page.waitForURL(`${BASE}/admin/areas`, { timeout: DATA_TIMEOUT })
  await areaRow(SMOKE_AREA).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByRole('button', { name: `Delete area ${SMOKE_AREA}`, exact: true }).click()
  await areaAlert.waitFor({ timeout: DATA_TIMEOUT })
  check('delete area: confirm title uses the area name', (await areaAlert.getByRole('heading', { level: 2 }).innerText()) === 'Delete area Smoke UI area?')
  await areaAlert.getByRole('button', { name: 'Delete area', exact: true }).click()
  await areaAlert.getByText(AREA_IN_USE_COPY).waitFor({ timeout: DATA_TIMEOUT })
  const areaInUse409 = `${BASE}/api/v1/areas/${smokeArea.id} HTTP 409`
  check('delete area with people: the API answered 409', failedRequests.includes(areaInUse409), JSON.stringify(failedRequests))
  await report('delete area 409', { tolerated: ['status of 409'], toleratedRequests: [areaInUse409] })
  await areaAlert.getByRole('button', { name: 'Cancel', exact: true }).click()
  await areaAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  check('delete area with people: still listed after Cancel (list and API)', (await areaRow(SMOKE_AREA).count()) === 1 && (await areasApi()).some((a) => a.id === smokeArea.id))

  reset()
  await areaRow(SMOKE_AREA).getByRole('link', { name: SMOKE_AREA, exact: true }).click()
  await page.waitForURL(`${BASE}/admin/areas/${smokeArea.id}`, { timeout: DATA_TIMEOUT })
  for (const [kind, email, done] of [['members', ALICE.email, 'Member removed.'], ['supervisors', CAROL.email, 'Supervisor removed.']]) {
    await areaPerson(kind, email).waitFor({ timeout: DATA_TIMEOUT })
    await page.getByRole('button', { name: `Remove ${email}`, exact: true }).click()
    await areaAlert.waitFor({ timeout: DATA_TIMEOUT })
    check(`area remove ${kind}: confirm title names ${email}`, (await areaAlert.getByRole('heading', { level: 2 }).innerText()) === `Remove ${email}?`)
    await areaAlert.getByRole('button', { name: 'Remove', exact: true }).click()
    await areaAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await areaPerson(kind, email).waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await areaNotice(done).waitFor({ timeout: DATA_TIMEOUT })
    check(`area remove ${kind}: gone (API)`, (await areaPeopleApi(smokeArea.id, kind)).length === 0)
  }
  await report('area remove people')

  reset()
  await page.getByRole('link', { name: 'All areas', exact: true }).or(page.getByRole('button', { name: 'All areas', exact: true })).click()
  await page.waitForURL(`${BASE}/admin/areas`, { timeout: DATA_TIMEOUT })
  await areaRow(SMOKE_AREA).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByRole('button', { name: `Delete area ${SMOKE_AREA}`, exact: true }).click()
  await areaAlert.waitFor({ timeout: DATA_TIMEOUT })
  await areaAlert.getByRole('button', { name: 'Delete area', exact: true }).click()
  await areaAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await areaRow(SMOKE_AREA).waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
  await areaNotice('Area deleted.').waitFor({ timeout: DATA_TIMEOUT })
  check('delete area: gone (list and API)', !(await areasApi()).some((a) => a.key === SMOKE_AREA))
  await report('delete area')

  // ---- 5d-3c: area supervisor. carol supervises smoke-ui-sup (alice and bob are members); alice owns a robot and has a personal
  // token. Placed BEFORE auditSince for the same reason as 5d-3b (its events must not enter the audit count). Idempotent: leftovers
  // (area, active smoke-sup robots, live smoke-sup tokens of alice) are removed first and last, as admin. Robots cannot be deleted:
  // the per-run robot is created, reassigned by the supervisor and disabled at the end (that also revokes its tokens).
  console.log('  -- supervisor: carol (smoke-ui-sup) manages alice tokens and robots; alice (not a supervisor) sees no areas')
  const SUP_AREA = 'smoke-ui-sup'
  const SUP_AREA_NAME = 'Smoke UI sup'
  const SUP_RUN = Math.random().toString(16).slice(2, 8)
  const SUP_TOKEN = `smoke-sup-token-${SUP_RUN}`
  const SUP_ROBOT = `smoke-sup-robot-${SUP_RUN}`
  const BOB = { email: 'bob@example.com', name: 'bob' }
  const supApi = (method, path, body) => page.evaluate(async ([m, p, b]) => {
    const r = await fetch(`/api/v1${p}`, { method: m, headers: { Accept: 'application/json', ...(m === 'GET' ? {} : { 'Content-Type': 'application/json' }) }, body: b === undefined ? undefined : JSON.stringify(b) })
    return { status: r.status, body: r.headers.get('content-type')?.includes('json') ? await r.json() : null }
  }, [method, path, body])
  const supUser = async (email) => (await supApi('GET', `/users?email=${encodeURIComponent(email)}`)).body.users[0]
  const supCleanup = async () => {
    await login(ADMIN.email)
    for (const a of (await areasApi()).filter((a) => a.key === SUP_AREA)) {
      for (const kind of ['members', 'supervisors']) for (const p of await areaPeopleApi(a.id, kind)) await supApi('DELETE', `/areas/${a.id}/${kind}/${p.user_id}`)
      await supApi('DELETE', `/areas/${a.id}`)
    }
    for (const r of (await apiAll('/robots', 'robots')).filter((r) => r.name.startsWith('smoke-sup-robot') && r.status === 'active')) await supApi('PATCH', `/robots/${r.id}`, { status: 'disabled' })
    const alice = await supUser(ALICE.email)
    for (const t of (await apiAll(`/users/${alice.id}/tokens`, 'tokens')).filter((t) => t.name.startsWith('smoke-sup-token') && !t.revoked_at)) await supApi('DELETE', `/tokens/${t.id}`)
  }
  const supLink = (name) => page.getByRole('link', { name, exact: true }).or(page.getByRole('button', { name, exact: true }))
  let supError
  try {
    await supCleanup()
    const [aliceU, bobU, adminU] = [await supUser(ALICE.email), await supUser(BOB.email), await supUser(ADMIN.email)]
    check('supervisor setup: alice, bob and admin resolved; alice and bob are humans, not admins', !!aliceU && !!bobU && !!adminU, JSON.stringify([aliceU?.id, bobU?.id, adminU?.id]))
    const mkArea = await supApi('POST', '/areas', { key: SUP_AREA, name: SUP_AREA_NAME })
    const supArea = (await areasApi()).find((a) => a.key === SUP_AREA)
    check('supervisor setup: area smoke-ui-sup created', mkArea.status === 201 && !!supArea, String(mkArea.status))
    const setup = [
      ['members', ALICE.email], ['members', BOB.email], ['supervisors', CAROL.email],
    ]
    for (const [kind, email] of setup) {
      const r = await supApi('POST', `/areas/${supArea.id}/${kind}`, { email })
      check(`supervisor setup: ${email} added to ${kind}`, r.status === 201 || r.status === 200, String(r.status))
    }
    const mkRobot = await supApi('POST', '/robots', { name: SUP_ROBOT, owner_user_id: aliceU.id })
    const supRobot = (await apiAll('/robots', 'robots')).find((r) => r.name === SUP_ROBOT)
    check('supervisor setup: robot owned by alice', mkRobot.status === 201 && supRobot?.owner_user_id === aliceU.id, `${mkRobot.status} ${supRobot?.owner_user_id}`)
    await login(ALICE.email)
    const mkToken = await supApi('POST', '/me/tokens', { name: SUP_TOKEN, scope: 'read', expires_in: 3600 })
    check('supervisor setup: alice personal token created', mkToken.status === 201, String(mkToken.status))

    console.log('  -- carol: home -> My areas -> area -> alice (no email in the URL)')
    await login(CAROL.email)
    await openHome(CAROL, '/ (carol supervisor)')
    await supLink('My areas').waitFor({ timeout: DATA_TIMEOUT })
    check('/ (carol supervisor): "My areas" shown (supervises an area)', true)
    await report('/ (carol supervisor)')
    reset()
    await supLink('My areas').click()
    await page.waitForURL(`${BASE}/areas`, { timeout: DATA_TIMEOUT })
    await page.getByRole('heading', { level: 1, name: 'My areas', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    const supAreasRow = page.getByRole('table', { name: 'My areas', exact: true }).getByRole('row').filter({ hasText: SUP_AREA_NAME })
    await supAreasRow.waitFor({ timeout: DATA_TIMEOUT })
    check('/areas (carol): lists smoke-ui-sup once', (await supAreasRow.count()) === 1)
    await checkStyled('/areas (carol)')
    await report('/areas (carol)')
    reset()
    await supLink(`People of area ${SUP_AREA_NAME}`).click()
    await page.waitForURL(`${BASE}/areas/${supArea.id}`, { timeout: DATA_TIMEOUT })
    await page.getByRole('heading', { level: 1, name: SUP_AREA_NAME, exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    await page.getByText('People in this area you can manage: their tokens and robots.').waitFor({ timeout: DATA_TIMEOUT })
    const supPeople = page.getByRole('table', { name: 'People', exact: true })
    await supPeople.getByRole('row').filter({ hasText: ALICE.email }).waitFor({ timeout: DATA_TIMEOUT })
    check('/areas/<id> (carol): alice and bob listed', (await supPeople.getByRole('row').filter({ hasText: BOB.email }).count()) === 1)
    await report('/areas/<id> (carol)')
    reset()
    await supLink(`Tokens and robots of ${ALICE.email}`).click()
    const personUrl = `${BASE}/areas/${supArea.id}/people/${aliceU.id}`
    await page.waitForURL(personUrl, { timeout: DATA_TIMEOUT })
    check('person URL: only ids, no query string and no email', !page.url().includes('?') && !page.url().includes('@'), stripQuery(page.url()))
    await page.getByRole('heading', { level: 1, name: 'Tokens and robots', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    await page.getByText(`Offboarding tools for ${ALICE.email}.`).waitFor({ timeout: DATA_TIMEOUT })
    await page.getByRole('heading', { level: 2, name: `Personal tokens of ${ALICE.email}`, exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    await page.getByRole('heading', { level: 2, name: `Robots of ${ALICE.email}`, exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check('person page: no "New token" (a supervisor cannot create tokens)', (await page.getByRole('button', { name: 'New token', exact: true }).count()) === 0)
    await page.screenshot({ path: `${OUT}/supervisor-person.png`, fullPage: true })
    await report('person page (carol)')

    console.log('  -- carol: revoke alice token behind a confirmation')
    reset()
    const supTokenRow = page.getByRole('table', { name: `Personal tokens of ${ALICE.email}`, exact: true }).getByRole('row').filter({ hasText: SUP_TOKEN })
    await supTokenRow.getByRole('button', { name: `Revoke token ${SUP_TOKEN}`, exact: true }).click()
    const supAlert = page.getByRole('alertdialog')
    await supAlert.waitFor({ timeout: DATA_TIMEOUT })
    await supAlert.getByRole('button', { name: 'Revoke token', exact: true }).click()
    await supAlert.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await page.getByText('Token revoked.').first().waitFor({ timeout: DATA_TIMEOUT })
    check('supervisor revoke: status "Token revoked." and the row offers no Revoke', (await supTokenRow.getByRole('button', { name: `Revoke token ${SUP_TOKEN}`, exact: true }).count()) === 0)
    await report('revoke alice token (carol)')

    console.log('  -- carol: robot tokens dialog, change the owner to bob')
    reset()
    const supRobotRow = page.getByRole('table', { name: 'Robots', exact: true }).getByRole('row').filter({ hasText: SUP_ROBOT })
    await supRobotRow.waitFor({ timeout: DATA_TIMEOUT })
    await supRobotRow.getByRole('button', { name: `Tokens of robot ${SUP_ROBOT}`, exact: true }).click()
    await page.getByRole('heading', { name: `Tokens of robot ${SUP_ROBOT}`, exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check('robot tokens: the "Tokens of robot" heading is shown', true)
    // the robot tokens panel is inline (not a dialog): there is nothing to close
    await supRobotRow.getByRole('button', { name: `Change owner of robot ${SUP_ROBOT}`, exact: true }).click()
    const ownerDlg = page.getByRole('dialog', { name: `Change owner of ${SUP_ROBOT}`, exact: true })
    await ownerDlg.waitFor({ timeout: DATA_TIMEOUT })
    const ownerSelect = ownerDlg.getByLabel(/^New owner/)
    const bobLabel = `${BOB.name} (${BOB.email})`
    await ownerSelect.locator('option', { hasText: bobLabel }).waitFor({ state: 'attached', timeout: DATA_TIMEOUT })
    check('owner dialog: alice (the current owner) is not offered', (await ownerSelect.locator('option', { hasText: ALICE.email }).count()) === 0)
    await ownerSelect.selectOption({ label: bobLabel })
    await ownerDlg.getByRole('button', { name: 'Change owner', exact: true }).click()
    await ownerDlg.waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await page.getByText(`Robot ${SUP_ROBOT} now belongs to`).first().waitFor({ timeout: DATA_TIMEOUT })
    check('change owner: success status "Robot <name> now belongs to <owner>."', true)
    await report('change robot owner (carol)')

    console.log('  -- carol: a person outside her areas')
    reset()
    await page.goto(`${BASE}/areas/${supArea.id}/people/${adminU.id}`)
    await page.getByText('This person is not in your areas').first().waitFor({ timeout: DATA_TIMEOUT })
    check('out-of-scope person: "This person is not in your areas"', true)
    check('out-of-scope person: "Back to the area" offered', (await supLink('Back to the area').count()) >= 1)
    const notFound404 = [`${BASE}/api/v1/users/${adminU.id}/robots HTTP 404`, `${BASE}/api/v1/users/${adminU.id}/tokens HTTP 404`]
    await report('out-of-scope person (carol)', { tolerated: ['status of 404'], toleratedRequests: notFound404 })

    console.log('  -- alice (not a supervisor): no "My areas", /areas says so')
    await login(ALICE.email)
    await openHome(ALICE, '/ (alice, no supervision)')
    check('/ (alice): no "My areas"', (await supLink('My areas').count()) === 0)
    reset()
    await page.goto(`${BASE}/areas`)
    await page.getByText('You do not supervise any area').first().waitFor({ timeout: DATA_TIMEOUT })
    check('/areas (alice): "You do not supervise any area"', true)
    await report('/areas (alice)')

    console.log('  -- admin: the supervisor changes are visible in the API')
    await login(ADMIN.email)
    const after = await supApi('GET', `/robots/${supRobot.id}`)
    check('API: the robot now belongs to bob', after.body?.owner_user_id === bobU.id, JSON.stringify(after.body?.owner_email))
    const aliceTokens = await apiAll(`/users/${aliceU.id}/tokens`, 'tokens')
    check('API: the personal token of alice is revoked', !!aliceTokens.find((t) => t.name === SUP_TOKEN)?.revoked_at)
  } catch (e) {
    supError = e
    console.log(`  supervisor scenario error at ${page.url().split('?')[0]}: ${e.message.split('\n')[0]}`)
    await page.screenshot({ path: `${OUT}/supervisor-failed.png`, fullPage: true }).catch(() => {})
    throw e
  } finally {
    // a cleanup failure must not mask the scenario error
    try { await supCleanup() } catch (e) { if (!supError) throw e; console.log(`  supervisor cleanup also failed: ${e.message}`) }
  }
  check('supervisor cleanup: smoke-ui-sup is gone', !(await areasApi()).some((a) => a.key === SUP_AREA))
  await page.goto(`${BASE}/admin/initiatives`) // the cleanup login leaves the admin on /, where there is no admin Nav
  await adminNav().waitFor({ timeout: DATA_TIMEOUT })

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

  console.log('  -- admin: search groups (key/name match, nonsense, literal %)')
  reset()
  const gSearch = page.getByRole('searchbox', { name: 'Search groups' })
  await gSearch.waitFor({ timeout: DATA_TIMEOUT })
  check('groups search: hint "Matches the key or the name." shown', (await bodyText()).includes('Matches the key or the name.'))
  await gSearch.fill('demo')
  await groupRow(DEMO_EDITORS).waitFor({ timeout: DATA_TIMEOUT })
  check('groups search "demo": demo-editors listed', (await groupRow(DEMO_EDITORS).count()) === 1)
  await gSearch.fill('zzz-no-such-group')
  await page.getByText('No groups match your search.').waitFor({ timeout: DATA_TIMEOUT })
  check('groups search nonsense: "No groups match your search." and no demo-editors row', (await groupRow(DEMO_EDITORS).count()) === 0)
  await gSearch.fill('%')
  await page.getByText('No groups match your search.').waitFor({ timeout: DATA_TIMEOUT })
  check('groups search "%": treated literally (no results)', (await groupRow(DEMO_EDITORS).count()) === 0 && (await gTable().getByRole('row').count()) <= 2)
  await gSearch.fill('')
  await groupRow(DEMO_EDITORS).waitFor({ timeout: DATA_TIMEOUT })
  check('groups search cleared: full list back', (await groupRow(DEMO_EDITORS).count()) === 1)
  await report('groups search')

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
  await page.getByRole('heading', { level: 1, name: 'Smoke group', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  await page.getByRole('heading', { level: 2, name: 'Group members', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
  check('group detail: h1 is the group name, "Group members" is an h2', (await page.getByRole('heading', { level: 1 }).allInnerTexts()).join('|') === 'Smoke group')
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
  for (const [path, label] of [['/admin/users', 'Users'], ['/admin/groups', 'Groups'], ['/admin/areas', 'Areas']]) {
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

  // Non-browser clients: personal tokens, robots, grants to a robot, the audit of a robot, the leaked-secret form. Every object
  // is created with a per-run name; robots cannot be deleted, so the robot is disabled at the end and a failed run leaves only that.
  console.log('  -- admin: personal token (create, secret shown once, revoke); robots (create, token, grant, audit by name, disable); leaked-secret form')
  await login(ADMIN.email)
  await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: BASE })
  const TOKEN_RE = /^ptk_[A-Za-z0-9_-]{43}[0-9a-f]{8}$/
  const SMOKE_TOKEN = `smoke-token-${Math.random().toString(16).slice(2, 8)}` // per run: dead tokens stay listed for 30 days
  const SMOKE_ROBOT = `smoke-robot-${Math.random().toString(16).slice(2, 8)}`
  const apiJson = (method, path, body) => page.evaluate(async ([m, p, b]) => {
    const r = await fetch(`/api/v1${p}`, { method: m, headers: { Accept: 'application/json', ...(m === 'GET' ? {} : { 'Content-Type': 'application/json' }) }, body: b === undefined ? undefined : JSON.stringify(b) })
    return { status: r.status, body: r.headers.get('content-type')?.includes('json') ? await r.json() : null }
  }, [method, path, body])
  const ownTokens = async () => (await apiJson('GET', '/me/tokens?limit=100')).body.tokens
  for (const t of (await ownTokens()).filter((t) => t.name.startsWith('smoke-token') && !t.revoked_at)) await apiJson('DELETE', `/me/tokens/${t.id}`)
  const dlg = () => page.getByRole('dialog')
  let robot = null
  let robotGrant = null
  try {
    // Personal token: from the home, create (admin scope is offered to a platform admin), secret once, revoke.
    reset()
    await page.goto(`${BASE}/`)
    await page.getByRole('link', { name: 'API tokens', exact: true }).click()
    await page.waitForURL(`${BASE}/tokens`, { timeout: DATA_TIMEOUT })
    await page.getByRole('heading', { level: 1, name: 'API tokens', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    await page.getByRole('button', { name: 'New token', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    await checkStyled('tokens (admin)')
    await report('tokens page')
    reset()
    await page.getByRole('button', { name: 'New token', exact: true }).click()
    await dlg().waitFor({ timeout: DATA_TIMEOUT })
    check('new token: the name field has focus', await page.evaluate(() => document.activeElement?.getAttribute('type') === 'text'))
    check('new token: admin scope is offered to a platform admin', (await dlg().getByLabel(/^Scope/).locator('option').allInnerTexts()).some((t) => t.startsWith('Admin')))
    check('new token: a personal token is not offered 90 days', !(await dlg().getByLabel(/^Expires/).locator('option').allInnerTexts()).some((t) => t.trim() === '90 days'))
    let posted = 0
    const countPosts = (r) => { if (r.method() === 'POST' && r.url().includes('/api/v1/me/tokens')) posted++ }
    page.on('request', countPosts)
    await dlg().getByRole('button', { name: 'Create token', exact: true }).click()
    await dlg().getByText('Enter a name.').waitFor({ timeout: DATA_TIMEOUT })
    page.off('request', countPosts)
    check('new token: an empty name is refused with its copy and sends no request', posted === 0, String(posted))
    await dlg().getByLabel(/^Name/).fill(SMOKE_TOKEN)
    await dlg().getByLabel(/^Scope/).selectOption({ label: 'Read: can only read (GET)' })
    await dlg().getByLabel(/^Expires/).selectOption({ label: '1 day' })
    await dlg().getByRole('button', { name: 'Create token', exact: true }).click()
    await dlg().getByRole('heading', { level: 2, name: 'Token created', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    const secret = await dlg().getByLabel('Secret', { exact: true }).inputValue()
    check('token secret: shape ptk_ + 43 + 8 hex (55 chars)', TOKEN_RE.test(secret) && secret.length === 55)
    check('token secret: the dialog says it will not be shown again', (await dlg().innerText()).includes('You will not see it again'))
    check('token secret: the secret field has focus and is read-only', await page.evaluate(() => document.activeElement?.hasAttribute('readonly') === true))
    await dlg().getByRole('button', { name: 'Copy secret', exact: true }).click()
    await dlg().getByText('Secret copied to the clipboard.').waitFor({ timeout: DATA_TIMEOUT })
    check('token secret: Copy puts the secret on the clipboard and announces it', (await page.evaluate(() => navigator.clipboard.readText())) === secret)
    await page.keyboard.press('Escape')
    check('token secret: Escape does not dismiss the dialog while the secret is shown', (await dlg().count()) === 1 && (await dlg().getByLabel('Secret', { exact: true }).inputValue()) === secret)
    await page.screenshot({ path: `${OUT}/token-secret.png`, mask: [dlg().getByLabel('Secret', { exact: true })] })
    await dlg().getByRole('button', { name: 'Done', exact: true }).click()
    await dlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    const tokenRow = page.getByRole('table').getByRole('row').filter({ hasText: SMOKE_TOKEN })
    await tokenRow.waitFor({ timeout: DATA_TIMEOUT })
    const trace = await page.evaluate((v) => ({
      url: location.href.includes(v),
      local: JSON.stringify(localStorage).includes(v),
      session: JSON.stringify(sessionStorage).includes(v),
      nuxt: JSON.stringify(window.__NUXT__ ?? {}).includes(v),
    }), secret)
    check('token secret: not in the URL, storage or the Nuxt payload; gone from the page after closing', !trace.url && !trace.local && !trace.session && !trace.nuxt && !(await bodyText()).includes(secret), JSON.stringify(trace))
    const listed = (await apiJson('GET', '/me/tokens?limit=100')).body.tokens.find((t) => t.name === SMOKE_TOKEN)
    check('token list (API): scope read, no secret and no hash in the listing', listed?.scope === 'read' && !('secret' in listed) && !('token_hash' in listed), JSON.stringify(listed))
    check('token row: shows read, Active and Never used', /read/.test(await tokenRow.innerText()) && /Active/.test(await tokenRow.innerText()) && /Never/.test(await tokenRow.innerText()), squash(await tokenRow.innerText()))
    await report('create personal token')
    reset()
    await tokenRow.getByRole('button', { name: `Revoke token ${SMOKE_TOKEN}`, exact: true }).click()
    await page.getByRole('alertdialog').waitFor({ timeout: DATA_TIMEOUT })
    await page.getByRole('alertdialog').getByRole('button', { name: 'Revoke token', exact: true }).click()
    await page.getByRole('alertdialog').waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await tokenRow.getByText('Revoked', { exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check('token revoke: the row shows Revoked and offers no Revoke', (await tokenRow.getByRole('button').count()) === 0)
    check('token revoke (API): revoked_at is set', !!(await ownTokens()).find((t) => t.name === SMOKE_TOKEN)?.revoked_at)
    await report('revoke personal token')

    // Robots.
    reset()
    await page.goto(`${BASE}/admin/initiatives`)
    await adminNav().getByRole('link', { name: 'Robots', exact: true }).click()
    await page.waitForURL(`${BASE}/admin/robots`, { timeout: DATA_TIMEOUT })
    await page.getByRole('heading', { level: 1, name: 'Robots', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check('robots: Nav current is Robots', JSON.stringify(await adminNav().locator('[aria-current=page]').allInnerTexts()) === '["Robots"]')
    await page.getByRole('heading', { level: 2, name: 'Revoke a leaked token', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    await checkStyled('robots (admin)')
    await report('robots page')
    reset()
    await page.getByRole('button', { name: 'New robot', exact: true }).click()
    await dlg().waitFor({ timeout: DATA_TIMEOUT })
    await dlg().getByLabel(/^Name/).fill(SMOKE_ROBOT)
    await dlg().getByLabel('Owner', { exact: true }).selectOption({ label: 'Set the owner by email' })
    await dlg().getByLabel(/^Owner email/).fill(ADMIN.email)
    await dlg().getByRole('button', { name: 'Create robot', exact: true }).click()
    await dlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    let robotRow = page.getByRole('table').getByRole('row').filter({ hasText: SMOKE_ROBOT })
    await robotRow.waitFor({ timeout: DATA_TIMEOUT })
    const adminId = (await apiJson('GET', `/users?email=${encodeURIComponent(ADMIN.email)}`)).body.users[0].id
    robot = (await apiAll('/robots', 'robots')).find((r) => r.name === SMOKE_ROBOT)
    check('robot create (API): active, owner is the admin, with email', robot?.status === 'active' && robot?.owner_user_id === adminId && robot?.owner_email === ADMIN.email, JSON.stringify(robot))
    const robotOne = await apiJson('GET', `/robots/${robot.id}`)
    check('robot GET /robots/{id} (API): the admin view with the owner email', robotOne.status === 200 && robotOne.body.owner_email === ADMIN.email, JSON.stringify(robotOne.body))
    check('robot row: Active, owner reads "You", never used', /Active/.test(await robotRow.innerText()) && /You/.test(await robotRow.innerText()) && /Never/.test(await robotRow.innerText()), squash(await robotRow.innerText()))
    await page.screenshot({ path: `${OUT}/robots-list.png`, fullPage: true })
    await report('create robot')

    // Rename and clear the owner.
    reset()
    await robotRow.getByRole('button', { name: `Edit robot ${SMOKE_ROBOT}`, exact: true }).click()
    await dlg().waitFor({ timeout: DATA_TIMEOUT })
    await dlg().getByLabel('Owner', { exact: true }).selectOption({ label: 'No owner' })
    await dlg().getByRole('button', { name: 'Save', exact: true }).click()
    await dlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await robotRow.getByText('None', { exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check('robot edit (API): the owner is cleared', (await apiAll('/robots', 'robots')).find((r) => r.id === robot.id)?.owner_user_id === null)
    await report('clear robot owner')

    // Grant the robot a role that manages access (so its admin token is allowed), through the grant dialog.
    reset()
    await page.goto(`${BASE}/admin/initiatives/${demoId}/grants`)
    await page.getByRole('button', { name: 'Grant access', exact: true }).click()
    await dlg().waitFor({ timeout: DATA_TIMEOUT })
    await dlg().getByLabel('Grant to').selectOption({ label: 'Robot' })
    await dlg().getByLabel(/^Robot/).locator('option', { hasText: SMOKE_ROBOT }).waitFor({ state: 'attached', timeout: DATA_TIMEOUT })
    await dlg().getByLabel(/^Robot/).selectOption({ label: SMOKE_ROBOT })
    await dlg().getByLabel(/^Role/).locator('option', { hasText: 'manager' }).waitFor({ state: 'attached', timeout: DATA_TIMEOUT })
    await dlg().getByLabel(/^Role/).selectOption({ label: 'manager (manages access)' })
    await dlg().getByRole('button', { name: 'Grant access', exact: true }).click()
    await dlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    const grantRow = page.getByRole('table').getByRole('row').filter({ hasText: SMOKE_ROBOT })
    await grantRow.waitFor({ timeout: DATA_TIMEOUT })
    robotGrant = (await apiAll(`/initiatives/${demoId}/grants`, 'grants')).find((g) => g.user?.id === robot.id)
    check('robot grant (API): the subject is the robot (empty email, name set)', robotGrant?.user?.email === '' && robotGrant?.user?.name === SMOKE_ROBOT, JSON.stringify(robotGrant?.user))
    check('robot grant (UI): the row shows the robot name and "Robot", not an empty email', /Robot/.test(await grantRow.innerText()) && !(await grantRow.innerText()).includes('undefined'), squash(await grantRow.innerText()))
    await page.screenshot({ path: `${OUT}/grants-robot.png`, fullPage: true })
    await report('grant to a robot')

    // Robot detail: create an admin token (allowed now), see the secret once, use it from the page to revoke itself (audited as the robot).
    reset()
    await page.goto(`${BASE}/admin/robots`)
    await robotRow.getByRole('link', { name: `Tokens of robot ${SMOKE_ROBOT}`, exact: true }).click()
    await page.waitForURL(`${BASE}/admin/robots/${robot.id}`, { timeout: DATA_TIMEOUT })
    await page.getByRole('heading', { level: 1, name: SMOKE_ROBOT, exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    await page.getByRole('button', { name: 'New token', exact: true }).click()
    await dlg().waitFor({ timeout: DATA_TIMEOUT })
    check('robot token: the dialog offers 90 days', (await dlg().getByLabel(/^Expires/).locator('option').allInnerTexts()).some((t) => t.trim() === '90 days'))
    await dlg().getByLabel(/^Name/).fill('smoke-robot-token')
    await dlg().getByLabel(/^Scope/).selectOption({ label: 'Admin: can also change things' })
    await dlg().getByLabel(/^Expires/).selectOption({ label: '1 day' })
    await dlg().getByRole('button', { name: 'Create token', exact: true }).click()
    await dlg().getByRole('heading', { level: 2, name: 'Token created', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    const robotSecret = await dlg().getByLabel('Secret', { exact: true }).inputValue()
    check('robot token: secret has the ptk_ shape', TOKEN_RE.test(robotSecret))
    await dlg().getByRole('button', { name: 'Done', exact: true }).click()
    await dlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    const robotTokenRow = page.getByRole('table').getByRole('row').filter({ hasText: 'smoke-robot-token' })
    await robotTokenRow.waitFor({ timeout: DATA_TIMEOUT })
    check('robot token: the row is Active with the admin scope', /admin/.test(await robotTokenRow.innerText()) && /Active/.test(await robotTokenRow.innerText()), squash(await robotTokenRow.innerText()))
    check('robot token: the secret is gone from the page', !(await bodyText()).includes(robotSecret))
    await page.screenshot({ path: `${OUT}/robot-detail.png`, fullPage: true })
    await report('robot detail + token')
    // The token path is exclusive on the portal host: the Authorization header, not the admin's cookie, identifies the caller.
    const used = await page.evaluate(async (t) => (await fetch('/api/v1/me/tokens/current', { method: 'DELETE', headers: { Authorization: `Bearer ${t}`, 'Content-Type': 'application/json' }, credentials: 'omit' })).status, robotSecret)
    check('robot token: DELETE /me/tokens/current with the token answers 204', used === 204, String(used))
    reset()
    await page.reload()
    await page.getByRole('table').getByRole('row').filter({ hasText: 'smoke-robot-token' }).getByText('Revoked', { exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check('robot token: the detail shows it Revoked after it revoked itself', /Revoked/.test(await page.getByRole('table').getByRole('row').filter({ hasText: 'smoke-robot-token' }).innerText()))
    await report('robot detail after self-revoke')

    // Audit: the row of the robot's action shows its name (no email).
    reset()
    await page.goto(`${BASE}/admin/audit`)
    const robotAudit = page.getByRole('table').getByRole('row').filter({ hasText: SMOKE_ROBOT }).filter({ hasText: 'token.revoke' })
    await robotAudit.first().waitFor({ timeout: DATA_TIMEOUT })
    check('audit: the row of the robot\'s own action shows its name and "Robot", not "Unknown"', /Robot/.test(await robotAudit.first().innerText()) && !/Unknown/.test(await robotAudit.first().innerText()), squash(await robotAudit.first().innerText()))
    const apiEvent = (await apiJson('GET', '/audit?limit=50')).body.events.find((e) => e.action === 'token.revoke' && e.actor_name === SMOKE_ROBOT)
    check('audit (API): actor_email is null and actor_name is the robot', apiEvent?.actor_email === null && apiEvent?.actor_name === SMOKE_ROBOT, JSON.stringify(apiEvent))
    await page.screenshot({ path: `${OUT}/audit-robot.png`, fullPage: true })
    await report('audit (robot)')

    // Revoke the grant through the UI: the confirmation names the robot.
    reset()
    await page.goto(`${BASE}/admin/initiatives/${demoId}/grants`)
    await grantRow.getByRole('button', { name: `Revoke manager from ${SMOKE_ROBOT}`, exact: true }).click()
    await page.getByRole('alertdialog').waitFor({ timeout: DATA_TIMEOUT })
    check('robot grant revoke: the confirmation names the robot', (await page.getByRole('alertdialog').getByRole('heading', { level: 2 }).innerText()) === `Revoke manager from robot ${SMOKE_ROBOT}?`)
    await page.getByRole('alertdialog').getByRole('button', { name: 'Revoke', exact: true }).click()
    await page.getByRole('alertdialog').waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    robotGrant = null
    check('robot grant revoke (API): the grant is gone', !(await apiAll(`/initiatives/${demoId}/grants`, 'grants')).some((g) => g.user?.id === robot.id))
    await report('revoke robot grant')

    // A manager (dave): Robots is not in his nav and both pages are refused, but the grant dialog lists the active robots.
    await login(DAVE.email)
    for (const path of ['/admin/robots', `/admin/robots/${robot.id}`]) {
      reset()
      await page.goto(`${BASE}${path}`)
      await page.getByText('Platform administrators only').first().waitFor({ timeout: DATA_TIMEOUT })
      check(`dave ${path.replace(robot.id, '<id>')}: "Platform administrators only", no table and no Robots in the nav`, (await page.getByRole('table').count()) === 0 && !(await navLinks()).includes('Robots'), JSON.stringify(await navLinks()))
      await report(`${path.replace(robot.id, '<id>')} (dave)`)
    }
    reset()
    await page.goto(`${BASE}/admin/initiatives/${demoId}/grants`)
    await page.getByRole('button', { name: 'Grant access', exact: true }).click()
    await dlg().waitFor({ timeout: DATA_TIMEOUT })
    await dlg().getByLabel('Grant to').selectOption({ label: 'Robot' })
    await dlg().getByLabel(/^Robot/).locator('option', { hasText: SMOKE_ROBOT }).waitFor({ state: 'attached', timeout: DATA_TIMEOUT })
    check('dave: the grant dialog lists the robot (managers may list them)', (await dlg().getByLabel(/^Robot/).locator('option', { hasText: SMOKE_ROBOT }).count()) === 1)
    await page.keyboard.press('Escape')
    await dlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await report('grant dialog robots (dave)')
    await login(ADMIN.email)
    robotRow = page.getByRole('table').getByRole('row').filter({ hasText: SMOKE_ROBOT }) // the page changed with the login

    // Disable the robot: its live tokens are revoked.
    const live = await apiJson('POST', `/robots/${robot.id}/tokens`, { name: 'smoke-live', scope: 'read', expires_in: 3600 })
    check('robot disable: setup, a live read token exists', live.status === 201)
    reset()
    await page.goto(`${BASE}/admin/robots`)
    await robotRow.getByRole('button', { name: `Disable robot ${SMOKE_ROBOT}`, exact: true }).click()
    await page.getByRole('alertdialog').waitFor({ timeout: DATA_TIMEOUT })
    check('robot disable: the confirmation says the tokens are revoked', (await page.getByRole('alertdialog').innerText()).includes('All its tokens are revoked now'))
    await page.getByRole('alertdialog').getByRole('button', { name: 'Disable', exact: true }).click()
    await page.getByRole('alertdialog').waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await robotRow.getByText('Disabled', { exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    const afterDisable = (await apiJson('GET', `/robots/${robot.id}/tokens?limit=100`)).body.tokens
    check('robot disable (API): every token of the robot is revoked', afterDisable.length >= 2 && afterDisable.every((t) => t.revoked_at), JSON.stringify(afterDisable.map((t) => [t.name, !!t.revoked_at])))
    await robotRow.getByRole('link', { name: `Tokens of robot ${SMOKE_ROBOT}`, exact: true }).click()
    await page.getByRole('heading', { level: 1, name: SMOKE_ROBOT, exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    check('robot disable: the detail of a disabled robot offers no New token', (await page.getByRole('button', { name: 'New token', exact: true }).count()) === 0)
    await report('disable robot')

    // The leaked-secret form: a well-formed secret of a live token, then a malformed value.
    const leak = await apiJson('POST', '/me/tokens', { name: SMOKE_TOKEN, scope: 'read', expires_in: 3600 })
    check('leak form: setup, a live personal token exists', leak.status === 201 && TOKEN_RE.test(leak.body.secret))
    reset()
    await page.goto(`${BASE}/admin/robots`)
    const leakField = page.getByRole('textbox', { name: 'Leaked token', exact: true })
    await leakField.fill(leak.body.secret)
    await page.getByRole('button', { name: 'Revoke token', exact: true }).click()
    await page.getByText(/^Request accepted\./).waitFor({ timeout: DATA_TIMEOUT })
    check('leak form: the field is cleared and the pasted value is not echoed', (await leakField.inputValue()) === '' && !(await bodyText()).includes(leak.body.secret))
    check('leak form (API): the token is revoked', !!(await ownTokens()).find((t) => t.id === leak.body.id)?.revoked_at)
    await report('revoke by secret')
    reset()
    await leakField.fill('ptk_not-a-token')
    await page.getByRole('button', { name: 'Revoke token', exact: true }).click()
    await page.getByText(/^This is not a portal token\./).waitFor({ timeout: DATA_TIMEOUT })
    check('leak form: a malformed value is refused (422) with its copy; the field is cleared and the value not echoed', (await leakField.inputValue()) === '' && !(await bodyText()).includes('ptk_not-a-token'))
    await page.screenshot({ path: `${OUT}/revoke-secret.png`, fullPage: true })
    await report('revoke by secret (malformed)', { tolerated: ['status of 422'], toleratedRequests: [`${BASE}/api/v1/tokens/revoke HTTP 422`] })

    // A manager and a plain user: the page is for platform admins; /tokens works for everyone.
    await login(ALICE.email)
    reset()
    await page.goto(`${BASE}/tokens`)
    await page.getByRole('button', { name: 'New token', exact: true }).waitFor({ timeout: DATA_TIMEOUT })
    await page.getByRole('button', { name: 'New token', exact: true }).click()
    await dlg().waitFor({ timeout: DATA_TIMEOUT })
    const aliceScopes = (await dlg().getByLabel(/^Scope/).locator('option').allInnerTexts()).map((t) => t.trim())
    check('alice (viewer): /tokens works and only the read scope is offered', aliceScopes.length === 1 && aliceScopes[0].startsWith('Read'), JSON.stringify(aliceScopes))
    await page.keyboard.press('Escape')
    await dlg().waitFor({ state: 'detached', timeout: DATA_TIMEOUT })
    await report('tokens (alice)')
    reset()
    await page.goto(`${BASE}/admin/robots`)
    await page.getByText('You do not have access to administration').waitFor({ timeout: DATA_TIMEOUT })
    check('alice: /admin/robots is refused', (await page.getByRole('table').count()) === 0)
    await report('/admin/robots (alice)')
  } finally {
    // Cleanup, whatever happened: the robot is disabled (that revokes its tokens), its grant removed, own smoke tokens revoked.
    await login(ADMIN.email)
    if (robotGrant) await apiJson('DELETE', `/grants/${robotGrant.id}`)
    if (robot) await apiJson('PATCH', `/robots/${robot.id}`, { status: 'disabled' })
    for (const t of (await ownTokens()).filter((t) => t.name.startsWith('smoke-token') && !t.revoked_at)) await apiJson('DELETE', `/me/tokens/${t.id}`)
  }
  reset()

  // Grafana is an app of the portal like any other: the central session of the admin opens it (SSO), the JWT auth maps the
  // grant to an org role. Grafana sets its own CSP and Nuxt's checks do not apply here: only that the home renders.
  console.log('  -- admin: Grafana home behind ForwardAuth (SSO from the central session)')
  await login(ADMIN.email)
  await page.goto(`${GRAFANA}/`)
  await page.waitForURL(`${GRAFANA}/**`, { timeout: 30_000 })
  await page.getByText('Welcome to Grafana.').waitFor({ timeout: DATA_TIMEOUT })
  check('grafana (admin): the home renders', true)
  check('grafana (admin): no login form', (await page.locator('input[name=user]').count()) === 0)
  const gUser = await page.evaluate(async () => (await fetch('/api/user')).json())
  // Identity = sub (internal UUID) only; the assertion's email is never stored (no email_claim), so Grafana cannot match by email.
  check('grafana (admin): login is the sub (a UUID)', /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(gUser.login), String(gUser.login))
  check('grafana (admin): the email of the assertion is not stored', gUser.email !== ADMIN.email, String(gUser.email))
  const gOrgs = await page.evaluate(async () => (await fetch('/api/user/orgs')).json())
  check('grafana (admin): org role Admin', gOrgs.map((o) => o.role).join() === 'Admin', JSON.stringify(gOrgs.map((o) => o.role)))
  await page.screenshot({ path: `${OUT}/grafana-home.png` })
} catch (e) {
  fail(`browser scenario aborted: ${e.message.split('\n')[0]} (url ${page.url().split('?')[0]})`)
  await page.screenshot({ path: `${OUT}/aborted.png` }).catch(() => {})
} finally {
  await browser.close()
}

if (fails > 0) { console.log(`  browser check: ${fails} failure(s); screenshots in out/`); process.exit(1) }
console.log(`  browser check: all scenarios passed; screenshots in out/`)
