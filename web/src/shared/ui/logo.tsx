import { useId } from 'react'
import { cn } from '@/shared/lib/cn'

/**
 * The NetShare Hub mark: a two-layer folder whose front flap carries a
 * network hub. Same drawing as docs/brand/logo.svg and public/favicon.svg;
 * change all three together.
 *
 * Brand colors are fixed on purpose, so the mark looks the same in light and
 * dark mode and everywhere it appears.
 */
export function LogoMark({ className, title }: { className?: string; title?: string }) {
  // Unique gradient ids, so several logos on one page never clash.
  const id = useId()
  const back = `${id}-back`
  const front = `${id}-front`

  return (
    <svg
      viewBox="0 0 32 32"
      className={cn('size-8 shrink-0', className)}
      role={title ? 'img' : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
    >
      <defs>
        <linearGradient id={back} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#4b37d8" />
          <stop offset="1" stopColor="#3a27b8" />
        </linearGradient>
        <linearGradient id={front} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#7b6cff" />
          <stop offset="1" stopColor="#5543ea" />
        </linearGradient>
      </defs>
      <path
        d="M2 8C2 5.2 3.9 3.5 6.6 3.5h5.9c1 0 1.9.4 2.5 1.1l1.7 2h8.7c2.7 0 4.6 1.7 4.6 4.5V24c0 2.8-1.9 4.5-4.6 4.5H6.6C3.9 28.5 2 26.8 2 24z"
        fill={`url(#${back})`}
      />
      <rect x="2" y="9.6" width="28" height="18.9" rx="4.6" fill={`url(#${front})`} />
      <path d="M6.6 9.6h18.8" stroke="#fff" strokeOpacity=".35" strokeWidth=".8" strokeLinecap="round" />
      <g fill="#fff" stroke="#fff" strokeWidth="1.9" strokeLinecap="round">
        <path d="M16 19.6v-5.1M16 19.6l4.4 2.6M16 19.6l-4.4 2.6" />
        <circle cx="16" cy="19.6" r="2.5" stroke="none" />
        <circle cx="16" cy="13.9" r="1.8" stroke="none" />
        <circle cx="21" cy="22.5" r="1.8" stroke="none" />
        <circle cx="11" cy="22.5" r="1.8" stroke="none" />
      </g>
    </svg>
  )
}
