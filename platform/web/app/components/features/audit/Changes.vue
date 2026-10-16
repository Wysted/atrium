<script setup lang="ts">
/**
 * `before` / `after` of an audit event, collapsed by default (native `<details>`). The values are arbitrary JSON
 * from the server: they are pretty-printed to text and rendered through text interpolation only, never as HTML.
 */
const props = defineProps<{
  before: unknown
  after: unknown
  /** Audit action and time, appended to the summary as visually hidden text. */
  action?: string
  occurredAt?: string
}>()

/** Rendered JSON cap per side; the rest is cut and flagged. */
const MAX_JSON_CHARS = 64 * 1024

interface Rendered {
  text: string
  truncated: boolean
}

function pretty(value: unknown): Rendered {
  if (value === null || value === undefined) return { text: '', truncated: false }
  let text = ''
  try {
    text = JSON.stringify(value, null, 2) ?? ''
  }
  catch {
    return { text: '', truncated: false }
  }
  return text.length > MAX_JSON_CHARS
    ? { text: text.slice(0, MAX_JSON_CHARS), truncated: true }
    : { text, truncated: false }
}

const open = ref(false)
const hasChanges = computed(() => (props.before ?? null) !== null || (props.after ?? null) !== null)
// Computed only while the details are open: large payloads cost nothing while collapsed.
const before = computed(() => (open.value ? pretty(props.before) : null))
const after = computed(() => (open.value ? pretty(props.after) : null))

function onToggle(event: Event) {
  open.value = (event.target as HTMLDetailsElement).open
}
</script>

<template>
  <BaseText v-if="!hasChanges" as="span" size="sm" tone="muted">
    None
  </BaseText>
  <details v-else class="text-sm" @toggle="onToggle">
    <summary class="cursor-pointer rounded-sm text-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus">
      Show changes<span v-if="action" class="sr-only"> for {{ action }}<template v-if="occurredAt"> at {{ formatTimestamp(occurredAt) }}</template></span>
    </summary>
    <BaseStack v-if="open && before && after" :gap="2">
      <template v-if="before.text">
        <BaseText as="span" size="xs" weight="semibold" tone="muted">
          Before
        </BaseText>
        <pre class="max-w-prose overflow-x-auto rounded-md border border-border bg-surface p-2 font-mono text-xs whitespace-pre-wrap break-all">{{ before.text }}</pre>
        <BaseText v-if="before.truncated" as="span" size="xs" tone="muted">
          Truncated
        </BaseText>
      </template>
      <template v-if="after.text">
        <BaseText as="span" size="xs" weight="semibold" tone="muted">
          After
        </BaseText>
        <pre class="max-w-prose overflow-x-auto rounded-md border border-border bg-surface p-2 font-mono text-xs whitespace-pre-wrap break-all">{{ after.text }}</pre>
        <BaseText v-if="after.truncated" as="span" size="xs" tone="muted">
          Truncated
        </BaseText>
      </template>
    </BaseStack>
  </details>
</template>
