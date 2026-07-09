<template>
  <el-drawer :model-value="modelValue" :title="name" size="60%"
    @update:model-value="$emit('update:modelValue', $event)" @open="load">
    <div v-loading="loading" class="body">
      <article v-if="kind === 'markdown'" class="markdown-body" v-html="html" />
      <pre v-else class="code"><code ref="codeRef" :class="langClass">{{ text }}</code></pre>
    </div>
    <template #footer>
      <a :href="rawUrl(path, true)">
        <el-button :icon="Download">下载</el-button>
      </a>
    </template>
  </el-drawer>
</template>

<script setup>
import { ref, computed, nextTick } from 'vue'
import { Download } from '@element-plus/icons-vue'
import axios from 'axios'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import hljs from 'highlight.js/lib/common'
import 'highlight.js/styles/github-dark.css'
import 'github-markdown-css/github-markdown-dark.css'
import { rawUrl } from '../utils/path'
import { ext } from '../utils/file'

const props = defineProps({
  modelValue: Boolean,
  path: { type: String, default: '' },
  kind: { type: String, default: 'text' }, // markdown | text
})
defineEmits(['update:modelValue'])

const loading = ref(false)
const text = ref('')
const html = ref('')
const codeRef = ref(null)

const name = computed(() => props.path.split('/').filter(Boolean).pop() || '')
const langClass = computed(() => {
  const e = ext(name.value)
  return hljs.getLanguage(e) ? 'language-' + e : ''
})

async function load() {
  loading.value = true
  text.value = ''
  html.value = ''
  try {
    // 1MB 以上只取前 1MB，防止大文件卡死抽屉
    const res = await axios.get(rawUrl(props.path), {
      responseType: 'text',
      headers: { Range: 'bytes=0-1048575' },
      transformResponse: [(x) => x],
    })
    text.value = typeof res.data === 'string' ? res.data : String(res.data)
    if (props.kind === 'markdown') {
      html.value = DOMPurify.sanitize(marked.parse(text.value))
    } else {
      await nextTick()
      if (codeRef.value) hljs.highlightElement(codeRef.value)
    }
  } catch (e) {
    text.value = '加载失败: ' + (e.message || '')
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.body { min-height: 200px; }
.code {
  margin: 0; padding: 14px;
  background: rgba(255, 255, 255, 0.04);
  border-radius: 10px;
  overflow: auto;
  font-size: 13px; line-height: 1.6;
}
.code code { background: transparent; white-space: pre-wrap; word-break: break-all; }
</style>
