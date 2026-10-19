import { useState } from "react";
import { ArrowUpRightIcon, MoreHorizontalIcon } from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type {
  Agent,
  EgressView,
  IngressView,
  ServerView,
} from "@/api/generated";
import { requestSignal, result } from "@/api/session";
import { Button } from "@/components/ui/button";
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
import {
  Name,
  NameCell,
  PrincipalCell,
  RoleBadges,
  StatusBadge,
} from "@/components/cells";
import { useFeedback } from "@/components/feedback";
import { AgentSheet } from "@/components/dialogs/agent-dialogs";
import { GrantDialog } from "@/components/dialogs/route-grant-dialogs";
import { rememberNames, useEntity } from "@/lib/names";
import { usePage, useTableState } from "@/lib/use-page";
import { enabledRoles, isSelf, serviceURL, short } from "@/lib/format";
import { navigate } from "@/lib/router";
import { paths } from "@/lib/routes";
import { useSession, useUser, type WorkspaceScope } from "@/lib/session";

export type Scope =
  | { admin: true }
  | { admin: false; workspaceId: string; can: WorkspaceScope["can"] };
// allowed reports a workspace permission; an administrator's page holds all.
const allowed = (scope: Scope, p: Parameters<WorkspaceScope["can"]>[0]) =>
  scope.admin || scope.can(p);
const scopeKey = (scope: Scope) => (scope.admin ? "admin" : scope.workspaceId);

function useOnline(scope: Scope) {
  const data = usePage(
    async (query, signal) =>
      result(
        scope.admin
          ? await sdk.listAdminConnections({ query, signal })
          : await sdk.listWorkspaceConnections({
              path: { workspaceId: scope.workspaceId },
              query,
              signal,
            }),
      ),
    { pageSize: 100 },
    scopeKey(scope) + ":connections",
  );
  if (data.error) return null;
  return new Set(data.items.map((c) => c.agentId));
}

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

const statusOptions = [
  { value: "all", label: "全部状态" },
  { value: "active", label: "有效" },
  { value: "revoked", label: "已撤销" },
];

export function AgentsTable({ scope }: { scope: Scope }) {
  const viewer = useUser();
  const { confirm } = useFeedback();
  const table = useTableState();
  const [status, setStatus] = useState("all");
  const [selected, setSelected] = useState<Agent | null>(null);
  const data = usePage<Agent>(
    async (query, signal) => {
      const page = result(
        scope.admin
          ? await sdk.listAdminAgents({ query, signal })
          : await sdk.listWorkspaceAgents({
              path: { workspaceId: scope.workspaceId },
              query,
              signal,
            }),
      );
      rememberNames("agents", page.items);
      return page;
    },
    {
      page: table.page,
      pageSize: table.pageSize,
      q: table.query,
      status: status === "all" ? undefined : status,
      sortBy: "name",
    },
    scopeKey(scope),
  );
  const online = useOnline(scope);
  const columns: Column<Agent>[] = [
    {
      header: "Agent",
      cell: (a) => <NameCell title={a.name} subtitle={short(a.id)} />,
    },
    ...(scope.admin
      ? [
          {
            header: "工作空间",
            cell: (a: Agent) => <Name kind="workspaces" id={a.workspaceId} />,
          },
        ]
      : []),
    {
      header: "运行身份",
      cell: (a) => (
        <PrincipalCell principal={a.principal} workspaceId={a.workspaceId} />
      ),
    },
    { header: "声明能力", cell: (a) => <RoleBadges roles={enabledRoles(a)} /> },
    {
      header: "状态",
      cell: (a) => (
        <div className="flex flex-wrap gap-1">
          {a.status === "revoked" ? (
            <StatusBadge label="已撤销" tone="muted" />
          ) : online === null ? (
            <StatusBadge label="状态未知" tone="muted" />
          ) : online.has(a.id) ? (
            <StatusBadge label="在线" tone="active" />
          ) : (
            <StatusBadge label="离线" tone="muted" />
          )}
          {a.lastRejection && <StatusBadge label="配置被拒绝" tone="warning" />}
        </div>
      ),
    },
    {
      header: "生效版本",
      cell: (a) => a.effective.revision,
      className: "w-20",
    },
    {
      header: "",
      className: "w-10",
      cell: (a) => (
        <RowMenu>
          <DropdownMenuItem onClick={() => setSelected(a)}>
            查看详情
          </DropdownMenuItem>
          {a.status !== "revoked" &&
            (allowed(scope, "agents.manage") ||
              isSelf(a.principal, viewer.id)) && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() =>
                    confirm({
                      title: `撤销 ${a.name}`,
                      description:
                        "设备的凭据和当前连接立即失效。撤销不能恢复，需要重新注册。",
                      action: "撤销",
                      destructive: true,
                      done: data.reload,
                      perform: async () =>
                        result(
                          scope.admin
                            ? await sdk.updateAdminAgent({
                                path: { id: a.id },
                                body: { status: "revoked" },
                                signal: requestSignal(),
                              })
                            : await sdk.updateWorkspaceAgent({
                                path: {
                                  workspaceId: a.workspaceId,
                                  agentId: a.id,
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
              </>
            )}
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
          options={statusOptions}
        />
      </TableToolbar>
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(a) => a.id}
        loading={data.loading}
        error={data.error}
        emptyTitle="还没有设备"
        emptyDescription="设备通过 agentd login 或服务账号的注册密钥接入。"
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
      {selected && (
        <AgentSheet agent={selected} onClose={() => setSelected(null)} />
      )}
    </>
  );
}

const enabledOptions = [
  { value: "all", label: "全部" },
  { value: "true", label: "已启用" },
  { value: "false", label: "未启用" },
];

export function RoleViewsTable({
  scope,
  role,
}: {
  scope: Scope;
  role: "ingress" | "egress";
}) {
  const table = useTableState();
  const [enabled, setEnabled] = useState("all");
  const data = usePage<IngressView | EgressView>(
    async (query, signal) => {
      if (role === "ingress")
        return result(
          scope.admin
            ? await sdk.listAdminIngresses({ query, signal })
            : await sdk.listWorkspaceIngresses({
                path: { workspaceId: scope.workspaceId },
                query,
                signal,
              }),
        );
      return result(
        scope.admin
          ? await sdk.listAdminEgresses({ query, signal })
          : await sdk.listWorkspaceEgresses({
              path: { workspaceId: scope.workspaceId },
              query,
              signal,
            }),
      );
    },
    {
      page: table.page,
      pageSize: table.pageSize,
      q: table.query,
      enabled: enabled === "all" ? undefined : enabled === "true",
      sortBy: "name",
    },
    scopeKey(scope) + role,
  );
  const columns: Column<IngressView | EgressView>[] = [
    {
      header: "设备",
      cell: (r) => (
        <NameCell title={r.agent.name} subtitle={short(r.agentId)} />
      ),
    },
    ...(scope.admin
      ? [
          {
            header: "工作空间",
            cell: (r: IngressView) => (
              <Name kind="workspaces" id={r.workspaceId} />
            ),
          },
        ]
      : []),
    {
      header: role === "ingress" ? "本机访问" : "出口能力",
      cell: (r) => (
        <StatusBadge
          label={r.enabled ? "已生效" : "未生效"}
          tone={r.enabled ? "active" : "muted"}
        />
      ),
    },
    {
      header: "设备状态",
      cell: (r) => (
        <StatusBadge
          label={r.status === "revoked" ? "已撤销" : "有效"}
          tone={r.status === "revoked" ? "muted" : "active"}
        />
      ),
    },
    { header: "生效版本", cell: (r) => r.effectiveRevision, className: "w-20" },
  ];
  return (
    <>
      <TableToolbar search={table.search} onSearch={table.setSearch}>
        <OptionSelect
          label="启用筛选"
          value={enabled}
          onChange={(v) => {
            setEnabled(v);
            table.setPage(1);
          }}
          options={enabledOptions}
        />
      </TableToolbar>
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(r) => r.id}
        loading={data.loading}
        error={data.error}
        emptyTitle={
          role === "ingress"
            ? "没有声明 Ingress 的设备"
            : "没有声明 Egress 的设备"
        }
        emptyDescription={
          role === "ingress"
            ? "设备在配置中启用 ingress 后会出现在这里。"
            : "设备在配置中启用 egress 后会出现在这里。"
        }
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
    </>
  );
}

function ServerActions({
  server,
  scope,
  reload,
}: {
  server: ServerView;
  scope: Scope;
  reload: () => void;
}) {
  const viewer = useUser();
  const { confirm } = useFeedback();
  const [granting, setGranting] = useState(false);
  const agent = useEntity("agents", server.agentId, server.workspaceId);
  const manage =
    allowed(scope, "services.manage") ||
    (allowed(scope, "services.manage_own") && isSelf(agent?.principal, viewer.id));
  if (!manage) return null;
  return (
    <>
      <RowMenu>
        {!scope.admin && (
          <>
            <DropdownMenuItem onClick={() => setGranting(true)}>
              添加授权
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() =>
                navigate(
                  paths.workspace(server.workspaceId, "access") +
                    "?resourceId=" +
                    server.serviceId,
                )
              }
            >
              查看授权
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        )}
        <DropdownMenuItem
          variant={server.enabled ? "destructive" : "default"}
          onClick={() =>
            confirm({
              title: `${server.enabled ? "停用" : "启用"} ${server.name}`,
              description: server.enabled
                ? "停用后服务地址不再可访问，授权保留。"
                : "启用后，持有有效授权的访问者可以访问。",
              action: server.enabled ? "停用" : "启用",
              destructive: server.enabled,
              done: reload,
              perform: async () =>
                result(
                  scope.admin
                    ? await sdk.updateAdminServer({
                        path: { id: server.id },
                        body: { enabled: !server.enabled },
                        signal: requestSignal(),
                      })
                    : await sdk.updateWorkspaceServer({
                        path: {
                          workspaceId: server.workspaceId,
                          serviceId: server.serviceId,
                        },
                        body: { enabled: !server.enabled },
                        signal: requestSignal(),
                      }),
                ),
            })
          }
        >
          {server.enabled ? "停用" : "启用"}
        </DropdownMenuItem>
      </RowMenu>
      {granting && !scope.admin && (
        <GrantDialog
          workspaceId={server.workspaceId}
          resource={{
            type: "service",
            id: server.serviceId,
            label: server.name,
          }}
          onClose={() => setGranting(false)}
          onDone={reload}
        />
      )}
    </>
  );
}

export function ServersTable({ scope }: { scope: Scope }) {
  const { info } = useSession();
  const table = useTableState();
  const [enabled, setEnabled] = useState("all");
  const data = usePage<ServerView>(
    async (query, signal) => {
      const page = result(
        scope.admin
          ? await sdk.listAdminServers({ query, signal })
          : await sdk.listWorkspaceServers({
              path: { workspaceId: scope.workspaceId },
              query,
              signal,
            }),
      );
      rememberNames("servers", page.items, (s) => s.serviceId);
      return page;
    },
    {
      page: table.page,
      pageSize: table.pageSize,
      q: table.query,
      enabled: enabled === "all" ? undefined : enabled === "true",
      sortBy: "name",
    },
    scopeKey(scope) + ":servers",
  );
  const columns: Column<ServerView>[] = [
    {
      header: "服务",
      cell: (s) => <NameCell title={s.name} subtitle={short(s.serviceId)} />,
    },
    ...(scope.admin
      ? [
          {
            header: "工作空间",
            cell: (s: ServerView) => (
              <Name kind="workspaces" id={s.workspaceId} />
            ),
          },
        ]
      : []),
    {
      header: "访问地址",
      cell: (s) => {
        const url = serviceURL(info?.serverURL ?? location.origin, s.hostname);
        return url ? (
          <a
            className="inline-flex items-center gap-1 underline-offset-4 hover:underline"
            href={url}
            target="_blank"
            rel="noopener noreferrer"
          >
            {new URL(url).host}
            <ArrowUpRightIcon className="size-3.5" />
          </a>
        ) : (
          s.hostname
        );
      },
    },
    { header: "提供方", cell: (s) => s.agent.name },
    {
      header: "状态",
      cell: (s) => (
        <StatusBadge
          label={s.enabled ? "已发布" : "已停用"}
          tone={s.enabled ? "active" : "muted"}
        />
      ),
    },
    {
      header: "",
      className: "w-10",
      cell: (s) => (
        <ServerActions server={s} scope={scope} reload={data.reload} />
      ),
    },
  ];
  return (
    <>
      <TableToolbar search={table.search} onSearch={table.setSearch}>
        <OptionSelect
          label="发布状态"
          value={enabled}
          onChange={(v) => {
            setEnabled(v);
            table.setPage(1);
          }}
          options={[
            { value: "all", label: "全部" },
            { value: "true", label: "已发布" },
            { value: "false", label: "已停用" },
          ]}
        />
      </TableToolbar>
      <DataTable
        columns={columns}
        rows={data.items}
        rowKey={(s) => s.id}
        loading={data.loading}
        error={data.error}
        emptyTitle="还没有发布的服务"
        emptyDescription="设备在配置中声明 serve 服务后，服务会出现在这里。"
      />
      <TablePagination
        pagination={data.page?.pagination}
        onPage={table.setPage}
        onPageSize={table.setPageSize}
      />
    </>
  );
}
