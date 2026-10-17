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
const { removing, open: removeOpen, busy: removeBusy, error: removeError, notice, ask: askRemove, confirm: confirmRemove } = useRemoveFlow<GroupMember>({
  remove: member => api.del(`${base}/${encodeURIComponent(member.user_id)}`),
  onRemoved: (member) => {
    list.items.value = list.items.value.filter(m => m.user_id !== member.user_id)
  },
  reload: () => void list.reload(true),
  done: 'Member removed.',
  gone: 'This member is no longer in the group.',
  conflict: 'The member could not be removed. Reload the page and try again.',
})

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

    <BaseEmptyState
      v-if="notFound"
      :level="2"
      title="Group not found"
      description="It may have been deleted."
    >
      <BaseButton to="/admin/groups">
        Back to groups
      </BaseButton>
    </BaseEmptyState>
    <FeaturesAdminPagedBlock v-else :list="list" subject="members" forbidden-description="Your account is not allowed to see group members.">
      <FeaturesGroupsMembersTable caption="Members" :members="list.items.value" @remove="askRemove" />
    </FeaturesAdminPagedBlock>

    <FeaturesAdminAddPersonDialog
      v-model:open="addOpen"
      :endpoint="base"
      title="Add member"
      description="The member gains every access this group is granted, in every initiative."
      submit-label="Add member"
      conflict="This user is already a member of the group."
      not-found="This group no longer exists. Reload the page."
      @added="onAdded"
    />
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
