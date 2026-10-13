<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type LinkView, type Ref } from '../api'
import LinkList from './LinkList.vue'
import Modal from './Modal.vue'

const props = defineProps<{ target: Ref }>()
const emit = defineEmits<{ close: [] }>()
const links = ref<LinkView[] | null>(null)
onMounted(async () => (links.value = await api.links(props.target)))
</script>

<template>
  <Modal title="Linked items" @close="emit('close')">
    <LinkList v-if="links" :links="links" />
    <div v-else class="muted">Loading…</div>
  </Modal>
</template>
