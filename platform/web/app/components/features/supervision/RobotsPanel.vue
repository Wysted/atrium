<script setup lang="ts">
import type { Robot, RobotList } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * Robots owned by one person (`GET /users/{id}/robots`). Per robot: its tokens (revoke only, shown below the table
 * for one robot at a time) and "Change owner" (the new owner is picked among the people of the caller's areas).
 * A supervisor cannot disable, rename or create robots, nor clear the owner.
 */
const props = defineProps<{ userId: string, label: string }>()
const emit = defineEmits<{ notFound: [] }>()

const list = usePagedList<RobotList, Robot>(`/users/${encodeURIComponent(props.userId)}/robots`, page => page.robots)
watch(() => list.error.value?.kind, (kind) => {
  if (kind === 'not_found') emit('notFound')
})
const tokensOf = ref<Robot | null>(null)
const changing = ref<Robot | null>(null)
const ownerOpen = computed({
  get: () => changing.value !== null,
  set: (value: boolean) => {
    if (!value) changing.value = null
  },
})
const notice = ref('')

const columns: TableColumn[] = [
  { key: 'name', label: 'Name' },
  { key: 'status', label: 'Status' },
  { key: 'last_used_at', label: 'Last used' },
  { key: 'actions', label: 'Actions', align: 'end', hideLabel: true },
]

function onReassigned(robot: Robot) {
  notice.value = `Robot ${robot.name} now belongs to ${robotOwnerLabel(robot) ?? 'the new owner'}.`
  if (tokensOf.value?.id === robot.id) tokensOf.value = null
  void list.reload(true)
}

function onGone() {
  notice.value = 'This robot is no longer in your scope.'
  tokensOf.value = null
  void list.reload(true)
}
</script>

<template>
  <BaseStack :gap="4">
    <BaseHeading :level="2" size="lg">
      Robots of {{ label }}
    </BaseHeading>
    <BaseText size="sm" tone="muted">
      A robot authenticates only with tokens: revoke its tokens to stop it, or hand it to someone who takes it over.
    </BaseText>
    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>
    <FeaturesAdminPagedBlock :list="list" subject="robots" forbidden-description="Your account is not allowed to see these robots.">
      <BaseTable
        caption="Robots"
        caption-hidden
        :columns="columns"
        :rows="list.items.value"
        :row-key="(row: Robot) => row.id"
        row-header="name"
        empty-text="This person owns no robots."
      >
        <template #cell="{ row, column }">
          <BaseText v-if="column.key === 'name'" as="span" size="sm" weight="medium">
            {{ row.name }}
          </BaseText>
          <FeaturesUsersStatusBadge v-else-if="column.key === 'status'" :status="row.status" />
          <BaseText v-else-if="column.key === 'last_used_at'" as="span" size="sm" :tone="row.last_used_at ? 'default' : 'muted'">
            {{ row.last_used_at ? formatTimestamp(row.last_used_at) : 'Never' }}
          </BaseText>
          <BaseCluster v-else-if="column.key === 'actions'" justify="end">
            <BaseButton size="sm" variant="secondary" :aria-label="`Tokens of robot ${row.name}`" @click="tokensOf = row">
              Tokens
            </BaseButton>
            <BaseButton size="sm" variant="secondary" :aria-label="`Change owner of robot ${row.name}`" @click="notice = ''; changing = row">
              Change owner
            </BaseButton>
          </BaseCluster>
        </template>
      </BaseTable>
    </FeaturesAdminPagedBlock>

    <FeaturesTokensPanel
      v-if="tokensOf"
      :key="tokensOf.id"
      :path="`/robots/${encodeURIComponent(tokensOf.id)}/tokens`"
      revoke-base="/tokens"
      :owner="`the robot ${tokensOf.name}`"
      :offer-admin="false"
      :can-create="false"
      :heading="`Tokens of robot ${tokensOf.name}`"
      :heading-level="3"
    />

    <FeaturesSupervisionOwnerDialog v-model:open="ownerOpen" :robot="changing" @saved="onReassigned" @gone="onGone" />
  </BaseStack>
</template>
