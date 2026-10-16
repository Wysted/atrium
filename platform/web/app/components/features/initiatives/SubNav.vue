<script setup lang="ts">
/**
 * Sub-navigation of one initiative: Overview, Grants, Roles. Plain links (not a tabs widget); the current one
 * carries `aria-current="page"`. Every initiative viewer sees all three: managers read the roles and edit grants,
 * the API enforces what each can do.
 */
const props = defineProps<{
  initiativeId: string
}>()

const base = computed(() => `/admin/initiatives/${encodeURIComponent(props.initiativeId)}`)
const items = computed(() => [
  { label: 'Overview', to: base.value, exact: true },
  { label: 'Grants', to: `${base.value}/grants`, exact: false },
  { label: 'Roles', to: `${base.value}/roles`, exact: false },
])

const route = useRoute()
const isCurrent = (item: { to: string, exact: boolean }) =>
  route.path === item.to || (!item.exact && route.path.startsWith(`${item.to}/`))
</script>

<template>
  <nav aria-label="Initiative">
    <BaseCluster as="ul" :gap="1">
      <li v-for="item in items" :key="item.to">
        <NuxtLink
          :to="item.to"
          :aria-current="isCurrent(item) ? 'page' : undefined"
          class="inline-block rounded-md px-3 py-2 text-sm font-medium no-underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
          :class="isCurrent(item) ? 'bg-surface-raised text-text' : 'text-text-muted hover:text-text'"
        >
          {{ item.label }}
        </NuxtLink>
      </li>
    </BaseCluster>
  </nav>
</template>
