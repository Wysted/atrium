<script setup lang="ts">
import type { Group, GroupList } from '~/types/api'

/**
 * Groups screen (platform admins only; the page does not mount it for anyone else and the API enforces).
 * Keyset list with "Load more", create, and delete behind a confirmation that explains groups are global.
 * 409 on delete = the group still has members or grants (Go refuses it). Fixed copy only.
 */
const api = useApi()
const search = useGroupSearch()
const list = usePagedList<GroupList, Group>(search.path, page => page.groups)
watch(search.query, () => void list.reload())

// Polite live region: the result count after each (re)load.
const announcement = ref('')
watch(list.loading, (now, was) => {
  if (was && !now && !list.error.value) {
    const n = list.items.value.length
    announcement.value = n === 0
      ? 'No groups found'
      : `${n} ${n === 1 ? 'group' : 'groups'} found${list.hasMore.value ? ', more available' : ''}`
  }
})

const createOpen = ref(false)
const { removing, open: removeOpen, busy: removeBusy, error: removeError, notice, ask: askRemove, confirm: confirmRemove } = useRemoveFlow<Group>({
  remove: group => api.del(`/groups/${encodeURIComponent(group.id)}`),
  onRemoved: (group) => {
    list.items.value = list.items.value.filter(g => g.id !== group.id)
  },
  reload: () => void list.reload(true),
  done: 'Group deleted.',
  gone: 'This group no longer exists.',
  conflict: 'This group still has members or grants. Remove them first.',
})

function onCreated() {
  notice.value = 'Group created.'
  void list.reload(true)
}
</script>

<template>
  <BaseStack :gap="4">
    <BasePageHeader title="Groups" description="Groups are global: access granted to a group applies in every initiative that grants to it.">
      <BaseButton @click="createOpen = true">
        <BaseIcon name="plus" size="sm" />
        New group
      </BaseButton>
    </BasePageHeader>

    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>

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

    <FeaturesAdminPagedBlock :list="list" subject="groups" forbidden-description="Your account is not allowed to manage groups.">
      <FeaturesGroupsTable
        caption="Groups"
        :groups="list.items.value"
        :empty-text="search.query.value ? 'No groups match your search.' : undefined"
        @remove="askRemove"
      />
    </FeaturesAdminPagedBlock>

    <FeaturesAdminCreateKeyDialog
      v-model:open="createOpen"
      endpoint="/groups"
      title="New group"
      description="A group can be granted access in any initiative. Its key identifies it and cannot be changed later."
      submit-label="Create group"
      conflict="A group with this key already exists. If you just retried, it may have been created by your earlier attempt: check the list."
      @created="onCreated"
    />
    <UiConfirmDialog
      v-model:open="removeOpen"
      :title="`Delete group ${removing ? groupLabel(removing) : ''}?`"
      description="Groups are global: deleting this one affects every initiative that could grant to it. A group with members or grants cannot be deleted; remove them first."
      confirm-label="Delete group"
      :busy="removeBusy"
      :error="removeError"
      @confirm="confirmRemove"
    />
  </BaseStack>
</template>
