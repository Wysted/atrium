<script setup lang="ts">
/**
 * Navigation of the administration area. The UI only hides what the caller cannot use; the API enforces.
 * Only screens that exist are listed. Planned: Groups (platform admin and managers), Users (platform admin),
 * Audit (both); each is added with its screen, with a `visible` rule from /me.
 */
const props = defineProps<{
  isPlatformAdmin: boolean
  isManager: boolean
}>()

interface NavItem { label: string, to: string, visible: boolean }

const items = computed<NavItem[]>(() => [
  { label: 'Initiatives', to: '/admin/initiatives', visible: props.isPlatformAdmin || props.isManager },
].filter(item => item.visible))

const route = useRoute()
const isCurrent = (to: string) => route.path === to || route.path.startsWith(`${to}/`)
</script>

<template>
  <nav aria-label="Administration">
    <BaseCluster as="ul" :gap="1">
      <li v-for="item in items" :key="item.to">
        <NuxtLink
          :to="item.to"
          :aria-current="isCurrent(item.to) ? 'page' : undefined"
          class="inline-block rounded-md px-3 py-2 text-sm font-medium no-underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
          :class="isCurrent(item.to) ? 'bg-surface-raised text-text' : 'text-text-muted hover:text-text'"
        >
          {{ item.label }}
        </NuxtLink>
      </li>
    </BaseCluster>
  </nav>
</template>
