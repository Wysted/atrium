<script setup lang="ts">
import type { ApiError } from '~/composables/useApi'

/**
 * Loading / error / "Load more" frame of a keyset list (`usePagedList`). `subject` ("groups", "members") builds the
 * fixed copy; the default slot is the table shown once loaded.
 */
defineProps<{
  list: {
    items: Readonly<Ref<readonly unknown[]>>
    error: Readonly<Ref<ApiError | null>>
    loading: Readonly<Ref<boolean>>
    loadingMore: Readonly<Ref<boolean>>
    hasMore: Readonly<Ref<boolean>>
    reload: (quiet?: boolean) => Promise<unknown>
    loadMore: () => Promise<unknown>
  }
  subject: string
  forbiddenDescription: string
}>()
</script>

<template>
  <BaseBox as="div" :aria-busy="list.loading.value">
    <BaseStack v-if="list.loading.value" :gap="3">
      <BaseText class="sr-only">
        Loading {{ subject }}
      </BaseText>
      <BaseSkeleton />
      <BaseSkeleton />
      <BaseSkeleton />
    </BaseStack>
    <FeaturesHomeLoadError
      v-else-if="list.error.value && list.items.value.length === 0"
      :error="list.error.value"
      :subject="subject"
      :forbidden-description="forbiddenDescription"
      @retry="list.reload()"
    />
    <BaseStack v-else :gap="4">
      <slot />
      <BaseCluster v-if="list.error.value" :gap="2">
        <BaseText role="alert" size="sm" tone="danger">
          Could not load more {{ subject }}.
        </BaseText>
        <BaseButton size="sm" variant="secondary" :loading="list.loadingMore.value" @click="list.loadMore()">
          Try again
        </BaseButton>
      </BaseCluster>
      <BaseCluster v-else-if="list.hasMore.value" justify="center">
        <BaseButton variant="secondary" :loading="list.loadingMore.value" @click="list.loadMore()">
          Load more
        </BaseButton>
      </BaseCluster>
    </BaseStack>
  </BaseBox>
</template>
