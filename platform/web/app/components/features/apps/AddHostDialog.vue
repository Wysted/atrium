<script setup lang="ts">
import type { App, CreateDeploymentBody, Deployment } from '~/types/api'

/**
 * Dialog to add a host (deployment) to an app. The input is trimmed and ASCII-lowercased explicitly, then
 * validated like Go `hostname.Valid`. Adding is idempotent by host, so a 503 can be retried; a 409 may be our own
 * earlier attempt and says so. 422 `reserved` = the host of the sign-in service.
 */
const props = defineProps<{
  app: App | null
}>()
const emit = defineEmits<{ added: [deployment: Deployment] }>()
const open = defineModel<boolean>('open', { required: true })

const api = useApi()
const host = ref('')
const hostError = ref('')
const formError = ref('')
const submitting = ref(false)
const normalizedPreview = computed(() => {
  const trimmed = host.value.trim()
  const normalized = normalizeHost(host.value)
  return trimmed !== '' && normalized !== trimmed ? normalized : ''
})
const hostInput = useTemplateRef<{ focus: () => void }>('hostInput')

watch(open, (isOpen) => {
  if (!isOpen) return
  host.value = ''
  hostError.value = ''
  formError.value = ''
})

async function submit() {
  const app = props.app
  if (!app || submitting.value) return
  formError.value = ''
  const normalized = normalizeHost(host.value)
  host.value = normalized
  hostError.value = validateHost(normalized) ?? ''
  if (hostError.value) {
    hostInput.value?.focus()
    return
  }
  const body: CreateDeploymentBody = { host: normalized }
  submitting.value = true
  try {
    const created = await api.post<Deployment>(`/apps/${encodeURIComponent(app.id)}/deployments`, body)
    open.value = false
    emit('added', created)
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: HOST_TAKEN_COPY,
      notFound: 'This app no longer exists. Reload the page.',
      fields: ['host'],
      fieldCopy: APP_FIELD_COPY,
    })
    const duplicate = e instanceof ApiError && e.kind === 'conflict'
    hostError.value = failure.fields.host ?? (duplicate ? failure.message : '')
    formError.value = duplicate ? '' : failure.message
    if (hostError.value) hostInput.value?.focus()
  }
  finally {
    submitting.value = false
  }
}
</script>

<template>
  <UiDialog
    v-model:open="open"
    title="Add host"
    :description="app ? `Requests for this host are routed to ${app.name || app.id}.` : 'Add a host to the app.'"
    :busy="submitting"
    :initial-focus="hostInput"
  >
    <form novalidate @submit.prevent="submit">
      <BaseStack :gap="4">
        <BaseField v-slot="field" label="Host" required :hint="HOST_HINT" :error="hostError">
          <BaseInput
            ref="hostInput"
            v-bind="field"
            v-model="host"
            autocomplete="off"
            autocapitalize="none"
            spellcheck="false"
            placeholder="app.example.com"
          />
        </BaseField>
        <BaseText role="status" aria-live="polite" size="sm" tone="muted" :class="normalizedPreview ? undefined : 'sr-only'">
          {{ normalizedPreview ? `Will be added as ${normalizedPreview}` : '' }}
        </BaseText>
        <BaseText v-if="formError" role="alert" tone="danger" size="sm">
          {{ formError }}
        </BaseText>
        <BaseCluster justify="end">
          <BaseButton variant="secondary" :disabled="submitting" @click="open = false">
            Cancel
          </BaseButton>
          <BaseButton type="submit" :loading="submitting">
            Add host
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </form>
  </UiDialog>
</template>
