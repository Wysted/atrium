<script setup lang="ts">
import type { Area, AreaList } from '~/types/api'

/**
 * Areas screen (platform admins only; the page does not mount it for anyone else and the API enforces).
 * Keyset list with "Load more", create, and delete behind a confirmation. 409 on delete = the area still has
 * members or supervisors. Fixed copy only.
 */
const api = useApi()
const list = usePagedList<AreaList, Area>('/areas', page => page.areas)

const createOpen = ref(false)
const { removing, open: removeOpen, busy: removeBusy, error: removeError, notice, ask: askRemove, confirm: confirmRemove } = useRemoveFlow<Area>({
  remove: area => api.del(`/areas/${encodeURIComponent(area.id)}`),
  onRemoved: (area) => {
    list.items.value = list.items.value.filter(a => a.id !== area.id)
  },
  reload: () => void list.reload(true),
  done: 'Area deleted.',
  gone: 'This area no longer exists.',
  conflict: 'This area still has members or supervisors. Remove them first, then delete the area.',
})

function onCreated() {
  notice.value = 'Area created.'
  void list.reload(true)
}
</script>

<template>
  <BaseStack :gap="4">
    <BasePageHeader title="Areas" description="An area is a set of people. Its supervisors manage the robots and API tokens of those people from a browser session. They never manage access.">
      <BaseButton @click="createOpen = true">
        <BaseIcon name="plus" size="sm" />
        New area
      </BaseButton>
    </BasePageHeader>

    <BaseText role="status" tone="success" size="sm" :class="notice ? undefined : 'sr-only'">
      {{ notice }}
    </BaseText>

    <FeaturesAdminPagedBlock :list="list" subject="areas" forbidden-description="Your account is not allowed to manage areas.">
      <FeaturesAreasTable caption="Areas" :areas="list.items.value" @remove="askRemove" />
    </FeaturesAdminPagedBlock>

    <FeaturesAdminCreateKeyDialog
      v-model:open="createOpen"
      endpoint="/areas"
      title="New area"
      description="An area is a set of people. Its supervisors manage their robots and API tokens. Its key cannot be changed later."
      submit-label="Create area"
      conflict="An area with this key already exists. If you just retried, it may have been created by your earlier attempt: check the list."
      @created="onCreated"
    />
    <UiConfirmDialog
      v-model:open="removeOpen"
      :title="`Delete area ${removing ? groupLabel(removing) : ''}?`"
      description="An area with members or supervisors cannot be deleted: remove them first."
      confirm-label="Delete area"
      :busy="removeBusy"
      :error="removeError"
      @confirm="confirmRemove"
    />
  </BaseStack>
</template>
