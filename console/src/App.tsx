import type { ConsoleView } from "@/lib/view";
import { emptyView } from "@/lib/view";
import { loadView, type Resource, type ResourceQuery } from "@/lib/load-view";
import { PaginationBar } from "@/components/pagination";
import { ResourceSelect } from "@/components/resource-select";
import type {
  IngressView,
  EgressView,
  ServerView,
  RouteView,
  EnrollmentKey,
  Membership,
  Workspace,
  FlowStatus,
} from "@/api/generated";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  Activity,
  ArrowRight,
  ArrowRightLeft,
  ArrowUpRight,
  Globe2,
  KeyRound,
  LayoutDashboard,
  LoaderCircle,
  LogOut,
  Plus,
  RefreshCw,
  Search,
  Server,
  ShieldCheck,
} from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type { Agent, Audit, Grant, Info, Session, User } from "@/api/generated";
import {
  APIFailure,
  message,
  oidcDestination,
  requestSignal,
  result,
  serviceReturnTo,
  setCSRF,
} from "@/api/session";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { Label } from "@/components/ui/label";
import {
  AgentDialog,
  ConfirmDialog,
  ManagementDialog,
  SecretDialog,
  type ConfirmAction,
  type FormRequest,
  type Secret,
} from "@/components/management-dialog";
import {
  Cell,
  ChoiceSelect,
  DataTable,
  Empty,
  Notice,
  Panel,
  Status,
  Tags,
} from "@/components/controls";
import {
  agentRoles,
  canManage,
  canManageResource,
  grantHealth,
  health,
  name,
  resourceName,
  roleNames,
  serviceURL,
  short,
  subjectName,
  time,
} from "@/lib/model";

const pages = {
  overview: {
    title: "总览",
    eyebrow: "WORKSPACE OVERVIEW",
    description: "了解设备、服务与访问连接的最新状态。",
    icon: LayoutDashboard,
  },
  agents: {
    title: "Agents",
    eyebrow: "AGENT DIRECTORY",
    description: "核对提供能力，观察设备身份与实际连接。",
    icon: Server,
  },
  services: {
    title: "服务发布",
    eyebrow: "PUBLISHED SERVICES",
    description: "由设备声明服务源站，按有效授权开放访问。",
    icon: Globe2,
  },
  egress: {
    title: "路由规则",
    eyebrow: "ROUTING RULES",
    description: "查看服务发布与 TLS 出口两类路由，核对目标资源和启用状态。",
    icon: ArrowRightLeft,
  },
  access: {
    title: "权限与成员",
    eyebrow: "ACCESS MANAGEMENT",
    description: "管理访问授权、注册凭据与工作空间成员。",
    icon: ShieldCheck,
  },
  audit: {
    title: "审计日志",
    eyebrow: "ACTIVITY & AUDIT",
    description: "追踪资源变更、批准与撤销操作。",
    icon: Activity,
  },
} as const;
export type Page = keyof typeof pages;
type AccessTab = "grants" | "keys" | "members" | "workspaces" | "users";
const accessTabs: Record<AccessTab, string> = {
  grants: "访问授权",
  keys: "注册密钥",
  members: "成员",
  workspaces: "工作空间",
  users: "用户",
};
const actionLabels: Record<string, string> = {
  bootstrap: "初始化实例",
  "workspace.create": "创建工作空间",
  "workspace.personal.create": "创建个人默认工作空间",
  "user.create": "创建用户",
  "user.oidc.create": "单点登录创建用户",
  "user.oidc.admin.grant": "授予邮箱名单管理员权限",
  "user.oidc.admin.revoke": "撤销邮箱名单管理员权限",
  "user.disable": "停用用户",
  "membership.set": "更新成员角色",
  "membership.remove": "移除成员",
  "agent.enroll": "注册 Agent",
  "agent.approve": "批准 Agent（已移除的旧流程）",
  "agent.revoke": "撤销 Agent",
  "agent.purge": "清除已撤销的 Agent",
  "agent.desired": "更新期望配置（旧流程）",
  "agent.desired.apply": "自动应用期望配置",
  "agent.desired.reject": "自动拒绝期望配置",
  "agent.migrate.approval_removed": "迁移旧审批记录",
  "service.publish": "发布服务",
  "service.purge": "清除服务名称",
  "agent.tokens.renew": "续期设备凭据",
  "service.enabled": "更新服务发布状态",
  "grant.create": "添加访问授权",
  "grant.revoke": "撤销访问授权",
  "enrollment_key.create": "创建注册密钥",
  "enrollment_key.revoke": "撤销注册密钥",
  "egress_route.create": "创建出口路由",
  "egress_route.enabled": "更新出口路由状态",
  "user.token.rotate": "更新用户令牌",
};
function currentPage(): Page {
  const value = location.hash.slice(1);
  return value in pages ? (value as Page) : "overview";
}
function Brand() {
  return (
    <a href="#overview" className="brand">
      <span className="brand-mark">a</span>
      <span>
        agents<span className="brand-dot">.</span>
      </span>
    </a>
  );
}

export default function App() {
  const [info, setInfo] = useState<Info | null>(null),
    [user, setUser] = useState<User | null>(null),
    [loadedView, setLoadedView] = useState<{
      scope: string;
      data: ConsoleView;
    } | null>(null);
  const [boot, setBoot] = useState(true),
    [loading, setLoading] = useState(false),
    [error, setError] = useState(""),
    [toast, setToast] = useState("");
  const [page, setPage] = useState<Page>(currentPage),
    [tab, setTab] = useState<AccessTab>("grants"),
    [agentTab, setAgentTab] = useState<"agents" | "ingresses" | "egresses">(
      "agents",
    );
  const [workspace, setWorkspace] = useState(""),
    [query, setQuery] = useState(""),
    [filter, setFilter] = useState("all"),
    [tablePage, setTablePage] = useState(1),
    [pageSize, setPageSize] = useState(20),
    [sortBy, setSortBy] = useState(currentPage() === "audit" ? "at" : "id"),
    [sortOrder, setSortOrder] = useState<"asc" | "desc">(
      currentPage() === "audit" ? "desc" : "asc",
    );
  const [form, setForm] = useState<FormRequest | null>(null),
    [confirm, setConfirm] = useState<ConfirmAction | null>(null),
    [selectedAgent, setSelectedAgent] = useState<Agent | null>(null),
    [secret, setSecret] = useState<Secret | null>(null);
  const sequence = useRef(0),
    request = useRef<AbortController | null>(null),
    activeUser = useRef<User | null>(null);
  const resource: Resource =
    page === "overview"
      ? "flows"
      : page === "agents"
        ? agentTab
        : page === "services"
          ? "servers"
          : page === "egress"
            ? "routes"
            : page === "audit"
              ? "audit"
              : (
                  {
                    grants: "grants",
                    keys: "enrollmentKeys",
                    members: "memberships",
                    workspaces: "workspaces",
                    users: "users",
                  } as const
                )[tab];
  // The render guard must run synchronously: effects execute after a new tab
  // has rendered and cannot prevent reading a previous resource's row shape.
  const viewScope = JSON.stringify([
    user?.id,
    user?.admin,
    workspace,
    page,
    resource,
    query,
    filter,
    tablePage,
    pageSize,
    sortBy,
    sortOrder,
    info?.serverURL,
  ]);
  const state = loadedView?.scope === viewScope ? loadedView.data : null;
  const sortFields: Record<Resource, string[]> = {
    users: ["id", "name", "createdAt"],
    workspaces: ["id", "name", "createdAt"],
    agents: ["id", "name", "createdAt", "status"],
    ingresses: ["id", "name", "status"],
    egresses: ["id", "name", "status"],
    servers: ["id", "name", "hostname"],
    routes: ["id", "name", "kind", "priority"],
    grants: ["id", "startsAt", "expiresAt"],
    enrollmentKeys: ["id", "expiresAt"],
    memberships: ["id", "workspaceId", "userId", "role"],
    audit: ["at", "id", "action"],
    flows: ["id", "startedAt", "state", "purpose"],
  };
  const searchField =
    resource === "memberships" || resource === "enrollmentKeys"
      ? "userId"
      : resource === "grants" || resource === "flows"
        ? "resourceId"
        : resource === "audit"
          ? "action"
          : "q";
  const filterField =
    resource === "users"
      ? "disabled"
      : ["agents", "ingresses", "egresses"].includes(resource)
        ? "status"
        : resource === "servers"
          ? "enabled"
          : resource === "routes"
            ? "kind"
            : ["grants", "enrollmentKeys"].includes(resource)
              ? "revoked"
              : resource === "memberships"
                ? "role"
                : resource === "flows"
                  ? "purpose"
                  : "";
  const filterOptions =
    filterField === "status"
      ? ["active", "revoked"]
      : filterField === "kind"
        ? ["serve", "egress"]
        : filterField === "role"
          ? ["owner", "maintainer", "developer", "guest"]
          : filterField === "purpose"
            ? ["serve", "egress"]
            : filterField
              ? ["true", "false"]
              : [];
  const signOut = useCallback(() => {
    sequence.current++;
    request.current?.abort();
    activeUser.current = null;
    setCSRF("");
    setUser(null);
    setLoadedView(null);
    setForm(null);
    setConfirm(null);
    setSelectedAgent(null);
    setSecret(null);
    setWorkspace("");
  }, []);
  const acceptSession = useCallback((session: Session) => {
    activeUser.current = session.user;
    setUser(session.user);
    setCSRF(session.csrfToken);
    const target = serviceReturnTo();
    if (target) location.replace(target);
  }, []);
  useEffect(() => {
    let stopped = false;
    void Promise.allSettled([
      sdk.getInstance({ signal: requestSignal() }),
      sdk.getCurrentSession({ signal: requestSignal() }),
    ]).then(([i, s]) => {
      if (stopped) return;
      if (i.status === "fulfilled") {
        try {
          setInfo(result(i.value));
        } catch (e) {
          setError(message(e));
        }
      }
      if (s.status === "fulfilled") {
        try {
          acceptSession(result(s.value));
        } catch {}
      }
      setBoot(false);
    });
    return () => {
      stopped = true;
    };
  }, [acceptSession]);
  useEffect(() => {
    const change = () => {
      setPage(currentPage());
      setQuery("");
      setFilter("all");
      setTablePage(1);
      setSortBy(currentPage() === "audit" ? "at" : "id");
      setSortOrder(currentPage() === "audit" ? "desc" : "asc");
    };
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  const refresh = useCallback(async () => {
    let viewer = activeUser.current;
    if (!viewer) return;
    const id = ++sequence.current;
    request.current?.abort();
    const abort = new AbortController();
    request.current = abort;
    const signal = AbortSignal.any([abort.signal, AbortSignal.timeout(15000)]);
    setLoading(true);
    setError("");
    try {
      const current = result(await sdk.getCurrentUser({ signal }));
      if (id !== sequence.current) return;
      if (viewer.id !== current.id || viewer.admin !== current.admin)
        setLoadedView(null);
      viewer = current;
      activeUser.current = current;
      setUser(current);
      if (!viewer.admin && !workspace && resource !== "workspaces") {
        const personalName = "personal-" + viewer.id;
        const personal = result(
          await sdk.listWorkspaces({
            query: { q: personalName, pageSize: 1, sortBy: "name" },
            signal,
          }),
        );
        if (id !== sequence.current) return;
        const preferred = personal.items.find((w) => w.name === personalName);
        if (preferred) {
          setWorkspace(preferred.id);
          return;
        }
        const first = result(
          await sdk.listWorkspaces({
            query: { pageSize: 1, sortBy: "name" },
            signal,
          }),
        );
        if (id !== sequence.current) return;
        if (first.items.length) {
          setWorkspace(first.items[0].id);
          return;
        }
        setLoadedView({
          scope: viewScope,
          data: emptyView(viewer, info?.serverURL || location.origin),
        });
        return;
      }
      const params: ResourceQuery = {
        page: tablePage,
        pageSize,
        sortBy: sortFields[resource].includes(sortBy)
          ? sortBy
          : sortFields[resource][0],
        sortOrder,
      };
      if (query.trim()) params[searchField] = query.trim();
      if (filter !== "all" && filterField)
        params[filterField] =
          filter === "true" ? true : filter === "false" ? false : filter;
      const next = await loadView(
        viewer,
        info?.serverURL || location.origin,
        workspace,
        resource,
        params,
        page === "overview",
        signal,
      );
      if (id !== sequence.current) return;
      setLoadedView({ scope: viewScope, data: next });
      activeUser.current = next.user;
      setUser(next.user);
    } catch (e) {
      if (id !== sequence.current || abort.signal.aborted) return;
      if (e instanceof APIFailure && e.status === 401) {
        signOut();
        setToast("登录已过期，请重新登录。");
      } else setError(message(e));
    } finally {
      if (id === sequence.current) setLoading(false);
    }
  }, [
    workspace,
    resource,
    query,
    filter,
    tablePage,
    pageSize,
    sortBy,
    sortOrder,
    info?.serverURL,
    page,
    viewScope,
    signOut,
  ]);
  useEffect(() => {
    setLoadedView(null);
    void refresh();
    return () => request.current?.abort();
  }, [refresh, user?.id]);
  useEffect(() => {
    if (!user || form || confirm || selectedAgent || secret) return;
    const timer = setInterval(() => {
      if (!document.hidden) void refresh();
    }, 10000);
    return () => clearInterval(timer);
  }, [user?.id, refresh, form, confirm, selectedAgent, secret]);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(""), 5000);
    return () => clearTimeout(timer);
  }, [toast]);
  function scopeTo(id: string) {
    setWorkspace(id);
    setTablePage(1);
    setQuery("");
    setFilter("all");
    setSelectedAgent(null);
    setForm(null);
  }
  function complete(value?: Secret) {
    setForm(null);
    setConfirm(null);
    setSelectedAgent(null);
    if (value) setSecret(value);
    else setToast("操作已完成");
    void refresh();
  }
  if (boot)
    return (
      <div className="boot">
        <span className="brand-mark">a</span>
        <LoaderCircle className="animate-spin" />
        <p>正在连接控制台…</p>
      </div>
    );
  if (!user)
    return (
      <>
        <Login info={info} onSession={acceptSession} error={error} />
        {toast && <div className="toast">{toast}</div>}
      </>
    );
  const viewer = user;
  const ownRole = state?.memberships.find(
    (m) => m.workspaceId === workspace && m.userId === viewer.id,
  )?.role;
  const manager =
      viewer.admin || ownRole === "owner" || ownRole === "maintainer",
    developer = manager || ownRole === "developer";
  function ask(
    title: string,
    description: string,
    perform: () => Promise<Secret | void>,
    danger = true,
  ) {
    setConfirm({ title, description, label: "确认", danger, perform });
  }
  async function openAgent(id: string, wid: string) {
    try {
      const a = result(
        viewer.admin
          ? await sdk.getAdminAgent({ path: { id }, signal: requestSignal() })
          : await sdk.getWorkspaceAgent({
              path: { workspaceId: wid, agentId: id },
              signal: requestSignal(),
            }),
      );
      setSelectedAgent(a);
    } catch (e) {
      setToast(message(e));
    }
  }
  function rotateToken(target: User) {
    ask(
      `更新 ${target.name} 的令牌`,
      "旧用户 Bearer 令牌立即失效；新令牌仅展示一次。",
      async () => {
        const r = result(
          viewer.admin
            ? await sdk.createAdminUserToken({
                path: { id: target.id },
                signal: requestSignal(),
              })
            : await sdk.createCurrentUserToken({ signal: requestSignal() }),
        );
        return {
          title: "新用户令牌已签发",
          subtitle: target.name,
          token: r.token,
          filename: "agents-user-token.txt",
        };
      },
      false,
    );
  }
  function grantForm(server: ServerView | RouteView) {
    const service = "serviceId" in server || server.kind === "serve";
    setForm({
      kind: "grant",
      resource: {
        id:
          "serviceId" in server
            ? server.serviceId
            : service
              ? server.targetId
              : server.id,
        workspaceId: server.workspaceId,
        type: service ? "service" : "egress",
      },
    });
  }
  function showGrants(id: string) {
    setTab("grants");
    setQuery(id);
    setTablePage(1);
    setFilter("all");
    setPage("access");
    history.replaceState(null, "", "#access");
  }
  function renderTable() {
    if (!state) return null;
    const items = state.primaryItems;
    let headers: string[] = [],
      rows: { key: string; cells: ReactNode[] }[] = [];
    if (resource === "agents") {
      headers = ["Agent", "工作空间", "角色", "状态", "生效 Revision", "操作"];
      rows = (items as Agent[]).map((a) => ({
        key: a.id,
        cells: [
          <Cell title={a.name} subtitle={short(a.id)} />,
          name(state, "workspaces", a.workspaceId),
          <Tags items={agentRoles(a)} />,
          <div className="flex gap-1">
            <Status {...health(state, a)} />
            {a.lastRejection && <Status label="配置被拒绝" tone="warn" />}
          </div>,
          String(a.effective.revision),
          <div className="row-actions">
            <Button
              size="sm"
              variant="outline"
              onClick={() => void openAgent(a.id, a.workspaceId)}
            >
              查看
            </Button>
            {a.status !== "revoked" &&
              (canManage(state, viewer, a.workspaceId) ||
                a.userId === viewer.id) && (
                <Button
                  size="sm"
                  variant="destructive"
                  onClick={() =>
                    ask(
                      `撤销 ${a.name}`,
                      "设备凭据和当前连接将失效。",
                      async () => {
                        result(
                          viewer.admin
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
                        );
                      },
                    )
                  }
                >
                  撤销
                </Button>
              )}
          </div>,
        ],
      }));
    }
    if (resource === "ingresses" || resource === "egresses") {
      headers = ["设备", "角色", "状态", "生效 Revision"];
      rows = (items as (IngressView | EgressView)[]).map((r) => ({
        key: r.id,
        cells: [
          <Cell title={r.agent.name} subtitle={short(r.agentId)} />,
          resource === "ingresses" ? "本机访问端" : "出口提供方",
          <Status
            label={r.enabled ? r.status : "未启用"}
            tone={r.enabled ? "good" : "muted"}
          />,
          String(r.effectiveRevision),
        ],
      }));
    }
    if (resource === "servers") {
      headers = ["服务", "工作空间", "提供方", "访问地址", "发布状态", "操作"];
      rows = (items as ServerView[]).map((s) => {
        const url = serviceURL(state, s.hostname);
        return {
          key: s.id,
          cells: [
            <Cell title={s.name} subtitle={short(s.serviceId)} />,
            name(state, "workspaces", s.workspaceId),
            s.agent.name,
            url ? (
              <a
                className="resource-link"
                href={url}
                target="_blank"
                rel="noopener noreferrer"
              >
                {new URL(url).host}
                <ArrowUpRight size={12} />
              </a>
            ) : (
              s.hostname
            ),
            <Status
              label={s.enabled ? "已发布" : "已停用"}
              tone={s.enabled ? "good" : "muted"}
            />,
            <div className="row-actions">
              <Button
                size="sm"
                variant="ghost"
                onClick={() => showGrants(s.serviceId)}
              >
                授权记录
              </Button>
              {canManageResource(state, viewer, "service", s.serviceId) && (
                <>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => grantForm(s)}
                  >
                    添加授权
                  </Button>
                  <Button
                    size="sm"
                    variant={s.enabled ? "destructive" : "default"}
                    onClick={() =>
                      ask(
                        `${s.enabled ? "停用" : "启用"} ${s.name}`,
                        "修改发布状态；访问仍需有效授权。",
                        async () => {
                          result(
                            viewer.admin
                              ? await sdk.updateAdminServer({
                                  path: { id: s.id },
                                  body: { enabled: !s.enabled },
                                  signal: requestSignal(),
                                })
                              : await sdk.updateWorkspaceServer({
                                  path: {
                                    workspaceId: s.workspaceId,
                                    serviceId: s.serviceId,
                                  },
                                  body: { enabled: !s.enabled },
                                  signal: requestSignal(),
                                }),
                          );
                        },
                      )
                    }
                  >
                    {s.enabled ? "停用" : "启用"}
                  </Button>
                </>
              )}
            </div>,
          ],
        };
      });
    }
    if (resource === "routes") {
      headers = ["路由", "类型", "目标", "匹配规则", "状态", "操作"];
      rows = (items as RouteView[]).map((r) => ({
        key: r.id,
        cells: [
          <Cell
            title={r.name}
            subtitle={name(state, "workspaces", r.workspaceId)}
          />,
          r.kind === "serve" ? "服务发布" : "TLS 出口",
          <Cell
            title={
              r.targetType === "server"
                  ? r.name
                : name(state, "agents", r.agentId)
            }
            subtitle={short(r.targetId)}
          />,
          `${r.rule.domain.exact || r.rule.domain.suffix} · ${r.rule.ports.join(", ")} · ${r.rule.protocol}`,
          <Status
            label={r.enabled ? "启用" : "停用"}
            tone={r.enabled ? "good" : "muted"}
          />,
          <div className="row-actions">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => showGrants(r.kind === "serve" ? r.targetId : r.id)}
            >
              授权记录
            </Button>
            {(manager ||
              (r.kind === "serve" &&
                canManageResource(state, viewer, "service", r.targetId))) && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => grantForm(r)}
                >
                  添加授权
                </Button>
                <Button
                  size="sm"
                  variant={r.enabled ? "destructive" : "default"}
                  onClick={() =>
                    ask(
                      `${r.enabled ? "停用" : "启用"} ${r.name}`,
                      "更新这条路由的启用状态。",
                      async () => {
                        result(
                          viewer.admin
                            ? await sdk.updateAdminRoute({
                                path: { id: r.id },
                                body: { enabled: !r.enabled },
                                signal: requestSignal(),
                              })
                            : await sdk.updateWorkspaceRoute({
                                path: {
                                  workspaceId: r.workspaceId,
                                  routeId: r.id,
                                },
                                body: { enabled: !r.enabled },
                                signal: requestSignal(),
                              }),
                        );
                      },
                    )
                  }
                >
                  {r.enabled ? "停用" : "启用"}
                </Button>
              </>
            )}
          </div>,
        ],
      }));
    }
    if (resource === "grants") {
      headers = ["资源", "授权对象", "开始", "截止", "状态", "操作"];
      rows = (items as Grant[]).map((g) => ({
        key: g.id,
        cells: [
          <Cell
            title={resourceName(state, g.resourceId)}
            subtitle={`${g.resourceType} · ${short(g.resourceId)}`}
          />,
          subjectName(state, g),
          time(g.startsAt),
          time(g.expiresAt),
          <Status {...grantHealth(g)} />,
          !g.revoked &&
          canManageResource(state, viewer, g.resourceType, g.resourceId) ? (
            <Button
              size="sm"
              variant="destructive"
              onClick={() =>
                ask(
                  "撤销访问授权",
                  "仅移除这一授权来源；其他有效授权仍然生效。",
                  async () => {
                    result(
                      viewer.admin
                        ? await sdk.updateAdminGrant({
                            path: { id: g.id },
                            body: { revoked: true },
                            signal: requestSignal(),
                          })
                        : await sdk.updateWorkspaceGrant({
                            path: { workspaceId: g.workspaceId, grantId: g.id },
                            body: { revoked: true },
                            signal: requestSignal(),
                          }),
                    );
                  },
                )
              }
            >
              撤销
            </Button>
          ) : (
            "—"
          ),
        ],
      }));
    }
    if (resource === "enrollmentKeys") {
      headers = [
        "密钥 / 工作空间",
        "用户",
        "角色",
        "使用次数",
        "截止",
        "状态",
        "操作",
      ];
      rows = (items as EnrollmentKey[]).map((k) => ({
        key: k.id,
        cells: [
          <Cell
            title={short(k.id)}
            subtitle={name(state, "workspaces", k.workspaceId)}
          />,
          name(state, "users", k.userId),
          <Tags items={k.roles.map((x) => roleNames[x])} />,
          `${k.uses} / ${k.maxUses}`,
          time(k.expiresAt),
          <Status
            label={
              k.revoked
                ? "撤销"
                : new Date(k.expiresAt) <= new Date()
                  ? "到期"
                  : k.uses >= k.maxUses
                    ? "用尽"
                    : "可用"
            }
            tone={k.revoked ? "muted" : "good"}
          />,
          !k.revoked && (manager || k.userId === viewer.id) ? (
            <Button
              size="sm"
              variant="destructive"
              onClick={() =>
                ask("撤销注册密钥", "此密钥不能再注册新设备。", async () => {
                  result(
                    viewer.admin
                      ? await sdk.updateAdminEnrollmentKey({
                          path: { id: k.id },
                          body: { revoked: true },
                          signal: requestSignal(),
                        })
                      : await sdk.updateWorkspaceEnrollmentKey({
                          path: { workspaceId: k.workspaceId, keyId: k.id },
                          body: { revoked: true },
                          signal: requestSignal(),
                        }),
                  );
                })
              }
            >
              撤销
            </Button>
          ) : (
            "—"
          ),
        ],
      }));
    }
    if (resource === "memberships") {
      headers = ["用户", "工作空间", "角色", "操作"];
      rows = (items as Membership[]).map((m) => ({
        key: m.workspaceId + ":" + m.userId,
        cells: [
          <Cell
            title={name(state, "users", m.userId)}
            subtitle={short(m.userId)}
          />,
          name(state, "workspaces", m.workspaceId),
          roleNames[m.role],
          manager ? (
            <Button
              size="sm"
              variant="destructive"
              onClick={() =>
                ask(
                  "移除成员",
                  "该用户将失去此工作空间成员权限，其设备可能被撤销。",
                  async () => {
                    result(
                      viewer.admin
                        ? await sdk.deleteAdminMembership({
                            path: {
                              workspaceId: m.workspaceId,
                              userId: m.userId,
                            },
                            signal: requestSignal(),
                          })
                        : await sdk.deleteMembership({
                            path: {
                              workspaceId: m.workspaceId,
                              userId: m.userId,
                            },
                            signal: requestSignal(),
                          }),
                    );
                  },
                )
              }
            >
              移除
            </Button>
          ) : (
            "—"
          ),
        ],
      }));
    }
    if (resource === "workspaces") {
      headers = ["工作空间", "ID", "创建时间", "操作"];
      rows = (items as Workspace[]).map((w) => ({
        key: w.id,
        cells: [
          w.name,
          <span className="mono">{w.id}</span>,
          time(w.createdAt),
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              scopeTo(w.id);
              location.hash = "overview";
            }}
          >
            进入工作空间
          </Button>,
        ],
      }));
    }
    if (resource === "users") {
      headers = ["用户", "实例角色", "状态", "创建时间", "操作"];
      rows = (items as User[]).map((u) => ({
        key: u.id,
        cells: [
          <Cell title={u.name} subtitle={u.id} />,
          u.admin ? "实例管理员" : "用户",
          <Status
            label={u.disabled ? "停用" : "有效"}
            tone={u.disabled ? "muted" : "good"}
          />,
          time(u.createdAt),
          <div className="row-actions">
            {!u.disabled && (viewer.admin || u.id === viewer.id) && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => rotateToken(u)}
              >
                更新令牌
              </Button>
            )}
            {!u.disabled && viewer.admin && u.id !== viewer.id && (
              <Button
                size="sm"
                variant="destructive"
                onClick={() =>
                  ask(
                    `停用 ${u.name}`,
                    "该用户将无法登录或使用授权与设备。",
                    async () => {
                      result(
                        await sdk.updateAdminUser({
                          path: { id: u.id },
                          body: { disabled: true },
                          signal: requestSignal(),
                        }),
                      );
                    },
                  )
                }
              >
                停用
              </Button>
            )}
          </div>,
        ],
      }));
    }
    if (resource === "audit") {
      headers = ["时间", "操作者", "动作", "资源", "详情"];
      rows = (items as Audit[]).map((a) => ({
        key: a.id,
        cells: [
          time(a.at, true),
          name(state, "users", a.actorId),
          <Cell
            title={actionLabels[a.action] || a.action}
            subtitle={a.action}
          />,
          a.resourceId,
          a.detail || "—",
        ],
      }));
    }
    if (resource === "flows") {
      headers = ["用途 / ID", "资源", "提供方", "状态", "租约截止"];
      rows = (items as FlowStatus[]).map((f) => ({
        key: f.id,
        cells: [
          <Cell title={roleNames[f.purpose]} subtitle={short(f.id)} />,
          resourceName(state, f.resourceId),
          name(state, "agents", f.agentId),
          <Status
            label={
              f.state === "pending"
                ? "正在建立"
                : f.state === "active"
                  ? "传输中"
                  : f.state
            }
            tone="good"
          />,
          time(f.expiresAt),
        ],
      }));
    }
    return (
      <Panel
        title={
          page === "overview"
            ? "活动流"
            : page === "access"
              ? accessTabs[tab]
              : page === "agents"
                ? {
                    agents: "设备目录",
                    ingresses: "访问端角色",
                    egresses: "出口提供方角色",
                  }[agentTab]
                : pages[page].title
        }
        description={
          resource === "audit"
            ? "实例级审计 · 当前工作空间筛选不作用于审计"
            : `${state.pagination.total} 条匹配记录`
        }
      >
        <DataTable
          headers={headers}
          rows={rows}
          empty={
            !state.edgeAvailable && resource === "flows" ? (
              <Empty
                title="连接状态未知"
                description="Edge 暂不可用，不能据此判断流已经结束。"
              />
            ) : undefined
          }
        />
        <PaginationBar
          value={state.pagination}
          busy={loading}
          onPage={setTablePage}
          onPageSize={(size) => {
            setPageSize(size);
            setTablePage(1);
          }}
        />
      </Panel>
    );
  }
  const formKind =
    page === "agents" || page === "overview"
      ? "key"
      : page === "egress"
        ? "route"
        : page === "access"
          ? (
              {
                grants: "grant",
                keys: "key",
                members: "member",
                workspaces: "workspace",
                users: "user",
              } as const
            )[tab]
          : null;
  const canCreate =
    formKind === "user" || formKind === "workspace"
      ? viewer.admin
      : formKind === "route" || formKind === "member"
        ? manager
        : formKind === "grant"
          ? developer
          : !!formKind;
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Brand />
        <nav>
          {Object.entries(pages)
            .filter(([key]) => key !== "audit" || viewer.admin)
            .map(([key, p]) => (
              <a
                key={key}
                href={"#" + key}
                className={page === key ? "active" : ""}
              >
                <p.icon size={18} />
                <span>{p.title}</span>
              </a>
            ))}
        </nav>
        <div className="sidebar-footer">
          <span>
            <i />
            当前实例
          </span>
          <strong>{location.host}</strong>
          <small>{viewer.admin ? "实例管理视图" : "工作空间视图"}</small>
        </div>
      </aside>
      <div className="main-shell">
        <header className="topbar">
          <div className="workspace-control">
            <span>工作空间</span>
            <div className="w-64">
              <ResourceSelect
                resource="workspaces"
                user={viewer}
                value={workspace}
                valueLabel={
                  state?.workspaces.find((w) => w.id === workspace)?.name
                }
                label="工作空间"
                onSelect={(w) => scopeTo(w.id)}
              />
            </div>
            {viewer.admin && workspace && (
              <Button variant="ghost" size="sm" onClick={() => scopeTo("")}>
                全部
              </Button>
            )}
          </div>
          <div className="topbar-right">
            <span className="user-avatar">
              {viewer.name.slice(0, 1).toUpperCase()}
            </span>
            <span className="user-name">{viewer.name}</span>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => rotateToken(viewer)}
            >
              我的令牌
            </Button>
            <Button
              variant="ghost"
              size="sm"
              aria-label="退出登录"
              onClick={async () => {
                try {
                  result(
                    await sdk.deleteCurrentSession({ signal: requestSignal() }),
                  );
                  signOut();
                } catch (e) {
                  setToast(message(e));
                }
              }}
            >
              <LogOut size={14} />
              退出
            </Button>
          </div>
        </header>
        <main>
          <div className="page-heading">
            <div>
              <span className="eyebrow">{pages[page].eyebrow}</span>
              <h1>{pages[page].title}</h1>
              <p>{pages[page].description}</p>
            </div>
            <div className="page-actions">
              <Button
                variant="outline"
                disabled={loading}
                onClick={() => void refresh()}
              >
                <RefreshCw
                  size={14}
                  className={loading ? "animate-spin" : ""}
                />
                刷新
              </Button>
              {formKind && canCreate && (
                <Button
                  disabled={!state}
                  onClick={() => setForm({ kind: formKind })}
                >
                  <Plus />
                  {
                    {
                      key: "注册密钥",
                      route: "创建出口路由",
                      grant: "添加授权",
                      member: "添加成员",
                      workspace: "创建工作空间",
                      user: "创建用户",
                    }[formKind]
                  }
                </Button>
              )}
            </div>
          </div>
          {page === "agents" && (
            <Tabs
              value={agentTab}
              onValueChange={(v) => {
                setAgentTab(v as typeof agentTab);
                setTablePage(1);
                setFilter("all");
                setQuery("");
              }}
            >
              <TabsList>
                <TabsTrigger value="agents">设备</TabsTrigger>
                <TabsTrigger value="ingresses">访问端</TabsTrigger>
                <TabsTrigger value="egresses">出口提供方</TabsTrigger>
              </TabsList>
            </Tabs>
          )}
          {page === "access" && (
            <Tabs
              value={tab}
              onValueChange={(v) => {
                setTab(v as AccessTab);
                setTablePage(1);
                setFilter("all");
                setQuery("");
              }}
            >
              <TabsList className="access-tabs">
                {Object.entries(accessTabs).map(([key, label]) => (
                  <TabsTrigger key={key} value={key}>
                    {label}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          )}
          <div className="flex flex-wrap items-center gap-3 py-4">
            <div className="search-field min-w-64">
              <Search size={15} />
              <Input
                aria-label="筛选当前列表"
                value={query}
                onChange={(e) => {
                  setQuery(e.target.value);
                  setTablePage(1);
                }}
                placeholder={
                  searchField === "q"
                    ? "搜索名称或 ID…"
                    : `${searchField === "userId" ? "用户 ID" : searchField === "resourceId" ? "资源 ID" : "审计动作"}（精确匹配）`
                }
              />
            </div>
            {filterField && (
              <div className="w-40">
                <ChoiceSelect
                  label="状态筛选"
                  value={filter}
                  onChange={(v) => {
                    setFilter(v);
                    setTablePage(1);
                  }}
                  options={[
                    { value: "all", label: "全部状态 / 类型" },
                    ...filterOptions.map((v) => ({
                      value: v,
                      label:
                        (
                          {
                            true:
                              filterField === "disabled"
                                ? "已停用"
                                : filterField === "enabled"
                                  ? "已启用"
                                  : "已撤销",
                            false:
                              filterField === "disabled"
                                ? "有效"
                                : filterField === "enabled"
                                  ? "已停用"
                                  : "未撤销",
                            active: "有效",
                            revoked: "撤销",
                            serve: "服务发布",
                            egress: "TLS 出口",
                            ...roleNames,
                          } as Record<string, string>
                        )[v] || v,
                    })),
                  ]}
                />
              </div>
            )}
            <div className="w-40">
              <ChoiceSelect
                label="排序字段"
                value={
                  sortFields[resource].includes(sortBy)
                    ? sortBy
                    : sortFields[resource][0]
                }
                onChange={(v) => {
                  setSortBy(v);
                  setTablePage(1);
                }}
                options={sortFields[resource].map((v) => ({
                  value: v,
                  label:
                    (
                      {
                        id: "按 ID",
                        name: "按名称",
                        createdAt: "按创建时间",
                        at: "按事件时间",
                        status: "按状态",
                        hostname: "按域名",
                        priority: "按优先级",
                        expiresAt: "按截止时间",
                        startsAt: "按生效时间",
                        startedAt: "按开始时间",
                        kind: "按类型",
                        role: "按角色",
                        userId: "按用户",
                        workspaceId: "按工作空间",
                        action: "按动作",
                        purpose: "按用途",
                        state: "按状态",
                      } as Record<string, string>
                    )[v] || v,
                }))}
              />
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setSortOrder(sortOrder === "asc" ? "desc" : "asc");
                setTablePage(1);
              }}
            >
              {sortOrder === "asc" ? "升序 ↑" : "降序 ↓"}
            </Button>
            <span className="text-xs text-muted-foreground">
              {loading ? "正在同步…" : "每 10 秒刷新当前列表"}
            </span>
          </div>
          {error && <Notice error>{error}</Notice>}
          {state && !state.edgeAvailable && (
            <Notice>
              Edge 状态不可用；在线状态和活动流未知，配置仍可管理。
            </Notice>
          )}
          {page === "overview" && state && (
            <div className="metrics">
              {[
                {
                  title: "已注册 Agents",
                  value: state.totals.agents,
                  foot: `${state.totals.revoked ?? "—"} 个已撤销`,
                },
                {
                  title: "已发布服务",
                  value: state.totals.servers,
                  foot: "已启用发布",
                },
                {
                  title: "出口路由",
                  value: state.totals.routes,
                  foot: "已启用 TLS 出口路由",
                },
                {
                  title: "活动流",
                  value: state.edgeAvailable
                    ? state.pagination.total
                    : undefined,
                  foot: "当前授权范围内",
                },
              ].map((m) => (
                <Card key={m.title} className="metric">
                  <CardContent>
                    <div className="metric-top">{m.title}</div>
                    <div className="metric-number">{m.value ?? "—"}</div>
                    <div className="metric-foot">{m.foot}</div>
                  </CardContent>
                </Card>
              ))}
            </div>
          )}
          {state ? (
            renderTable()
          ) : loading ? (
            <div className="loading-state">
              <Skeleton className="h-20 w-full" />
              <Skeleton className="h-20 w-full" />
            </div>
          ) : (
            <Empty
              title="尚未加载资源"
              description="选择工作空间或刷新重试。"
            />
          )}
          <footer className="main-footer">
            <span>agents 控制台</span>
            <span>
              服务端按当前身份检查资源权限 · REV {state?.revision ?? "—"}
            </span>
          </footer>
        </main>
      </div>
      {form && state && (
        <ManagementDialog
          request={form}
          state={state}
          user={viewer}
          workspace={workspace}
          onClose={() => setForm(null)}
          onComplete={complete}
        />
      )}{" "}
      {confirm && (
        <ConfirmDialog
          action={confirm}
          onClose={() => setConfirm(null)}
          onComplete={complete}
        />
      )}{" "}
      {selectedAgent && state && (
        <AgentDialog
          agent={selectedAgent}
          state={state}
          user={viewer}
          onClose={() => setSelectedAgent(null)}
          onComplete={() => complete()}
        />
      )}{" "}
      {secret && (
        <SecretDialog secret={secret} onClose={() => setSecret(null)} />
      )}{" "}
      {toast && (
        <div className="toast" role="status">
          {toast}
        </div>
      )}
    </div>
  );
}

function Login({
  info,
  onSession,
  error,
}: {
  info: Info | null;
  onSession: (session: Session) => void;
  error: string;
}) {
  const [token, setToken] = useState(""),
    [busy, setBusy] = useState(false),
    [loginError, setLoginError] = useState(""),
    [showToken, setShowToken] = useState(false);
  const oidc = info?.oidc.enabled === true;
  async function login(event: FormEvent) {
    event.preventDefault();
    if (busy || !token.trim()) return;
    setBusy(true);
    setLoginError("");
    try {
      const session = result(
        await sdk.createSession({
          body: { token: token.trim() },
          signal: requestSignal(),
        }),
      );
      setToken("");
      onSession(session);
    } catch (e) {
      setLoginError(message(e));
    } finally {
      setToken("");
      setBusy(false);
    }
  }
  return (
    <div className="login-layout">
      <section className="login-story">
        <Brand />
        <div className="login-copy">
          <span className="eyebrow">YOUR WORKSPACE, CONNECTED</span>
          <h1>
            连接你的服务。
            <br />
            掌握每一次访问。
          </h1>
          <p>从设备注册到服务发布，在同一个空间管理连接、出口与访问权限。</p>
          <div className="login-topology">
            <span>应用与访客</span>
            <i />
            <strong>agents</strong>
            <i />
            <span>你的 Agent</span>
          </div>
        </div>
        <span className="login-foot">身份清晰 · 权限明确 · 状态可见</span>
      </section>
      <section className="login-main">
        <div className="login-card">
          <span className="eyebrow">WELCOME BACK</span>
          <h2>登录控制台</h2>
          <p className="login-description">
            {oidc
              ? "通过实例配置的身份提供方安全登录。"
              : "使用管理员提供的用户访问令牌登录。"}
          </p>
          {error && <Notice error>{error}</Notice>}
          {oidc && (
            <>
              <Button
                className="login-primary"
                size="lg"
                onClick={() => {
                  try {
                    location.assign(oidcDestination(info.oidc.loginURL));
                  } catch (e) {
                    setLoginError(message(e));
                  }
                }}
              >
                <ShieldCheck />
                使用单点登录
                <ArrowRight />
              </Button>
              <Button
                className="w-full mt-3"
                variant="outline"
                onClick={() => setShowToken((v) => !v)}
              >
                <KeyRound />
                使用访问令牌
              </Button>
            </>
          )}
          {(!oidc || showToken) && (
            <form
              className={oidc ? "token-login secondary-login" : "token-login"}
              onSubmit={login}
            >
              <Label htmlFor="login-token">访问令牌</Label>
              <Input
                id="login-token"
                type="password"
                autoComplete="off"
                maxLength={4096}
                required
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="输入你的访问令牌"
              />
              <Button
                type="submit"
                className="login-primary"
                size="lg"
                disabled={busy}
              >
                {busy ? (
                  <LoaderCircle className="animate-spin" />
                ) : (
                  <KeyRound />
                )}
                {busy ? "正在登录…" : "登录"}
                <ArrowRight />
              </Button>
              <p className="login-note">
                令牌仅用于本次登录，不保存在浏览器存储中。
              </p>
            </form>
          )}
          {loginError && <Notice error>{loginError}</Notice>}
        </div>
      </section>
    </div>
  );
}
