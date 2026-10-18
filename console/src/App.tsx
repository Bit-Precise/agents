import { useEffect, useState, type ReactNode } from "react";
import { SearchXIcon, ShieldOffIcon } from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import { APIFailure, message, onUnauthorized, result } from "@/api/session";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { buttonVariants } from "@/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { AppLayout, adminNav, workspaceNav } from "@/components/app-layout";
import { FeedbackProvider } from "@/components/feedback";
import { AdminPage, WorkspacesPage } from "@/pages/admin-pages";
import { DevicePage } from "@/pages/device-page";
import { LoginPage } from "@/pages/login-page";
import {
  AccessPage,
  AgentsPage,
  EgressesPage,
  IngressesPage,
  MembersPage,
  OverviewPage,
  RoutesPage,
  ServersPage,
} from "@/pages/workspace-pages";
import { workspaceLabel } from "@/lib/format";
import { Link, navigate, useLocation } from "@/lib/router";
import { parseRoute, paths, type WorkspaceSection } from "@/lib/routes";
import {
  loadWorkspace,
  SessionProvider,
  useSession,
  useUser,
  WorkspaceProvider,
  type WorkspaceScope,
} from "@/lib/session";

export default function App() {
  return (
    <SessionProvider>
      <FeedbackProvider>
        <Router />
      </FeedbackProvider>
    </SessionProvider>
  );
}

function Router() {
  const { booting, user, expired } = useSession();
  const { path } = useLocation();
  useEffect(() => onUnauthorized(expired), [expired]);
  useEffect(() => {
    // Earlier releases sent the Device Flow confirmation to #device/<id>.
    if (location.hash.startsWith("#device/"))
      navigate(paths.device(decodeURIComponent(location.hash.slice(8))), {
        replace: true,
      });
  }, []);
  if (booting)
    return (
      <div className="flex min-h-svh items-center justify-center">
        <Spinner className="size-6" />
      </div>
    );
  if (!user) return <LoginPage />;
  const route = parseRoute(path);
  switch (route.kind) {
    case "home":
    case "login":
      return <Home />;
    case "device":
      return <DevicePage id={route.id} />;
    case "workspaces":
      return (
        <AppLayout breadcrumbs={["工作空间"]}>
          <WorkspacesPage />
        </AppLayout>
      );
    case "workspace":
      return (
        <WorkspaceRoute
          key={route.workspaceId}
          workspaceId={route.workspaceId}
          section={route.section}
        />
      );
    case "admin":
      return (
        <AppLayout
          adminSection={route.section}
          breadcrumbs={[
            "管理员面板",
            adminNav.find((n) => n.section === route.section)?.title ?? "",
          ]}
        >
          {user.admin ? (
            <AdminPage section={route.section} />
          ) : (
            <Message
              icon={<ShieldOffIcon />}
              title="需要实例管理员权限"
              description="管理员面板只对实例管理员开放。"
            />
          )}
        </AppLayout>
      );
    case "notFound":
      return (
        <AppLayout breadcrumbs={["页面不存在"]}>
          <Message
            icon={<SearchXIcon />}
            title="页面不存在"
            description="请检查地址，或从侧边栏选择页面。"
          />
        </AppLayout>
      );
  }
}

function Message({
  icon,
  title,
  description,
}: {
  icon: ReactNode;
  title: string;
  description: string;
}) {
  return (
    <Empty className="flex-1">
      <EmptyHeader>
        <EmptyMedia variant="icon">{icon}</EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{description}</EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Link
          className={buttonVariants({ variant: "outline" })}
          to={paths.home}
        >
          返回首页
        </Link>
      </EmptyContent>
    </Empty>
  );
}

// "/" opens the personal Workspace, else the first one, else the list.
function Home() {
  const user = useUser();
  const [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    sdk
      .listWorkspaces({
        query: { userId: user.id, pageSize: 100, sortBy: "name" },
        signal: abort.signal,
      })
      .then((r) => {
        const items = result(r).items;
        const preferred =
          items.find((w) => w.name === "personal-" + user.id) ?? items[0];
        navigate(
          preferred
            ? paths.workspace(preferred.id)
            : user.admin
              ? paths.admin()
              : paths.workspaces,
          {
            replace: true,
          },
        );
      })
      .catch((e) => !abort.signal.aborted && setError(message(e)));
    return () => abort.abort();
  }, [user.id, user.admin]);
  return error ? (
    <div className="p-6">
      <Alert variant="destructive">
        <AlertDescription>{error}</AlertDescription>
      </Alert>
    </div>
  ) : (
    <div className="flex min-h-svh items-center justify-center">
      <Spinner className="size-6" />
    </div>
  );
}

const pages: Record<WorkspaceSection, () => ReactNode> = {
  overview: () => <OverviewPage />,
  members: () => <MembersPage />,
  agents: () => <AgentsPage />,
  ingresses: () => <IngressesPage />,
  egresses: () => <EgressesPage />,
  servers: () => <ServersPage />,
  routes: () => <RoutesPage />,
  access: () => <AccessPage />,
};

function WorkspaceRoute({
  workspaceId,
  section,
}: {
  workspaceId: string;
  section: WorkspaceSection;
}) {
  const user = useUser();
  const { path } = useLocation();
  const [scope, setScope] = useState<WorkspaceScope | null>(null);
  const [error, setError] = useState<{
    status: number;
    message: string;
  } | null>(null);
  useEffect(() => {
    if (!path.endsWith("/" + section))
      navigate(paths.workspace(workspaceId, section), { replace: true });
  }, [path, section, workspaceId]);
  useEffect(() => {
    const abort = new AbortController();
    loadWorkspace(user, workspaceId, abort.signal)
      .then(setScope)
      .catch(
        (e) =>
          !abort.signal.aborted &&
          setError({
            status: e instanceof APIFailure ? e.status : 0,
            message: message(e),
          }),
      );
    return () => abort.abort();
  }, [user, workspaceId]);
  const title = workspaceNav.find((n) => n.section === section)?.title ?? "";
  if (error)
    return (
      <AppLayout breadcrumbs={["工作空间"]}>
        {error.status === 404 ? (
          <Message
            icon={<SearchXIcon />}
            title="找不到这个工作空间"
            description="它不存在，或你不是它的成员。"
          />
        ) : (
          <Alert variant="destructive">
            <AlertDescription>{error.message}</AlertDescription>
          </Alert>
        )}
      </AppLayout>
    );
  if (!scope)
    return (
      <AppLayout breadcrumbs={["工作空间", title]}>
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-64 w-full" />
      </AppLayout>
    );
  return (
    <WorkspaceProvider value={scope}>
      <AppLayout
        workspace={scope.workspace}
        role={scope.membership?.role}
        workspaceSection={section}
        breadcrumbs={[workspaceLabel(scope.workspace.name, user.id), title]}
      >
        {pages[section]()}
      </AppLayout>
    </WorkspaceProvider>
  );
}
