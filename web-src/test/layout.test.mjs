// 布局回归护栏(第五十三批;第五十四批打开 CI 严档;第五十五批完善)
//
// 为什么需要浏览器:这类缺陷(2026-09-22 用户实测那次)是**真实布局**的结果 ——
// Sidebar 里 `v-else` 绑错 `v-if` 多渲染了一个 height:100% 的 ☰ 按钮,它按 block 流排在
// 769px 高的 .panel 之后,把文档撑到 1573px。jsdom 之类不算布局(得 0 高度),静态 AST
// 也判不出来(v-else 绑到邻近的另一个 v-if 对 Vue 完全合法),所以只有真渲染才验得了。
//
// 跑法:`cd web-src && npm run test:layout`(需先有 web/dist —— scripts/gen-web.sh)。
// 依赖:本机 Chrome/Chromium;找不到就**跳过**(不拦人)。CI 用 GAH_LAYOUT_REQUIRE=1 变红灯。
//   GAH_LAYOUT_CHROME=/path/to/chrome   指定可执行文件(否则按常见路径 + channel:'chrome' 依次试)
//   GAH_LAYOUT_NO_SANDBOX=1             直接带 --no-sandbox --disable-dev-shm-usage(CI 调试用)
//   GAH_LAYOUT_ARTIFACTS=/dir           失败时把该用例的截图落盘(CI 传 runner.temp 便于排查)
import { after, describe, test } from 'node:test'
import assert from 'node:assert/strict'
import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const WEB_SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const DIST = path.resolve(WEB_SRC, '..', 'web', 'dist')
const REQUIRE = process.env.GAH_LAYOUT_REQUIRE === '1'
const ARTIFACTS = process.env.GAH_LAYOUT_ARTIFACTS || ''

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
  '.woff2': 'font/woff2',
  '.png': 'image/png',
}

// startStatic 起一个只读静态服务(不发包、不落盘;SPA 兜底到 index.html)。
function startStatic(root) {
  const server = http.createServer((req, res) => {
    const rel = decodeURIComponent(req.url.split('?')[0])
    let file = path.join(root, rel)
    if (!file.startsWith(root) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      file = path.join(root, 'index.html')
    }
    const body = fs.readFileSync(file)
    res.writeHead(200, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream' })
    res.end(body)
  })
  return new Promise((ok) => server.listen(0, '127.0.0.1', () => ok(server)))
}

// apiStub 给页面喂最小可用数据(形状取自 src/types.ts):会话列表给 40 条,长列表正是当年把
// 越界放大到 521 个元素的场景。渲染不出内容不影响判定 —— 这里断言的是外壳几何,不是业务数据。
// 文案一律 ASCII:CI 的 ubuntu 镜像只带 fonts-noto-color-emoji(**没有 CJK 字体**),中文会渲成
// 豆腐块 —— 字形宽度差异不是我们要测的东西,别让它污染几何断言。
function makeStub(
  withProviders,
  longTokens = false,
  running = false,
  manyPlugins = false,
  withRoles = false,
  currentRole = 'finance',
  patchDelayMs = 0,
  tierRole = false,
  withMemory = true,
  sseTurns = 0,
  sessionOverride = false,
  withModels = false,
  // mainSessionId 非空时 /api/state 下发 session.id —— 首帧校准与 api 侧会话绑定都靠它。
  // 默认空:很多用例只测渲染,给一个真实 session 会把页签改绑/同步标题的路径也拖进来。
  mainSessionId = '',
) {
  // seen:记录写类请求(方法/路径/体),供角色面板用例断言「面板真的提交了」而不是只改了本地状态。
  const seen = []
  // eventReqs 事件流连接记录(会话 + after 游标):用于断言“切回已开过的会话没有从头重放”
  const eventReqs = []
  // toolQueries:GET /api/tools 的查询串(第九十一批 —— 面板必须读 ?all=1 全量清单;
  // seen 只记写类请求,读类的口径单记一处)。
  const toolQueries = []
  // packPosts:角色包导入的每次尝试(第九十三批 —— 要证“先不带 overwrite,确认后才带”)。
  const packPosts = []
  // memState:记忆治理面板的桩状态(第一百一十批)。内容**故意用无空格长 token** ——
  // 记忆是用户自己写的整句话,是面板横向滚动的高危位置(与技能/角色名同类)。
  const memState = {
    enabled: true,
    // 候选池(记忆层 M2 前置件)。**候选不进上下文** —— 桩里也分开放,免得用例看不出来。
    candidates: [
      '1. [2026-10-02] CAND-ONE-' + 'z'.repeat(120) + ' (来源: 会话 sess-abc123)',
      '2. [2026-10-01] 候选二(无来源)',
    ],
    candidateUsed: 2,
    candidateLimit: 12,
    candidateToday: 2,
    candidateTodayMax: 5,
    user: [
      '1. [2026-10-02] ' + 'MEM-LONG-' + 'y'.repeat(140) + ' (来源: 会话 sess-abc123)',
      '2. [2026-10-01] 第二条记忆(来源: 会话 sess-abc123)',
      '3. [2026-09-30] 手工记的一条(无来源)',
    ],
    project: ['1. [2026-10-02] 本项目的口径约定'],
    lastDeleted: 0,
    writes: [],
  }
  const memView = (st) => ({
    enabled: st.enabled,
    budget: 2048,
    user: st.user,
    project: st.project,
    user_path: '/tmp/gah-home/memory/user.md',
    project_path: '/tmp/gah-home/memory/projects/proj-1.md',
    project_key: 'proj-1',
    candidates: st.candidates,
    candidate_path: '/tmp/gah-home/memory/candidates.md',
    candidate_used: st.candidateUsed,
    candidate_limit: st.candidateLimit,
    candidate_today: st.candidateToday,
    candidate_today_max: st.candidateTodayMax,
  })
  const handler = async (route) => {
    const url = new URL(route.request().url())
    const p = url.pathname
    // 事件流(EventSource):返回一段 text/event-stream body。
    // 浏览器会把 body 里的多个 event **逐个 dispatch**(每个 event 一个任务)—— 这正好
    // 复现「几百个会话帧一次性到达」的真实形状,是批零合帧护栏能立住的前提。
    // turns=0(默认)时不接管,走下面的 JSON 兜底(其余用例看不到会话流,行为不变)。
    if (p === '/api/events') {
      const turns = Number(url.searchParams.get('turns') || 0) || sseTurns
      if (!turns) return json([])
      const now = new Date().toISOString()
      // 遵守 ?after=(真实后端就是差集续传):桩先前无视它、每次都从头重放 ——
      // 于是「切回已开过的会话有没有重新下载一遍」这类断言根本立不住(第一三九批)。
      const after = Number(url.searchParams.get('after') || 0) || 0
      eventReqs.push({ session: url.searchParams.get('session') || '', after })
      const frames = []
      let seq = 1
      for (let i = 0; i < turns; i++) {
        frames.push(`event: session\ndata: ${JSON.stringify({ id: seq, type: 'session', ts: Date.now(), payload: { Kind: 'user/message', Payload: { Content: `u${i}` }, Seq: seq++, TS: now } })}\n\n`)
        frames.push(`event: session\ndata: ${JSON.stringify({ id: seq, type: 'session', ts: Date.now(), payload: { Kind: 'assistant/message', Payload: { Content: `a${i} ` + 'A'.repeat(80), ToolCalls: [] }, Seq: seq++, TS: now } })}\n\n`)
      }
      // baseline 只在“全新连接”(after=0)时发,与真实后端一致(断线续传不描述窗口)
      const tail = frames.filter((f) => {
        const m = /"id":(\d+)/.exec(f)
        return m ? Number(m[1]) > after : true
      })
      const body =
        tail.join('') +
        (after === 0 ? `event: baseline\ndata: ${JSON.stringify({ count: seq - 1, has_more: false, window: seq - 1, from: 1, to: seq - 1 })}\n\n` : '')
      return route.fulfill({ status: 200, contentType: 'text/event-stream; charset=utf-8', headers: { 'Cache-Control': 'no-cache' }, body })
    }
    // patchDelayMs:角色定义的 PATCH 拖一拍 —— 用来观测「在途禁用/串行提交」(默认 0,不影响其它用例)。
    if (patchDelayMs && route.request().method() === 'PATCH') {
      await new Promise((r) => setTimeout(r, patchDelayMs))
    }
    const json = (v, status = 200) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(v) })
    if (route.request().method() !== 'GET') {
      let body = ''
      try {
        body = route.request().postData() ?? ''
      } catch {
        body = ''
      }
      seen.push({ method: route.request().method(), path: p, query: url.search, body })
    }
    if (p === '/api/roles') {
      // 未装配 ctx.roles 的环境:真实后端回 503 文本 → 面板应整段隐藏(导航项也不出现)。
      if (!withRoles) return route.fulfill({ status: 503, contentType: 'text/plain; charset=utf-8', body: 'role service unavailable' })
      // 角色名/技能名故意用无空格长 token:那正是「面板多出一条横向滚动条」的触发器。
      if (route.request().method() === 'POST') return json({ id: 'new-role' })
      return json({
        // currentRole 可注入:面板「默认(基线)」合成行与"当前角色悬空"提示都靠它构造(第八十一批 形态 B)。
        current: currentRole,
        max_agents_bytes: 32768,
        roles: [
          {
            id: 'finance',
            name: 'Finance-Analyst-With-A-Very-Long-Display-Name-0123456789abcdef',
            description: 'Bookkeeping and reporting with a deliberately long ASCII description to stress panel wrapping',
            identity: 'You are a senior finance analyst.',
            exclude_global: true,
            skills_set: false,
            skills: [],
            // 角色携带模型/思考档(第八十六批):行内回显 + 详情编辑区都读这两个字段。
            // 模型名故意长(27 字符无空格 token):它正是“面板被撑出横向滚动条”的触发器。
            model: 'claude-sonnet-4-5-20250929',
            thinking: 'high',
            // 角色收紧档(第九十二批):列表行要回显"这个角色收紧了什么"
            approval: 'strict',
            sandbox: 'read-only',
            agents_bytes: 21,
            seed: true,
          },
          { id: 'assistant', name: 'Assistant', description: '', identity: '', skills_set: true, skills: ['skill-alpha'], agents_bytes: 0 },
        ],
        library: [
          { name: 'skill-alpha', description: 'Alpha skill description with a long ASCII tail 0123456789abcdef' },
          { name: 'skill-with-a-very-long-name-0123456789abcdef', description: 'Another skill' },
          { name: 'private-beta', description: 'Private skill of finance', role: 'finance' },
        ],
      })
    }
    if (p === '/api/tools') {
      // 工具清单(第九十一批):面板的工具勾选读 ?all=1 = **全量**(管理面)——
      // 被角色排除的工具也必须列得出来,否则面板分不清"被排除"与"没装插件"。
      // 故意**不含** ghost-tool(模拟插件卸载后的悬空名):面板要把它显示成「该工具当前不存在」。
      toolQueries.push(url.search)
      return json([
        { name: 'shell', description: 'Run a shell command' },
        { name: 'read_skill', description: 'Read a skill by name' },
        { name: 'list_roles', description: 'List roles' },
        { name: 'tool-with-a-very-long-name-0123456789abcdef', description: 'Long name stress' },
      ])
    }
    if (p.startsWith('/api/roles/') && p.endsWith('/agents')) return json({ ok: true, bytes: 21 })
    if (p.startsWith('/api/roles/')) {
      if (route.request().method() === 'DELETE') return route.fulfill({ status: 200, body: '' })
      if (p.endsWith('/use')) return json({ ok: true, current: p.split('/')[3] })
      if (p.endsWith('/rename')) return json({ id: 'renamed' })
      if (route.request().method() === 'PATCH') {
        // 回包故意夹一个「库里已经没有的技能名」(技能被删/改名后的残留):面板必须把它
        // 显示成「已失效挂载」并能单独移除 —— 否则它既看不见也取消不掉。
        const b = JSON.parse(route.request().postData() || '{}')
        return json({
          id: 'finance',
          name: 'Finance',
          skills_set: true,
          skills: [...(b.skills ?? []), 'stale-skill-removed-0123456789abcdef'],
          // 真实后端回的是整份更新后的 spec:带上了 tools_exclude 才不至于让面板把刚提交的
          // 排除清单从本地状态里"回滚"掉(第九十一批)。
          ...(b.tools_exclude ? { tools_exclude: b.tools_exclude } : {}),
          ...(b.approval !== undefined ? { approval: b.approval } : {}),
          ...(b.sandbox !== undefined ? { sandbox: b.sandbox } : {}),
          agents_bytes: 21,
        })
      }
      // 详情按**请求的 id** 回(两个角色正文不同):草稿保护的用例要靠正文区分"切过去没有"。
      if (p.split('/')[3] === 'assistant') {
        return json({
          id: 'assistant',
          name: 'Assistant',
          identity: '',
          description: '',
          skills_set: true,
          skills: ['skill-alpha'],
          // 工具排除清单(第九十一批):一个真实工具 + 一个**当前不存在**的名字(悬空名)。
          tools_exclude: ['read_skill', 'ghost-tool'],
          // 角色收紧档(第九十二批):详情回显 + 下拉初始选中值
          approval: 'strict',
          sandbox: 'read-only',
          agents: 'ASSISTANT-RULES\n',
          agents_bytes: 17,
        })
      }
      return json({
        id: 'finance',
        name: 'Finance',
        identity: 'You are a senior finance analyst.',
        description: 'Bookkeeping',
        exclude_global: true,
        skills_set: false,
        skills: [],
        // 详情必须回**整份 spec**(真实后端 roleGet 就是这样):思考/模型/收紧档都在里面。
        // 此前这里漏了它们,于是详情表单里 thinking 是空的 —— 点「跟随会话」等于没点,
        // 而旧的「点一下就 PATCH」实现照样发请求,把这个桩保真度的缺口**藏住了**。
        thinking: 'high',
        model: 'claude-sonnet-4-5-20250929',
        agents: 'Always reconcile before reporting.\n'.repeat(3),
        agents_bytes: 96,
      })
    }
    if (p === '/api/instructions') {
      if (route.request().method() === 'PUT') {
        const b = JSON.parse(route.request().postData() || '{}')
        // NO-RELOAD 标记 = 服务端"写盘成功但重载失败":面板必须如实说"未生效",不许报成功。
        if ((b.text ?? '').includes('NO-RELOAD')) {
          return json({ ok: true, bytes: (b.text ?? '').length, applied: false, warning: 'ctx.systemPrompt 未装配(缺 host-system-prompt 插件)' })
        }
        return json({ ok: true, bytes: (b.text ?? '').length, applied: true })
      }
      // 正文用无空格长 ASCII token:全局指令是自由文本,与角色名/技能名同类 ——
      // 面板的横向滚动条高危位置。
      return json({
        path: '/tmp/gah-home/AGENTS.md',
        text: 'GLOBAL-INSTRUCTIONS-' + 'x'.repeat(120) + '\nsecond line\n',
        bytes: 141,
        exists: true,
        max_bytes: 32768,
        over: false,
      })
    }
    if (p === '/api/skills') return json({ name: 'new-skill', path: '/tmp/skills/new-skill/SKILL.md' })
    // 记忆治理面板(第一百一十批):读 + 四个写动作。回执里带 deleted(面板要显示“删了几条”)。
    if (p === '/api/memory') {
      if (!withMemory) return json({ error: '记忆服务未装配(缺 ctx.memory / host-memory 插件)' }, 503)
      if (route.request().method() === 'POST') {
        const b = JSON.parse(route.request().postData() || '{}')
        memState.writes.push(b)
        if (b.action === 'add') memState.user.unshift(`1. [2026-10-02] ${b.content}`)
        if (b.action === 'remove') memState.user.splice((b.index ?? 1) - 1, 1)
        if (b.action === 'remove_source') {
          const before = memState.user.length
          memState.user = memState.user.filter((l) => !l.includes(`会话 ${b.source}`))
          memState.lastDeleted = before - memState.user.length
        }
        if (b.action === 'toggle') memState.enabled = !!b.enabled
        // 候选动作:propose 进候选池;accept 转正进 user;reject 只丢候选
        if (b.action === 'propose') {
          memState.candidates = [`1. [2026-10-02] ${b.content}`, ...memState.candidates]
          memState.candidateUsed = memState.candidates.length
          memState.candidateToday += 1
        }
        if (b.action === 'accept') {
          memState.candidates.splice((b.index ?? 1) - 1, 1)
          memState.user = [`1. [2026-10-02] ACCEPTED-ENTRY`, ...memState.user]
          memState.candidateUsed = memState.candidates.length
        }
        if (b.action === 'reject') memState.candidates.splice((b.index ?? 1) - 1, 1)
        if (b.action === 'accept_all') {
          memState.candidateUsed = 0
          memState.candidates = []
          memState.user = [...memState.user, '1. [2026-10-02] BULK-ACCEPTED']
        }
        if (b.action === 'reject_all') {
          memState.candidateUsed = 0
          memState.candidates = []
        }
        return json({ ...memView(memState), deleted: b.action === 'remove' ? 1 : memState.lastDeleted ?? 0 })
      }
      return json(memView(memState))
    }
    // 技能改名/跨库移动(第八十四批):POST /api/skills/{name}/relocate。
    // 必须放在通用 /api/skills/ 兕底之前(那个会先把多段路径吃掉)。
    if (p.endsWith('/relocate')) {
      const q = new URLSearchParams(p.split('?')[1] || '')
      const b = JSON.parse(route.request().postData() || '{}')
      return json({
        ok: true,
        name: b.to_name || 'skill-alpha',
        role: b.to_role || '',
        from_name: p.split('/')[3],
        from_role: q.get('role') || '',
        // 故意带一条被同步的角色挂载:面板必须把这个副作用说出来(不能假装只是改了名字)
        mounts_updated: ['assistant'],
      })
    }
    if (p.startsWith('/api/skills/')) return json({ name: 'skill-alpha', content: '---\nname: skill-alpha\n---\nbody', path: '/tmp/skills/skill-alpha/SKILL.md' })
    // 回收站(第八十三批):删除的角色/技能都移进 .trash,这里列出并可恢复。
    if (p === '/api/trash') {
      return json({
        roles: [{ name: 'oldrole-20260928-153005', id: 'oldrole', deleted_at: '20260928-153005' }],
        skills: [
          { name: 'shared-gone-20260928-153010', skill: 'shared-gone', role: '', deleted_at: '20260928-153010' },
          { name: 'private-gone-20260928-153015', skill: 'private-gone', role: 'finance', deleted_at: '20260928-153015' },
        ],
      })
    }
    if (p === '/api/trash/restore') return json({ ok: true })
    if (p === '/api/state') {
      return json({
        model: 'layout-guard/model',
        thinking: 'off',
        sandbox: 'full',
        // stats 内层字段不带 json tag(直接用 sdk.UsageStats 字段名)——必须 PascalCase,
        // 写成 snake_case 前端读不到(上下文会显示 '–',桩就与真实契约不一致了)。
        stats: { PromptTokens: 1200, CompletionTokens: 300, CachedTokens: 0, Requests: 3, LastPromptTokens: 1200, Window: 200000 },
        // 会话级覆盖(第一百三十四批)。
        //
        // 桩必须**同时**给两样才对得上真实后端,只给一样正是上一版把 bug 藏住的原因:
        //   session_prefs = 「这一项跟随全局吗」的唯一权威口径(前端只认它);
        //   model_from     = 「生效值是不是角色给的」,没有角色时**恒为 'session'**,
        //                    与「会话有没有压过全局」毫无关系 —— 早期判据读它 ⇒ 全误判。
        // 真实后端这两者是独立的(见 sdk.EffectiveModel 与 web/server.go 的 SessionPrefs)。
        model_from: 'session',
        ...(sessionOverride ? { session_prefs: { model: true, sandbox: true } } : {}),
        // 角色徽标(第七十九批):状态栏多一个 "角色 <名>" 项 —— 长角色名不得把底栏挤变形。
        // 生效值来源(第八十六批):withRoles → finance 已声明模型/思考档,
        // 于是 model/thinking 应是**角色值**(与真实后端同一判据:生效值 + 会话原值都给)。
        ...(withRoles
          ? {
              role: 'finance',
              role_name: 'Finance-Analyst-With-A-Very-Long-Display-Name-0123456789abcdef',
              model: 'claude-sonnet-4-5-20250929',
              model_from: 'role',
              model_session: 'layout-guard/model',
              thinking: 'high',
              thinking_from: 'role',
              thinking_session: 'off',
              // 角色收紧后的**有效**档与来源(第九十二批):面板据此说"实际按哪档裁决"。
              // 只在 tierRole 用例下发 —— 底栏沙箱标签会因此多一段"(角色收紧)",
              // 默认下发会平白改动其它用例的渲染文案。
              ...(tierRole ? { approval: 'smart', approval_effective: 'strict', approval_from: 'role', sandbox: 'workspace-write', sandbox_effective: 'read-only', sandbox_from: 'role' } : {}),
            }
          : {}),
        running,
        // session.id 必须**回显请求的 ?session=**(真实后端如此:每个页签拉的是**那个会话**的快照)。
        // 桩先前恒返回 main ⇒ 前端每次打开非主会话页签都会判定「服务端当前会话被切走了」
        // → rebuild(false) 清空重放(第一三九批写用例时踩到:明明有缓存,流还是空的)。
        ...(mainSessionId
          ? { session: { id: url.searchParams.get('session') || mainSessionId, name: '主会话', path: '/tmp/' + (url.searchParams.get('session') || mainSessionId) + '.jsonl', key: 'k-' + (url.searchParams.get('session') || mainSessionId) } }
          : {}),
        version: 'layout-guard',
      })
    }
    if (p === '/api/sessions') {
      const now = Date.now()
      return json(
        Array.from({ length: 40 }, (_, i) => ({
          ID: i === 0 ? '' : `layout-${i}`,
          Path: `/tmp/layout-${i}.jsonl`,
          Name: i === 0 ? 'main' : `session ${i}`,
          Preview: `Session ${i} preview text, long enough to wrap into several lines. `.repeat(3),
          MTime: Math.floor((now - i * 3600_000) / 1000),
          Frames: 100 + i,
        })),
      )
    }
    if (p === '/api/notices') return json({ items: [], max_id: 0 })
    if (p === '/api/session/events') return json({ events: [], from: 0, to: 0, count: 0, has_more: false })
    // provider 数为 0 时首屏会自动弹设置面板(App.vue maybeOnboard)—— 那也是要覆盖的真实状态,
    // 所以不是一律给 1 个(见「首启态」用例)。
    if (p === '/api/providers') {
      if (!withProviders) return json([])
      // longTokens:provider 名与插件 ID 都是「用户/打包决定、可能出现无空格长 token」的字段。
      const pname = longTokens ? 'provider-name-without-any-break-0123456789abcdef' : 'layout'
      return json([{ Name: pname, BaseURL: 'http://127.0.0.1:1/v1', APIKey: 'sk-layout', Model: 'layout-guard/model', Active: true }])
    }
    if (p === '/api/plugins') {
      // manyPlugins:43 条正是真机里把「关于 gah / 检查更新」顶到面板最底的那个长度(插件段收纳的用例源)
      if (manyPlugins) {
        return json(
          Array.from({ length: 43 }, (_, i) => ({
            ID: `host-plugin-${String(i).padStart(2, '0')}`,
            Type: 'host',
            State: i < 3 ? 'loaded' : 'idle',
            manage: 'web',
          })),
        )
      }
      if (longTokens) {
        return json([{ ID: 'host-plugin-with-a-very-long-identifier-0123456789abcdef', Type: 'host', State: 'loaded', manage: 'external' }])
      }
      return json([])
    }
    if (p === '/api/models') {
      if (!withModels) return json({ providers: [] })
      // 本会话模型选择器需要「当前 provider 有模型」这个真实形状:
      // 一个普通模型 + 一个免费模型(免费徽标分支)。可用性一律由 Verdict 给(后端算好)。
      const pname = longTokens ? 'provider-name-without-any-break-0123456789abcdef' : 'layout'
      return json({
        providers: [
          {
            Name: pname,
            Models: [
              {
                ID: 'layout-guard/model',
                Verdict: { Free: false, ToolsKnown: true, Tools: true, Vision: false, ContextWindow: 200000, Usable: true, Tags: ['工具'], Warn: '' },
              },
              {
                ID: 'layout-guard/other-model',
                Verdict: { Free: true, ToolsKnown: true, Tools: true, Vision: false, ContextWindow: 128000, Usable: true, Tags: ['免费'], Warn: '' },
              },
            ],
          },
        ],
      })
    }
    if (p === '/api/mcp') {
      if (!longTokens) return json({ path: '', servers: [], reload_available: false, plugin_loaded: false })
      // 长 token 场景照搬真机形状:Windows 配置路径与 MCP 启动命令都是一整段无空格文本,正是
      // 「设置面板多出一条横向滚动条」的触发器(见下方专门用例)。
      return json({
        path: 'C:\\Users\\nekole\\AppData\\Local\\dev.gah.desktop\\bin\\gah-data\\config\\mcp.json',
        servers: [
          {
            name: 'deja',
            command: 'npx -y @modelcontextprotocol/server-memory --registry=https://registry.npmmirror.com',
            enabled: true,
            mode: 'direct',
          },
        ],
        reload_available: true,
        plugin_loaded: true,
      })
    }
    if (p === '/api/schedules' && longTokens) {
      return json([
        {
          id: 's1',
          name: '每日对账',
          cron: '0 8 * * *',
          prompt: 'x',
          enabled: true,
          next_run: 1_900_000_000,
          next_runs: [1_900_000_000, 1_900_086_400, 1_900_172_800],
          // 中文排期描述(界面显示的是它,不再显示裸 cron):刻意给长文本 ——
          // 它取代了原本那条 mono 表达式,布局护栏必须跟着盯它。
          cron_label: '每个工作日 08:00(以及非常长的中文补充说明用来撑宽度检测)',
          last_run_at: 1_900_000_000,
          last_status: 'failed',
          last_error: 'connect ECONNREFUSED 127.0.0.1:11434',
        },
      ])
    }
    if (p === '/api/input') {
      // 运行中的提交回 accepted=steer(宿主把消息注入当前回合);否则 turn(开新回合)。
      return json({ ok: true, accepted: running ? 'steer' : 'turn' })
    }
    // 角色包(第九十三批):导出 = 下载(zip 字节),导入 = multipart。
    // 导入**第一次一律回"已存在"** —— 面板必须先问用户再带 overwrite 重试(不静默覆盖)。
    if (p === '/api/rolepack') {
      packPosts.push({ as: url.searchParams.get('as'), overwrite: url.searchParams.get('overwrite') })
      if (url.searchParams.get('overwrite') !== '1') {
        return route.fulfill({ status: 400, contentType: 'text/plain; charset=utf-8', body: '角色 finance 已存在:要覆盖请加 force' })
      }
      return json({
        ok: true,
        result: { id: url.searchParams.get('as') || 'finance', name: 'Finance', agents_bytes: 21, skills: ['tax'], replaced: true, backup_name: 'finance-20260929-000000' },
      })
    }
    if (p.startsWith('/api/rolepack/')) {
      return route.fulfill({ status: 200, contentType: 'application/zip', body: 'PK\x03\x04stub' })
    }
    // 桌面壳导出:面板挑完目录后跑 /role export(服务端写文件)
    if (p === '/api/commands/role') return json({ output: '已导出角色 Finance(finance)→ /tmp/gah-role-finance.zip' })
    if (p === '/api/doc/tree') return json({ entries: [] })
    return json([])
  }
  handler.seen = seen
  handler.eventReqs = eventReqs
  handler.toolQueries = toolQueries
  handler.packPosts = packPosts
  handler.memState = memState
  return handler
}
const apiStub = makeStub(true)

// launchOpts args 非空时一并带上(重试路径用)。依次尝试:显式路径 → 系统 Chrome → 常见 Linux 路径
// → 交给 Playwright 的 channel 解析(它自己的表就是 linux `/opt/google/chrome/chrome`、
// darwin `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,与 CI 镜像给的位置一致)。
function launchOpts(args) {
  const cands = [
    process.env.GAH_LAYOUT_CHROME,
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/usr/bin/google-chrome',
    '/usr/bin/google-chrome-stable',
    '/opt/google/chrome/chrome',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
  ].filter(Boolean)
  const found = cands.find((c) => fs.existsSync(c))
  const opts = found ? { executablePath: found } : { channel: 'chrome' }
  if (args.length) opts.args = args
  return opts
}

// UBUNTU_SANDBOX_ARGS:Ubuntu 23.10+ 用 AppArmor 限制非特权 user namespace,Chrome 内建沙箱起不来
// (报 "No usable sandbox!")。CI 的 ubuntu-latest 就是 24.04 ⇒ 首次失败后带这两个参数重试一次
// (仅测试期的自渲染,风险可忽略);GAH_LAYOUT_NO_SANDBOX=1 可直接走这条参数。
const UBUNTU_SANDBOX_ARGS = ['--no-sandbox', '--disable-dev-shm-usage']

// shouldRetryWithoutSandbox 只对「沙箱 / user namespace」类失败重试 —— 其余错误(浏览器没装、
// 路径不对、版本不匹配)必须原样暴露,不能被重试掩盖。
function shouldRetryWithoutSandbox(msg) {
  return /sandbox|namespace/i.test(String(msg))
}

// launchBrowser 先按标准参数启动;若失败且形如沙箱问题,再带 --no-sandbox 重试一次。
async function launchBrowser(chromium) {
  const forced = process.env.GAH_LAYOUT_NO_SANDBOX === '1'
  try {
    const browser = await chromium.launch(launchOpts(forced ? UBUNTU_SANDBOX_ARGS : []))
    return { browser, how: forced ? 'GAH_LAYOUT_NO_SANDBOX=1 指定 --no-sandbox' : '标准参数' }
  } catch (e) {
    if (forced || !shouldRetryWithoutSandbox(e.message)) throw e
    console.log(`  首次启动失败(疑似沙箱限制),带 --no-sandbox 重试:${String(e.message).split('\n')[0]}`)
    return { browser: await chromium.launch(launchOpts(UBUNTU_SANDBOX_ARGS)), how: '--no-sandbox 重试' }
  }
}

// 共享上下文:一个浏览器一个静态服务,跑完统一收摊。
// 前置条件缺失(没产物/没浏览器/没 playwright-core)默认**跳过**(不拦人);
// GAH_LAYOUT_REQUIRE=1 时改成硬失败 —— CI 想要"绝不静默通过"就设它。
let server = null
let browser = null
let skip = false
let skipWhy = ''

function unavailable(why) {
  skip = true
  skipWhy = why
  if (REQUIRE) {
    throw new Error(
      `GAH_LAYOUT_REQUIRE=1 但布局护栏跑不了:${why}\n` +
        '  runner 上装 Chrome 或设 GAH_LAYOUT_CHROME=/path/to/chrome;确属环境不可用再撤掉 CI 那步的 GAH_LAYOUT_REQUIRE。',
    )
  }
}

const ready = (async () => {
  if (!fs.existsSync(path.join(DIST, 'index.html'))) {
    return unavailable(`缺少 ${DIST}/index.html:先 bash scripts/gen-web.sh 再跑本护栏`)
  }
  const { chromium } = await import('playwright-core').catch((e) => {
    unavailable(`未安装 playwright-core(${e.message})`)
    return {}
  })
  if (!chromium) return
  try {
    const r = await launchBrowser(chromium)
    browser = r.browser
    console.log(`  布局护栏浏览器:${browser.version()}(${r.how})`)
  } catch (e) {
    // 本机没 Chrome:跳过(不拦人);CI 用 GAH_LAYOUT_REQUIRE=1 把它变成失败。
    return unavailable(`未找到可用 Chrome/Chromium(${String(e.message).split('\n')[0]})`)
  }
  server = await startStatic(DIST)
})()
await ready

const baseURL = () => `http://127.0.0.1:${server.address().port}/`

after(async () => {
  if (browser) await browser.close()
  if (server) await server.close()
})

// shoot 失败取证:把该用例当帧截图落盘(仅在 GAH_LAYOUT_ARTIFACTS 指定时;CI 传 runner.temp)。
// settle:轮询到条件成立为止(上限 timeoutMs),替代写死的 waitForTimeout。
// 起因(第九十一批 CI 修正):「段落导航对齐」用例在 macos runner 上以 delta 74.25px 失败 ——
// 平滑滚动在负载高的 runner 上 400ms 内没滚完,而断言本身(段顶对齐内容区顶)是对的。
// 纪律:布局用例不得依赖机器速度 —— 断言"最终状态",别断言"某个时刻的状态"。
async function settle(page, fn, { timeoutMs = 3000, stepMs = 50 } = {}) {
  const deadline = Date.now() + timeoutMs
  let last
  for (;;) {
    last = await page.evaluate(fn)
    if (last?.ok) return last
    if (Date.now() > deadline) return last
    await page.waitForTimeout(stepMs)
  }
}

async function shoot(page, name) {
  if (!ARTIFACTS || !page) return
  try {
    fs.mkdirSync(ARTIFACTS, { recursive: true })
    const file = path.join(ARTIFACTS, name.replace(/[^\w.\u4e00-\u9fa5-]+/g, '_') + '.png')
    await page.screenshot({ path: file })
    console.log(`  失败快照:${file}`)
  } catch (e) {
    console.log(`  失败快照写入失败:${String(e.message).slice(0, 120)}`)
  }
}

// waitSkeleton 等骨架 DOM 出现(不再靠固定 sleep):侧栏两种形态之一 + 输入区 + 状态栏。
// 之后给一帧时间让样式/字体落定。
async function waitSkeleton(page) {
  await page.waitForFunction(
    () =>
      !!document.querySelector('.sidebar') &&
      (!!document.querySelector('.sidebar .panel') || !!document.querySelector('.sidebar .handle')) &&
      !!document.querySelector('.input-slot') &&
      !!document.querySelector('.statusbar-slot'),
    null,
    { timeout: 10_000 },
  )
  await page.waitForTimeout(250)
}

// open 统一开页:注布局偏好 → 桩 API → 加载 → 等骨架稳定。
async function open(ctx, stub, dock) {
  await ctx.addInitScript(
    ([k, v]) => {
      window.localStorage.setItem(k, v)
      window.sessionStorage.setItem('gah.onboard.auto', '1') // 首屏引导标记:默认不自动弹设置面板
    },
    ['gah.dock', JSON.stringify(dock)],
  )
  const page = await ctx.newPage()
  await page.route('**/api/**', stub)
  await page.goto(baseURL(), { waitUntil: 'load' })
  await waitSkeleton(page)
  return page
}

// measure 在页面里量三个不变量(与人工验收用的探针同一套判定):
//   ① 文档不被撑高(外壳永不滚:html/body overflow:hidden + 无越界子元素)
//   ② 关键骨架在视口内(输入区/状态栏在场且不越界)
//   ③ 没有"无裁剪祖先"的元素越出视口(裁剪=内容被切掉,同样是缺陷)
async function measure(page) {
  return page.evaluate(() => {
    const se = document.scrollingElement
    window.scrollTo(0, 500)
    const scrolled = se.scrollTop
    window.scrollTo(500, 0)
    const scrolledX = se.scrollLeft
    window.scrollTo(0, 0)
    const vh = window.innerHeight
    const vw = window.innerWidth
    // clip:该元素上方有没有裁切柜(垂直或水平任一侧都算 —— 只要有一侧不 visible,
    // 另一侧在 CSS 上也会计算成 auto)。走链到 body 为止:html/body 的 overflow:hidden
    // **不算裁切柜**,否则 2026-09-22 那种"被 body 裁掉但实际越界"的元素就漏检了。
    const HIDES = ['auto', 'scroll', 'hidden']
    const clip = (el) => {
      for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
        const cs = getComputedStyle(a)
        if (HIDES.includes(cs.overflowY) || HIDES.includes(cs.overflowX)) return true
      }
      return false
    }
    const name = (el) => {
      const c = typeof el.className === 'string' ? el.className.trim().split(/\s+/).slice(0, 2).join('.') : ''
      return el.tagName.toLowerCase() + (c ? '.' + c : '')
    }
    const outside = []
    for (const el of document.querySelectorAll('body *')) {
      const bb = el.getBoundingClientRect()
      if (bb.width === 0 && bb.height === 0) continue
      const offV = bb.bottom > vh + 1 || bb.top < -1
      const offH = bb.right > vw + 1 || bb.left < -1
      if ((offV || offH) && !clip(el)) {
        outside.push({
          el: name(el),
          axis: offV && offH ? 'vh' : offV ? 'v' : 'h',
          top: Math.round(bb.top),
          bottom: Math.round(bb.bottom),
          left: Math.round(bb.left),
          right: Math.round(bb.right),
          h: Math.round(bb.height),
        })
      }
    }
    const box = (sel) => {
      const el = document.querySelector(sel)
      if (!el) return null
      const bb = el.getBoundingClientRect()
      return { top: Math.round(bb.top), bottom: Math.round(bb.bottom), inside: bb.bottom <= vh + 1 && bb.top >= -1 }
    }
    return {
      scrollHeight: se.scrollHeight,
      clientHeight: se.clientHeight,
      scrollWidth: se.scrollWidth,
      clientWidth: se.clientWidth,
      scrolled,
      scrolledX,
      innerHeight: vh,
      innerWidth: vw,
      outside: outside.slice(0, 6),
      outsideTotal: outside.length,
      panels: document.querySelectorAll('.sidebar .panel').length,
      handles: document.querySelectorAll('.sidebar .handle').length,
      hasSidebar: !!document.querySelector('.sidebar'),
      hasSettings: !!document.querySelector('[aria-label="设置"]'),
      composer: box('.input-slot'),
      statusbar: box('.statusbar-slot'),
      tabbar: box('.tabbar'),
    }
  })
}

function assertInvariants(m) {
  assert.ok(
    m.scrollHeight <= m.clientHeight + 1,
    `文档被撑高(整页可滚):scrollHeight=${m.scrollHeight} > clientHeight=${m.clientHeight};越界元素=${JSON.stringify(m.outside)}`,
  )
  assert.equal(m.scrolled, 0, `页面能竖向滚动(scrollTo 生效),越界元素=${JSON.stringify(m.outside)}`)
  assert.ok(
    m.scrollWidth <= m.clientWidth + 1,
    `文档被撑宽(能横向滚):scrollWidth=${m.scrollWidth} > clientWidth=${m.clientWidth};越界元素=${JSON.stringify(m.outside)}`,
  )
  assert.equal(m.scrolledX, 0, `页面能横向滚动,越界元素=${JSON.stringify(m.outside)}`)
  assert.equal(m.outsideTotal, 0, `有元素越出视口且未被裁剪:${JSON.stringify(m.outside)}`)
  assert.ok(m.hasSidebar, '侧栏未渲染')
  assert.ok(m.composer && m.composer.inside, `输入区不在视口内:${JSON.stringify(m.composer)}`)
  assert.ok(m.statusbar && m.statusbar.inside, `状态栏不在视口内:${JSON.stringify(m.statusbar)}`)
}

const viewports = [
  { w: 1200, h: 800 },
  { w: 1440, h: 1000 },
  { w: 1000, h: 620 },
  { w: 820, h: 560 },
  { w: 700, h: 460 },
]
const docks = [
  { tag: '停靠收起', dock: { open: false, panel: 'changes', width: 392 } },
  { tag: '停靠-变更', dock: { open: true, panel: 'changes', width: 392 } },
  { tag: '停靠-看板', dock: { open: true, panel: 'board', width: 392 } },
  { tag: '停靠-任务', dock: { open: true, panel: 'jobs', width: 392 } },
]

describe('布局护栏:整页永不滚动(第五十二/五十三批)', { skip: skip && skipWhy }, () => {
  for (const vp of viewports) {
    for (const d of docks) {
      test(`${vp.w}x${vp.h} ${d.tag}`, async (t) => {
        const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
        let page = null
        try {
          page = await open(ctx, apiStub, d.dock)
          assertInvariants(await measure(page))
        } catch (e) {
          await shoot(page, t.name)
          throw e
        } finally {
          await ctx.close()
        }
      })
    }
  }

  // 设置面板内部不许出现横向滚动条(第六十三批真机反馈)。
  // 为何整页护栏查不到:.body 的 overflow-y:auto 会把 overflow-x 也算成 auto ⇒ 面板里任何一处
  // 不换行的长 token(Windows 配置路径、MCP 启动命令、计划报错)都会在面板底部多出一条横向滚动条;
  // 而 measure() 的 clip() 把带 auto/hidden 的祖先当裁切柜 —— .body 恰好就是。只能单测面板内部。
  for (const vp of [{ w: 1200, h: 800 }, { w: 820, h: 560 }]) {
    test(`${vp.w}x${vp.h} 设置面板内不出现横向滚动条(长路径/长命令)`, async (t) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
      let page = null
      try {
        page = await open(ctx, makeStub(true, true), docks[1].dock)
        await page.click('.gear')
        await page.waitForSelector('[aria-label="设置"]')
        await page.waitForTimeout(300)
        const m = await page.evaluate(() => {
          const panel = document.querySelector('[aria-label="设置"]')
          const body = panel.querySelector('.body')
          const br = body.getBoundingClientRect()
          const nm = (el) =>
            el.tagName.toLowerCase() +
            (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : '')
          const off = []
          // 扫的是 .body 的后代:左导航是它的兄弟节点(在 body 左侧),拿 panel 当基准会把导航本身当成越界。
          for (const el of body.querySelectorAll('*')) {
            const bb = el.getBoundingClientRect()
            if (bb.width === 0 && bb.height === 0) continue
            if (bb.right > br.right + 1 || bb.left < br.left - 1) off.push(nm(el))
          }
          return { scrollW: body.scrollWidth, clientW: body.clientWidth, off: off.slice(0, 6), panelScrollW: panel.scrollWidth, panelClientW: panel.clientWidth }
        })
        assert.ok(
          m.scrollW <= m.clientW + 1,
          `设置面板被撑出横向滚动条:scrollWidth=${m.scrollW} > clientWidth=${m.clientW};越界元素=${JSON.stringify(m.off)}`,
        )
        assert.ok(m.panelScrollW <= m.panelClientW + 1, `设置面板外壳被撑宽:${m.panelScrollW} > ${m.panelClientW}`)
      } catch (e) {
        await shoot(page, t.name)
        throw e
      } finally {
        await ctx.close()
      }
    })
  }

  // 设置面板的分段导航:插件列表 43 条曾把「关于 gah / 检查更新」顶到必须长滚的位置。
  // 这条验的是导航真的能一下到达 + 高亮跟着走(修的是「要滚到底」,不是换个写法)。
  test('设置面板:段导航点击即到达,高亮跟随滚动', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, true, false, true), docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[aria-label="设置"] .nav-it')
      // 先展开到 43 条:真机里把面板拉成长滚动的就是它 —— 导航要在那种长度下仍然一跳到顶。
      await page.click('[data-sec="plugin"] .more')
      await page.click('.nav-it:has-text("数据备份")')
      // 轮询到"滚完且对齐"为止(写死 sleep 会在负载高的 runner 上假红,见 settle 注释)
      const m = await settle(page, () => {
        const panel = document.querySelector('[aria-label="设置"]')
        const body = panel.querySelector('.body')
        const sec = body.querySelector('section[data-sec="backup"]')
        const on = panel.querySelector('.nav-it.on')?.textContent?.trim() ?? ''
        const delta = Math.abs(sec.getBoundingClientRect().top - body.getBoundingClientRect().top)
        return { ok: on === '数据备份' && delta <= 40, scrollTop: body.scrollTop, delta, on }
      })
      assert.ok(m.scrollTop > 0, '点「数据备份」后内容区没有滚动')
      assert.ok(m.delta <= 40, `「数据备份」段未对齐到内容区顶部:偏差 ${m.delta}px`)
      assert.equal(m.on, '数据备份', `高亮没落在「数据备份」上,而是「${m.on}」`)
      // 末段(插件)在内容已被展开后仍可能顶不到上沿:高亮仍必须落在它身上(滚到底特判)
      await page.click('.nav-it:has-text("插件")')
      const lastOn = (
        await settle(page, () => {
          const on = document.querySelector('[aria-label="设置"] .nav-it.on')?.textContent?.trim() ?? ''
          return { ok: on === '插件', on }
        })
      ).on
      assert.equal(lastOn, '插件', `滚到底后高亮应留在「插件」上,实际「${lastOn}」`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 插件段收纳:≤ 5 条全列,> 5 条只列前 5 + 「展开全部」;筛选命中少时不需要折叠。
  test('设置面板:插件列表默认只列 5 条,展开后全列、筛选后自动收窄', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, true, false, true), docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[aria-label="设置"] [data-sec="plugin"] .prow')
      const read = () =>
        page.evaluate(() => {
          const sec = document.querySelector('[aria-label="设置"] [data-sec="plugin"]')
          return { rows: sec.querySelectorAll('.prow').length, more: sec.querySelector('.more')?.textContent?.trim() ?? '' }
        })
      const before = await read()
      assert.equal(before.rows, 5, `默认应只列 5 条,实际 ${before.rows} 条`)
      assert.match(before.more, /还有 38 个/, `折叠按钮文案不对:「${before.more}」`)

      await page.click('[data-sec="plugin"] .more')
      const expanded = await read()
      assert.equal(expanded.rows, 43, `展开后应列全部 43 条,实际 ${expanded.rows} 条`)
      assert.equal(expanded.more, '收起', `展开后的按钮应为「收起」:「${expanded.more}」`)

      // 选择器收紧到筛选框本身(该段新增了「安装插件」的输入框,`input` 已不唯一)。
      await page.fill('[data-sec="plugin"] [data-testid="plugin-filter"]', 'host-plugin-42')
      const filtered = await read()
      assert.equal(filtered.rows, 1, `筛选后应只剩 1 条,实际 ${filtered.rows} 条`)
      assert.equal(filtered.more, '', '命中 ≤ 5 条时不应再出现折叠按钮')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 浮层显隐动效(第七十七批):抽屉/停靠区不再是「啪」一下出现与消失。动效本身没法靠截图断言,
  // 这里钉它的两个可观测不变量 —— ① 退场那一瞬过渡确实挂在元素上(时长 > 0、透明度已在变);
  // ② 元素在退场期间仍留在 DOM(异步退场),过渡结束后必须清干净。删掉动效退回硬切,这条必红。
  // 角色面板(第七十九批 1b):装配态可展开编辑、挂载技能即时提交、删除走全局二次确认。
  // 这里钉的是**面板真的提交了** —— 只改本地状态的假保存(刷新即回)正是这类面板最容易犯的错。
  test('设置面板:角色段可展开编辑、挂载即提交、删除进二次确认', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      const rows = await page.evaluate(() =>
        Array.from(document.querySelectorAll('[data-sec="role"] .prow')).map((r) => r.textContent ?? ''),
      )
      // 行数 = 真实角色 2 条 + 顶部合成的「默认（基线）」1 条(形态 B:不落盘,纯展示层)
      assert.equal(rows.length, 3, `角色行数不对:${rows.length}`)
      assert.ok(rows[0].includes('默认（基线）'), `顶部应是合成的基线行:${rows[0]}`)
      assert.ok(!rows[0].includes('当前'), '当前是 finance,基线行不该标「当前」')
      assert.ok(rows[0].includes('切换'), '基线不是当前态时应能一键切回')
      assert.ok(rows[1].includes('当前'), '当前角色未标注「当前」')
      assert.ok(rows[2].includes('切换'), '非当前角色应有「切换」按钮')
      // 段导航能到达(与其它段同一套 key→DOM 机制)
      await page.click('.nav-it:has-text("角色")')
      // 平滑滚动需要时间(机器快慢不可控)⇒ 轮询到"已对齐"为止;超时后照样拿最终值去断言
      const nav = await settle(page, () => {
        const panel = document.querySelector('[aria-label="设置"]')
        const body = panel.querySelector('.body')
        const sec = body.querySelector('section[data-sec="role"]')
        const on = panel.querySelector('.nav-it.on')?.textContent?.trim() ?? ''
        const delta = Math.abs(sec.getBoundingClientRect().top - body.getBoundingClientRect().top)
        return { ok: on === '角色' && delta <= 40, on, delta }
      })
      assert.equal(nav.on, '角色', `高亮没落在「角色」上,而是「${nav.on}」`)
      assert.ok(nav.delta <= 40, `「角色」段未对齐到内容区顶部:偏差 ${nav.delta}px`)

      // 切换另一个角色 → POST /use
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("切换")')
      await page.waitForTimeout(200)
      assert.ok(
        stub.seen.some((r) => r.method === 'POST' && r.path === '/api/roles/assistant/use'),
        `切换未提交到后端:${JSON.stringify(stub.seen)}`,
      )

      // 展开编辑:拉详情(正文只在展开时拉)+ 字节计数器按上限显示
      await page.click('[data-sec="role"] .prow:has-text("Finance-Analyst") button:has-text("编辑")')
      await page.waitForSelector('[data-sec="role"] textarea')
      const detail = await page.evaluate(() => {
        const sec = document.querySelector('[data-sec="role"]')
        const l = Array.from(sec.querySelectorAll('.fld-lab')).find((x) => (x.textContent ?? '').includes('/ 32768'))
        return { lab: l?.textContent?.trim() ?? '', ta: sec.querySelector('textarea')?.value?.length ?? 0 }
      })
      assert.match(detail.lab, /\/ 32768/, `工作规则未显示字节上限:「${detail.lab}」`)
      assert.ok(detail.ta > 0, '展开后未回填 AGENTS.md 正文')

      // 默认池 → 勾一个技能 = 切「替换」并把 skills 一起提交(只改本地会刷新即回)
      await page.click('[data-sec="role"] .m-list .m-item input[type=checkbox]')
      await page.waitForTimeout(200)
      const patch = stub.seen.find((r) => r.method === 'PATCH' && r.path === '/api/roles/finance')
      assert.ok(patch, `挂载未提交 PATCH:${JSON.stringify(stub.seen)}`)
      const pbody = JSON.parse(patch.body)
      assert.equal(pbody.skills_set, true, `挂载应带 skills_set=true:${patch.body}`)
      assert.ok(pbody.skills.includes('skill-alpha'), `挂载应带上被勾的技能:${patch.body}`)

      // 失效挂载(库中已不存在的技能名)必须可见、可单独移除:后端对存量悬空名放行,
      // 所以「移除」是一次正常的 PATCH,摘掉那一个名字、其余挂载不动。
      await page.waitForSelector('[data-sec="role"] .m-item:has-text("stale-skill-removed-0123456789abcdef")')
      await page.click(
        '[data-sec="role"] .m-item:has-text("stale-skill-removed-0123456789abcdef") button:has-text("移除")',
      )
      await page.waitForTimeout(200)
      const patches = stub.seen.filter((r) => r.method === 'PATCH' && r.path === '/api/roles/finance')
      const last = JSON.parse(patches[patches.length - 1].body)
      assert.ok(
        !last.skills.includes('stale-skill-removed-0123456789abcdef'),
        `移除未提交:${JSON.stringify(last)}`,
      )
      assert.ok(last.skills.includes('skill-alpha'), `移除不应动其它挂载:${JSON.stringify(last)}`)

      // 删除 = 有副作用 → 必须二次确认;取消后不得发请求
      const before = stub.seen.filter((r) => r.method === 'DELETE').length
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("删除")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(stub.seen.filter((r) => r.method === 'DELETE').length, before, '取消确认后仍发出了 DELETE')
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("删除")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(200)
      assert.ok(
        stub.seen.some((r) => r.method === 'DELETE' && r.path === '/api/roles/assistant'),
        `确认后未删除:${JSON.stringify(stub.seen)}`,
      )
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 角色携带模型/思考档(第八十六批):面板要如实区分“生效值”与“会话档”——
  // 角色声明了就标「角色指定」并在设置区说清“这里切换要停用角色后才生效”(静默失效比不做更糟)。
  test('设置面板:角色指定模型/思考档时回显生效值并标出来源', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      await page.waitForTimeout(200)

      // 行内回显(不必展开就能看出这个角色自己带模型)
      const rows = await page.evaluate(() =>
        Array.from(document.querySelectorAll('[data-sec="role"] .prow')).map((r) => r.textContent ?? ''),
      )
      const financeRow = rows.find((r) => r.includes('Finance-Analyst')) ?? ''
      assert.ok(financeRow.includes('claude-sonnet-4-5-20250929'), `角色行未回显模型:${financeRow}`)
      assert.ok(financeRow.includes('思考'), `角色行未回显思考档:${financeRow}`)

      // 「当前生效」行:值 = 角色模型,且带「角色指定」标 + 会话档说明
      const cur = await page.evaluate(() => document.querySelector('[data-testid="cur-model"]')?.textContent ?? '')
      assert.ok(cur.includes('claude-sonnet-4-5-20250929'), `当前生效未显示角色模型:${cur}`)
      assert.ok(cur.includes('角色指定'), `未标出模型来源是角色:${cur}`)
      assert.ok(cur.includes('layout-guard/model'), `未说清被覆盖的会话档:${cur}`)

      // 模型区与推理区都必须有“覆盖”告知(用户点了没反应才是真问题)
      assert.ok(await page.isVisible('[data-testid="model-role-note"]'), '模型区缺少角色覆盖说明')
      const note = await page.textContent('[data-testid="thinking-role-note"]')
      assert.ok(note && note.includes('高'), `思考覆盖提示未说明实际生效档:${note}`)

      // 展开编辑:思考档可选「跟随会话」。
      // 2026-10-04 起**只改本地草稿**,点「保存定义」才写盘(与同区文本框一致;
      // 此前点一下就 PATCH,两套语义并排)。故这里要多点一次保存。
      await page.click('[data-sec="role"] .prow:has-text("Finance-Analyst") button:has-text("编辑")')
      await page.waitForSelector('[data-sec="role"] textarea')
      const finPatches = () => stub.seen.filter((r) => r.method === 'PATCH' && r.path === '/api/roles/finance')
      await page.click('[data-sec="role"] .role-detail .seg-it:has-text("跟随会话")')
      await page.waitForTimeout(200)
      assert.equal(finPatches().length, 0, `选中不该写盘,却有:${JSON.stringify(finPatches())}`)
      await page.click('[data-sec="role"] .acts button:has-text("保存定义")')
      await page.waitForTimeout(300)
      const patches = finPatches()
      assert.ok(patches.length > 0, `点保存定义后应提交:${JSON.stringify(stub.seen)}`)
      assert.equal(JSON.parse(patches[patches.length - 1].body).thinking, '', `未清掉角色思考档:${patches[patches.length - 1].body}`)

      // 新增的回显行不得把面板撑出横向滚动条(长模型名是无空格 token)
      const m = await page.evaluate(() => {
        const body = document.querySelector('[aria-label="设置"] .body')
        return { scrollW: body.scrollWidth, clientW: body.clientWidth }
      })
      assert.ok(m.scrollW <= m.clientW + 1, `新回显行撑出横向滚动条:${m.scrollW} > ${m.clientW}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 回收站(第八十三批):删除的角色/技能移进 .trash 后**有入口恢复** —— 此前面板文案写着
  // "可恢复"却没有列表也没有动作。这里钉:展开才拉 /api/trash、两组条目都渲染、恢复走
  // 二次确认且取消不发请求、确认后 POST 用**回收站目录名**(不是原 ID/技能名)定位。
  test('设置面板:回收站列出已删角色/技能并可按目录名恢复', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"]')
      // 收起态不发请求(展开才拉,不在每次轮询里拖大响应)
      assert.equal(await page.$('[data-sec="role"] .m-item:has-text("oldrole")'), null, '未展开时不该有回收站条目')
      await page.click('[data-sec="role"] h3:has-text("回收站") button:has-text("查看")')
      await page.waitForSelector('[data-sec="role"] .m-item:has-text("oldrole")')
      const texts = await page.evaluate(() =>
        Array.from(document.querySelectorAll('[data-sec="role"] .m-item')).map((x) => x.textContent ?? ''),
      )
      assert.ok(
        texts.some((x) => x.includes('oldrole') && x.includes('2026-09-28 15:30')),
        `角色条目应显示名字与删除时间:${JSON.stringify(texts)}`,
      )
      assert.ok(
        texts.some((x) => x.includes('shared-gone') && x.includes('共享技能库')),
        `共享库技能条目应标注归属:${JSON.stringify(texts)}`,
      )
      assert.ok(
        texts.some((x) => x.includes('private-gone') && x.includes('角色私有 · finance')),
        `角色私有技能条目应标注归属:${JSON.stringify(texts)}`,
      )

      // 恢复 = 有副作用 → 二次确认;取消后不得发请求
      const before = stub.seen.filter((r) => r.path === '/api/trash/restore').length
      await page.click('[data-sec="role"] .m-item:has-text("oldrole") button:has-text("恢复")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(stub.seen.filter((r) => r.path === '/api/trash/restore').length, before, '取消确认后仍发了恢复请求')

      await page.click('[data-sec="role"] .m-item:has-text("oldrole") button:has-text("恢复")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(200)
      const post = stub.seen.find((r) => r.method === 'POST' && r.path === '/api/trash/restore')
      assert.ok(post, `恢复未提交:${JSON.stringify(stub.seen)}`)
      const rb = JSON.parse(post.body)
      assert.equal(rb.kind, 'role')
      assert.equal(rb.name, 'oldrole-20260928-153005', `恢复要按回收站目录名定位:${post.body}`)

      // 恢复技能:共享库不带 role,角色私有带 role
      await page.click('[data-sec="role"] .m-item:has-text("private-gone") button:has-text("恢复")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(200)
      const posts = stub.seen.filter((r) => r.method === 'POST' && r.path === '/api/trash/restore')
      const sb = JSON.parse(posts[posts.length - 1].body)
      assert.equal(sb.kind, 'skill')
      assert.equal(sb.role, 'finance')
      assert.equal(sb.name, 'private-gone-20260928-153015')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 技能改名 / 跨库移动(第八十四批):技能身份 = 目录名 + 归属库。面板要能一次把两件改完,
  // 且必须把"会同步改掉挂载它的角色"这个副作用说出来 —— 挂载是按名字存的,改名会连带改引用。
  test('设置面板:技能可改名/换库,走二次确认且回显挂载同步', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"]')
      const form = '[data-sec="role"] .add-form:has-text("改名 / 移动")'
      const row = '[data-sec="role"] .m-item:has-text("skill-alpha")'
      assert.equal(await page.$(form), null, '未点按钮时不该有改名/移动表单')
      // 技能列表在"展开某个角色"的详情里(与挂载勾选同一段),先展开 finance
      await page.click('[data-sec="role"] .prow:has-text("Bookkeeping") button:has-text("编辑")')
      await page.waitForSelector(`${row} button:has-text("改名/移动")`)

      await page.click(`${row} button:has-text("改名/移动")`)
      await page.waitForSelector(form)
      // 默认填现值:名字与所属库都不变 = 没有变化 → 提交禁用(后端也会拒"无变化")
      assert.ok(await page.isDisabled(`${form} button:has-text("确认改名/移动")`), '未填变化时提交应禁用')
      assert.equal(await page.inputValue(`${form} input.inp`), 'skill-alpha', '新名称应预填现值')

      // 改名 = 有副作用 → 二次确认;取消后不得发请求
      await page.fill(`${form} input.inp`, 'skill-renamed')
      assert.ok(!(await page.isDisabled(`${form} button:has-text("确认改名/移动")`)), '填了变化后提交应可用')
      const before = stub.seen.filter((r) => r.path.endsWith('/relocate')).length
      await page.click(`${form} button:has-text("确认改名/移动")`)
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(stub.seen.filter((r) => r.path.endsWith('/relocate')).length, before, '取消确认后仍发了 relocate 请求')

      await page.click(`${form} button:has-text("确认改名/移动")`)
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(200)
      const post = stub.seen.find((r) => r.method === 'POST' && r.path.endsWith('/relocate'))
      assert.ok(post, `改名未提交:${JSON.stringify(stub.seen)}`)
      // 共享库技能:路径不带 role 查询参数,to_name 是新名字
      assert.equal(post.path, '/api/skills/skill-alpha/relocate', `源库应写在路径上:${post.path}`)
      const pb = JSON.parse(post.body)
      assert.equal(pb.to_name, 'skill-renamed')
      assert.equal(pb.to_role, '')
      // 副作用必须说出来(否则用户不知道角色挂载被动过)
      await page.waitForSelector('[data-sec="role"] p.ok:has-text("已同步 1 个角色的挂载")')
      const msg = await page.textContent('[data-sec="role"] p.ok')
      assert.ok(String(msg || '').includes('skill-alpha'), `应回显改名前后名字:${msg}`)

      // 角色私有技能:源库写在查询参数上,换到共享库 = to_role 空
      await page.click('[data-sec="role"] .m-item:has-text("private-beta") button:has-text("改名/移动")')
      await page.waitForSelector(form)
      await page.selectOption(`${form} select.sel`, '')
      await page.click(`${form} button:has-text("确认改名/移动")`)
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(200)
      const posts = stub.seen.filter((r) => r.method === 'POST' && r.path.endsWith('/relocate'))
      const last = posts[posts.length - 1]
      assert.equal(last.path, '/api/skills/private-beta/relocate', `源库应写在路径旁的查询参数上:${last.path}`)
      assert.equal(last.query, '?role=finance', `角色私有技能的源库应带 role:${last.query}`)
      const lb = JSON.parse(last.body)
      assert.equal(lb.to_role, '', '应搬到共享库')
      assert.equal(lb.to_name, 'private-beta', '不改名时 to_name 仍是原名(后端按此判"只换库")')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 全局指令(第八十一批):与角色段正交的一段 —— 它对**所有角色**生效(角色未声明
  // exclude_global 时)。保存是覆盖写 + 二次确认;取消后不得发 PUT。
  test('设置面板:全局指令可编辑、保存走二次确认、取消不发 PUT', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[0].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="instr"]')
      // 导航项与段同键出现,且正文是拉回来的原文(不是本地写死的默认值)
      const nav = await page.evaluate(() => Array.from(document.querySelectorAll('.nav-it')).map((b) => b.textContent?.trim() ?? ''))
      assert.ok(nav.includes('指令'), `导航应有「指令」:${JSON.stringify(nav)}`)
      await page.click('[data-sec="instr"] button:has-text("编辑")')
      await page.waitForSelector('[data-sec="instr"] textarea')
      const val = await page.inputValue('[data-sec="instr"] textarea')
      assert.ok(val.includes('GLOBAL-INSTRUCTIONS-'), `未回填全局指令原文:${val.slice(0, 40)}`)
      // 改一段再保存:先弹确认;取消 → 不得发 PUT
      await page.fill('[data-sec="instr"] textarea', 'GLOBAL-EDIT-1\n')
      // 计数必须跟着草稿走(14 字节):它同时是"保存前拦超限"的依据
      const cnt = await page.textContent('[data-sec="instr"]')
      assert.ok(cnt?.includes('当前 14 字节'), `字节计数未跟随草稿:${cnt?.slice(0, 160)}`)
      await page.click('[data-sec="instr"] button:has-text("保存全局指令")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(
        stub.seen.filter((r) => r.method === 'PUT' && r.path === '/api/instructions').length,
        0,
        `取消确认后仍发出了 PUT:${JSON.stringify(stub.seen)}`,
      )
      // 确认 → PUT 体里是新文本
      await page.click('[data-sec="instr"] button:has-text("保存全局指令")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(200)
      const put = stub.seen.find((r) => r.method === 'PUT' && r.path === '/api/instructions')
      assert.ok(put, `保存未提交:${JSON.stringify(stub.seen)}`)
      assert.equal(JSON.parse(put.body).text, 'GLOBAL-EDIT-1\n', `PUT 体不对:${put.body}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 草稿只在内存里:面板内关掉/再打开都不丢(组件常驻),但**页面卸载**(刷新/关标签)会丢 ——
  // 而且悄无声息,重开就是服务端那版。所以拦截只装在 beforeunload 上,且只脏时拦;
  // 点空白关闭面板照常关(它不丢东西,拦它只是噪音)。这三条一起钉住,免得以后只留一半。
  test('未保存草稿:点空白关闭面板不拦也不丢,刷新/关标签才拦', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, true, false, false, true), docks[0].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="instr"]')
      // dispatchEvent 返回 false ⇔ 监听器 preventDefault 过(即浏览器会弹「确认离开」)
      const unloadBlocked = () =>
        page.evaluate(() => !window.dispatchEvent(new Event('beforeunload', { cancelable: true })))

      assert.equal(await unloadBlocked(), false, '干净时也拦刷新 ⇒ 给正常操作加了没来由的确认框')
      await page.click('[data-sec="instr"] button:has-text("编辑")')
      await page.fill('[data-sec="instr"] textarea', 'DRAFT-KEEP-ME\n')

      assert.equal(await unloadBlocked(), true, '有未保存草稿却不拦刷新/关标签 ⇒ 草稿被静默丢弃')

      // 点空白关闭:直接关掉,不出确认层;草稿留在内存里(下面重开还能取到)
      await page.click('.mask', { position: { x: 40, y: 400 } })
      await page.waitForFunction(() => !document.querySelector('.mask'), null, { timeout: 2000 })
      assert.equal(
        await page.evaluate(() => document.querySelectorAll('[aria-label="操作确认"]').length),
        0,
        '点空白关闭弹了确认层:关闭面板不丢草稿,不该拦',
      )

      // 重开面板 → 草稿还在(这正是「点空白关闭可以放心关」的依据)。编辑区的展开态也是组件级
      // ref,重开时通常已经展开;没展开才点一下「编辑」。
      await page.click('.gear')
      await page.waitForSelector('[data-sec="instr"]')
      if ((await page.locator('[data-sec="instr"] textarea').count()) === 0) {
        await page.click('[data-sec="instr"] button:has-text("编辑")')
      }
      const kept = await page.inputValue('[data-sec="instr"] textarea')
      assert.equal(kept, 'DRAFT-KEEP-ME\n', `点空白关闭把草稿丢了:重开拿到 ${JSON.stringify(kept)}`)

      await page.click('[data-sec="instr"] button:has-text("放弃修改")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForFunction(
        () => !document.querySelector('[data-sec="instr"] .dirty'),
        null,
        { timeout: 2000 },
      )
      assert.equal(await unloadBlocked(), false, '放弃修改后仍拦刷新 ⇒ 拦了一个已经不存在的问题')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // "写了但没生效"不许谎报成功(第八十一批):文件确实落盘了,但跑在进程里的提示还是旧的 ——
  // 这两种状态在界面上必须长得不一样(绿色成功文案 vs 黄框警示),否则用户以为改完就生效了。
  test('设置面板:全局指令重载失败时给出警示而不是成功回执', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, true, false, false, true), docks[0].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="instr"]')
      await page.click('[data-sec="instr"] button:has-text("编辑")')
      await page.waitForSelector('[data-sec="instr"] textarea')
      await page.fill('[data-sec="instr"] textarea', 'NO-RELOAD')
      await page.click('[data-sec="instr"] button:has-text("保存全局指令")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForSelector('[data-sec="instr"] .swarn')
      const warn = await page.textContent('[data-sec="instr"] .swarn')
      assert.ok(warn?.includes('未生效'), `警示文案应说清状态:${warn}`)
      assert.ok(warn?.includes('ctx.systemPrompt 未装配'), `警示应带上服务端原因:${warn}`)
      const sec = await page.textContent('[data-sec="instr"]')
      assert.ok(!sec?.includes('已保存并生效'), `重载失败时不得出现成功回执:${sec?.slice(0, 160)}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 记忆治理面板(第一百一十批):与 /memory 命令同一份实现,此前只能在命令里管。
  // 钉四件事:① 段与导航同键出现、内容是拉回来的真实数据;② 写动作真的提交后端
  // (不是只改本地状态);③ 删除走二次确认;④ 未装配(503)时整段隐藏不摆空壳。
  // 记忆正文故意是无空格长 token —— 与技能名/角色名同属横向滚动高危位置。
  test('设置面板:记忆段的读写与治理动作', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="memory"]')
      const nav = await page.evaluate(() => Array.from(document.querySelectorAll('.nav-it')).map((b) => b.textContent?.trim() ?? ''))
      assert.ok(nav.includes('记忆'), `导航应有「记忆」:${JSON.stringify(nav)}`)
      const sec = await page.textContent('[data-sec="memory"]')
      assert.ok(sec?.includes('MEM-LONG-'), `未回填真实记忆内容:${sec?.slice(0, 120)}`)
      assert.ok(sec?.includes('以指令为准'), `应说清记忆与指令的优先级:${sec?.slice(0, 200)}`)
      assert.ok(sec?.includes('user.md'), `应给出记忆文件路径(设计上允许人手改):${sec?.slice(0, 200)}`)

      // 记一条 → 真发 POST,列表随后多一条
      await page.fill('[data-sec="memory"] [data-testid="mem-input"]', 'NEW-MEM-ENTRY')
      await page.click('[data-sec="memory"] [data-testid="mem-add"]')
      await page.waitForTimeout(200)
      const adds = stub.memState.writes.filter((w) => w.action === 'add')
      assert.equal(adds.length, 1, `记一条应提交一次:${JSON.stringify(stub.memState.writes)}`)
      assert.equal(adds[0].content, 'NEW-MEM-ENTRY')
      assert.ok((await page.textContent('[data-sec="memory"]'))?.includes('NEW-MEM-ENTRY'), '新记忆应出现在列表里')

      // 逐条删 → 二次确认,取消不发请求
      await page.click('[data-sec="memory"] .mem-item button:has-text("删除")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(stub.memState.writes.filter((w) => w.action === 'remove').length, 0, '取消确认后仍发了删除')
      // 确认 → 真删
      await page.click('[data-sec="memory"] .mem-item button:has-text("删除")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(200)
      assert.equal(stub.memState.writes.filter((w) => w.action === 'remove').length, 1, '确认后应发出删除')

      // 按来源整段删:治理的关键动作(某个会话写进来的记忆一次性清干净)
      const srcBtn = '[data-sec="memory"] .mem-src button'
      await page.waitForSelector(srcBtn)
      assert.ok((await page.textContent(srcBtn))?.includes('sess-abc123'), '来源按钮应标出来源会话')
      await page.click(srcBtn)
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(200)
      const bySrc = stub.memState.writes.find((w) => w.action === 'remove_source')
      assert.equal(bySrc?.source, 'sess-abc123', `按来源删的参数不对:${JSON.stringify(bySrc)}`)

      // 关掉注入 → 标记出现,数据仍在
      await page.click('[data-sec="memory"] button:has-text("关掉注入")')
      await page.waitForSelector('[data-sec="memory"] [data-testid="mem-off"]')
      assert.equal(stub.memState.enabled, false, '关掉注入应提交 toggle:false')
      assert.ok((await page.textContent('[data-sec="memory"] .mem-list')) !== null, '关注入是关注入,不该把记忆清空')
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 候选池(记忆层 M2 前置件):候选**不进上下文**,只有转正才生效 —— 界面上必须把这
  // 句话说在按钮旁边(否则用户会以为「提了就已经记住了」),且两个动作真提交后端。
  test('设置面板:候选池的提/转正/丢弃与二次确认', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="memory"] [data-testid="mem-cand-head"]')
      const head = await page.textContent('[data-testid="mem-cand-head"]')
      assert.ok(head?.includes('候选不进上下文'), `必须说清候选不进上下文:${head}`)
      assert.ok(head?.includes('2/12'), `应回显池子用量:${head}`)
      const sec = await page.textContent('[data-sec="memory"]')
      assert.ok(sec?.includes('CAND-ONE-'), `未回填候选内容:${sec?.slice(0, 200)}`)

      // 提一条 → 真发 propose
      await page.fill('[data-sec="memory"] [data-testid="mem-input"]', 'NEW-CANDIDATE')
      await page.click('[data-sec="memory"] [data-testid="mem-propose"]')
      await page.waitForTimeout(200)
      const props = stub.memState.writes.filter((w) => w.action === 'propose')
      assert.equal(props.length, 1, `提候选应提交一次:${JSON.stringify(stub.memState.writes)}`)
      assert.equal(props[0].content, 'NEW-CANDIDATE')
      assert.ok((await page.textContent('[data-sec="memory"]'))?.includes('NEW-CANDIDATE'), '新候选应出现在候选区')

      // 转正第一条 → 真发 accept
      await page.click('[data-sec="memory"] .mem-item button:has-text("转正")')
      await page.waitForTimeout(200)
      assert.equal(stub.memState.writes.filter((w) => w.action === 'accept').length, 1, '转正应提交 accept')

      // 全部转正 → 二次确认,取消不发请求
      await page.click('[data-sec="memory"] [data-testid="mem-accept-all"]')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(stub.memState.writes.filter((w) => w.action === 'accept_all').length, 0, '取消确认后仍发了全部转正')
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  test('设置面板:未装配记忆服务时整段隐藏(不摆空壳)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, true, false, false, true, 'finance', 0, false, false), docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="instr"]')
      await page.waitForTimeout(300)
      assert.equal(await page.locator('[data-sec="memory"]').count(), 0, '未装配 ctx.memory 时不该出现记忆段')
      const nav = await page.evaluate(() => Array.from(document.querySelectorAll('.nav-it')).map((b) => b.textContent?.trim() ?? ''))
      assert.ok(!nav.includes('记忆'), `导航也不该有「记忆」:${JSON.stringify(nav)}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 草稿保护(第八十二批):角色工作规则是「写进磁盘就长期生效」的东西,而**切换目标**
  // (换角色/收起编辑区)会重新拉服务端正文覆盖草稿 —— 用户的修改不能就这么没了。
  // 这里钉三件事:脏了有「未保存」标记、切目标前问一声、取消后草稿原封不动。
  test('设置面板:未保存的工作规则不静默丢(切角色先问、取消保留草稿)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      await page.click('[data-sec="role"] .prow:has-text("Finance-Analyst") button:has-text("编辑")')
      await page.waitForSelector('[data-sec="role"] label.fld:has-text("工作规则") textarea')
      await page.fill('[data-sec="role"] label.fld:has-text("工作规则") textarea', 'DRAFT-KEEP-ME\n')
      const sec = await page.textContent('[data-sec="role"]')
      assert.ok(sec?.includes('未保存'), `草稿动了却没标「未保存」:${sec?.slice(0, 200)}`)

      // 切到另一个角色:必须先问一声(取消 → 既没切走、草稿也在)
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("编辑")')
      await page.waitForSelector('[aria-label="操作确认"]')
      const ask = (await page.textContent('[aria-label="操作确认"]')) ?? ''
      assert.ok(ask.includes('未保存'), `确认文案应点名未保存的草稿:${ask}`)
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(
        await page.inputValue('[data-sec="role"] label.fld:has-text("工作规则") textarea'),
        'DRAFT-KEEP-ME\n',
        '取消后草稿丢了',
      )

      // 确认才切过去;而且只是换目标,不得顺手发出任何写请求
      const writes = stub.seen.length
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("编辑")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(250)
      const val = await page.inputValue('[data-sec="role"] label.fld:has-text("工作规则") textarea')
      assert.ok(val.includes('ASSISTANT-RULES'), `未换到目标角色的正文:${val}`)
      assert.equal(stub.seen.length, writes, `切目标不该发写请求:${JSON.stringify(stub.seen.slice(writes))}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 第八十九批三条(都是「静默失信」):① 改名不是换角色 —— 旧实现接一句 selectRole(d)
  // 会撞「同 id = 收起」分支把编辑区收掉,再展开时服务端版本盖掉草稿;② 确认文案只列真会被
  // 丢弃的项(全局指令不在「换目标」路径上);③ 同一个技能再点一次「原文」= 重读磁盘,
  // 有草稿时必须先问(旧实现用 `!same` 把这一次跳过了)。
  test('设置面板:改名不动草稿、确认文案不虚报、重复点「原文」要问', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      const agents = '[data-sec="role"] label.fld:has-text("工作规则") textarea'
      await page.click('[data-sec="role"] .prow:has-text("Finance-Analyst") button:has-text("编辑")')
      await page.waitForSelector(agents)
      await page.fill(agents, 'DRAFT-SURVIVES-RENAME\n')
      // 顺手给全局指令也留一份草稿:它**不会**被「换角色/改名」丢掉,故不该出现在确认文案里
      await page.click('.nav-it:has-text("指令")')
      await page.click('[data-sec="instr"] button:has-text("编辑")')
      await page.fill('[data-sec="instr"] textarea', 'INSTR-DRAFT\n')

      // 切到另一个角色:确认文案只该点名角色工作规则(技能无草稿),不许提全局指令
      await page.click('.nav-it:has-text("角色")')
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("编辑")')
      await page.waitForSelector('[aria-label="操作确认"]')
      const ask = (await page.textContent('[aria-label="操作确认"]')) ?? ''
      assert.ok(ask.includes('工作规则'), `确认文案应点名会丢的角色草稿:${ask}`)
      assert.ok(!ask.includes('全局指令'), `确认文案虚报了不会被丢的全局指令草稿:${ask}`)
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)

      // 改名:编辑区不许收起、草稿不许被服务端版本盖掉
      await page.fill('[data-sec="role"] .row:has-text("改标识") input', 'finance-renamed')
      await page.click('[data-sec="role"] button:has-text("改标识")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(300)
      assert.equal(await page.inputValue(agents), 'DRAFT-SURVIVES-RENAME\n', '改名把工作规则草稿冲掉了')
      assert.ok(
        ((await page.textContent('[data-sec="role"]')) ?? '').includes('未保存'),
        '改名后不再标「未保存」= 草稿已经不在了',
      )
      // 新 id 已生效(改标识按钮回到禁用态:输入框与当前 id 一致)
      assert.ok(await page.isDisabled('[data-sec="role"] button:has-text("改标识")'), '改标识未生效或输入框未跟上新 id')

      // 同一个技能再点一次「原文」:重读磁盘会盖草稿 → 必须先问;取消后草稿原封不动
      const skTa = '[data-sec="role"] .add-form:has-text("SKILL.md") textarea'
      await page.click('[data-sec="role"] .m-item:has-text("skill-alpha") button:has-text("原文")')
      await page.waitForSelector(skTa)
      await page.fill(skTa, 'SKILL-DRAFT\n')
      await page.click('[data-sec="role"] .m-item:has-text("skill-alpha") button:has-text("原文")')
      await page.waitForSelector('[aria-label="操作确认"]')
      const ask2 = (await page.textContent('[aria-label="操作确认"]')) ?? ''
      assert.ok(ask2.includes('SKILL.md'), `重复点「原文」的确认文案应点名技能草稿:${ask2}`)
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(await page.inputValue(skTa), 'SKILL-DRAFT\n', '取消后技能草稿丢了')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 第八十九批:MCP server 配置也是「改了就长期生效」的东西,而每次打开面板都会 loadMcp()
  // 重拉磁盘那一份 —— 不拦就是静默丢草稿(与三处文本框同口径)。
  test('设置面板:MCP 草稿不被重拉覆盖(打开面板先问一声)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, false)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.click('.nav-it:has-text("MCP server")')
      const cmd = '[data-sec="mcp"] label.fld:has-text("启动命令") input'
      await page.waitForSelector(cmd)
      await page.fill(cmd, 'npx -y somewhere-else --flag=1')
      assert.ok(((await page.textContent('[data-sec="mcp"]')) ?? '').includes('未保存'), 'MCP 草稿没标「未保存」')
      // 关面板再打开:onMounted/watch 都会 loadMcp() → 会话里那份不能就这么没了
      await page.keyboard.press('Escape')
      await page.waitForTimeout(100)
      await page.click('.gear')
      await page.waitForSelector('[aria-label="操作确认"]')
      const ask = (await page.textContent('[aria-label="操作确认"]')) ?? ''
      assert.ok(ask.includes('MCP server 配置'), `确认文案应点名 MCP 草稿:${ask}`)
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      await page.click('.nav-it:has-text("MCP server")')
      assert.equal(await page.inputValue(cmd), 'npx -y somewhere-else --flag=1', '取消后 MCP 草稿丢了')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 第八十九批:技能挂载是「整份替换」(skills_set + skills)的即时提交 —— 各发各的会让后一个
  // 请求带着旧清单覆盖前一个(勾两个只生效一个),且请求在途时控件看不出已经点过。
  test('设置面板:挂载即时提交串行且在途禁用(连点两次不丢改动)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true, 'finance', 250)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      // 用 assistant:它是「替换」态(skills_set=true,已挂 skill-alpha),勾选框的勾/去勾
      // 才真的是加/减(默认池态下所有勾都是"显示为已勾",点一下反而是去掉)。
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("编辑")')
      await page.waitForSelector('[data-sec="role"] .m-item')
      const box = (n) => `[data-sec="role"] .m-item:has-text("${n}") input[type=checkbox]`
      const patches = () => stub.seen.filter((r) => r.method === 'PATCH' && r.path === '/api/roles/assistant').map((r) => JSON.parse(r.body || '{}'))
      const last = () => patches()[patches().length - 1]

      // ① 在途禁用:点一下之后另一处挂载控件立刻不可点,且显示「提交中」
      await page.click(box('skill-with-a-very-long-name'))
      await page.waitForTimeout(60)
      assert.ok(await page.isDisabled(box('private-beta')), '提交在途时其它挂载控件应禁用')
      assert.ok(await page.isVisible('[data-testid="role-saving"]'), '在途时应有「提交中」回执')
      await page.waitForTimeout(400)
      assert.ok(!(await page.isVisible('[data-testid="role-saving"]')), '请求结束后不应还挂着「提交中」')

      // ② 同一 tick 连点两下(真人手速上限;此时 :disabled 还没落到 DOM 上):两次改动都要在。
      //    两次都是"去掉"(两个名字当前都是已挂):期望最终清单两都不剩 —— 旧实现第二次带着
      //    点击时的旧清单发,会把前一次去掉的那个名字又挂回来。
      await page.evaluate(() => {
        const pick = (n) =>
          Array.from(document.querySelectorAll('[data-sec="role"] .m-item')).find((m) => m.textContent.includes(n))
        pick('skill-alpha').querySelector('input[type=checkbox]').click()
        pick('skill-with-a-very-long-name').querySelector('input[type=checkbox]').click()
      })
      await page.waitForTimeout(600)
      const got = last().skills ?? []
      assert.ok(
        !got.includes('skill-alpha') && !got.includes('skill-with-a-very-long-name'),
        `连点两次丢了改动(后者带着旧清单覆盖):${JSON.stringify(got)}`,
      )
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 第九十一批:角色工具集 —— 默认全给、勾上即排除(整份替换)。
  // 面板必须能分清三种状态:能用 / 被本角色排除 / 名字当前不存在(插件卸载后悬空)。
  test('设置面板:角色工具排除(整份替换、悬空名可清、读全量清单)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, false, false, false, true, 'finance', 250)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("编辑")')
      await page.waitForSelector('[data-sec="role"] textarea')
      // 折叠时不拉工具清单(不拖大每次轮询的响应)
      assert.equal(stub.toolQueries.length, 0, '工具清单不该在未展开时就拉')
      await page.click('[data-sec="role"] h3:has-text("工具") button.link')
      await page.waitForSelector('[data-sec="role"] .m-item:has-text("shell") input[type=checkbox]')
      // 管理面口径:必须带 ?all=1(全量)—— 被排除的工具也要列出来
      assert.equal(stub.toolQueries.length, 1, `展开应只拉一次工具清单:${JSON.stringify(stub.toolQueries)}`)
      assert.ok(stub.toolQueries[0].includes('all=1'), `工具清单应读全量(?all=1):${stub.toolQueries[0]}`)

      const sec = () => page.textContent('[data-sec="role"]')
      assert.ok(((await sec()) ?? '').includes('已排除 2 项'), '应显示已排除项数')
      const box = (n) => `[data-sec="role"] .m-item:has-text("${n}") input[type=checkbox]`
      assert.ok(await page.isChecked(box('read_skill')), '已被排除的工具应显示为勾选')
      assert.ok(!(await page.isChecked(box('shell'))), '未被排除的工具不该是勾选态')
      // 悬空名(排除清单里有、工具清单里没有)必须可见且能单独清掉 —— 否则只能手改 role.yaml
      const staleRow = '[data-sec="role"] .m-item:has-text("ghost-tool")'
      await page.waitForSelector(staleRow)
      assert.ok(((await page.textContent(staleRow)) ?? '').includes('该工具当前不存在'), '悬空名应标出「当前不存在」')
      await page.click(`${staleRow} button:has-text("清除")`)
      await page.waitForTimeout(400)
      const patches = () => stub.seen.filter((r) => r.method === 'PATCH' && r.path === '/api/roles/assistant').map((r) => JSON.parse(r.body || '{}'))
      assert.deepEqual(patches().at(-1).tools_exclude, ['read_skill'], `清除悬空名应整份替换:${JSON.stringify(patches().at(-1))}`)
      await page.waitForSelector('[data-sec="role"] .m-item:has-text("shell") input[type=checkbox]')

      // 同一 tick 连点两下(此时 :disabled 还没落到 DOM 上):两次改动都要在。
      // 一个"恢复"(read_skill 去掉)+ 一个"排除"(shell 加上)—— 后一次若带着点击时的旧清单发,
      // 就会把刚恢复的 read_skill 又排除回去(整份替换字段的经典丢改动)。
      await page.evaluate(() => {
        const pick = (n) =>
          Array.from(document.querySelectorAll('[data-sec="role"] .m-item')).find((m) => m.textContent.includes(n))
        const a = pick('read_skill').querySelector('input[type=checkbox]')
        a.checked = false
        a.dispatchEvent(new Event('change', { bubbles: true }))
        const b = pick('shell').querySelector('input[type=checkbox]')
        b.checked = true
        b.dispatchEvent(new Event('change', { bubbles: true }))
      })
      await page.waitForTimeout(800)
      const got = patches().at(-1).tools_exclude ?? []
      assert.ok(got.includes('shell'), `后一次提交丢了新勾的排除项:${JSON.stringify(got)}`)
      assert.ok(!got.includes('read_skill'), `后一次提交带着旧清单把刚恢复的工具又排除了:${JSON.stringify(got)}`)
      assert.equal(patches().length, 3, `应一共三次 PATCH(清除悬空名 + 两次勾选):${JSON.stringify(patches())}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 角色权限收紧(第九十二批):角色只能往里收 —— 下拉只给"更严"的档(open/full-access
  // 是放宽,后端直接 400,界面不摆点了必失败的选项),提交走部分更新,且全局沙箱/审批区
  // 必须点明"上面选的是全局档、实际按角色收紧的档裁决"(否则就是"点了严格却不生效")。
  test('设置面板:角色权限只收紧(值域 / 部分更新 / 有效档回显)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, false, false, false, true, 'finance', 0, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      // 列表行:这个角色收紧了什么(声明值,不是有效值)
      const finRow = await page.textContent('[data-sec="role"] .prow:has-text("Finance-Analyst")')
      assert.ok(finRow?.includes('收紧：审批严格 · 沙箱只读'), `角色行应回显收紧摘要:${finRow}`)
      // 全局区(沙箱/审批):角色收紧中必须说清"实际按哪档裁决"
      const reason = (await page.textContent('[data-sec="reason"]')) ?? ''
      assert.ok(reason.includes('当前角色把审批收紧为「严格」'), `全局审批区应说明角色收紧:${reason.slice(0, 200)}`)
      assert.ok(reason.includes('当前角色把沙箱收紧为「只读」'), `全局沙箱区应说明角色收紧:${reason.slice(0, 200)}`)

      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("编辑")')
      await page.waitForSelector('[data-sec="role"] select')
      const sel = (lab) => `[data-sec="role"] .row:has-text("${lab}") select`
      assert.equal(await page.inputValue(sel('审批')), 'strict', '角色声明的审批档应回显在下拉里')
      assert.equal(await page.inputValue(sel('沙箱')), 'read-only', '角色声明的沙箱档应回显在下拉里')
      // 值域:只有"跟随全局 + 更严的档";open / full-access 是放宽,后端会 400 ⇒ 不出现
      const apOpts = await page.$$eval(`${sel('审批')} option`, (os) => os.map((o) => o.value))
      const sbOpts = await page.$$eval(`${sel('沙箱')} option`, (os) => os.map((o) => o.value))
      assert.deepEqual(apOpts, ['', 'smart', 'strict'], `审批下拉值域不对:${JSON.stringify(apOpts)}`)
      assert.deepEqual(sbOpts, ['', 'read-only', 'workspace-write'], `沙箱下拉值域不对:${JSON.stringify(sbOpts)}`)
      // 生效档回显:后端给的**实际**档与来源(前端不自己合成)
      const note = (await page.textContent('[data-testid="role-tier-note"]')) ?? ''
      assert.ok(note.includes('审批 全局智能 → 实际严格'), `生效档回显缺审批:${note}`)
      assert.ok(note.includes('沙箱 全局工作区 → 实际只读'), `生效档回显缺沙箱:${note}`)

      // 改沙箱档 → **只改本地草稿,一个 PATCH 都不发**(2026-10-04 用户实测反馈)。
      // 此前是 @change="saveRoleDef(...)" = 选中即写盘,而同一区的文本框却要点「保存定义」——
      // 两套语义并排,用户无法形成稳定预期;而这个选项恰好是**权限档**。
      await page.selectOption(sel('沙箱'), 'workspace-write')
      await page.waitForTimeout(300)
      const patchesOf = () =>
        stub.seen
          .filter((r) => r.method === 'PATCH' && r.path === '/api/roles/assistant')
          .map((r) => JSON.parse(r.body || '{}'))
      assert.equal(patchesOf().length, 0, `选中不该写盘,却有:${JSON.stringify(patchesOf())}`)
      assert.equal(await page.inputValue(sel('沙箱')), 'workspace-write', '草稿应即时反映在下拉上')
      assert.ok(
        await page.locator('[data-sec="role"] .acts .dirty').isVisible(),
        '未保存要有常驻标记(否则用户不知道刚才那一下没生效)',
      )

      // 「放弃改动」→ 回到已保存的值,且**不发请求**(不丢数据也不写盘的退出口)
      await page.click('[data-sec="role"] .acts button:has-text("放弃改动")')
      await page.waitForTimeout(300)
      assert.equal(patchesOf().length, 0, `放弃不该写盘:${JSON.stringify(patchesOf())}`)
      assert.equal(await page.inputValue(sel('沙箱')), 'read-only', '放弃后应回到已保存的档位')

      // 点「保存定义」→ **一次** PATCH,且带上整份定义(不再是一个字段一个请求)
      await page.selectOption(sel('沙箱'), 'workspace-write')
      await page.waitForTimeout(200)
      await page.click('[data-sec="role"] .acts button:has-text("保存定义")')
      await page.waitForTimeout(400)
      const patches = patchesOf()
      assert.equal(patches.length, 1, `应只提交一次:${JSON.stringify(patches)}`)
      for (const k of ['sandbox', 'approval', 'model', 'thinking', 'exclude_global', 'name', 'description', 'identity']) {
        assert.ok(k in patches[0], `保存定义应带上 ${k}(缺它 = 这个控件的改动会丢):${JSON.stringify(patches[0])}`)
      }
      assert.equal(patches[0].sandbox, 'workspace-write', `保存体不对:${JSON.stringify(patches[0])}`)
      // 回包被回填(真后端回整份 spec):下拉不回弹,且「未保存」标记消失
      assert.equal(await page.inputValue(sel('沙箱')), 'workspace-write', '提交后下拉回弹成旧值')
      assert.equal(
        await page.locator('[data-sec="role"] .acts .dirty').count(),
        0,
        '保存成功后未保存标记应消失(否则看着像存了又没存)',
      )
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 角色包导出/导入(第九十三批):导出走按钮(浏览器下载 / 桌面壳交服务端写文件),
  // 导入走 multipart 上传,且**同名覆盖必须先问**——默认那次不带 overwrite。
  test('设置面板:角色包导出与导入(下载 / multipart / 覆盖先问)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 }, acceptDownloads: true })
    let page = null
    try {
      const stub = makeStub(true, false, false, false, true, 'finance')
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')

      // 导出:点「导出」= 触发一次下载,建议文件名与后端 Content-Disposition 同口径
      const [dl] = await Promise.all([
        page.waitForEvent('download'),
        page.click('[data-sec="role"] .prow:has-text("Finance-Analyst") button:has-text("导出")'),
      ])
      assert.equal(dl.suggestedFilename(), 'gah-role-finance.zip', `下载文件名不对:${dl.suggestedFilename()}`)
      const msg1 = (await page.textContent('[data-sec="role"]')) ?? ''
      assert.ok(msg1.includes('已开始下载 gah-role-finance.zip'), `导出后应有回执:${msg1.slice(0, 200)}`)

      // 导入:选文件 → 首次提交**不带** overwrite → 后端拒 → 面板弹确认(说明旧份进回收站)
      await page.click('[data-sec="role"] h3 button:has-text("导入")')
      await page.setInputFiles('[data-testid="pack-file"]', {
        name: 'gah-role-finance.zip',
        mimeType: 'application/zip',
        buffer: Buffer.from('PK\x03\x04stub'),
      })
      await page.waitForSelector('.dialog .btn.ok')
      const dlg = (await page.textContent('.dialog .t')) ?? ''
      assert.ok(dlg.includes('覆盖已存在的角色「finance」'), `确认文案要说清覆盖谁:${dlg}`)
      assert.ok(dlg.includes('回收站'), `确认文案要说清旧份去哪:${dlg}`)
      assert.deepEqual(stub.packPosts, [{ as: null, overwrite: null }], `首次提交不该带 overwrite:${JSON.stringify(stub.packPosts)}`)

      // 确认 → 带 overwrite=1 重试 → 回执说清旧份进回收站
      await page.click('.dialog .btn.ok')
      await page.waitForTimeout(300)
      assert.equal(stub.packPosts.length, 2, `确认后应再提交一次:${JSON.stringify(stub.packPosts)}`)
      assert.equal(stub.packPosts[1].overwrite, '1', `确认后必须带 overwrite:${JSON.stringify(stub.packPosts[1])}`)
      const msg2 = (await page.textContent('[data-sec="role"]')) ?? ''
      assert.ok(msg2.includes('已导入角色'), `导入后应有回执:${msg2.slice(0, 200)}`)
      assert.ok(msg2.includes('回收站'), `回执要说清旧份进回收站:${msg2.slice(0, 200)}`)
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 同样的丢失路径:技能正文(换另一个技能)与全局指令(收起编辑区)。指令侧的处理不同 ——
  // 收起**不清草稿**,清掉它的出口只有「保存」与显式「放弃修改」(不留静默丢失路径)。
  test('设置面板:技能草稿换目标要问、指令草稿收起不丢且可显式放弃', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      await page.click('[data-sec="role"] .prow:has-text("Finance-Analyst") button:has-text("编辑")')
      await page.waitForSelector('[data-sec="role"] .m-item')

      // 技能原文:改了再看另一个技能 → 先问;取消 → 草稿还在
      const skTa = '[data-sec="role"] .add-form:has-text("SKILL.md") textarea'
      await page.click('[data-sec="role"] .m-item:has-text("skill-alpha") button:has-text("原文")')
      await page.waitForSelector(skTa)
      await page.fill(skTa, 'SKILL-DRAFT\n')
      assert.ok(
        ((await page.textContent('[data-sec="role"] .add-form')) ?? '').includes('未保存'),
        '技能草稿没标「未保存」',
      )
      await page.click('[data-sec="role"] .m-item:has-text("skill-with-a-very-long-name") button:has-text("原文")')
      await page.waitForSelector('[aria-label="操作确认"]')
      const ask = (await page.textContent('[aria-label="操作确认"]')) ?? ''
      assert.ok(ask.includes('SKILL.md'), `技能草稿的确认文案应点名：${ask}`)
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(await page.inputValue(skTa), 'SKILL-DRAFT\n', '取消后技能草稿丢了')

      // 全局指令:收起再展开,草稿必须还在(旧实现是收起→回填服务端内容=静默丢)
      await page.click('[data-sec="instr"] button:has-text("编辑")')
      await page.fill('[data-sec="instr"] textarea', 'INSTR-DRAFT\n')
      assert.ok(
        ((await page.textContent('[data-sec="instr"] h3')) ?? '').includes('未保存'),
        '指令草稿没标「未保存」',
      )
      await page.click('[data-sec="instr"] button:has-text("收起")')
      await page.waitForTimeout(200)
      await page.click('[data-sec="instr"] button:has-text("编辑")')
      assert.equal(
        await page.inputValue('[data-sec="instr"] textarea'),
        'INSTR-DRAFT\n',
        '收起编辑区把全局指令草稿弄丢了',
      )
      // 放弃修改 = 唯一主动清草稿的出口(需确认)→ 回到磁盘上的那一版
      await page.click('[data-sec="instr"] button:has-text("放弃修改")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(250)
      const back = await page.inputValue('[data-sec="instr"] textarea')
      assert.ok(back.includes('GLOBAL-INSTRUCTIONS-'), `放弃修改未回到磁盘版本:${back.slice(0, 40)}`)
      assert.ok(
        !((await page.textContent('[data-sec="instr"] h3')) ?? '').includes('未保存'),
        '放弃修改后仍标着「未保存」',
      )
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 形态 B(第八十一批):基线态在列表里**有名字**且能一键切回 —— 此前"不启用角色"在面板里
  // 是无形的(只有当前有角色时才出现的「停用当前角色」按钮),用户看不出基线意味着什么。
  test('设置面板:默认（基线）行可一键切回,且不下发角色删除/编辑', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      // 点基线行的「切换」→ POST /api/roles/-/use(id 空),且请求体里不带 id(服务端认 "-" 占位符)
      await page.click('[data-sec="role"] .prow:has-text("默认（基线）") button:has-text("切换")')
      await page.waitForTimeout(250)
      const use = stub.seen.find((r) => r.method === 'POST' && r.path.endsWith('/use'))
      assert.ok(use, `基线切换未提交:${JSON.stringify(stub.seen)}`)
      assert.equal(use.path, '/api/roles/-/use', `基线切换的路径不对:${use.path}`)
      // 基线行只该有「切换」:它是合成行,没有定义可改、没有东西可删
      const acts = await page.evaluate(() =>
        Array.from(document.querySelectorAll('[data-sec="role"] .prow')).find((r) => r.textContent?.includes('默认（基线）'))?.querySelector('.sacts')?.textContent?.trim() ?? '',
      )
      assert.equal(acts, '切换', `基线行按钮不对:${acts}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 基线态(current = ""):基线行自己标「当前」且不再给自己「切换」按钮
  test('设置面板:基线态下「默认（基线）」标当前且无切换按钮', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, true, false, false, true, ''), docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      const rows = await page.evaluate(() =>
        Array.from(document.querySelectorAll('[data-sec="role"] .prow')).map((r) => r.textContent ?? ''),
      )
      assert.ok(rows[0].includes('当前'), `基线态下基线行应标「当前」:${rows[0]}`)
      assert.ok(!rows[0].includes('切换'), `基线已是当前态,不该再给切换按钮:${rows[0]}`)
      assert.ok(
        rows.slice(1).every((r) => r.includes('切换')),
        `基线态下所有真实角色都应可切换:${JSON.stringify(rows.slice(1))}`,
      )
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 悬空当前角色(偏好指向的角色目录被外部删了):面板不静默改偏好,但必须给说法与一键修复落点
  test('设置面板:当前角色悬空时给出说明与切回基线入口', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, true, false, false, true, 'ghost-role-deleted-outside'), docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      const txt = await page.textContent('[data-sec="role"]')
      assert.ok(txt?.includes('已经不存在了'), `悬空态应有说明:${txt?.slice(0, 120)}`)
      assert.ok(txt?.includes('ghost-role-deleted-outside'), '说明里应点名具体角色 id')
      assert.ok(txt?.includes('本轮按基线运行'), '应说明当前实际运行形态')
      const rows = await page.evaluate(() =>
        Array.from(document.querySelectorAll('[data-sec="role"] .prow')).map((r) => r.textContent ?? ''),
      )
      assert.ok(rows[0].includes('切换'), '悬空态下基线行必须能一键切回')
      assert.ok(!rows.some((r) => r.includes('当前')), `悬空态下不该有任何行标「当前」:${JSON.stringify(rows)}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 未装配 ctx.roles(真实后端 503)时整段隐藏:导航项与 section 都不出现,
  // 而不是渲染一个点不动的空壳(旧后端/裁剪装配下都会走到这条)。
  test('设置面板:未装配角色服务时「角色」段整段不出现', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true), docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[aria-label="设置"] .nav-it')
      const m = await page.evaluate(() => ({
        nav: Array.from(document.querySelectorAll('[aria-label="设置"] .nav-it')).map((b) => b.textContent?.trim() ?? ''),
        sec: !!document.querySelector('[data-sec="role"]'),
      }))
      assert.ok(!m.sec, '503 时不应渲染角色段')
      assert.ok(!m.nav.includes('角色'), `503 时导航不应有「角色」:${JSON.stringify(m.nav)}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 展开的角色编辑区里全是「用户输入的长 token」(角色名/技能名/技能描述)—— 面板横向滚动条的
  // 高危位置(与长路径/长命令同类)。窄窗口下再验一遍。
  for (const vp of [{ w: 1200, h: 800 }, { w: 820, h: 560 }]) {
    test(`${vp.w}x${vp.h} 指令段展开编辑后仍不出横向滚动条`, async (t) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
      let page = null
      try {
        page = await open(ctx, makeStub(true, true, false, false, true), docks[0].dock)
        await page.click('.gear')
        await page.waitForSelector('[data-sec="instr"]')
        await page.click('[data-sec="instr"] button:has-text("编辑")')
        await page.waitForSelector('[data-sec="instr"] textarea')
        await page.waitForTimeout(200)
        const m = await page.evaluate(() => {
          const panel = document.querySelector('[aria-label="设置"]')
          const body = panel.querySelector('.body')
          const br = body.getBoundingClientRect()
          const nm = (el) =>
            el.tagName.toLowerCase() +
            (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : '')
          const off = []
          for (const el of body.querySelectorAll('[data-sec="instr"] *')) {
            const bb = el.getBoundingClientRect()
            if (bb.width === 0 && bb.height === 0) continue
            if (bb.right > br.right + 1 || bb.left < br.left - 1) off.push(nm(el))
          }
          return { scrollW: body.scrollWidth, clientW: body.clientWidth, off: off.slice(0, 6) }
        })
        assert.ok(
          m.scrollW <= m.clientW + 1,
          `指令段展开了横向滚动条:scrollWidth=${m.scrollW} > clientWidth=${m.clientW};越界元素=${JSON.stringify(m.off)}`,
        )
      } catch (e) {
        await shoot(page, t.name)
        throw e
      } finally {
        await ctx.close()
      }
    })

    test(`${vp.w}x${vp.h} 角色段展开后仍不出横向滚动条`, async (t) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
      let page = null
      try {
        // 停靠收起:窄窗口下打开停靠抽屉会盖住底栏右侧的设置按钮(与本用例无关的干扰)
        page = await open(ctx, makeStub(true, true, false, false, true), docks[0].dock)
        await page.click('.gear')
        await page.waitForSelector('[data-sec="role"] .prow')
        await page.click('[data-sec="role"] .prow:has-text("Finance-Analyst") button:has-text("编辑")')
        await page.waitForSelector('[data-sec="role"] textarea')
        // 勾一项 → 切「替换」并把「失效挂载」长 token 一起带出来(那是面板里最长的用户输入)
        await page.click('[data-sec="role"] .m-list .m-item input[type=checkbox]')
        await page.waitForSelector('[data-sec="role"] .m-item:has-text("stale-skill-removed")')
        await page.waitForTimeout(200)
        const m = await page.evaluate(() => {
          const panel = document.querySelector('[aria-label="设置"]')
          const body = panel.querySelector('.body')
          const br = body.getBoundingClientRect()
          const nm = (el) =>
            el.tagName.toLowerCase() +
            (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : '')
          const off = []
          for (const el of body.querySelectorAll('[data-sec="role"] *')) {
            const bb = el.getBoundingClientRect()
            if (bb.width === 0 && bb.height === 0) continue
            if (bb.right > br.right + 1 || bb.left < br.left - 1) off.push(nm(el))
          }
          return { scrollW: body.scrollWidth, clientW: body.clientWidth, off: off.slice(0, 6) }
        })
        assert.ok(
          m.scrollW <= m.clientW + 1,
          `角色段展开了横向滚动条:scrollWidth=${m.scrollW} > clientWidth=${m.clientW};越界元素=${JSON.stringify(m.off)}`,
        )
      } catch (e) {
        await shoot(page, t.name)
        throw e
      } finally {
        await ctx.close()
      }
    })
  }

  // 状态栏角色徽标(第七十九批):角色名是底栏唯一「用户自定长度」的字段 ——
  // 长名字必须截断(实测未截断时文字折成多行,底栏 22px → 58px,并把右侧按钮挤出去)。
  for (const vp of [{ w: 1200, h: 800 }, { w: 820, h: 560 }, { w: 700, h: 460 }]) {
    test(`${vp.w}x${vp.h} 状态栏角色徽标截断显示且不破坏整页不变量`, async (t) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
      let page = null
      try {
        page = await open(ctx, makeStub(true, true, false, false, true), docks[0].dock)
        const b = await page.evaluate(() => {
          const bar = document.querySelector('.statusbar-slot .bar')
          const badge = bar?.querySelector('.role')
          return {
            has: !!badge,
            // 单行:未截断的溢出文字会把 .bar 撑高(min-height 22px)
            h: bar ? Math.round(bar.getBoundingClientRect().height) : 0,
            barOver: bar ? bar.scrollWidth - bar.clientWidth : 0,
            // 长名字被省略:元素内文本比可见宽度长 ⇒ 出了省略号(而不是溢出到栏外)
            badgeOver: badge ? badge.scrollWidth - badge.clientWidth : 0,
          }
        })
        assert.ok(b.has, '状态栏未显示当前角色徽标')
        assert.ok(b.h <= 26, `底栏被角色名撑成多行:高度 ${b.h}px(应 ≤ 26)`)
        assert.ok(b.barOver <= 1, `底栏内容溢出 ${b.barOver}px(角色名未截断)`)
        assert.ok(b.badgeOver > 0, '超长角色名未被截断(应出现省略号)')
        assertInvariants(await measure(page))
      } catch (e) {
        await shoot(page, t.name)
        throw e
      } finally {
        await ctx.close()
      }
    })
  }

  test('浮层显隐带动效:设置抽屉与侧栏停靠区都不是硬切', async (t) => {
    // 显式声明不要 reduced-motion:宿主系统开了「减弱动效」时全局 CSS 会把时长压到 0.01ms,
    // 那时测的就不是我们的动效而是宿主偏好了。
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 }, reducedMotion: 'no-preference' })
    let page = null
    try {
      page = await open(ctx, apiStub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[aria-label="设置"]')

      // 过渡类（enter/leave-active）只在过渡期间存在，故不能等它结束再看 computed style：
      // 点完给 Vue 20ms 刷完这轮 DOM（类已挂上、但 0.15s 的过渡远未走完）再读。
      // 容器与内容面分开读：淡出在容器（遮罩）上，位移/缩放在那层内容面（.panel）上。
      const probe = async (clickSel, watchSel, childSel) => {
        await page.evaluate((cs) => document.querySelector(cs)?.click(), clickSel)
        await page.waitForTimeout(20)
        return page.evaluate(
          ([ws, cs]) => {
            const read = (el) => {
              const st = el ? getComputedStyle(el) : null
              return {
                present: !!el,
                dur: st ? parseFloat(st.transitionDuration) || 0 : 0,
                prop: st ? st.transitionProperty : '',
                opacity: st ? parseFloat(st.opacity) : 1,
              }
            }
            const el = document.querySelector(ws)
            return { el: read(el), child: cs ? read(el?.querySelector(cs)) : null }
          },
          [watchSel, childSel ?? null],
        )
      }

      // 不透明度必须真的在变(不是只挂了类):rAF 里连续采 6 帧取最小值 —— 慢机器一帧掉到
      // 过渡结束也不怕(元素已摘 ⇒ 当作 0),快机器则能采到中间值。
      const faded = (ws) =>
        page.evaluate(
          (sel) =>
            new Promise((res) => {
              let min = 1
              let n = 0
              const tick = () => {
                const el = document.querySelector(sel)
                min = el ? Math.min(min, parseFloat(getComputedStyle(el).opacity)) : 0
                if (++n < 6) requestAnimationFrame(tick)
                else res(min)
              }
              requestAnimationFrame(tick)
            }),
          ws,
        )

      const pane = await probe('[aria-label="设置"] .x', '.mask', '[aria-label="设置"]')
      assert.ok(pane.el.present, '设置面板关掉后元素当场就没了 ⇒ 没有退场过程(硬切)')
      assert.ok(pane.el.dur > 0, `设置遮罩退场时长为 0(硬切):prop=${pane.el.prop}`)
      assert.match(pane.el.prop, /opacity/, `设置遮罩退场缺少淡出过渡:${pane.el.prop}`)
      assert.ok(pane.child?.dur > 0, `设置抽屉内容面退场时长为 0(硬切):prop=${pane.child?.prop}`)
      assert.match(pane.child?.prop ?? '', /transform/, `设置抽屉内容面退场缺少位移过渡:${pane.child?.prop}`)
      const paneOpacity = await faded('.mask')
      assert.ok(paneOpacity < 1, `设置遮罩退场没在动(opacity 始终为 ${paneOpacity})`)
      await page.waitForFunction(() => !document.querySelector('.mask'), null, { timeout: 2000 })

      // 侧栏停靠区（变更 / 看板 / 任务都在里面）同理；它在宽屏下是并排的 flex 兄弟，
      // 宽度变化仍是瞬时的（拖拽调宽要手感），但内容面不该硬切。
      const dock = await probe('[data-tip^="收起侧栏"]', '[data-ui-dock]')
      assert.ok(dock.el.present, '停靠区收起后元素当场就没了 ⇒ 没有退场过程(硬切)')
      assert.ok(dock.el.dur > 0, `停靠区退场时长为 0(硬切):prop=${dock.el.prop}`)
      assert.match(dock.el.prop, /opacity/, `停靠区退场缺少淡出过渡:${dock.el.prop}`)
      const dockOpacity = await faded('[data-ui-dock]')
      assert.ok(dockOpacity < 1, `停靠区退场没在动(opacity 始终为 ${dockOpacity})`)
      await page.waitForFunction(() => !document.querySelector('[data-ui-dock]'), null, { timeout: 2000 })
      await page.waitForFunction(() => !document.querySelector('[data-ui-dock]'), null, { timeout: 2000 })

      // 退场结束后外壳必须回到干净状态（动效不能把视图卡在半路）
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 发消息必须能看见回复:视图是全屏切换的,而 `/diff` 会把视图切到「变更」且此前没有
  // 任何逻辑切回 —— 真机反馈的「说什么都返回『本会话还没有捕获到文件改动』」就是它
  // (那是变更视图的空态,回复全进了看不见的会话流)。
  // 删**非当前**历史会话,不该动当前会话(2026-10-04 用户实测)。
  //
  // 判据用 sessionStorage 的续传游标键:App.rebuild(false)(session-changed 的处理函数)
  // 第一件事就是清它(transport.clearSessionCursor)—— 它是「整条会话流被重建过」
  // 留下的**可观测痕迹**,且不依赖任何 DOM 细节。
  // 键是**按会话分桶**的(多页签各一个,主会话 `gah.lastSeq.main`),所以先找出当前在用的那个桶。
  test('删非当前历史会话不刷新当前会话(删当前会话仍要刷新)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, false)
      page = await open(ctx, stub, docks[0].dock)
      await page.waitForSelector('.sidebar .items', { timeout: 10000 })
      const mark = async (v) =>
        page.evaluate((x) => {
          const k = Object.keys(sessionStorage).find((n) => n.startsWith('gah.lastSeq.')) || 'gah.lastSeq.main'
          sessionStorage.setItem(k, x)
          return k
        }, v)
      const readMark = (k) => page.evaluate((key) => sessionStorage.getItem(key), k)
      const delRow = async (name) => {
        await page.click(`.sidebar .session-item:has-text("${name}") .op.del`)
        await page.waitForSelector('[aria-label="操作确认"]')
        await page.click('[aria-label="操作确认"] .btn.ok')
        await page.waitForTimeout(300)
      }

      // ① 删一个**非当前**的历史会话(桩里当前会话名是 main,历史是 session N)
      const k1 = await mark('777')
      await delRow('session 7')
      assert.equal(await readMark(k1), '777', '删非当前会话不该重建当前会话的流(上一次会话才被清过)')

      // ② 删**当前**会话:后端会新建空会话承接,前端必须重新对齐 ⇒ 流要被重建
      const k2 = await mark('777')
      await delRow('main')
      assert.equal(await readMark(k2), null, '删当前会话后端已新建空会话承接,前端必须重建会话流')

      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 会话页签(第一百一十三批):页签条是**新增的横向通栏**,最容易破坏"整页不滚"与
  // "主列不被挤"两条纪律;顺带钉住「点侧栏 = 在本页签打开」与「关到最后一个回落」。
  test('会话页签条:多页签仍整页不滚、关掉最后一个回主会话', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, false)
      page = await open(ctx, stub, docks[0].dock)
      await page.waitForSelector('.tabbar', { timeout: 10000 })
      const tabs = () => page.$$eval('.tabbar .tab', (ns) => ns.length)

      assert.equal(await tabs(), 1, '首屏只有当前会话一个页签')
      const before = await measure(page)

      // 点侧栏里的历史会话 = 在本页签打开它(不做全局切换)
      await page.click('.sidebar .session-item:has-text("session 1")')
      await page.waitForFunction(() => document.querySelectorAll('.tabbar .tab').length === 2, null, { timeout: 8000 })
      assert.equal(await tabs(), 2, '点侧栏应开出第二个页签')

      // 页签条不得挤压主列(输入框列宽不变)、整页不滚
      const after = await measure(page)
      assert.equal(after.scrollHeight <= after.clientHeight + 1, true, '加页签后整页不得出现纵向滚动')
      assert.equal(after.scrollWidth <= after.clientWidth + 1, true, '加页签后整页不得出现横向滚动')
      assert.equal(after.composer.width, before.composer.width, '页签条是通栏,不许挤压主列宽度')
      assert.ok(after.tabbar && after.tabbar.bottom > after.tabbar.top, '页签条应可见且有高度')

      // 关掉刚开的那一个 → 回到 1 个
      await page.$$eval('.tabbar .tab .x', (els) => els[els.length - 1].click()) // 关最后一个页签(.add 不是 .tab,别用 :last-child)
      await page.waitForFunction(() => document.querySelectorAll('.tabbar .tab').length === 1, null, { timeout: 8000 })
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 草稿必须**跟着页签走**(用户在同一窗口开多个会话,最怕的是切个页签就把没发出去的字弄丢,
  // 或反过来把上一个会话的字发到另一个会话去)。
  test('会话页签:草稿跟着页签走,切过去不串台', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, false)
      page = await open(ctx, stub, docks[0].dock)
      await page.waitForSelector('.tabbar', { timeout: 10000 })
      await page.click('.sidebar .session-item:has-text("session 1")')
      await page.waitForFunction(() => document.querySelectorAll('.tabbar .tab').length === 2, null, { timeout: 8000 })

      const ta = '.input-slot textarea'
      await page.fill(ta, 'draft-for-tab-2')
      // 切到第一个页签:那个页签没有草稿 ⇒ 输入框应为空(不能带过去的字)
      await page.$$eval('.tabbar .tab', (ns) => ns[0].click())
      await page.waitForTimeout(600)
      assert.equal(await page.$eval(ta, (el) => el.value), '', '切到没草稿的页签,输入框不该带上一页的字')

      // 切回来:草稿还在
      await page.$$eval('.tabbar .tab', (ns) => ns[ns.length - 1].click())
      await page.waitForTimeout(600)
      assert.equal(await page.$eval(ta, (el) => el.value), 'draft-for-tab-2', '切回来草稿应还在')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  test('发消息自动切回会话流(变更视图不吞掉回复)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, false), docks[0].dock)
      // 视图按钮循环:会话流 → 轨迹 → 变更。用空态里的「变更」自述确认确实切过去了。
      await page.click('[data-tip^="切换视图"]')
      await page.click('[data-tip^="切换视图"]')
      await page.waitForSelector('.chg .empty', { timeout: 5000 })
      // 非会话流视图必须常驻提醒「消息在会话流」(这条才是真机误判的根治:光靠发消息切回,
      // 切过来之后那一段时间依然看不到回复)。
      const bar = await page.textContent('.vbar-text')
      assert.ok(bar?.includes('会话流'), `非会话流视图应提示消息在会话流: ${bar}`)
      // 发一条消息(桥的 /api/input 走桖的 200 空数组即可 —— 判的是视图,不是回合内容)
      await page.fill('.input-slot textarea', 'hello')
      await page.keyboard.press('Enter')
      await page.waitForFunction(() => !document.querySelector('.chg .empty') && !document.querySelector('.vbar'), null, { timeout: 5000 })
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 回合进行中输入框不得锁死:宿主早已支持把消息注入当前回合(转向),前端一旦把
  // running 当 disabled 传下去,用户按 Enter 什么都不会发生 —— 真机反馈正是
  // 「会话进行时,输入框无法输入」。
  test('回合进行中可输入并 Enter 注入当前回合', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, false, true), docks[0].dock)
      const ta = '.input-slot textarea'
      assert.equal(await page.isDisabled(ta), false, '回合进行中输入框不应被禁用')
      await page.fill(ta, '改成 B 方案')
      await page.keyboard.press('Enter')
      await page.waitForFunction(
        () => (document.querySelector('.stream-slot')?.textContent || '').includes('已注入当前回合'),
        null,
        { timeout: 5000 },
      )
      assert.equal(await page.inputValue(ta), '', '已受理的注入应清空草稿')
      // 运行中必须有中止出口:审批默认不限时等待,没有它就只剩「拒绝」一招。
      assert.ok(await page.isVisible('.stop'), '回合进行中应显示「停止」按钮')
      const [req] = await Promise.all([
        page.waitForRequest((r) => r.url().includes('/api/control')),
        page.click('.stop'),
      ])
      assert.ok((req.postData() || '').includes('"cancel":true'), '停止应发 cancel:true')
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  test('侧栏开合语义:展开只有 .panel,收起只有 .handle', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, apiStub, docks[1].dock)
      const openState = await measure(page)
      assertInvariants(openState)
      assert.equal(
        openState.handles,
        0,
        '侧栏展开时不该有收起态 ☰ 按钮(第五十二批事故:多渲染了一个 height:100% 的按钮把文档撑到 1573px)',
      )
      assert.equal(openState.panels, 1, '侧栏展开时应恰好一个 .panel')
      await page.click('.sidebar .toggle')
      await page.waitForTimeout(400)
      const closedState = await measure(page)
      assertInvariants(closedState)
      assert.equal(closedState.panels, 0, '侧栏收起时不该有 .panel')
      assert.equal(closedState.handles, 1, '侧栏收起时应恰好一个 ☰ 按钮')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 首启态(没配过 provider ⇒ App.vue 自动弹设置面板):遮罩层叠在主视图上时,外壳同样不许滚、
  // 不许有元素被挤出视口 —— 这是新用户第一眼看到的画面,也是最容易"没人测到"的状态。
  for (const vp of [{ w: 1200, h: 800 }, { w: 820, h: 560 }]) {
    test(`${vp.w}x${vp.h} 首启态(设置面板遮罩)`, async (t) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
      let page = null
      try {
        await ctx.addInitScript(() => window.localStorage.setItem('gah.dock', JSON.stringify(docks[1].dock)))
        page = await ctx.newPage()
        await page.route('**/api/**', makeStub(false))
        await page.goto(baseURL(), { waitUntil: 'load' })
        await waitSkeleton(page)
        await page.waitForTimeout(400)
        const m = await measure(page)
        assert.ok(m.hasSettings, '首启态没弹出设置面板(桩没生效或引导逻辑变了)')
        assertInvariants(m)
      } catch (e) {
        await shoot(page, t.name)
        throw e
      } finally {
        await ctx.close()
      }
    })
  }
})

// 导航平滑滚动期间的**高亮归属**(第七十七批后修正):点「插件」时高亮不许中途跑到途经段上
// —— 滚动反查逐帧跑,不锁的话动画一路上会把途经段点亮,动画被拖慢/中断时(CI 的 macOS runner)
// 停下来高亮就停在错的段上(实测断言拿到「指令」而目标是「角色」)。
// 做法:掐掉真实平滑动画 + 手动摆位置,让这条不依赖机器速度 —— 旧实现下第 2 个断言必红。
test('设置面板:导航跳转途中高亮不落到途经段', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    page = await open(ctx, makeStub(true, true, false, false, true), docks[1].dock)
    await page.click('.gear')
    await page.waitForSelector('.nav-it')
    const at = () =>
      page.evaluate(() => {
        const p = document.querySelector('[aria-label="设置"]')
        return p.querySelector('.nav-it.on')?.textContent?.trim() ?? ''
      })
    assert.equal(await at(), '模型', '打开面板应停在首段')
    await page.evaluate(() => {
      Element.prototype.scrollIntoView = () => {} // 不要真实动画(否则本用例变成计时竞速)
      const panel = document.querySelector('[aria-label="设置"]')
      const btn = Array.from(panel.querySelectorAll('.nav-it')).find((b) => b.textContent?.trim() === '插件')
      btn.click()
      // 模拟「平滑滚动途中」:容器现在停在 role 段上并抛一次 scroll(选中间段,避开“滚到底=末段”特判)
      const body = panel.querySelector('.body')
      const mid = body.querySelector('section[data-sec="role"]')
      body.scrollTop += mid.getBoundingClientRect().top - body.getBoundingClientRect().top
      body.dispatchEvent(new Event('scroll'))
    })
    await page.waitForTimeout(80) // 等 rAF 里的反查跑完
    assert.equal(await at(), '插件', `跳转途中高亮跑到了「${await at()}」(应锁在目标段)`)
    // 锁不是永久的:兜底 700ms 到期后交还反查,高亮跟随真实滚动位置(否则用户手滚后高亮会撒谎)
    await page.waitForTimeout(800)
    assert.equal(await at(), '角色', '兜底到期后高亮应跟随真实滚动位置')
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// provider 预设的可见性(2026-10-06 用户反馈:「已添加一个 provider 之后,就没法再用预设添加」)。
//
// 真 bug 的形状:预设 chips 整块包在 `!providers.length` 里 ⇒ 配好第一个之后预设全部消失,
// 而「＋ 新增」打开的表单只能手打 base_url。这条钉住「**有 provider 之后预设依然可点**」,
// 并顺手钉住同名预设的诚实提示(同名保存是更新,不是新增 —— 后端 providerfile.Add 走 mergeFields)。
test('设置面板:已有 provider 后,预设仍能新增(且同名会说明是更新)', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    // makeStub(withProviders=true, longTokens=false) ⇒ /api/providers 返回 1 个名为 'layout' 的 provider。
    // longTokens 必须为 false:它会把 provider 名换成无空格长 token,那样下面「同名冲突」就构造不出来。
    page = await open(ctx, makeStub(true, false, false, false, true), docks[1].dock)
    await page.click('.gear')
    await page.waitForSelector('.nav-it')
    await page.evaluate(() => {
      const panel = document.querySelector('[aria-label="设置"]')
      const btn = Array.from(panel.querySelectorAll('.nav-it')).find((b) => b.textContent?.trim() === 'Provider')
      btn.click()
    })
    await page.waitForSelector('section[data-sec="provider"]')
    // 点「＋ 新增」
    await page.click('section[data-sec="provider"] .h button.link')
    await page.waitForSelector('section[data-sec="provider"] .add-form .chip')
    const chips = await page.$$eval('section[data-sec="provider"] .add-form .chip', (ns) =>
      ns.map((n) => n.textContent.trim()),
    )
    assert.ok(chips.length >= 5, `表单里应有 provider 预设 chips,实得 ${chips.length}`)
    // 点 OpenRouter 预设 → name/base_url 被填好(默认模型带上 openrouter/free)
    await page.click('section[data-sec="provider"] .add-form .chip:has-text("OpenRouter")')
    const vals = await page.evaluate(() => {
      const f = document.querySelector('section[data-sec="provider"] .add-form')
      const ins = f.querySelectorAll('input.inp')
      return { name: ins[0]?.value, url: ins[1]?.value, model: ins[3]?.value }
    })
    assert.equal(vals.name, 'openrouter')
    assert.equal(vals.url, 'https://openrouter.ai/api/v1')
    assert.equal(vals.model, 'openrouter/free')
    // 同名冲突提示:把名称改成已存在的那个(layout),应出现「更新」字样的说明
    await page.evaluate(() => {
      const f = document.querySelector('section[data-sec="provider"] .add-form')
      const inp = f.querySelectorAll('input.inp')[0]
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
      setter.call(inp, 'layout')
      inp.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await page.waitForSelector('section[data-sec="provider"] .add-form p.dim')
    const hint = await page.$eval('section[data-sec="provider"] .add-form p.dim', (n) => n.textContent)
    assert.match(hint, /更新/, `同名保存应说明是更新而不是新增,实得:${hint}`)
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// 拖放落点:此前只有落在输入外壳上才收附件 —— 拖到窗口别处(会话流/侧栏/停靠区)时交给浏览器
// 默认动作,它直接把该文件**导航打开**,当前会话界面被顶掉。这条用真浏览器 + 合成 DataTransfer
// 钉住三件事:① 落到非输入区 → 出现全窗提示、松手后附件进清单、window 层 drop 被 preventDefault;
// ② 落到输入外壳 → 只收一次(输入区自己的 drop 处理与全窗落点不得各收一遍);
// ③ 拖选中文本(非文件)→ 不弹提示也不收。
test('拖放:窗口任意位置都是附件落点(不再被浏览器导航打开)', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  const names = () => page.$$eval('.att-name', (els) => els.map((e) => e.textContent))
  const fileDT = (name) =>
    page.evaluateHandle((n) => {
      const d = new DataTransfer()
      d.items.add(new File(['hello'], n, { type: 'text/plain' }))
      return d
    }, name)
  try {
    page = await open(ctx, apiStub, docks[1].dock)
    // 记录 window 层 drop 的 defaultPrevented —— 「不再导航打开」在合成事件下的可观测代理
    await page.evaluate(() => {
      window.__drops = []
      window.addEventListener('drop', (e) =>
        window.__drops.push({ prevented: e.defaultPrevented, files: e.dataTransfer?.files?.length ?? 0 }),
      )
    })

    // ① 落到会话流(非输入区)
    const dt1 = await fileDT('note.txt')
    await page.dispatchEvent('.stream-slot', 'dragover', { dataTransfer: dt1 })
    await page.waitForSelector('.windrop')
    assert.deepEqual(await names(), [], '拖动中不该提前收附件')
    await page.dispatchEvent('.stream-slot', 'drop', { dataTransfer: dt1 })
    await page.waitForFunction(() => !document.querySelector('.windrop'))
    assert.deepEqual(await names(), ['note.txt'], '拖到非输入区也要收到附件')
    assert.deepEqual(
      await page.evaluate(() => window.__drops),
      [{ prevented: true, files: 1 }],
      'window 层 drop 必须 preventDefault(否则浏览器会导航打开该文件)',
    )

    // ② 落到输入外壳:输入区自己的 drop 已处理 ⇒ 全窗监听不得再收一遍
    const dt2 = await fileDT('second.txt')
    await page.dispatchEvent('.shell', 'dragover', { dataTransfer: dt2 })
    await page.waitForTimeout(80)
    assert.equal(await page.$('.windrop'), null, '落在输入外壳内不该再叠全窗提示(它有自己的一圈高亮)')
    await page.dispatchEvent('.shell', 'drop', { dataTransfer: dt2 })
    await page.waitForTimeout(80)
    assert.deepEqual(await names(), ['note.txt', 'second.txt'], '输入外壳落点只应收集一次')

    // ③ 拖选中文本(非文件)
    const textDT = await page.evaluateHandle(() => {
      const d = new DataTransfer()
      d.setData('text/plain', 'just text')
      return d
    })
    await page.dispatchEvent('.stream-slot', 'dragover', { dataTransfer: textDT })
    await page.dispatchEvent('.stream-slot', 'drop', { dataTransfer: textDT })
    await page.waitForTimeout(80)
    assert.equal(await page.$('.windrop'), null, '非文件拖拽不该弹附件提示')
    assert.deepEqual(await names(), ['note.txt', 'second.txt'], '非文件拖拽不该收附件')
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// 仪器自检:护栏本身失效是最危险的失败模式(全绿但什么都没测)。这里注入一个与 2026-09-22 事故
// 同形的越界元素(侧栏末尾 height:100% —— `.sidebar`/`.app` 无 overflow,故必须被检测到),
// 断言不变量**必须开火**;删掉后必须恢复干净。
test('检测器自检:注入越界元素必须被抓到,移除后必须恢复', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    page = await open(ctx, apiStub, docks[1].dock)
    await page.evaluate(() => {
      const el = document.createElement('div')
      el.className = 'canary-stray'
      el.style.height = '100%'
      el.textContent = 'canary'
      document.querySelector('.sidebar').appendChild(el)
    })
    const dirty = await measure(page)
    const fired = dirty.scrollHeight > dirty.clientHeight + 1 || dirty.scrolled !== 0 || dirty.outsideTotal > 0
    assert.ok(fired, `检测器没开火 —— 护栏本身失效:${JSON.stringify(dirty)}`)
    await page.evaluate(() => document.querySelector('.canary-stray')?.remove())
    assertInvariants(await measure(page))
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// 批零合帧护栏:长会话首屏不得出现长任务。
//
// 为什么要有这条(2026-10-08 实测):会话帧原先是「到达一条即同步消费一条」,每条都触发
// 一次消息列表的 Vue patch;keyed diff 仍要遍历整棵列表做 key 比对 ⇒ 追加一条是 O(n),
// 首屏回放几百帧就是几百次 O(n) 叠加 = O(n²)。实测 800 条消息 = **单个 7.9s 主线程长任务**
// (页面白屏到出内容,期间滚动/点击/动画全部停摆)。改成「入队 + 一个动画帧内批量消费」
// 后同场景 135ms。
//
// 判据为什么用长任务而不是总时长:总时长会被机器快慢与桩响应速度带偏;长任务(>50ms 的
// 不可中断区间)才是「界面冻结」的直接度量,且与帧率无关。
//
// 这条护栏防的是**回归**(有人把合帧去掉、或再加一条逐帧 patch 的路径),不是验收当前实现。
const PERF_TURNS = 400 // 800 条消息,贴 MAX_LIVE_MSGS(=400 条窗口)的两倍 —— 覆盖裁剪路径
const PERF_LONG_TASK_MS = 1000 // 单个长任务上限;实测修复后最大 65ms,留足余量又足够抓回归

test(`批零合帧护栏:${PERF_TURNS * 2} 条消息首屏无 >${PERF_LONG_TASK_MS}ms 长任务`, { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 }, reducedMotion: 'no-preference' })
  let page = null
  try {
    const stub = makeStub(true, false, false, false, false, 'finance', 0, false, true, PERF_TURNS)
    page = await ctx.newPage()
    await page.addInitScript(() => {
      // 收集主线程长任务(>50ms 的不可中断区间)。必须在文档脚本前装,否则漏掉首批。
      window.__long = []
      try {
        performance.setResourceTimingBufferSize(2000)
        new PerformanceObserver((l) => {
          for (const e of l.getEntries()) window.__long.push(Math.round(e.duration))
        }).observe({ entryTypes: ['longtask'] })
      } catch {
        /* 浏览器不支持 longtask:下面按「无长任务」判定,即该护栏退化为不拦人 */
      }
    })
    await page.route('**/api/**', stub)
    const started = Date.now()
    await page.goto(baseURL(), { waitUntil: 'load' })
    // 等消息全部落进 DOM(窗口上限会裁剪,故按「至少一半到位」判定,不给裁剪留歧义)
    await page.waitForFunction((n) => document.querySelectorAll('.msg').length >= n, PERF_TURNS, { timeout: 30_000 })
    const elapsed = Date.now() - started
    const long = await page.evaluate(() => window.__long)
    const worst = long.length ? Math.max(...long) : 0
    assert.ok(
      worst <= PERF_LONG_TASK_MS,
      `首屏出现 ${worst}ms 的主线程长任务(上限 ${PERF_LONG_TASK_MS}ms);` +
        `全部长任务=[${long.join(',')}];共 ${elapsed}ms。` +
        `若合帧(前端 framequeue.ts)被去掉,这里会退化到秒级 —— 那是本护栏要抓的回归。`,
    )
    console.log(`  批零合帧护栏:${PERF_TURNS * 2} 条消息首屏 ${elapsed}ms,最长长任务 ${worst}ms`)
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// 第一百三十四批 · 设置作用域收敛 —— 三条护栏,各钉一件「作用域没说清就会误解」的事。
//
// 为何要护栏:这一批改动全是**文案与徽标**,没有一条逻辑断言能覆盖它;而它坏了不会崩,
// 只会让用户重新产生「我改的到底是全局还是这个页签」那个疑问 —— 正是它要消灭的东西。
// 动效能截图断言,作用域不能,只能钉可观测的不变量(标记出现/不出现、文案在不在)。

// ① 页签方块:会话有独立设置时出现,跟随全局时不出现。
test('作用域:会话级覆盖时页签出方块标记,跟随全局时不出现', { skip: skip && skipWhy }, async (t) => {
  for (const override of [false, true]) {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, false, false, false, false, 'finance', 0, false, true, 0, override)
      page = await ctx.newPage()
      await page.addInitScript(
        ([k, v]) => {
          window.localStorage.setItem(k, v)
          window.sessionStorage.setItem('gah.onboard.auto', '1')
        },
        ['gah.dock', JSON.stringify(docks[1].dock)],
      )
      await page.route('**/api/**', stub)
      await page.goto(baseURL(), { waitUntil: 'load' })
      await waitSkeleton(page)
      await page.waitForTimeout(400)
      const has = await page.evaluate(() => !!document.querySelector('.tabbar .tab .custom'))
      assert.equal(
        has,
        override,
        `sessionOverride=${override} 时页签方块应 ${override ? '出现' : '不出现'};` +
          `形状选方块正是为了与运行脉冲点/未读点(两个圆点)区分开。`,
      )
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  }
})

// ② 本会话设置面板:二态表达必须跟着来源走 —— 跟随时不摆复位按钮(没得复位就别给按钮)。
test('作用域:本会话设置面板按来源显示跟随/独立与复位入口', { skip: skip && skipWhy }, async (t) => {
  for (const override of [false, true]) {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, false, false, false, false, 'finance', 0, false, true, 0, override)
      page = await ctx.newPage()
      await page.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
      await page.route('**/api/**', stub)
      await page.goto(baseURL(), { waitUntil: 'load' })
      await waitSkeleton(page)
      // 入口在**右上角**(第一百三十六批从输入框工具条移来):用 .gear.ses 选中,
      // 若哪天又跑回工具条,这条会直接找不到元素。
      await page.click('.statusbar-slot .gear.ses')
      await page.waitForSelector('.scp')
      const d = await page.evaluate(() => ({
        summary: document.querySelector('[data-testid="scp-summary"]')?.textContent?.trim() ?? '',
        indep: document.querySelectorAll('.scp-tag:not(.scp-tag-off)').length,
        reset: document.querySelectorAll('.scp-reset').length,
        secs: document.querySelectorAll('.scp-sec').length,
      }))
      assert.equal(d.secs, 4, `本会话设置应有四段(模型/思考/沙箱/审批),实际 ${d.secs}`)
      assert.equal(d.indep, override ? 2 : 0, `sessionOverride=${override} 时独立标记数应为 ${override ? 2 : 0},实际 ${d.indep}`)
      assert.equal(d.reset, override ? 2 : 0, `复位入口只在该项独立时出现,实际 ${d.reset}`)
      if (override) assert.ok(d.summary.includes('独立'), `独立时摘要要说清:${d.summary}`)
      else assert.ok(d.summary.includes('跟随'), `跟随时摘要要说清:${d.summary}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  }
})

// ③ 设置面板的模型段必须写明作用域:控件改的是全局默认。
// 这条防的是最贵的那种回归 —— 面板改回会话档而界面照旧说「全局默认」,用户就会
// 「改了全局却只有这个页签变了」,比改前更糟。
test('作用域:设置面板模型段写明「改的是全局默认」', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    page = await ctx.newPage()
    await page.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
    await page.route('**/api/**', makeStub(true))
    await page.goto(baseURL(), { waitUntil: 'load' })
    await waitSkeleton(page)
    await page.click('.gear')
    await page.waitForSelector('[data-sec="model"]')
    const notes = await page.$$eval('[data-sec="model"] .scope-note, [data-sec="reason"] .scope-note', (ns) =>
      ns.map((n) => n.textContent?.replace(/\s+/g, ' ').trim() ?? ''),
    )
    assert.equal(notes.length, 2, `模型段与推理段都该有作用域声明,实际 ${notes.length} 处`)
    for (const n of notes) {
      assert.ok(n.includes('全局默认'), `作用域声明须点明「全局默认」:${n}`)
    }
    // 「本会话在用」那行也要在 —— 只说改哪里、不说当前是什么,仍然答不上「那我用的啥」
    const cur = await page.textContent('[data-testid="cur-model"]')
    assert.ok(cur && cur.includes('本会话在用'), `模型段应显示本会话实际在用的值:${cur}`)
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// ④ 当前会话设置入口必须在**右上角**（状态栏右端、与其它 gear 用分隔线隔开），且本会话独立时
// 用计数徽标点明。这条防三件事：入口又跑回输入框工具条（用户反馈“不够明显”的原始问题）、
// 标题名改回短名（用户要求“当前会话设置”）、以及“独立了但界面上看不出来”（与页签方块/状态栏前缀同一口径）。
test('作用域:当前会话设置入口单独靠右、独立项数以徽标点明', { skip: skip && skipWhy }, async (t) => {
  for (const override of [false, true]) {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await ctx.newPage()
      await page.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
      await page.route('**/api/**', makeStub(true, false, false, false, false, 'finance', 0, false, true, 0, override))
      await page.goto(baseURL(), { waitUntil: 'load' })
      await waitSkeleton(page)
      const d = await page.evaluate(() => {
        const slot = document.querySelector('.statusbar-slot')
        const btn = slot?.querySelector('.gear.ses')
        const kids = slot ? [...slot.children] : []
        return {
          inTopRight: !!btn,
          inComposer: [...document.querySelectorAll('.input-slot .ctl')].some((b) => (b.textContent || '').includes('会话设置')),
          badge: document.querySelector('.statusbar-slot .gear.ses .ses-badge')?.textContent?.trim() ?? '',
          label: (btn?.textContent || '').replace(/\s+/g, ''),
          // 右对齐 = 它是状态栏槽位的**最后一个**子元素;单独放置 = 前面紧着一个分隔线
          isLast: kids.length > 0 && kids[kids.length - 1] === btn,
          hasSep: !!btn && btn.previousElementSibling?.classList.contains('gear-sep'),
        }
      })
      assert.ok(d.inTopRight, '当前会话设置入口应在右上角状态栏(.statusbar-slot .gear.ses)')
      assert.ok(!d.inComposer, '会话设置入口不应再出现在输入框工具条里(第一百三十六批已移走)')
      assert.ok(d.label.includes('当前会话设置'), `入口标题应为「当前会话设置」,实际 "${d.label}"`)
      assert.ok(d.isLast, '入口应右对齐(是状态栏槽位的最后一个元素)')
      assert.ok(d.hasSep, '入口应与其它 gear 按钮用分隔线隔开(单独放置)')
      assert.equal(d.badge, override ? '2' : '', `sessionOverride=${override} 时徽标应为 ${override ? '2' : '空'},实际 "${d.badge}"`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  }
})

// ⑤ 会话级模型必须能**真正设上**(不只是能清除)。
// 缺口原状：设置面板的模型段只写全局（session:""），本会话面板只有「改为跟随全局」——
// “只让这个页签用另一个模型”在界面层完全做不到。这条钉住选中的是**会话档**而不是全局。
test('作用域:本会话设置面板可单独选模型(写会话档)', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    const stub = makeStub(true, false, false, false, false, 'finance', 0, false, true, 0, false, true)
    page = await ctx.newPage()
    await page.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
    await page.route('**/api/**', stub)
    await page.goto(baseURL(), { waitUntil: 'load' })
    await waitSkeleton(page)
    await page.click('.statusbar-slot .gear.ses')
    await page.waitForSelector('.scp')
    await page.waitForSelector('[data-testid="scp-models"]')
    const n = await page.$$eval('[data-testid="scp-models"] .scp-opt', (els) => els.length)
    assert.ok(n >= 2, `本会话模型列表应列出当前 provider 的模型,实际 ${n} 条`)
    await page.click('[data-testid="scp-models"] .scp-opt:has-text("other-model")')
    const hit = stub.seen.find(
      (r) => r.path === '/api/control' && (r.body || '').includes('layout-guard/other-model'),
    )
    assert.ok(hit, `选模型应提交 /api/control:${JSON.stringify(stub.seen)}`)
    assert.ok((hit.body || '').includes('"session"'), `必须是会话档(带 session)而不是全局:${hit.body}`)
    assert.ok(!(hit.body || '').includes('"also_global"'), `会话档不该顺手改全局:${hit.body}`)
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})


// ⑥ 会话级角色：右上角面板能单独给这个页签选角色（写会话档）。
// 这条同时钉住一个真 bug（第一百三十八批）：主会话占位 'main' 必须解析成服务端**真实会话 id**
// —— 绑占位时请求里的 session 是空串 = 全局档，表现为「设了角色却仍跟随全局」。
test('作用域:当前会话设置可单独选角色(写会话档,非空 session)', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    const stub = makeStub(true, false, false, false, true, 'finance', 0, false, true, 0, false, false, 'layout-main')
    page = await ctx.newPage()
    await page.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
    await page.route('**/api/**', stub)
    await page.goto(baseURL(), { waitUntil: 'load' })
    await waitSkeleton(page)
    await page.click('.statusbar-slot .gear.ses')
    await page.waitForSelector('[data-testid="scp-roles"]')
    const secs = await page.$$eval('.scp .scp-sec', (els) => els.length)
    assert.equal(secs, 5, `本会话面板应有五段(角色/模型/思考/沙箱/审批),实际 ${secs}`)
    await page.click('[data-testid="scp-roles"] .scp-opt:has-text("Assistant")')
    const hit = stub.seen.find((r) => r.method === 'POST' && r.path === '/api/roles/assistant/use')
    assert.ok(hit, `选角色应提交 /api/roles/assistant/use:${JSON.stringify(stub.seen)}`)
    assert.equal(JSON.parse(hit.body || '{}').session, 'layout-main', `必须写当前会话档(真实 id),实际 ${hit.body}`)
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// ⑦ 设置面板的角色切换是**全局默认**（与模型/思考/沙箱/审批同一作用域纪律）。
// 防回归：roleUse 不给 session 时默认绑当前页签，设置面板会变成“第二个会话级入口”。
test('作用域:设置面板切换角色写全局(session 为空)', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    const stub = makeStub(true, false, false, false, true, 'finance', 0, false, true, 0, false, false, 'layout-main')
    page = await ctx.newPage()
    await page.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
    await page.route('**/api/**', stub)
    await page.goto(baseURL(), { waitUntil: 'load' })
    await waitSkeleton(page)
    await page.click('.gear')
    await page.waitForSelector('[data-sec="role"]')
    const note = await page.textContent('[data-sec="role"] .scope-note')
    assert.ok(note && note.includes('全局默认'), `角色段应声明改的是全局默认:${note}`)
    await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("切换")')
    const hit = stub.seen.find((r) => r.method === 'POST' && r.path === '/api/roles/assistant/use')
    assert.ok(hit, `设置面板切换角色应提交:${JSON.stringify(stub.seen)}`)
    assert.equal(JSON.parse(hit.body || '{}').session, '', `设置面板应写全局(session 为空),实际 ${hit.body}`)
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// ⑧ 关闭再打开设置面板：滚回**上次停留的段**（不仅是高亮留着）。
// 用户报的「内容回滚到最上面、选项还选中着之前的」：正文 DOM 随 v-if 销毁重建、scrollTop 归零，
// 而 activeSec 仍停在旧段。
test('设置面板:关开后滚回上次停留的段', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    page = await ctx.newPage()
    await page.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
    await page.route('**/api/**', makeStub(true, true))
    await page.goto(baseURL(), { waitUntil: 'load' })
    await waitSkeleton(page)
    const gap = () =>
      page.evaluate(() => {
        const body = document.querySelector('.panel .body')
        const sec = body?.querySelector('section[data-sec="schedule"]')
        if (!body || !sec) return null
        return Math.round(sec.getBoundingClientRect().top - body.getBoundingClientRect().top)
      })
    await page.click('.gear')
    await page.waitForSelector('[data-sec="schedule"]')
    await page.click('.nav button:has-text("计划")')
    await page.waitForTimeout(900) // 平滑滚动落定
    const before = await gap()
    assert.ok(before !== null && Math.abs(before) <= 40, `跳转后「计划」段未对齐顶部:${before}`)
    await page.click('.panel .head .x')
    await page.waitForTimeout(200)
    await page.click('.gear')
    await page.waitForSelector('[data-sec="schedule"]')
    await page.waitForTimeout(300)
    const after = await gap()
    assert.ok(after !== null && Math.abs(after) <= 40, `关开后面板没滚回「计划」段(内容在顶部、高亮却停着):${after}`)
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// ⑨ 「改用选择器」必须真的回到选择器：早先只清空输入框、粘贴区还开着，空输入框下看不出变化 = 无反应。
test('设置面板:计划「改用选择器」关闭自定义表达式区', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    page = await ctx.newPage()
    await page.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
    await page.route('**/api/**', makeStub(true, true))
    await page.goto(baseURL(), { waitUntil: 'load' })
    await waitSkeleton(page)
    await page.click('.gear')
    await page.waitForSelector('[data-sec="schedule"]')
    // 自定义表达式区在「新增计划」表单里 —— 先展开表单
    await page.click('[data-sec="schedule"] button:has-text("新增")')
    await page.click('[data-sec="schedule"] button:has-text("从别处粘贴排期表达式")')
    await page.waitForSelector('[data-sec="schedule"] input[placeholder="0 9 * * 1-5"]')
    await page.click('[data-sec="schedule"] button:has-text("改用选择器")')
    await page.waitForTimeout(200)
    const stillOpen = await page.$('[data-sec="schedule"] input[placeholder="0 9 * * 1-5"]')
    assert.equal(stillOpen, null, '点了「改用选择器」后自定义表达式输入框应消失(回到选择器)')
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// ⑩ 本会话设置写档失败必须**说出来**(2026-10-09 Windows 真机反馈「点击无法选中」)。
// 原先三处写档都是裸 await:后端 4xx ⇒ Promise 拒绝无人接收 ⇒ 界面零反馈,表现正是
// 「点了没反应」;桌面壳还没有终端,连错误文本都拿不到(壳日志里也不会有)。
// 这条钉两件事:失败可见(带后端原始文本) + 不假装成功(高亮不跟着动)。
test('作用域:本会话设置写档失败时就地报错(不再静默)', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    const stub = makeStub(true)
    page = await ctx.newPage()
    await page.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
    // 路由按**后注册者优先**匹配:先挂全量桩,再用更具体的 /api/control 覆写它。
    await page.route('**/api/**', stub)
    await page.route('**/api/control', (r) =>
      r.fulfill({
        status: 400,
        contentType: 'text/plain',
        body: '会话级思考档设置失败: meta.json 被占用',
      }),
    )
    await page.goto(baseURL(), { waitUntil: 'load' })
    await waitSkeleton(page)
    await page.click('.statusbar-slot .gear.ses')
    await page.waitForSelector('.scp')
    const onBefore = await page.$eval('.scp-seg', (el) => el.querySelector('.scp-it.on')?.textContent?.trim() ?? '')
    await page.click('.scp-it:has-text("高")')
    await page.waitForSelector('[data-testid="scp-err"]')
    const txt = (await page.textContent('[data-testid="scp-err"]')) || ''
    assert.ok(txt.includes('没生效'), `失败必须说清哪一项没生效:${txt}`)
    assert.ok(txt.includes('meta.json 被占用'), `失败必须带上后端的原始错误文本(排查全靠它):${txt}`)
    const onAfter = await page.$eval('.scp-seg', (el) => el.querySelector('.scp-it.on')?.textContent?.trim() ?? '')
    assert.equal(onAfter, onBefore, `写档失败后高亮不得变化(否则等于谎报成功):${onBefore} → ${onAfter}`)
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

// ⑪ 刷新/重开后恢复出来的页签若**就是真实会话 id**,请求层必须跟着绑过去(而不是停在 ''= 全局档)。
// 这条钉一个实测真 bug(2026-10-09):api 绑定原先只在「首次校准」分支里做,而恢复出真实 id 时
// `calibrated` 一上来就是 true ⇒ 分支永不执行 ⇒ 「本会话设置」的每次写都静默落成全局档
// (实测 POST /api/control 的 session 变成 "")。
test('作用域:刷新后恢复的页签仍写会话档(api 绑定不丢)', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    // sessionStorage 里的 gah.tabs = 上一轮留下的真实会话 id(刷新的实况形状)
    const stub = makeStub(true, false, false, false, false, 'finance', 0, false, true, 0, false, false, 'layout-main')
    page = await ctx.newPage()
    await page.addInitScript(() => {
      window.sessionStorage.setItem('gah.onboard.auto', '1')
      window.sessionStorage.setItem('gah.tabs', JSON.stringify({ ids: ['layout-restored'], active: 'layout-restored' }))
    })
    await page.route('**/api/**', stub)
    await page.goto(baseURL(), { waitUntil: 'load' })
    await waitSkeleton(page)
    await page.click('.statusbar-slot .gear.ses')
    await page.waitForSelector('.scp')
    await page.click('.scp-it:has-text("高")')
    const hit = stub.seen.find((r) => r.path === '/api/control')
    assert.ok(hit, `本会话设置的写档应提交 /api/control:${JSON.stringify(stub.seen)}`)
    assert.equal(JSON.parse(hit.body || '{}').session, 'layout-restored', `恢复的页签必须写自己的会话档,实际 ${hit.body}`)
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

test('布局护栏:跳过原因(仅在没有浏览器/产物时输出)', { skip: !skip }, () => {
  console.log(`  跳过:${skipWhy}`)
})
// CI 的 ubuntu-latest(24.04)那两个参数能不能救场,本地验不了;但"什么失败该重试"这个判定
// 是纯函数,拿真实报错文案钉住 —— 既不让沙箱失败白挂,也不让装错浏览器被重试掩盖。
test('无沙箱重试判定:只对沙箱/namespace 类失败生效(ubuntu-latest 24.04 的 AppArmor)', () => {
  assert.ok(shouldRetryWithoutSandbox('No usable sandbox! If you are running on Ubuntu 23.10+ or another Linux distro...'))
  assert.ok(shouldRetryWithoutSandbox('Failed to move to new namespace: PID namespaces supported, Network namespace supported'))
  assert.ok(shouldRetryWithoutSandbox('Running as root without --no-sandbox is not supported'))
  assert.ok(!shouldRetryWithoutSandbox("Chromium distribution 'chrome' is not found at /opt/google/chrome/chrome"))
  assert.ok(!shouldRetryWithoutSandbox('spawn /usr/bin/google-chrome ENOENT'))
})
