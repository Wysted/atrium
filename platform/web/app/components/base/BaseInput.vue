<script setup lang="ts">
/**
 * Single-line text input with `v-model`. Wire label, hint and error through `BaseField`, which passes `id`,
 * `aria-describedby`, `aria-invalid` and `aria-required` (all fall through to the <input>). Invalid state is
 * styled from `aria-invalid`. `focus()` is exposed so a form can move focus to the first invalid field.
 */
withDefaults(defineProps<{
  type?: 'text' | 'email' | 'search' | 'datetime-local'
  readonly?: boolean
  disabled?: boolean
}>(), {
  type: 'text',
})

const model = defineModel<string>({ default: '' })
const input = useTemplateRef<HTMLInputElement>('input')
defineExpose({ focus: () => input.value?.focus() })
</script>

<template>
  <input
    ref="input"
    v-model="model"
    :type="type"
    :readonly="readonly"
    :disabled="disabled"
    class="w-full rounded-md border border-border bg-surface px-3 py-2 text-base text-text placeholder:text-text-muted read-only:bg-surface-raised focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-danger"
  >
</template>
