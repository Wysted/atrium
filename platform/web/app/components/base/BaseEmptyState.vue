<script setup lang="ts">
import type { HeadingLevel, IconName } from '~/types/ui'

/** Centered message for "nothing here" and "could not load" situations; actions go in the default slot. */
withDefaults(defineProps<{
  title: string
  description?: string
  icon?: IconName
  level?: HeadingLevel
}>(), {
  description: undefined,
  icon: 'info',
  level: 2,
})
</script>

<template>
  <BaseBox as="section" bordered rounded="md" :padding="8">
    <BaseStack :gap="3" align="center">
      <BaseText as="span" tone="muted">
        <BaseIcon :name="icon" size="lg" />
      </BaseText>
      <BaseHeading :level="level" size="lg">
        {{ title }}
      </BaseHeading>
      <BaseText v-if="description" tone="muted" size="sm">
        {{ description }}
      </BaseText>
      <BaseCluster v-if="$slots.default" justify="center">
        <slot />
      </BaseCluster>
    </BaseStack>
  </BaseBox>
</template>
