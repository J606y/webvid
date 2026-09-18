import { ElMessage, ElMessageBox } from 'element-plus'
import 'element-plus/es/components/message-box/style/css'
import { api } from '../utils/api'

// 媒体库删除：视频库与照片墙共用。
//
// 不预判能不能删。媒体库的项跨多个挂载，前端拿不到逐目录的写权限——Files 的 caps 是
// api.fs.list 随每个目录返回的，媒体库这条路上没有对应来源。与其猜一个不准的权限去
// 灰掉菜单项，不如直接调，后端拒绝时由 http 拦截器把原话弹出来。
//
// lists 是本页所有会展示这一项的列表（网格 / 各条货架 / 随机推荐横幅）。删掉后就地
// 摘除而不整页重拉：媒体库是分页累积的，重拉会把用户滚到的位置连同已加载的几百项
// 一起丢掉。
export function useMediaRemove(lists) {
  async function removeItem(item) {
    const ok = await ElMessageBox.confirm(
      `确定删除「${item.name}」？此操作不可恢复。`, '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    ).then(() => true).catch(() => false)
    if (!ok) return

    const d = await api.fs.remove([item.path]).catch(() => null)
    if (!d) return // 失败提示拦截器已经弹过，不重复打扰
    const errs = d.errors || []
    if (errs.length) {
      ElMessage.warning('未删除：' + errs.join('；'))
      return
    }
    ElMessage.success('已删除')
    for (const list of lists) {
      const i = list.value.findIndex((x) => x.path === item.path)
      if (i >= 0) list.value.splice(i, 1)
    }
  }

  return { removeItem }
}
