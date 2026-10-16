<script setup lang="ts">
/**
 * Page title (the page's single h1) with an optional description and an actions slot. The h1 is programmatically
 * focusable (`tabindex="-1"`) and `focusTitle()` is exposed, so a page can park focus there when the control that
 * had it disappears (e.g. after a successful retry).
 */
defineProps<{
  title: string
  description?: string
}>()

const heading = useTemplateRef<{ $el: HTMLElement }>('heading')
defineExpose({ focusTitle: () => heading.value?.$el.focus() })
</script>

<template>
  <BaseCluster as="header" justify="between" :gap="4">
    <BaseStack :gap="1">
      <BaseHeading ref="heading" :level="1" tabindex="-1">
        {{ title }}
      </BaseHeading>
      <BaseText v-if="description" tone="muted">
        {{ description }}
      </BaseText>
    </BaseStack>
    <BaseCluster v-if="$slots.default" :gap="2">
      <slot />
    </BaseCluster>
  </BaseCluster>
</template>
