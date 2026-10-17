<script setup lang="ts">
import type { SupervisedArea } from '~/types/api'
import type { TableColumn } from '~/types/ui'
import { groupLabel } from '~/utils/group'

/** `/areas`: the areas the caller supervises (from /me), explained, each linking to its people. */
defineProps<{ areas: SupervisedArea[] }>()

const columns: TableColumn[] = [
  { key: 'area', label: 'Area' },
  { key: 'key', label: 'Key' },
  { key: 'actions', label: 'Actions', align: 'end', hideLabel: true },
]
</script>

<template>
  <BaseStack :gap="4">
    <BaseText size="sm" tone="muted">
      As a supervisor you can revoke the API tokens of the people in your areas and hand their robots to someone else,
      for example when a person leaves. Access to apps and accounts stay with the platform administrators: you cannot
      create tokens, change access, or disable anyone.
    </BaseText>
    <BaseTable
      caption="My areas"
      caption-hidden
      :columns="columns"
      :rows="areas"
      :row-key="(row: SupervisedArea) => row.id"
      row-header="area"
      empty-text="You do not supervise any area."
    >
      <template #cell="{ row, column }">
        <BaseText v-if="column.key === 'area'" as="span" size="sm" weight="medium">
          {{ groupLabel(row) }}
        </BaseText>
        <BaseText v-else-if="column.key === 'key'" as="span" size="sm" tone="muted">
          {{ row.key }}
        </BaseText>
        <BaseCluster v-else-if="column.key === 'actions'" justify="end">
          <BaseButton size="sm" variant="secondary" :to="`/areas/${row.id}`" :aria-label="`People of area ${groupLabel(row)}`">
            People
          </BaseButton>
        </BaseCluster>
      </template>
    </BaseTable>
  </BaseStack>
</template>
