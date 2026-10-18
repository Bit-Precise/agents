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
  Membership,
  Session,
  User,
  Workspace,
} from "@/api/generated";
import {
  APIFailure,
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

// The Workspace a page belongs to and the signed-in person's role in it.
export type WorkspaceScope = {
  workspace: Workspace;
  membership: Membership | null;
  manager: boolean;
};
const WorkspaceContext = createContext<WorkspaceScope | null>(null);
export const WorkspaceProvider = WorkspaceContext.Provider;
export function useWorkspace() {
  const value = useContext(WorkspaceContext);
  if (!value) throw new Error("workspace context missing");
  return value;
}

export async function loadWorkspace(
  user: User,
  workspaceId: string,
  signal: AbortSignal,
): Promise<WorkspaceScope> {
  const workspace = result(
    await sdk.getWorkspace({ path: { workspaceId }, signal }),
  );
  let membership: Membership | null = null;
  try {
    membership = result(
      await sdk.getMembership({
        path: { workspaceId, userId: user.id },
        signal,
      }),
    );
  } catch (e) {
    if (!(e instanceof APIFailure && e.status === 404)) throw e;
  }
  const manager =
    user.admin ||
    membership?.role === "owner" ||
    membership?.role === "maintainer";
  return { workspace, membership, manager };
}
