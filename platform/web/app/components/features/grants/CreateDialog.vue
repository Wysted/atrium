<script setup lang="ts">
import type { CreateGrantBody, Grant, Role, RoleList } from '~/types/api'
import type { SelectOption } from '~/types/ui'

/**
 * Dialog to grant a role of the initiative to a user (by email: exactly one active user) or to a group
 * (native picker over GET /groups). The role list is this initiative's roles (all pages are loaded, they are few).
 * Optional expiry from a native `datetime-local` input: sent as RFC 3339 with the user's offset. "In the future" is
 * enforced only by the server (422 `must_be_in_the_future`, mapped to the field). Platform admins and managers of the
 * initiative may use it; the API enforces. 409 = this subject already holds this role. Fixed copy only.
 */
const props = defineProps<{
  initiativeId: string
}>()
const emit = defineEmits<{ created: [grant: Grant] }>()
const open = defineModel<boolean>('open', { required: true })

const api = useApi()
const kind = ref<'user' | 'group'>('user')
const email = ref('')
const groupId = ref('')
const roleId = ref('')
const expiresLocal = ref('')
const errors = reactive({ email: '', group_id: '', role_id: '', expires_at: '' })
const formError = ref('')
const submitting = ref(false)

type Focusable = { focus: () => void }
const kindInput = useTemplateRef<Focusable>('kindInput')
const emailInput = useTemplateRef<Focusable>('emailInput')
const groupInput = useTemplateRef<Focusable>('groupInput')
const roleInput = useTemplateRef<Focusable>('roleInput')
const expiresInput = useTemplateRef<Focusable>('expiresInput')

const roles = usePagedList<RoleList, Role>(
  `/initiatives/${encodeURIComponent(props.initiativeId)}/roles`,
  page => page.roles,
  { immediate: false },
)
// Roles are few: keep loading until the last page so the picker is complete.
watch(
  () => [roles.loading.value, roles.loadingMore.value, roles.hasMore.value, roles.error.value] as const,
  ([loading, loadingMore, hasMore, error]) => {
    if (!loading && !loadingMore && hasMore && !error) void roles.loadMore()
  },
)

const kindOptions: SelectOption[] = [
  { value: 'user', label: 'User (by email)' },
  { value: 'group', label: 'Group' },
]
const roleOptions = computed<SelectOption[]>(() =>
  roles.items.value.map(r => ({ value: r.id, label: r.manages_access ? `${r.key} (manages access)` : r.key })),
)
const rolesReady = computed(() => !roles.loading.value && !roles.loadingMore.value && !roles.hasMore.value && !roles.error.value)

watch(open, (isOpen) => {
  if (!isOpen) return
  kind.value = 'user'
  email.value = ''
  groupId.value = ''
  roleId.value = ''
  expiresLocal.value = ''
  clearErrors()
  // Roles may have changed since the last opening.
  void roles.reload()
})

// Switching the subject kind drops the other kind's value and errors.
watch(kind, () => {
  email.value = ''
  groupId.value = ''
  errors.email = ''
  errors.group_id = ''
})

function clearErrors() {
  errors.email = ''
  errors.group_id = ''
  errors.role_id = ''
  errors.expires_at = ''
  formError.value = ''
}

function focusFirstInvalid() {
  if (errors.email) emailInput.value?.focus()
  else if (errors.group_id) groupInput.value?.focus()
  else if (errors.role_id) roleInput.value?.focus()
  else if (errors.expires_at) expiresInput.value?.focus()
}

/** Validates and builds the body; null when something is invalid (errors are set). */
function build(): CreateGrantBody | null {
  clearErrors()
  const body: CreateGrantBody = { role_id: roleId.value }
  if (kind.value === 'user') {
    const trimmed = email.value.trim()
    if (!trimmed) errors.email = 'Enter the email of the user.'
    else body.email = trimmed
  }
  else if (!groupId.value) {
    errors.group_id = 'Choose a group.'
  }
  else {
    body.group_id = groupId.value
  }
  if (!roleId.value) errors.role_id = 'Choose a role.'
  if (expiresLocal.value) {
    const converted = localInputToRfc3339(expiresLocal.value)
    if (converted.ok) body.expires_at = converted.value
    else if (converted.reason === 'nonexistent') errors.expires_at = 'That time does not exist in your time zone. Choose another time.'
    else errors.expires_at = 'Enter a valid date and time.'
  }
  if (errors.email || errors.group_id || errors.role_id || errors.expires_at) {
    focusFirstInvalid()
    return null
  }
  return body
}

async function submit() {
  if (submitting.value) return
  const body = build()
  if (!body) return
  submitting.value = true
  try {
    const created = await api.post<Grant>(`/initiatives/${encodeURIComponent(props.initiativeId)}/grants`, body)
    open.value = false
    emit('created', created)
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: 'This subject already has this role. If you just retried, it may have been created by your earlier attempt: check the list.',
      notFound: 'This initiative no longer exists.',
      fields: ['role_id', 'expires_at', kind.value === 'user' ? 'email' : 'group_id'],
      fieldCopy: {
        'role_id.unknown': 'This role does not exist in this initiative.',
        'role_id.invalid': 'Choose a role.',
        'group_id.unknown': 'This group no longer exists.',
        'group_id.invalid': 'Choose a group.',
        'email.invalid': 'Enter a valid email address.',
      },
    })
    errors.email = failure.fields.email ?? ''
    errors.group_id = failure.fields.group_id ?? ''
    errors.role_id = failure.fields.role_id ?? ''
    errors.expires_at = failure.fields.expires_at ?? ''
    formError.value = failure.message
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
    title="Grant access"
    description="Give a user or a group a role in this initiative."
    :busy="submitting"
    :initial-focus="kindInput"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Grant to">
          <BaseSelect ref="kindInput" v-bind="field" v-model="kind" :options="kindOptions" />
        </BaseField>
        <BaseField
          v-if="kind === 'user'"
          v-slot="field"
          label="Email"
          required
          hint="The email of one active user."
          :error="errors.email"
        >
          <BaseInput ref="emailInput" v-bind="field" v-model="email" type="email" autocomplete="off" autocapitalize="none" spellcheck="false" />
        </BaseField>
        <BaseField v-else v-slot="field" label="Group" required :error="errors.group_id">
          <FeaturesGrantsGroupSelect ref="groupInput" v-bind="field" v-model="groupId" />
        </BaseField>
        <BaseField
          v-slot="field"
          label="Role"
          required
          :hint="rolesReady && roleOptions.length === 0 ? 'This initiative has no roles yet. A platform administrator creates them.' : undefined"
          :error="errors.role_id || (roles.error.value ? 'Could not load the roles. Close and reopen this dialog to retry.' : '')"
        >
          <BaseSelect
            ref="roleInput"
            v-bind="field"
            v-model="roleId"
            :options="roleOptions"
            :placeholder="roles.loading.value ? 'Loading roles' : 'Choose a role'"
            :disabled="!rolesReady"
          />
        </BaseField>
        <BaseField
          v-slot="field"
          label="Expires"
          hint="Optional. In your local time zone. Leave empty for no expiry."
          :error="errors.expires_at"
        >
          <BaseInput ref="expiresInput" v-bind="field" v-model="expiresLocal" type="datetime-local" />
        </BaseField>
        <BaseText v-if="formError" role="alert" tone="danger" size="sm">
          {{ formError }}
        </BaseText>
        <BaseCluster justify="end">
          <BaseButton variant="secondary" :disabled="submitting" @click="open = false">
            Cancel
          </BaseButton>
          <BaseButton type="submit" :loading="submitting" :disabled="!rolesReady || roleOptions.length === 0">
            Grant access
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
