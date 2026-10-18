import { useEffect, useSyncExternalStore } from "react";
import * as sdk from "@/api/generated/sdk.gen";
import type {
  Agent,
  RouteView,
  ServerView,
  ServiceAccount,
  User,
  Workspace,
} from "@/api/generated";
import { result } from "@/api/session";

// Rows reference other resources by ID. This cache resolves each reference
// once through the caller's own authorized detail endpoint.
type Entities = {
  users: User;
  agents: Agent;
  serviceAccounts: ServiceAccount;
  workspaces: Workspace;
  servers: ServerView;
  routes: RouteView;
};
export type EntityKind = keyof Entities;

const cache = new Map<string, unknown | null>();
const pending = new Set<string>();
const listeners = new Set<() => void>();
let version = 0;
let admin = false;

function changed() {
  version++;
  for (const listener of listeners) listener();
}
export function resetNames(isAdmin: boolean) {
  admin = isAdmin;
  cache.clear();
  pending.clear();
  changed();
}
export function rememberNames<K extends EntityKind>(
  kind: K,
  items: Entities[K][],
  id: (item: Entities[K]) => string = (item) => (item as { id: string }).id,
) {
  for (const item of items) cache.set(`${kind}:${id(item)}`, item);
  if (items.length) changed();
}

async function fetchEntity(kind: EntityKind, id: string, workspaceId: string) {
  const signal = AbortSignal.timeout(15000);
  const w = workspaceId;
  switch (kind) {
    case "users":
      return result(
        admin
          ? await sdk.getAdminUser({ path: { id }, signal })
          : await sdk.getWorkspaceUser({
              path: { workspaceId: w, userId: id },
              signal,
            }),
      );
    case "agents":
      return result(
        admin
          ? await sdk.getAdminAgent({ path: { id }, signal })
          : await sdk.getWorkspaceAgent({
              path: { workspaceId: w, agentId: id },
              signal,
            }),
      );
    case "serviceAccounts":
      return result(
        admin
          ? await sdk.getAdminServiceAccount({ path: { id }, signal })
          : await sdk.getWorkspaceServiceAccount({
              path: { workspaceId: w, serviceAccountId: id },
              signal,
            }),
      );
    case "workspaces":
      return result(
        admin
          ? await sdk.getAdminWorkspace({ path: { id }, signal })
          : await sdk.getWorkspace({ path: { workspaceId: id }, signal }),
      );
    case "servers":
      return result(
        admin
          ? await sdk.getAdminServer({ path: { id }, signal })
          : await sdk.getWorkspaceServer({
              path: { workspaceId: w, serviceId: id },
              signal,
            }),
      );
    case "routes":
      return result(
        admin
          ? await sdk.getAdminRoute({ path: { id }, signal })
          : await sdk.getWorkspaceRoute({
              path: { workspaceId: w, routeId: id },
              signal,
            }),
      );
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useEntity<K extends EntityKind>(
  kind: K,
  id: string | undefined,
  workspaceId = "",
): Entities[K] | undefined {
  useSyncExternalStore(subscribe, () => version);
  const key = `${kind}:${id}`;
  useEffect(() => {
    if (!id || cache.has(key) || pending.has(key)) return;
    pending.add(key);
    fetchEntity(kind, id, workspaceId)
      .then((value) => cache.set(key, value))
      .catch(() => cache.set(key, null))
      .finally(() => {
        pending.delete(key);
        changed();
      });
  }, [key, kind, id, workspaceId]);
  return id ? ((cache.get(key) as Entities[K] | null) ?? undefined) : undefined;
}

export function useName(
  kind: EntityKind,
  id: string | undefined,
  workspaceId = "",
) {
  const entity = useEntity(kind, id, workspaceId) as
    { name?: string } | undefined;
  return entity?.name || (id ? id.slice(0, 8) : "—");
}
