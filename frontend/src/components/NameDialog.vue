<template>
  <el-dialog :model-value="modelValue" :title="title" width="400px"
    @update:model-value="$emit('update:modelValue', $event)" @open="onOpen">
    <el-input ref="inputRef" v-model="name" :placeholder="placeholder" @keyup.enter="submit" />
    <div v-if="error" class="err">{{ error }}</div>
    <template #footer>
      <el-button @click="$emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" @click="submit">确定</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, nextTick } from 'vue'

const props = defineProps({
  modelValue: Boolean,
  title: { type: String, default: '名称' },
  placeholder: { type: String, default: '请输入名称' },
  initial: { type: String, default: '' },
})
const emit = defineEmits(['update:modelValue', 'confirm'])

const name = ref('')
const error = ref('')
const inputRef = ref(null)

// 与后端 checkName 一致的校验
const reserved = new Set(['CON', 'PRN', 'AUX', 'NUL',
  'COM1', 'COM2', 'COM3', 'COM4', 'COM5', 'COM6', 'COM7', 'COM8', 'COM9',
  'LPT1', 'LPT2', 'LPT3', 'LPT4', 'LPT5', 'LPT6', 'LPT7', 'LPT8', 'LPT9'])

function validate(n) {
  if (!n) return '名称不能为空'
  if (n === '.' || n === '..') return '名称非法'
  if (/[<>:"/\\|?*]/.test(n)) return '不能包含 < > : " / \\ | ? * 字符'
  for (const ch of n) if (ch.codePointAt(0) < 0x20) return '不能包含控制字符'
  if (n.endsWith('.') || n.endsWith(' ')) return '不能以点或空格结尾'
  const base = n.split('.')[0].toUpperCase()
  if (reserved.has(base)) return `"${base}" 是系统保留名`
  return ''
}

async function onOpen() {
  name.value = props.initial
  error.value = ''
  await nextTick()
  inputRef.value?.focus()
  // 重命名场景：默认选中主文件名部分
  if (props.initial) {
    const i = props.initial.lastIndexOf('.')
    const el = inputRef.value?.input
    if (el) el.setSelectionRange(0, i > 0 ? i : props.initial.length)
  }
}

function submit() {
  const n = name.value.trim()
  error.value = validate(n)
  if (error.value) return
  emit('confirm', n)
  emit('update:modelValue', false)
}
</script>

<style scoped>
.err { color: var(--el-color-error); font-size: 12px; margin-top: 8px; }
</style>
