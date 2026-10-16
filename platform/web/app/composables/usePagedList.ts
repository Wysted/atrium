import type { ApiError } from '~/composables/useApi'

/**
 * Keyset-paginated list: first page on mount, "load more" appends the next one. The cursor is opaque: it is only
 * echoed back (URL-encoded). A failed first page leaves `items` empty and sets `error`; a failed "load more"
 * keeps the items already shown and sets `error` too (the UI tells them apart by `items.length`).
 * A newer request wins over an older one still in flight (`reload()` after a mutation).
 */
export function usePagedList<TPage extends { next_cursor?: string }, TItem>(
  path: string,
  select: (page: TPage) => TItem[],
) {
  const items = shallowRef<TItem[]>([])
  const nextCursor = ref<string | null>(null)
  const error = shallowRef<ApiError | null>(null)
  const loading = ref(true)
  const loadingMore = ref(false)
  let generation = 0

  async function fetchPage(cursor: string | null): Promise<TPage> {
    const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''
    return await apiGet<TPage>(`${path}${query}`)
  }

  /** (Re)loads the first page, replacing everything shown. */
  async function reload() {
    const mine = ++generation
    loading.value = true
    loadingMore.value = false
    error.value = null
    try {
      const page = await fetchPage(null)
      if (mine !== generation) return
      items.value = select(page)
      nextCursor.value = page.next_cursor ?? null
    }
    catch (e) {
      if (mine !== generation) return
      items.value = []
      nextCursor.value = null
      error.value = e as ApiError
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

  onMounted(reload)

  return { items, error, loading, loadingMore, hasMore: computed(() => nextCursor.value !== null), reload, loadMore }
}
