import { Search } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Input } from '@/shared/ui'

/** A search box that reports the value after the user stops typing. */
export function SearchInput({
  value,
  onChange,
  placeholder = 'Search…',
  delay = 300,
}: {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  delay?: number
}) {
  const [text, setText] = useState(value)

  // Follow outside changes (e.g. browser back/forward) without an effect.
  const [lastValue, setLastValue] = useState(value)
  if (value !== lastValue) {
    setLastValue(value)
    setText(value)
  }

  useEffect(() => {
    if (text === value) return
    const timer = setTimeout(() => onChange(text), delay)
    return () => clearTimeout(timer)
  }, [text, value, onChange, delay])

  return (
    <div className="relative w-full max-w-xs">
      <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-fg-muted" />
      <Input
        type="search"
        className="pl-9"
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={placeholder}
        aria-label={placeholder}
      />
    </div>
  )
}
