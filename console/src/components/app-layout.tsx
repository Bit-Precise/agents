import { Fragment, type ReactNode } from "react";
import {
  ArrowRightLeftIcon,
  BoxesIcon,
  GlobeIcon,
  LayoutDashboardIcon,
  LaptopIcon,
  ScrollTextIcon,
  ServerIcon,
  ShieldCheckIcon,
  UsersIcon,
  WaypointsIcon,
  AppWindowIcon,
} from "lucide-react";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import { Separator } from "@/components/ui/separator";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarInset,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import type { Workspace } from "@/api/generated";
import { NavMain, type NavItem } from "@/components/nav-main";
import { NavUser } from "@/components/nav-user";
import { WorkspaceSwitcher } from "@/components/workspace-switcher";
import { paths, type AdminSection, type WorkspaceSection } from "@/lib/routes";

export const workspaceNav: {
  section: WorkspaceSection;
  title: string;
  icon: NavItem["icon"];
}[] = [
  { section: "overview", title: "概览", icon: LayoutDashboardIcon },
  { section: "members", title: "成员管理", icon: UsersIcon },
  { section: "agents", title: "Agent 管理", icon: ServerIcon },
  { section: "ingresses", title: "Ingress 管理", icon: LaptopIcon },
  { section: "egresses", title: "Egress 管理", icon: WaypointsIcon },
  { section: "servers", title: "Server 管理", icon: AppWindowIcon },
  { section: "routes", title: "路由管理", icon: ArrowRightLeftIcon },
  { section: "access", title: "权限管理", icon: ShieldCheckIcon },
];
export const adminNav: {
  section: AdminSection;
  title: string;
  icon: NavItem["icon"];
}[] = [
  { section: "workspaces", title: "工作空间", icon: BoxesIcon },
  { section: "users", title: "用户", icon: UsersIcon },
  { section: "agents", title: "Agents", icon: ServerIcon },
  { section: "ingresses", title: "Ingress", icon: LaptopIcon },
  { section: "egresses", title: "Egress", icon: WaypointsIcon },
  { section: "servers", title: "Server", icon: GlobeIcon },
  { section: "audit", title: "审计日志", icon: ScrollTextIcon },
];

export function AppLayout({
  workspace,
  role,
  workspaceSection,
  adminSection,
  breadcrumbs,
  children,
}: {
  workspace?: Workspace;
  role?: string;
  workspaceSection?: WorkspaceSection;
  adminSection?: AdminSection;
  breadcrumbs: string[];
  children: ReactNode;
}) {
  const admin = adminSection !== undefined;
  const items: NavItem[] = admin
    ? adminNav.map((n) => ({
        title: n.title,
        icon: n.icon,
        url: paths.admin(n.section),
        active: n.section === adminSection,
      }))
    : workspace
      ? workspaceNav.map((n) => ({
          title: n.title,
          icon: n.icon,
          url: paths.workspace(workspace.id, n.section),
          active: n.section === workspaceSection,
        }))
      : [];
  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <SidebarHeader>
          <WorkspaceSwitcher active={workspace} role={role} admin={admin} />
        </SidebarHeader>
        <SidebarContent>
          {items.length > 0 && (
            <NavMain label={admin ? "实例管理" : "工作空间"} items={items} />
          )}
        </SidebarContent>
        <SidebarFooter>
          <NavUser />
        </SidebarFooter>
        <SidebarRail />
      </Sidebar>
      <SidebarInset>
        <header className="flex h-16 shrink-0 items-center gap-2 transition-[width,height] ease-linear group-has-data-[collapsible=icon]/sidebar-wrapper:h-12">
          <div className="flex items-center gap-2 px-4">
            <SidebarTrigger className="-ml-1" />
            <Separator
              orientation="vertical"
              className="mr-2 data-vertical:h-4 data-vertical:self-auto"
            />
            <Breadcrumb>
              <BreadcrumbList>
                {breadcrumbs.map((crumb, index) =>
                  index < breadcrumbs.length - 1 ? (
                    <Fragment key={index}>
                      <BreadcrumbItem className="hidden md:inline-flex">
                        {crumb}
                      </BreadcrumbItem>
                      <BreadcrumbSeparator className="hidden md:block" />
                    </Fragment>
                  ) : (
                    <BreadcrumbItem key={index}>
                      <BreadcrumbPage>{crumb}</BreadcrumbPage>
                    </BreadcrumbItem>
                  ),
                )}
              </BreadcrumbList>
            </Breadcrumb>
          </div>
        </header>
        <div className="flex flex-1 flex-col gap-4 p-4 pt-0 md:px-6">
          {children}
        </div>
      </SidebarInset>
    </SidebarProvider>
  );
}
