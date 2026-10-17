<script setup lang="ts">
import type { Area } from '~/types/api'

/**
 * One area (platform admins only; the API enforces): heading and key, then its members and its supervisors.
 * `GET /areas/{id}` supplies the heading (a failure only costs the name); a 404 there or on a list = "Area not found".
 */
const props = defineProps<{ areaId: string }>()

const area = useApiData<Area>(`area-${props.areaId}`, `/areas/${encodeURIComponent(props.areaId)}`)
const listGone = ref(false)
const notFound = computed(() => listGone.value || area.error.value?.kind === 'not_found')
const heading = computed(() => (area.data.value ? groupLabel(area.data.value) : 'Area'))
</script>

<template>
  <BaseStack :gap="6">
    <BasePageHeader :title="heading" description="Supervisors manage the robots and API tokens of the area's members, from a browser session. They never manage access.">
      <BaseButton variant="secondary" to="/admin/areas">
        All areas
      </BaseButton>
    </BasePageHeader>

    <BaseText v-if="area.data.value && !notFound" size="sm" tone="muted">
      Key: <code class="font-mono">{{ area.data.value.key }}</code>
    </BaseText>

    <BaseEmptyState v-if="notFound" :level="2" title="Area not found" description="It may have been deleted.">
      <BaseButton to="/admin/areas">
        Back to areas
      </BaseButton>
    </BaseEmptyState>
    <template v-else>
      <FeaturesAreasPeoplePanel :area-id="areaId" kind="members" @not-found="listGone = true" />
      <FeaturesAreasPeoplePanel :area-id="areaId" kind="supervisors" @not-found="listGone = true" />
    </template>
  </BaseStack>
</template>
