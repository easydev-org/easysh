// Client for the easysh HTTP API. The bearer token is read from ?token=... once
// and kept in sessionStorage, matching the worker panel.

const params = new URLSearchParams(location.search)
if (params.get('token')) sessionStorage.setItem('easysh_token', params.get('token')!)
const token = () => sessionStorage.getItem('easysh_token') || ''

function headers(): Record<string, string> {
  const h: Record<string, string> = { 'Content-Type': 'application/json' }
  if (token()) h.Authorization = 'Bearer ' + token()
  return h
}

async function jget<T>(path: string): Promise<T> {
  const r = await fetch(path, { headers: headers() })
  if (!r.ok) throw new Error(`${r.status} ${r.statusText}`)
  return r.json() as Promise<T>
}

async function jpost<T = unknown>(path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, { method: 'POST', headers: headers(), body: JSON.stringify(body ?? {}) })
  if (!r.ok) throw new Error(await r.text())
  return (await r.json().catch(() => ({}))) as T
}

export interface SessionInfo {
  name: string
  dir: string
  jobs: number
}

export interface JobInfo {
  id: string
  command: string
  state: string
  exitCode: number
  startedAt: number
  endedAt: number
  detached: boolean
  lines: number
  bytes: number
}

export interface FlowInfo {
  fid: number
  kind: string
  fd?: string
  owner?: string
  producer?: string
  consumer?: string
  path?: string
  bytes: number
  lines: number
  // source span of the owning stage (1-based; end exclusive)
  line?: number
  col?: number
  endLine?: number
  endCol?: number
  // loop-iteration label ("#2", "#2.1") when the stage ran inside a loop
  iter?: string
}

export interface Graph {
  flows: FlowInfo[]
  edges: [number, number][]
}

export const api = {
  token,
  sessions: () => jget<SessionInfo[]>('/api/sessions'),
  newSession: (name: string) => jpost<SessionInfo>('/api/sessions', { name }),
  jobs: (session: string) => jget<JobInfo[]>('/api/jobs?session=' + encodeURIComponent(session)),
  job: (id: string) => jget<JobInfo>('/api/jobs/' + encodeURIComponent(id)),
  graph: (id: string) => jget<Graph>('/api/jobs/' + encodeURIComponent(id) + '/graph'),
  run: (session: string, script: string, detachMs = 0) =>
    jpost<{ jobId: string }>('/api/sessions/' + encodeURIComponent(session) + '/run', { script, detachMs }),
  history: (session: string) =>
    jget<{ history: string[] }>('/api/sessions/' + encodeURIComponent(session) + '/history'),
  addHistory: (session: string, command: string) =>
    jpost('/api/sessions/' + encodeURIComponent(session) + '/history', { command }),
  kill: (id: string) => jpost('/api/jobs/' + encodeURIComponent(id) + '/kill'),
  stdin: (id: string, data: string, close = false) =>
    jpost('/api/jobs/' + encodeURIComponent(id) + '/stdin', { data, close }),

  async output(id: string): Promise<string> {
    const r = await fetch('/api/jobs/' + encodeURIComponent(id) + '/output', { headers: headers() })
    return r.ok ? r.text() : ''
  },
  async flowContent(id: string, fid: number): Promise<string> {
    const r = await fetch(`/api/jobs/${encodeURIComponent(id)}/flows/${fid}`, { headers: headers() })
    return r.ok ? r.text() : ''
  },

  // watchFlow streams one flow's bytes live (SSE): the intermediate result of a
  // pipe/redirect as it is produced. Non-blocking on the server side.
  watchFlow(id: string, fid: number, onOutput: (d: string) => void, onDone?: () => void) {
    const url =
      `/api/jobs/${encodeURIComponent(id)}/flows/${fid}/watch` + (token() ? '?token=' + token() : '')
    const es = new EventSource(url)
    es.addEventListener('output', (e) => onOutput((e as MessageEvent).data))
    es.addEventListener('done', () => {
      onDone?.()
      es.close()
    })
    es.onerror = () => es.close()
    return es
  },

  // watch streams a job's output via SSE (all flows, tagged by kind).
  watch(id: string, onOutput: (data: string) => void, onDone?: (data: string) => void) {
    const url = '/api/jobs/' + encodeURIComponent(id) + '/watch' + (token() ? '?token=' + token() : '')
    const es = new EventSource(url)
    es.addEventListener('output', (e) => onOutput((e as MessageEvent).data))
    es.addEventListener('done', (e) => {
      onDone?.((e as MessageEvent).data)
      es.close()
    })
    es.onerror = () => es.close()
    return es
  },

  ws(session: string): WebSocket {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const q = token() ? '?token=' + token() : ''
    return new WebSocket(`${proto}://${location.host}/api/sessions/${encodeURIComponent(session)}/ws${q}`)
  },
}

// stripAnsi removes terminal escape sequences from recorded output.
export function stripAnsi(s: string): string {
  // biome-ignore lint/suspicious/noControlCharactersInRegex: intentional ANSI strip
  return s.replace(/\x1b\[[0-9;?]*[ -/]*[@-~]/g, '')
}
