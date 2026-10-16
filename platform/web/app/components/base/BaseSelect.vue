<script setup lang="ts">
import type { SelectOption } from '~/types/ui'

/**
 * Native `<select>` with `v-model` (string value). Reka's Select injects a `<style>` element, which the CSP
 * forbids; the native control also gives the platform picker and keyboard behaviour for free. Wire label, hint
 * and error through `BaseField` (`id`, `aria-*` fall through to the <select>). A `placeholder` adds a disabled
 * first option with the empty value, shown while nothing is chosen. `focus()` is exposed.
 */
defineProps<{
  options: SelectOption[]
  placeholder?: string
  disabled?: boolean
}>()

const model = defineModel<string>({ default: '' })
const select = useTemplateRef<HTMLSelectElement>('select')
defineExpose({ focus: () => select.value?.focus() })
</script>

<template>
  <select
    ref="select"
    v-model="model"
    :disabled="disabled"
    class="w-full rounded-md border border-border bg-surface px-3 py-2 text-base text-text focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-danger"
  >
    <option v-if="placeholder !== undefined" value="" disabled>
      {{ placeholder }}
    </option>
    <option v-for="option in options" :key="option.value" :value="option.value" :disabled="option.disabled">
      {{ option.label }}
    </option>
  </select>
</template>
