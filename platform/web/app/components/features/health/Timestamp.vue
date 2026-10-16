<script setup lang="ts">
/** A point in time as relative text with the absolute time underneath; `emptyText` when the value is null. */
defineProps<{
  value: string | null
  emptyText: string
}>()
</script>

<template>
  <BaseStack v-if="value" :gap="0">
    <BaseText as="span" size="sm">
      {{ formatRelative(value) }}
    </BaseText>
    <BaseText as="time" size="xs" tone="muted" :datetime="value">
      {{ formatTimestamp(value) }}
    </BaseText>
  </BaseStack>
  <BaseText v-else as="span" size="sm" tone="muted">
    {{ emptyText }}
  </BaseText>
</template>
