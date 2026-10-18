import type { Agent, Principal, Rule } from "@/api/generated";

export const roleNames: Record<string, string> = {
  owner: "所有者",
  maintainer: "维护者",
  developer: "开发者",
  guest: "访客",
  ingress: "Ingress",
  serve: "Server",
  egress: "Egress",
};
export const memberRoles = ["owner", "maintainer", "developer", "guest"];

export function time(value?: string, full = false) {
  const d = value ? new Date(value) : null;
  if (!d || !Number.isFinite(d.getTime()) || d.getFullYear() < 1971) return "—";
  return d.toLocaleString(
    "zh-CN",
    full
      ? { hour12: false }
      : {
          month: "2-digit",
          day: "2-digit",
          hour: "2-digit",
          minute: "2-digit",
          hour12: false,
        },
  );
}
export function short(value?: string) {
  return value?.slice(0, 8) || "—";
}
export function initials(name?: string) {
  return (name || "?").trim().slice(0, 1).toUpperCase();
}
export function isPersonal(workspaceName: string, userID: string) {
  return workspaceName === "personal-" + userID;
}
export function workspaceLabel(workspaceName: string, userID: string) {
  return isPersonal(workspaceName, userID) ? "个人空间" : workspaceName;
}
export function isSelf(principal: Principal | undefined, userID: string) {
  return principal?.type === "user" && principal.id === userID;
}
export function enabledRoles(agent: Agent) {
  return (["ingress", "serve", "egress"] as const).filter(
    (role) => agent.desired[role].enabled,
  );
}
export function rule(r: Rule) {
  const domain =
    r.domain.exact || (r.domain.suffix ? `*.${r.domain.suffix}` : "—");
  return `${domain}:${(r.ports ?? []).join(",") || "*"}`;
}
export function localTime(hours = 0) {
  const date = new Date(Date.now() + hours * 3600000);
  date.setSeconds(0, 0);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}
export function serviceURL(serverURL: string, hostname: string): string | null {
  try {
    const target = new URL(serverURL);
    if (!/^[a-z0-9.-]+$/i.test(hostname)) return null;
    target.hostname = hostname;
    target.pathname = "/";
    target.search = target.hash = "";
    return target.href;
  } catch {
    return null;
  }
}
