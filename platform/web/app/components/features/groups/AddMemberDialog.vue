<script setup lang="ts">
import type { AddMemberBody, GroupMember } from '~/types/api'

/**
 * Dialog to add a member to a group by email (platform admins; the API enforces). The email must match exactly
 * one active user: 422 `no_unique_active_user` otherwise, shown on the field. 409 = already a member. The email
 * input takes focus on open. Groups are global, so the dialog says the change reaches every initiative.
 */
const props = defineProps<{ groupId: string }>()
const emit = defineEmits<{ added: [member: GroupMember] }>()
const open = defineModel<boolean>('open', { required: true })

const api = useApi()
const email = ref('')
const emailError = ref('')
const formError = ref('')
const submitting = ref(false)
const emailInput = useTemplateRef<{ focus: () => void }>('emailInput')

watch(open, (isOpen) => {
  if (!isOpen) return
  email.value = ''
  emailError.value = ''
  formError.value = ''
})

async function submit() {
  if (submitting.value) return
  const value = email.value.trim()
  emailError.value = validateEmail(value) ?? ''
  formError.value = ''
  if (emailError.value) {
    emailInput.value?.focus()
    return
  }
  const body: AddMemberBody = { email: value }
  submitting.value = true
  try {
    const member = await api.post<GroupMember>(`/groups/${encodeURIComponent(props.groupId)}/members`, body)
    open.value = false
    emit('added', member)
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: 'This user is already a member of the group.',
      notFound: 'This group no longer exists. Reload the page.',
      fields: ['email'],
      fieldCopy: EMAIL_FIELD_COPY,
    })
    const duplicate = e instanceof ApiError && e.kind === 'conflict'
    emailError.value = failure.fields.email ?? (duplicate ? failure.message : '')
    formError.value = emailError.value ? '' : failure.message
    if (emailError.value) emailInput.value?.focus()
  }
  finally {
    submitting.value = false
  }
}
</script>

<template>
  <UiDialog
    v-model:open="open"
    title="Add member"
    description="The member gains every access this group is granted, in every initiative."
    :busy="submitting"
    :initial-focus="emailInput"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Email" required hint="Must match exactly one active user." :error="emailError">
          <BaseInput ref="emailInput" v-bind="field" v-model="email" type="email" autocomplete="off" spellcheck="false" />
        </BaseField>
        <BaseText v-if="formError" role="alert" tone="danger" size="sm">
          {{ formError }}
        </BaseText>
        <BaseCluster justify="end">
          <BaseButton variant="secondary" :disabled="submitting" @click="open = false">
            Cancel
          </BaseButton>
          <BaseButton type="submit" :loading="submitting">
            Add member
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
