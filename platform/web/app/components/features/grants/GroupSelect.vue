<script setup lang="ts">
import type { Group, GroupList } from '~/types/api'

/**
 * Group picker for a grant: a native select over GET /groups (paged; "Load more groups" while there is a next
 * page). Platform admins and managers may list groups (id, key, name; never members). Mount it only when needed:
 * it fetches on mount. Scaling debt: no search, so a very large group list means many "Load more" clicks. Attributes from `BaseField` fall through to the <select>. `v-model` is the group id.
 */
defineOptions({ inheritAttrs: false })

const model = defineModel<string>({ default: '' })
const groups = usePagedList<GroupList, Group>('/groups', page => page.groups)
const select = useTemplateRef<{ focus: () => void }>('select')
defineExpose({ focus: () => select.value?.focus() })

const options = computed(() =>
  groups.items.value.map(g => ({ value: g.id, label: g.name ? `${g.name} (${g.key})` : g.key })),
)

// A selected id that is not among the loaded options (stale value) is cleared.
watch(
  () => [options.value, groups.loading.value] as const,
  ([list, loading]) => {
    if (model.value && !loading && !list.some(o => o.value === model.value)) model.value = ''
  },
)

// Polite live region: the total after each "Load more groups".
const announcement = ref('')
watch(groups.loadingMore, (now, was) => {
  if (was && !now && !groups.error.value) {
    const n = groups.items.value.length
    announcement.value = `${n} ${n === 1 ? 'group' : 'groups'} loaded`
  }
})
</script>

<template>
  <BaseStack :gap="2">
    <BaseText role="status" class="sr-only">
      {{ announcement }}
    </BaseText>
    <BaseSelect
      ref="select"
      v-bind="$attrs"
      v-model="model"
      :options="options"
      :placeholder="groups.loading.value ? 'Loading groups' : 'Choose a group'"
      :disabled="groups.loading.value"
    />
    <BaseCluster v-if="groups.error.value" :gap="2">
      <BaseText role="alert" size="sm" tone="danger">
        Could not load the groups.
      </BaseText>
      <BaseButton
        size="sm"
        variant="secondary"
        :loading="groups.loadingMore.value || groups.loading.value"
        @click="groups.items.value.length === 0 ? groups.reload() : groups.loadMore()"
      >
        Try again
      </BaseButton>
    </BaseCluster>
    <BaseCluster v-else-if="groups.hasMore.value">
      <BaseButton size="sm" variant="secondary" :loading="groups.loadingMore.value" @click="groups.loadMore()">
        Load more groups
      </BaseButton>
    </BaseCluster>
    <BaseText v-else-if="!groups.loading.value && groups.items.value.length === 0" size="sm" tone="muted">
      There are no groups yet.
    </BaseText>
  </BaseStack>
</template>
