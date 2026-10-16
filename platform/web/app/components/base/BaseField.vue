<script setup lang="ts">
/**
 * Label + control + hint + error. The control goes in the default slot and receives the wiring as slot props:
 * `<BaseField label="Name" :error="e" v-slot="field"><BaseInput v-bind="field" v-model="name" /></BaseField>`.
 * `field` carries `id` (matches the label `for`), `aria-describedby` (hint and error ids, only those rendered),
 * `aria-invalid` and `aria-required`. A `required` mark is added to the label text; the error is plain text next
 * to the control, so it is not colour-only.
 */
const props = defineProps<{
  label: string
  hint?: string
  error?: string
  required?: boolean
}>()

const uid = useId()
const id = `field-${uid}`
const hintId = `${id}-hint`
const errorId = `${id}-error`

const describedBy = computed(() => {
  const ids = [props.hint ? hintId : '', props.error ? errorId : ''].filter(Boolean)
  return ids.length ? ids.join(' ') : undefined
})
const wiring = computed(() => ({
  'id': id,
  'aria-describedby': describedBy.value,
  'aria-invalid': props.error ? ('true' as const) : undefined,
  'aria-required': props.required ? ('true' as const) : undefined,
}))
</script>

<template>
  <BaseStack :gap="1">
    <label :for="id" class="text-sm font-medium text-text">
      {{ label }}
      <span v-if="required" aria-hidden="true" class="text-danger"> *</span>
    </label>
    <slot v-bind="wiring" />
    <BaseText v-if="hint" :id="hintId" size="sm" tone="muted">
      {{ hint }}
    </BaseText>
    <BaseText v-if="error" :id="errorId" size="sm" tone="danger">
      {{ error }}
    </BaseText>
  </BaseStack>
</template>
