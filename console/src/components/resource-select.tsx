import { useEffect, useState } from "react";
import type { User, Pagination } from "@/api/generated";
import {
  queryResource,
  type Resource,
  type ResourceQuery,
} from "@/lib/load-view";
import { message } from "@/api/session";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "./ui/dialog";
import { Notice } from "./controls";
import { PaginationBar } from "./pagination";
export type Selectable = { id: string; name?: string; [key: string]: unknown };
export function ResourceSelect({
  resource,
  user,
  workspace = "",
  filters = {},
  value,
  label,
  valueLabel,
  onSelect,
  disabled = false,
}: {
  resource: Resource;
  user: User;
  workspace?: string;
  filters?: ResourceQuery;
  value: string;
  label: string;
  valueLabel?: string;
  onSelect: (item: Selectable) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false),
    [query, setQuery] = useState(""),
    [page, setPage] = useState(1),
    [items, setItems] = useState<Selectable[]>([]),
    [pagination, setPagination] = useState<Pagination>({
      page: 1,
      pageSize: 20,
      total: 0,
      totalPages: 0,
    }),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [itemsScope, setItemsScope] = useState(""),
    [selectedLabel, setSelectedLabel] = useState("");
  const filterKey = JSON.stringify(filters);
  const choiceScope = JSON.stringify([
    resource,
    user.id,
    user.admin,
    workspace,
    filterKey,
    query,
    page,
  ]);
  const visibleItems = itemsScope === choiceScope ? items : [];
  useEffect(() => {
    setPage(1);
    setQuery("");
    setSelectedLabel("");
  }, [workspace, resource]);
  useEffect(() => {
    if (!open) return;
    const abort = new AbortController();
    setBusy(true);
    setItems([]);
    setError("");
    void queryResource(
      resource,
      user,
      workspace,
      {
        ...JSON.parse(filterKey),
        q: query || undefined,
        page,
        pageSize: 20,
        sortBy: "name",
      },
      AbortSignal.any([abort.signal, AbortSignal.timeout(15000)]),
    )
      .then((r) => {
        if (abort.signal.aborted) return;
        setItems(r.items as Selectable[]);
        setPagination(r.pagination);
        setItemsScope(choiceScope);
      })
      .catch((e) => {
        if (!abort.signal.aborted) {
          setError(message(e));
          setItemsScope(choiceScope);
          setPagination({ page, pageSize: 20, total: 0, totalPages: 0 });
        }
      })
      .finally(() => {
        if (!abort.signal.aborted) setBusy(false);
      });
    return () => abort.abort();
  }, [
    open,
    query,
    page,
    workspace,
    resource,
    filterKey,
    user.id,
    user.admin,
    choiceScope,
  ]);
  return (
    <>
      <Button
        type="button"
        className="w-full justify-between font-normal"
        variant="outline"
        disabled={disabled}
        onClick={() => setOpen(true)}
      >
        {value ? valueLabel || selectedLabel || value : `选择${label}`}
        <span aria-hidden>⌕</span>
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>选择{label}</DialogTitle>
          </DialogHeader>
          <Input
            aria-label={`搜索${label}`}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setPage(1);
            }}
            placeholder={`搜索${label}名称…`}
          />
          {error && <Notice error>{error}</Notice>}
          <div className="max-h-80 overflow-y-auto space-y-1">
            {visibleItems.map((item) => (
              <Button
                key={item.id}
                type="button"
                variant={item.id === value ? "secondary" : "ghost"}
                className="w-full justify-between text-left"
                disabled={busy}
                onClick={() => {
                  setSelectedLabel(item.name || item.id);
                  onSelect(item);
                  setOpen(false);
                }}
              >
                <span className="truncate">{item.name || item.id}</span>
                <span className="ml-3 text-xs text-muted-foreground">
                  {item.id.slice(0, 8)}
                </span>
              </Button>
            ))}
            {!visibleItems.length && !busy && itemsScope === choiceScope && (
              <p className="py-6 text-center text-sm text-muted-foreground">
                没有匹配项
              </p>
            )}
            {(busy || itemsScope !== choiceScope) && (
              <p className="text-sm text-muted-foreground">正在加载…</p>
            )}
          </div>
          {itemsScope === choiceScope && (
            <PaginationBar value={pagination} onPage={setPage} busy={busy} />
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
