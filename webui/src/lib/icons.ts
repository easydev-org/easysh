// Semantic icon slots, one name per meaning (mirrors the worker panel's
// convention), so call sites read AppIcons.terminal rather than a lucide id.
import {
  Ban,
  Boxes,
  ChevronDown,
  ChevronRight,
  Eye,
  GitBranch,
  History,
  LogOut,
  Moon,
  Play,
  Radio,
  Sun,
  Terminal,
  Waypoints,
  X,
} from '@lucide/svelte'

export const AppIcons = {
  terminal: Terminal,
  jobs: Boxes,
  graph: Waypoints,
  flow: GitBranch,
  history: History,
  sun: Sun,
  moon: Moon,
  watch: Radio,
  view: Eye,
  kill: Ban,
  play: Play,
  signOut: LogOut,
  close: X,
  chevronRight: ChevronRight,
  chevronDown: ChevronDown,
} as const

export type AppIcon = (typeof AppIcons)[keyof typeof AppIcons]
