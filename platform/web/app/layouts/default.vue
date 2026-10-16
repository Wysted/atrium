<template>
  <BaseBox as="div" surface="base">
    <BaseBox as="header" bordered :padding="4">
      <BaseContainer>
        <BaseText as="p" size="lg" weight="semibold">
          Portal
        </BaseText>
      </BaseContainer>
    </BaseBox>
    <BaseBox as="main" :padding="6">
      <BaseContainer>
        <slot />
      </BaseContainer>
    </BaseBox>
  </BaseBox>
</template>
