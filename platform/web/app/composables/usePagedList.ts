import type { ApiError } from '~/composables/useApi'

/**
 * Keyset-paginated list: first page on mount, "load more" appends the next one. The cursor is opaque: it is only
 * echoed back (URL-encoded). A failed first page leaves `items` empty and sets `error`; a failed "load more"
 * keeps the items already shown and sets `error` too (the UI tells them apart by `items.length`).
 * A newer request wins over an older one still in flight (`reload()` after a mutation).
 * `path` may be a getter (e.g. one that reads a filter) and may already carry a query string; the cursor is appended.
 * `{ immediate: false }` skips the load on mount (the caller runs `reload()` itself, e.g. when a dialog opens).
 */
export function usePagedList<TPage extends { next_cursor?: string }, TItem>(
  path: string | (() => string),
  select: (page: TPage) => TItem[],
  options: { immediate?: boolean } = {},
) {
  const items = shallowRef<TItem[]>([])
  const nextCursor = ref<string | null>(null)
  const error = shallowRef<ApiError | null>(null)
  const loading = ref(options.immediate ?? true)
  const loadingMore = ref(false)
  let generation = 0

  async function fetchPage(cursor: string | null): Promise<TPage> {
    const base = typeof path === 'function' ? path() : path
    if (!cursor) return await apiGet<TPage>(base)
    return await apiGet<TPage>(`${base}${base.includes('?') ? '&' : '?'}cursor=${encodeURIComponent(cursor)}`)
  }

  /**
   * (Re)loads the first page, replacing everything shown. `quiet` keeps the current rows on screen (no skeleton)
   * and, if the request fails, keeps them too. Resolves to false when the request failed (a superseded one counts as ok).
   */
  async function reload(quiet = false): Promise<boolean> {
    const mine = ++generation
    if (!quiet) loading.value = true
    loadingMore.value = false
    error.value = null
    try {
      const page = await fetchPage(null)
      if (mine !== generation) return true
      items.value = select(page)
      nextCursor.value = page.next_cursor ?? null
      return true
    }
    catch (e) {
      if (mine !== generation) return true
      if (quiet) return false
      items.value = []
      nextCursor.value = null
      error.value = e as ApiError
      return false
    }
    finally {
      if (mine === generation) loading.value = false
    }
  }

  /** Appends the next page; also the retry of a failed "load more". */
  async function loadMore() {
    if (!nextCursor.value || loadingMore.value || loading.value) return
    const mine = ++generation
    loadingMore.value = true
    error.value = null
    try {
      const page = await fetchPage(nextCursor.value)
      if (mine !== generation) return
      items.value = [...items.value, ...select(page)]
      nextCursor.value = page.next_cursor ?? null
    }
    catch (e) {
      if (mine !== generation) return
      error.value = e as ApiError
    }
    finally {
      if (mine === generation) loadingMore.value = false
    }
  }

  if (options.immediate ?? true) onMounted(reload)

  return { items, error, loading, loadingMore, hasMore: computed(() => nextCursor.value !== null), reload, loadMore }
}
