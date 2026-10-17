<script setup lang="ts">
import type { AreaPersonKind, AreaPersonList, GroupMember } from '~/types/api'

/**
 * Members or supervisors of one area (platform admins only; the API enforces): keyset list with "Load more", add by
 * email, remove behind a confirmation. A 404 on the list (area deleted) is reported to the parent via `not-found`.
 */
const props = defineProps<{ areaId: string, kind: AreaPersonKind }>()
const emit = defineEmits<{ notFound: [] }>()

const isMember = props.kind === 'members'
const noun = isMember ? 'member' : 'supervisor'
const api = useApi()
const base = `/areas/${encodeURIComponent(props.areaId)}/${props.kind}`
const list = usePagedList<AreaPersonList, GroupMember>(base, page => page[props.kind] ?? [])
watch(() => list.error.value?.kind, (kind) => {
  if (kind === 'not_found') emit('notFound')
})

const addOpen = ref(false)
const label = isMember ? 'Member' : 'Supervisor'
const { removing, open: removeOpen, busy: removeBusy, error: removeError, notice, ask: askRemove, confirm: confirmRemove } = useRemoveFlow<GroupMember>({
  remove: person => api.del(`${base}/${encodeURIComponent(person.user_id)}`),
  onRemoved: (person) => {
    list.items.value = list.items.value.filter(p => p.user_id !== person.user_id)
  },
  reload: () => void list.reload(true),
  done: `${label} removed.`,
  gone: `This ${noun} is no longer in the area.`,
  conflict: `The ${noun} could not be removed. Reload the page and try again.`,
})

function onAdded() {
  notice.value = `${label} added.`
  void list.reload(true)
}
</script>

<template>
  <BaseStack :gap="4">
    <BaseCluster justify="between">
      <BaseHeading :level="2" size="xl">
        {{ label }}s
      </BaseHeading>
      <BaseButton @click="addOpen = true">
        <BaseIcon name="plus" size="sm" />
        Add {{ noun }}
      </BaseButton>
    </BaseCluster>
    <BaseText size="sm" tone="muted">
      {{ isMember
        ? 'The people whose robots and API tokens this area\'s supervisors manage. A person belongs to one area at most.'
        : 'Can manage the robots and API tokens of this area\'s members, from a browser session. Supervising gives no access to initiatives or apps.' }}
    </BaseText>

    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>

    <FeaturesAdminPagedBlock :list="list" :subject="kind" :forbidden-description="`Your account is not allowed to see the ${kind} of an area.`">
      <FeaturesGroupsMembersTable
        :caption="label + 's'"
        :person-label="label"
        :members="list.items.value"
        :empty-text="`This area has no ${kind} yet.`"
        @remove="askRemove"
      />
    </FeaturesAdminPagedBlock>

    <FeaturesAdminAddPersonDialog
      v-model:open="addOpen"
      :endpoint="base"
      :title="`Add ${noun}`"
      :description="isMember
        ? 'The supervisors of this area will manage this person\'s robots and API tokens. A person belongs to one area at most.'
        : 'The supervisor will manage the robots and API tokens of this area\'s members, from a browser session. It gives no access.'"
      :submit-label="`Add ${noun}`"
      :conflict="isMember ? 'This user already belongs to an area. A person is in one area at most.' : 'This user already supervises this area.'"
      not-found="This area no longer exists. Reload the page."
      @added="onAdded"
      @not-found="emit('notFound')"
    />
    <UiConfirmDialog
      v-model:open="removeOpen"
      :title="`Remove ${removing?.email ?? noun}?`"
      :description="isMember
        ? 'The supervisors of this area stop managing their robots and API tokens. Their own access does not change.'
        : 'They stop managing the robots and API tokens of this area\'s members. Their own access does not change.'"
      confirm-label="Remove"
      :busy="removeBusy"
      :error="removeError"
      @confirm="confirmRemove"
    />
  </BaseStack>
</template>
