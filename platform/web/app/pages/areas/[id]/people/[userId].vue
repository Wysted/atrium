<script setup lang="ts">
const route = useRoute()
const areaId = computed(() => String(route.params.id))
const userId = computed(() => String(route.params.userId))
// Display only, remembered by the people list; the API decides what the id reaches.
const people = useState<Record<string, string>>('supervision-people', () => ({}))
const label = computed(() => people.value[userId.value] || 'this person')
useHead({ title: 'Tokens and robots - Portal' })
</script>

<template>
  <FeaturesSupervisionGate :key="userId" :area-id="areaId">
    <BaseStack :gap="6">
      <BasePageHeader title="Tokens and robots" :description="`Offboarding tools for ${label}.`">
        <BaseButton :to="`/areas/${encodeURIComponent(areaId)}`" variant="secondary">
          Back to the area
        </BaseButton>
      </BasePageHeader>
      <FeaturesSupervisionPersonPanel :user-id="userId" :area-id="areaId" :label="label" />
    </BaseStack>
  </FeaturesSupervisionGate>
</template>
