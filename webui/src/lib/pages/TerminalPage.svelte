<script lang="ts">
  // TerminalPage — an xterm.js terminal wired to the daemon over a WebSocket.
  // The daemon side is the same session API the CLI uses; the browser gets
  // live output, async detach, and stdin just like a native terminal.
  import { onMount, onDestroy } from 'svelte'
  import { Terminal } from '@xterm/xterm'
  import { FitAddon } from '@xterm/addon-fit'
  import { api, stripAnsi } from '$lib/api'
  import { AppIcons } from '$lib/icons'

  let { sessionName }: { sessionName: string } = $props()

  let el: HTMLDivElement
  let term: Terminal
  let fit: FitAddon
  let ws: WebSocket | null = null
  let curJob = ''
  let buf = '' // typed line (the daemon has no pty; we line-buffer)
  let status = $state('connecting')
  let fontSize = $state(13)

  function send(m: Record<string, unknown>) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(m))
  }

  function write(s: string) {
    term.write(s.replace(/\n/g, '\r\n'))
  }

  function onData(d: string) {
    for (const ch of d) {
      const c = ch.charCodeAt(0)
      if (c === 13) {
        // Enter
        write('\r\n')
        const line = buf.trim()
        buf = ''
        if (line === 'exit') {
          write('(use the browser tab to leave)\r\n$ ')
          continue
        }
        if (line) send({ op: 'run', script: line })
        term.write('$ ')
      } else if (c === 127 || c === 8) {
        // Backspace
        if (buf.length) {
          buf = buf.slice(0, -1)
          term.write('\b \b')
        }
      } else if (c === 3) {
        write('^C\r\n$ ')
        buf = ''
      } else if (c === 4) {
        // Ctrl-D
      } else if (c >= 32) {
        buf += ch
        term.write(ch)
      }
    }
  }

  function connect() {
    status = 'connecting'
    ws = api.ws(sessionName)
    ws.onopen = () => {
      status = 'connected'
      term.write('$ ')
    }
    ws.onclose = () => {
      status = 'disconnected'
      write('\r\n[disconnected]\r\n')
    }
    ws.onmessage = (ev) => {
      const m = JSON.parse(ev.data)
      switch (m.event) {
        case 'started':
          curJob = m.id
          break
        case 'output':
          term.write(stripAnsi(m.data))
          break
        case 'detached':
          write(`\r\n[job ${m.id} detached — see the Jobs drawer]\r\n`)
          break
        case 'done':
          if (m.code) write(`\r\n[exit ${m.code}]`)
          break
        case 'error':
          write(`\r\nerror: ${m.err}\r\n`)
          break
      }
    }
  }

  // xterm cannot use CSS variables directly, so we resolve the panel's design
  // tokens and pass them as the terminal theme; a MutationObserver on <html
  // data-theme> re-applies them when the user toggles light/dark.
  function cssVar(name: string, fallback: string): string {
    const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
    return v || fallback
  }

  function termTheme() {
    return {
      background: cssVar('--background', '#0a0a0a'),
      foreground: cssVar('--foreground', '#eeeeee'),
      cursor: cssVar('--primary', '#fab283'),
      selectionBackground: cssVar('--border', '#484848'),
    }
  }

  onMount(() => {
    term = new Terminal({
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize,
      cursorBlink: true,
      theme: termTheme(),
    })
    fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el)
    fit.fit()
    term.onData(onData)
    connect()

    const ro = new ResizeObserver(() => fit.fit())
    ro.observe(el)
    const mo = new MutationObserver(() => {
      term.options.theme = termTheme()
    })
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => {
      ro.disconnect()
      mo.disconnect()
      ws?.close()
      term.dispose()
    }
  })

  function zoom(d: number) {
    fontSize = Math.max(9, Math.min(22, fontSize + d))
    if (term) {
      term.options.fontSize = fontSize
      fit.fit()
    }
  }
</script>

<div class="flex min-h-0 flex-1 flex-col">
  <div class="flex h-8 shrink-0 items-center gap-2 border-b border-border px-3 text-micro text-muted-foreground">
    <AppIcons.terminal class="size-3.5" />
    <span class="font-mono">{sessionName}</span>
    <span class="rounded bg-muted px-1.5 py-0.5">{status}</span>
    <span class="ml-auto"></span>
    <button class="hover:text-foreground" title="smaller" onclick={() => zoom(-1)}>A−</button>
    <button class="hover:text-foreground" title="larger" onclick={() => zoom(1)}>A+</button>
  </div>
  <div class="min-h-0 flex-1 overflow-hidden bg-background p-1.5" bind:this={el}></div>
</div>
