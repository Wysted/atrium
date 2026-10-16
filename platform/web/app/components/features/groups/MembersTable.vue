<script setup lang="ts">
import type { GroupMember } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/** Members of a group: name and email, account status (disabled users are listed too), added date, "Remove". */
defineProps<{
  members: GroupMember[]
  caption: string
}>()

const emit = defineEmits<{ remove: [member: GroupMember] }>()

const columns: TableColumn[] = [
  { key: 'member', label: 'Member' },
  { key: 'status', label: 'Status' },
  { key: 'added_at', label: 'Added' },
  { key: 'actions', label: 'Actions', align: 'end', hideLabel: true },
]
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="members"
    :row-key="(row: GroupMember) => row.user_id"
    row-header="member"
    empty-text="This group has no members yet."
  >
    <template #cell="{ row, column }">
      <BaseStack v-if="column.key === 'member'" :gap="0">
        <BaseText as="span" size="sm" weight="medium">
          {{ row.name || row.email }}
        </BaseText>
        <BaseText v-if="row.name" as="span" size="xs" tone="muted">
          {{ row.email }}
        </BaseText>
      </BaseStack>
      <FeaturesUsersStatusBadge v-else-if="column.key === 'status'" :status="row.status" />
      <BaseText v-else-if="column.key === 'added_at'" as="span" size="sm">
        {{ formatTimestamp(row.added_at) }}
      </BaseText>
      <BaseCluster v-else-if="column.key === 'actions'" justify="end">
        <BaseButton size="sm" variant="danger" :aria-label="`Remove ${row.email}`" @click="emit('remove', row)">
          Remove
        </BaseButton>
      </BaseCluster>
    </template>
  </BaseTable>
</template>
