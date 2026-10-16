<script setup lang="ts">
import type { ApiError } from '~/composables/useApi'

/** Error state of a failed API load, with copy per error kind. Never shows server-provided detail. */
const props = withDefaults(defineProps<{
  error: ApiError
  /** A retry is in flight: the "Try again" button shows its loading state. */
  retrying?: boolean
  /** What failed to load, for the title ("Could not load {subject}"). */
  subject?: string
  /** Description of a 403 (default: the account is not allowed to use the portal). */
  forbiddenDescription?: string
}>(), {
  retrying: false,
  subject: 'your apps',
  forbiddenDescription: 'Your account is not allowed to use the portal.',
})

defineEmits<{ retry: [] }>()

const copy = computed(() => {
  switch (props.error.kind) {
    case 'unauthorized':
      return props.error.reloading
        ? { title: 'Signing you in again', description: 'Your session ended. Redirecting to sign in.', retry: false }
        : { title: 'Your session has ended', description: 'Reload the page to sign in again.', retry: true }
    case 'forbidden':
      return { title: 'No access', description: props.forbiddenDescription, retry: false }
    case 'network':
    case 'unavailable':
    case 'server':
      return { title: `Could not load ${props.subject}`, description: 'The portal is not reachable right now. Try again in a moment.', retry: true }
    default:
      return { title: 'Something went wrong', description: 'The request could not be completed. Try again.', retry: true }
  }
})
const reload = () => window.location.reload()
</script>

<template>
  <BaseEmptyState role="alert" icon="alert-triangle" :title="copy.title" :description="copy.description">
    <BaseButton v-if="copy.retry && error.kind === 'unauthorized'" variant="primary" @click="reload">
      Reload page
    </BaseButton>
    <BaseButton v-else-if="copy.retry" variant="secondary" :loading="retrying" @click="$emit('retry')">
      Try again
    </BaseButton>
  </BaseEmptyState>
</template>
