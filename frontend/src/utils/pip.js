// 系统画中画（iOS/iPadOS/macOS 的悬浮小窗，AVKit 那一层，不是网页里画的小窗）。
//
// 浏览器有两套 API：
//   标准 W3C —— document.pictureInPictureEnabled + video.requestPictureInPicture()
//   WebKit  —— video.webkitSetPresentationMode('picture-in-picture' | 'inline' | 'fullscreen')
// iPhone 至今只给后者：document.pictureInPictureEnabled 在 iOS 上是 undefined。
// 两条路进的都是同一个系统小窗，本模块把差异收在这里，调用方只谈语义。
//
// 两处 WebKit 的事实决定了外层怎么设计：
//   1. webkitSupportsPresentationMode('picture-in-picture') 才是真能力（系统设置里关掉
//      画中画、或元数据未就绪时它返回 false），只判断方法存在会得到一颗点了没反应的按钮；
//   2. Safari 不通知进入失败（没有可 catch 的 promise），所以按钮按能力显隐，
//      而不是先给按钮、点了再报错。

// pipMode 返回可用的画中画通道：'webkit' | 'native' | ''（空 = 该视频当前不支持）。
// 先判 WebKit：Apple 平台上它才是系统小窗那条链。
export function pipMode(video) {
  if (!video) return ''
  if (
    typeof video.webkitSetPresentationMode === 'function'
    && typeof video.webkitSupportsPresentationMode === 'function'
    && video.webkitSupportsPresentationMode('picture-in-picture')
  ) return 'webkit'
  if (document.pictureInPictureEnabled && !video.disablePictureInPicture) return 'native'
  return ''
}

// isPipActive 不走 pipMode：能力判定会随元数据与系统设置变化，而"此刻在不在小窗里"
// 必须恒定可读 —— 两套 API 各有自己的真值来源，谁有读谁。
export function isPipActive(video) {
  if (!video) return false
  if (video.webkitPresentationMode) return video.webkitPresentationMode === 'picture-in-picture'
  return !!document.pictureInPictureElement && document.pictureInPictureElement === video
}

// enterPip 进小窗。必须在用户手势里调用（两套 API 都要求）。
// WebKit 侧同步返回，成功与否只能靠 onPipChange 回来的事件确认。
export function enterPip(video) {
  const mode = pipMode(video)
  if (mode === 'webkit') {
    video.webkitSetPresentationMode('picture-in-picture')
    return Promise.resolve()
  }
  if (mode === 'native') return video.requestPictureInPicture().then(() => {})
  return Promise.reject(new Error('当前设备不支持画中画'))
}

// exitPip 收小窗回内联。不在小窗里就是空操作（接回播放页时会无条件调一次）。
export function exitPip(video) {
  if (!isPipActive(video)) return Promise.resolve()
  if (typeof video.webkitSetPresentationMode === 'function') {
    video.webkitSetPresentationMode('inline')
    return Promise.resolve()
  }
  return document.exitPictureInPicture().catch(() => {}) // 已被系统收走时会抛，无所谓
}

// onPipChange 监听进出小窗，回调收布尔，返回摘除函数。
// webkitpresentationmodechanged 也管全屏：从内联进全屏同样触发，此时 mode 是 'fullscreen'，
// 按"不在小窗"上报正是我们要的语义。
export function onPipChange(video, cb) {
  const onWebkit = () => cb(video.webkitPresentationMode === 'picture-in-picture')
  const onEnter = () => cb(true)
  const onLeave = () => cb(false)
  video.addEventListener('webkitpresentationmodechanged', onWebkit)
  video.addEventListener('enterpictureinpicture', onEnter)
  video.addEventListener('leavepictureinpicture', onLeave)
  return () => {
    video.removeEventListener('webkitpresentationmodechanged', onWebkit)
    video.removeEventListener('enterpictureinpicture', onEnter)
    video.removeEventListener('leavepictureinpicture', onLeave)
  }
}
