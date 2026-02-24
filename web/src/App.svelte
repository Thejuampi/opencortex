<script>
  import { onMount } from 'svelte'
  import { marked } from 'marked'

  let route = window.location.pathname
  let stats = {}
  let statsRestricted = false
  let feed = []
  let knowledge = []
  let messages = []
  let agents = []
  let skills = []
  let remotes = []
  let conflicts = []
  let selectedKnowledge = null
  let selectedSkill = null
  let selectedSkillHistory = []
  let selectedSkillVersion = null
  let skillSearch = ''
  let skillInstallPlatform = 'all'
  let skillInstallForce = false
  let skillNotice = ''
  let currentAgent = null
  let apiKey = localStorage.getItem('opencortex_api_key') || ''
  let wsState = 'disconnected'
  let ws
  let bootstrapStatus = null
  let bootstrapPoller
  let uiDebug = localStorage.getItem('opencortex_ui_debug') === '1'
  let diagnostics = []
  let localAuthAttempted = false

  const tabs = [
    ['/', 'Dashboard'],
    ['/knowledge', 'Knowledge'],
    ['/messages', 'Messages'],
    ['/agents', 'Agents'],
    ['/skills', 'Skills'],
    ['/sync', 'Sync'],
    ['/settings', 'Settings']
  ]

  function goto(path) {
    history.pushState({}, '', path)
    route = path
    loadData()
  }

  window.addEventListener('popstate', () => {
    route = window.location.pathname
    loadData()
  })

  function authHeaders() {
    const key = apiKey.trim()
    return key ? { Authorization: `Bearer ${key}` } : {}
  }

  function logUI(event, data = null) {
    const row = {
      at: new Date().toISOString(),
      event,
      data
    }
    diagnostics = [row, ...diagnostics].slice(0, 120)
    if (uiDebug) {
      // Keep logs visible in browser devtools for backend mismatch debugging.
      console.log('[opencortex-ui]', row)
    }
  }

  async function fetchEnvelope(path, options = {}) {
    const req = { ...options }
    req.headers = { ...(options.headers || {}), ...authHeaders() }
    if (req.body && typeof req.body !== 'string') {
      req.headers['Content-Type'] = 'application/json'
      req.body = JSON.stringify(req.body)
    }
    const hasAuth = Boolean(req.headers.Authorization)
    logUI('fetch:start', { path, has_auth: hasAuth, route })
    let res
    try {
      res = await fetch(`/api/v1${path}`, req)
    } catch (err) {
      logUI('fetch:error', { path, message: err?.message || 'network error' })
      return { status: 0, ok: false, data: null, error: { code: 'NETWORK_ERROR', message: 'network error' }, pagination: null }
    }
    let body = null
    try {
      body = await res.json()
    } catch {
      body = null
    }
    logUI('fetch:done', { path, status: res.status, ok: body?.ok === true, error: body?.error || null })
    return {
      status: res.status,
      ok: body?.ok === true,
      data: body?.data || null,
      error: body?.error || null,
      pagination: body?.pagination || null
    }
  }

  async function fetchJSON(path, options = {}) {
    const env = await fetchEnvelope(path, options)
    if (!env.ok) return null
    return env.data
  }

  async function tryLocalWebAuth() {
    if (apiKey.trim() || localAuthAttempted) return false
    localAuthAttempted = true
    const env = await fetchEnvelope('/web/auth/local-admin', { method: 'POST', body: {} })
    if (!env.ok || !env.data?.api_key) {
      logUI('local-auth:failed', { status: env.status, error: env.error || null })
      return false
    }
    apiKey = String(env.data.api_key || '').trim()
    if (!apiKey) {
      return false
    }
    localStorage.setItem('opencortex_api_key', apiKey)
    logUI('local-auth:ok', { agent: env.data?.agent?.name || 'unknown' })
    connectWS()
    return true
  }

  function totalFromPagination(env) {
    if (!env?.pagination) return null
    const total = env.pagination.total
    return Number.isFinite(total) ? total : null
  }

  async function loadDashboardStats() {
    statsRestricted = false
    const statEnv = await fetchEnvelope('/admin/stats')
    if (statEnv.ok) {
      stats = statEnv.data?.stats || {}
      return
    }
    if (statEnv.status !== 403) {
      stats = {}
      return
    }

    statsRestricted = true
    const [aEnv, mEnv, kEnv, cEnv] = await Promise.all([
      fetchEnvelope('/agents?per_page=1'),
      fetchEnvelope('/messages?limit=1'),
      fetchEnvelope('/knowledge?limit=1'),
      fetchEnvelope('/sync/conflicts')
    ])
    stats = {
      agents: aEnv.ok ? totalFromPagination(aEnv) : null,
      messages: mEnv.ok ? totalFromPagination(mEnv) : null,
      knowledge_entries: kEnv.ok ? totalFromPagination(kEnv) : null,
      sync_conflicts: cEnv.ok ? (cEnv.data?.conflicts || []).length : null
    }
  }

  async function loadSkills(query = '') {
    const q = query.trim()
    const path = q ? `/skills?limit=50&q=${encodeURIComponent(q)}` : '/skills?limit=50'
    const env = await fetchEnvelope(path)
    if (!env.ok) {
      skills = []
      selectedSkill = null
      selectedSkillHistory = []
      selectedSkillVersion = null
      return
    }
    skills = env.data?.skills || []
    if (!selectedSkill || !skills.find((s) => s.id === selectedSkill.id)) {
      selectedSkill = skills[0] || null
    }
    if (selectedSkill) {
      await loadSkill(selectedSkill.id)
    }
  }

  async function loadSkill(id) {
    selectedSkillHistory = []
    selectedSkillVersion = null
    const env = await fetchEnvelope(`/skills/${encodeURIComponent(id)}`)
    if (env.ok) {
      selectedSkill = env.data?.skill || null
    }
  }

  async function loadSkillHistory() {
    if (!selectedSkill) return
    const env = await fetchEnvelope(`/skills/${encodeURIComponent(selectedSkill.id)}/history`)
    if (env.ok) {
      selectedSkillHistory = env.data?.history || []
    }
  }

  async function loadSkillVersion(version) {
    if (!selectedSkill || !version) return
    const env = await fetchEnvelope(`/skills/${encodeURIComponent(selectedSkill.id)}/versions/${encodeURIComponent(version)}`)
    if (env.ok) {
      selectedSkillVersion = env.data?.version || null
    }
  }

  async function runSkillAction(method, path, body = null) {
    skillNotice = ''
    const env = await fetchEnvelope(path, { method, body })
    if (!env.ok) {
      const code = env.error?.code || 'ERROR'
      const msg = env.error?.message || 'request failed'
      skillNotice = `${code}: ${msg}`
      return null
    }
    return env.data
  }

  async function createSkill() {
    const title = window.prompt('Skill title')
    if (!title) return
    const slug = window.prompt('Slug (optional, defaults from title)') || ''
    const repo = window.prompt('Install repo (owner/repo)')
    if (!repo) return
    const path = window.prompt('Install path inside repo')
    if (!path) return
    const content = window.prompt('Skill content (markdown)', '# Skill\n')
    if (!content) return
    const data = await runSkillAction('POST', '/skills', {
      title,
      slug,
      content,
      install: {
        repo,
        path,
        ref: 'main',
        method: 'auto'
      }
    })
    if (data?.skill?.id) {
      selectedSkill = data.skill
      await loadSkills(skillSearch)
    }
  }

  async function updateSkillContent() {
    if (!selectedSkill) return
    const content = window.prompt('New content', selectedSkill.content || '')
    if (!content) return
    const note = window.prompt('Change note (optional)') || ''
    const body = { content }
    if (note) body.change_note = note
    const data = await runSkillAction('PUT', `/skills/${encodeURIComponent(selectedSkill.id)}`, body)
    if (data?.skill) {
      selectedSkill = data.skill
      await loadSkills(skillSearch)
    }
  }

  async function patchSkillMetadata() {
    if (!selectedSkill) return
    const slug = window.prompt('New slug (optional)', selectedSkill.slug || '') || ''
    const summary = window.prompt('Summary (optional)', selectedSkill.summary || '') || ''
    const body = {}
    if (slug.trim()) body.slug = slug.trim()
    if (summary.trim()) body.summary = summary.trim()
    if (Object.keys(body).length === 0) {
      skillNotice = 'No metadata changes provided.'
      return
    }
    const data = await runSkillAction('PATCH', `/skills/${encodeURIComponent(selectedSkill.id)}`, body)
    if (data?.skill) {
      selectedSkill = data.skill
      await loadSkills(skillSearch)
    }
  }

  async function toggleSkillPin() {
    if (!selectedSkill) return
    const method = selectedSkill.is_pinned ? 'DELETE' : 'POST'
    const data = await runSkillAction(method, `/skills/${encodeURIComponent(selectedSkill.id)}/pin`, {})
    if (data?.skill) {
      selectedSkill = data.skill
      await loadSkills(skillSearch)
    }
  }

  async function deleteSkill() {
    if (!selectedSkill) return
    const ok = window.confirm(`Delete skill "${selectedSkill.title}"?`)
    if (!ok) return
    const data = await runSkillAction('DELETE', `/skills/${encodeURIComponent(selectedSkill.id)}`)
    if (data?.deleted) {
      selectedSkill = null
      selectedSkillHistory = []
      selectedSkillVersion = null
      await loadSkills(skillSearch)
    }
  }

  async function installSkillFromServer() {
    if (!selectedSkill) return
    const data = await runSkillAction('POST', `/skills/${encodeURIComponent(selectedSkill.id)}/install`, {
      target: 'global',
      platform: skillInstallPlatform,
      force: skillInstallForce
    })
    if (data?.result) {
      const warnings = data.result.warnings || []
      skillNotice = `Installed to ${data.result.canonical_path}${warnings.length ? ` (${warnings.length} warning(s))` : ''}`
    }
  }

  async function loadData() {
    logUI('load:start', { route, has_api_key: Boolean(apiKey.trim()) })
    if (route === '/') {
      try {
        const res = await fetch('/api/v1/bootstrap/status')
        const body = await res.json()
        logUI('bootstrap:status', { http: res.status, ok: body?.ok === true, status: body?.data?.status || null })
        bootstrapStatus = body?.ok ? body?.data?.status : null
      } catch {
        logUI('bootstrap:error')
        bootstrapStatus = null
      }
    }
    if (!apiKey.trim()) {
      if (await tryLocalWebAuth()) {
        return loadData()
      }
      logUI('load:no-api-key', { route })
      stats = {}
      statsRestricted = false
      knowledge = []
      messages = []
      agents = []
      skills = []
      remotes = []
      conflicts = []
      selectedKnowledge = null
      selectedSkill = null
      selectedSkillHistory = []
      selectedSkillVersion = null
      currentAgent = null
      return
    }
    const me = await fetchEnvelope('/agents/me')
    if (!me.ok && me.status === 401) {
      apiKey = ''
      localStorage.removeItem('opencortex_api_key')
      localAuthAttempted = false
      if (await tryLocalWebAuth()) {
        return loadData()
      }
    }
    currentAgent = me.ok ? me.data?.agent || null : null
    if (route === '/') {
      await loadDashboardStats()
      logUI('stats:update', { agents: stats.agents || 0, messages: stats.messages || 0, knowledge: stats.knowledge_entries || 0 })
    }
    if (route.startsWith('/knowledge')) {
      const k = await fetchJSON('/knowledge?limit=30')
      knowledge = k?.knowledge || []
      logUI('knowledge:update', { count: knowledge.length })
      if (!selectedKnowledge && knowledge.length > 0) selectedKnowledge = knowledge[0]
    }
    if (route.startsWith('/messages')) {
      const m = await fetchJSON('/messages?limit=30')
      messages = m?.messages || []
      logUI('messages:update', { count: messages.length })
    }
    if (route.startsWith('/agents')) {
      const a = await fetchJSON('/agents?per_page=50&status=active')
      agents = a?.agents || []
      logUI('agents:update', { count: agents.length })
    }
    if (route.startsWith('/skills')) {
      await loadSkills(skillSearch)
      logUI('skills:update', { count: skills.length })
    }
    if (route.startsWith('/sync')) {
      const r = await fetchJSON('/sync/remotes')
      const c = await fetchJSON('/sync/conflicts')
      remotes = r?.remotes || []
      conflicts = c?.conflicts || []
      logUI('sync:update', { remotes: remotes.length, conflicts: conflicts.length })
    }
  }

  function connectWS() {
    const key = apiKey.trim()
    if (!key) {
      wsState = 'disconnected'
      logUI('ws:skip-no-api-key')
      return
    }
    const protocol = location.protocol === 'https:' ? 'wss' : 'ws'
    const wsURL = `${protocol}://${location.host}/api/v1/ws?api_key=${encodeURIComponent(key)}`
    logUI('ws:connect', { url: wsURL.replace(key, '***') })
    ws = new WebSocket(wsURL)
    ws.onopen = () => {
      wsState = 'connected'
      logUI('ws:open')
      ws.send(JSON.stringify({ type: 'ping' }))
    }
    ws.onclose = () => {
      wsState = 'disconnected'
      logUI('ws:close')
      setTimeout(connectWS, 1500)
    }
    ws.onerror = () => {
      logUI('ws:error')
    }
    ws.onmessage = (ev) => {
      try {
        const m = JSON.parse(ev.data)
        if (m.type === 'message' || m.type === 'knowledge_updated' || m.type === 'agent_status') {
          feed = [m, ...feed].slice(0, 40)
        }
      } catch {}
    }
  }

  function saveKey() {
    apiKey = apiKey.trim()
    localStorage.setItem('opencortex_api_key', apiKey)
    if (!apiKey) {
      localAuthAttempted = false
    }
    logUI('settings:save-key', { has_api_key: Boolean(apiKey) })
    connectWS()
    loadData()
  }

  function saveDebug() {
    localStorage.setItem('opencortex_ui_debug', uiDebug ? '1' : '0')
    logUI('settings:debug', { enabled: uiDebug })
  }

  onMount(() => {
    loadData()
    connectWS()
    bootstrapPoller = setInterval(() => {
      if (route === '/') loadData()
    }, 2000)
    return () => {
      if (bootstrapPoller) clearInterval(bootstrapPoller)
    }
  })

  $: renderedMarkdown = selectedKnowledge?.content ? marked.parse(selectedKnowledge.content) : ''
  $: renderedSkillMarkdown = selectedSkill?.content ? marked.parse(selectedSkill.content) : ''
</script>

<svelte:window on:keydown={(e) => {
  if (e.key === 'Escape' && route !== '/') goto('/')
}} />

<main class="app-shell">
  <div class="bg-shape one"></div>
  <div class="bg-shape two"></div>
  <header class="topbar">
    <div class="brand">
      <span class="dot"></span>
      <h1>Opencortex</h1>
    </div>
    <nav>
      {#each tabs as tab}
        <button class:active={route === tab[0]} on:click={() => goto(tab[0])}>{tab[1]}</button>
      {/each}
    </nav>
    <div class="ws-pill {wsState}">{wsState}</div>
  </header>

  {#if !apiKey.trim()}
    <article class="panel notice">
      <h3>Disconnected</h3>
      <p>No API key configured. Open Settings, set your key, and save to load agents, knowledge, messages, and skills.</p>
      <button on:click={() => goto('/settings')}>Open Settings</button>
    </article>
  {/if}

  {#if route === '/'}
    <section class="dashboard">
      <article class="hero panel">
        <h2>Live Activity</h2>
        <p>High-throughput local-first agent coordination with embedded realtime channels.</p>
      </article>
      {#if bootstrapStatus}
        <article class="panel ready">
          <h3>Ready</h3>
          <div class="ready-grid">
            <p>Server: <strong>{bootstrapStatus.server_url || 'starting...'}</strong></p>
            <p>MCP: <strong>{bootstrapStatus.copilot_mcp_configured && bootstrapStatus.codex_mcp_configured ? 'configured' : 'pending'}</strong></p>
            <p>VSCode: <strong>{bootstrapStatus.vscode_extension_installed ? 'installed' : 'not detected'}</strong></p>
          </div>
          <p class="hint">Waiting for agents to connect...</p>
          <pre><code>opencortex send --to self "hello"
opencortex inbox</code></pre>
        </article>
      {/if}
      <article class="panel now-what">
        <h3>Now What?</h3>
        <p class="hint">Tell your agents to follow this quick flow on the same host.</p>
        <ol>
          <li>
            Register each agent with a stable profile and keep that profile name forever.
            <pre><code>opencortex --agent-profile planner auth whoami
opencortex --agent-profile reviewer auth whoami</code></pre>
          </li>
          <li>
            Save knowledge from one agent.
            <pre><code>opencortex --agent-profile planner knowledge add --title "Release Plan" --file ./release-plan.md --tags plan,release</code></pre>
          </li>
          <li>
            Query knowledge from another agent.
            <pre><code>opencortex --agent-profile reviewer knowledge search "release plan"</code></pre>
          </li>
          <li>
            Share skills as special knowledge.
            <pre><code>opencortex --agent-profile planner skills add --title "PR Review Skill" --slug pr-review --file ./SKILL.md --repo owner/repo --path skills/pr-review
opencortex --agent-profile reviewer skills install pr-review --target repo</code></pre>
          </li>
          <li>
            Coordinate with direct and broadcast messages.
            <pre><code>opencortex --agent-profile planner send --to reviewer "Review release plan v3"
opencortex --agent-profile reviewer inbox --wait --ack
opencortex --agent-profile planner broadcast "Release planning knowledge updated"</code></pre>
          </li>
          <li>
            Sync with a remote hub when ready.
            <pre><code>opencortex --agent-profile planner sync remote add origin https://hub.example.com --key amk_remote_xxx
opencortex --agent-profile planner sync diff origin</code></pre>
          </li>
        </ol>
      </article>
      <div class="card-grid">
        <article class="panel">
          <h3>Agents</h3>
          <p>{stats.agents ?? (statsRestricted ? 'restricted' : 0)}</p>
        </article>
        <article class="panel">
          <h3>Messages</h3>
          <p>{stats.messages ?? (statsRestricted ? 'restricted' : 0)}</p>
        </article>
        <article class="panel">
          <h3>Knowledge Entries</h3>
          <p>{stats.knowledge_entries ?? (statsRestricted ? 'restricted' : 0)}</p>
        </article>
        <article class="panel">
          <h3>Open Conflicts</h3>
          <p>{stats.sync_conflicts ?? (statsRestricted ? 'restricted' : 0)}</p>
        </article>
      </div>
      <article class="panel feed">
        <h3>Realtime Feed</h3>
        {#if feed.length === 0}
          <p>No live events yet.</p>
        {:else}
          <ul>
            {#each feed as item}
              <li><strong>{item.type}</strong> {JSON.stringify(item.data || {}).slice(0, 160)}</li>
            {/each}
          </ul>
        {/if}
      </article>
    </section>
  {/if}

  {#if route === '/knowledge'}
    <section class="split two-col">
      <aside class="panel list">
        <h3>Knowledge</h3>
        {#each knowledge as entry}
          <button class:selected={selectedKnowledge?.id === entry.id} on:click={() => (selectedKnowledge = entry)}>{entry.title}</button>
        {/each}
      </aside>
      <article class="panel detail">
        {#if selectedKnowledge}
          <h2>{selectedKnowledge.title}</h2>
          <div class="meta">version {selectedKnowledge.version} · tags {selectedKnowledge.tags?.join(', ')}</div>
          <div class="markdown">{@html renderedMarkdown}</div>
        {:else}
          <p>Select an entry.</p>
        {/if}
      </article>
    </section>
  {/if}

  {#if route === '/messages'}
    <section class="panel list">
      <h2>Message Explorer</h2>
      {#if messages.length === 0}<p>No inbox items.</p>{/if}
      {#each messages as msg}
        <article class="row">
          <div>{msg.content}</div>
          <span>{msg.priority}</span>
        </article>
      {/each}
    </section>
  {/if}

  {#if route === '/agents'}
    <section class="panel list">
      <h2>Agents</h2>
      {#if agents.length === 0}<p>No agents found.</p>{/if}
      {#each agents as a}
        <article class="row">
          <div>
            <strong>{a.name}</strong>
            <span class="sub">{a.id}</span>
          </div>
          <span class="status {a.status}">{a.status}</span>
        </article>
      {/each}
    </section>
  {/if}

  {#if route === '/sync'}
    <section class="split two-col">
      <article class="panel list">
        <h2>Remotes</h2>
        {#if remotes.length === 0}<p>No remotes configured.</p>{/if}
        {#each remotes as r}
          <article class="row">
            <div>{r.remote_name}</div>
            <span>{r.scope}</span>
          </article>
        {/each}
      </article>
      <article class="panel list">
        <h2>Conflicts</h2>
        {#if conflicts.length === 0}<p>No open conflicts.</p>{/if}
        {#each conflicts as c}
          <article class="row">
            <div>{c.entity_type} / {c.entity_id}</div>
            <span>{c.strategy}</span>
          </article>
        {/each}
      </article>
    </section>
  {/if}

  {#if route === '/skills'}
    <section class="split two-col">
      <aside class="panel list">
        <h3>Skills</h3>
        <div class="row-controls">
          <input placeholder="Search skills..." bind:value={skillSearch} />
          <button on:click={() => loadSkills(skillSearch)}>Search</button>
          <button on:click={createSkill}>Add</button>
        </div>
        {#if skills.length === 0}
          <p>No skills found.</p>
        {/if}
        {#each skills as skill}
          <button class:selected={selectedSkill?.id === skill.id} on:click={() => loadSkill(skill.id)}>
            <strong>{skill.title}</strong>
            <span class="sub">{skill.slug}</span>
          </button>
        {/each}
      </aside>
      <article class="panel detail">
        {#if selectedSkill}
          <h2>{selectedSkill.title}</h2>
          <div class="meta">slug {selectedSkill.slug} · version {selectedSkill.version} · pinned {selectedSkill.is_pinned ? 'yes' : 'no'}</div>
          <div class="row-controls">
            <button on:click={toggleSkillPin}>{selectedSkill.is_pinned ? 'Unpin' : 'Pin'}</button>
            <button on:click={updateSkillContent}>Update Content</button>
            <button on:click={patchSkillMetadata}>Patch Metadata</button>
            <button class="danger" on:click={deleteSkill}>Delete</button>
          </div>
          <div class="row-controls">
            <label>
              Install platform
              <select bind:value={skillInstallPlatform}>
                <option value="all">all</option>
                <option value="codex">codex</option>
                <option value="copilot">copilot</option>
                <option value="claude">claude</option>
              </select>
            </label>
            <label class="checkbox">
              <input type="checkbox" bind:checked={skillInstallForce} />
              force
            </label>
            <button on:click={installSkillFromServer}>Install (server/global)</button>
            <button on:click={loadSkillHistory}>History</button>
            <button on:click={() => loadSkillVersion(window.prompt('Version number'))}>Version</button>
          </div>
          {#if skillNotice}
            <p class="hint">{skillNotice}</p>
          {/if}
          {#if selectedSkillVersion}
            <article class="panel version">
              <h3>Version {selectedSkillVersion.version}</h3>
              <pre><code>{selectedSkillVersion.content}</code></pre>
            </article>
          {/if}
          {#if selectedSkillHistory.length > 0}
            <article class="panel version">
              <h3>History</h3>
              <ul>
                {#each selectedSkillHistory as item}
                  <li>v{item.version} · {item.created_at} · {item.changed_by}</li>
                {/each}
              </ul>
            </article>
          {/if}
          <div class="markdown">{@html renderedSkillMarkdown}</div>
        {:else}
          <p>Select a skill.</p>
        {/if}
      </article>
    </section>
  {/if}

  {#if route === '/settings'}
    <section class="panel settings">
      <h2>Settings</h2>
      <label>
        API Key
        <input type="password" bind:value={apiKey} placeholder="amk_live_..." />
      </label>
      <button on:click={saveKey}>Save</button>
      <label class="checkbox">
        <input type="checkbox" bind:checked={uiDebug} on:change={saveDebug} />
        Enable UI diagnostics logging
      </label>
      <div class="diag-meta">
        <p>Current agent: <code>{currentAgent?.name || 'unknown'}</code></p>
        <p>Current agent id: <code>{currentAgent?.id || 'unknown'}</code></p>
        <p>Backend origin: <code>{location.origin}</code></p>
        <p>API base: <code>{location.origin}/api/v1</code></p>
        <p>Route: <code>{route}</code></p>
        <p>WS: <code>{wsState}</code></p>
      </div>
      <article class="diag-log">
        <h3>Diagnostics</h3>
        {#if diagnostics.length === 0}
          <p>No logs yet.</p>
        {:else}
          <ul>
            {#each diagnostics as row}
              <li>
                <strong>{row.at}</strong> {row.event}
                {#if row.data}
                  <code>{JSON.stringify(row.data)}</code>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      </article>
      <p>Stored locally and used for API + WebSocket auth.</p>
    </section>
  {/if}
</main>

<style>
  :global(body) {
    margin: 0;
    font-family: 'Space Grotesk', 'IBM Plex Sans', sans-serif;
    color: #f9fafc;
    background: radial-gradient(circle at 15% 20%, #0f4c5c, #0f172a 45%, #070b14 100%);
    min-height: 100vh;
  }

  .app-shell {
    position: relative;
    min-height: 100vh;
    padding: 1rem;
    overflow: hidden;
  }

  .bg-shape {
    position: fixed;
    border-radius: 999px;
    filter: blur(55px);
    opacity: 0.25;
    pointer-events: none;
    animation: drift 12s ease-in-out infinite alternate;
  }

  .bg-shape.one {
    width: 320px;
    height: 320px;
    background: #f2c14e;
    top: -80px;
    right: -60px;
  }

  .bg-shape.two {
    width: 260px;
    height: 260px;
    background: #32b3a8;
    left: -60px;
    bottom: -40px;
  }

  @keyframes drift {
    from { transform: translateY(-8px); }
    to { transform: translateY(14px); }
  }

  .topbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1rem;
    backdrop-filter: blur(12px);
    padding: .85rem;
    border: 1px solid rgba(255,255,255,0.16);
    border-radius: 16px;
    background: rgba(255,255,255,0.06);
    animation: fadeIn .35s ease-out;
  }

  .brand { display: flex; align-items: center; gap: .6rem; }
  .brand h1 { margin: 0; font-size: 1rem; letter-spacing: .06em; text-transform: uppercase; }
  .dot { width: 10px; height: 10px; border-radius: 999px; background: #ffd166; box-shadow: 0 0 12px #ffd166; }

  nav { display: flex; flex-wrap: wrap; gap: .4rem; }
  nav button {
    border: 1px solid rgba(255,255,255,0.14);
    background: rgba(255,255,255,0.06);
    color: #e4ebff;
    border-radius: 999px;
    padding: .45rem .8rem;
    cursor: pointer;
    transition: .2s;
  }
  nav button:hover, nav button.active {
    background: #f2c14e;
    color: #1f222b;
    transform: translateY(-1px);
  }

  .ws-pill {
    border-radius: 999px;
    padding: .25rem .6rem;
    font-size: .8rem;
    border: 1px solid rgba(255,255,255,0.15);
  }
  .ws-pill.connected { background: rgba(84, 214, 116, 0.2); }
  .ws-pill.disconnected { background: rgba(255, 107, 107, 0.2); }

  .panel {
    border: 1px solid rgba(255,255,255,0.14);
    border-radius: 16px;
    background: rgba(255,255,255,0.06);
    backdrop-filter: blur(8px);
    padding: 1rem;
    animation: rise .25s ease-out;
  }

  @keyframes rise {
    from { opacity: 0; transform: translateY(6px); }
    to { opacity: 1; transform: translateY(0); }
  }
  .notice {
    margin-bottom: 1rem;
  }
  .notice button {
    border: none;
    border-radius: 10px;
    background: #f2c14e;
    color: #1c1f29;
    font-weight: 700;
    padding: .45rem .8rem;
    cursor: pointer;
  }

  .dashboard { display: grid; gap: 1rem; }
  .hero p { color: #d4dcef; }
  .ready .ready-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: .5rem;
  }
  .ready pre {
    margin: .6rem 0 0;
    background: rgba(0,0,0,0.25);
    border: 1px solid rgba(255,255,255,0.12);
    border-radius: 10px;
    padding: .6rem;
    overflow: auto;
  }
  .ready .hint { color: #d4dcef; margin: .3rem 0; }
  .now-what .hint { color: #d4dcef; margin: .2rem 0 .8rem; }
  .now-what ol {
    margin: 0;
    padding-left: 1.2rem;
    display: grid;
    gap: .6rem;
  }
  .now-what li {
    line-height: 1.35;
    color: #edf2ff;
  }
  .now-what pre {
    margin: .4rem 0 0;
    background: rgba(0,0,0,0.25);
    border: 1px solid rgba(255,255,255,0.12);
    border-radius: 10px;
    padding: .55rem;
    overflow: auto;
  }
  .card-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: .75rem;
  }
  .card-grid p { font-size: 1.8rem; margin: 0; }

  .feed ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: .35rem;
    max-height: 260px;
    overflow: auto;
  }
  .feed li {
    padding: .45rem;
    background: rgba(0,0,0,0.2);
    border-radius: 8px;
    border: 1px solid rgba(255,255,255,0.1);
    font-size: .9rem;
  }

  .split {
    display: grid;
    gap: .85rem;
  }
  .two-col { grid-template-columns: minmax(220px, 30%) 1fr; }

  .list button, .row {
    width: 100%;
    text-align: left;
    margin-bottom: .45rem;
    border: 1px solid rgba(255,255,255,0.08);
    background: rgba(255,255,255,0.04);
    color: #e8efff;
    border-radius: 10px;
    padding: .6rem;
  }
  .list button.selected { border-color: #f2c14e; background: rgba(242,193,78,0.15); }
  .row-controls {
    display: flex;
    gap: .5rem;
    flex-wrap: wrap;
    margin-bottom: .55rem;
    align-items: center;
  }
  .row-controls button {
    border: 1px solid rgba(255,255,255,0.15);
    background: rgba(255,255,255,0.08);
    color: #eff3ff;
    border-radius: 9px;
    padding: .4rem .65rem;
    cursor: pointer;
  }
  .row-controls .danger {
    border-color: rgba(255, 107, 107, 0.45);
    background: rgba(255, 107, 107, 0.2);
  }
  .row-controls label {
    display: flex;
    align-items: center;
    gap: .35rem;
  }
  .row-controls select {
    border: 1px solid rgba(255,255,255,0.2);
    background: rgba(255,255,255,0.08);
    color: #f0f5ff;
    border-radius: 8px;
    padding: .35rem .45rem;
  }

  .row {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .version {
    margin: .55rem 0;
    padding: .65rem;
  }
  .version h3 {
    margin: 0 0 .45rem;
    font-size: .95rem;
  }
  .version pre {
    margin: 0;
    background: rgba(0,0,0,0.25);
    border: 1px solid rgba(255,255,255,0.1);
    border-radius: 10px;
    padding: .55rem;
    white-space: pre-wrap;
    overflow: auto;
    max-height: 220px;
  }

  .detail .meta { color: #c4d2f7; margin-bottom: .8rem; font-size: .85rem; }
  .markdown :global(h1),
  .markdown :global(h2),
  .markdown :global(h3) {
    margin-top: .6rem;
  }

  .status {
    text-transform: uppercase;
    font-size: .75rem;
    border: 1px solid rgba(255,255,255,0.2);
    border-radius: 999px;
    padding: .2rem .5rem;
  }
  .status.active { background: rgba(84,214,116,0.2); }
  .status.inactive { background: rgba(255,190,92,0.2); }
  .status.banned { background: rgba(255,107,107,0.2); }

  .settings {
    max-width: 560px;
    margin: 0 auto;
    display: grid;
    gap: .7rem;
  }
  .settings .checkbox {
    display: flex;
    align-items: center;
    gap: .5rem;
    color: #d9e4ff;
  }
  .diag-meta {
    background: rgba(0,0,0,0.18);
    border: 1px solid rgba(255,255,255,0.12);
    border-radius: 10px;
    padding: .55rem .65rem;
  }
  .diag-meta p {
    margin: .15rem 0;
    font-size: .85rem;
  }
  .diag-meta code {
    font-family: 'IBM Plex Mono', Consolas, monospace;
    color: #cce4ff;
  }
  .diag-log {
    background: rgba(0,0,0,0.18);
    border: 1px solid rgba(255,255,255,0.12);
    border-radius: 10px;
    padding: .55rem .65rem;
  }
  .diag-log h3 {
    margin: 0 0 .45rem 0;
    font-size: .95rem;
  }
  .diag-log ul {
    margin: 0;
    padding: 0;
    list-style: none;
    max-height: 180px;
    overflow: auto;
    display: grid;
    gap: .35rem;
  }
  .diag-log li {
    font-size: .8rem;
    border: 1px solid rgba(255,255,255,0.1);
    background: rgba(255,255,255,0.04);
    border-radius: 8px;
    padding: .4rem .45rem;
    color: #d9e4ff;
  }
  .diag-log code {
    display: block;
    margin-top: .25rem;
    font-family: 'IBM Plex Mono', Consolas, monospace;
    white-space: pre-wrap;
    word-break: break-word;
    color: #bad8ff;
  }
  label { display: grid; gap: .35rem; }
  input {
    border: 1px solid rgba(255,255,255,0.2);
    background: rgba(255,255,255,0.08);
    color: white;
    border-radius: 10px;
    padding: .6rem;
  }
  .settings button {
    width: fit-content;
    border: none;
    border-radius: 10px;
    background: #f2c14e;
    color: #1c1f29;
    font-weight: 700;
    padding: .55rem .9rem;
    cursor: pointer;
  }

  .sub { display: block; color: #9fb0d8; font-size: .75rem; }

  @media (max-width: 860px) {
    .two-col { grid-template-columns: 1fr; }
    .topbar { flex-direction: column; align-items: stretch; }
  }
</style>
