import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import * as sdk from "@/api/generated/sdk.gen";
import type {
  Info,
  Session,
  User,
  Workspace,
  WorkspacePermissions,
} from "@/api/generated";
import {
  requestSignal,
  result,
  serviceReturnTo,
  setCSRF,
} from "@/api/session";
import { resetNames } from "./names";

type SessionState = {
  info: Info | null;
  user: User | null;
  booting: boolean;
  accept: (session: Session) => void;
  signOut: () => Promise<void>;
  expired: () => void;
};
const SessionContext = createContext<SessionState | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [info, setInfo] = useState<Info | null>(null);
  const [user, setUser] = useState<User | null>(null);
  const [booting, setBooting] = useState(true);
  const accept = useCallback((session: Session) => {
    resetNames(session.user.admin);
    setCSRF(session.csrfToken);
    setUser(session.user);
    // A service authorization or `agentd login` continues after sign-in.
    const target = serviceReturnTo();
    if (target) location.replace(target);
  }, []);
  const expired = useCallback(() => {
    setCSRF("");
    setUser(null);
  }, []);
  const signOut = useCallback(async () => {
    result(await sdk.deleteCurrentSession({ signal: requestSignal() }));
    expired();
  }, [expired]);
  useEffect(() => {
    void Promise.allSettled([
      sdk.getInstance({ signal: requestSignal() }),
      sdk.getCurrentSession({ signal: requestSignal() }),
    ]).then(([i, s]) => {
      if (i.status === "fulfilled") {
        try {
          setInfo(result(i.value));
        } catch {}
      }
      if (s.status === "fulfilled") {
        try {
          accept(result(s.value));
        } catch {}
      }
      setBooting(false);
    });
  }, [accept]);
  return (
    <SessionContext.Provider
      value={{ info, user, booting, accept, signOut, expired }}
    >
      {children}
    </SessionContext.Provider>
  );
}

export function useSession() {
  const value = useContext(SessionContext);
  if (!value) throw new Error("session context missing");
  return value;
}
export function useUser(): User {
  const { user } = useSession();
  if (!user) throw new Error("signed-in user required");
  return user;
}

// Permission is a control-plane permission (docs/authorization.md).
export type Permission = WorkspacePermissions["permissions"][number];

// The Workspace a page belongs to and what the signed-in person may manage in
// it. can reflects GET /permissions so pages only offer what Control allows;
// Control still checks every operation.
export type WorkspaceScope = {
  workspace: Workspace;
  role?: Role;
  can: (permission: Permission) => boolean;
};
type Role = NonNullable<WorkspacePermissions["role"]>;
const WorkspaceContext = createContext<WorkspaceScope | null>(null);
export const WorkspaceProvider = WorkspaceContext.Provider;
export function useWorkspace() {
  const value = useContext(WorkspaceContext);
  if (!value) throw new Error("workspace context missing");
  return value;
}

// membershipPermission mirrors authz.MembershipChange: any change that
// involves owner or maintainer needs members.manage_privileged.
export function membershipPermission(...roles: (string | undefined)[]) {
  return roles.some((r) => r === "owner" || r === "maintainer")
    ? "members.manage_privileged"
    : "members.manage";
}

export async function loadWorkspace(
  workspaceId: string,
  signal: AbortSignal,
): Promise<WorkspaceScope> {
  const [workspace, access] = await Promise.all([
    sdk.getWorkspace({ path: { workspaceId }, signal }).then(result),
    sdk.getWorkspacePermissions({ path: { workspaceId }, signal }).then(result),
  ]);
  const held = new Set<string>(access.permissions);
  return { workspace, role: access.role, can: (p) => held.has(p) };
}
