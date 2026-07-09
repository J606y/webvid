<template>
  <el-dialog :model-value="modelValue" :title="mode === 'move' ? '移动到' : '复制到'" width="440px"
    @update:model-value="$emit('update:modelValue', $event)" @open="onOpen">
    <div class="tip dim">选择目标目录（{{ selected || '未选择' }}）</div>
    <div class="tree-box">
      <el-tree v-if="treeKey" :key="treeKey" lazy :load="loadNode" :props="treeProps"
        node-key="path" highlight-current @current-change="onSelect" />
    </div>
    <template #footer>
      <el-button @click="$emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :disabled="!selected" :loading="loading" @click="submit">
        {{ mode === 'move' ? '移动' : '复制' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../api/http'
import { join } from '../utils/path'

const props = defineProps({
  modelValue: Boolean,
  mode: { type: String, default: 'move' }, // move | copy
  paths: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:modelValue', 'done', 'tasks'])

const selected = ref('')
const loading = ref(false)
const treeKey = ref(0)
const treeProps = { label: 'name', isLeaf: 'leaf' }

function onOpen() {
  selected.value = ''
  treeKey.value++ // 重新挂载树，清掉旧的懒加载缓存
}

async function loadNode(node, resolve) {
  const path = node.level === 0 ? '/' : node.data.path
  try {
    const d = await http.get('/fs/list', { params: { path } })
    const dirs = (d.items || []).filter((x) => x.is_dir).map((x) => ({
      name: x.name,
      path: join(path, x.name),
      leaf: false,
    }))
    if (node.level === 0) {
      resolve([{ name: '根目录 /', path: '/', leaf: false, children: dirs }])
    } else {
      resolve(dirs)
    }
  } catch {
    resolve([])
  }
}

function onSelect(data) {
  selected.value = data?.path || ''
}

async function submit() {
  loading.value = true
  try {
    const d = await http.post(props.mode === 'move' ? '/fs/move' : '/fs/copy', {
      paths: props.paths,
      dst_dir: selected.value,
    })
    const taskIDs = d?.task_ids || []
    const errs = d?.errors || []
    if (errs.length) ElMessage.warning(errs.join('；'))
    if (taskIDs.length) {
      ElMessage.success(`已创建 ${taskIDs.length} 个转存任务`)
      emit('tasks')
    } else if (!errs.length) {
      ElMessage.success(props.mode === 'move' ? '已移动' : '已复制')
    }
    emit('update:modelValue', false)
    emit('done')
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.tip { font-size: 12px; margin-bottom: 8px; }
.tree-box {
  max-height: 320px; overflow: auto;
  border: 1px solid var(--glass-border); border-radius: 10px; padding: 8px;
}
</style>
