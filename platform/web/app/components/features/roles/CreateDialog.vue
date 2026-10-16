<script setup lang="ts">
import type { CreateRoleBody, Role } from '~/types/api'

/**
 * Dialog to create a role in an initiative (platform admins; the API enforces). The key is validated here with
 * the same rules as the server and is immutable afterwards (it is part of the apps' `roles` claim). 409 means the
 * key is taken in this initiative. The Key input takes focus on open.
 */
const props = defineProps<{
  initiativeId: string
}>()
const emit = defineEmits<{ created: [role: Role] }>()
const open = defineModel<boolean>('open', { required: true })

const api = useApi()
const key = ref('')
const managesAccess = ref(false)
const keyError = ref('')
const formError = ref('')
const submitting = ref(false)
const keyInput = useTemplateRef<{ focus: () => void }>('keyInput')

watch(open, (isOpen) => {
  if (!isOpen) return
  key.value = ''
  managesAccess.value = false
  keyError.value = ''
  formError.value = ''
})

async function submit() {
  if (submitting.value) return
  formError.value = ''
  keyError.value = validateRoleKey(key.value.trim()) ?? ''
  if (keyError.value) {
    keyInput.value?.focus()
    return
  }
  const body: CreateRoleBody = { key: key.value.trim(), manages_access: managesAccess.value }
  submitting.value = true
  try {
    const created = await api.post<Role>(`/initiatives/${encodeURIComponent(props.initiativeId)}/roles`, body)
    open.value = false
    emit('created', created)
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: 'This initiative already has a role with this key. If you just retried, it may have been created by your earlier attempt: check the list.',
      notFound: 'This initiative no longer exists.',
      fields: ['key'],
      fieldCopy: ROLE_FIELD_COPY,
    })
    const duplicate = e instanceof ApiError && e.kind === 'conflict'
    keyError.value = failure.fields.key ?? (duplicate ? failure.message : '')
    formError.value = duplicate ? '' : failure.message
    if (keyError.value) keyInput.value?.focus()
  }
  finally {
    submitting.value = false
  }
}
</script>

<template>
  <UiDialog
    v-model:open="open"
    title="New role"
    description="Roles are what apps receive in the roles claim. The key cannot be changed later."
    :busy="submitting"
    :initial-focus="keyInput"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Key" required :hint="ROLE_KEY_RULES" :error="keyError">
          <BaseInput ref="keyInput" v-bind="field" v-model="key" autocomplete="off" autocapitalize="none" spellcheck="false" />
        </BaseField>
        <BaseCheckbox v-model="managesAccess" hint="Holders of this role can grant and revoke access in this initiative.">
          Manages access
        </BaseCheckbox>
        <BaseText v-if="formError" role="alert" tone="danger" size="sm">
          {{ formError }}
        </BaseText>
        <BaseCluster justify="end">
          <BaseButton variant="secondary" :disabled="submitting" @click="open = false">
            Cancel
          </BaseButton>
          <BaseButton type="submit" :loading="submitting">
            Create role
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
