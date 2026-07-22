// 从封面图提取莫奈调（Material You 式）主色：缩样 24×24 → 粗量化分桶挑主导色
// （跳过近黑/近白）→ HSL 里抬饱和度、钳亮度。返回 {r,g,b}（0-255）或 null（取不到，
// 交由调用方落中性玻璃）。纯函数，从 VideoDetailCard 抽出以便复用与单测。

// rgb→hsl 调整→rgb：s ≥ 0.42（莫奈鲜度）、l 钳 [0.34, 0.58]（深色 UI 上既显色又不刺眼）
function vibrant(r, g, b) {
  r /= 255; g /= 255; b /= 255
  const max = Math.max(r, g, b), min = Math.min(r, g, b)
  let h = 0
  const l = (max + min) / 2
  const dd = max - min
  let s = dd === 0 ? 0 : dd / (1 - Math.abs(2 * l - 1))
  if (dd > 0) {
    if (max === r) h = ((g - b) / dd + (g < b ? 6 : 0)) / 6
    else if (max === g) h = ((b - r) / dd + 2) / 6
    else h = ((r - g) / dd + 4) / 6
  }
  s = Math.max(s, 0.42)
  const l2 = Math.min(Math.max(l, 0.34), 0.58)
  const q = l2 < 0.5 ? l2 * (1 + s) : l2 + s - l2 * s
  const p = 2 * l2 - q
  const f = (t) => {
    t = (t + 1) % 1
    if (t < 1 / 6) return p + (q - p) * 6 * t
    if (t < 1 / 2) return q
    if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6
    return p
  }
  return {
    r: Math.round(f(h + 1 / 3) * 255),
    g: Math.round(f(h) * 255),
    b: Math.round(f(h - 1 / 3) * 255),
  }
}

// extractVibrant 从已加载的 <img> 提取主色。缩样到 24×24 → 粗量化分桶挑主导色
//（跳过近黑/近白）→ vibrant 修正。跨域 302 兜底会让 getImageData 抛错 → 落 null。
export function extractVibrant(img) {
  try {
    const S = 24
    const c = document.createElement('canvas')
    c.width = S; c.height = S
    const ctx = c.getContext('2d', { willReadFrequently: true })
    ctx.drawImage(img, 0, 0, S, S)
    const d = ctx.getImageData(0, 0, S, S).data // 同源缩略图；跨域会抛错走 catch
    const buckets = new Map()
    for (let i = 0; i < d.length; i += 4) {
      const r = d[i], g = d[i + 1], b = d[i + 2]
      const max = Math.max(r, g, b), min = Math.min(r, g, b)
      if ((max + min) / 2 < 24 || (max + min) / 2 > 235) continue
      const key = ((r >> 5) << 10) | ((g >> 5) << 5) | (b >> 5)
      const e = buckets.get(key) || { n: 0, r: 0, g: 0, b: 0, s: 0 }
      e.n++; e.r += r; e.g += g; e.b += b; e.s += max - min
      buckets.set(key, e)
    }
    let best = null, bestScore = -1
    for (const v of buckets.values()) {
      const score = v.n * (1 + v.s / v.n / 64) // 占比为主，饱和度加成防灰底压过主体色
      if (score > bestScore) { bestScore = score; best = v }
    }
    return best ? vibrant(best.r / best.n, best.g / best.n, best.b / best.n) : null
  } catch {
    return null
  }
}
