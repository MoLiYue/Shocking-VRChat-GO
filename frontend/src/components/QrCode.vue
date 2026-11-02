<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import QrCreator from 'qr-creator'

const props = defineProps<{ content: string; size?: number }>()
const canvasRef = ref<HTMLCanvasElement | null>(null)

// Bundled (no CDN): the QR must render even when the PC is offline.

function render() {
  const canvas = canvasRef.value
  if (!canvas || !props.content) return
  const size = props.size || 240
  canvas.width = size
  canvas.height = size
  const ctx = canvas.getContext('2d')!
  ctx.clearRect(0, 0, size, size)
  QrCreator.render({
    text: props.content,
    radius: 0.0,
    ecLevel: 'L',
    background: '#ffffff',
    fill: '#1a1a2e',
    size,
  }, canvas)
}

watch(() => props.content, render)
onMounted(render)
</script>

<template>
  <div class="qr-container">
    <canvas ref="canvasRef" class="qr-canvas"></canvas>
  </div>
</template>

<style scoped>
.qr-container { display: flex; justify-content: center; padding: var(--sp-4); }
.qr-canvas { border-radius: var(--radius-md); max-width: 100%; background: #ffffff; padding: 12px; }
</style>
