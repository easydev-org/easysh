import { type ClassValue, clsx } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

// text-micro/meta/body are custom @utility font-size classes (see app.css), so
// tailwind-merge must be told they belong to the font-size group — otherwise
// cn() would drop them as if they conflicted with text-muted-foreground, etc.
// (Same fix as the worker control panel.)
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      'font-size': ['text-micro', 'text-meta', 'text-body'],
    },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export type WithElementRef<T, U extends HTMLElement = HTMLElement> = T & {
  ref?: U | null
}
