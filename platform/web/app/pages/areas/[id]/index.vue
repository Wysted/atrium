<script setup lang="ts">
const route = useRoute()
const areaId = computed(() => String(route.params.id))
useHead({ title: 'Area - Portal' })
</script>

<template>
  <FeaturesSupervisionGate :key="areaId" v-slot="{ area }" :area-id="areaId">
    <BaseStack :gap="6">
      <BasePageHeader :title="area ? groupLabel(area) : 'Area'" :description="area ? `Area ${area.key}` : undefined">
        <BaseButton to="/areas" variant="secondary">
          Back to my areas
        </BaseButton>
      </BasePageHeader>
      <FeaturesSupervisionMembersPanel :area-id="areaId" />
    </BaseStack>
  </FeaturesSupervisionGate>
</template>
