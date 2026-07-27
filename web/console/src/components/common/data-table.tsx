import type { ReactNode } from "react";
import { flexRender, getCoreRowModel, useReactTable, type ColumnDef } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight } from "lucide-react";

import type { Pagination } from "@/api/types";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";

import { EmptyState, ErrorState, LoadingState } from "./state";

export type DataTableProps<TData> = {
  columns: ColumnDef<TData, unknown>[];
  data: TData[] | undefined;
  isLoading?: boolean;
  isFetching?: boolean;
  error?: unknown;
  onRetry?: () => void;
  emptyTitle?: string;
  emptyDescription?: ReactNode;
  emptyAction?: ReactNode;
  toolbar?: ReactNode;
  pagination?: {
    value: Pagination | undefined;
    onOffsetChange: (offset: number) => void;
  };
  onRowClick?: (row: TData) => void;
  rowKey?: (row: TData) => string;
};

/**
 * Table shell shared by every management view: it renders the standard
 * loading, error, empty and paged states so each application only supplies
 * columns and data.
 */
export function DataTable<TData>({
  columns,
  data,
  isLoading,
  isFetching,
  error,
  onRetry,
  emptyTitle = "暂无数据",
  emptyDescription,
  emptyAction,
  toolbar,
  pagination,
  onRowClick,
  rowKey,
}: DataTableProps<TData>) {
  const table = useReactTable({
    data: data ?? [],
    columns,
    getCoreRowModel: getCoreRowModel(),
    getRowId: rowKey ? (row) => rowKey(row) : undefined,
  });

  const page = pagination?.value;
  const canGoPrevious = Boolean(page && page.offset > 0);
  const canGoNext = Boolean(page?.has_more);

  return (
    <div className="flex min-h-0 flex-col gap-3">
      {toolbar ? <div className="flex flex-wrap items-center gap-2">{toolbar}</div> : null}

      <div className="border-border bg-surface rounded-panel shadow-e1 min-h-0 flex-1 overflow-auto border">
        {error ? (
          <ErrorState error={error} onRetry={onRetry} />
        ) : isLoading ? (
          <LoadingState />
        ) : (data?.length ?? 0) === 0 ? (
          <EmptyState title={emptyTitle} description={emptyDescription} action={emptyAction} />
        ) : (
          <table className="zke-tnum w-full border-collapse text-[13px]">
            {/* The header floats over scrolled rows, so it needs its own opaque
                fill and a blur — a translucent one would let text show through. */}
            <thead className="bg-surface-muted/95 sticky top-0 z-10 backdrop-blur-sm">
              {table.getHeaderGroups().map((headerGroup) => (
                <tr key={headerGroup.id}>
                  {headerGroup.headers.map((header) => (
                    <th
                      key={header.id}
                      scope="col"
                      className="border-border text-subtle-foreground border-b px-3 py-2 text-left text-[11px] font-semibold tracking-[0.06em] whitespace-nowrap uppercase"
                      style={
                        header.column.columnDef.size
                          ? { width: header.column.columnDef.size }
                          : undefined
                      }
                    >
                      {header.isPlaceholder
                        ? null
                        : flexRender(header.column.columnDef.header, header.getContext())}
                    </th>
                  ))}
                </tr>
              ))}
            </thead>
            <tbody>
              {table.getRowModel().rows.map((row) => (
                <tr
                  key={row.id}
                  onClick={onRowClick ? () => onRowClick(row.original) : undefined}
                  className={cn(
                    "border-border/60 border-b transition-colors last:border-b-0",
                    onRowClick
                      ? "hover:bg-primary-surface/40 cursor-pointer"
                      : "hover:bg-surface-muted/60",
                  )}
                >
                  {row.getVisibleCells().map((cell) => (
                    <td key={cell.id} className="px-3 py-2.5 align-middle">
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {page ? (
        <div className="text-muted-foreground flex items-center justify-between gap-3 px-0.5 text-xs">
          <span className="zke-tnum">
            共 {page.total} 条
            {isFetching ? <span className="text-subtle-foreground ml-2">刷新中…</span> : null}
          </span>
          <div className="flex items-center gap-1.5">
            <span className="zke-mono text-subtle-foreground">
              {page.total === 0 ? 0 : page.offset + 1}–
              {Math.min(page.offset + page.limit, page.total)}
            </span>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="上一页"
              disabled={!canGoPrevious}
              onClick={() => pagination?.onOffsetChange(Math.max(0, page.offset - page.limit))}
            >
              <ChevronLeft />
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="下一页"
              disabled={!canGoNext}
              onClick={() => pagination?.onOffsetChange(page.offset + page.limit)}
            >
              <ChevronRight />
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
