import { cva, type VariantProps } from 'class-variance-authority'
import type { ComponentProps } from 'react'
import { cn } from '@/shared/lib/cn'
import { Spinner } from './spinner'

/** Every button look in the app is defined here, once. */
const buttonVariants = cva(
  'inline-flex items-center justify-center gap-2 rounded-md font-medium whitespace-nowrap select-none ' +
    'transition-[background-color,border-color,color,box-shadow,transform] duration-150 ' +
    'active:scale-[0.98] disabled:opacity-50 disabled:pointer-events-none',
  {
    variants: {
      variant: {
        // The inset highlight gives filled buttons a little depth without a gradient.
        primary:
          'bg-primary text-primary-fg shadow-xs inset-shadow-[0_1px_0_rgb(255_255_255/0.18)] hover:bg-primary-hover',
        secondary:
          'border border-border-strong bg-surface text-fg shadow-xs hover:bg-surface-2',
        ghost: 'text-fg-muted hover:bg-surface-2 hover:text-fg',
        danger:
          'bg-danger text-danger-fg shadow-xs inset-shadow-[0_1px_0_rgb(255_255_255/0.18)] hover:bg-danger-hover',
      },
      size: {
        sm: 'h-8 px-3 text-[0.8125rem]',
        md: 'h-9 px-3.5 text-sm',
        icon: 'size-9',
      },
    },
    defaultVariants: { variant: 'primary', size: 'md' },
  },
)

export interface ButtonProps extends ComponentProps<'button'>, VariantProps<typeof buttonVariants> {
  /** Shows a spinner and disables the button while an action is running. */
  loading?: boolean
}

export function Button({ className, variant, size, loading, disabled, children, type = 'button', ...props }: ButtonProps) {
  return (
    <button
      type={type}
      className={cn(buttonVariants({ variant, size }), className)}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...props}
    >
      {loading && <Spinner className="size-4" />}
      {children}
    </button>
  )
}
