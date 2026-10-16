<script setup lang="ts" generic="T extends object">
import type { TableColumn } from '~/types/ui'

/**
 * Semantic data table. Columns are data (`columns`), rows are data (`rows`); a cell renders `row[column.key]`
 * unless the `cell` slot (`{ row, column }`) is used. `caption` is required (it names the table); with
 * `captionHidden` it is visually hidden but still read. `rowHeader` is the key of the column rendered as
 * `<th scope="row">`. No rows renders one row with `emptyText`. The table sits in a horizontally scrollable
 * region (focusable, labelled by the caption) so a wide table scrolls inside itself, never the page.
 */
const props = withDefaults(defineProps<{
  caption: string
  captionHidden?: boolean
  columns: TableColumn[]
  rows: T[]
  rowKey: (row: T) => string
  rowHeader?: string
  emptyText?: string
}>(), {
  rowHeader: undefined,
  emptyText: 'Nothing to show.',
})

defineSlots<{
  cell?: (props: { row: T, column: TableColumn }) => unknown
}>()

const uid = useId()
const captionId = `table-${uid}`
function value(row: T, column: TableColumn): string {
  const v = (row as Record<string, unknown>)[column.key]
  return v === null || v === undefined ? '' : String(v)
}
const aligns = { start: 'text-left', end: 'text-right' } as const
const colspan = computed(() => Math.max(props.columns.length, 1))
</script>

<template>
  <div
    role="region"
    :aria-labelledby="captionId"
    tabindex="0"
    class="overflow-x-auto rounded-md border border-border focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
  >
    <table class="w-full border-collapse text-sm text-text">
      <caption :id="captionId" :class="captionHidden ? 'sr-only' : 'px-4 py-2 text-left font-semibold'">
        {{ caption }}
      </caption>
      <thead class="bg-surface-raised">
        <tr>
          <th
            v-for="column in columns"
            :key="column.key"
            scope="col"
            :class="['border-b border-border px-4 py-2 font-semibold text-text-muted', aligns[column.align ?? 'start']]"
          >
            <span :class="column.hideLabel ? 'sr-only' : undefined">{{ column.label }}</span>
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="rows.length === 0">
          <td :colspan="colspan" class="px-4 py-6 text-center text-text-muted">
            {{ emptyText }}
          </td>
        </tr>
        <tr v-for="row in rows" :key="rowKey(row)" class="border-b border-border last:border-b-0">
          <template v-for="column in columns" :key="column.key">
            <component
              :is="column.key === rowHeader ? 'th' : 'td'"
              :scope="column.key === rowHeader ? 'row' : undefined"
              :class="['px-4 py-2', aligns[column.align ?? 'start'], column.key === rowHeader ? 'font-medium' : 'font-normal']"
            >
              <slot name="cell" :row="row" :column="column">
                {{ value(row, column) }}
              </slot>
            </component>
          </template>
        </tr>
      </tbody>
    </table>
  </div>
</template>
