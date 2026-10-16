<script setup lang="ts">
import type { App, CreateAppBody } from '~/types/api'

/**
 * Dialog to create an app in an initiative (platform admins; the API enforces). The name is optional. Creating
 * is NOT idempotent (the server generates the id): the submit is blocked while the request runs, nothing retries,
 * and an ambiguous failure (network error, 503 or other 5xx after sending) does not offer a retry. It emits
 * `uncertain` instead and closes, so the panel reloads the list and shows the fixed "may have been created" copy.
 */
const props = defineProps<{
  initiativeId: string
}>()
const emit = defineEmits<{ created: [app: App], uncertain: [] }>()
const open = defineModel<boolean>('open', { required: true })

const api = useApi()
const name = ref('')
const nameError = ref('')
const formError = ref('')
const submitting = ref(false)
const nameInput = useTemplateRef<{ focus: () => void }>('nameInput')

watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = ''
  nameError.value = ''
  formError.value = ''
})

async function submit() {
  if (submitting.value) return
  formError.value = ''
  nameError.value = validateAppName(name.value.trim()) ?? ''
  if (nameError.value) {
    nameInput.value?.focus()
    return
  }
  const body: CreateAppBody = { name: name.value.trim() }
  submitting.value = true
  try {
    const created = await api.post<App>(`/initiatives/${encodeURIComponent(props.initiativeId)}/apps`, body)
    open.value = false
    emit('created', created)
  }
  catch (e) {
    if (isAmbiguousFailure(e)) {
      open.value = false
      emit('uncertain')
      return
    }
    const failure = describeMutationError(e, {
      conflict: 'The initiative changed in the meantime. Reload the page and try again.',
      notFound: 'This initiative no longer exists.',
      fields: ['name'],
      fieldCopy: APP_FIELD_COPY,
    })
    nameError.value = failure.fields.name ?? ''
    formError.value = failure.message
    if (nameError.value) nameInput.value?.focus()
  }
  finally {
    submitting.value = false
  }
}
</script>

<template>
  <UiDialog
    v-model:open="open"
    title="New app"
    description="The app id is generated and cannot be changed. Add its hosts once it exists."
    :busy="submitting"
    :initial-focus="nameInput"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Name" hint="Optional. The id is shown when the name is empty." :error="nameError">
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
            Create app
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
