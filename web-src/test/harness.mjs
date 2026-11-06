// 布局护栏与性能脚手架的共享测试基座(2026-11-06 从 layout.test.mjs 抽出):
// 静态服务 / API 桩 / Chrome 启动。抽出的理由:perf.test.mjs 要复用同一批 fixtures ——
// 两处各复制一份 550 行桩,第一个漂移就会让「性能数字」和「布局断言」跑在不同的 API 上。
import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'

export const MIME = {
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
//
// onRequest(req, res) 可选:返回 true 表示"这条我处理了",静态服务不再插手。
// 为什么要它:桩走 `page.route` 只能**一次性 fulfill**(body 是静态串),要真流(SSE 逐帧下发、
// 用来量稳态追加成本)必须落到真 http 上 —— 见 perf.test.mjs。
export function startStatic(root, onRequest) {
  const server = http.createServer((req, res) => {
    if (onRequest && onRequest(req, res)) return
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
export function makeStub(
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
  // extraSse:连接时一并下发的**额外帧**（`[{ event, payload }]`）。
  // 为什么只能"连接时发":桩的 SSE body 是一次性 fulfill 的静态串（见下面 /api/events），
  // 想要"先开页、later 再来一帧"得把它改成真流 —— 那点复杂度换不来更多判据，
  // 而介入条要验的是「帧接上了没有」，不是时序。
  extraSse = [],
  // roleProblems:角色列表的「部分角色文件读不了」提示(宿主 v.problems)。
  // 之前桩里没有这个字段,于是模板里 roleProblems 的渲染分支**一次都没被真实浏览器跑过** ——
  // 而拆分设置面板时正好在这一段引入过「把 ref 当数组用」的运行时缺陷。
  roleProblems = [],
) {
  // seen:记录写类请求(方法/路径/体),供角色面板用例断言「面板真的提交了」而不是只改了本地状态。
  const seen = []
  // serverCurrentId 服务端**当前会话** id:非空时 /api/state 恒下发它(与 ?session= 无关)。
  // 可变:用例可中途改它,模拟「别的端/命令切走当前会话」而不需重启页面。
  let serverCurrentId = mainSessionId
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
  const memQueries = []
  const memView = (st) => ({
    enabled: st.enabled,
    budget: 2048,
    user: st.user,
    project: st.project,
    // 过滤/观测(P2):面板要显示"过滤中 N/M"、自动生成与自动调取的最近一次状态。
    counts: { user: st.user.length, project: st.project.length, auto: 1, stale: 0 },
    filter: st.filter ?? undefined,
    extract: {
      enabled: true,
      at: '2026-11-03T10:00:00Z',
      rule: '长期指令',
      produced: 1,
      written: 1,
      candidate: 0,
      dropped: 0,
      ledger: '提取账本:今日请求 1/30,直写 1/10,候选 0;累计 请求 1 / 空 0 / 失败 0',
    },
    retrieve: { mode: 'per_step', query: '怎么部署', items: 1, total: 1, bytes: 120, truncated: false, at: '2026-11-03T10:00:00Z' },
    tool_remember: 2,
    // 全量来源(不受过滤影响):"按来源整段删"要靠它 —— 与真实后端同源,由用户级行推导。
    sources: Array.from(
      new Set(
        st.user
          .map((l) => {
            const m = /\(来源:\s*会话\s+(\S+?)\s*\)/.exec(String(l))
            return m ? m[1] : ''
          })
          .filter(Boolean),
      ),
    ),
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
    // json 必须**先声明**:/api/events 的 turns=0 分支(下面几行)就会用到它 ——
    // 原来它声明在分支之后,等于把那句 return 放进 TDZ(2026-11-06 抽出基座时 node 明确报
    // `Cannot access 'json' before initialization`,而此前只是每条用例跑完冒一次
    // unhandledRejection ⇒ 一直没被当成缺陷)。
    const json = (v, status = 200) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(v) })
    // 事件流(EventSource):返回一段 text/event-stream body。
    // 浏览器会把 body 里的多个 event **逐个 dispatch**(每个 event 一个任务)—— 这正好
    // 复现「几百个会话帧一次性到达」的真实形状,是批零合帧护栏能立住的前提。
    // turns=0(默认)时不接管,走下面的 JSON 兜底(其余用例看不到会话流,行为不变)。
    if (p === '/api/events') {
      const turns = Number(url.searchParams.get('turns') || 0) || sseTurns
      // turns=0:回一条**空的事件流**(200 + text/event-stream),不是 JSON。
      // 先前的 `json([])` 是致命错:EventSource 收到 application/json ⇒ readyState 直接 CLOSED
      // ⇒ 前端判「链路已断」,输入框禁用 ⇒ 所有要打字的用例全红(2026-11-06 修掉 TDZ 后暴露)。
      // 空 SSE 的语义才是对的:连接建立、没有帧、EOF 后浏览器自动重连(与真实后方 idle 一致)。
      if (!turns) {
        return route.fulfill({
          status: 200,
          contentType: 'text/event-stream; charset=utf-8',
          headers: { 'Cache-Control': 'no-cache' },
          body: '',
        })
      }
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
      for (const x of extraSse) {
        frames.push(`event: ${x.event}\ndata: ${JSON.stringify({ id: seq, type: x.event, ts: Date.now(), payload: x.payload })}\n\n`)
        seq++
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
        problems: roleProblems,
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
      if (route.request().method() === 'GET') memQueries.push(url.search)
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
        // session.id **恒为服务端当前会话**(与 ?session= 无关)—— 真实后端 handleState 如此。
        // 别让它回显请求参数:那正是第一四〇批前端 P1(非当前会话页签的弹层永不弹)长期没被
        // 用例拦住的根因(W3:测试桩替产品说假话)。前端要的是「本页签的会话」时用页签键
        // (App.vue 的 mainTabKey / askedForTab),不是 state.session.id。
        ...(serverCurrentId
          ? { session: { id: serverCurrentId, name: '主会话', path: '/tmp/' + serverCurrentId + '.jsonl', key: 'k-' + serverCurrentId } }
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
    // 命令注册表(输入区 "/" 提示 + 逐级参数选择):键盘候选靠这两个端点。
    if (p === '/api/commands') {
      return json([
        { name: 'status', usage: '/status', desc: '看状态' },
        { name: 'sample', usage: '/sample <alpha|beta>', desc: '演示参数级' },
      ])
    }
    if (p === '/api/commands/sample/options') {
      let picked = []
      try {
        picked = route.request().postDataJSON()?.picked ?? []
      } catch {
        picked = []
      }
      if (Array.isArray(picked) && picked.length === 0) {
        return json({
          items: [
            { value: 'alpha', desc: '甲' },
            { value: 'beta', desc: '乙' },
          ],
          freeArgs: [],
          done: false,
        })
      }
      return json({ items: [], freeArgs: [], done: true })
    }
    if (p === '/api/commands/status/options') return json({ items: [], freeArgs: [], done: true })
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
  handler.memQueries = memQueries
  handler.eventReqs = eventReqs
  handler.toolQueries = toolQueries
  handler.packPosts = packPosts
  handler.memState = memState
  // setServerCurrent 切走服务端当前会话(模拟别的端/命令改动;见 W1 用例)。
  handler.setServerCurrent = (id) => {
    serverCurrentId = id
  }
  return handler
}

// launchOpts args 非空时一并带上(重试路径用)。依次尝试:显式路径 → 系统 Chrome → 常见 Linux 路径
// → 交给 Playwright 的 channel 解析(它自己的表就是 linux `/opt/google/chrome/chrome`、
// darwin `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,与 CI 镜像给的位置一致)。
export function launchOpts(args) {
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
export function shouldRetryWithoutSandbox(msg) {
  return /sandbox|namespace/i.test(String(msg))
}

// launchBrowser 先按标准参数启动;若失败且形如沙箱问题,再带 --no-sandbox 重试一次。
export async function launchBrowser(chromium) {
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

