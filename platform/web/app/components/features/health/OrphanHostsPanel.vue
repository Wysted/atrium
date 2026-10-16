<script setup lang="ts">
import type { OrphanHost, OrphanHostList } from '~/types/api'

/**
 * Orphan hosts (platform admins only; the page does not mount it for anyone else and the API enforces): hosts
 * refused as `unknown_host` in the last 7 days that have no deployment now, most recent first, at most 100, no
 * paging. Explains what an orphan host is; there is no shortcut to add one to an app (it needs choosing the app).
 */
const state = useApiData<OrphanHostList>('orphan-hosts', '/health/orphan-hosts')
const hosts = computed<OrphanHost[]>(() => state.data.value?.hosts ?? [])
const refreshing = ref(false)

async function refresh() {
  refreshing.value = true
  try {
    await state.refreshQuiet()
  }
  finally {
    refreshing.value = false
  }
}
</script>

<template>
  <BaseStack :gap="6">
    <BasePageHeader
      title="Health"
      description="Hosts that reached the portal but belong to no app."
    >
      <BaseButton variant="secondary" :loading="refreshing" @click="refresh">
        Refresh
      </BaseButton>
    </BasePageHeader>

    <BaseCard as="section" aria-labelledby="orphan-explain">
      <BaseStack :gap="2">
        <BaseHeading id="orphan-explain" :level="2" size="lg">
          Orphan hosts
        </BaseHeading>
        <BaseText size="sm">
          An orphan host is a host whose traffic reached the sign-in check but that no app declares. Typically an app is missing its host (add it to the app in its initiative), or a stale DNS record or route still points the host at the portal.
        </BaseText>
        <BaseText size="sm" tone="muted">
          Lists the last 7 days, most recent first, up to 100 hosts. A host added to an app disappears from the list. The count is the number of recorded denials, at most one every 10 minutes per replica: it shows for how long the host has been hit, not how many requests it received.
        </BaseText>
      </BaseStack>
    </BaseCard>

    <BaseBox as="div" :aria-busy="state.pending.value && !state.data.value">
      <BaseStack v-if="!state.data.value && !state.error.value" :gap="3">
        <BaseText class="sr-only">
          Loading orphan hosts
        </BaseText>
        <BaseSkeleton />
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
      <FeaturesHomeLoadError
        v-else-if="state.error.value && !state.data.value"
        :error="state.error.value"
        :retrying="state.pending.value"
        subject="orphan hosts"
        forbidden-description="Only platform administrators can see orphan hosts."
        @retry="state.refresh()"
      />
      <BaseStack v-else :gap="3">
        <BaseText v-if="state.error.value" role="alert" size="sm" tone="danger">
          Could not refresh. Showing the last result.
        </BaseText>
        <FeaturesHealthOrphanHostsTable caption="Orphan hosts" :hosts="hosts" />
      </BaseStack>
    </BaseBox>
  </BaseStack>
</template>
