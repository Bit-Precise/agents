import type { Agent, Grant, Rule, State, User } from "@/api/generated";

export type Tone = "good" | "warn" | "bad" | "muted" | "info";
export type Health = { label: string; tone: Tone };
export const roleNames: Record<string, string> = {
  owner: "所有者",
  maintainer: "维护者",
  developer: "开发者",
  guest: "访客",
  ingress: "访问端",
  access: "访问端",
  provider: "提供方",
  serve: "服务发布",
  egress: "域名出口",
};
export function rows(state: State) {
  return {
    users: state.users ?? [],
    workspaces: state.workspaces ?? [],
    memberships: state.memberships ?? [],
    agents: state.agents ?? [],
    services: state.services ?? [],
    egressRoutes: state.egressRoutes ?? [],
    grants: state.grants ?? [],
    enrollmentKeys: state.enrollmentKeys ?? [],
    audit: state.audit ?? [],
    connections: state.connections ?? [],
    flows: state.flows ?? [],
  };
}
export function time(value?: string, full = false) {
  const d = value ? new Date(value) : null;
  if (!d || !Number.isFinite(d.getTime()) || d.getFullYear() < 1971) return "—";
  return full
    ? d.toLocaleString("zh-CN", { hour12: false })
    : d.toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      });
}
export function short(value?: string) {
  return value?.slice(0, 12) || "—";
}
export function name(
  state: State,
  kind: "users" | "agents" | "workspaces",
  id?: string,
) {
  return rows(state)[kind].find((item) => item.id === id)?.name || id || "—";
}
export function resourceName(state: State, id: string) {
  const r = rows(state);
  return (
    [
      ...r.services,
      ...r.egressRoutes,
      ...r.agents,
      ...r.workspaces,
      ...r.users,
    ].find((item) => item.id === id)?.name || short(id)
  );
}
export function pending(agent: Agent) {
  return agent.status === "pending" || agent.pendingApproval;
}
export function health(state: State, agent: Agent): Health {
  if (agent.status === "revoked") return { label: "已撤销", tone: "bad" };
  if (agent.status === "pending") return { label: "待批准", tone: "warn" };
  if (new Date(agent.approval.expiresAt) <= new Date())
    return { label: "批准已过期", tone: "warn" };
  if (!state.edgeAvailable) return { label: "状态未知", tone: "muted" };
  return rows(state).connections.some((c) => c.agentId === agent.id)
    ? { label: "在线", tone: "good" }
    : { label: "离线", tone: "muted" };
}
export function grantHealth(grant: Grant): Health {
  if (grant.revoked) return { label: "已撤销", tone: "muted" };
  if (new Date(grant.expiresAt) <= new Date())
    return { label: "已到期", tone: "muted" };
  if (new Date(grant.startsAt) > new Date())
    return { label: "未开始", tone: "info" };
  return { label: "生效中", tone: "good" };
}
export function activeGrants(state: State, id: string) {
  return rows(state).grants.filter(
    (g) => g.resourceId === id && grantHealth(g).tone === "good",
  );
}
export function subjectName(state: State, g: Grant) {
  return g.subjectType === "anonymous"
    ? "匿名访问者"
    : g.subjectType === "workspace"
      ? `${name(state, "workspaces", g.subjectId)} 的成员`
      : name(state, "users", g.subjectId);
}
export function canManage(state: State, user: User, workspace = "") {
  return (
    user.admin ||
    rows(state).memberships.some(
      (m) =>
        m.userId === user.id &&
        (!workspace || m.workspaceId === workspace) &&
        ["owner", "maintainer"].includes(m.role),
    )
  );
}
export function canEnroll(state: State, user: User, workspace = "") {
  return (
    user.admin ||
    rows(state).memberships.some(
      (m) =>
        m.userId === user.id && (!workspace || m.workspaceId === workspace),
    )
  );
}
export function canManageResource(
  state: State,
  user: User,
  type: "service" | "egress",
  id: string,
) {
  const r = rows(state);
  const target = (type === "service" ? r.services : r.egressRoutes).find(
    (i) => i.id === id,
  );
  if (!target) return false;
  return (
    canManage(state, user, target.workspaceId) ||
    (type === "service" &&
      r.memberships.some(
        (m) =>
          m.workspaceId === target.workspaceId &&
          m.userId === user.id &&
          m.role === "developer",
      ) &&
      r.agents.some((a) => a.id === target.agentId && a.userId === user.id))
  );
}
export function canGrant(state: State, user: User, workspace = "") {
  return (
    canManage(state, user, workspace) ||
    rows(state).services.some(
      (s) =>
        (!workspace || s.workspaceId === workspace) &&
        canManageResource(state, user, "service", s.id),
    )
  );
}
export function agentRoles(agent: Agent) {
  return ["ingress", "serve", "egress"]
    .filter(
      (role) => agent.desired[role as "ingress" | "serve" | "egress"].enabled,
    )
    .map((role) => roleNames[role]);
}
export function rule(rule: Rule) {
  return `${rule.domain.exact || (rule.domain.suffix ? `${rule.domain.suffix} 及其子域` : "未指定")} · ${(rule.ports ?? []).join(", ") || "所有端口"} · TLS`;
}
export function serviceURL(state: State, hostname: string): string | null {
  try {
    const host = new URL("https://" + hostname);
    if (
      host.host !== hostname.toLowerCase() ||
      host.username ||
      host.password ||
      host.pathname !== "/" ||
      host.search ||
      host.hash
    )
      return null;
    const target = new URL(state.serverURL);
    if (!["http:", "https:"].includes(target.protocol)) return null;
    target.hostname = host.hostname;
    target.pathname = "/";
    target.search = "";
    target.hash = "";
    target.username = "";
    target.password = "";
    return target.href;
  } catch {
    return null;
  }
}
export function localTime(hours = 0) {
  const date = new Date(Date.now() + hours * 3600000);
  date.setSeconds(0, 0);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}
