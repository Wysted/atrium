<script setup lang="ts">
import type { App, PatchAppBody } from '~/types/api'

/**
 * Dialog to rename an app (PATCH `name`, idempotent). Clearing the name is valid (the UI then shows the id).
 * `app` is the row being edited; the dialog emits `saved` with the PATCH response.
 */
const props = defineProps<{
  app: App | null
}>()
const emit = defineEmits<{ saved: [app: App] }>()
const open = defineModel<boolean>('open', { required: true })

const api = useApi()
const name = ref('')
const nameError = ref('')
const formError = ref('')
const submitting = ref(false)
const nameInput = useTemplateRef<{ focus: () => void }>('nameInput')

watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = props.app?.name ?? ''
  nameError.value = ''
  formError.value = ''
})

async function submit() {
  const app = props.app
  if (!app || submitting.value) return
  formError.value = ''
  nameError.value = validateAppName(name.value.trim()) ?? ''
  if (nameError.value) {
    nameInput.value?.focus()
    return
  }
  const body: PatchAppBody = { name: name.value.trim() }
  submitting.value = true
  try {
    const updated = await api.patch<App>(`/apps/${encodeURIComponent(app.id)}`, body)
    open.value = false
    emit('saved', updated)
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: 'The app changed in the meantime. Reload the page and try again.',
      notFound: 'This app no longer exists. Reload the page.',
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
    title="Rename app"
    :description="app ? `Change the name of ${app.id}. The id stays the same.` : 'Change the name of the app.'"
    :busy="submitting"
    :initial-focus="nameInput"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Name" hint="Leave it empty to show the id." :error="nameError">
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
            Save name
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
