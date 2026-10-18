import { useEffect, useState } from "react";
import { MoreHorizontalIcon, PlusIcon } from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type { Audit, Membership, User, Workspace } from "@/api/generated";
import { requestSignal, result } from "@/api/session";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  DataTable,
  OptionSelect,
  TablePagination,
  TableToolbar,
  type Column,
} from "@/components/data-table";
import { Name, NameCell, StatusBadge, UserAvatar } from "@/components/cells";
import { useFeedback } from "@/components/feedback";
import { PageHeader } from "@/components/page-header";
import {
  AgentsTable,
  RoleViewsTable,
  ServersTable,
} from "@/components/resource-tables";
import {
  CreateUserDialog,
  CreateWorkspaceDialog,
} from "@/components/dialogs/workspace-dialogs";
import { rememberNames } from "@/lib/names";
import { usePage, useTableState } from "@/lib/use-page";
import { roleNames, time, workspaceLabel } from "@/lib/format";
import { Link } from "@/lib/router";
import { paths, type AdminSection } from "@/lib/routes";
import { useUser } from "@/lib/session";

const actionLabels: Record<string, string> = {
  bootstrap: "初始化实例",
  "workspace.create": "创建工作空间",
  "workspace.personal.create": "创建个人空间",
  "user.create": "创建用户",
  "user.oidc.create": "单点登录创建用户",
  "user.disable": "停用用户",
  "user.token.rotate": "更新用户令牌",
  "membership.set": "更新成员角色",
  "membership.remove": "移除成员",
  "agent.enroll": "注册设备",
  "agent.revoke": "撤销设备",
  "agent.desired.apply": "应用设备配置",
  "agent.desired.reject": "拒绝设备配置",
  "service.publish": "发布服务",
  "service.enabled": "更新服务状态",
  "grant.create": "添加授权",
  "grant.revoke": "撤销授权",
  "enrollment_key.create": "创建注册密钥",
  "enrollment_key.revoke": "撤销注册密钥",
  "egress_route.create": "创建出口路由",
  "egress_route.enabled": "更新出口路由状态",
  "service_account.create": "创建服务账号",
  "service_account.update": "更新服务账号",
  "service_account.revoke": "撤销服务账号",
  "device_authorization.begin": "开始设备授权",
  "device_authorization.authorize": "确认设备授权",
  "device_authorization.revoke": "拒绝设备授权",
};

function RowMenu({ children }: { children: React.ReactNode }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="ghost" size="icon-sm" aria-label="更多操作" />}
      >
        <MoreHorizontalIcon />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">{children}</DropdownMenuContent>
    </DropdownMenu>
  );
}

function AdminWorkspaces() {
  const table = useTableState();
  const [creating, setCreating] = useState(false);
  const data = usePage<Workspace>(
    async (query, signal) => {
      const page = result(await sdk.listAdminWorkspaces({ query, signal }));
      rememberNames("workspaces", page.items);
      return page;
    },
    {
      page: table.page,
      pageSize: table.pageSize,
      q: table.query,
      sortBy: "name",
    },
    "admin:workspaces",
  );
  const columns: Column<Workspace>[] = [
    {
      header: "工作空间",
      cell: (w) => <NameCell title={w.name} subtitle={w.id} />,
    },
    { header: "创建时间", cell: (w) => time(w.createdAt) },
    {
      header: "",
      className: "w-24 text-right",
      cell: (w) => (
        <Link
          className={buttonVariants({ variant: "outline", size: "sm" })}
          to={paths.workspace(w.id)}
        >
          进入
        </Link>
      ),
    },
  ];
  return (
    <>
      <PageHeader
        title="工作空间"
        description="实例中的全部工作空间。"
        actions={
          <Button onClick={() => setCreating(true)}>
            <PlusIcon />
            创建工作空间
          </Button>
        }
      />
      <TableToolbar search={table.search} onSearch={table.setSearch} />
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(w) => w.id}
        loading={data.loading}
        error={data.error}
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
      {creating && <CreateWorkspaceDialog onClose={() => setCreating(false)} />}
    </>
  );
}

function AdminUsers() {
  const viewer = useUser();
  const { confirm, showSecret } = useFeedback();
  const table = useTableState();
  const [disabled, setDisabled] = useState("all");
  const [creating, setCreating] = useState(false);
  const data = usePage<User>(
    async (query, signal) => {
      const page = result(await sdk.listAdminUsers({ query, signal }));
      rememberNames("users", page.items);
      return page;
    },
    {
      page: table.page,
      pageSize: table.pageSize,
      q: table.query,
      disabled: disabled === "all" ? undefined : disabled === "true",
      sortBy: "name",
    },
    "admin:users",
  );
  const columns: Column<User>[] = [
    {
      header: "用户",
      cell: (u) => (
        <div className="flex items-center gap-2">
          <UserAvatar user={u} className="size-7" />
          <NameCell title={u.name} subtitle={u.email || u.id} />
        </div>
      ),
    },
    {
      header: "实例角色",
      cell: (u) =>
        u.admin ? <Badge>管理员</Badge> : <Badge variant="outline">用户</Badge>,
    },
    {
      header: "状态",
      cell: (u) => (
        <StatusBadge
          label={u.disabled ? "已停用" : "有效"}
          tone={u.disabled ? "muted" : "active"}
        />
      ),
    },
    { header: "创建时间", cell: (u) => time(u.createdAt) },
    {
      header: "",
      className: "w-10",
      cell: (u) =>
        !u.disabled && (
          <RowMenu>
            <DropdownMenuItem
              onClick={() =>
                confirm({
                  title: `更新 ${u.name} 的访问令牌`,
                  description: "旧令牌立即失效，新令牌只显示一次。",
                  action: "更新令牌",
                  perform: async () => {
                    const r = result(
                      await sdk.createAdminUserToken({
                        path: { id: u.id },
                        signal: requestSignal(),
                      }),
                    );
                    showSecret({
                      title: "新的访问令牌",
                      description: u.name,
                      token: r.token,
                      filename: "agents-user-token.txt",
                    });
                  },
                })
              }
            >
              更新访问令牌
            </DropdownMenuItem>
            {u.id !== viewer.id && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() =>
                    confirm({
                      title: `停用 ${u.name}`,
                      description:
                        "该用户无法再登录，以其本人身份运行的设备与授权全部失效。",
                      action: "停用",
                      destructive: true,
                      done: data.reload,
                      perform: async () =>
                        result(
                          await sdk.updateAdminUser({
                            path: { id: u.id },
                            body: { disabled: true },
                            signal: requestSignal(),
                          }),
                        ),
                    })
                  }
                >
                  停用
                </DropdownMenuItem>
              </>
            )}
          </RowMenu>
        ),
    },
  ];
  return (
    <>
      <PageHeader
        title="用户"
        description="实例中的全部用户。启用单点登录时，用户首次登录会自动创建。"
        actions={
          <Button onClick={() => setCreating(true)}>
            <PlusIcon />
            创建用户
          </Button>
        }
      />
      <TableToolbar search={table.search} onSearch={table.setSearch}>
        <OptionSelect
          label="状态筛选"
          value={disabled}
          onChange={(v) => {
            setDisabled(v);
            table.setPage(1);
          }}
          options={[
            { value: "all", label: "全部状态" },
            { value: "false", label: "有效" },
            { value: "true", label: "已停用" },
          ]}
        />
      </TableToolbar>
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(u) => u.id}
        loading={data.loading}
        error={data.error}
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
      {creating && (
        <CreateUserDialog
          onClose={() => setCreating(false)}
          onDone={data.reload}
        />
      )}
    </>
  );
}

function AdminAudit() {
  const table = useTableState();
  const data = usePage<Audit>(
    async (query, signal) =>
      result(await sdk.listAdminAuditEvents({ query, signal })),
    {
      page: table.page,
      pageSize: table.pageSize,
      action: table.query,
      sortBy: "at",
      sortOrder: "desc",
    },
    "admin:audit",
  );
  const columns: Column<Audit>[] = [
    { header: "时间", cell: (a) => time(a.at, true), className: "w-44" },
    { header: "操作者", cell: (a) => <Name kind="users" id={a.actorId} /> },
    {
      header: "动作",
      cell: (a) => (
        <NameCell
          title={actionLabels[a.action] ?? a.action}
          subtitle={a.action}
        />
      ),
    },
    {
      header: "资源",
      cell: (a) => <span className="font-mono text-xs">{a.resourceId}</span>,
    },
    {
      header: "详情",
      cell: (a) => (
        <span className="text-muted-foreground">{a.detail || "—"}</span>
      ),
    },
  ];
  return (
    <>
      <PageHeader title="审计日志" description="实例范围内的资源变更记录。" />
      <TableToolbar
        search={table.search}
        onSearch={table.setSearch}
        placeholder="审计动作（精确匹配）"
      />
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(a) => a.id}
        loading={data.loading}
        error={data.error}
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
    </>
  );
}

export function AdminPage({ section }: { section: AdminSection }) {
  switch (section) {
    case "workspaces":
      return <AdminWorkspaces />;
    case "users":
      return <AdminUsers />;
    case "audit":
      return <AdminAudit />;
    case "agents":
      return (
        <>
          <PageHeader title="Agents" description="实例中的全部设备。" />
          <AgentsTable scope={{ admin: true }} />
        </>
      );
    case "ingresses":
      return (
        <>
          <PageHeader
            title="Ingress"
            description="实例中声明 Ingress 的全部设备。"
          />
          <RoleViewsTable role="ingress" scope={{ admin: true }} />
        </>
      );
    case "egresses":
      return (
        <>
          <PageHeader
            title="Egress"
            description="实例中声明 Egress 的全部设备。"
          />
          <RoleViewsTable role="egress" scope={{ admin: true }} />
        </>
      );
    case "servers":
      return (
        <>
          <PageHeader title="Server" description="实例中发布的全部服务。" />
          <ServersTable scope={{ admin: true }} />
        </>
      );
  }
}

function MyRole({ workspaceId }: { workspaceId: string }) {
  const viewer = useUser();
  const [role, setRole] = useState<Membership["role"] | null>(null);
  useEffect(() => {
    const abort = new AbortController();
    sdk
      .getMembership({
        path: { workspaceId, userId: viewer.id },
        signal: abort.signal,
      })
      .then((r) => setRole(result(r).role))
      .catch(() => {});
    return () => abort.abort();
  }, [workspaceId, viewer.id]);
  return role ? (
    <Badge variant="secondary">{roleNames[role]}</Badge>
  ) : (
    <span className="text-muted-foreground">—</span>
  );
}

export function WorkspacesPage() {
  const viewer = useUser();
  const table = useTableState();
  const [creating, setCreating] = useState(false);
  const data = usePage<Workspace>(
    async (query, signal) => {
      const page = result(await sdk.listWorkspaces({ query, signal }));
      rememberNames("workspaces", page.items);
      return page;
    },
    {
      page: table.page,
      pageSize: table.pageSize,
      q: table.query,
      userId: viewer.id,
      sortBy: "name",
    },
    "workspaces",
  );
  const columns: Column<Workspace>[] = [
    {
      header: "工作空间",
      cell: (w) => (
        <NameCell title={workspaceLabel(w.name, viewer.id)} subtitle={w.name} />
      ),
    },
    { header: "我的角色", cell: (w) => <MyRole workspaceId={w.id} /> },
    { header: "创建时间", cell: (w) => time(w.createdAt) },
    {
      header: "",
      className: "w-24 text-right",
      cell: (w) => (
        <Link
          className={buttonVariants({ variant: "outline", size: "sm" })}
          to={paths.workspace(w.id)}
        >
          进入
        </Link>
      ),
    },
  ];
  return (
    <>
      <PageHeader
        title="工作空间"
        description="你加入的全部工作空间。"
        actions={
          <Button onClick={() => setCreating(true)}>
            <PlusIcon />
            创建工作空间
          </Button>
        }
      />
      <TableToolbar search={table.search} onSearch={table.setSearch} />
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(w) => w.id}
        loading={data.loading}
        error={data.error}
        emptyTitle="你还没有加入工作空间"
        emptyDescription="创建一个工作空间，或请管理员把你加入已有的工作空间。"
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
      {creating && <CreateWorkspaceDialog onClose={() => setCreating(false)} />}
    </>
  );
}
