<script lang="ts">
  // HistoryDrawer — the session's typed-command history, kept in the daemon (and
  // SQLite), shared by every terminal on the session. Click a line to re-run it
  // in the terminal.
  import { onMount } from 'svelte'
  import { Button } from '$lib/components/ui/button'
  import { AppIcons } from '$lib/icons'
  import { api } from '$lib/api'

  let { sessionName, onClose }: { sessionName: string; onClose: () => void } = $props()

  let lines = $state<string[]>([])
  let filter = $state('')
  let timer: number | undefined
  let copied = $state<number | null>(null) // index just copied, for feedback

  async function load() {
    try {
      lines = (await api.history(sessionName)).history ?? []
    } catch {
      lines = []
    }
  }

  // Newest first for display; filter is a case-insensitive substring.
  const shown = $derived(
    lines
      .map((l, i) => ({ l, i }))
      .filter((x) => !filter || x.l.toLowerCase().includes(filter.toLowerCase()))
      .reverse(),
  )

  // copyCmd puts a history line on the clipboard (we never auto-run it; the
  // terminal is where a command should be executed, so the shell's own history
  // and echoing stay single-sourced).
  async function copyCmd(cmd: string, idx: number) {
    try {
      await navigator.clipboard.writeText(cmd)
    } catch {
      // clipboard may be blocked; ignore
    }
    copied = idx
    setTimeout(() => {
      if (copied === idx) copied = null
    }, 1200)
  }

  onMount(() => {
    load()
    timer = window.setInterval(load, 3000)
    return () => window.clearInterval(timer)
  })
</script>

<div class="flex h-8 shrink-0 items-center gap-2 border-b border-border px-3 text-meta">
  <AppIcons.history class="size-4" /><span class="font-semibold">History</span>
  <span class="text-micro text-muted-foreground">{lines.length}</span>
  <span class="ml-auto"></span>
  <Button variant="ghost" size="sm" onclick={load}>refresh</Button>
  <Button variant="ghost" size="icon" onclick={onClose} title="close">
    <AppIcons.close class="size-4" />
  </Button>
</div>

<div class="shrink-0 border-b border-border p-2">
  <input
    class="w-full rounded border border-border bg-background px-2 py-1 font-mono text-meta text-foreground outline-none focus:border-primary"
    placeholder="filter…"
    bind:value={filter}
  />
</div>

<div class="min-h-0 flex-1 overflow-y-auto">
  {#each shown as { l, i } (i)}
    <div
      class="group flex w-full items-start gap-2 border-b border-border px-3 py-1.5 hover:bg-muted"
    >
      <span class="shrink-0 font-mono text-micro text-muted-foreground">{i + 1}</span>
      <span class="term-line min-w-0 flex-1">{l}</span>
      <button
        class="shrink-0 rounded border border-border px-1.5 py-0.5 text-micro transition-colors
          {copied === i ? 'border-success text-success' : 'text-muted-foreground hover:text-foreground'}"
        title="copy to clipboard"
        onclick={() => copyCmd(l, i)}
      >
        {copied === i ? 'copied' : 'copy'}
      </button>
    </div>
  {/each}
  {#if shown.length === 0}
    <p class="p-3 text-meta text-muted-foreground">No history yet.</p>
  {/if}
</div>
