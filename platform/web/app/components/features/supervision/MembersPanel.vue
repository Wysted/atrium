<script setup lang="ts">
import type { AreaPersonList, GroupMember } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * `/areas/[id]`: the people of one supervised area the caller can act on (the API lists supervisable members only),
 * each linking to their tokens and robots. Keyset list with "Load more".
 */
const props = defineProps<{ areaId: string }>()

const list = usePagedList<AreaPersonList, GroupMember>(`/areas/${encodeURIComponent(props.areaId)}/members`, page => page.members ?? [])

// Person labels for the person page, kept in memory so no email travels in the URL.
const people = useState<Record<string, string>>('supervision-people', () => ({}))
watch(list.items, (items) => {
  for (const m of items) people.value[m.user_id] = m.email
})

const columns: TableColumn[] = [
  { key: 'member', label: 'Person' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: 'Actions', align: 'end', hideLabel: true },
]

const personPath = (m: GroupMember) =>
  `/areas/${encodeURIComponent(props.areaId)}/people/${encodeURIComponent(m.user_id)}`
</script>

<template>
  <BaseStack :gap="4">
    <BaseText size="sm" tone="muted">
      People in this area you can manage: their tokens and robots.
    </BaseText>
    <FeaturesAdminPagedBlock :list="list" subject="people" forbidden-description="Your account is not allowed to see the people of this area.">
      <BaseTable
        caption="People"
        caption-hidden
        :columns="columns"
        :rows="list.items.value"
        :row-key="(row: GroupMember) => row.user_id"
        row-header="member"
        empty-text="No one in this area needs your attention."
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
          <BaseCluster v-else-if="column.key === 'actions'" justify="end">
            <BaseButton size="sm" variant="secondary" :to="personPath(row)" :aria-label="`Tokens and robots of ${row.email}`">
              Tokens and robots
            </BaseButton>
          </BaseCluster>
        </template>
      </BaseTable>
    </FeaturesAdminPagedBlock>
  </BaseStack>
</template>
