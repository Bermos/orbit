<script setup lang="ts">
import type { RepoDetail } from '../api'

defineProps<{ app: string; repo: string; detail: RepoDetail | null; tab: 'code' | 'commits' | 'pipelines'; gitRef?: string }>()
</script>

<template>
  <div class="page-header">
    <div>
      <div class="crumbs small muted"><RouterLink :to="`/apps/${app}`">{{ app }}</RouterLink> /</div>
      <h1 class="mono">{{ repo }}</h1>
    </div>
    <span class="spacer" />
    <div v-if="detail" class="clone row">
      <span class="small muted">Clone</span>
      <input class="mono small" :value="detail.clone_url" readonly aria-label="Clone URL" @focus="($event.target as HTMLInputElement).select()" />
    </div>
  </div>
  <nav v-if="detail && !detail.empty" class="tabs">
    <RouterLink :to="`/apps/${app}/repos/${repo}${gitRef ? '/tree/' + gitRef : ''}`" :class="{ active: tab === 'code' }">Code</RouterLink>
    <RouterLink :to="{ path: `/apps/${app}/repos/${repo}${gitRef ? '/tree/' + gitRef : ''}`, query: { tab: 'commits' } }" :class="{ active: tab === 'commits' }">
      Commits
    </RouterLink>
    <RouterLink :to="`/apps/${app}/repos/${repo}/pipelines`" :class="{ active: tab === 'pipelines' }">Pipelines</RouterLink>
  </nav>
</template>

<style scoped>
.clone input { width: 360px; }
</style>
