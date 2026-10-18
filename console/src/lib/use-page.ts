import { useCallback, useEffect, useRef, useState } from "react";
import type { Pagination } from "@/api/generated";
import { message } from "@/api/session";

export type Query = Record<string, string | number | boolean | undefined>;
export type Page<T> = { items: T[]; pagination: Pagination };
export type Loader<T> = (query: Query, signal: AbortSignal) => Promise<Page<T>>;

// One server-paginated collection. It reloads when the query changes, on
// demand, and every 10 seconds while the tab is visible.
export function usePage<T>(load: Loader<T>, query: Query, scope: string) {
  const [page, setPage] = useState<Page<T> | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);
  const loader = useRef(load);
  loader.current = load;
  const key = scope + JSON.stringify(query);
  const shown = useRef("");
  useEffect(() => {
    const abort = new AbortController();
    setLoading(true);
    if (shown.current !== key) setPage(null);
    const clean = Object.fromEntries(
      Object.entries(query).filter(([, v]) => v !== undefined && v !== ""),
    );
    loader
      .current(
        clean,
        AbortSignal.any([abort.signal, AbortSignal.timeout(15000)]),
      )
      .then((next) => {
        if (abort.signal.aborted) return;
        shown.current = key;
        setPage(next);
        setError("");
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(message(e));
      })
      .finally(() => {
        if (!abort.signal.aborted) setLoading(false);
      });
    return () => abort.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, tick]);
  useEffect(() => {
    const timer = setInterval(() => {
      if (!document.hidden) setTick((t) => t + 1);
    }, 10000);
    return () => clearInterval(timer);
  }, []);
  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { page, items: page?.items ?? [], error, loading, reload };
}

// Table state shared by every list: page, size, search and one filter.
export function useTableState(initial: Partial<{ pageSize: number }> = {}) {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(initial.pageSize ?? 20);
  const [search, setSearchValue] = useState("");
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(search.trim()), 300);
    return () => clearTimeout(timer);
  }, [search]);
  return {
    page,
    setPage,
    pageSize,
    setPageSize: (size: number) => {
      setPageSize(size);
      setPage(1);
    },
    search,
    query: debounced,
    setSearch: (value: string) => {
      setSearchValue(value);
      setPage(1);
    },
  };
}
