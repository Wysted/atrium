<script setup lang="ts">
/**
 * Native checkbox with its own visible label (default slot) and an optional hint, wired with `aria-describedby`.
 * `v-model` is a boolean. Other attributes (`disabled`, `name`...) fall through to the <input>.
 */
defineOptions({ inheritAttrs: false })
defineProps<{
  hint?: string
}>()

const model = defineModel<boolean>({ default: false })
const uid = useId()
const id = `checkbox-${uid}`
const hintId = `${id}-hint`
</script>

<template>
  <BaseStack :gap="1">
    <label :for="id" class="inline-flex items-center gap-2 text-sm font-medium text-text">
      <input
        :id="id"
        v-model="model"
        type="checkbox"
        :aria-describedby="hint ? hintId : undefined"
        v-bind="$attrs"
        class="size-4 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus disabled:cursor-not-allowed disabled:opacity-60"
      >
      <slot />
    </label>
    <BaseText v-if="hint" :id="hintId" size="sm" tone="muted">
      {{ hint }}
    </BaseText>
  </BaseStack>
</template>
