import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { rawUrl, playRoute } from '../utils/path'
import { extType } from '../utils/file'
import { openLightbox } from '../utils/lightbox'

// useFileOpen 统一「点一个文件该发生什么」，文件管理与搜索结果共用。
// 早先两处各写一份：同一个 PDF 在文件管理里直接打开、在搜索结果里却只跳到所在文件夹，
// 图片更是一个开灯箱、一个跳去照片墙看整个目录 —— 同一个动作两种结果。
//
// 返回的 textVisible/textPath/textKind 供调用页绑到自己的 TextDrawer 上
//（抽屉是各页自己的组件实例，不能塞进这里）。
export function useFileOpen() {
  const router = useRouter()
  const textVisible = ref(false)
  const textPath = ref('')
  const textKind = ref('text')

  // downloadFile 触发浏览器下载（rawUrl 第二参 = 让服务端带 Content-Disposition）
  function downloadFile(path, name) {
    const a = document.createElement('a')
    a.href = rawUrl(path, true)
    a.download = name
    a.click()
  }

  // openFile 派发单个文件的打开方式。
  // imagePaths 为同组图片的全路径列表（供灯箱左右翻页），不给则只开当前这张。
  function openFile(path, name, imagePaths) {
    switch (extType(name)) {
      case 'image': {
        const list = imagePaths?.length ? imagePaths : [path]
        openLightbox(list, Math.max(0, list.indexOf(path)))
        break
      }
      case 'video':
        router.push(playRoute(path))
        break
      case 'markdown':
        textPath.value = path; textKind.value = 'markdown'; textVisible.value = true
        break
      case 'text':
        textPath.value = path; textKind.value = 'text'; textVisible.value = true
        break
      case 'pdf':
        window.open(rawUrl(path), '_blank')
        break
      default:
        downloadFile(path, name)
    }
  }

  return { textVisible, textPath, textKind, openFile, downloadFile }
}
