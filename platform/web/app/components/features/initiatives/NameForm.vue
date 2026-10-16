<script setup lang="ts">
import type { Initiative, PatchInitiativeBody } from '~/types/api'

/**
 * Overview of an initiative: the key (immutable, always read-only) and the name. With `editable` (platform
 * admins; the API enforces) the name can be changed with PATCH; otherwise both fields are read-only. On success
 * it emits `saved` so the page refreshes the data. The PATCH is idempotent, so a 503 can simply be retried.
 */
const props = defineProps<{
  initiative: Initiative
  editable: boolean
}>()

const emit = defineEmits<{ saved: [initiative: Initiative] }>()

const api = useApi()
const name = ref(props.initiative.name)
const nameError = ref('')
const formError = ref('')
const notice = ref('')
const saving = ref(false)
const nameInput = useTemplateRef<{ focus: () => void }>('nameInput')

// Follow the server value after a refresh (own save or a reload).
watch(() => props.initiative.name, (value) => {
  name.value = value
})

const dirty = computed(() => name.value.trim() !== props.initiative.name)

async function submit() {
  if (saving.value || !dirty.value) return
  notice.value = ''
  formError.value = ''
  nameError.value = validateInitiativeName(name.value.trim()) ?? ''
  if (nameError.value) {
    nameInput.value?.focus()
    return
  }
  const body: PatchInitiativeBody = { name: name.value.trim() }
  saving.value = true
  try {
    const updated = await api.patch<Initiative>(`/initiatives/${encodeURIComponent(props.initiative.id)}`, body)
    notice.value = 'Name saved.'
    emit('saved', updated)
  }
  catch (e) {
    const failure = describeMutationError(e, {
      conflict: 'The initiative changed in the meantime. Reload the page and try again.',
      fields: ['name'],
      fieldCopy: INITIATIVE_FIELD_COPY,
    })
    nameError.value = failure.fields.name ?? ''
    formError.value = failure.message
    if (nameError.value) nameInput.value?.focus()
  }
  finally {
    saving.value = false
  }
}
</script>

<template>
  <form novalidate @submit.prevent="submit">
    <BaseStack :gap="4">
      <BaseField v-slot="field" label="Key" hint="The key identifies the initiative and cannot be changed.">
        <BaseInput v-bind="field" :model-value="initiative.key" readonly />
      </BaseField>
      <BaseField v-slot="field" label="Name" :hint="editable ? 'Shown instead of the key when set.' : undefined" :error="nameError">
        <BaseInput ref="nameInput" v-bind="field" v-model="name" :readonly="!editable" autocomplete="off" />
      </BaseField>
      <template v-if="editable">
        <BaseText v-if="formError" role="alert" tone="danger" size="sm">
          {{ formError }}
        </BaseText>
        <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
          {{ notice }}
        </BaseText>
        <BaseCluster>
          <BaseButton type="submit" :loading="saving" :disabled="!dirty && !saving">
            Save name
          </BaseButton>
        </BaseCluster>
      </template>
    </BaseStack>
  </form>
</template>
