// M3/M4 远端存储写操作联调脚本（真实账号）：
//   小文件(<4MiB)上传 → 302 下载校验 → 缩略图 → rename → copy → move → remove 清理
// 用法：node od-tail-check.mjs   （需服务在跑；挂载根默认 /test，可 OD_ROOT=/pikpak 复用）
import { createHash, randomBytes } from 'node:crypto'
import { readFile } from 'node:fs/promises'

const BASE = process.env.NL_BASE || 'http://localhost:5243'
const ROOT = process.env.OD_ROOT || '/test'
const DIR = `${ROOT}/nl-尾项联调`

let token = ''
let passed = 0, failed = 0
const ok = (name, cond, extra = '') => {
  if (cond) { passed++; console.log(`  ✅ ${name}${extra ? '  ' + extra : ''}`) }
  else { failed++; console.error(`  ❌ ${name}${extra ? '  ' + extra : ''}`) }
  return cond
}
const enc = p => p.split('/').map(encodeURIComponent).join('/')
const sha256 = b => createHash('sha256').update(b).digest('hex')

async function api(method, path, body, raw = false) {
  const r = await fetch(BASE + path, {
    method,
    headers: {
      Authorization: 'Bearer ' + token,
      ...(body && !raw ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body ? (raw ? body : JSON.stringify(body)) : undefined,
    signal: AbortSignal.timeout(180_000),
  })
  const j = await r.json().catch(() => ({}))
  return { status: r.status, code: j.code, data: j.data, message: j.message }
}
const list = async p => (await api('GET', `/api/fs/list?path=${encodeURIComponent(p)}`)).data?.items ?? []

async function main() {
  // 登录
  const lg = await api('POST', '/api/auth/login', { username: 'admin', password: process.env.NL_PASS || 'admin123' })
  if (!lg.data?.token) throw new Error('登录失败: ' + JSON.stringify(lg))
  token = lg.data.token
  console.log(`目标挂载根: ${ROOT}`)

  // 0. 挂载可用（顺带验证 token 获取/Init 正常）
  const rootFiles = await list(ROOT)
  ok('挂载可列', Array.isArray(rootFiles), `(${rootFiles.length} 项)`)

  // 1. mkdir（中文名）
  const mk = await api('POST', '/api/fs/mkdir', { path: DIR })
  ok('mkdir 中文目录', mk.code === 200, mk.message)

  // 2. 小文件上传（<4MiB 单请求路径；名称含中文+空格）
  const payload = randomBytes(256 * 1024)
  const smallPath = `${DIR}/小文件 测试.bin`
  const up = await fetch(`${BASE}/api/fs/upload?path=${encodeURIComponent(smallPath)}`, {
    method: 'PUT', headers: { Authorization: 'Bearer ' + token }, body: payload,
    signal: AbortSignal.timeout(120_000),
  }).then(r => r.json())
  ok('小文件上传(<4MiB)', up.code === 200, up.message)
  const inDir = await list(DIR)
  const upEntry = inDir.find(f => f.name === '小文件 测试.bin')
  ok('列表可见且大小一致', upEntry?.size === payload.length, `size=${upEntry?.size}`)

  // 3. 302 直链下载校验
  const rawURL = `${BASE}/api/raw${enc(smallPath)}?token=${token}`
  const rd = await fetch(rawURL, { redirect: 'manual' })
  ok('raw 返回 302 直链', rd.status === 302)
  const body = Buffer.from(await (await fetch(rawURL)).arrayBuffer())
  ok('下载内容 sha256 一致', sha256(body) === sha256(payload))

  // 4. 缩略图（上传真实 jpg，轮询等云端生成）
  const jpg = await readFile(new URL('../files/图片/壁纸/暖阳橘子海.jpg', import.meta.url))
  const imgPath = `${DIR}/缩略图样张.jpg`
  const upImg = await fetch(`${BASE}/api/fs/upload?path=${encodeURIComponent(imgPath)}`, {
    method: 'PUT', headers: { Authorization: 'Bearer ' + token }, body: jpg,
    signal: AbortSignal.timeout(120_000),
  }).then(r => r.json())
  ok('图片上传', upImg.code === 200, upImg.message)
  let thumbOK = false, thumbInfo = ''
  for (let i = 0; i < 12 && !thumbOK; i++) {          // 云端生成缩略图可能要等
    const t = await fetch(`${BASE}/api/thumb${enc(imgPath)}?token=${token}`)
    const ct = t.headers.get('content-type') || ''
    if (t.status === 200 && ct.startsWith('image/')) {
      const len = (await t.arrayBuffer()).byteLength
      thumbOK = len > 1000
      thumbInfo = `${ct} ${len}B 第${i + 1}次`
    } else { await t.arrayBuffer().catch(() => {}); await new Promise(r => setTimeout(r, 5000)) }
  }
  ok('缩略图可取(服务端落盘)', thumbOK, thumbInfo)

  // 5. rename
  const rn = await api('POST', '/api/fs/rename', { path: smallPath, name: '改名后.bin' })
  ok('rename', rn.code === 200, rn.message)
  const afterRn = await list(DIR)
  ok('改名生效', afterRn.some(f => f.name === '改名后.bin') && !afterRn.some(f => f.name === '小文件 测试.bin'))

  // 6. copy（同存储 → Graph 异步复制+monitor 轮询）
  const sub = `${DIR}/子目录`
  await api('POST', '/api/fs/mkdir', { path: sub })
  const cp = await api('POST', '/api/fs/copy', { paths: [`${DIR}/改名后.bin`], dst_dir: sub })
  ok('copy 同存储', cp.code === 200, cp.message)
  const inSub = await list(sub)
  ok('副本存在且大小一致', inSub.find(f => f.name === '改名后.bin')?.size === payload.length)
  const rn2 = await api('POST', '/api/fs/rename', { path: `${sub}/改名后.bin`, name: '副本.bin' })
  ok('副本改名', rn2.code === 200, rn2.message)

  // 7. move
  const mv = await api('POST', '/api/fs/move', { paths: [`${DIR}/改名后.bin`], dst_dir: sub })
  ok('move 同存储', mv.code === 200, mv.message)
  const parentAfter = await list(DIR)
  const subAfter = await list(sub)
  ok('源目录已无原文件', !parentAfter.some(f => f.name === '改名后.bin'))
  ok('目标目录两文件齐', subAfter.some(f => f.name === '改名后.bin') && subAfter.some(f => f.name === '副本.bin'))
  const moved = Buffer.from(await (await fetch(`${BASE}/api/raw${enc(sub + '/改名后.bin')}?token=${token}`)).arrayBuffer())
  ok('移动后内容 sha256 一致', sha256(moved) === sha256(payload))

  // 8. 清理：整目录删除
  const rm = await api('POST', '/api/fs/remove', { paths: [DIR] })
  ok('remove 整目录', rm.code === 200, rm.message)
  const rootAfter = await list(ROOT)
  ok('清理后目录已消失', !rootAfter.some(f => f.name === 'nl-尾项联调'))

  console.log(`\n${failed === 0 ? '🎉' : '⚠️'} 通过 ${passed}/${passed + failed}`)
  process.exit(failed === 0 ? 0 : 1)
}

main().catch(e => { console.error('💥 中断:', e); process.exit(1) })
