// Verifies the generated HTML honors the strict CSP: script-src 'self' 'sha256-...'; style-src 'self'.
// Run after `nuxt generate`. Usage: node scripts/check-csp.mjs [dir]   (default: .output/public)
// Checks every *.html under dir with a real HTML parser and prints the sha256 of the allowed inline script
// (Go hashes the same text at startup). The parser normalizes CRLF to LF, like browsers do, and runs with
// scripting disabled so <noscript> content is parsed as elements and inspected too.
import { createHash } from 'node:crypto'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import * as acorn from 'acorn'
import { parse } from 'parse5'

const defaultDir = join(dirname(fileURLToPath(import.meta.url)), '..', '.output', 'public')
const root = resolve(process.argv[2] ?? defaultDir)

// The only executable inline script is Nuxt's runtime config:
//   window.__NUXT__={};window.__NUXT__.config=<plain data literal>
// Validated on the AST (allowlist; anything else fails), so no string or regex trick can smuggle in code.
const isIdent = (n, name) => n?.type === 'Identifier' && n.name === name
const isWindowNuxt = n => n?.type === 'MemberExpression' && !n.computed && !n.optional && isIdent(n.object, 'window') && isIdent(n.property, '__NUXT__')
const isWindowNuxtConfig = n => n?.type === 'MemberExpression' && !n.computed && !n.optional && isWindowNuxt(n.object) && isIdent(n.property, 'config')
const isAssign = n => n?.type === 'ExpressionStatement' && !n.directive && n.expression.type === 'AssignmentExpression' && n.expression.operator === '='

function isPlainData(n) {
  switch (n?.type) {
    case 'Literal':
      return !n.regex && !n.bigint && (n.value === null || ['string', 'number', 'boolean'].includes(typeof n.value))
    case 'UnaryExpression':
      return n.operator === '-' && n.argument.type === 'Literal' && typeof n.argument.value === 'number' && !n.argument.bigint
    case 'ArrayExpression':
      return n.elements.every(e => e !== null && e.type !== 'SpreadElement' && isPlainData(e))
    case 'ObjectExpression':
      return n.properties.every(p => p.type === 'Property' && p.kind === 'init' && !p.computed && !p.method && !p.shorthand
        && ((p.key.type === 'Identifier' && p.key.name !== '__proto__')
          || (p.key.type === 'Literal' && ['string', 'number'].includes(typeof p.key.value) && String(p.key.value) !== '__proto__'))
        && isPlainData(p.value))
    default:
      return false
  }
}

// Returns an error message, or null when the script body is exactly the allowed Nuxt config statements.
function nuxtConfigError(body) {
  let ast
  try {
    ast = acorn.parse(body, { ecmaVersion: 'latest', sourceType: 'script' })
  } catch (err) {
    return `inline script does not parse: ${err.message}`
  }
  const [first, second] = ast.body
  if (ast.body.length !== 2 || !isAssign(first) || !isAssign(second)) return 'inline script is not exactly the two Nuxt config assignments'
  if (!isWindowNuxt(first.expression.left) || first.expression.right.type !== 'ObjectExpression' || first.expression.right.properties.length > 0) {
    return 'first statement is not window.__NUXT__={}'
  }
  if (!isWindowNuxtConfig(second.expression.left)) return 'second statement does not assign window.__NUXT__.config'
  if (!isPlainData(second.expression.right)) return 'window.__NUXT__.config is not a plain data literal'
  return null
}

const URL_ATTRS = ['href', 'src', 'action', 'formaction', 'srcset', 'data', 'poster'] // xlink:href parses as `href`
const DANGEROUS_SCHEME = /^(javascript|data|vbscript):/i
const EMBED_TAGS = new Set(['iframe', 'object', 'embed', 'base'])

// URL parsing drops leading C0 controls/spaces and strips tab/CR/LF anywhere; backslash acts as slash.
// eslint-disable-next-line no-control-regex
const normalizeUrl = url => url.replace(/^[\u0000-\u0020]+/, '').replace(/[\t\n\r]/g, '').replaceAll('\\', '/')
// Same-origin only: no scheme, no protocol-relative `//host`.
const isSameOrigin = url => !/^([a-z][a-z0-9+.-]*:|\/\/)/i.test(normalizeUrl(url))

function listHtml(dir) {
  return readdirSync(dir, { recursive: true }).filter(f => f.endsWith('.html')).sort()
}

function* walk(node) {
  yield node
  for (const child of node.childNodes ?? []) yield* walk(child)
  if (node.content) yield* walk(node.content) // <template> content lives in a separate fragment
}

const textOf = node => (node.childNodes ?? []).map(c => c.value ?? '').join('')

function check(file, fail) {
  const doc = parse(readFileSync(join(root, file), 'utf8'), { sourceCodeLocationInfo: true, scriptingEnabled: false })
  const hashes = []
  let dataScripts = 0
  let htmlLang = null

  for (const node of walk(doc)) {
    if (!node.tagName) continue
    const tag = node.tagName
    const attrs = Object.fromEntries(node.attrs.map(a => [a.name, a.value]))

    if (tag === 'html' && htmlLang === null) htmlLang = attrs.lang?.trim() ?? ''

    if (tag === 'style') fail('<style> element found')
    if (EMBED_TAGS.has(tag)) fail(`<${tag}> element found`)
    if (tag === 'meta' && attrs['http-equiv']?.trim().toLowerCase() === 'refresh') fail('meta refresh found')
    if ('style' in attrs) fail(`style attribute on <${tag}>`)

    for (const [name, value] of Object.entries(attrs)) {
      if (name.startsWith('on')) fail(`event handler attribute ${name} on <${tag}>`)
      if (!URL_ATTRS.includes(name)) continue
      const tokens = name === 'srcset' ? value.split(/[\s,]+/) : [value]
      for (const t of tokens) {
        if (DANGEROUS_SCHEME.test(normalizeUrl(t))) fail(`${name}="${value.slice(0, 60)}" on <${tag}> uses a dangerous scheme`)
      }
    }

    if (tag === 'link' && attrs.href !== undefined && !isSameOrigin(attrs.href)) fail(`link href is not same-origin: ${attrs.href}`)
    if (tag !== 'script') continue

    const type = attrs.type?.trim().toLowerCase()
    if (type === 'importmap') fail('importmap script found')
    else if ('src' in attrs) {
      if (!isSameOrigin(attrs.src)) fail(`script src is not same-origin: ${attrs.src}`)
      if (textOf(node).trim() !== '') fail(`script with src also has an inline body: ${attrs.src}`)
    } else if (node.sourceCodeLocation && !node.sourceCodeLocation.endTag) {
      fail('unclosed inline <script> (EOF)')
    } else if (attrs.type === 'application/json' && attrs.id === '__NUXT_DATA__') {
      if (++dataScripts > 1) fail('more than one __NUXT_DATA__ script')
    } else if (!('type' in attrs)) {
      const body = textOf(node)
      const problem = nuxtConfigError(body)
      if (problem === null) hashes.push(createHash('sha256').update(body).digest('base64'))
      else fail(`${problem}: ${body.slice(0, 80).replace(/\s+/g, ' ')}`)
    } else {
      fail(`inline script not allowed (type="${attrs.type}", id=${attrs.id ?? '(none)'})`)
    }
  }

  if (!htmlLang) fail('<html> has no non-empty lang attribute')
  if (hashes.length !== 1) fail(`expected exactly 1 Nuxt config inline script, found ${hashes.length}`)
  return hashes
}

const errors = []
const files = listHtml(root)
if (files.length === 0) errors.push(`no *.html files found under ${root}`)
for (const file of files) {
  const fail = msg => errors.push(`${file}: ${msg}`)
  try {
    for (const hash of check(file, fail)) console.log(`${file}: sha256-${hash}`)
  } catch (err) {
    fail(err.message)
  }
}

if (errors.length > 0) {
  console.error('CSP check FAILED:')
  for (const e of errors) console.error(`  - ${e}`)
  process.exit(1)
}
console.log(`CSP check OK (${files.length} files)`)
