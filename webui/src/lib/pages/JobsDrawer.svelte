<script lang="ts">
  // JobsDrawer — list jobs, view their output (watch live via SSE), and inspect
  // the dataflow graph. The graph view is easysh's differentiator: every stage's
  // stdin/stdout/stderr, every pipe/redirect, with each intermediate result
  // readable by clicking a flow.
  import { onMount } from 'svelte'
  import { Button } from '$lib/components/ui/button'
  import { AppIcons } from '$lib/icons'
  import { api, stripAnsi, type JobInfo } from '$lib/api'
  import FlowGraph from './FlowGraph.svelte'

  let { onClose }: { onClose: () => void } = $props()

  let jobs = $state<JobInfo[]>([])
  let loading = $state(false)
  let toast = $state('')
  let selected = $state<string | null>(null)
  let output = $state('')
  let watching = $state(false)
  let view = $state<'output' | 'graph'>('output')
  let es: EventSource | null = null
  let timer: number | undefined

  function say(m: string) {
    toast = m
    setTimeout(() => (toast = ''), 2500)
  }

  async function load() {
    loading = true
    try {
      jobs = await api.jobs('default')
    } catch (e) {
      say(String((e as Error)?.message ?? e))
    }
    loading = false
  }

  async function select(id: string) {
    selected = id
    es?.close()
    watching = false
    output = stripAnsi(await api.output(id))
    if (view === 'output') {
      const j = jobs.find((x) => x.id === id)
      if (j && (j.state === 'running' || j.state === 'detached')) watch(id)
    }
  }

  function watch(id: string) {
    es?.close()
    output = ''
    watching = true
    es = api.watch(
      id,
      (d) => {
        output += stripAnsi(d)
      },
      () => {
        watching = false
        load()
      },
    )
  }

  async function kill(id: string) {
    try {
      await api.kill(id)
      say('killed ' + id)
      load()
    } catch (e) {
      say(String((e as Error)?.message ?? e))
    }
  }

  onMount(() => {
    load()
    timer = window.setInterval(load, 2000)
    return () => {
      window.clearInterval(timer)
      es?.close()
    }
  })

  const stateClass = (s: string) =>
    s === 'done'
      ? 'text-success'
      : s === 'failed'
        ? 'text-destructive'
        : s === 'detached'
          ? 'text-warning'
          : 'text-primary'
</script>

<div class="flex h-8 shrink-0 items-center gap-2 border-b border-border px-3 text-meta">
  <AppIcons.jobs class="size-4" /><span class="font-semibold">Jobs</span>
  <span class="text-micro text-muted-foreground">{jobs.length}</span>
  {#if toast}<span class="text-micro text-warning">{toast}</span>{/if}
  <span class="ml-auto"></span>
  <Button variant="ghost" size="sm" onclick={load}>{loading ? '…' : 'refresh'}</Button>
  <Button variant="ghost" size="icon" onclick={onClose} title="close">
    <AppIcons.close class="size-4" />
  </Button>
</div>

<div class="flex min-h-0 flex-1">
  <!-- Job list -->
  <div class="w-2/5 min-w-52 shrink-0 overflow-y-auto border-r border-border">
    {#each jobs as j (j.id)}
      <button
        class="flex w-full flex-col gap-0.5 border-b border-border px-3 py-2 text-left hover:bg-muted {selected ===
        j.id
          ? 'bg-muted'
          : ''}"
        onclick={() => select(j.id)}
      >
        <span class="flex items-center gap-2">
          <span class="font-mono text-micro">{j.id}</span>
          <span class="text-micro {stateClass(j.state)}">{j.state}</span>
          {#if j.detached}<span class="text-micro text-warning">detached</span>{/if}
          <span class="ml-auto text-micro text-muted-foreground">#{j.exitCode}</span>
        </span>
        <span class="truncate font-mono text-meta">{j.command}</span>
      </button>
    {/each}
    {#if jobs.length === 0}
      <p class="p-3 text-meta text-muted-foreground">No jobs yet.</p>
    {/if}
  </div>

  <!-- Detail -->
  <div class="flex min-h-0 flex-1 flex-col">
    {#if selected}
      <div class="flex h-8 shrink-0 items-center gap-1 border-b border-border px-2 text-meta">
        <Button
          variant="ghost"
          size="sm"
          class={view === 'output' ? 'bg-muted text-foreground' : ''}
          onclick={() => (view = 'output')}
        >
          <AppIcons.view class="size-3.5" />Output
        </Button>
        <Button
          variant="ghost"
          size="sm"
          class={view === 'graph' ? 'bg-muted text-foreground' : ''}
          onclick={() => (view = 'graph')}
        >
          <AppIcons.graph class="size-3.5" />Flow graph
        </Button>
        <span class="ml-auto"></span>
        {#if watching}<span class="text-micro text-primary">live</span>{/if}
        <Button variant="ghost" size="sm" onclick={() => watch(selected!)} title="watch live">
          <AppIcons.watch class="size-3.5" />
        </Button>
        <Button variant="ghost" size="sm" onclick={() => kill(selected!)} title="kill">
          <AppIcons.kill class="size-3.5" />
        </Button>
      </div>
      {#if view === 'output'}
        <pre class="term-line min-h-0 flex-1 overflow-auto p-3">{output}</pre>
      {:else}
        <FlowGraph jobId={selected} command={jobs.find((x) => x.id === selected)?.command ?? ''} />
      {/if}
    {:else}
      <p class="p-3 text-meta text-muted-foreground">Select a job to view its output or flow graph.</p>
    {/if}
  </div>
</div>
