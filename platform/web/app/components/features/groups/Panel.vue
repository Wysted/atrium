<script setup lang="ts">
import type { Group, GroupList } from '~/types/api'

/**
 * Groups screen (platform admins only; the page does not mount it for anyone else and the API enforces).
 * Keyset list with "Load more", create, and delete behind a confirmation that explains groups are global.
 * 409 on delete = the group still has members or grants (Go refuses it). Fixed copy only.
 */
const api = useApi()
const list = usePagedList<GroupList, Group>('/groups', page => page.groups)

const createOpen = ref(false)
const removing = ref<Group | null>(null)
const removeOpen = computed({
  get: () => removing.value !== null,
  set: (value: boolean) => {
    if (!value) removing.value = null
  },
})
const removeBusy = ref(false)
const removeError = ref('')
const notice = ref('')

function askRemove(group: Group) {
  notice.value = ''
  removeError.value = ''
  removing.value = group
}

async function confirmRemove() {
  const group = removing.value
  if (!group || removeBusy.value) return
  removeBusy.value = true
  removeError.value = ''
  try {
    await api.del(`/groups/${encodeURIComponent(group.id)}`)
    list.items.value = list.items.value.filter(g => g.id !== group.id)
    removing.value = null
    notice.value = 'Group deleted.'
  }
  catch (e) {
    if (e instanceof ApiError && e.kind === 'not_found') {
      removing.value = null
      notice.value = 'This group no longer exists.'
      void list.reload(true)
    }
    else {
      removeError.value = describeMutationError(e, {
        conflict: 'This group still has members or grants. Remove them first.',
      }).message
    }
  }
  finally {
    removeBusy.value = false
  }
}

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

    <BaseBox as="div" :aria-busy="list.loading.value">
      <BaseStack v-if="list.loading.value" :gap="3">
        <BaseText class="sr-only">
          Loading groups
        </BaseText>
        <BaseSkeleton />
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
      <FeaturesHomeLoadError
        v-else-if="list.error.value && list.items.value.length === 0"
        :error="list.error.value"
        subject="groups"
        forbidden-description="Your account is not allowed to manage groups."
        @retry="list.reload()"
      />
      <BaseStack v-else :gap="4">
        <FeaturesGroupsTable caption="Groups" :groups="list.items.value" @remove="askRemove" />
        <BaseCluster v-if="list.error.value" :gap="2">
          <BaseText role="alert" size="sm" tone="danger">
            Could not load more groups.
          </BaseText>
          <BaseButton size="sm" variant="secondary" :loading="list.loadingMore.value" @click="list.loadMore()">
            Try again
          </BaseButton>
        </BaseCluster>
        <BaseCluster v-else-if="list.hasMore.value" justify="center">
          <BaseButton variant="secondary" :loading="list.loadingMore.value" @click="list.loadMore()">
            Load more
          </BaseButton>
        </BaseCluster>
      </BaseStack>
    </BaseBox>

    <FeaturesGroupsCreateDialog v-model:open="createOpen" @created="onCreated" />
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
