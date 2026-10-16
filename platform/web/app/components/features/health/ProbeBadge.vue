<script setup lang="ts">
import type { ReceivedProbeStatus } from '~/types/api'

/** Result of the last probe of a host, as the API stores it (`null`: not probed yet). The text carries the meaning. */
const props = defineProps<{ status: ReceivedProbeStatus | null }>()

const copy = computed(() => probeStatusCopy(props.status))
</script>

<template>
  <BaseBadge :tone="copy.tone">
    {{ copy.label }}
  </BaseBadge>
</template>
