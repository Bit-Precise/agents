export const workspaceSections = [
  "overview",
  "members",
  "agents",
  "ingresses",
  "egresses",
  "servers",
  "routes",
  "access",
] as const;
export const adminSections = [
  "workspaces",
  "users",
  "agents",
  "ingresses",
  "egresses",
  "servers",
  "audit",
] as const;
export type WorkspaceSection = (typeof workspaceSections)[number];
export type AdminSection = (typeof adminSections)[number];

export type Route =
  | { kind: "home" }
  | { kind: "login" }
  | { kind: "device"; id: string }
  | { kind: "workspaces" }
  | { kind: "workspace"; workspaceId: string; section: WorkspaceSection }
  | { kind: "admin"; section: AdminSection }
  | { kind: "notFound" };

export const paths = {
  home: "/",
  login: "/login",
  workspaces: "/workspaces",
  workspace: (id: string, section: WorkspaceSection = "overview") =>
    `/workspaces/${encodeURIComponent(id)}/${section}`,
  admin: (section: AdminSection = "workspaces") => `/admin/${section}`,
  device: (id: string) => `/device/${encodeURIComponent(id)}`,
};

export function parseRoute(path: string): Route {
  const parts = path
    .split("/")
    .filter(Boolean)
    .map((part) => decodeURIComponent(part));
  if (!parts.length || (parts.length === 1 && parts[0] === "console"))
    return { kind: "home" };
  if (parts[0] === "login" && parts.length === 1) return { kind: "login" };
  if (parts[0] === "device" && parts.length === 2)
    return { kind: "device", id: parts[1] };
  if (parts[0] === "workspaces") {
    if (parts.length === 1) return { kind: "workspaces" };
    const section = parts[2] ?? "overview";
    if (
      parts.length <= 3 &&
      (workspaceSections as readonly string[]).includes(section)
    )
      return {
        kind: "workspace",
        workspaceId: parts[1],
        section: section as WorkspaceSection,
      };
  }
  if (parts[0] === "admin") {
    const section = parts[1] ?? "workspaces";
    if (
      parts.length <= 2 &&
      (adminSections as readonly string[]).includes(section)
    )
      return { kind: "admin", section: section as AdminSection };
  }
  return { kind: "notFound" };
}
