import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ChoiceSelect } from "./controls";
import type { Pagination } from "@/api/generated";
export function PaginationBar({
  value,
  onPage,
  onPageSize,
  busy = false,
}: {
  value: Pagination;
  onPage: (page: number) => void;
  onPageSize?: (size: number) => void;
  busy?: boolean;
}) {
  const [jump, setJump] = useState(String(value.page));
  useEffect(() => setJump(String(value.page)), [value.page]);
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-t px-5 py-3 text-sm">
      <span className="text-muted-foreground">
        共 {value.total} 条 · 第 {value.page} / {Math.max(1, value.totalPages)}{" "}
        页
      </span>
      <div className="flex items-center gap-2">
        {onPageSize && (
          <div className="w-28">
            <ChoiceSelect
              label="每页条数"
              value={String(value.pageSize)}
              onChange={(v) => onPageSize(Number(v))}
              options={[20, 50, 100].map((n) => ({
                value: String(n),
                label: `${n} 条 / 页`,
              }))}
            />
          </div>
        )}
        <Button
          variant="outline"
          size="sm"
          disabled={busy || value.page <= 1}
          onClick={() => onPage(value.page - 1)}
        >
          上一页
        </Button>
        <form
          className="flex gap-1"
          onSubmit={(e) => {
            e.preventDefault();
            const n = Number(jump);
            if (
              Number.isInteger(n) &&
              n >= 1 &&
              n <= Math.max(1, value.totalPages)
            )
              onPage(n);
          }}
        >
          <Input
            aria-label="跳转页码"
            className="w-16"
            type="number"
            min={1}
            max={Math.max(1, value.totalPages)}
            value={jump}
            onChange={(e) => setJump(e.target.value)}
          />
          <Button type="submit" variant="ghost" size="sm" disabled={busy}>
            跳转
          </Button>
        </form>
        <Button
          variant="outline"
          size="sm"
          disabled={busy || value.page >= value.totalPages}
          onClick={() => onPage(value.page + 1)}
        >
          下一页
        </Button>
      </div>
    </div>
  );
}
