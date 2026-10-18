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
  CheckCircle2,
  CircleHelp,
  Clock3,
  Globe2,
  KeyRound,
  Layers3,
  LayoutDashboard,
  LoaderCircle,
  LogOut,
  Plus,
  RefreshCw,
  Search,
  Server,
  ShieldCheck,
  Users,
} from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type {
  Agent,
  Audit,
  EgressRoute,
  Grant,
  Info,
  Service,
  Session,
  State,
  User,
} from "@/api/generated";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
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
  activeGrants,
  agentRoles,
  canEnroll,
  canGrant,
  canManage,
  canManageResource,
  grantHealth,
  health,
  name,
  pending,
  resourceName,
  roleNames,
  rows,
  rule,
  serviceURL,
  short,
  subjectName,
  time,
  type Health,
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
    title: "出口路由",
    eyebrow: "EGRESS ROUTES",
    description: "将获准的 TLS 域名请求交给指定 Agent。",
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
  "agent.approve": "批准 Agent",
  "agent.revoke": "撤销 Agent",
  "agent.desired": "更新期望配置",
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
    [state, setState] = useState<State | null>(null);
  const [boot, setBoot] = useState(true),
    [loading, setLoading] = useState(false),
    [syncError, setSyncError] = useState(""),
    [lastSync, setLastSync] = useState<Date | null>(null);
  const [page, setPage] = useState<Page>(currentPage),
    [tab, setTab] = useState<AccessTab>("grants"),
    [workspace, setWorkspace] = useState(""),
    [query, setQuery] = useState("");
  const [form, setForm] = useState<FormRequest | null>(null),
    [confirm, setConfirm] = useState<ConfirmAction | null>(null),
    [selectedAgent, setSelectedAgent] = useState<Agent | null>(null),
    [secret, setSecret] = useState<Secret | null>(null),
    [toast, setToast] = useState("");
  const activeUser = useRef<User | null>(null),
    generation = useRef(0),
    fetching = useRef(false),
    hasState = useRef(false);
  const signOut = useCallback(() => {
    generation.current++;
    activeUser.current = null;
    hasState.current = false;
    setCSRF("");
    setUser(null);
    setState(null);
    setSecret(null);
    setConfirm(null);
    setForm(null);
    setSelectedAgent(null);
    setWorkspace("");
    setQuery("");
  }, []);
  const refresh = useCallback(async () => {
    if (!activeUser.current || fetching.current) return;
    const serial = generation.current;
    fetching.current = true;
    setLoading(true);
    try {
      const next = result(await sdk.getState({ signal: requestSignal() }));
      if (serial !== generation.current) return;
      setState(next);
      hasState.current = true;
      setUser(next.user);
      activeUser.current = next.user;
      setLastSync(new Date());
      setSyncError("");
      setWorkspace((previous) =>
        !previous || (next.workspaces ?? []).some((w) => w.id === previous)
          ? previous
          : "",
      );
    } catch (error) {
      if (serial !== generation.current) return;
      if (error instanceof APIFailure && error.status === 401) {
        signOut();
        setToast("登录已过期，请重新登录。");
      } else setSyncError(message(error));
    } finally {
      fetching.current = false;
      setLoading(false);
    }
  }, [signOut]);
  const acceptSession = useCallback(
    (session: Session) => {
      generation.current++;
      activeUser.current = session.user;
      setUser(session.user);
      setCSRF(session.csrfToken);
      const returnTo = serviceReturnTo();
      if (returnTo) {
        location.replace(returnTo);
        return;
      }
      void refresh();
    },
    [refresh],
  );
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const [publicResult, sessionResult] = await Promise.allSettled([
        sdk.getInfo({ signal: requestSignal() }),
        sdk.getMe({ signal: requestSignal() }),
      ]);
      if (cancelled) return;
      if (publicResult.status === "fulfilled") {
        try {
          setInfo(result(publicResult.value));
        } catch {
          setSyncError("暂时无法获取登录方式。");
        }
      }
      if (sessionResult.status === "fulfilled") {
        try {
          acceptSession(result(sessionResult.value));
        } catch {
          /* An anonymous browser starts on the login page. */
        }
      }
      setBoot(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [acceptSession]);
  useEffect(() => {
    const listener = () => {
      setPage(currentPage());
      setQuery("");
    };
    window.addEventListener("hashchange", listener);
    return () => window.removeEventListener("hashchange", listener);
  }, []);
  const modalOpen = Boolean(form || confirm || selectedAgent || secret);
  useEffect(() => {
    const timer = setInterval(() => {
      if (!document.hidden && !modalOpen) void refresh();
    }, 10000);
    const visible = () => {
      if (!document.hidden && !modalOpen) void refresh();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [refresh, modalOpen]);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(""), 4500);
    return () => clearTimeout(timer);
  }, [toast]);
  useEffect(() => {
    document.title = pages[page].title + " · agents";
  }, [page]);
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
        <Login info={info} onSession={acceptSession} error={syncError} />
        {toast && (
          <div className="toast" role="status">
            {toast}
          </div>
        )}
      </>
    );
  const viewer = user;
  const data = state ? rows(state) : null;
  const scope = <T extends { workspaceId: string }>(items: T[]) =>
    items.filter((i) => !workspace || i.workspaceId === workspace);
  const match = (...values: (string | undefined)[]) =>
    !query ||
    values
      .filter(Boolean)
      .join(" ")
      .toLocaleLowerCase()
      .includes(query.toLocaleLowerCase());
  const pendingCount = data ? scope(data.agents).filter(pending).length : 0;
  function grantForm(
    resource?: Service | EgressRoute,
    type: "service" | "egress" = "service",
  ) {
    setForm({
      kind: "grant",
      resource: resource
        ? { id: resource.id, workspaceId: resource.workspaceId, type }
        : undefined,
    });
  }
  function revokeAgent(agent: Agent) {
    setConfirm({
      title: `撤销 ${agent.name}`,
      description: "该 Agent 将失去连接与提供业务的资格，当前访问可能中断。",
      label: "确认撤销",
      danger: true,
      perform: async () => {
        result(
          await sdk.revokeAgent({
            path: { id: agent.id },
            signal: requestSignal(),
          }),
        );
      },
    });
  }
  function toggleService(service: Service) {
    setConfirm({
      title: `${service.enabled ? "停用" : "启用"} ${service.name}`,
      description: service.enabled
        ? "服务将暂停发布，访问此服务的用户会受到影响。"
        : "恢复发布状态；实际访问仍需提供方连接就绪并命中有效授权。",
      label: service.enabled ? "确认停用" : "确认启用",
      danger: service.enabled,
      perform: async () => {
        if (service.enabled)
          result(
            await sdk.disableService({
              path: { id: service.id },
              signal: requestSignal(),
            }),
          );
        else
          result(
            await sdk.enableService({
              path: { id: service.id },
              signal: requestSignal(),
            }),
          );
      },
    });
  }
  function toggleRoute(route: EgressRoute) {
    setConfirm({
      title: `${route.enabled ? "停用" : "启用"} ${route.name}`,
      description: route.enabled
        ? "这条规则将暂停选择出口，依赖它的访问会受到影响。"
        : "恢复规则状态；实际访问仍需有效授权与出口能力。",
      label: route.enabled ? "确认停用" : "确认启用",
      danger: route.enabled,
      perform: async () => {
        if (route.enabled)
          result(
            await sdk.disableEgressRoute({
              path: { id: route.id },
              signal: requestSignal(),
            }),
          );
        else
          result(
            await sdk.enableEgressRoute({
              path: { id: route.id },
              signal: requestSignal(),
            }),
          );
      },
    });
  }
  function revokeGrant(grant: Grant) {
    setConfirm({
      title: "撤销访问授权",
      description: "移除这一授权来源；访客仍命中其他有效授权时可以继续访问。",
      label: "确认撤销",
      danger: true,
      perform: async () => {
        result(
          await sdk.revokeGrant({
            path: { id: grant.id },
            signal: requestSignal(),
          }),
        );
      },
    });
  }
  function rotateToken(target: User) {
    setConfirm({
      title: `更新 ${target.name} 的令牌`,
      description:
        "旧用户 Bearer 令牌立即失效，请更新使用它的调用方。已有浏览器会话仍按原期限有效；新令牌只展示一次。",
      label: "确认更新令牌",
      perform: async () => {
        const response = result(
          await sdk.rotateUserToken({
            path: { id: target.id },
            signal: requestSignal(),
          }),
        );
        return {
          title: "新用户令牌已签发",
          subtitle: target.name,
          token: response.token,
          filename: "agents-user-token.txt",
        };
      },
    });
  }
  function resourceHealth(agentID: string, serviceName?: string): Health {
    if (!state || !data) return { label: "状态未知", tone: "muted" };
    const agent = data.agents.find((a) => a.id === agentID);
    if (!agent) return { label: "提供方不存在", tone: "bad" };
    const known = health(state, agent);
    if (
      agent.status !== "active" ||
      known.label === "批准已过期" ||
      !state.edgeAvailable
    )
      return known;
    const ready = data.connections.some(
      (c) =>
        c.agentId === agentID &&
        c.role === "provider" &&
        c.revision === agent.approval.revision &&
        (serviceName
          ? (c.services ?? []).includes(serviceName)
          : c.egressEnabled),
    );
    return ready
      ? { label: "已连接", tone: "good" }
      : { label: "提供方未就绪", tone: "warn" };
  }
  function agentStatus(agent: Agent) {
    return (
      <div className="flex flex-wrap gap-1.5">
        <Status {...health(state!, agent)} />
        {agent.pendingApproval && agent.status === "active" && (
          <Status label="配置待批准" tone="warn" />
        )}
      </div>
    );
  }
  function audits(): Audit[] {
    if (!data) return [];
    return data.audit
      .filter(
        (a) =>
          match(
            a.action,
            a.detail,
            name(state!, "users", a.actorId),
            resourceName(state!, a.resourceId),
          ) &&
          (!workspace ||
            a.resourceId === workspace ||
            [
              ...data.agents,
              ...data.services,
              ...data.egressRoutes,
              ...data.grants,
              ...data.enrollmentKeys,
            ].some(
              (r) => r.id === a.resourceId && r.workspaceId === workspace,
            )),
      )
      .sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime());
  }
  let primary: ReactNode = null;
  if (state) {
    if (
      ["overview", "agents"].includes(page) &&
      canEnroll(state, viewer, workspace)
    )
      primary = (
        <Button onClick={() => setForm({ kind: "key" })}>
          <Plus />
          注册密钥
        </Button>
      );
    if (page === "egress" && canManage(state, viewer, workspace))
      primary = (
        <Button onClick={() => setForm({ kind: "route" })}>
          <Plus />
          创建路由
        </Button>
      );
    if (page === "access") {
      const kind = (
        {
          grants: "grant",
          keys: "key",
          members: "member",
          workspaces: "workspace",
          users: "user",
        } as const
      )[tab];
      const allowed =
        tab === "users" || tab === "workspaces"
          ? viewer.admin
          : tab === "keys"
            ? canEnroll(state, viewer, workspace)
            : tab === "grants"
              ? canGrant(state, viewer, workspace)
              : canManage(state, viewer, workspace);
      if (allowed)
        primary = (
          <Button onClick={() => setForm({ kind })}>
            <Plus />
            {
              {
                grants: "添加授权",
                keys: "注册密钥",
                members: "添加成员",
                workspaces: "创建 Workspace",
                users: "创建用户",
              }[tab]
            }
          </Button>
        );
    }
  }
  function renderOverview() {
    if (!state || !data) return null;
    const agents = scope(data.agents),
      services = scope(data.services),
      routes = scope(data.egressRoutes),
      flows = scope(data.flows);
    const metrics = [
      {
        title: "在线 Agents",
        value: state.edgeAvailable
          ? agents.filter((a) => health(state, a).tone === "good").length
          : "—",
        foot: `${agents.length} 个已注册 · ${pendingCount} 个待批准`,
        icon: Server,
      },
      {
        title: "已发布服务",
        value: services.filter((s) => s.enabled).length,
        foot: `${services.length} 个服务 · ${services.filter((s) => activeGrants(state, s.id).length).length} 个有有效授权`,
        icon: Globe2,
      },
      {
        title: "出口路由",
        value: routes.filter((r) => r.enabled).length,
        foot: "按域名和指定 Agent 选择出口",
        icon: ArrowRightLeft,
      },
      {
        title: "活动流",
        value: state.edgeAvailable ? flows.length : "—",
        foot: state.edgeAvailable
          ? `${scope(data.connections).length} 条已认证连接`
          : "等待 Edge 恢复状态",
        icon: Activity,
      },
    ];
    const recent = audits().slice(0, 4);
    return (
      <>
        <div className="metrics">
          {metrics.map((m) => (
            <Card key={m.title} className="metric">
              <CardContent>
                <div className="metric-top">
                  <span>{m.title}</span>
                  <m.icon size={17} />
                </div>
                <div className="metric-number">{m.value}</div>
                <div className="metric-foot">{m.foot}</div>
              </CardContent>
            </Card>
          ))}
        </div>
        {pendingCount > 0 && (
          <Notice>
            有 {pendingCount} 个 Agent
            等待批准，请核对声明的本地服务与出口范围。
          </Notice>
        )}
        <div className="overview-grid">
          <Panel
            title="Agent 状态"
            description="设备身份与实际连接分别展示"
            action={
              <Button
                size="sm"
                variant="ghost"
                onClick={() => (location.hash = "agents")}
              >
                查看全部
                <ArrowRight size={13} />
              </Button>
            }
          >
            <DataTable
              headers={["Agent", "状态", "声明角色", "操作"]}
              rows={agents
                .filter((a) => match(a.name, a.workspace))
                .sort((a, b) => Number(pending(b)) - Number(pending(a)))
                .slice(0, 5)
                .map((a) => ({
                  key: a.id,
                  cells: [
                    <Cell
                      title={a.name}
                      subtitle={name(state, "workspaces", a.workspaceId)}
                      icon={<Server size={15} />}
                    />,
                    agentStatus(a),
                    <Tags items={agentRoles(a)} />,
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setSelectedAgent(a)}
                    >
                      查看
                    </Button>,
                  ],
                }))}
              empty={
                <Empty
                  title="还没有 Agent"
                  description="创建注册密钥，在设备上完成 agentd 注册后，设备会出现在这里。"
                />
              }
            />
          </Panel>
          <Panel
            title="最近操作"
            description="资源与授权策略变更"
            action={
              <Button
                size="sm"
                variant="ghost"
                onClick={() => (location.hash = "audit")}
              >
                全部日志
                <ArrowRight size={13} />
              </Button>
            }
          >
            {recent.length ? (
              <div className="activity-list">
                {recent.map((a) => (
                  <div className="activity-row" key={a.id}>
                    <span className="activity-icon">
                      <Activity size={14} />
                    </span>
                    <div>
                      <strong>{actionLabels[a.action] || a.action}</strong>
                      <p>
                        {name(state, "users", a.actorId)} ·{" "}
                        {resourceName(state, a.resourceId)}
                      </p>
                    </div>
                    <time>{time(a.at)}</time>
                  </div>
                ))}
              </div>
            ) : (
              <Empty
                title="暂无操作记录"
                description="创建、批准与撤销操作会记录在这里。"
              />
            )}
          </Panel>
        </div>
        <Panel title="活动流" description="当前传输的请求与连接">
          {!state.edgeAvailable ? (
            <Empty
              title="连接状态未知"
              description="Edge 暂不可用，不能据此判断流已结束。"
            />
          ) : (
            <DataTable
              headers={["用途 / 流 ID", "资源", "提供方", "状态", "租约截止"]}
              rows={flows
                .filter((f) => match(f.purpose, f.resourceId, f.agentId))
                .slice(0, 20)
                .map((f) => ({
                  key: f.id,
                  cells: [
                    <Cell
                      title={roleNames[f.purpose]}
                      subtitle={short(f.id)}
                    />,
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
                }))}
              empty={
                <Empty
                  title="当前没有活动流"
                  description="服务访问或出口请求建立后，对应业务流会显示在这里。"
                />
              }
            />
          )}
        </Panel>
      </>
    );
  }
  function renderAgents() {
    if (!state || !data) return null;
    const agents = scope(data.agents).filter((a) =>
      match(a.name, a.id, a.workspace, name(state, "users", a.userId)),
    );
    return (
      <Panel
        title="已注册 Agents"
        description={`${agents.length} 个设备 / profile`}
      >
        <DataTable
          headers={[
            "Agent",
            "工作空间",
            "声明角色",
            "状态",
            "批准期限",
            "操作",
          ]}
          rows={agents.map((a) => ({
            key: a.id,
            cells: [
              <Cell
                title={a.name}
                subtitle={short(a.id)}
                icon={<Server size={15} />}
              />,
              name(state, "workspaces", a.workspaceId),
              <Tags items={agentRoles(a)} />,
              agentStatus(a),
              time(a.approval.expiresAt),
              <div className="row-actions">
                <Button
                  size="sm"
                  variant={
                    pending(a) && canManage(state, viewer, a.workspaceId)
                      ? "default"
                      : "outline"
                  }
                  onClick={() => setSelectedAgent(a)}
                >
                  {pending(a) && canManage(state, viewer, a.workspaceId)
                    ? "审核"
                    : "查看"}
                </Button>
                {canManage(state, viewer, a.workspaceId) &&
                  a.status !== "revoked" && (
                    <Button
                      size="sm"
                      variant="destructive"
                      onClick={() => revokeAgent(a)}
                    >
                      撤销
                    </Button>
                  )}
              </div>,
            ],
          }))}
          empty={
            <Empty
              title="还没有注册的 Agent"
              description="注册密钥用于设备首次接入；设备提交期望配置后会显示在这里。"
            />
          }
        />
      </Panel>
    );
  }
  function renderServices() {
    if (!state || !data) return null;
    const services = scope(data.services).filter((s) =>
      match(s.name, s.hostname, name(state, "agents", s.agentId)),
    );
    return (
      <>
        <Notice>
          源站由 agentd
          本地配置声明。控制台管理发布与访问授权，不直接改写设备的源站地址。
        </Notice>
        <Panel
          title="已发布服务"
          description={`${services.length} 个服务 · 连接状态不等同于源站健康探测`}
        >
          <DataTable
            headers={["服务", "访问地址", "提供方", "状态", "有效授权", "操作"]}
            rows={services.map((s) => {
              const url = serviceURL(state, s.hostname);
              return {
                key: s.id,
                cells: [
                  <Cell
                    title={s.name}
                    subtitle={name(state, "workspaces", s.workspaceId)}
                    icon={<Globe2 size={15} />}
                  />,
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
                  name(state, "agents", s.agentId),
                  <Status
                    {...(s.enabled
                      ? resourceHealth(s.agentId, s.name)
                      : { label: "已停用", tone: "muted" })}
                  />,
                  `${activeGrants(state, s.id).length} 条`,
                  canManageResource(state, viewer, "service", s.id) ? (
                    <div className="row-actions">
                      {s.enabled && (
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => grantForm(s)}
                        >
                          授权
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant={s.enabled ? "destructive" : "default"}
                        onClick={() => toggleService(s)}
                      >
                        {s.enabled ? "停用" : "启用"}
                      </Button>
                    </div>
                  ) : (
                    <span className="readonly">只读</span>
                  ),
                ],
              };
            })}
            empty={
              <Empty
                title="还没有发布的服务"
                description="在设备配置中启用 serve 并声明本地源站，完成注册和批准后即可在这里管理。"
              />
            }
          />
        </Panel>
      </>
    );
  }
  function renderEgress() {
    if (!state || !data) return null;
    const routes = scope(data.egressRoutes).filter((r) =>
      match(r.name, rule(r.rule), name(state, "agents", r.agentId)),
    );
    return (
      <>
        <Notice>
          路由与使用授权分别管理。出口离线或访问被拒绝时，不会自动换用其他设备。
        </Notice>
        <Panel
          title="域名出口规则"
          description={`${routes.length} 条规则 · 目标网站 TLS 保持端到端`}
        >
          <DataTable
            headers={[
              "规则 / 目标",
              "指定出口",
              "优先级",
              "状态",
              "有效授权",
              "操作",
            ]}
            rows={routes.map((r) => ({
              key: r.id,
              cells: [
                <Cell
                  title={r.name}
                  subtitle={rule(r.rule)}
                  icon={<ArrowRightLeft size={15} />}
                />,
                <Cell
                  title={name(state, "agents", r.agentId)}
                  subtitle={name(state, "workspaces", r.workspaceId)}
                />,
                String(r.priority),
                <Status
                  {...(r.enabled
                    ? resourceHealth(r.agentId)
                    : { label: "已停用", tone: "muted" })}
                />,
                `${activeGrants(state, r.id).length} 条`,
                canManage(state, viewer, r.workspaceId) ? (
                  <div className="row-actions">
                    {r.enabled && (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => grantForm(r, "egress")}
                      >
                        授权
                      </Button>
                    )}
                    <Button
                      size="sm"
                      variant={r.enabled ? "destructive" : "default"}
                      onClick={() => toggleRoute(r)}
                    >
                      {r.enabled ? "停用" : "启用"}
                    </Button>
                  </div>
                ) : (
                  <span className="readonly">只读</span>
                ),
              ],
            }))}
            empty={
              <Empty
                title="还没有出口路由"
                description="先批准提供出口能力的 Agent，再创建域名到该 Agent 的规则。"
              />
            }
          />
        </Panel>
      </>
    );
  }
  function renderAccess() {
    if (!state || !data) return null;
    let body: ReactNode;
    if (tab === "grants") {
      const grants = scope(data.grants).filter((g) =>
        match(resourceName(state, g.resourceId), subjectName(state, g)),
      );
      body = (
        <Panel
          title="访问授权"
          description="多条有效授权按并集生效；登录不会延长授权期限"
        >
          <DataTable
            headers={["资源", "授权对象", "开始", "截止", "状态", "操作"]}
            rows={grants.map((g) => ({
              key: g.id,
              cells: [
                <Cell
                  title={resourceName(state, g.resourceId)}
                  subtitle={`${g.resourceType === "service" ? "Web 服务" : "TLS 出口"} · ${name(state, "workspaces", g.workspaceId)}`}
                />,
                subjectName(state, g),
                time(g.startsAt),
                time(g.expiresAt),
                <Status {...grantHealth(g)} />,
                !g.revoked &&
                canManageResource(
                  state,
                  viewer,
                  g.resourceType,
                  g.resourceId,
                ) ? (
                  <Button
                    variant="destructive"
                    size="sm"
                    onClick={() => revokeGrant(g)}
                  >
                    撤销
                  </Button>
                ) : (
                  "—"
                ),
              ],
            }))}
            empty={
              <Empty
                title="还没有访问授权"
                description="为指定资源选择访客与有限有效期。"
              />
            }
          />
        </Panel>
      );
    }
    if (tab === "keys") {
      const keys = scope(data.enrollmentKeys).filter((k) =>
        match(k.id, name(state, "users", k.userId), ...k.roles),
      );
      body = (
        <Panel title="设备注册密钥" description="完整令牌仅在创建时显示一次">
          <DataTable
            headers={[
              "密钥 / 工作空间",
              "注册用户",
              "允许角色",
              "使用情况",
              "截止时间",
              "状态",
              "操作",
            ]}
            rows={keys.map((k) => {
              const status: Health = k.revoked
                ? { label: "已撤销", tone: "muted" }
                : new Date(k.expiresAt) <= new Date()
                  ? { label: "已到期", tone: "muted" }
                  : k.uses >= k.maxUses
                    ? { label: "已用尽", tone: "muted" }
                    : { label: "可使用", tone: "good" };
              return {
                key: k.id,
                cells: [
                  <Cell
                    title={short(k.id)}
                    subtitle={name(state, "workspaces", k.workspaceId)}
                  />,
                  name(state, "users", k.userId),
                  <Tags items={k.roles.map((r) => roleNames[r])} />,
                  `${k.uses} / ${k.maxUses}`,
                  time(k.expiresAt),
                  <Status {...status} />,
                  !k.revoked &&
                  (canManage(state, viewer, k.workspaceId) ||
                    k.userId === viewer.id) ? (
                    <Button
                      size="sm"
                      variant="destructive"
                      onClick={() =>
                        setConfirm({
                          title: "撤销注册密钥",
                          description:
                            "此密钥不能再用于注册，已注册设备需要在 Agents 页面单独管理。",
                          label: "确认撤销",
                          danger: true,
                          perform: async () => {
                            result(
                              await sdk.revokeEnrollmentKey({
                                path: { id: k.id },
                                signal: requestSignal(),
                              }),
                            );
                          },
                        })
                      }
                    >
                      撤销
                    </Button>
                  ) : (
                    "—"
                  ),
                ],
              };
            })}
            empty={
              <Empty
                title="还没有注册密钥"
                description="为指定成员创建限时、限次的设备注册凭据。"
              />
            }
          />
        </Panel>
      );
    }
    if (tab === "members") {
      const members = scope(data.memberships).filter((m) =>
        match(
          name(state, "users", m.userId),
          name(state, "workspaces", m.workspaceId),
          roleNames[m.role],
        ),
      );
      body = (
        <Panel
          title="工作空间成员"
          description="同一用户在不同 Workspace 的角色独立"
        >
          <DataTable
            headers={["用户", "工作空间", "角色", "操作"]}
            rows={members.map((m) => ({
              key: m.workspaceId + ":" + m.userId,
              cells: [
                <Cell
                  title={name(state, "users", m.userId)}
                  subtitle={short(m.userId)}
                  icon={<Users size={15} />}
                />,
                name(state, "workspaces", m.workspaceId),
                <Status
                  label={roleNames[m.role]}
                  tone={m.role === "owner" ? "good" : "muted"}
                />,
                canManage(state, viewer, m.workspaceId) ? (
                  <Button
                    size="sm"
                    variant="destructive"
                    onClick={() =>
                      setConfirm({
                        title: "移除工作空间成员",
                        description: `${name(state, "users", m.userId)} 将失去 ${name(state, "workspaces", m.workspaceId)} 的成员授权。个人设备可能随之暂停。`,
                        label: "确认移除",
                        danger: true,
                        perform: async () => {
                          result(
                            await sdk.removeMembership({
                              path: {
                                workspace: m.workspaceId,
                                user: m.userId,
                              },
                              signal: requestSignal(),
                            }),
                          );
                        },
                      })
                    }
                  >
                    移除
                  </Button>
                ) : (
                  "—"
                ),
              ],
            }))}
            empty={<Empty title="暂无成员记录" />}
          />
        </Panel>
      );
    }
    if (tab === "workspaces") {
      const workspaces = data.workspaces.filter(
        (w) => (!workspace || w.id === workspace) && match(w.name, w.id),
      );
      body = (
        <Panel
          title="工作空间"
          description="资源归属、成员身份与策略的管理边界"
        >
          <DataTable
            headers={["工作空间", "成员", "Agents", "服务", "创建时间"]}
            rows={workspaces.map((w) => ({
              key: w.id,
              cells: [
                <Cell
                  title={w.name}
                  subtitle={short(w.id)}
                  icon={<Layers3 size={15} />}
                />,
                String(
                  data.memberships.filter((m) => m.workspaceId === w.id).length,
                ),
                String(
                  data.agents.filter((a) => a.workspaceId === w.id).length,
                ),
                String(
                  data.services.filter((s) => s.workspaceId === w.id).length,
                ),
                time(w.createdAt),
              ],
            }))}
            empty={
              <Empty
                title="还没有工作空间"
                description="创建第一个 Workspace，开始组织设备、服务和成员。"
              />
            }
          />
        </Panel>
      );
    }
    if (tab === "users") {
      const users = data.users.filter(
        (u) =>
          (!workspace ||
            data.memberships.some(
              (m) => m.workspaceId === workspace && m.userId === u.id,
            )) &&
          match(u.name, u.id),
      );
      body = (
        <Panel
          title="实例用户"
          description="新令牌仅在签发后显示一次；用户可以加入多个工作空间"
        >
          <DataTable
            headers={["用户", "身份", "工作空间", "状态", "操作"]}
            rows={users.map((u) => ({
              key: u.id,
              cells: [
                <Cell
                  title={u.name}
                  subtitle={short(u.id)}
                  icon={<Users size={15} />}
                />,
                <Status
                  label={u.admin ? "实例管理员" : "用户"}
                  tone={u.admin ? "info" : "muted"}
                />,
                <span className="wrap-text">
                  {data.memberships
                    .filter((m) => m.userId === u.id)
                    .map((m) => name(state, "workspaces", m.workspaceId))
                    .join("、") || "未加入"}
                </span>,
                <Status
                  label={u.disabled ? "已停用" : "有效"}
                  tone={u.disabled ? "muted" : "good"}
                />,
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
                        setConfirm({
                          title: `停用 ${u.name}`,
                          description:
                            "该用户将无法继续登录或使用自己的授权和设备。",
                          label: "确认停用用户",
                          danger: true,
                          perform: async () => {
                            result(
                              await sdk.disableUser({
                                path: { id: u.id },
                                signal: requestSignal(),
                              }),
                            );
                          },
                        })
                      }
                    >
                      停用
                    </Button>
                  )}
                </div>,
              ],
            }))}
            empty={
              <Empty
                title="没有匹配的用户"
                description="切换到全部 Workspace 可以查看尚未加入成员组的用户。"
              />
            }
          />
        </Panel>
      );
    }
    return (
      <Tabs
        value={tab}
        onValueChange={(value) => {
          setTab(value as AccessTab);
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
        <TabsContent value={tab}>{body}</TabsContent>
      </Tabs>
    );
  }
  function renderAudit() {
    if (!state) return null;
    const events = audits();
    return (
      <Panel
        title="操作记录"
        description={`${events.length} 条记录${workspace ? " · 实例级事件请切换到全部 Workspace" : ""}`}
      >
        <DataTable
          headers={["时间", "操作者", "操作", "资源", "详情"]}
          rows={events.map((a) => ({
            key: a.id,
            cells: [
              <span className="mono">{time(a.at, true)}</span>,
              name(state, "users", a.actorId),
              <Cell
                title={actionLabels[a.action] || a.action}
                subtitle={a.action}
              />,
              <Cell
                title={resourceName(state, a.resourceId)}
                subtitle={short(a.resourceId)}
              />,
              <span className="wrap-text text-muted-foreground">
                {a.detail || "—"}
              </span>,
            ],
          }))}
          empty={
            <Empty
              title="暂无审计记录"
              description="创建、批准与撤销操作会显示在这里。"
            />
          }
        />
      </Panel>
    );
  }
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Brand />
        <span className="sidebar-label">管理控制台</span>
        <nav aria-label="主导航">
          {Object.entries(pages).map(([key, p]) => (
            <a
              key={key}
              href={"#" + key}
              className={page === key ? "active" : ""}
              aria-current={page === key ? "page" : undefined}
            >
              <p.icon size={18} />
              <span>{p.title}</span>
              {key === "agents" && pendingCount > 0 && (
                <span className="nav-count">{pendingCount}</span>
              )}
            </a>
          ))}
        </nav>
        <div className="sidebar-footer">
          <span>
            <i />
            当前实例
          </span>
          <strong>{location.host}</strong>
          <small>私有服务与域名出口</small>
        </div>
      </aside>
      <div className="main-shell">
        <header className="topbar">
          <div className="workspace-control">
            <span>工作空间</span>
            <ChoiceSelect
              label="切换工作空间"
              value={workspace || "__all"}
              onChange={(v) => {
                setWorkspace(v === "__all" ? "" : v);
                setQuery("");
              }}
              options={[
                { value: "__all", label: "全部 Workspace" },
                ...(data?.workspaces ?? []).map((w) => ({
                  value: w.id,
                  label: w.name,
                })),
              ]}
            />
          </div>
          <div className="topbar-right">
            <div className="search-field">
              <Search size={15} />
              <Input
                aria-label="搜索当前页面"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="搜索当前页面…"
              />
            </div>
            <span className="user-avatar">
              {viewer.name.slice(0, 1).toUpperCase()}
            </span>
            <span className="user-name">{viewer.name}</span>
            <Button
              variant="ghost"
              size="sm"
              aria-label="退出登录"
              onClick={async () => {
                try {
                  result(await sdk.logout({ signal: requestSignal() }));
                  signOut();
                } catch (e) {
                  setToast(message(e));
                }
              }}
            >
              <LogOut size={14} />
              <span className="logout-label">退出</span>
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
                <span>刷新</span>
              </Button>
              {primary}
            </div>
          </div>
          <div className="sync-line">
            <span>
              {loading
                ? "正在同步…"
                : syncError
                  ? "连接暂不可用 · 数据可能已过时"
                  : lastSync
                    ? `已同步 ${lastSync.toLocaleTimeString("zh-CN", { hour12: false })} · 每 10 秒更新`
                    : "正在加载状态…"}
            </span>
            <span className="mono">REV {state?.revision ?? "—"}</span>
          </div>
          {syncError && (
            <Notice error>
              {syncError}
              {state ? " 当前保留上次成功同步的数据。" : ""}
            </Notice>
          )}
          {state && !state.edgeAvailable && (
            <Notice>
              Edge 状态暂不可用，在线数与活动流显示为未知。配置仍可管理。
            </Notice>
          )}
          {loading && !state ? (
            <div className="loading-state">
              <Skeleton className="h-8 w-2/5" />
              <Skeleton className="h-20 w-full" />
              <Skeleton className="h-20 w-full" />
            </div>
          ) : !state ? (
            <Empty
              title="状态尚未加载"
              description="请确认服务正常后重新加载。"
              action={<Button onClick={() => void refresh()}>重新加载</Button>}
            />
          ) : (
            {
              overview: renderOverview,
              agents: renderAgents,
              services: renderServices,
              egress: renderEgress,
              access: renderAccess,
              audit: renderAudit,
            }[page]()
          )}
          <footer className="main-footer">
            <span>agents 控制台</span>
            <span>所有操作均按当前身份执行授权检查</span>
          </footer>
        </main>
      </div>
      {form && state && (
        <ManagementDialog
          key={form.kind + ":" + (form.resource?.id || "")}
          request={form}
          state={state}
          user={viewer}
          workspace={workspace}
          onClose={() => setForm(null)}
          onComplete={complete}
        />
      )}
      {confirm && (
        <ConfirmDialog
          action={confirm}
          onClose={() => setConfirm(null)}
          onComplete={complete}
        />
      )}
      {selectedAgent && state && (
        <AgentDialog
          agent={selectedAgent}
          state={state}
          user={viewer}
          onClose={() => setSelectedAgent(null)}
          onComplete={() => complete()}
        />
      )}
      {secret && (
        <SecretDialog secret={secret} onClose={() => setSecret(null)} />
      )}
      {toast && (
        <div className="toast" role="status">
          <CheckCircle2 size={16} />
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
        await sdk.login({
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
