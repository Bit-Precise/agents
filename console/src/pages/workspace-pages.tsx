import { useState } from "react";
import { MoreHorizontalIcon, PlusIcon } from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type {
  EnrollmentKey,
  FlowStatus,
  Grant,
  Membership,
  Pagination,
  RouteView,
  ServiceAccount,
} from "@/api/generated";
import { requestSignal, result } from "@/api/session";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import {
  DataTable,
  OptionSelect,
  TablePagination,
  TableToolbar,
  type Column,
} from "@/components/data-table";
import {
  Name,
  NameCell,
  PrincipalCell,
  RoleBadges,
  StatusBadge,
  UserCell,
} from "@/components/cells";
import { useFeedback } from "@/components/feedback";
import { PageHeader } from "@/components/page-header";
import {
  AgentsTable,
  RoleViewsTable,
  ServersTable,
} from "@/components/resource-tables";
import { RegisterDeviceDialog } from "@/components/dialogs/agent-dialogs";
import {
  GrantDialog,
  RouteDialog,
} from "@/components/dialogs/route-grant-dialogs";
import {
  EnrollmentKeyDialog,
  ServiceAccountDialog,
} from "@/components/dialogs/service-account-dialogs";
import { MemberDialog } from "@/components/dialogs/workspace-dialogs";
import { rememberNames, useEntity } from "@/lib/names";
import { usePage, useTableState } from "@/lib/use-page";
import { isSelf, roleNames, rule, short, time } from "@/lib/format";
import { useQueryParam } from "@/lib/router";
import { useUser, useWorkspace } from "@/lib/session";

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

function useCount(
  load: (signal: AbortSignal) => Promise<{ pagination: Pagination }>,
  key: string,
) {
  const data = usePage(
    async (_query, signal) => ({
      items: [],
      pagination: (await load(signal)).pagination,
    }),
    {},
    key,
  );
  return data.page ? data.page.pagination.total : undefined;
}

export function OverviewPage() {
  const { workspace } = useWorkspace();
  const w = { path: { workspaceId: workspace.id } };
  const agents = useCount(
    async (signal) =>
      result(
        await sdk.listWorkspaceAgents({
          ...w,
          query: { pageSize: 1, status: "active" },
          signal,
        }),
      ),
    workspace.id + ":agents",
  );
  const servers = useCount(
    async (signal) =>
      result(
        await sdk.listWorkspaceServers({
          ...w,
          query: { pageSize: 1, enabled: true },
          signal,
        }),
      ),
    workspace.id + ":servers",
  );
  const routes = useCount(
    async (signal) =>
      result(
        await sdk.listWorkspaceRoutes({
          ...w,
          query: { pageSize: 1, kind: "egress", enabled: true },
          signal,
        }),
      ),
    workspace.id + ":routes",
  );
  const table = useTableState();
  const flows = usePage<FlowStatus>(
    async (query, signal) =>
      result(await sdk.listWorkspaceFlows({ ...w, query, signal })),
    {
      page: table.page,
      pageSize: table.pageSize,
      sortBy: "startedAt",
      sortOrder: "desc",
    },
    workspace.id + ":flows",
  );
  const cards = [
    { title: "Agents", value: agents, foot: "有效的设备" },
    { title: "已发布服务", value: servers, foot: "处于发布状态的 Server" },
    { title: "出口路由", value: routes, foot: "已启用的 TLS 出口" },
    {
      title: "活动流",
      value: flows.error ? undefined : flows.page?.pagination.total,
      foot: "当前正在传输的连接",
    },
  ];
  const columns: Column<FlowStatus>[] = [
    {
      header: "用途",
      cell: (f) => (
        <Badge variant="outline">
          {f.purpose === "serve" ? "Server" : "Egress"}
        </Badge>
      ),
    },
    {
      header: "资源",
      cell: (f) => (
        <Name
          kind={f.purpose === "serve" ? "servers" : "routes"}
          id={f.resourceId}
          workspaceId={f.workspaceId}
        />
      ),
    },
    {
      header: "提供方",
      cell: (f) => (
        <Name kind="agents" id={f.agentId} workspaceId={f.workspaceId} />
      ),
    },
    {
      header: "使用者",
      cell: (f) => (
        <PrincipalCell principal={f.consumer} workspaceId={f.workspaceId} />
      ),
    },
    {
      header: "状态",
      cell: (f) => (
        <StatusBadge
          label={f.state === "active" ? "传输中" : "建立中"}
          tone="active"
        />
      ),
    },
    { header: "开始时间", cell: (f) => time(f.startedAt) },
  ];
  return (
    <>
      <PageHeader title="概览" description="设备、服务与访问连接的最新状态。" />
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {cards.map((card) => (
          <Card key={card.title}>
            <CardHeader>
              <CardDescription>{card.title}</CardDescription>
              <CardTitle className="text-2xl font-semibold tabular-nums">
                {card.value ?? <Skeleton className="h-8 w-12" />}
              </CardTitle>
            </CardHeader>
            <CardFooter className="text-sm text-muted-foreground">
              {card.foot}
            </CardFooter>
          </Card>
        ))}
      </div>
      <h2 className="mt-2 text-base font-medium">活动流</h2>
      {flows.error ? (
        <Alert>
          <AlertDescription>
            Edge 状态暂不可用，不能据此判断连接已经结束。
          </AlertDescription>
        </Alert>
      ) : (
        <>
          <DataTable
            columns={columns}
            rows={flows.items}
            rowKey={(f) => f.id}
            loading={flows.loading}
            emptyTitle="没有活动流"
            emptyDescription="访问服务或使用出口时，正在传输的连接会显示在这里。"
          />
          <TablePagination
            pagination={flows.page?.pagination}
            onPage={table.setPage}
            onPageSize={table.setPageSize}
          />
        </>
      )}
    </>
  );
}

export function MembersPage() {
  const { workspace, manager } = useWorkspace();
  const [tab, setTab] = useQueryParam("tab", "members");
  const [creating, setCreating] = useState<
    "member" | "serviceAccount" | "key" | null
  >(null);
  const [version, setVersion] = useState(0);
  const reload = () => setVersion((v) => v + 1);
  const actions = manager && (
    <Button
      onClick={() =>
        setCreating(
          tab === "service-accounts"
            ? "serviceAccount"
            : tab === "keys"
              ? "key"
              : "member",
        )
      }
    >
      <PlusIcon />
      {tab === "service-accounts"
        ? "创建服务账号"
        : tab === "keys"
          ? "创建注册密钥"
          : "添加成员"}
    </Button>
  );
  return (
    <>
      <PageHeader
        title="成员管理"
        description="成员与服务账号是工作空间里的两类身份；注册密钥让服务账号接入无人值守的设备。"
        actions={actions}
      />
      <Tabs value={tab} onValueChange={(v) => setTab(String(v))}>
        <TabsList>
          <TabsTrigger value="members">成员</TabsTrigger>
          <TabsTrigger value="service-accounts">服务账号</TabsTrigger>
          <TabsTrigger value="keys">注册密钥</TabsTrigger>
        </TabsList>
      </Tabs>
      {tab === "service-accounts" ? (
        manager ? (
          <ServiceAccountsTable key={version} />
        ) : (
          <Alert>
            <AlertDescription>
              只有维护者和所有者可以查看与管理服务账号。
            </AlertDescription>
          </Alert>
        )
      ) : tab === "keys" ? (
        <EnrollmentKeysTable key={version} />
      ) : (
        <MembershipsTable key={version} />
      )}
      {creating === "member" && (
        <MemberDialog
          workspaceId={workspace.id}
          onClose={() => setCreating(null)}
          onDone={reload}
        />
      )}
      {creating === "serviceAccount" && (
        <ServiceAccountDialog
          workspaceId={workspace.id}
          onClose={() => setCreating(null)}
          onDone={reload}
        />
      )}
      {creating === "key" && (
        <EnrollmentKeyDialog
          workspaceId={workspace.id}
          onClose={() => setCreating(null)}
          onDone={reload}
        />
      )}
    </>
  );
}

function MembershipsTable() {
  const viewer = useUser();
  const { workspace, manager } = useWorkspace();
  const { confirm } = useFeedback();
  const table = useTableState();
  const [role, setRole] = useState("all");
  const [editing, setEditing] = useState<Membership | null>(null);
  const data = usePage<Membership>(
    async (query, signal) =>
      result(
        await sdk.listWorkspaceMemberships({
          path: { workspaceId: workspace.id },
          query,
          signal,
        }),
      ),
    {
      page: table.page,
      pageSize: table.pageSize,
      role: role === "all" ? undefined : role,
      userId: table.query,
    },
    workspace.id + ":members",
  );
  const columns: Column<Membership>[] = [
    {
      header: "成员",
      cell: (m) => <UserCell id={m.userId} workspaceId={m.workspaceId} />,
    },
    {
      header: "角色",
      cell: (m) => <Badge variant="secondary">{roleNames[m.role]}</Badge>,
    },
    {
      header: "",
      className: "w-10",
      cell: (m) =>
        manager && (
          <RowMenu>
            <DropdownMenuItem onClick={() => setEditing(m)}>
              修改角色
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="destructive"
              onClick={() =>
                confirm({
                  title: "移除成员",
                  description:
                    "该用户失去此工作空间的成员身份，以其本人身份运行的设备会被撤销。",
                  action: "移除",
                  destructive: true,
                  done: data.reload,
                  perform: async () => {
                    const options = {
                      path: { workspaceId: m.workspaceId, userId: m.userId },
                      signal: requestSignal(),
                    };
                    result(
                      viewer.admin
                        ? await sdk.deleteAdminMembership(options)
                        : await sdk.deleteMembership(options),
                    );
                  },
                })
              }
            >
              移除
            </DropdownMenuItem>
          </RowMenu>
        ),
    },
  ];
  return (
    <>
      <TableToolbar
        search={table.search}
        onSearch={table.setSearch}
        placeholder="用户 ID（精确匹配）"
      >
        <OptionSelect
          label="角色筛选"
          value={role}
          onChange={(v) => {
            setRole(v);
            table.setPage(1);
          }}
          options={[
            { value: "all", label: "全部角色" },
            ...["owner", "maintainer", "developer", "guest"].map((r) => ({
              value: r,
              label: roleNames[r],
            })),
          ]}
        />
      </TableToolbar>
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(m) => m.userId}
        loading={data.loading}
        error={data.error}
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
      {editing && (
        <MemberDialog
          workspaceId={workspace.id}
          member={editing}
          onClose={() => setEditing(null)}
          onDone={data.reload}
        />
      )}
    </>
  );
}

function ServiceAccountsTable() {
  const viewer = useUser();
  const { workspace } = useWorkspace();
  const { confirm } = useFeedback();
  const table = useTableState();
  const [status, setStatus] = useState("active");
  const [editing, setEditing] = useState<ServiceAccount | null>(null);
  const [keyFor, setKeyFor] = useState<ServiceAccount | null>(null);
  const data = usePage<ServiceAccount>(
    async (query, signal) => {
      const page = result(
        await sdk.listWorkspaceServiceAccounts({
          path: { workspaceId: workspace.id },
          query,
          signal,
        }),
      );
      rememberNames("serviceAccounts", page.items);
      return page;
    },
    {
      page: table.page,
      pageSize: table.pageSize,
      q: table.query,
      status: status === "all" ? undefined : status,
      sortBy: "name",
    },
    workspace.id + ":sa",
  );
  const columns: Column<ServiceAccount>[] = [
    {
      header: "服务账号",
      cell: (s) => (
        <NameCell title={s.name} subtitle={s.description || short(s.id)} />
      ),
    },
    {
      header: "状态",
      cell: (s) => (
        <StatusBadge
          label={s.status === "active" ? "有效" : "已撤销"}
          tone={s.status === "active" ? "active" : "muted"}
        />
      ),
    },
    {
      header: "创建者",
      cell: (s) =>
        s.createdBy ? (
          <Name kind="users" id={s.createdBy} workspaceId={workspace.id} />
        ) : (
          "—"
        ),
    },
    { header: "创建时间", cell: (s) => time(s.createdAt) },
    {
      header: "",
      className: "w-10",
      cell: (s) =>
        s.status === "active" && (
          <RowMenu>
            <DropdownMenuItem onClick={() => setKeyFor(s)}>
              创建注册密钥
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => setEditing(s)}>
              编辑说明
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="destructive"
              onClick={() =>
                confirm({
                  title: `撤销服务账号 ${s.name}`,
                  description:
                    "以它运行的全部设备、未使用的注册密钥和授权立即失效，且不能恢复。",
                  action: "撤销",
                  destructive: true,
                  done: data.reload,
                  perform: async () =>
                    result(
                      viewer.admin
                        ? await sdk.updateAdminServiceAccount({
                            path: { id: s.id },
                            body: { status: "revoked" },
                            signal: requestSignal(),
                          })
                        : await sdk.updateWorkspaceServiceAccount({
                            path: {
                              workspaceId: workspace.id,
                              serviceAccountId: s.id,
                            },
                            body: { status: "revoked" },
                            signal: requestSignal(),
                          }),
                    ),
                })
              }
            >
              撤销
            </DropdownMenuItem>
          </RowMenu>
        ),
    },
  ];
  return (
    <>
      <TableToolbar search={table.search} onSearch={table.setSearch}>
        <OptionSelect
          label="状态筛选"
          value={status}
          onChange={(v) => {
            setStatus(v);
            table.setPage(1);
          }}
          options={[
            { value: "all", label: "全部状态" },
            { value: "active", label: "有效" },
            { value: "revoked", label: "已撤销" },
          ]}
        />
      </TableToolbar>
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(s) => s.id}
        loading={data.loading}
        error={data.error}
        emptyTitle="还没有服务账号"
        emptyDescription="服务账号让服务器等无人值守的设备以团队身份运行。"
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
      {editing && (
        <ServiceAccountDialog
          workspaceId={workspace.id}
          account={editing}
          onClose={() => setEditing(null)}
          onDone={data.reload}
        />
      )}
      {keyFor && (
        <EnrollmentKeyDialog
          workspaceId={workspace.id}
          account={keyFor}
          onClose={() => setKeyFor(null)}
          onDone={data.reload}
        />
      )}
    </>
  );
}

function EnrollmentKeysTable() {
  const viewer = useUser();
  const { workspace, manager } = useWorkspace();
  const { confirm } = useFeedback();
  const table = useTableState();
  const [revoked, setRevoked] = useState("false");
  const data = usePage<EnrollmentKey>(
    async (query, signal) =>
      result(
        await sdk.listWorkspaceEnrollmentKeys({
          path: { workspaceId: workspace.id },
          query,
          signal,
        }),
      ),
    {
      page: table.page,
      pageSize: table.pageSize,
      revoked: revoked === "all" ? undefined : revoked === "true",
      sortBy: "expiresAt",
      sortOrder: "desc",
    },
    workspace.id + ":keys",
  );
  const state = (k: EnrollmentKey) =>
    k.revoked
      ? "已撤销"
      : new Date(k.expiresAt) <= new Date()
        ? "已过期"
        : k.uses >= k.maxUses
          ? "已用完"
          : "可用";
  const columns: Column<EnrollmentKey>[] = [
    {
      header: "密钥",
      cell: (k) => <span className="font-mono text-xs">{short(k.id)}</span>,
    },
    {
      header: "服务账号",
      cell: (k) =>
        k.serviceAccountId ? (
          <Name
            kind="serviceAccounts"
            id={k.serviceAccountId}
            workspaceId={workspace.id}
          />
        ) : (
          <span className="text-muted-foreground">
            <Name kind="users" id={k.userId} workspaceId={workspace.id} />
            （旧版个人密钥）
          </span>
        ),
    },
    { header: "角色", cell: (k) => <RoleBadges roles={k.roles} /> },
    { header: "使用次数", cell: (k) => `${k.uses} / ${k.maxUses}` },
    { header: "失效时间", cell: (k) => time(k.expiresAt) },
    {
      header: "状态",
      cell: (k) => (
        <StatusBadge
          label={state(k)}
          tone={state(k) === "可用" ? "active" : "muted"}
        />
      ),
    },
    {
      header: "",
      className: "w-10",
      cell: (k) =>
        !k.revoked &&
        (manager || k.userId === viewer.id) && (
          <RowMenu>
            <DropdownMenuItem
              variant="destructive"
              onClick={() =>
                confirm({
                  title: "撤销注册密钥",
                  description: "此密钥不能再注册新设备；已注册的设备不受影响。",
                  action: "撤销",
                  destructive: true,
                  done: data.reload,
                  perform: async () =>
                    result(
                      viewer.admin
                        ? await sdk.updateAdminEnrollmentKey({
                            path: { id: k.id },
                            body: { revoked: true },
                            signal: requestSignal(),
                          })
                        : await sdk.updateWorkspaceEnrollmentKey({
                            path: { workspaceId: workspace.id, keyId: k.id },
                            body: { revoked: true },
                            signal: requestSignal(),
                          }),
                    ),
                })
              }
            >
              撤销
            </DropdownMenuItem>
          </RowMenu>
        ),
    },
  ];
  return (
    <>
      <TableToolbar>
        <OptionSelect
          label="撤销筛选"
          value={revoked}
          onChange={(v) => {
            setRevoked(v);
            table.setPage(1);
          }}
          options={[
            { value: "false", label: "未撤销" },
            { value: "true", label: "已撤销" },
            { value: "all", label: "全部" },
          ]}
        />
      </TableToolbar>
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(k) => k.id}
        loading={data.loading}
        error={data.error}
        emptyTitle="没有注册密钥"
        emptyDescription="在服务账号中创建注册密钥，用于 agentd enroll --key-file。"
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
    </>
  );
}

export function AgentsPage() {
  const { workspace, manager } = useWorkspace();
  const [registering, setRegistering] = useState(false);
  return (
    <>
      <PageHeader
        title="Agent 管理"
        description="注册到此工作空间的设备，以及它们的运行身份与在线状态。"
        actions={
          <Button onClick={() => setRegistering(true)}>
            <PlusIcon />
            注册设备
          </Button>
        }
      />
      <AgentsTable
        scope={{ admin: false, workspaceId: workspace.id, manager }}
      />
      {registering && (
        <RegisterDeviceDialog
          workspaceId={workspace.id}
          workspaceName={workspace.name}
          onClose={() => setRegistering(false)}
        />
      )}
    </>
  );
}

export function IngressesPage() {
  const { workspace, manager } = useWorkspace();
  return (
    <>
      <PageHeader
        title="Ingress 管理"
        description="在本机提供 HTTP CONNECT / SOCKS 入口、把获准流量送往远端出口的设备。"
      />
      <RoleViewsTable
        role="ingress"
        scope={{ admin: false, workspaceId: workspace.id, manager }}
      />
    </>
  );
}

export function EgressesPage() {
  const { workspace, manager } = useWorkspace();
  return (
    <>
      <PageHeader
        title="Egress 管理"
        description="作为出口、按路由与授权把流量送往目标网站的设备。"
      />
      <RoleViewsTable
        role="egress"
        scope={{ admin: false, workspaceId: workspace.id, manager }}
      />
    </>
  );
}

export function ServersPage() {
  const { workspace, manager } = useWorkspace();
  return (
    <>
      <PageHeader
        title="Server 管理"
        description="设备发布的服务。访问者需要有效授权，删除授权不会退回全员访问。"
      />
      <ServersTable
        scope={{ admin: false, workspaceId: workspace.id, manager }}
      />
    </>
  );
}

function RouteActions({
  route,
  reload,
}: {
  route: RouteView;
  reload: () => void;
}) {
  const viewer = useUser();
  const { manager } = useWorkspace();
  const { confirm } = useFeedback();
  const [granting, setGranting] = useState(false);
  const agent = useEntity("agents", route.agentId, route.workspaceId);
  if (!(
    manager ||
    (route.kind === "serve" && isSelf(agent?.principal, viewer.id))
  ))
    return null;
  return (
    <>
      <RowMenu>
        <DropdownMenuItem onClick={() => setGranting(true)}>
          添加授权
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          variant={route.enabled ? "destructive" : "default"}
          onClick={() =>
            confirm({
              title: `${route.enabled ? "停用" : "启用"} ${route.name}`,
              description: "更新这条路由的启用状态。",
              action: route.enabled ? "停用" : "启用",
              destructive: route.enabled,
              done: reload,
              perform: async () =>
                result(
                  await sdk.updateWorkspaceRoute({
                    path: { workspaceId: route.workspaceId, routeId: route.id },
                    body: { enabled: !route.enabled },
                    signal: requestSignal(),
                  }),
                ),
            })
          }
        >
          {route.enabled ? "停用" : "启用"}
        </DropdownMenuItem>
      </RowMenu>
      {granting && (
        <GrantDialog
          workspaceId={route.workspaceId}
          manager={manager}
          resource={{
            type: route.kind === "serve" ? "service" : "egress",
            id: route.kind === "serve" ? route.targetId : route.id,
            label: route.name,
          }}
          onClose={() => setGranting(false)}
          onDone={reload}
        />
      )}
    </>
  );
}

export function RoutesPage() {
  const { workspace, manager } = useWorkspace();
  const table = useTableState();
  const [kind, setKind] = useState("all");
  const [creating, setCreating] = useState(false);
  const data = usePage<RouteView>(
    async (query, signal) => {
      const page = result(
        await sdk.listWorkspaceRoutes({
          path: { workspaceId: workspace.id },
          query,
          signal,
        }),
      );
      rememberNames("routes", page.items);
      return page;
    },
    {
      page: table.page,
      pageSize: table.pageSize,
      q: table.query,
      kind: kind === "all" ? undefined : kind,
      sortBy: "name",
    },
    workspace.id + ":routes",
  );
  const columns: Column<RouteView>[] = [
    {
      header: "名称",
      cell: (r) => <NameCell title={r.name} subtitle={short(r.id)} />,
    },
    {
      header: "类型",
      cell: (r) => (
        <Badge variant="outline">
          {r.kind === "serve" ? "服务发布" : "TLS 出口"}
        </Badge>
      ),
    },
    {
      header: "目标设备",
      cell: (r) => (
        <Name kind="agents" id={r.agentId} workspaceId={r.workspaceId} />
      ),
    },
    {
      header: "匹配规则",
      cell: (r) => (
        <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
          {rule({ ...r.rule, protocol: "tls" })} · {r.rule.protocol}
        </code>
      ),
    },
    {
      header: "优先级",
      cell: (r) => (r.kind === "egress" ? r.priority : "—"),
      className: "w-16",
    },
    {
      header: "状态",
      cell: (r) => (
        <StatusBadge
          label={r.enabled ? "启用" : "停用"}
          tone={r.enabled ? "active" : "muted"}
        />
      ),
    },
    {
      header: "",
      className: "w-10",
      cell: (r) => <RouteActions route={r} reload={data.reload} />,
    },
  ];
  return (
    <>
      <PageHeader
        title="路由管理"
        description="服务发布路由由设备配置生成；出口路由把 TLS 域名交给指定的 Egress 设备。"
        actions={
          manager && (
            <Button onClick={() => setCreating(true)}>
              <PlusIcon />
              创建出口路由
            </Button>
          )
        }
      />
      <TableToolbar search={table.search} onSearch={table.setSearch}>
        <OptionSelect
          label="类型筛选"
          value={kind}
          onChange={(v) => {
            setKind(v);
            table.setPage(1);
          }}
          options={[
            { value: "all", label: "全部类型" },
            { value: "serve", label: "服务发布" },
            { value: "egress", label: "TLS 出口" },
          ]}
        />
      </TableToolbar>
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(r) => r.id}
        loading={data.loading}
        error={data.error}
        emptyTitle="还没有路由"
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
      {creating && (
        <RouteDialog
          workspaceId={workspace.id}
          onClose={() => setCreating(false)}
          onDone={data.reload}
        />
      )}
    </>
  );
}

function grantState(g: Grant) {
  if (g.revoked) return { label: "已撤销", tone: "muted" as const };
  if (new Date(g.expiresAt) <= new Date())
    return { label: "已过期", tone: "muted" as const };
  if (new Date(g.startsAt) > new Date())
    return { label: "未开始", tone: "warning" as const };
  return { label: "生效中", tone: "active" as const };
}

function GrantSubject({ grant }: { grant: Grant }) {
  if (grant.subjectType === "anonymous") return <span>匿名访问者</span>;
  if (grant.subjectType === "workspace")
    return (
      <span>
        <Name kind="workspaces" id={grant.subjectId} /> 的成员
      </span>
    );
  return (
    <PrincipalCell
      principal={{
        type:
          grant.subjectType === "service_account" ? "service_account" : "user",
        id: grant.subjectId ?? "",
      }}
      workspaceId={grant.workspaceId}
    />
  );
}

export function AccessPage() {
  const viewer = useUser();
  const { workspace, manager } = useWorkspace();
  const { confirm } = useFeedback();
  const [resourceID, setResourceID] = useQueryParam("resourceId");
  const table = useTableState();
  const [resourceType, setResourceType] = useState("all");
  const [revoked, setRevoked] = useState("false");
  const [creating, setCreating] = useState(false);
  const data = usePage<Grant>(
    async (query, signal) =>
      result(
        await sdk.listWorkspaceGrants({
          path: { workspaceId: workspace.id },
          query,
          signal,
        }),
      ),
    {
      page: table.page,
      pageSize: table.pageSize,
      resourceId: resourceID,
      resourceType: resourceType === "all" ? undefined : resourceType,
      revoked: revoked === "all" ? undefined : revoked === "true",
      sortBy: "expiresAt",
      sortOrder: "desc",
    },
    workspace.id + ":grants",
  );
  const columns: Column<Grant>[] = [
    {
      header: "资源",
      cell: (g) => (
        <NameCell
          title={
            <Name
              kind={g.resourceType === "service" ? "servers" : "routes"}
              id={g.resourceId}
              workspaceId={g.workspaceId}
            />
          }
          subtitle={g.resourceType === "service" ? "服务" : "出口路由"}
        />
      ),
    },
    { header: "授权对象", cell: (g) => <GrantSubject grant={g} /> },
    {
      header: "有效期",
      cell: (g) => `${time(g.startsAt)} – ${time(g.expiresAt)}`,
    },
    { header: "状态", cell: (g) => <StatusBadge {...grantState(g)} /> },
    {
      header: "",
      className: "w-10",
      cell: (g) =>
        !g.revoked &&
        (manager || g.createdBy === viewer.id) && (
          <RowMenu>
            <DropdownMenuItem
              variant="destructive"
              onClick={() =>
                confirm({
                  title: "撤销访问授权",
                  description: "只移除这一条授权；其他有效授权仍然生效。",
                  action: "撤销",
                  destructive: true,
                  done: data.reload,
                  perform: async () =>
                    result(
                      await sdk.updateWorkspaceGrant({
                        path: { workspaceId: workspace.id, grantId: g.id },
                        body: { revoked: true },
                        signal: requestSignal(),
                      }),
                    ),
                })
              }
            >
              撤销
            </DropdownMenuItem>
          </RowMenu>
        ),
    },
  ];
  return (
    <>
      <PageHeader
        title="权限管理"
        description="访问授权决定谁可以使用哪项服务或出口路由。"
        actions={
          <Button onClick={() => setCreating(true)}>
            <PlusIcon />
            添加授权
          </Button>
        }
      />
      <TableToolbar
        search={resourceID}
        onSearch={(v) => {
          setResourceID(v);
          table.setPage(1);
        }}
        placeholder="资源 ID（精确匹配）"
      >
        <OptionSelect
          label="资源类型"
          value={resourceType}
          onChange={(v) => {
            setResourceType(v);
            table.setPage(1);
          }}
          options={[
            { value: "all", label: "全部资源" },
            { value: "service", label: "服务" },
            { value: "egress", label: "出口路由" },
          ]}
        />
        <OptionSelect
          label="撤销筛选"
          value={revoked}
          onChange={(v) => {
            setRevoked(v);
            table.setPage(1);
          }}
          options={[
            { value: "false", label: "未撤销" },
            { value: "true", label: "已撤销" },
            { value: "all", label: "全部" },
          ]}
        />
      </TableToolbar>
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(g) => g.id}
        loading={data.loading}
        error={data.error}
        emptyTitle="没有访问授权"
        emptyDescription="为服务或出口路由添加授权后，对应的人或服务账号才能使用。"
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
      {creating && (
        <GrantDialog
          workspaceId={workspace.id}
          manager={manager}
          onClose={() => setCreating(false)}
          onDone={data.reload}
        />
      )}
    </>
  );
}
