<script setup lang="ts">
import type { CreateInitiativeBody, Initiative } from '~/types/api'

/**
 * Dialog to create an initiative (platform admins; the API enforces). The key is validated here with the same
 * rules as the server and is immutable afterwards. Server errors map to fixed copy: 422 per field, 409 "key
 * already exists" on the key field, 503/network as a retryable message. Creation is idempotent in effect (a repeat
 * never makes a duplicate) but a repeat answers 409, so the 409 copy mentions an earlier attempt. The Key input
 * takes focus on open.
 */
const emit = defineEmits<{ created: [initiative: Initiative] }>()
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

// Every opening starts from a clean form.
watch(open, (isOpen) => {
  if (!isOpen) return
  key.value = ''
  name.value = ''
  keyError.value = ''
  nameError.value = ''
  formError.value = ''
})

function validate(): boolean {
  keyError.value = validateInitiativeKey(key.value.trim()) ?? ''
  nameError.value = validateInitiativeName(name.value.trim()) ?? ''
  formError.value = ''
  if (keyError.value) keyInput.value?.focus()
  else if (nameError.value) nameInput.value?.focus()
  return !keyError.value && !nameError.value
}

async function submit() {
  if (submitting.value || !validate()) return
  const body: CreateInitiativeBody = { key: key.value.trim() }
  const trimmedName = name.value.trim()
  if (trimmedName) body.name = trimmedName
  submitting.value = true
  try {
    const created = await api.post<Initiative>('/initiatives', body)
    open.value = false
    emit('created', created)
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: 'An initiative with this key already exists. If you just retried, it may have been created by your earlier attempt: check the list.',
      fields: ['key', 'name'],
      fieldCopy: INITIATIVE_FIELD_COPY,
    })
    // A duplicate key belongs to the key field, not to the banner.
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
    title="New initiative"
    description="An initiative groups apps, roles and access. Its key identifies it and cannot be changed later."
    :busy="submitting"
    :initial-focus="keyInput"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Key" required :hint="INITIATIVE_KEY_RULES" :error="keyError">
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
            Create initiative
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
