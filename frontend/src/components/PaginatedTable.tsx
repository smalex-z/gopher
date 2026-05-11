import { useState, useMemo, useEffect, type ReactNode } from 'react'
import { ChevronLeft, ChevronRight } from 'lucide-react'

export interface Column<T> {
  key: string
  header: ReactNode
  headerClassName?: string
  cellClassName?: string
  render: (row: T, idx: number) => ReactNode
}

interface PaginatedTableProps<T> {
  rows: T[]
  columns: Column<T>[]
  rowKey: (row: T, idx: number) => string | number
  pageSizeOptions?: number[]
  defaultPageSize?: number
}

export function PaginatedTable<T>({
  rows,
  columns,
  rowKey,
  pageSizeOptions = [10, 25, 100],
  defaultPageSize = 10,
}: PaginatedTableProps<T>) {
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(defaultPageSize)

  const totalPages = Math.max(1, Math.ceil(rows.length / pageSize))

  useEffect(() => {
    if (page > totalPages) setPage(totalPages)
  }, [page, totalPages])

  const safePage = Math.min(page, totalPages)
  const pageRows = useMemo(() => {
    const start = (safePage - 1) * pageSize
    return rows.slice(start, start + pageSize)
  }, [rows, safePage, pageSize])

  const startIdx = rows.length === 0 ? 0 : (safePage - 1) * pageSize + 1
  const endIdx = Math.min(rows.length, safePage * pageSize)

  return (
    <div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-100">
              {columns.map(c => (
                <th
                  key={c.key}
                  className={`text-left py-2 pr-4 font-medium text-gray-500 text-xs ${c.headerClassName ?? ''}`}
                >
                  {c.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {pageRows.map((row, i) => (
              <tr key={rowKey(row, i)} className="hover:bg-gray-50">
                {columns.map(c => (
                  <td key={c.key} className={`py-2 pr-4 ${c.cellClassName ?? ''}`}>
                    {c.render(row, i)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-between mt-3 pt-3 border-t border-gray-100 text-xs text-gray-500">
        <div className="flex items-center gap-2">
          <span>Rows per page:</span>
          <select
            value={pageSize}
            onChange={e => { setPageSize(Number(e.target.value)); setPage(1) }}
            className="border border-gray-300 rounded px-2 py-1 text-xs focus:outline-none focus:ring-2 focus:ring-blue-500"
          >
            {pageSizeOptions.map(s => <option key={s} value={s}>{s}</option>)}
          </select>
        </div>

        <div className="flex items-center gap-3">
          <span className="tabular-nums">{startIdx}–{endIdx} of {rows.length}</span>
          <div className="flex items-center gap-1">
            <button
              type="button"
              onClick={() => setPage(p => Math.max(1, p - 1))}
              disabled={safePage === 1}
              className="p-1 rounded hover:bg-gray-100 disabled:opacity-30 disabled:cursor-not-allowed"
              aria-label="Previous page"
            >
              <ChevronLeft size={14} />
            </button>
            <span className="tabular-nums px-1">{safePage} / {totalPages}</span>
            <button
              type="button"
              onClick={() => setPage(p => Math.min(totalPages, p + 1))}
              disabled={safePage === totalPages}
              className="p-1 rounded hover:bg-gray-100 disabled:opacity-30 disabled:cursor-not-allowed"
              aria-label="Next page"
            >
              <ChevronRight size={14} />
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
