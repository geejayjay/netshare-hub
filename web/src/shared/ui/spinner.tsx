import { LoaderCircle } from 'lucide-react'
import { cn } from '@/shared/lib/cn'

export function Spinner({ className }: { className?: string }) {
  return <LoaderCircle className={cn('size-5 animate-spin', className)} aria-hidden="true" />
}
