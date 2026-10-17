<script setup lang="ts">
import type { CreateGroupBody, Group } from '~/types/api'

/**
 * Dialog to create a group (platform admins; the API enforces). Groups are global: they can be granted access in
 * any initiative. Key and name are validated here like Go; the key cannot be changed later. 409 = key taken, shown
 * on the key field (it may be our own earlier attempt). The Key input takes focus on open.
 */
const emit = defineEmits<{ created: [group: Group] }>()
const open = defineModel<boolean>('open', { required: true })

const api = useApi()
const key = ref('')
const name = ref('')
const keyError = ref('')
const nameError = ref('')
const formError = ref('')
const submitting = ref(false)
const keyInput = useTemplateRef<{ focus: () => void }>('keyInput')
const nameInput = useTemplateRef<{ focus: () => void }>('nameInput')

watch(open, (isOpen) => {
  if (!isOpen) return
  key.value = ''
  name.value = ''
  keyError.value = ''
  nameError.value = ''
  formError.value = ''
})

function validate(): boolean {
  keyError.value = validateGroupKey(key.value.trim()) ?? ''
  nameError.value = validateGroupName(name.value.trim()) ?? ''
  formError.value = ''
  if (keyError.value) keyInput.value?.focus()
  else if (nameError.value) nameInput.value?.focus()
  return !keyError.value && !nameError.value
}

async function submit() {
  if (submitting.value || !validate()) return
  const body: CreateGroupBody = { key: key.value.trim() }
  const trimmedName = name.value.trim()
  if (trimmedName) body.name = trimmedName
  submitting.value = true
  try {
    const created = await api.post<Group>('/groups', body)
    open.value = false
    emit('created', created)
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: 'A group with this key already exists. If you just retried, it may have been created by your earlier attempt: check the list.',
      fields: ['key', 'name'],
      fieldCopy: GROUP_FIELD_COPY,
    })
    const duplicate = e instanceof ApiError && e.kind === 'conflict'
    keyError.value = failure.fields.key ?? (duplicate ? failure.message : '')
    nameError.value = failure.fields.name ?? ''
    formError.value = duplicate ? '' : failure.message
    if (keyError.value) keyInput.value?.focus()
    else if (nameError.value) nameInput.value?.focus()
  }
  finally {
    submitting.value = false
  }
}
</script>

<template>
  <UiDialog
    v-model:open="open"
    title="New group"
    description="A group can be granted access in any initiative. Its key identifies it and cannot be changed later."
    :busy="submitting"
    :initial-focus="keyInput"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Key" required :hint="GROUP_KEY_RULES" :error="keyError">
          <BaseInput ref="keyInput" v-bind="field" v-model="key" autocomplete="off" autocapitalize="none" spellcheck="false" />
        </BaseField>
        <BaseField v-slot="field" label="Name" hint="Optional. Shown instead of the key." :error="nameError">
          <BaseInput ref="nameInput" v-bind="field" v-model="name" autocomplete="off" />
        </BaseField>
        <BaseText v-if="formError" role="alert" tone="danger" size="sm">
          {{ formError }}
        </BaseText>
        <BaseCluster justify="end">
          <BaseButton variant="secondary" :disabled="submitting" @click="open = false">
            Cancel
          </BaseButton>
          <BaseButton type="submit" :loading="submitting">
            Create group
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
