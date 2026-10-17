import type { MeResponse } from '~/types/api'

/**
 * What the caller may administer, from GET /me (the same `me` key as the home page, so it is shared).
 * The UI only hides: the API enforces every call. A platform admin sees everything; a manager (a non-expired
 * `manages_access` grant in at least one initiative) sees the sections that apply to them.
 * `revalidate()` re-reads /me in the background (no flicker, previous data kept on failure) so permissions are
 * not stale after navigating from Home.
 */
export function useAdminAccess() {
  const state = useApiData<MeResponse>('me', '/me')
  const permissions = computed(() => state.data.value?.permissions ?? null)
  const isPlatformAdmin = computed(() => !!permissions.value?.platform_admin)
  const isManager = computed(() => (permissions.value?.managed_initiatives.length ?? 0) > 0)
  const supervisedAreas = computed(() => permissions.value?.supervised_areas ?? [])
  /** Platform admin, or a non-expired `manages_access` grant on this initiative (grants and roles screens). */
  const managesInitiative = (initiativeId: string) =>
    isPlatformAdmin.value || !!permissions.value?.managed_initiatives.some(i => i.id === initiativeId)
  return {
    me: state.data,
    error: state.error,
    pending: state.pending,
    refresh: state.refresh,
    revalidate: state.refreshQuiet,
    isPlatformAdmin,
    isManager,
    managesInitiative,
    supervisedAreas,
    isSupervisor: computed(() => supervisedAreas.value.length > 0),
    canAdminister: computed(() => isPlatformAdmin.value || isManager.value),
  }
}
