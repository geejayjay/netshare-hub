import { flexRender, getCoreRowModel, useReactTable, type ColumnDef } from '@tanstack/react-table'
import { ArrowDown, ArrowUp, ArrowUpDown, ChevronLeft, ChevronRight } from 'lucide-react'
import type { ReactNode } from 'react'
import type { ListResponse, PageMeta } from '@/shared/api'
import { Button, Spinner, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from '@/shared/ui'
import { EmptyState, ErrorState } from './states'

export type { ColumnDef }

interface DataTableProps<T> {
  /** Column definitions. Set `enableSorting: true` on a column to make its header sortable by the column id. */
  columns: ColumnDef<T>[]
  /** The list response from the API, paginated or not. */
  data: ListResponse<T> | undefined
  isLoading: boolean
  error: unknown
  onRetry?: () => void
  /** Current sort, e.g. "username" or "-createdAt". */
  sort?: string
  onSortChange?: (sort: string | undefined) => void
  onPageChange?: (page: number) => void
  empty?: ReactNode
}

/**
 * The one table used for every list in the app. Sorting and paging happen on
 * the server; this component only renders and reports what the user asked for.
 * The pager appears on its own when the response carries page metadata.
 */
export function DataTable<T>({
  columns,
  data,
  isLoading,
  error,
  onRetry,
  sort,
  onSortChange,
  onPageChange,
  empty,
}: DataTableProps<T>) {
  const table = useReactTable({
    data: data?.items ?? [],
    columns,
    getCoreRowModel: getCoreRowModel(),
    manualSorting: true,
    manualPagination: true,
    enableSorting: false, // opt in per column
  })

  if (error) return <ErrorState error={error} onRetry={onRetry} />
  if (isLoading && !data) {
    return (
      <div className="flex justify-center py-12 text-fg-muted">
        <Spinner />
      </div>
    )
  }
  if (data?.items.length === 0) return empty ?? <EmptyState title="Nothing here yet" />

  return (
    <div aria-busy={isLoading}>
      <Table>
        <TableHead>
          {table.getHeaderGroups().map((group) => (
            <tr key={group.id}>
              {group.headers.map((header) => {
                const label = flexRender(header.column.columnDef.header, header.getContext())
                return (
                  <TableHeaderCell key={header.id}>
                    {header.column.columnDef.enableSorting && onSortChange ? (
                      <SortButton field={header.column.id} sort={sort} onChange={onSortChange}>
                        {label}
                      </SortButton>
                    ) : (
                      label
                    )}
                  </TableHeaderCell>
                )
              })}
            </tr>
          ))}
        </TableHead>
        <TableBody>
          {table.getRowModel().rows.map((row) => (
            <TableRow key={row.id}>
              {row.getVisibleCells().map((cell) => (
                <TableCell key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {data?.page && onPageChange && <Pager meta={data.page} onChange={onPageChange} />}
    </div>
  )
}

/** Cycles a column through ascending -> descending -> unsorted. */
function SortButton({
  field,
  sort,
  onChange,
  children,
}: {
  field: string
  sort?: string
  onChange: (sort: string | undefined) => void
  children: ReactNode
}) {
  const state = sort === field ? 'asc' : sort === `-${field}` ? 'desc' : 'none'
  const next = state === 'none' ? field : state === 'asc' ? `-${field}` : undefined
  const Icon = state === 'asc' ? ArrowUp : state === 'desc' ? ArrowDown : ArrowUpDown

  return (
    <button
      type="button"
      onClick={() => onChange(next)}
      className="-mx-1 inline-flex items-center gap-1 rounded-sm px-1 uppercase hover:text-fg"
      aria-label={`Sort by ${field}`}
    >
      {children}
      <Icon className={state === 'none' ? 'size-3.5 opacity-40' : 'size-3.5 text-fg'} />
    </button>
  )
}

function Pager({ meta, onChange }: { meta: PageMeta; onChange: (page: number) => void }) {
  const first = (meta.page - 1) * meta.pageSize + 1
  const last = Math.min(meta.page * meta.pageSize, meta.total)

  return (
    <div className="flex items-center justify-between gap-4 border-t border-border px-4 py-3 text-sm text-fg-muted">
      <span>
        {first}–{last} of {meta.total}
      </span>
      <div className="flex items-center gap-2">
        <Button
          variant="secondary"
          size="icon"
          disabled={meta.page <= 1}
          onClick={() => onChange(meta.page - 1)}
          aria-label="Previous page"
        >
          <ChevronLeft className="size-4" />
        </Button>
        <span>
          Page {meta.page} of {Math.max(meta.totalPages, 1)}
        </span>
        <Button
          variant="secondary"
          size="icon"
          disabled={!meta.hasNext}
          onClick={() => onChange(meta.page + 1)}
          aria-label="Next page"
        >
          <ChevronRight className="size-4" />
        </Button>
      </div>
    </div>
  )
}
