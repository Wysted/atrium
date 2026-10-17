/**
 * Debounced search text for `GET /groups?q=`. `input` is bound to the search box; `query` follows it after
 * `delay` ms, trimmed and capped at `GROUP_SEARCH_MAX`, so the list reloads once per pause, not per key.
 * `path` is the list path for the current query (use it as the `usePagedList` getter).
 */
export function useGroupSearch(delay = 300) {
  const input = ref('')
  const query = ref('')
  let timer: ReturnType<typeof setTimeout> | undefined

  watch(input, (value) => {
    clearTimeout(timer)
    timer = setTimeout(() => {
      query.value = Array.from(value.trim()).slice(0, GROUP_SEARCH_MAX).join('')
    }, delay)
  })
  onBeforeUnmount(() => clearTimeout(timer))

  return { input, query, path: () => groupsPath(query.value) }
}
