<script setup lang="ts">
withDefaults(defineProps<{
  as?: string
  size?: 'page' | 'reading'
}>(), {
  as: 'div',
  size: 'page',
})

const sizes = { page: 'max-w-page', reading: 'max-w-reading' }
</script>

<template>
  <component :is="as" :class="['mx-auto w-full px-gutter', sizes[size]]">
    <slot />
  </component>
</template>
