<script setup lang="ts">
import type { AuditEvent } from '~/types/api'
import type { TableColumn } from '~/types/ui'

/**
 * Audit events, newest first: time (locale), actor email, action, target and initiative (label when the initiative
 * is among `initiativeLabels`, else the id), plus a collapsed before/after. Everything is rendered as text.
 */
defineProps<{
  events: AuditEvent[]
  caption: string
  /** Initiative id to display label, for the initiatives the caller could load. */
  initiativeLabels: Readonly<Record<string, string>>
}>()

const columns: TableColumn[] = [
  { key: 'occurred_at', label: 'When' },
  { key: 'actor', label: 'Actor' },
  { key: 'action', label: 'Action' },
  { key: 'target', label: 'Target' },
  { key: 'initiative', label: 'Initiative' },
  { key: 'changes', label: 'Changes' },
]
</script>

<template>
  <BaseTable
    :caption="caption"
    caption-hidden
    :columns="columns"
    :rows="events"
    :row-key="(row: AuditEvent) => row.id"
    row-header="occurred_at"
    empty-text="No audit events."
  >
    <template #cell="{ row, column }">
      <BaseText v-if="column.key === 'occurred_at'" as="span" size="sm">
        {{ formatTimestamp(row.occurred_at) }}
      </BaseText>
      <BaseText v-else-if="column.key === 'actor'" as="span" size="sm" :tone="row.actor_email ? 'default' : 'muted'">
        {{ row.actor_email || 'Unknown' }}
      </BaseText>
      <BaseText v-else-if="column.key === 'action'" as="span" size="sm">
        <code class="font-mono">{{ row.action }}</code>
      </BaseText>
      <BaseStack v-else-if="column.key === 'target'" :gap="0">
        <BaseText as="span" size="sm">
          {{ row.target_type }}
        </BaseText>
        <BaseText as="span" size="xs" tone="muted">
          <code class="font-mono break-all">{{ row.target_id }}</code>
        </BaseText>
      </BaseStack>
      <template v-else-if="column.key === 'initiative'">
        <BaseText v-if="!row.initiative_id" as="span" size="sm" tone="muted">
          None
        </BaseText>
        <BaseText v-else as="span" size="sm">
          {{ initiativeLabels[row.initiative_id] ?? row.initiative_id }}
        </BaseText>
      </template>
      <FeaturesAuditChanges v-else-if="column.key === 'changes'" :before="row.before" :after="row.after" :action="row.action" :occurred-at="row.occurred_at" />
    </template>
  </BaseTable>
</template>
