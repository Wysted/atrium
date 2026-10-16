<script setup lang="ts">
import type { HealthProblem } from '~/types/api'

/** Findings of the health report with their fixed explanation. The tone is decoration; the label carries the meaning. */
defineProps<{ problems: HealthProblem[] }>()
</script>

<template>
  <BaseStack as="ul" :gap="2">
    <BaseBox v-for="(problem, index) in problems" :key="`${problem.code}-${problem.host ?? ''}-${index}`" as="li">
      <BaseStack :gap="1" align="start">
        <BaseBadge :tone="problemCopy(problem.code).tone">
          {{ problemCopy(problem.code).label }}
        </BaseBadge>
        <BaseText size="sm" tone="muted">
          {{ problemCopy(problem.code).explanation }}
        </BaseText>
      </BaseStack>
    </BaseBox>
  </BaseStack>
</template>
