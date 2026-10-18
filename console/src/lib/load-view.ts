import * as sdk from "@/api/generated/sdk.gen";
import type { User, Pagination, ServerView, RouteView } from "@/api/generated";
import { result, APIFailure } from "@/api/session";
import { emptyView, type ConsoleView } from "./view";

export type Resource =
  | "agents"
  | "ingresses"
  | "servers"
  | "egresses"
  | "routes"
  | "grants"
  | "enrollmentKeys"
  | "memberships"
  | "workspaces"
  | "users"
  | "audit"
  | "flows";
export type ResourceQuery = {
  page?: number;
  pageSize?: number;
  sortBy?: string;
  sortOrder?: "asc" | "desc";
  q?: string;
  [key: string]: string | number | boolean | undefined;
};
export type PageResult = { items: unknown[]; pagination: Pagination };

// Select generated operations for a view; transport, URLs and serialization stay in the SDK.
export async function queryResource(
  resource: Resource,
  user: User,
  workspaceId: string,
  query: ResourceQuery,
  signal: AbortSignal,
): Promise<PageResult> {
  const o = { query, signal },
    w = { ...o, path: { workspaceId } };
  if (resource === "workspaces")
    return result(
      user.admin
        ? await sdk.listAdminWorkspaces(o)
        : await sdk.listWorkspaces(o),
    );
  if (resource === "audit") return result(await sdk.listAdminAuditEvents(o));
  if (!user.admin && !workspaceId) throw new Error("请先选择工作空间。");
  const adminQuery = { ...query, ...(workspaceId ? { workspaceId } : {}) };
  const a = { query: adminQuery, signal };
  switch (resource) {
    case "users":
      return result(
        user.admin
          ? await sdk.listAdminUsers(a)
          : await sdk.listWorkspaceUsers(w),
      );
    case "agents":
      return result(
        user.admin
          ? await sdk.listAdminAgents(a)
          : await sdk.listWorkspaceAgents(w),
      );
    case "servers":
      return result(
        user.admin
          ? await sdk.listAdminServers(a)
          : await sdk.listWorkspaceServers(w),
      );
    case "routes":
      return result(
        user.admin
          ? await sdk.listAdminRoutes(a)
          : await sdk.listWorkspaceRoutes(w),
      );
    case "grants":
      return result(
        user.admin
          ? await sdk.listAdminGrants(a)
          : await sdk.listWorkspaceGrants(w),
      );
    case "enrollmentKeys":
      return result(
        user.admin
          ? await sdk.listAdminEnrollmentKeys(a)
          : await sdk.listWorkspaceEnrollmentKeys(w),
      );
    case "memberships":
      return result(
        user.admin
          ? await sdk.listAdminMemberships(a)
          : await sdk.listWorkspaceMemberships(w),
      );
    case "flows":
      return result(
        user.admin
          ? await sdk.listAdminFlows(a)
          : await sdk.listWorkspaceFlows(w),
      );
    case "ingresses":
      return result(
        user.admin
          ? await sdk.listAdminIngresses(a)
          : await sdk.listWorkspaceIngresses(w),
      );
    case "egresses":
      return result(
        user.admin
          ? await sdk.listAdminEgresses(a)
          : await sdk.listWorkspaceEgresses(w),
      );
  }
}
export function mergeBy<T>(
  items: T[],
  next: T[],
  key: (value: T) => string,
): T[] {
  const map = new Map(items.map((x) => [key(x), x]));
  for (const x of next) map.set(key(x), x);
  return [...map.values()];
}
export function addServers(view: ConsoleView, servers: ServerView[]) {
  view.servers = mergeBy(view.servers, servers, (x) => x.id);
  view.services = mergeBy(
    view.services,
    servers.map((s) => ({
      id: s.serviceId,
      workspaceId: s.workspaceId,
      agentId: s.agentId,
      name: s.name,
      hostname: s.hostname,
      enabled: s.enabled,
    })),
    (x) => x.id,
  );
}
export function addRoutes(view: ConsoleView, routes: RouteView[]) {
  view.routes = mergeBy(view.routes, routes, (x) => x.id);
  view.egressRoutes = mergeBy(
    view.egressRoutes,
    routes
      .filter((r) => r.kind === "egress" && r.rule.protocol === "tls")
      .map((r) => ({
        id: r.id,
        workspaceId: r.workspaceId,
        agentId: r.agentId,
        name: r.name,
        enabled: r.enabled,
        priority: r.priority,
        rule: { ...r.rule, protocol: "tls" as const },
      })),
    (x) => x.id,
  );
}
export async function loadView(
  user: User,
  serverURL: string,
  workspaceId: string,
  resource: Resource,
  query: ResourceQuery,
  overview: boolean,
  signal: AbortSignal,
): Promise<ConsoleView> {
  const view = emptyView(user, serverURL);
  const status = result(await sdk.getInstanceStatus({ signal }));
  view.revision = status.revision;
  view.edgeAvailable = status.edgeAvailable;
  const requests: Promise<void>[] = [];
  // The active table is the only pageable dataset loaded in full for this view.
  let page: PageResult;
  try {
    page = await queryResource(resource, user, workspaceId, query, signal);
  } catch (e) {
    if (e instanceof APIFailure && e.status === 503 && resource === "flows") {
      view.edgeAvailable = false;
      page = {
        items: [],
        pagination: {
          page: query.page || 1,
          pageSize: query.pageSize || 20,
          total: 0,
          totalPages: 0,
        },
      };
    } else throw e;
  }
  view.pagination = page.pagination;
  view.primaryItems = page.items.slice();
  if (resource === "servers") addServers(view, page.items as ServerView[]);
  else if (resource === "routes") addRoutes(view, page.items as RouteView[]);
  else (view[resource] as unknown[]) = page.items;
  if (workspaceId) {
    requests.push(
      (async () => {
        const w = result(
          user.admin
            ? await sdk.getAdminWorkspace({ path: { id: workspaceId }, signal })
            : await sdk.getWorkspace({ path: { workspaceId }, signal }),
        );
        view.workspaces = mergeBy(view.workspaces, [w], (x) => x.id);
      })(),
    );
    if (!user.admin)
      requests.push(
        (async () => {
          try {
            const m = result(
              await sdk.getMembership({
                path: { workspaceId, userId: user.id },
                signal,
              }),
            );
            view.memberships = mergeBy(
              view.memberships,
              [m],
              (x) => x.workspaceId + ":" + x.userId,
            );
          } catch (e) {
            if (!(e instanceof APIFailure && e.status === 404)) throw e;
          }
        })(),
      );
  }
  await Promise.all(requests);
  if (overview) {
    const counts = await Promise.allSettled([
      queryResource("agents", user, workspaceId, { pageSize: 1 }, signal),
      queryResource(
        "servers",
        user,
        workspaceId,
        { pageSize: 1, enabled: true },
        signal,
      ),
      queryResource(
        "routes",
        user,
        workspaceId,
        { pageSize: 1, kind: "egress", enabled: true },
        signal,
      ),
      queryResource(
        "agents",
        user,
        workspaceId,
        { pageSize: 1, pendingApproval: true },
        signal,
      ),
    ]);
    counts.forEach((r, i) => {
      if (r.status === "fulfilled") {
        view.totals[["agents", "servers", "routes", "pending"][i]] =
          r.value.pagination.total;
      }
    });
  }
  // Bounded references for the rows on this page, fetched by authorized resource detail.
  const workspaceIDs = new Set<string>(),
    userRefs = new Map<string, string>(),
    agentRefs = new Map<string, string>();
  for (const x of [
    ...view.agents,
    ...view.enrollmentKeys,
    ...view.memberships,
    ...view.grants,
    ...view.servers,
    ...view.routes,
    ...view.flows,
  ]) {
    workspaceIDs.add(x.workspaceId);
    if ("userId" in x && x.userId) userRefs.set(x.userId, x.workspaceId);
    if ("agentId" in x && x.agentId) agentRefs.set(x.agentId, x.workspaceId);
  }
  for (const x of view.agents) agentRefs.set(x.id, x.workspaceId);
  for (const g of view.grants) {
    if (g.subjectType === "user" && g.subjectId)
      userRefs.set(g.subjectId, g.workspaceId);
    if (g.subjectType === "workspace" && g.subjectId)
      workspaceIDs.add(g.subjectId);
  }
  for (const event of view.audit)
    if (user.admin) userRefs.set(event.actorId, "");
  const refs: Promise<unknown>[] = [];
  for (const id of workspaceIDs) {
    if (view.workspaces.some((w) => w.id === id)) continue;
    refs.push(
      (async () => {
        const w = result(
          user.admin
            ? await sdk.getAdminWorkspace({ path: { id }, signal })
            : await sdk.getWorkspace({ path: { workspaceId: id }, signal }),
        );
        view.workspaces.push(w);
      })(),
    );
  }
  for (const [id, wid] of userRefs) {
    if (view.users.some((u) => u.id === id)) continue;
    refs.push(
      (async () => {
        const u = result(
          user.admin
            ? await sdk.getAdminUser({ path: { id }, signal })
            : await sdk.getWorkspaceUser({
                path: { workspaceId: wid, userId: id },
                signal,
              }),
        );
        view.users.push(u);
      })(),
    );
  }
  for (const [id, wid] of agentRefs) {
    if (view.agents.some((a) => a.id === id)) continue;
    refs.push(
      (async () => {
        const a = result(
          user.admin
            ? await sdk.getAdminAgent({ path: { id }, signal })
            : await sdk.getWorkspaceAgent({
                path: { workspaceId: wid, agentId: id },
                signal,
              }),
        );
        view.agents.push(a);
      })(),
    );
  }
  for (const g of view.grants)
    refs.push(
      (async () => {
        if (g.resourceType === "service") {
          const s = result(
            user.admin
              ? await sdk.getAdminServer({ path: { id: g.resourceId }, signal })
              : await sdk.getWorkspaceServer({
                  path: { workspaceId: g.workspaceId, serviceId: g.resourceId },
                  signal,
                }),
          );
          addServers(view, [s]);
          if (!view.agents.some((a) => a.id === s.agentId)) {
            const agent = result(
              user.admin
                ? await sdk.getAdminAgent({ path: { id: s.agentId }, signal })
                : await sdk.getWorkspaceAgent({
                    path: { workspaceId: s.workspaceId, agentId: s.agentId },
                    signal,
                  }),
            );
            view.agents = mergeBy(view.agents, [agent], (a) => a.id);
          }
        } else {
          const r = result(
            user.admin
              ? await sdk.getAdminRoute({ path: { id: g.resourceId }, signal })
              : await sdk.getWorkspaceRoute({
                  path: { workspaceId: g.workspaceId, routeId: g.resourceId },
                  signal,
                }),
          );
          addRoutes(view, [r]);
        }
      })(),
    );
  const outcomes = await Promise.allSettled(refs);
  for (const r of outcomes)
    if (
      r.status === "rejected" &&
      r.reason instanceof APIFailure &&
      r.reason.status === 401
    )
      throw r.reason;
  // Connection absence is meaningful only for complete per-Agent queries.
  if (resource === "agents") {
    const telemetry = await Promise.allSettled(
      view.agents.map(async (a) => {
        const o = { query: { agentId: a.id, pageSize: 100 }, signal };
        const p = result(
          user.admin
            ? await sdk.listAdminConnections(o)
            : await sdk.listWorkspaceConnections({
                ...o,
                path: { workspaceId: a.workspaceId },
              }),
        );
        view.connections.push(...p.items);
      }),
    );
    for (const x of telemetry) {
      if (
        x.status === "rejected" &&
        x.reason instanceof APIFailure &&
        x.reason.status === 401
      )
        throw x.reason;
    }
    if (telemetry.some((x) => x.status === "rejected"))
      view.edgeAvailable = false;
  }
  return view;
}
