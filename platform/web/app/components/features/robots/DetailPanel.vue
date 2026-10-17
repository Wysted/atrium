<script setup lang="ts">
import type { Robot } from '~/types/api'
import { ApiError } from '~/composables/useApi'

/**
 * One robot and its tokens (platform admins). `GET /robots/{id}`: 404 (unknown, malformed or not a robot) = "Robot not found", 403 = load error. A disabled robot cannot get new tokens (409), so the
 * button is hidden for it. The admin scope is always offered: the API refuses it (422 `scope.not_allowed`) unless
 * the robot holds a role that manages access.
 */
const props = defineProps<{ robotId: string }>()

const robot = shallowRef<Robot | null>(null)
const loading = ref(true)
const error = shallowRef<ApiError | null>(null)
const notFound = ref(false)

async function load() {
  loading.value = true
  error.value = null
  notFound.value = false
  try {
    robot.value = await apiGet<Robot>(`/robots/${encodeURIComponent(props.robotId)}`)
  }
  catch (e) {
    if (e instanceof ApiError && e.kind === 'not_found') notFound.value = true
    else error.value = e instanceof ApiError ? e : new ApiError(0, null)
  }
  finally {
    loading.value = false
  }
}
onMounted(load)
</script>

<template>
  <BaseStack :gap="6">
    <BaseBox v-if="loading" as="div" aria-busy="true">
      <BaseText class="sr-only">
        Loading robot
      </BaseText>
      <BaseStack :gap="3">
        <BaseSkeleton height="lg" width="half" />
        <BaseSkeleton />
      </BaseStack>
    </BaseBox>
    <FeaturesHomeLoadError
      v-else-if="error"
      :error="error"
      subject="the robot"
      forbidden-description="Your account is not allowed to manage robots."
      @retry="load"
    />
    <BaseEmptyState v-else-if="notFound || !robot" title="Robot not found" description="This robot does not exist.">
      <BaseButton to="/admin/robots">
        Back to robots
      </BaseButton>
    </BaseEmptyState>
    <template v-else>
      <BasePageHeader :title="robot.name" description="Tokens let this robot call the portal API. Only platform admins create or revoke them.">
        <BaseButton to="/admin/robots" variant="secondary">
          Back to robots
        </BaseButton>
      </BasePageHeader>
      <BaseCluster :gap="2">
        <FeaturesUsersStatusBadge :status="robot.status" />
        <BaseText v-if="robot.status === 'disabled'" size="sm" tone="muted">
          A disabled robot has no live tokens and cannot get new ones. Enable it from the robots list.
        </BaseText>
      </BaseCluster>
      <FeaturesTokensPanel
        :path="`/robots/${encodeURIComponent(robot.id)}/tokens`"
        revoke-base="/tokens"
        :owner="`the robot ${robot.name}`"
        :can-create="robot.status === 'active'"
        offer-admin
        :expiry-options="ROBOT_EXPIRY_OPTIONS"
      />
    </template>
  </BaseStack>
</template>
