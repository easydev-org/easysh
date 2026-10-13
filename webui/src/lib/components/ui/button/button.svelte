<script lang="ts">
  // A tiny button with the same variant surface as the worker panel's, built
  // with tailwind-variants over the shared design tokens.
  import { tv, type VariantProps } from 'tailwind-variants'
  import { cn } from '$lib/utils'
  import type { Snippet } from 'svelte'

  const button = tv({
    base: 'inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-md text-body font-medium transition-colors disabled:pointer-events-none disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground hover:opacity-90',
        ghost: 'hover:bg-muted text-muted-foreground hover:text-foreground',
        outline: 'border border-border bg-card hover:bg-muted text-foreground',
        destructive: 'bg-destructive text-destructive-foreground hover:opacity-90',
      },
      size: {
        sm: 'h-7 px-2 text-meta',
        md: 'h-8 px-3',
        icon: 'size-7',
      },
    },
    defaultVariants: { variant: 'default', size: 'md' },
  })

  type Base = VariantProps<typeof button>
  interface Props extends Base {
    class?: string
    disabled?: boolean
    title?: string
    onclick?: (e: MouseEvent) => void
    children?: Snippet
  }
  let { class: klass, variant, size, disabled, title, onclick, children }: Props = $props()
</script>

<button
  type="button"
  class={cn(button({ variant, size }), klass)}
  {disabled}
  {title}
  {onclick}
>
  {@render children?.()}
</button>
