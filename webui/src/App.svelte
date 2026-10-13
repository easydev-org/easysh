<script lang="ts">
  // App — the root: a top bar (session picker, theme), a terminal surface, and
  // mutually-exclusive Jobs / History drawers. Mirrors the worker panel's layout.
  import { onMount } from 'svelte'
  import { Button } from '$lib/components/ui/button'
  import { AppIcons } from '$lib/icons'
  import TerminalPage from '$lib/pages/TerminalPage.svelte'
  import JobsDrawer from '$lib/pages/JobsDrawer.svelte'
  import HistoryDrawer from '$lib/pages/HistoryDrawer.svelte'
  import { api, type SessionInfo } from '$lib/api'

  let drawer = $state<'jobs' | 'history' | null>(null)
  let sessions = $state<SessionInfo[]>([])
  let sessionName = $state('default')
  let dir = $state('')
  let connected = $state(false)
  let theme = $state<'dark' | 'light'>('dark')

  onMount(() => {
    theme = (sessionStorage.getItem('easysh_theme') as 'dark' | 'light') ?? 'dark'
    document.documentElement.dataset.theme = theme
    refreshSessions()
  })

  async function refreshSessions() {
    try {
      sessions = await api.sessions()
      const s = sessions.find((x) => x.name === sessionName) ?? sessions[0]
      if (s) {
        sessionName = s.name
        dir = s.dir
      }
      connected = true
    } catch {
      connected = false
    }
  }

  function pickSession(e: Event) {
    sessionName = (e.target as HTMLSelectElement).value
    const s = sessions.find((x) => x.name === sessionName)
    if (s) dir = s.dir
    drawer = null
  }

  async function newSession() {
    const name = prompt('New session name:')
    if (!name) return
    try {
      const s = await api.newSession(name)
      await refreshSessions()
      sessionName = s.name
      dir = s.dir
    } catch (err) {
      alert(String((err as Error)?.message ?? err))
    }
  }

  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark'
    document.documentElement.dataset.theme = theme
    sessionStorage.setItem('easysh_theme', theme)
  }

  function toggle(which: 'jobs' | 'history') {
    drawer = drawer === which ? null : which
  }
</script>

<div class="flex h-dvh flex-col overflow-hidden bg-background">
  <header class="flex h-12 shrink-0 items-center gap-2 border-b border-border bg-card px-3">
    <span class="flex items-center gap-2 font-semibold">
      <span
        class="flex size-5 items-center justify-center rounded-md bg-primary font-mono text-[10px] font-bold text-primary-foreground"
        >es</span
      >
      easysh
    </span>
    <select
      class="rounded border border-border bg-card px-1.5 py-0.5 font-mono text-micro text-foreground"
      value={sessionName}
      onchange={pickSession}
      title="session"
    >
      {#each sessions as s (s.name)}
        <option value={s.name}>{s.name} ({s.jobs})</option>
      {/each}
    </select>
    <button
      class="text-micro text-muted-foreground hover:text-foreground"
      onclick={newSession}
      title="new session">+ session</button
    >
    <span class="hidden truncate font-mono text-micro text-muted-foreground sm:inline">{dir}</span>
    {#if !connected}
      <span class="text-micro text-destructive">daemon unreachable</span>
    {/if}
    <span class="ml-auto"></span>
    <Button
      variant="ghost"
      size="sm"
      class="gap-1.5 {drawer === 'history' ? 'bg-muted text-foreground' : ''}"
      onclick={() => toggle('history')}
    >
      <AppIcons.history class="size-4" /><span class="max-sm:hidden">History</span>
    </Button>
    <Button
      variant="ghost"
      size="sm"
      class="gap-1.5 {drawer === 'jobs' ? 'bg-muted text-foreground' : ''}"
      onclick={() => toggle('jobs')}
    >
      <AppIcons.jobs class="size-4" />Jobs
    </Button>
    <Button variant="ghost" size="icon" onclick={toggleTheme} title="toggle theme">
      {#if theme === 'dark'}<AppIcons.sun class="size-4" />{:else}<AppIcons.moon class="size-4" />{/if}
    </Button>
  </header>

  <div class="relative flex min-h-0 flex-1">
    {#key sessionName}
      <TerminalPage {sessionName} />
    {/key}
    {#if drawer === 'jobs'}
      <aside
        class="flex min-h-0 w-full flex-col border-border bg-card max-sm:absolute max-sm:inset-0 max-sm:z-10 sm:w-1/2 sm:border-l"
      >
        <JobsDrawer onClose={() => (drawer = null)} />
      </aside>
    {:else if drawer === 'history'}
      <aside
        class="flex min-h-0 w-full flex-col border-border bg-card max-sm:absolute max-sm:inset-0 max-sm:z-10 sm:w-1/2 sm:border-l"
      >
        <HistoryDrawer {sessionName} onClose={() => (drawer = null)} />
      </aside>
    {/if}
  </div>
</div>
