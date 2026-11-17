import { Monitor, Moon, Sun, type LucideIcon } from 'lucide-react'
import { cn } from '@/shared/lib/cn'
import { themes, useThemeStore, type Theme } from '@/shared/theme/theme'

const options: Record<Theme, { icon: LucideIcon; label: string }> = {
  light: { icon: Sun, label: 'Light' },
  dark: { icon: Moon, label: 'Dark' },
  system: { icon: Monitor, label: 'Match system' },
}

/** Light / Dark / System switch. The choice is remembered in this browser. */
export function ThemeSwitcher({ className }: { className?: string }) {
  const { theme, setTheme } = useThemeStore()

  return (
    <div
      role="radiogroup"
      aria-label="Color theme"
      className={cn('inline-flex items-center gap-0.5 rounded-full border border-border bg-surface-2 p-0.5', className)}
    >
      {themes.map((value) => {
        const { icon: Icon, label } = options[value]
        const active = theme === value
        return (
          <button
            key={value}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={label}
            title={label}
            onClick={() => setTheme(value)}
            className={cn(
              'grid size-7 place-items-center rounded-full text-fg-subtle transition-colors hover:text-fg',
              active && 'bg-surface text-fg shadow-xs',
            )}
          >
            <Icon className="size-3.5" />
          </button>
        )
      })}
    </div>
  )
}
