#!/usr/bin/env node
// 发布产物校验(release gate):检查 updater 端点当前供应的 latest.json 是否**真的能装**。
//
// 为什么需要它(2026-09-18):`scripts/publish-desktop.sh` 只在**本平台**产出
// `latest.<平台>.json`,跨平台靠人工 `merge` 合并上传 —— 漏跑一个平台的后果是
// 该平台更新器拿到 404,而壳侧把 404 翻译成「暂无可用更新」(`explainUpdateError`),
// 看起来和「已是最新」一模一样。同类静默失效还有:用了别的密钥签名(keyid 不匹配)、
// 产物上传半截(签名对不上)、合并后的版本号比线上已装的还旧(装完反而降级)。
// 这些都不需要真机点托盘就能查出来,所以放在脚本里,每次发版跑一次。
//
// 用法:
//   node scripts/verify-release.mjs                    # 校验线上最新 release(默认只验本机平台产物)
//   node scripts/verify-release.mjs --all              # 逐平台下载产物验签(慢:每平台 20~40 MiB)
//   node scripts/verify-release.mjs --tag v0.1.4       # 校验指定 tag
//   node scripts/verify-release.mjs --local dist-desktop  # 发布前干跑:校验本地 latest.json 与产物
//   node scripts/verify-release.mjs --skip-artifacts   # 只查结构/矩阵/密钥指纹(零下载)
//
// 校验口径:签名 = minisign(tauri updater 消费的那一条);哈希 = BLAKE2b-512(`ED` 档)
// 或原始消息(`Ed` 档)。文件里的第三条「global signature」tauri 更新器不消费,
// 本脚本只做存在性检查并如实标注未校验,不假装验过。

import { execFileSync } from 'node:child_process'
import { createHash, createPublicKey, verify as edVerify } from 'node:crypto'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'

// ---------- 结果收集 ----------
const results = []
const ok = (name, detail = '') => results.push({ level: 'OK', name, detail })
const fail = (name, detail = '') => results.push({ level: 'FAIL', name, detail })
const info = (name, detail = '') => results.push({ level: 'INFO', name, detail })

// ---------- 参数 ----------
function parseArgs(argv) {
  const o = { all: false, skipArtifacts: false, tag: '', local: '', keep: false, platforms: [] }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--all') o.all = true
    else if (a === '--skip-artifacts') o.skipArtifacts = true
    else if (a === '--keep') o.keep = true
    else if (a === '--tag') o.tag = argv[++i] ?? ''
    else if (a === '--local') o.local = argv[++i] ?? ''
    else if (a === '--platform') (o.platforms ??= []).push(argv[++i] ?? '')
    else if (a === '--help' || a === '-h') o.help = true
    else {
      console.error(`未知参数:${a}`)
      process.exit(2)
    }
  }
  return o
}

// 平台矩阵:publish-desktop.sh 的发行矩阵(darwin aarch64 + windows x64)。
// 加新平台时改这里(或用 GAH_RELEASE_PLATFORMS 覆盖,便于先跑后登记)。
const DEFAULT_PLATFORMS = ['darwin-aarch64', 'windows-x86_64']

// 本机平台 → tauri updater 平台键(默认只验本机,避免每次发版下载全平台)。
function hostPlatform() {
  const m = { darwin: { arm64: 'darwin-aarch64', x64: 'darwin-x86_64' }, win32: { x64: 'windows-x86_64' }, linux: { x64: 'linux-x86_64', arm64: 'linux-aarch64' } }
  return m[process.platform]?.[process.arch] ?? ''
}

// ---------- 基础设施 ----------
function sh(cmd, args, opts = {}) {
  return execFileSync(cmd, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], ...opts })
}

function hasCmd(name) {
  try {
    sh('which', [name])
    return true
  } catch {
    return false
  }
}

function repoSlug() {
  const url = sh('git', ['remote', 'get-url', 'origin']).trim()
  const m = url.match(/github\.com[/:]([^/]+)\/([^/.]+)/)
  return m ? `${m[1]}/${m[2]}` : 'nekoleamo/go-agent-harness'
}

function repoRoot() {
  return sh('git', ['rev-parse', '--show-toplevel']).trim()
}

// 下载一个 release 资产:优先 `gh`(带重试、进度),缺失时回退到裸 HTTPS。
async function downloadAsset(slug, tag, name, dir) {
  const gh = hasCmd('gh')
  if (gh) {
    const args = ['release', 'download']
    if (tag) args.push(tag)
    args.push('-R', slug, '-p', name, '-D', dir, '--clobber')
    sh('gh', args, { timeout: 900_000 })
    return join(dir, name)
  }
  const base = tag ? `https://github.com/${slug}/releases/download/${tag}` : `https://github.com/${slug}/releases/latest/download`
  for (let attempt = 1; attempt <= 3; attempt++) {
    try {
      const r = await fetch(`${base}/${name}`, { signal: AbortSignal.timeout(900_000) })
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const buf = Buffer.from(await r.arrayBuffer())
      const p = join(dir, name)
      const { writeFileSync } = await import('node:fs')
      writeFileSync(p, buf)
      return p
    } catch (e) {
      if (attempt === 3) throw e
    }
  }
}

async function fetchManifest(slug, tag, dir) {
  const base = tag ? `https://github.com/${slug}/releases/download/${tag}` : `https://github.com/${slug}/releases/latest/download`
  for (let attempt = 1; attempt <= 2; attempt++) {
    try {
      const r = await fetch(`${base}/latest.json`, { signal: AbortSignal.timeout(60_000) })
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      return { json: await r.json(), via: `updater 端点(${base}/latest.json)` }
    } catch (e) {
      if (attempt === 2) {
        // 端点不可用时退到 gh(例如网络对裸 HTTPS 不稳)
        if (hasCmd('gh')) {
          const p = await downloadAsset(slug, tag, 'latest.json', dir)
          return { json: JSON.parse(readFileSync(p, 'utf8')), via: `gh release download(${p})` }
        }
        throw e
      }
    }
  }
}

// ---------- minisign / ed25519 ----------
const SPKI_ED25519 = Buffer.from('302a300506032b6570032100', 'hex')

function parsePubkey(b64) {
  const text = Buffer.from(b64, 'base64').toString('utf8').trim()
  const lines = text.split('\n')
  if (lines.length < 2) return { err: 'pubkey 不是 minisign 公钥文件(期望两行)' }
  const blob = Buffer.from(lines[1], 'base64')
  if (blob.length !== 42) return { err: `pubkey 长度异常 ${blob.length}(期望 42)` }
  return { algo: blob.subarray(0, 2).toString(), keyid: blob.subarray(2, 10), key: blob.subarray(10), comment: lines[0] }
}

function parseSignature(b64) {
  const text = Buffer.from(b64, 'base64').toString('utf8').trim()
  const lines = text.split('\n')
  if (lines.length < 2) return { err: '签名不是 minisign 签名文件(期望 ≥2 行)' }
  const blob = Buffer.from(lines[1], 'base64')
  if (blob.length !== 74) return { err: `签名长度异常 ${blob.length}(期望 74)` }
  return { algo: blob.subarray(0, 2).toString(), keyid: blob.subarray(2, 10), sig: blob.subarray(10), globalSig: lines[3] ?? '' }
}

// verifyArtifact 校验签名:algo `Ed` = 直接对消息签名;`ED` = 对 BLAKE2b-512 摘要签名。
function verifyArtifact(pub, sig, message) {
  if (!sig.keyid.equals(pub.keyid)) return { ok: false, reason: `密钥指纹不匹配(签名 ${sig.keyid.toString('hex')} ≠ 公钥 ${pub.keyid.toString('hex')})——用别的密钥签的,更新器会拒装` }
  const key = createPublicKey({ key: Buffer.concat([SPKI_ED25519, pub.key]), format: 'der', type: 'spki' })
  let msg = message
  if (sig.algo === 'ED') msg = createHash('blake2b512').update(message).digest()
  else if (sig.algo !== 'Ed') return { ok: false, reason: `未知签名算法 ${JSON.stringify(sig.algo)}` }
  const good = edVerify(null, msg, key, sig.sig)
  return { ok: good, reason: good ? '' : '签名验证失败(产物与签名不匹配:上传半截/被改动)' }
}

// ---------- 各阶段检查 ----------
function checkManifest(m) {
  if (typeof m.version !== 'string' || !/^\d+\.\d+\.\d+/.test(m.version)) return fail('manifest.version', `语义化版本缺失/异常:${JSON.stringify(m.version)}`) || false
  ok('manifest.version', m.version)
  const d = Date.parse(m.pub_date ?? '')
  if (Number.isNaN(d)) fail('manifest.pub_date', `不是可解析时间:${JSON.stringify(m.pub_date)}`)
  else ok('manifest.pub_date', `${m.pub_date}(${new Date(d).toISOString()})`)
  const keys = Object.keys(m.platforms ?? {})
  if (keys.length === 0) return fail('manifest.platforms', '为空(更新器拿到空清单 = 所有平台都「已是最新」)') || false
  ok('manifest.platforms', keys.join(', '))
  for (const k of keys) {
    const e = m.platforms[k] ?? {}
    if (typeof e.url !== 'string' || !e.url.startsWith('https://')) fail(`platforms.${k}.url`, `不是 https 绝对地址:${JSON.stringify(e.url)}`)
    if (typeof e.signature !== 'string' || e.signature.length === 0) fail(`platforms.${k}.signature`, '缺失(更新器会拒绝安装)')
  }
  return true
}

function checkMatrix(m, expected) {
  const keys = Object.keys(m.platforms ?? {})
  const missing = expected.filter((k) => !keys.includes(k))
  if (missing.length > 0) fail('平台矩阵完整', `缺少 ${missing.join(', ')}——该平台用户会拿到 404 并被显示成「暂无可用更新」`)
  else ok('平台矩阵完整', expected.join(', '))
  const extra = keys.filter((k) => !expected.includes(k))
  if (extra.length > 0) info('矩阵外平台', `${extra.join(', ')}(已在线上但未登记在 DEFAULT_PLATFORMS,确认后补登记)`)
}

// 平台产物的内容自查(darwin:解包看 app 版本 + 内嵌 sidecar 版本)。
function checkDarwinBundle(platform, path) {
  if (!platform.startsWith('darwin-')) return
  if (process.platform !== 'darwin') {
    info(`${platform} 包内容`, '非 macOS 主机,跳过解包自查(tar/plutil 不保证可用)')
    return
  }
  const dir = mkdtempSync(join(tmpdir(), 'gah-verify-'))
  try {
    sh('tar', ['-xzf', path, '-C', dir], { timeout: 300_000 })
    const app = join(dir, 'gah.app')
    if (!existsSync(app)) return fail(`${platform} 包内容`, '解包后没有 gah.app(updater 产物布局变了)')
    const plist = join(app, 'Contents', 'Info.plist')
    const ver = sh('plutil', ['-extract', 'CFBundleShortVersionString', 'raw', '-o', '-', plist]).trim()
    const bid = sh('plutil', ['-extract', 'CFBundleIdentifier', 'raw', '-o', '-', plist]).trim()
    const want = VERIFIED_VERSION
    if (ver !== want) fail(`${platform} 包内容`, `Info.plist 版本 ${ver} ≠ manifest ${want}(包与 manifest 不是同一次构建)`)
    else ok(`${platform} 包内容`, `gah.app ${ver}(${bid})`)
    const sidecar = join(app, 'Contents', 'MacOS', 'gah')
    if (!existsSync(sidecar)) fail(`${platform} sidecar`, 'Contents/MacOS/gah 不存在')
    else {
      const out = sh(sidecar, ['--version'], { timeout: 60_000 }).trim()
      if (!out.includes(want)) fail(`${platform} sidecar`, `--version 输出 ${JSON.stringify(out)} 不含 ${want}`)
      else ok(`${platform} sidecar`, out)
    }
  } catch (e) {
    fail(`${platform} 包内容`, `解包/自检失败:${e.message}`)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

// 与已装版本比:线上 latest 比本机已装还旧 = 更新器会「升级」成降级。
function checkInstalledVersion(m, remoteIsLatest) {
  if (!remoteIsLatest) return
  const app = '/Applications/gah.app/Contents/Info.plist'
  if (!existsSync(app)) return info('已装版本对比', '本机没有 /Applications/gah.app,跳过')
  try {
    const installed = sh('plutil', ['-extract', 'CFBundleShortVersionString', 'raw', '-o', '-', app]).trim()
    const cmp = compareSemver(m.version, installed)
    if (cmp < 0) fail('已装版本对比', `线上 latest ${m.version} < 本机已装 ${installed}(装完会降级)`)
    else if (cmp === 0) ok('已装版本对比', `线上 latest = 本机已装 ${installed}`)
    else ok('已装版本对比', `线上 latest ${m.version} > 本机已装 ${installed}(可升级)`)
    return m.version
  } catch (e) {
    info('已装版本对比', `读取失败:${e.message}`)
  }
}

function compareSemver(a, b) {
  const pa = a.split(/[.-]/).map((x) => Number.parseInt(x, 10) || 0)
  const pb = b.split(/[.-]/).map((x) => Number.parseInt(x, 10) || 0)
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0)
    if (d !== 0) return d > 0 ? 1 : -1
  }
  return 0
}

// ---------- 主流程 ----------
let VERIFIED_VERSION = ''

const opts = parseArgs(process.argv.slice(2))
if (opts.help) {
  console.log(readFileSync(new URL(import.meta.url)).toString().split('\n').filter((l) => l.startsWith('//')).map((l) => l.slice(3)).join('\n'))
  process.exit(0)
}

const slug = repoSlug()
const root = repoRoot()
const tmp = mkdtempSync(join(tmpdir(), 'gah-rel-'))
const expected = (process.env.GAH_RELEASE_PLATFORMS ?? '').split(',').filter(Boolean)
const platforms = expected.length > 0 ? expected : DEFAULT_PLATFORMS

console.log(`发布产物校验:${slug}${opts.tag ? ` tag=${opts.tag}` : '(latest)'}${opts.local ? ` local=${opts.local}` : ''}`)

try {
  // 1) manifest
  let manifest
  let via
  if (opts.local) {
    const p = join(resolve(opts.local), 'latest.json')
    if (!existsSync(p)) {
      fail('latest.json', `本地不存在:${p}(先跑 publish-desktop.sh merge)`)
      throw new Error('no manifest')
    }
    manifest = JSON.parse(readFileSync(p, 'utf8'))
    via = p
  } else {
    const r = await fetchManifest(slug, opts.tag, tmp)
    manifest = r.json
    via = r.via
  }
  if (!checkManifest(manifest)) throw new Error('manifest 结构不可用')
  VERIFIED_VERSION = manifest.version

  // 平台矩阵:线上(latest)按完整矩阵查漏;--local 单平台收窄到被点名的平台
  // (本地 latest.<平台>.json 本就只有一条,拿完整矩阵去比会把「按平台分别发」判成失败)。
  const matrixWant = opts.local && opts.platforms.length > 0 ? opts.platforms : platforms
  checkMatrix(manifest, matrixWant)

  // 2) 密钥指纹:每条签名都必须用**仓库里登记的那把钥匙**签的
  const conf = JSON.parse(readFileSync(join(root, 'desktop/src-tauri/tauri.conf.json'), 'utf8'))
  const pubB64 = conf?.plugins?.updater?.pubkey
  let pub
  if (typeof pubB64 !== 'string' || pubB64.length === 0) fail('公钥', 'tauri.conf.json 未配置 plugins.updater.pubkey')
  else {
    pub = parsePubkey(pubB64)
    if (pub.err) fail('公钥', pub.err)
    else ok('公钥', `${pub.algo} ${pub.keyid.toString('hex')}${String(pub.comment).includes(':') ? `(${pub.comment.split(':').pop().trim()})` : ''}`)
  }
  for (const [k, e] of Object.entries(manifest.platforms ?? {})) {
    const sig = parseSignature(e.signature ?? '')
    if (sig.err) fail(`signature.${k}`, sig.err)
    else if (pub && !sig.keyid.equals(pub.keyid)) fail(`signature.${k}`, `指纹不匹配(${sig.keyid.toString('hex')})——不是这把钥匙签的`)
    else if (pub) ok(`signature.${k}`, `${sig.algo} ${sig.keyid.toString('hex')}${sig.globalSig ? ', global signature 存在(更新器不消费,未校验)' : ''}`)
  }

  // 3) 产物逐字节验签
  const want = opts.platforms?.length > 0 ? opts.platforms : opts.all ? ['*'] : [hostPlatform()]
  const selected = opts.skipArtifacts ? [] : Object.keys(manifest.platforms ?? {}).filter((k) => want.includes('*') || want.includes(k))
  for (const k of want.filter((x) => x !== '*' && !(k_has(manifest, x)))) fail('平台选择', `--platform ${k} 不在线上清单里`)
  if (opts.skipArtifacts) info('产物验签', '--skip-artifacts:只做结构/矩阵/指纹检查')
  else if (selected.length === 0) info('产物验签', `本机平台 ${JSON.stringify(hostPlatform())} 不在清单内;需要时用 --platform <键> / --all`)
  for (const k of selected) {
    const e = manifest.platforms[k]
    const name = basename(new URL(e.url).pathname)
    let path
    try {
      path = opts.local ? findLocal(opts.local, name) : await downloadAsset(slug, opts.tag, name, tmp)
      if (!path) throw new Error(`本地目录找不到 ${name}`)
    } catch (err) {
      fail(`产物 ${k}`, `下载失败:${err.message}`)
      continue
    }
    const buf = readFileSync(path)
    const sha = createHash('sha256').update(buf).digest('hex')
    const sig = parseSignature(e.signature)
    const res = sig.err || !pub ? { ok: false, reason: sig.err ?? '公钥不可用' } : verifyArtifact(pub, sig, buf)
    if (res.ok) ok(`产物 ${k}`, `${name} ${(statSync(path).size / 1048576).toFixed(1)} MiB sha256 ${sha.slice(0, 16)}… 签名通过`)
    else fail(`产物 ${k}`, `${name}:${res.reason}`)
    if (res.ok) checkDarwinBundle(k, path)
    if (opts.local) {
      const p2 = join(resolve(opts.local), 'checksums.txt')
      if (existsSync(p2)) {
        const line = readFileSync(p2, 'utf8').split('\n').find((l) => l.trim().endsWith(name))
        if (line && !line.startsWith(sha)) fail(`checksums.txt ${k}`, `${name} 的 sha256 与 checksums.txt 不一致`)
        else if (line) ok(`checksums.txt ${k}`, '一致')
      }
    }
  }

  // 4) 已装版本不回退(仅线上 latest 有意义)
  checkInstalledVersion(manifest, !opts.local && !opts.tag)

  // 5) checksums.txt 覆盖面(桌面产物是否登记;缺 = 上传方与哈希清单各自为政)
  if (!opts.skipArtifacts && !opts.local) {
    try {
      const p = await downloadAsset(slug, opts.tag, 'checksums.txt', tmp)
      const text = readFileSync(p, 'utf8')
      const missing = Object.values(manifest.platforms).map((e) => basename(new URL(e.url).pathname)).filter((n) => !text.includes(n))
      if (missing.length > 0) info('checksums.txt 覆盖', `桌面产物未登记:${missing.join(', ')}(签名已验证,此项仅提示)`)
      else ok('checksums.txt 覆盖', '桌面产物已登记')
    } catch (e) {
      info('checksums.txt 覆盖', `拉取失败:${e.message}`)
    }
  }
  console.log(`清单来源:${via}`)
} catch (e) {
  if (e.message !== 'manifest 结构不可用' && e.message !== 'no manifest') fail('执行', e.message)
} finally {
  if (!opts.keep) rmSync(tmp, { recursive: true, force: true })
  else console.log(`临时目录保留:${tmp}`)
}

// findLocal 在本地发布目录里按文件名找产物(publish-desktop 按平台分目录)。
function findLocal(dir, name) {
  const root = resolve(dir)
  const stack = [root]
  while (stack.length > 0) {
    const d = stack.pop()
    for (const entry of readdirSyncSafe(d)) {
      const p = join(d, entry)
      try {
        const st = statSync(p)
        if (st.isDirectory() && !entry.startsWith('.')) stack.push(p)
        else if (st.isFile() && entry === name) return p
      } catch {}
    }
  }
  return ''
}

function readdirSyncSafe(d) {
  try {
    return readdirSync(d)
  } catch {
    return []
  }
}

function k_has(manifest, k) {
  return Object.keys(manifest.platforms ?? {}).includes(k)
}

// ---------- 汇总 ----------
const bad = results.filter((r) => r.level === 'FAIL')
for (const r of results) console.log(`[${r.level}] ${r.name}${r.detail ? ` — ${r.detail}` : ''}`)
console.log(`\n${results.filter((r) => r.level === 'OK').length} 通过 / ${bad.length} 失败 / ${results.filter((r) => r.level === 'INFO').length} 提示`)
if (bad.length > 0) {
  console.error('\n校验未通过:上述 FAIL 项会让对应平台更新器静默失效(404/签名不符/降级),修好再发版。')
  process.exit(1)
}
console.log('校验通过:updater 清单与产物一致。')
