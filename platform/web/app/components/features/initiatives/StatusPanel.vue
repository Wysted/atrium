<script setup lang="ts">
import type { Initiative, PatchInitiativeBody } from '~/types/api'

/**
 * Status of an initiative plus, for platform admins (the API enforces), a single button that deactivates or
 * activates it behind a confirmation. The `platform` initiative cannot be deactivated: the button is hidden for
 * it, and a 409 from the API is still handled with a fixed message. The button is one element whose label
 * changes, so keyboard focus survives the status change.
 */
const props = defineProps<{
  initiative: Initiative
  canChange: boolean
}>()

const emit = defineEmits<{ changed: [initiative: Initiative] }>()

const api = useApi()
const confirmOpen = ref(false)
const busy = ref(false)
const error = ref('')

const isPlatform = computed(() => props.initiative.key === PLATFORM_INITIATIVE_KEY)
const target = computed(() => (props.initiative.status === 'active' ? 'inactive' : 'active'))
const deactivating = computed(() => target.value === 'inactive')
const label = computed(() => initiativeLabel(props.initiative))

watch(confirmOpen, (isOpen) => {
  if (isOpen) error.value = ''
})

async function confirm() {
  if (busy.value) return
  const body: PatchInitiativeBody = { status: target.value }
  busy.value = true
  error.value = ''
  try {
    const updated = await api.patch<Initiative>(`/initiatives/${encodeURIComponent(props.initiative.id)}`, body)
    confirmOpen.value = false
    emit('changed', updated)
  }
  catch (e) {
    error.value = describeMutationError(e, {
      conflict: deactivating.value
        ? 'This initiative cannot be deactivated.'
        : 'The initiative changed in the meantime. Reload the page and try again.',
    }).message
  }
  finally {
    busy.value = false
  }
}
</script>

<template>
  <BaseStack :gap="3">
    <BaseCluster>
      <BaseText as="span" weight="medium">
        Current status
      </BaseText>
      <FeaturesInitiativesStatusBadge :status="initiative.status" />
    </BaseCluster>
    <BaseText v-if="initiative.status === 'inactive'" size="sm" tone="muted">
      Nobody can open the apps of an inactive initiative.
    </BaseText>
    <template v-if="canChange">
      <BaseText v-if="isPlatform && deactivating" size="sm" tone="muted">
        The platform initiative cannot be deactivated: it holds the portal itself.
      </BaseText>
      <BaseCluster v-else>
        <BaseButton :variant="deactivating ? 'danger' : 'primary'" @click="confirmOpen = true">
          {{ deactivating ? 'Deactivate initiative' : 'Activate initiative' }}
        </BaseButton>
      </BaseCluster>
    </template>

    <UiConfirmDialog
      v-model:open="confirmOpen"
      :title="deactivating ? `Deactivate ${label}?` : `Activate ${label}?`"
      :description="deactivating
        ? 'Everyone loses access to the apps of this initiative until it is activated again. Roles and grants are kept.'
        : 'The apps of this initiative become available again to the people who have access.'"
      :confirm-label="deactivating ? 'Deactivate' : 'Activate'"
      :variant="deactivating ? 'danger' : 'primary'"
      :busy="busy"
      :error="error"
      @confirm="confirm"
    />
  </BaseStack>
</template>
