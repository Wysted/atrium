<script setup lang="ts">
import type { Group, GroupList } from '~/types/api'

/**
 * Group picker for a grant: a search box (debounced, `GET /groups?q=`) over a native select, with "Load more groups"
 * for the current query. Platform admins and managers may list groups (id, key, name; never members). Mount it only
 * when needed: it fetches on mount. The chosen group stays in the select even when a later search does not return it.
 * Attributes from `BaseField` fall through to the <select>. `v-model` is the group id.
 */
defineOptions({ inheritAttrs: false })

const model = defineModel<string>({ default: '' })
const search = useGroupSearch()
const groups = usePagedList<GroupList, Group>(search.path, page => page.groups)
// usePagedList reads the path getter only on mount and loadMore: reload on a new query so the cursor matches its q.
watch(search.query, () => void groups.reload())
const select = useTemplateRef<{ focus: () => void }>('select')
defineExpose({ focus: () => select.value?.focus() })

// The chosen group, remembered so it stays listed when the current results do not include it.
const chosen = ref<Group | null>(null)
watch(
  [model, groups.items, groups.loading],
  ([id, items, loading]) => {
    if (!id) chosen.value = null
    else chosen.value = items.find(g => g.id === id) ?? (chosen.value?.id === id ? chosen.value : null)
    // Once loaded, a model that is neither listed nor remembered would submit an invisible group id: clear it.
    if (id && !chosen.value && !loading && !groups.error.value) model.value = ''
  },
  { immediate: true },
)

function toOption(g: Group) {
  return { value: g.id, label: g.name ? `${g.name} (${g.key})` : g.key }
}
const options = computed(() => {
  const list = groups.items.value.map(toOption)
  const pick = chosen.value
  if (pick && !list.some(o => o.value === pick.id)) list.unshift(toOption(pick))
  return list
})

// Polite live region: the result count after each (re)load and each "Load more groups".
const announcement = ref('')
function announce() {
  if (groups.error.value) return
  const n = groups.items.value.length
  announcement.value = n === 0
    ? 'No groups found'
    : `${n} ${n === 1 ? 'group' : 'groups'} found${groups.hasMore.value ? ', more available' : ''}`
}
watch(groups.loading, (now, was) => {
  if (was && !now) announce()
})
watch(groups.loadingMore, (now, was) => {
  if (was && !now) announce()
})
</script>

<template>
  <BaseStack :gap="2">
    <BaseText role="status" class="sr-only">
      {{ announcement }}
    </BaseText>
    <BaseField v-slot="field" label="Search groups" hint="Matches the key or the name.">
      <BaseInput
        v-bind="field"
        v-model="search.input.value"
        type="search"
        autocomplete="off"
        :maxlength="GROUP_SEARCH_MAX"
      />
    </BaseField>
    <BaseSelect
      ref="select"
      v-bind="$attrs"
      v-model="model"
      :options="options"
      :placeholder="groups.loading.value ? 'Loading groups' : 'Choose a group'"
      :disabled="groups.loading.value && options.length === 0"
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
      {{ search.query.value ? 'No groups match your search.' : 'There are no groups yet.' }}
    </BaseText>
  </BaseStack>
</template>
