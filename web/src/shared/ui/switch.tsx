import { cn } from '@/shared/lib/cn'

interface SwitchProps {
  checked: boolean
  onChange: (checked: boolean) => void
  disabled?: boolean
  id?: string
  'aria-label'?: string
}

/** Accessible pill toggle switch. */
export function Switch({ checked, onChange, disabled, id, 'aria-label': ariaLabel }: SwitchProps) {
  return (
    <button
      id={id}
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative inline-flex h-5.5 w-10 shrink-0 items-center rounded-full border transition-colors duration-150',
        'disabled:pointer-events-none disabled:opacity-50',
        checked ? 'border-primary bg-primary' : 'border-border-strong bg-surface-3',
      )}
    >
      <span
        className={cn(
          'pointer-events-none block size-4 rounded-full bg-white shadow-xs transition-transform duration-150',
          checked ? 'translate-x-[1.1875rem]' : 'translate-x-0.5',
        )}
      />
    </button>
  )
}
