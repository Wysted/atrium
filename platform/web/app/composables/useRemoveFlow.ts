/**
 * The "remove behind a confirmation" state of the admin lists: `ask(item)` opens the dialog (`open`), `confirm()`
 * calls `remove`, then `onRemoved`. A 404 closes the dialog, says `gone` and calls `reload`; any other failure shows
 * in the dialog (`error`, 409 = `conflict`). `notice` is also the panel's status line for other actions.
 */
export function useRemoveFlow<T>(o: {
  remove: (item: T) => Promise<unknown>
  onRemoved: (item: T) => void
  reload: () => void
  done: string
  gone: string
  conflict: string
}) {
  const removing = ref<T | null>(null) as Ref<T | null>
  const open = computed({
    get: () => removing.value !== null,
    set: (value: boolean) => {
      if (!value) removing.value = null
    },
  })
  const busy = ref(false)
  const error = ref('')
  const notice = ref('')

  function ask(item: T) {
    notice.value = ''
    error.value = ''
    removing.value = item
  }

  async function confirm() {
    const item = removing.value
    if (!item || busy.value) return
    busy.value = true
    error.value = ''
    try {
      await o.remove(item)
      o.onRemoved(item)
      removing.value = null
      notice.value = o.done
    }
    catch (e) {
      if (e instanceof ApiError && e.kind === 'not_found') {
        removing.value = null
        notice.value = o.gone
        o.reload()
      }
      else {
        error.value = describeMutationError(e, { conflict: o.conflict }).message
      }
    }
    finally {
      busy.value = false
    }
  }

  return { removing, open, busy, error, notice, ask, confirm }
}
