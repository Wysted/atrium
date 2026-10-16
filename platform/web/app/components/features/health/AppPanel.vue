<script setup lang="ts">
import type { AppHealth } from '~/types/api'

/**
 * Integration health of one app (`GET /apps/{id}/health`; platform admins and managers of its initiative, the API
 * answers 404 to anyone else). Shows what the API returns: app and initiative state, grant counts, the findings
 * (app-level first) and, per host, last seen and last probe. Thresholds and verdicts are the server's. A 404, or
 * an app of another initiative than the route's, shows "App not found". Fixed copy only.
 */
const props = defineProps<{
  appId: string
  initiativeId: string
}>()

const state = useApiData<AppHealth>(`app-health-${props.appId}`, `/apps/${encodeURIComponent(props.appId)}/health`)
const health = state.data
const refreshing = ref(false)
const refreshedNotice = ref('')

const notFound = computed(() =>
  state.error.value?.kind === 'not_found' || (!!health.value && health.value.app.initiative_id !== props.initiativeId))
const appProblems = computed(() => health.value?.problems.filter(p => p.host === null) ?? [])
const hostProblemCount = computed(() => health.value?.problems.filter(p => p.host !== null).length ?? 0)

async function refresh() {
  refreshing.value = true
  try {
    refreshedNotice.value = ''
    await state.refreshQuiet()
    if (!state.error.value) refreshedNotice.value = 'Refreshed'
  }
  finally {
    refreshing.value = false
  }
}

const appsPath = computed(() => `/admin/initiatives/${encodeURIComponent(props.initiativeId)}/apps`)
</script>

<template>
  <BaseStack :gap="6">
    <BaseLink :to="appsPath">
      Back to apps
    </BaseLink>

    <BaseBox v-if="!health && !state.error.value" as="div" aria-busy="true">
      <BaseText class="sr-only">
        Loading app health
      </BaseText>
      <BaseStack :gap="3">
        <BaseSkeleton height="lg" width="half" />
        <BaseSkeleton />
        <BaseSkeleton />
      </BaseStack>
    </BaseBox>

    <BaseEmptyState
      v-else-if="notFound"
      title="App not found"
      description="It does not exist, or you do not manage its initiative."
    >
      <BaseButton :to="appsPath" variant="secondary">
        Back to apps
      </BaseButton>
    </BaseEmptyState>

    <FeaturesHomeLoadError
      v-else-if="state.error.value && !health"
      :error="state.error.value"
      :retrying="state.pending.value"
      subject="the app health"
      forbidden-description="Your account is not allowed to see the health of this app."
      @retry="state.refresh()"
    />

    <template v-else-if="health">
      <BaseCluster justify="between" :gap="4">
        <BaseStack :gap="1">
          <BaseHeading :level="2">
            Health of {{ appLabel(health.app) }}
          </BaseHeading>
          <BaseText tone="muted" size="sm">
            What the portal knows about how this app is integrated: traffic it has received and whether its hosts answer behind ForwardAuth.
          </BaseText>
        </BaseStack>
        <BaseButton variant="secondary" :loading="refreshing" @click="refresh">
          Refresh
        </BaseButton>
      </BaseCluster>

      <BaseText as="span" class="sr-only" role="status" aria-live="polite">
        {{ refreshedNotice }}
      </BaseText>

      <BaseText v-if="state.error.value" role="alert" size="sm" tone="danger">
        Could not refresh. Showing the last result.
      </BaseText>

      <BaseCard as="section" aria-labelledby="health-summary">
        <BaseStack :gap="3">
          <BaseHeading id="health-summary" :level="3" size="lg">
            Summary
          </BaseHeading>
          <BaseCluster :gap="4">
            <BaseCluster :gap="2">
              <BaseText as="span" size="sm" tone="muted">
                App
              </BaseText>
              <FeaturesAppsStatusBadge :status="health.app.status" />
            </BaseCluster>
            <BaseCluster :gap="2">
              <BaseText as="span" size="sm" tone="muted">
                Initiative
              </BaseText>
              <FeaturesInitiativesStatusBadge :status="health.initiative_status" />
            </BaseCluster>
            <BaseText as="span" size="sm">
              <BaseText as="span" size="sm" weight="semibold">
                {{ health.grant_count }}
              </BaseText>
              {{ health.grant_count === 1 ? 'active grant' : 'active grants' }}
            </BaseText>
            <BaseText as="span" size="sm">
              <BaseText as="span" size="sm" weight="semibold">
                {{ health.effective_manager_grants }}
              </BaseText>
              {{ health.effective_manager_grants === 1 ? 'effective manager grant' : 'effective manager grants' }}
            </BaseText>
          </BaseCluster>
          <BaseText size="xs" tone="muted">
            Checked {{ formatRelative(health.checked_at) }} ({{ formatTimestamp(health.checked_at) }}).
          </BaseText>
        </BaseStack>
      </BaseCard>

      <BaseCard as="section" aria-labelledby="health-findings">
        <BaseStack :gap="3">
          <BaseHeading id="health-findings" :level="3" size="lg">
            Findings
          </BaseHeading>
          <BaseText v-if="health.problems.length === 0" size="sm" tone="success">
            No findings: the portal reports nothing to fix for this app.
          </BaseText>
          <template v-else>
            <FeaturesHealthProblemList v-if="appProblems.length" :problems="appProblems" />
            <BaseText v-if="hostProblemCount" size="sm" tone="muted">
              {{ hostProblemCount === 1 ? '1 finding' : `${hostProblemCount} findings` }} about hosts, listed below next to each host.
            </BaseText>
          </template>
        </BaseStack>
      </BaseCard>

      <BaseStack as="section" aria-labelledby="health-hosts" :gap="3">
        <BaseHeading id="health-hosts" :level="3" size="lg">
          Hosts
        </BaseHeading>
        <FeaturesHealthHostsTable caption="Hosts" :deployments="health.app.deployments" :problems="health.problems" />
        <BaseText size="xs" tone="muted">
          "Last seen" is the last request ForwardAuth authorized for the host. "Last probe" is the portal checking that the host redirects to sign in without a session.
        </BaseText>
      </BaseStack>
    </template>
  </BaseStack>
</template>
