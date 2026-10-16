/**
 * Focus return for modals. Reka restores focus to the element that was focused when the dialog opened, but
 * Safari (and Firefox on macOS) do not focus a button on click, so that element is `<body>` and focus is lost.
 * This remembers the element that was activated: `document.activeElement` when it is a real element, else the
 * target of the last click (tracked in the capture phase, so it is known before the click handler opens the
 * dialog). Call `capture()` when `open` turns true (a `flush: 'sync'` watcher) and `restore(event)` from the
 * dialog's `@close-auto-focus`: it prevents Reka's own restore and focuses the stored element if it is still
 * connected (otherwise Reka's default applies).
 */
const INTERACTIVE = 'button, a[href], input, select, textarea, [tabindex]'

let lastClicked: Element | null = null
let tracking = false

function trackClicks() {
  if (tracking || !import.meta.client) return
  tracking = true
  document.addEventListener('click', (event) => {
    lastClicked = event.target instanceof Element ? event.target.closest(INTERACTIVE) : null
  }, true)
}

export function useFocusReturn() {
  trackClicks()
  let trigger: HTMLElement | null = null

  function capture() {
    const active = document.activeElement
    const candidate = active && active !== document.body ? active : lastClicked
    trigger = candidate instanceof HTMLElement ? candidate : null
  }

  function restore(event: Event) {
    const target = trigger
    trigger = null
    if (!target?.isConnected) return
    event.preventDefault()
    target.focus()
  }

  return { capture, restore }
}
