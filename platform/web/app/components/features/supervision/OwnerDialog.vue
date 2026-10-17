<script setup lang="ts">
import type { AreaPersonList, GroupMember, PatchRobotBody, Robot } from '~/types/api'
import type { SelectOption } from '~/types/ui'

/**
 * "Change owner" of a robot (`PATCH /robots/{id}` with `owner_user_id` only). The candidates are the active people of
 * every area the caller supervises (`GET /areas/{id}/members`, all pages, read on open); the API validates the choice
 * (422 `owner_user_id`: unknown or not_allowed). The owner cannot be cleared.
 */
const props = defineProps<{ robot: Robot | null }>()
const emit = defineEmits<{ saved: [robot: Robot], gone: [] }>()
const open = defineModel<boolean>('open', { required: true })

// shortcut: at most 20 pages of 200 per area are read for the candidates, raise it or add a search if an area grows past that.
const MAX_PAGES = 20
const PAGE_SIZE = 200

const access = useAdminAccess()
const api = useApi()
const owner = ref('')
const people = ref<GroupMember[]>([])
const loading = ref(false)
const truncated = ref(false)
const loadError = ref('')
const fieldError = ref('')
const formError = ref('')
const submitting = ref(false)
const select = useTemplateRef<{ focus: () => void }>('select')
let generation = 0

const options = computed<SelectOption[]>(() =>
  people.value
    .filter(p => p.status === 'active' && p.user_id !== props.robot?.owner_user_id)
    .map(p => ({ value: p.user_id, label: p.name ? `${p.name} (${p.email})` : p.email })))

// A newer run, or closing the dialog, supersedes this one: its results are dropped and `loading` is not touched.
async function loadPeople() {
  const mine = ++generation
  loading.value = true
  loadError.value = ''
  truncated.value = false
  people.value = []
  const seen = new Map<string, GroupMember>()
  let capped = false
  try {
    for (const area of access.supervisedAreas.value) {
      let cursor = ''
      for (let page = 0; page < MAX_PAGES; page++) {
        const query = `limit=${PAGE_SIZE}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`
        const result = await apiGet<AreaPersonList>(`/areas/${encodeURIComponent(area.id)}/members?${query}`)
        if (mine !== generation) return
        for (const m of result.members ?? []) seen.set(m.user_id, m)
        if (!result.next_cursor) break
        if (page === MAX_PAGES - 1) capped = true
        cursor = result.next_cursor
      }
    }
    people.value = [...seen.values()]
    truncated.value = capped
  }
  catch {
    if (mine !== generation) return
    loadError.value = 'Could not load the people of your areas. Close the dialog and try again.'
  }
  loading.value = false
}

watch(open, (isOpen) => {
  if (!isOpen) {
    generation++
    return
  }
  owner.value = ''
  fieldError.value = formError.value = ''
  void loadPeople()
})

async function submit() {
  const robot = props.robot
  if (!robot || submitting.value) return
  fieldError.value = formError.value = ''
  if (!owner.value) {
    fieldError.value = 'Choose the new owner.'
    select.value?.focus()
    return
  }
  const body: PatchRobotBody = { owner_user_id: owner.value }
  submitting.value = true
  try {
    const updated = await api.patch<Robot>(`/robots/${encodeURIComponent(robot.id)}`, body)
    open.value = false
    emit('saved', updated)
  }
  catch (e) {
    if (e instanceof ApiError && e.kind === 'not_found') {
      open.value = false
      emit('gone')
      return
    }
    const failure = describeMutationError(e, {
      conflict: 'The robot could not be changed. Reload the page and try again.',
      notFound: 'This robot is no longer in your scope. Reload the page.',
      fields: ['owner_user_id'],
      fieldCopy: {
        'owner_user_id.unknown': 'The new owner must be an active person of one of your areas.',
        'owner_user_id.not_allowed': 'The new owner must be an active person of one of your areas.',
      },
    })
    fieldError.value = failure.fields.owner_user_id ?? ''
    formError.value = fieldError.value ? '' : failure.message
    if (fieldError.value) select.value?.focus()
  }
  finally {
    submitting.value = false
  }
}
</script>

<template>
  <UiDialog
    v-model:open="open"
    :title="`Change owner of ${robot?.name ?? 'robot'}`"
    description="The robot and its tokens move to the person you choose. The tokens keep working; the new owner's supervisors manage the robot from now on."
    :busy="submitting"
    :initial-focus="select"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseText v-if="loading" role="status" size="sm" tone="muted">
          Loading people...
        </BaseText>
        <BaseText v-else-if="loadError" role="alert" tone="danger" size="sm">
          {{ loadError }}
        </BaseText>
        <BaseField v-else v-slot="field" label="New owner" required hint="Only active people of the areas you supervise." :error="fieldError">
          <BaseSelect ref="select" v-bind="field" v-model="owner" :options="options" placeholder="Choose a person" />
        </BaseField>
        <BaseText v-if="truncated" size="sm" tone="warning">
          Only the first people of your areas are listed. If the person you need is missing, ask a platform administrator.
        </BaseText>
        <BaseText v-if="formError" role="alert" tone="danger" size="sm">
          {{ formError }}
        </BaseText>
        <BaseCluster justify="end">
          <BaseButton variant="secondary" :disabled="submitting" @click="open = false">
            Cancel
          </BaseButton>
          <BaseButton type="submit" :loading="submitting" :disabled="loading || !!loadError">
            Change owner
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
