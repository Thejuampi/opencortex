<script>
  import { onMount } from 'svelte'
  import { marked } from 'marked'

  let route = window.location.pathname
  let stats = {}
  let feed = []
  let knowledge = []
  let messages = []
  let agents = []
  let remotes = []
  let conflicts = []
  let selectedKnowledge = null
  let apiKey = localStorage.getItem('opencortex_api_key') || ''
  let wsState = 'disconnected'
  let ws

  const tabs = [
    ['/', 'Dashboard'],
    ['/knowledge', 'Knowledge'],
    ['/messages', 'Messages'],
    ['/agents', 'Agents'],
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
    return apiKey ? { Authorization: `Bearer ${apiKey}` } : {}
  }

  async function fetchJSON(path) {
    const res = await fetch(`/api/v1${path}`, { headers: authHeaders() })
    const body = await res.json()
    if (!body.ok) return null
    return body.data
  }

  async function loadData() {
    if (route === '/') {
      const statData = await fetchJSON('/admin/stats')
      stats = statData?.stats || {}
    }
    if (route.startsWith('/knowledge')) {
      const k = await fetchJSON('/knowledge?limit=30')
      knowledge = k?.knowledge || []
      if (!selectedKnowledge && knowledge.length > 0) selectedKnowledge = knowledge[0]
    }
    if (route.startsWith('/messages')) {
      const m = await fetchJSON('/messages?limit=30')
      messages = m?.messages || []
    }
    if (route.startsWith('/agents')) {
      const a = await fetchJSON('/agents?per_page=50')
      agents = a?.agents || []
    }
    if (route.startsWith('/sync')) {
      const r = await fetchJSON('/sync/remotes')
      const c = await fetchJSON('/sync/conflicts')
      remotes = r?.remotes || []
      conflicts = c?.conflicts || []
    }
  }

  function connectWS() {
    if (!apiKey) return
    const protocol = location.protocol === 'https:' ? 'wss' : 'ws'
    ws = new WebSocket(`${protocol}://${location.host}/api/v1/ws?api_key=${encodeURIComponent(apiKey)}`)
    ws.onopen = () => {
      wsState = 'connected'
      ws.send(JSON.stringify({ type: 'ping' }))
    }
    ws.onclose = () => {
      wsState = 'disconnected'
      setTimeout(connectWS, 1500)
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
    localStorage.setItem('opencortex_api_key', apiKey)
    connectWS()
    loadData()
  }

  onMount(() => {
    loadData()
    connectWS()
  })

  $: renderedMarkdown = selectedKnowledge?.content ? marked.parse(selectedKnowledge.content) : ''
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

  {#if route === '/'}
    <section class="dashboard">
      <article class="hero panel">
        <h2>Live Activity</h2>
        <p>High-throughput local-first agent coordination with embedded realtime channels.</p>
      </article>
      <div class="card-grid">
        <article class="panel">
          <h3>Agents</h3>
          <p>{stats.agents || 0}</p>
        </article>
        <article class="panel">
          <h3>Messages</h3>
          <p>{stats.messages || 0}</p>
        </article>
        <article class="panel">
          <h3>Knowledge Entries</h3>
          <p>{stats.knowledge_entries || 0}</p>
        </article>
        <article class="panel">
          <h3>Open Conflicts</h3>
          <p>{stats.sync_conflicts || 0}</p>
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

  {#if route === '/settings'}
    <section class="panel settings">
      <h2>Settings</h2>
      <label>
        API Key
        <input type="password" bind:value={apiKey} placeholder="amk_live_..." />
      </label>
      <button on:click={saveKey}>Save</button>
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

  .dashboard { display: grid; gap: 1rem; }
  .hero p { color: #d4dcef; }
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

  .row {
    display: flex;
    justify-content: space-between;
    align-items: center;
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