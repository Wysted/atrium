<script setup lang="ts">
import type { HomeApp } from '~/types/api'

/**
 * One app of the home: name, initiative, the hosts that open it and the caller's roles.
 *
 * The host links are the one place where `BaseLink href` is not a literal: each URL comes from `hostUrl()`,
 * which only builds `https://<host>/` for a value that passes the hostname check (no scheme, port, path or
 * userinfo can get through). Hosts that fail the check are not rendered; when none passes, a muted "No address available" note shows instead.
 */
const props = defineProps<{
  app: HomeApp
}>()

const links = computed(() =>
  props.app.hosts.flatMap((host) => {
    const href = hostUrl(host)
    return href ? [{ host, href }] : []
  }),
)
</script>

<template>
  <BaseCard as="li">
    <BaseStack :gap="4">
      <BaseStack :gap="1">
        <BaseHeading :level="2" size="lg">
          {{ app.name }}
        </BaseHeading>
        <BaseText size="sm" tone="muted">
          {{ app.initiative.name }}
        </BaseText>
      </BaseStack>

      <BaseStack v-if="links.length" as="ul" :gap="1">
        <BaseBox v-for="link in links" :key="link.host" as="li">
          <BaseLink :href="link.href" target="_blank">
            {{ link.host }}
            <BaseText as="span" size="xs" class="sr-only">
              (opens in a new tab)
            </BaseText>
            <BaseIcon name="external-link" size="sm" />
          </BaseLink>
        </BaseBox>
      </BaseStack>
      <BaseText v-else size="sm" tone="muted">
        No address available
      </BaseText>

      <BaseCluster v-if="app.roles.length" :gap="2">
        <BaseText as="span" size="xs" tone="muted">
          Your roles
        </BaseText>
        <BaseBadge v-for="role in app.roles" :key="role" tone="primary">
          {{ role }}
        </BaseBadge>
      </BaseCluster>
    </BaseStack>
  </BaseCard>
</template>
