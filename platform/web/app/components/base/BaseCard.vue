<script setup lang="ts">
import type { Space } from '~/types/ui'

/** A bordered raised surface that groups related content. Use `as="li"` inside a list. */
withDefaults(defineProps<{
  as?: string
  padding?: Space
}>(), {
  as: 'div',
  padding: 4,
})
</script>

<template>
  <BaseBox :as="as" surface="raised" bordered rounded="md" shadow="sm" :padding="padding">
    <slot />
  </BaseBox>
</template>
