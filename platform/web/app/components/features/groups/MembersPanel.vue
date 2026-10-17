<script setup lang="ts">
import type { Group, GroupMember, GroupMemberList } from '~/types/api'

/**
 * Members of one group (platform admins only; the API enforces). Keyset list with "Load more", add by email and
 * remove behind a confirmation. Groups are global, so both actions say they reach every initiative. A 404 on the
 * list (group deleted) shows "Group not found". Fixed copy only.
 */
const props = defineProps<{ groupId: string }>()

const api = useApi()
const base = `/groups/${encodeURIComponent(props.groupId)}/members`
const list = usePagedList<GroupMemberList, GroupMember>(base, page => page.members)

const addOpen = ref(false)
const removing = ref<GroupMember | null>(null)
const removeOpen = computed({
  get: () => removing.value !== null,
  set: (value: boolean) => {
    if (!value) removing.value = null
  },
})
const removeBusy = ref(false)
const removeError = ref('')
const notice = ref('')

function askRemove(member: GroupMember) {
  notice.value = ''
  removeError.value = ''
  removing.value = member
}

async function confirmRemove() {
  const member = removing.value
  if (!member || removeBusy.value) return
  removeBusy.value = true
  removeError.value = ''
  try {
    await api.del(`${base}/${encodeURIComponent(member.user_id)}`)
    list.items.value = list.items.value.filter(m => m.user_id !== member.user_id)
    removing.value = null
    notice.value = 'Member removed.'
  }
  catch (e) {
    if (e instanceof ApiError && e.kind === 'not_found') {
      removing.value = null
      notice.value = 'This member is no longer in the group.'
      void list.reload(true)
    }
    else {
      removeError.value = describeMutationError(e, {
        conflict: 'The member could not be removed. Reload the page and try again.',
      }).message
    }
  }
  finally {
    removeBusy.value = false
  }
}

function onAdded() {
  notice.value = 'Member added.'
  void list.reload(true)
}

// The group itself (heading). A failure here only costs the name: the members list has its own error states.
const group = useApiData<Group>(`group-${props.groupId}`, `/groups/${encodeURIComponent(props.groupId)}`)
const notFound = computed(() => list.error.value?.kind === 'not_found' || group.error.value?.kind === 'not_found')
const heading = computed(() => (group.data.value ? groupLabel(group.data.value) : 'Group'))
</script>

<template>
  <BaseStack :gap="4">
    <BasePageHeader :title="heading" description="Groups are global: membership changes reach every initiative that grants to this group.">
      <BaseButton variant="secondary" to="/admin/groups">
        All groups
      </BaseButton>
      <BaseButton v-if="!notFound" @click="addOpen = true">
        <BaseIcon name="plus" size="sm" />
        Add member
      </BaseButton>
    </BasePageHeader>

    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>

    <BaseHeading v-if="!notFound" :level="2" size="xl">
      Group members
    </BaseHeading>

    <BaseBox as="div" :aria-busy="list.loading.value">
      <BaseStack v-if="list.loading.value" :gap="3">
        <BaseText class="sr-only">
          Loading members
        </BaseText>
        <BaseSkeleton />
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
      <BaseEmptyState
        v-else-if="notFound"
        :level="2"
        title="Group not found"
        description="It may have been deleted."
      >
        <BaseButton to="/admin/groups">
          Back to groups
        </BaseButton>
      </BaseEmptyState>
      <FeaturesHomeLoadError
        v-else-if="list.error.value && list.items.value.length === 0"
        :error="list.error.value"
        subject="members"
        forbidden-description="Your account is not allowed to see group members."
        @retry="list.reload()"
      />
      <BaseStack v-else :gap="4">
        <FeaturesGroupsMembersTable caption="Members" :members="list.items.value" @remove="askRemove" />
        <BaseCluster v-if="list.error.value" :gap="2">
          <BaseText role="alert" size="sm" tone="danger">
            Could not load more members.
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

    <FeaturesGroupsAddMemberDialog v-model:open="addOpen" :group-id="groupId" @added="onAdded" />
    <UiConfirmDialog
      v-model:open="removeOpen"
      :title="`Remove ${removing?.email ?? 'member'}?`"
      description="They lose the access this group gives them in every initiative. Grants they hold directly, or through other groups, stay."
      confirm-label="Remove"
      :busy="removeBusy"
      :error="removeError"
      @confirm="confirmRemove"
    />
  </BaseStack>
</template>
