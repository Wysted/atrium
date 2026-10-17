<script setup lang="ts">
import type { SupervisedArea } from '~/types/api'

/**
 * Gate of the supervisor screens (default layout, not the admin one: a supervisor is often no admin). Resolves /me:
 * skeleton, load error, "no areas" for a caller who supervises none, and "Area not found" when `areaId` is not one of
 * theirs. The slot gets the matching `area` (null without `areaId`). The UI only hides; the API answers 404 outside scope.
 */
const props = defineProps<{ areaId?: string }>()

const access = useAdminAccess()
onMounted(() => {
  void access.revalidate()
})
const loading = computed(() => !access.me.value && !access.error.value)
const area = computed<SupervisedArea | null>(() =>
  props.areaId ? access.supervisedAreas.value.find(a => a.id === props.areaId) ?? null : null)
</script>

<template>
  <BaseBox v-if="loading" as="div" aria-busy="true">
    <BaseText class="sr-only">
      Loading
    </BaseText>
    <BaseStack :gap="3">
      <BaseSkeleton height="lg" width="half" />
      <BaseSkeleton />
    </BaseStack>
  </BaseBox>
  <FeaturesHomeLoadError
    v-else-if="access.error.value"
    :error="access.error.value"
    :retrying="access.pending.value"
    subject="your areas"
    forbidden-description="Your account is not allowed to use the portal."
    @retry="access.refresh()"
  />
  <BaseEmptyState
    v-else-if="!access.isSupervisor.value"
    title="You do not supervise any area"
    description="Area supervisors are appointed by the platform administrators."
  >
    <BaseButton to="/">
      Back to home
    </BaseButton>
  </BaseEmptyState>
  <BaseEmptyState
    v-else-if="areaId && !area"
    title="Area not found"
    description="You do not supervise this area."
  >
    <BaseButton to="/areas">
      Back to my areas
    </BaseButton>
  </BaseEmptyState>
  <slot v-else :area="area" />
</template>
