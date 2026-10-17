<script setup lang="ts">
import type { AdminUserList, CreateRobotBody, PatchRobotBody, Robot } from '~/types/api'
import type { SelectOption } from '~/types/ui'

/**
 * Creates a robot (`robot` null) or edits one (rename, set, change or clear the owner). The owner is chosen by the
 * email of one active person (`GET /users?email=`, the same lookup as the Users screen) and sent as
 * `owner_user_id`; the API takes ids only and checks it is an active human (422 `owner_user_id`). When editing,
 * only changed fields are sent (PATCH needs at least one; with none the dialog just closes). Creating is not
 * idempotent: after an ambiguous failure the dialog closes and emits `uncertain`. Platform admins only; the API enforces. 409 = the name is taken. Fixed copy only.
 */
const props = defineProps<{
  robot: Robot | null
}>()
const emit = defineEmits<{ saved: [robot: Robot], uncertain: [] }>()
const open = defineModel<boolean>('open', { required: true })

const api = useApi()
const name = ref('')
const ownerMode = ref<'keep' | 'set' | 'none'>('none')
const ownerEmail = ref('')
const errors = reactive({ name: '', owner: '' })
const formError = ref('')
const submitting = ref(false)

type Focusable = { focus: () => void }
const nameInput = useTemplateRef<Focusable>('nameInput')
const ownerModeInput = useTemplateRef<Focusable>('ownerModeInput')
const ownerEmailInput = useTemplateRef<Focusable>('ownerEmailInput')

const editing = computed(() => props.robot !== null)
const ownerOptions = computed<SelectOption[]>(() => [
  ...(props.robot?.owner_user_id ? [{ value: 'keep', label: `Keep the current owner (${robotOwnerLabel(props.robot)})` }] : []),
  { value: 'set', label: 'Set the owner by email' },
  { value: 'none', label: 'No owner' },
])

watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = props.robot?.name ?? ''
  ownerMode.value = props.robot?.owner_user_id ? 'keep' : 'none'
  ownerEmail.value = ''
  errors.name = errors.owner = formError.value = ''
})

function focusFirstInvalid() {
  if (errors.name) nameInput.value?.focus()
  else if (errors.owner) (ownerMode.value === 'set' ? ownerEmailInput : ownerModeInput).value?.focus()
}

/** The id of the one active person with this email; sets the owner error and returns null otherwise. */
async function lookupOwner(): Promise<string | null> {
  const email = ownerEmail.value.trim()
  errors.owner = validateEmail(email) ?? ''
  if (errors.owner) return null
  const found = (await api.get<AdminUserList>(`/users?email=${encodeURIComponent(email)}`)).users.filter(u => u.status === 'active')
  if (found.length !== 1) {
    errors.owner = EMAIL_FIELD_COPY['email.no_unique_active_user']!
    return null
  }
  return found[0]!.id
}

async function submit() {
  if (submitting.value) return
  const trimmed = name.value.trim()
  errors.name = validateRobotName(trimmed) ?? ''
  errors.owner = formError.value = ''
  if (errors.name) {
    focusFirstInvalid()
    return
  }
  submitting.value = true
  try {
    const owner = ownerMode.value === 'set' ? await lookupOwner() : null
    if (ownerMode.value === 'set' && owner === null) {
      focusFirstInvalid()
      return
    }
    const robot = props.robot
    if (!robot) {
      const body: CreateRobotBody = { name: trimmed }
      if (owner) body.owner_user_id = owner
      const created = await api.post<Robot>('/robots', body)
      open.value = false
      emit('saved', created)
      return
    }
    const body: PatchRobotBody = {}
    if (trimmed !== robot.name) body.name = trimmed
    if (owner) body.owner_user_id = owner
    else if (ownerMode.value === 'none' && robot.owner_user_id) body.owner_user_id = ''
    if (Object.keys(body).length === 0) {
      open.value = false
      return
    }
    const updated = await api.patch<Robot>(`/robots/${encodeURIComponent(robot.id)}`, body)
    open.value = false
    emit('saved', updated)
  }
  catch (e) {
    if (!props.robot && isAmbiguousFailure(e)) {
      open.value = false
      emit('uncertain')
      return
    }
    const failure = describeMutationError(e, {
      conflict: ROBOT_NAME_TAKEN_COPY,
      notFound: 'This robot no longer exists. Reload the page.',
      fields: ['name', 'owner_user_id', 'email'],
      fieldCopy: { ...ROBOT_FIELD_COPY, ...EMAIL_FIELD_COPY },
    })
    const duplicate = e instanceof ApiError && e.kind === 'conflict'
    errors.name = failure.fields.name ?? (duplicate ? failure.message : '')
    errors.owner = failure.fields.owner_user_id ?? failure.fields.email ?? ''
    formError.value = duplicate ? '' : failure.message
    focusFirstInvalid()
  }
  finally {
    submitting.value = false
  }
}
</script>

<template>
  <UiDialog
    v-model:open="open"
    :title="editing ? `Edit robot ${robot?.name}` : 'New robot'"
    description="A robot is a non-human identity for automation. It gets access only through direct grants and is never a platform admin. The owner is the accountable person; it grants nothing."
    :busy="submitting"
    :initial-focus="nameInput"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Name" required hint="Unique among robots. Up to 64 characters." :error="errors.name">
          <BaseInput ref="nameInput" v-bind="field" v-model="name" autocomplete="off" />
        </BaseField>
        <BaseField v-slot="field" label="Owner">
          <BaseSelect ref="ownerModeInput" v-bind="field" v-model="ownerMode" :options="ownerOptions" />
        </BaseField>
        <BaseField
          v-if="ownerMode === 'set'"
          v-slot="field"
          label="Owner email"
          required
          hint="The email of one active person."
          :error="errors.owner"
        >
          <BaseInput ref="ownerEmailInput" v-bind="field" v-model="ownerEmail" type="email" autocomplete="off" autocapitalize="none" spellcheck="false" />
        </BaseField>
        <BaseText v-if="formError" role="alert" tone="danger" size="sm">
          {{ formError }}
        </BaseText>
        <BaseCluster justify="end">
          <BaseButton variant="secondary" :disabled="submitting" @click="open = false">
            Cancel
          </BaseButton>
          <BaseButton type="submit" :loading="submitting">
            {{ editing ? 'Save' : 'Create robot' }}
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
