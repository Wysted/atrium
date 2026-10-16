import type { InjectionKey } from 'vue'
import type { Initiative } from '~/types/api'

/** What the pages nested under `/admin/initiatives/[id]` get from their parent route, which loads the initiative. */
export interface InitiativeContext {
  initiative: Ref<Initiative>
  /** Replaces the cached initiative with a mutation response (no re-fetch). */
  set: (initiative: Initiative) => void
}

const key: InjectionKey<InitiativeContext> = Symbol('initiative')

export function provideInitiativeContext(context: InitiativeContext) {
  provide(key, context)
}

/** Only valid inside the nested pages: the parent renders them once the initiative is loaded. */
export function useInitiativeContext(): InitiativeContext {
  const context = inject(key)
  if (!context) throw new Error('useInitiativeContext() needs the /admin/initiatives/[id] parent route')
  return context
}
