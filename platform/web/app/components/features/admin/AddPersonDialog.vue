<script setup lang="ts">
import type { AddMemberBody } from '~/types/api'

/**
 * Dialog to add a member or supervisor to a group or an area by email (POST to `endpoint`; platform admins, the API
 * enforces). The email must match exactly one active user: 422 `no_unique_active_user` otherwise, shown on the field.
 * 409 = `conflict`. 404 = the parent is gone: `notFound` is shown and `not-found` emitted. The email input takes focus on open.
 */
const props = defineProps<{
  endpoint: string
  title: string
  description: string
  submitLabel: string
  conflict: string
  notFound: string
}>()
const emit = defineEmits<{ added: [], notFound: [] }>()
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
    await api.post(props.endpoint, body)
    open.value = false
    emit('added')
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: props.conflict,
      notFound: props.notFound,
      fields: ['email'],
      fieldCopy: EMAIL_FIELD_COPY,
    })
    const duplicate = e instanceof ApiError && e.kind === 'conflict'
    if (e instanceof ApiError && e.kind === 'not_found') emit('notFound')
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
    :title="title"
    :description="description"
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
            {{ submitLabel }}
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
