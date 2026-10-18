import { useEffect, useState } from "react";
import {
  ChevronsUpDownIcon,
  LayoutGridIcon,
  PlusIcon,
  ShieldIcon,
} from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type { Workspace } from "@/api/generated";
import { result } from "@/api/session";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { CreateWorkspaceDialog } from "@/components/dialogs/workspace-dialogs";
import { navigate } from "@/lib/router";
import { paths } from "@/lib/routes";
import { initials, roleNames, workspaceLabel } from "@/lib/format";
import { useUser } from "@/lib/session";

export function WorkspaceSwitcher({
  active,
  role,
  admin,
}: {
  active?: Workspace;
  role?: string;
  admin?: boolean;
}) {
  const user = useUser();
  const { isMobile } = useSidebar();
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [open, setOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  useEffect(() => {
    if (!open) return;
    const abort = new AbortController();
    sdk
      .listWorkspaces({
        query: { userId: user.id, pageSize: 100, sortBy: "name" },
        signal: abort.signal,
      })
      .then((r) => setWorkspaces(result(r).items))
      .catch(() => {});
    return () => abort.abort();
  }, [open, user.id]);
  const title = admin
    ? "管理员面板"
    : active
      ? workspaceLabel(active.name, user.id)
      : "选择工作空间";
  const subtitle = admin
    ? "实例管理"
    : role
      ? roleNames[role]
      : user.admin
        ? "实例管理员"
        : "";
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu open={open} onOpenChange={setOpen}>
          <DropdownMenuTrigger
            render={
              <SidebarMenuButton
                size="lg"
                aria-label="切换工作空间"
                className="data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground"
              />
            }
          >
            <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
              {admin ? <ShieldIcon className="size-4" /> : initials(title)}
            </div>
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">{title}</span>
              <span className="truncate text-xs">{subtitle}</span>
            </div>
            <ChevronsUpDownIcon className="ml-auto" />
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-(--anchor-width) min-w-56"
            align="start"
            side={isMobile ? "bottom" : "right"}
            sideOffset={4}
          >
            <DropdownMenuGroup>
              <DropdownMenuLabel className="text-xs text-muted-foreground">
                工作空间
              </DropdownMenuLabel>
              {workspaces.map((w) => (
                <DropdownMenuItem
                  key={w.id}
                  className="gap-2 p-2"
                  onClick={() => navigate(paths.workspace(w.id))}
                >
                  <div className="flex size-6 items-center justify-center rounded-md border text-xs">
                    {initials(workspaceLabel(w.name, user.id))}
                  </div>
                  <span className="truncate">
                    {workspaceLabel(w.name, user.id)}
                  </span>
                  {w.id === active?.id && !admin && (
                    <span className="ml-auto text-xs text-muted-foreground">
                      当前
                    </span>
                  )}
                </DropdownMenuItem>
              ))}
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem
                className="gap-2 p-2"
                onClick={() => navigate(paths.workspaces)}
              >
                <div className="flex size-6 items-center justify-center rounded-md border bg-transparent">
                  <LayoutGridIcon className="size-4" />
                </div>
                <span className="text-muted-foreground">管理工作空间</span>
              </DropdownMenuItem>
              <DropdownMenuItem
                className="gap-2 p-2"
                onClick={() => setCreating(true)}
              >
                <div className="flex size-6 items-center justify-center rounded-md border bg-transparent">
                  <PlusIcon className="size-4" />
                </div>
                <span className="text-muted-foreground">创建工作空间</span>
              </DropdownMenuItem>
            </DropdownMenuGroup>
            {user.admin && !admin && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  className="gap-2 p-2"
                  onClick={() => navigate(paths.admin())}
                >
                  <div className="flex size-6 items-center justify-center rounded-md border bg-transparent">
                    <ShieldIcon className="size-4" />
                  </div>
                  <span className="text-muted-foreground">进入管理员面板</span>
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
      {creating && <CreateWorkspaceDialog onClose={() => setCreating(false)} />}
    </SidebarMenu>
  );
}
