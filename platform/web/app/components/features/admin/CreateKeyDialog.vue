<script setup lang="ts">
import type { CreateGroupBody } from '~/types/api'

/**
 * Dialog to create a keyed thing (a group or an area; platform admins, the API enforces) by POST to `endpoint`. Key and
 * name follow the group rules and are validated here like Go; the key cannot be changed later. 409 = key taken, shown
 * on the key field (it may be our own earlier attempt). The Key input takes focus on open.
 */
const props = defineProps<{
  endpoint: string
  title: string
  description: string
  submitLabel: string
  conflict: string
}>()
const emit = defineEmits<{ created: [] }>()
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
    await api.post(props.endpoint, body)
    open.value = false
    emit('created')
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: props.conflict,
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
    :title="title"
    :description="description"
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
            {{ submitLabel }}
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
