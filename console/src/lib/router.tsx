import { useSyncExternalStore, type AnchorHTMLAttributes } from "react";

// A minimal History API router: the address bar is the source of truth.
const listeners = new Set<() => void>();
function emit() {
  for (const listener of listeners) listener();
}
window.addEventListener("popstate", emit);
function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function navigate(to: string, options: { replace?: boolean } = {}) {
  const url = new URL(to, location.origin);
  if (url.origin !== location.origin) {
    location.assign(url.href);
    return;
  }
  const target = url.pathname + url.search + url.hash;
  if (target !== location.pathname + location.search + location.hash)
    history[options.replace ? "replaceState" : "pushState"](null, "", target);
  emit();
}

export function useLocation() {
  const path = useSyncExternalStore(subscribe, () => location.pathname);
  const search = useSyncExternalStore(subscribe, () => location.search);
  return { path, search };
}

// The query string holds a page's view state (tab, filters) so it can be shared.
export function useQueryParam(
  name: string,
  fallback = "",
): [string, (value: string) => void] {
  const { search } = useLocation();
  const value = new URLSearchParams(search).get(name) ?? fallback;
  return [
    value,
    (next) => {
      const params = new URLSearchParams(location.search);
      if (next && next !== fallback) params.set(name, next);
      else params.delete(name);
      const query = params.toString();
      navigate(location.pathname + (query ? "?" + query : ""), {
        replace: true,
      });
    },
  ];
}

export function Link({
  to,
  onClick,
  ...props
}: AnchorHTMLAttributes<HTMLAnchorElement> & { to: string }) {
  return (
    <a
      href={to}
      {...props}
      onClick={(event) => {
        onClick?.(event);
        if (
          event.defaultPrevented ||
          event.button !== 0 ||
          event.metaKey ||
          event.ctrlKey ||
          event.shiftKey ||
          event.altKey ||
          props.target
        )
          return;
        event.preventDefault();
        navigate(to);
      }}
    />
  );
}
