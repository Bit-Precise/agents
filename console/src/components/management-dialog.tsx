import { ResourceSelect } from "./resource-select";
import { addServers, addRoutes, mergeBy } from "@/lib/load-view";
import type { ServerView, RouteView } from "@/api/generated";
import type { ConsoleView as State } from "@/lib/view";
import { useEffect, useState, type FormEvent } from "react";
import { Check, Copy, Download, LoaderCircle } from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type { Agent, CreateGrant, Membership, User } from "@/api/generated";
import { result, message, requestSignal, APIFailure } from "@/api/session";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ChoiceSelect, Field, Notice, Tags } from "./controls";
import {
  agentRoles,
  canEnroll,
  canGrant,
  canManage,
  canManageResource,
  localTime,
  name,
  roleNames,
  rows,
  rule,
} from "@/lib/model";

export type Secret = {
  title: string;
  subtitle: string;
  token: string;
  filename: string;
};
export type FormKind =
  | "workspace"
  | "user"
  | "member"
  | "key"
  | "route"
  | "grant";
export type FormRequest = {
  kind: FormKind;
  resource?: { id: string; workspaceId: string; type: "service" | "egress" };
};
export type ConfirmAction = {
  title: string;
  description: string;
  label: string;
  danger?: boolean;
  perform: () => Promise<Secret | void>;
};
const titles: Record<FormKind, string> = {
  workspace: "创建工作空间",
  user: "创建用户",
  member: "添加工作空间成员",
  key: "创建设备注册密钥",
  route: "创建出口路由",
  grant: "添加访问授权",
};
const descriptions: Record<FormKind, string> = {
  workspace: "为成员、设备与服务建立独立的管理空间。",
  user: "用户可以加入多个工作空间；新的登录令牌仅在签发后显示一次。",
  member: "成员角色控制管理权限，业务访问仍需要有效资源授权。",
  key: "注册凭据限定设备可声明的角色，不授予访问他人服务的权限。",
  route: "将获准 TLS 域名交给指定出口。创建规则后还需要添加使用授权。",
  grant: "授权只针对所选资源；已有登录状态不会延长其有效期限。",
};

export function ManagementDialog({
  request,
  state: initialState,
  user,
  workspace,
  onClose,
  onComplete,
}: {
  request: FormRequest;
  state: State;
  user: User;
  workspace: string;
  onClose: () => void;
  onComplete: (secret?: Secret) => void;
}) {
  const [state, setState] = useState(initialState);
  const [contextLoading, setContextLoading] = useState(false);
  const [resourceKind, setResourceKind] = useState<"service" | "egress">(
    request.resource?.type || "service",
  );
  const data = rows(state),
    kind = request.kind;
  const permission = (id: string) =>
    kind === "key"
      ? canEnroll(state, user, id)
      : kind === "grant"
        ? canGrant(state, user, id)
        : canManage(state, user, id);
  const workspaces = data.workspaces.filter((w) => permission(w.id));
  const [workspaceID, setWorkspaceID] = useState(
    request.resource?.workspaceId ||
      workspace ||
      (workspaces.length === 1 ? workspaces[0].id : ""),
  );
  const [nameValue, setName] = useState(""),
    [userID, setUserID] = useState(""),
    [role, setRole] = useState<Membership["role"]>("developer");
  const [roles, setRoles] = useState<Array<"ingress" | "serve" | "egress">>([
    "ingress",
  ]);
  const [uses, setUses] = useState("1"),
    [expires, setExpires] = useState(localTime(24)),
    [starts, setStarts] = useState(localTime());
  const [agentID, setAgentID] = useState(""),
    [domain, setDomain] = useState(""),
    [match, setMatch] = useState("exact"),
    [ports, setPorts] = useState("443"),
    [priority, setPriority] = useState("100");
  const [target, setTarget] = useState(
      request.resource ? request.resource.type + ":" + request.resource.id : "",
    ),
    [subjectType, setSubjectType] =
      useState<CreateGrant["subjectType"]>("user"),
    [subjectID, setSubjectID] = useState("");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const manager = canManage(state, user, workspaceID);
  const members = new Set(
    data.memberships
      .filter((m) => m.workspaceId === workspaceID)
      .map((m) => m.userId),
  );
  const guest = data.memberships.some(
    (m) =>
      m.workspaceId === workspaceID &&
      m.userId === userID &&
      m.role === "guest",
  );
  const eligibleAgents = data.agents.filter(
    (a) =>
      a.workspaceId === workspaceID &&
      a.status === "active" &&
      (a.effective.egress ?? []).length > 0,
  );
  const targets = [
    ...data.services
      .filter(
        (s) =>
          s.enabled &&
          s.workspaceId === workspaceID &&
          canManageResource(state, user, "service", s.id),
      )
      .map((s) => ({ value: "service:" + s.id, label: "服务 · " + s.name })),
    ...data.egressRoutes
      .filter(
        (r) =>
          r.enabled &&
          r.workspaceId === workspaceID &&
          canManageResource(state, user, "egress", r.id),
      )
      .map((r) => ({ value: "egress:" + r.id, label: "出口 · " + r.name })),
  ];
  const localSubjects = target.startsWith("egress:") || !manager;
  useEffect(() => {
    if (kind === "key") setUserID(members.has(user.id) ? user.id : "");
  }, [workspaceID, kind, user.id]);
  useEffect(() => {
    if (guest) setRoles(["ingress"]);
  }, [guest]);
  useEffect(() => {
    if (localSubjects && subjectType === "anonymous") setSubjectType("user");
  }, [localSubjects, subjectType]);
  function changeWorkspace(value: string) {
    setWorkspaceID(value);
    setAgentID("");
    setTarget("");
    setSubjectID("");
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy || contextLoading) return;
    setBusy(true);
    setError("");
    try {
      let secret: Secret | undefined;
      if (["key", "member", "route", "grant"].includes(kind) && !workspaceID)
        throw new Error("请选择工作空间。");
      if (kind === "workspace")
        result(
          await sdk.createAdminWorkspace({
            body: { name: nameValue.trim() },
            signal: requestSignal(),
          }),
        );
      if (kind === "user") {
        const response = result(
          await sdk.createAdminUser({
            body: { name: nameValue.trim() },
            signal: requestSignal(),
          }),
        );
        secret = {
          title: "用户已创建",
          subtitle: response.user.name,
          token: response.token,
          filename: "agents-user-token.txt",
        };
      }
      if (kind === "member") {
        if (!userID) throw new Error("请选择或填写用户 ID。");
        result(
          user.admin
            ? await sdk.putAdminMembership({
                path: { workspaceId: workspaceID, userId: userID.trim() },
                body: { role },
                signal: requestSignal(),
              })
            : await sdk.putMembership({
                path: { workspaceId: workspaceID, userId: userID.trim() },
                body: { role },
                signal: requestSignal(),
              }),
        );
      }
      if (kind === "key") {
        if (!userID || !roles.length)
          throw new Error("请选择注册用户与至少一种角色。");
        if (new Date(expires) <= new Date())
          throw new Error("失效时间必须晚于当前时间。");
        const body = {
          userId: userID,
          roles,
          maxUses: Number(uses),
          expiresAt: new Date(expires).toISOString(),
        };
        const response = result(
          user.admin
            ? await sdk.createAdminEnrollmentKey({
                body: { ...body, workspaceId: workspaceID },
                signal: requestSignal(),
              })
            : await sdk.createWorkspaceEnrollmentKey({
                path: { workspaceId: workspaceID },
                body,
                signal: requestSignal(),
              }),
        );
        secret = {
          title: "注册密钥已生成",
          subtitle:
            name(state, "workspaces", workspaceID) +
            " · " +
            name(state, "users", userID),
          token: response.token,
          filename: "agents-enrollment-key.txt",
        };
      }
      if (kind === "route") {
        if (!agentID) throw new Error("请选择已批准的出口 Agent。");
        const values = ports.split(/[,，]/).map((v) => Number(v.trim()));
        if (
          values.some((v) => !Number.isInteger(v) || v < 1 || v > 65535) ||
          new Set(values).size !== values.length
        )
          throw new Error("端口必须是 1–65535 的整数且不能重复。");
        const body = {
          kind: "egress" as const,
          agentId: agentID,
          name: nameValue.trim(),
          rule: {
            domain: { [match]: domain.trim() },
            ports: values,
            protocol: "tls" as const,
          },
          priority: Number(priority),
        };
        result(
          user.admin
            ? await sdk.createAdminRoute({
                body: { ...body, workspaceId: workspaceID },
                signal: requestSignal(),
              })
            : await sdk.createWorkspaceRoute({
                path: { workspaceId: workspaceID },
                body,
                signal: requestSignal(),
              }),
        );
      }
      if (kind === "grant") {
        if (!target || (!subjectID && subjectType !== "anonymous"))
          throw new Error("请选择资源与授权对象。");
        if (
          new Date(expires) <= new Date(starts) ||
          new Date(expires) <= new Date()
        )
          throw new Error("截止时间必须晚于开始时间和当前时间。");
        const colon = target.indexOf(":");
        const resourceType = target.slice(0, colon) as "service" | "egress";
        const body = {
          resourceType,
          resourceId: target.slice(colon + 1),
          subjectType,
          subjectId: subjectType === "anonymous" ? undefined : subjectID.trim(),
          startsAt: new Date(starts).toISOString(),
          expiresAt: new Date(expires).toISOString(),
        };
        result(
          user.admin
            ? await sdk.createAdminGrant({
                body: { ...body, workspaceId: workspaceID },
                signal: requestSignal(),
              })
            : await sdk.createWorkspaceGrant({
                path: { workspaceId: workspaceID },
                body,
                signal: requestSignal(),
              }),
        );
      }
      onComplete(secret);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  const workspaceField = (
    <Field label="工作空间">
      <ResourceSelect
        resource="workspaces"
        user={user}
        label="工作空间"
        value={workspaceID}
        valueLabel={data.workspaces.find((w) => w.id === workspaceID)?.name}
        onSelect={(w) => {
          setState((s) => ({
            ...s,
            workspaces: mergeBy(
              s.workspaces,
              [w as unknown as (typeof s.workspaces)[number]],
              (x) => x.id,
            ),
          }));
          changeWorkspace(w.id);
        }}
      />
    </Field>
  );
  useEffect(() => {
    if (!workspaceID || !["key", "member", "grant", "route"].includes(kind))
      return;
    const abort = new AbortController();
    setContextLoading(true);
    const ids = [
      ...new Set([user.id, ...(kind === "key" && userID ? [userID] : [])]),
    ];
    void Promise.allSettled(
      ids.map(async (id) => {
        const response = user.admin
          ? await sdk.getAdminMembership({
              path: { workspaceId: workspaceID, userId: id },
              signal: abort.signal,
            })
          : await sdk.getMembership({
              path: { workspaceId: workspaceID, userId: id },
              signal: abort.signal,
            });
        const m = result(response);
        return m;
      }),
    ).then((outcomes) => {
      if (abort.signal.aborted) return;
      const memberships = outcomes.flatMap((x) =>
        x.status === "fulfilled" ? [x.value] : [],
      );
      setState((s) => ({
        ...s,
        memberships: [
          ...s.memberships.filter(
            (m) => m.workspaceId !== workspaceID || !ids.includes(m.userId),
          ),
          ...memberships,
        ],
      }));
      const failed = outcomes.find(
        (x) =>
          x.status === "rejected" &&
          !(x.reason instanceof APIFailure && x.reason.status === 404),
      );
      if (failed?.status === "rejected") setError(message(failed.reason));
      setContextLoading(false);
    });
    return () => abort.abort();
  }, [workspaceID, userID, kind, user.id, user.admin]);
  useEffect(() => {
    if (kind === "key" && !userID && members.has(user.id)) setUserID(user.id);
  }, [kind, workspaceID, state.memberships, userID, user.id]);
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent className="management-dialog" showCloseButton={!busy}>
        <DialogHeader>
          <span className="eyebrow">MANAGE WORKSPACE</span>
          <DialogTitle>{titles[kind]}</DialogTitle>
          <DialogDescription>{descriptions[kind]}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit}>
          {["workspace", "user"].includes(kind) && (
            <Field
              label={kind === "workspace" ? "工作空间名称" : "用户名称"}
              id="form-name"
            >
              <Input
                id="form-name"
                required
                autoFocus
                maxLength={200}
                value={nameValue}
                onChange={(e) => setName(e.target.value)}
                placeholder={
                  kind === "workspace" ? "例如 design-team" : "输入用户名称"
                }
              />
            </Field>
          )}
          {!["workspace", "user"].includes(kind) && workspaceField}
          {["member", "key"].includes(kind) && (
            <Field
              label={kind === "key" ? "注册用户" : "用户"}
              hint={kind === "key" ? "用户需要先加入所选工作空间。" : undefined}
            >
              <ResourceSelect
                resource="users"
                user={user}
                workspace={kind === "key" || !user.admin ? workspaceID : ""}
                filters={{ disabled: false }}
                label="用户"
                value={userID}
                valueLabel={data.users.find((u) => u.id === userID)?.name}
                disabled={!workspaceID}
                onSelect={(u) => {
                  setState((s) => ({
                    ...s,
                    users: mergeBy(
                      s.users,
                      [u as unknown as User],
                      (x) => x.id,
                    ),
                  }));
                  setUserID(u.id);
                }}
              />
              {kind === "member" && (
                <Input
                  aria-label="已知用户 ID"
                  placeholder="也可填写已知用户 ID"
                  value={userID}
                  onChange={(e) => setUserID(e.target.value)}
                />
              )}
            </Field>
          )}
          {kind === "member" && (
            <Field label="角色">
              <ChoiceSelect
                label="角色"
                value={role}
                onChange={(v) => setRole(v as Membership["role"])}
                options={["developer", "guest", "maintainer", "owner"].map(
                  (value) => ({ value, label: roleNames[value] }),
                )}
              />
            </Field>
          )}
          {kind === "key" && (
            <>
              <Field label="允许注册的角色">
                <div className="checkbox-row">
                  {(["ingress", "serve", "egress"] as const).map((value) => (
                    <label className="checkbox-label" key={value}>
                      <Checkbox
                        checked={roles.includes(value)}
                        disabled={guest && value !== "ingress"}
                        onCheckedChange={(checked) =>
                          setRoles((previous) =>
                            checked
                              ? [...previous, value]
                              : previous.filter((r) => r !== value),
                          )
                        }
                      />
                      {roleNames[value]}
                    </label>
                  ))}
                </div>
              </Field>
              <div className="form-grid">
                <Field label="最多使用次数" id="key-uses">
                  <Input
                    id="key-uses"
                    type="number"
                    min={1}
                    max={10000}
                    required
                    value={uses}
                    onChange={(e) => setUses(e.target.value)}
                  />
                </Field>
                <Field label="失效时间" id="key-expires">
                  <Input
                    id="key-expires"
                    type="datetime-local"
                    required
                    value={expires}
                    onChange={(e) => setExpires(e.target.value)}
                  />
                </Field>
              </div>
              <p className="field-hint">
                注册无需人工审批：Control 按注册角色和成员角色自动校验设备声明，通过即生效，越权或冲突直接拒绝。
              </p>
            </>
          )}
          {kind === "route" && (
            <>
              <Field label="指定出口 Agent">
                <ResourceSelect
                  resource="agents"
                  user={user}
                  workspace={workspaceID}
                  filters={{ status: "active" }}
                  label="出口 Agent"
                  value={agentID}
                  valueLabel={data.agents.find((a) => a.id === agentID)?.name}
                  disabled={!workspaceID}
                  onSelect={(value) => {
                    const agent = value as unknown as Agent;
                    if (!(agent.effective.egress ?? []).length) {
                      setError("此设备的生效配置没有出口能力，请选择出口提供方。");
                      return;
                    }
                    setState((s) => ({
                      ...s,
                      agents: mergeBy(s.agents, [agent], (x) => x.id),
                    }));
                    setAgentID(agent.id);
                  }}
                />
              </Field>
              {agentID && (
                <Notice>
                  生效的出口范围：
                  {(
                    eligibleAgents.find((a) => a.id === agentID)?.effective
                      .egress ?? []
                  )
                    .map(rule)
                    .join("；") || "无允许目标"}
                  。路由只能使用此范围内的目标。
                </Notice>
              )}
              <Field label="规则名称" id="route-name">
                <Input
                  id="route-name"
                  value={nameValue}
                  onChange={(e) => setName(e.target.value)}
                  required
                  placeholder="例如 company-oa"
                />
              </Field>
              <div className="form-grid">
                <Field label="匹配方式">
                  <ChoiceSelect
                    label="匹配方式"
                    value={match}
                    onChange={setMatch}
                    options={[
                      { value: "exact", label: "精确域名" },
                      { value: "suffix", label: "域名及其子域" },
                    ]}
                  />
                </Field>
                <Field label="目标域名" id="route-domain">
                  <Input
                    id="route-domain"
                    value={domain}
                    onChange={(e) => setDomain(e.target.value)}
                    required
                    placeholder="oa.example.com"
                  />
                </Field>
              </div>
              <div className="form-grid">
                <Field
                  label="TCP 端口"
                  id="route-ports"
                  hint="多个端口以逗号分隔。"
                >
                  <Input
                    id="route-ports"
                    value={ports}
                    onChange={(e) => setPorts(e.target.value)}
                    required
                  />
                </Field>
                <Field label="优先级" id="route-priority">
                  <Input
                    id="route-priority"
                    type="number"
                    min={0}
                    max={1000000}
                    value={priority}
                    onChange={(e) => setPriority(e.target.value)}
                    required
                  />
                </Field>
              </div>
            </>
          )}
          {kind === "grant" && (
            <>
              <Field label="服务或出口规则">
                <ChoiceSelect
                  label="资源类型"
                  value={resourceKind}
                  onChange={(v) => {
                    setResourceKind(v as "service" | "egress");
                    setTarget("");
                  }}
                  options={[
                    { value: "service", label: "发布服务" },
                    { value: "egress", label: "出口路由" },
                  ]}
                />
                <ResourceSelect
                  resource={resourceKind === "service" ? "servers" : "routes"}
                  user={user}
                  workspace={workspaceID}
                  filters={{
                    enabled: true,
                    ...(resourceKind === "egress" ? { kind: "egress" } : {}),
                  }}
                  label="资源"
                  value={target.split(":")[1] || ""}
                  valueLabel={targets.find((t) => t.value === target)?.label}
                  disabled={!workspaceID}
                  onSelect={(value) => {
                    setState((s) => {
                      const next = { ...s };
                      if (resourceKind === "service")
                        addServers(next, [value as unknown as ServerView]);
                      else addRoutes(next, [value as unknown as RouteView]);
                      return next;
                    });
                    setTarget(resourceKind + ":" + value.id);
                    setSubjectID("");
                  }}
                />
              </Field>
              <div className="form-grid">
                <Field label="授权对象类型">
                  <ChoiceSelect
                    label="授权对象类型"
                    value={subjectType}
                    onChange={(v) => {
                      setSubjectType(v as CreateGrant["subjectType"]);
                      setSubjectID("");
                    }}
                    options={[
                      { value: "user", label: "指定用户" },
                      { value: "workspace", label: "Workspace 成员" },
                      {
                        value: "anonymous",
                        label: "匿名访问者",
                        disabled: localSubjects,
                      },
                    ]}
                  />
                </Field>
                <Field label="授权对象">
                  {subjectType === "anonymous" ? (
                    <Input value="任何人，无需登录" readOnly />
                  ) : (
                    <div className="space-y-2">
                      {subjectType === "workspace" && localSubjects ? (
                        <Button
                          type="button"
                          variant="outline"
                          onClick={() => setSubjectID(workspaceID)}
                        >
                          {name(state, "workspaces", workspaceID)}
                          {subjectID === workspaceID ? " ✓" : ""}
                        </Button>
                      ) : (
                        <ResourceSelect
                          resource={
                            subjectType === "workspace" ? "workspaces" : "users"
                          }
                          user={user}
                          workspace={
                            subjectType === "user" &&
                            (localSubjects || !user.admin)
                              ? workspaceID
                              : ""
                          }
                          filters={
                            subjectType === "user" ? { disabled: false } : {}
                          }
                          label="授权对象"
                          value={subjectID}
                          onSelect={(value) => setSubjectID(value.id)}
                        />
                      )}{" "}
                      {!localSubjects && (
                        <Input
                          aria-label="已知授权对象 ID"
                          placeholder="也可填写已知对象 ID"
                          value={subjectID}
                          onChange={(e) => setSubjectID(e.target.value)}
                        />
                      )}
                    </div>
                  )}
                </Field>
              </div>
              <div className="form-grid">
                <Field label="开始时间" id="grant-starts">
                  <Input
                    id="grant-starts"
                    type="datetime-local"
                    required
                    value={starts}
                    onChange={(e) => setStarts(e.target.value)}
                  />
                </Field>
                <Field label="截止时间" id="grant-expires">
                  <Input
                    id="grant-expires"
                    type="datetime-local"
                    required
                    value={expires}
                    onChange={(e) => setExpires(e.target.value)}
                  />
                </Field>
              </div>
            </>
          )}
          {error && <Notice error>{error}</Notice>}
          <DialogFooter className="form-footer">
            <Button
              type="button"
              variant="outline"
              onClick={onClose}
              disabled={busy}
            >
              取消
            </Button>
            <Button type="submit" disabled={busy || contextLoading}>
              {busy && <LoaderCircle className="animate-spin" />}
              {busy
                ? "正在提交…"
                : kind === "key"
                  ? "生成注册密钥"
                  : "确认保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function ConfirmDialog({
  action,
  onClose,
  onComplete,
}: {
  action: ConfirmAction;
  onClose: () => void;
  onComplete: (secret?: Secret) => void;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent showCloseButton={!busy}>
        <DialogHeader>
          <DialogTitle>{action.title}</DialogTitle>
          <DialogDescription>{action.description}</DialogDescription>
        </DialogHeader>
        {error && <Notice error>{error}</Notice>}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            取消
          </Button>
          <Button
            variant={action.danger ? "destructive" : "default"}
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              setError("");
              try {
                const secret = await action.perform();
                onComplete(secret || undefined);
              } catch (e) {
                setError(message(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            {busy && <LoaderCircle className="animate-spin" />}
            {action.label}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function AgentDialog({
  agent,
  state,
  onClose,
}: {
  agent: Agent;
  state: State;
  user: User;
  onClose: () => void;
  onComplete: () => void;
}) {
  const effective = agent.effective;
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="management-dialog">
        <DialogHeader>
          <span className="eyebrow">AGENT CONFIGURATION</span>
          <DialogTitle>Agent 详情</DialogTitle>
          <DialogDescription>
            {agent.name} · {name(state, "workspaces", agent.workspaceId)} ·{" "}
            {name(state, "users", agent.userId)}
          </DialogDescription>
        </DialogHeader>
        <Tags items={agentRoles(agent)} />
        {agent.lastRejection && (
          <Notice error>
            最近一次配置更新被自动拒绝（{agent.lastRejection.kind}）：
            {agent.lastRejection.message}。设备继续使用下方的生效配置。
          </Notice>
        )}
        <section className="review-section">
          <h3>生效的服务</h3>
          {(effective.services ?? []).length ? (
            (effective.services ?? []).map((s) => (
              <div className="review-item" key={s.name}>
                <strong>{s.name}</strong>
                <span>
                  {s.hostname} → {s.origin}
                </span>
              </div>
            ))
          ) : (
            <p className="text-xs text-muted-foreground">无</p>
          )}
        </section>
        <section className="review-section">
          <h3>生效的出口目标</h3>
          {(effective.egress ?? []).length ? (
            (effective.egress ?? []).map((r, i) => (
              <div className="review-item" key={i}>
                {rule(r)}
              </div>
            ))
          ) : (
            <p className="text-xs text-muted-foreground">无</p>
          )}
          {(effective.deny ?? []).length > 0 && (
            <>
              <h3>明确拒绝</h3>
              {(effective.deny ?? []).map((r, i) => (
                <div className="review-item text-destructive" key={i}>
                  {rule(r)}
                </div>
              ))}
            </>
          )}
        </section>
        <p className="text-xs text-muted-foreground">
          本机访问（ingress）：{effective.ingress ? "已生效" : "未生效"} ·
          生效 Revision {effective.revision}
          {effective.policy ? ` · 策略 ${effective.policy}` : ""}
        </p>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            关闭
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function SecretDialog({
  secret,
  onClose,
}: {
  secret: Secret;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false),
    [error, setError] = useState("");
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="management-dialog">
        <DialogHeader>
          <DialogTitle>{secret.title}</DialogTitle>
          <DialogDescription>{secret.subtitle}</DialogDescription>
        </DialogHeader>
        <Notice>
          完整令牌仅显示这一次。关闭后无法再次查看，请妥善保存并仅交给指定使用者。
        </Notice>
        <textarea
          readOnly
          aria-label="一次性显示的令牌"
          className="token-box"
          value={secret.token}
          spellCheck={false}
        />
        <div className="flex gap-2">
          <Button
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(secret.token);
                setCopied(true);
              } catch {
                setError("自动复制不可用，请手动选中令牌复制。");
              }
            }}
          >
            {copied ? <Check /> : <Copy />}
            {copied ? "已复制" : "复制令牌"}
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              const url = URL.createObjectURL(
                new Blob([secret.token + "\n"], {
                  type: "text/plain;charset=utf-8",
                }),
              );
              const link = document.createElement("a");
              link.href = url;
              link.download = secret.filename;
              link.click();
              setTimeout(() => URL.revokeObjectURL(url), 1000);
            }}
          >
            <Download />
            下载令牌
          </Button>
        </div>
        {error && <Notice error>{error}</Notice>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            已保存，关闭
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
