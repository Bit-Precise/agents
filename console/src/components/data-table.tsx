import type { ReactNode } from "react";
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  InboxIcon,
  SearchIcon,
} from "lucide-react";
import type { Pagination } from "@/api/generated";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Alert, AlertDescription } from "@/components/ui/alert";

export type Column<T> = {
  header: string;
  cell: (row: T) => ReactNode;
  className?: string;
};

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  loading,
  error,
  emptyTitle = "暂无记录",
  emptyDescription = "符合条件的记录会显示在这里。",
}: {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  loading?: boolean;
  error?: string;
  emptyTitle?: string;
  emptyDescription?: string;
}) {
  return (
    <div className="grid gap-3">
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <div className="overflow-hidden rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              {columns.map((column) => (
                <TableHead key={column.header} className={column.className}>
                  {column.header}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading && !rows.length
              ? Array.from({ length: 3 }, (_, i) => (
                  <TableRow key={i}>
                    {columns.map((column) => (
                      <TableCell key={column.header}>
                        <Skeleton className="h-4 w-full max-w-32" />
                      </TableCell>
                    ))}
                  </TableRow>
                ))
              : rows.map((row) => (
                  <TableRow key={rowKey(row)}>
                    {columns.map((column) => (
                      <TableCell
                        key={column.header}
                        className={column.className}
                      >
                        {column.cell(row)}
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
            {!loading && !rows.length && (
              <TableRow className="hover:bg-transparent">
                <TableCell colSpan={columns.length}>
                  <Empty className="py-8">
                    <EmptyHeader>
                      <EmptyMedia variant="icon">
                        <InboxIcon />
                      </EmptyMedia>
                      <EmptyTitle>{emptyTitle}</EmptyTitle>
                      <EmptyDescription>{emptyDescription}</EmptyDescription>
                    </EmptyHeader>
                  </Empty>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

export function TableToolbar({
  search,
  onSearch,
  placeholder = "搜索名称或 ID…",
  children,
}: {
  search?: string;
  onSearch?: (value: string) => void;
  placeholder?: string;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      {onSearch && (
        <InputGroup className="w-full sm:w-64">
          <InputGroupInput
            aria-label="筛选当前列表"
            value={search}
            onChange={(e) => onSearch(e.target.value)}
            placeholder={placeholder}
          />
          <InputGroupAddon>
            <SearchIcon />
          </InputGroupAddon>
        </InputGroup>
      )}
      {children}
    </div>
  );
}

export type Option = { value: string; label: string; disabled?: boolean };

export function OptionSelect({
  label,
  value,
  onChange,
  options,
  className = "w-36",
  disabled,
  id,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: Option[];
  className?: string;
  disabled?: boolean;
  id?: string;
}) {
  return (
    <Select
      value={value}
      onValueChange={(v) => onChange((v as string | null) ?? "")}
      items={options}
      disabled={disabled}
    >
      <SelectTrigger id={id} aria-label={label} className={className}>
        <SelectValue placeholder={label} />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem
            key={option.value}
            value={option.value}
            disabled={option.disabled}
          >
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export function TablePagination({
  pagination,
  onPage,
  onPageSize,
}: {
  pagination?: Pagination;
  onPage: (page: number) => void;
  onPageSize: (size: number) => void;
}) {
  if (!pagination) return null;
  const pages = Math.max(pagination.totalPages, 1);
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 text-sm text-muted-foreground">
      <span>共 {pagination.total} 条</span>
      <div className="flex items-center gap-4">
        <div className="flex items-center gap-2">
          <span className="hidden sm:inline">每页</span>
          <OptionSelect
            label="每页条数"
            value={String(pagination.pageSize)}
            onChange={(v) => onPageSize(Number(v))}
            options={["10", "20", "50", "100"].map((v) => ({
              value: v,
              label: v,
            }))}
            className="w-20"
          />
        </div>
        <span>
          第 {pagination.page} / {pages} 页
        </span>
        <div className="flex items-center gap-1">
          <Button
            variant="outline"
            size="icon-sm"
            aria-label="上一页"
            disabled={pagination.page <= 1}
            onClick={() => onPage(pagination.page - 1)}
          >
            <ChevronLeftIcon />
          </Button>
          <Button
            variant="outline"
            size="icon-sm"
            aria-label="下一页"
            disabled={pagination.page >= pages}
            onClick={() => onPage(pagination.page + 1)}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      </div>
    </div>
  );
}
