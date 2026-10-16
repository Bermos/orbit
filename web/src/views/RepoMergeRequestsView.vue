<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, roleRank, type RepoDetail } from '../api'
import MergeRequestList from '../components/MergeRequestList.vue'
import RepoHeader from '../components/RepoHeader.vue'

const props = defineProps<{ app: string; repo: string }>()
const detail = ref<RepoDetail | null>(null)
const canCreate = ref(false)

watch(
  () => [props.app, props.repo],
  async () => {
    const [d, a] = await Promise.all([api.repo(props.app, props.repo), api.app(props.app)])
    detail.value = d
    canCreate.value = roleRank[a.my_role] >= roleRank.developer && d.branches.length > 1
  },
  { immediate: true },
)
</script>

<template>
  <div class="page wide">
    <RepoHeader :app="app" :repo="repo" :detail="detail" tab="merge_requests" />
    <MergeRequestList :app="app" :repo="repo" :can-create="canCreate" />
  </div>
</template>

<style scoped>
.wide { max-width: 1300px; }
</style>
