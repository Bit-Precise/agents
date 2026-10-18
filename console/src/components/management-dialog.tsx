import { useEffect, useState, type FormEvent } from "react";
import { Check, Copy, Download, LoaderCircle } from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type {
  Agent,
  CreateGrant,
  Membership,
  State,
  User,
} from "@/api/generated";
import { result, message, requestSignal } from "@/api/session";
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
  time,
} from "@/lib/model";

export type Secret = {
  title: string;
  subtitle: string;
  token: string;
  filename: string;
};
export type FormKind =
  "workspace" | "user" | "member" | "key" | "route" | "grant";
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
  state,
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
    ]),
    [autoApprove, setAutoApprove] = useState(false);
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
  const eligibleUsers = data.users.filter(
    (u) =>
      !u.disabled &&
      (kind !== "key" || (members.has(u.id) && (manager || u.id === user.id))),
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
      a.desired.egress.enabled,
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
  const subjectChoices =
    subjectType === "workspace"
      ? data.workspaces
          .filter((w) => !localSubjects || w.id === workspaceID)
          .map((w) => ({ value: w.id, label: w.name }))
      : data.users
          .filter((u) => !u.disabled && (!localSubjects || members.has(u.id)))
          .map((u) => ({ value: u.id, label: u.name }));
  useEffect(() => {
    if (kind === "key") setUserID(members.has(user.id) ? user.id : "");
  }, [workspaceID, kind, user.id]);
  useEffect(() => {
    if (guest) setRoles(["ingress"]);
    if (!manager) setAutoApprove(false);
  }, [guest, manager]);
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
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      let secret: Secret | undefined;
      if (["key", "member", "route", "grant"].includes(kind) && !workspaceID)
        throw new Error("请选择工作空间。");
      if (kind === "workspace")
        result(
          await sdk.createWorkspace({
            body: { name: nameValue.trim() },
            signal: requestSignal(),
          }),
        );
      if (kind === "user") {
        const response = result(
          await sdk.createUser({
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
        if (!userID) throw new Error("请选择用户。");
        result(
          await sdk.setMembership({
            body: { workspaceId: workspaceID, userId: userID, role },
            signal: requestSignal(),
          }),
        );
      }
      if (kind === "key") {
        if (!userID || !roles.length)
          throw new Error("请选择注册用户与至少一种角色。");
        if (new Date(expires) <= new Date())
          throw new Error("失效时间必须晚于当前时间。");
        const response = result(
          await sdk.createEnrollmentKey({
            body: {
              workspaceId: workspaceID,
              userId: userID,
              roles,
              autoApprove: manager && autoApprove,
              maxUses: Number(uses),
              expiresAt: new Date(expires).toISOString(),
            },
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
        result(
          await sdk.createEgressRoute({
            body: {
              workspaceId: workspaceID,
              agentId: agentID,
              name: nameValue.trim(),
              rule: {
                domain: { [match]: domain.trim() },
                ports: values,
                protocol: "tls",
              },
              priority: Number(priority),
              enabled: true,
            },
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
        result(
          await sdk.createGrant({
            body: {
              workspaceId: workspaceID,
              resourceType,
              resourceId: target.slice(colon + 1),
              subjectType,
              subjectId: subjectType === "anonymous" ? undefined : subjectID,
              startsAt: new Date(starts).toISOString(),
              expiresAt: new Date(expires).toISOString(),
            },
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
      <ChoiceSelect
        label="工作空间"
        value={workspaceID}
        onChange={changeWorkspace}
        options={workspaces.map((w) => ({ value: w.id, label: w.name }))}
      />
    </Field>
  );
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
              <ChoiceSelect
                label="用户"
                value={userID}
                onChange={setUserID}
                options={eligibleUsers.map((u) => ({
                  value: u.id,
                  label: u.name,
                }))}
              />
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
              <Field label="批准方式">
                <label className="checkbox-label">
                  <Checkbox
                    checked={autoApprove}
                    disabled={!manager}
                    onCheckedChange={(v) => setAutoApprove(v)}
                  />
                  自动批准声明范围内的能力
                </label>
                <p className="field-hint">
                  不勾选时，注册后需要管理员核对期望配置。
                </p>
              </Field>
            </>
          )}
          {kind === "route" && (
            <>
              <Field label="指定出口 Agent">
                <ChoiceSelect
                  value={agentID}
                  onChange={setAgentID}
                  label="指定出口 Agent"
                  options={eligibleAgents.map((a) => ({
                    value: a.id,
                    label: a.name,
                  }))}
                />
              </Field>
              {agentID && (
                <Notice>
                  本地声明：
                  {(
                    eligibleAgents.find((a) => a.id === agentID)?.desired.egress
                      .allow ?? []
                  )
                    .map(rule)
                    .join("；") || "无允许目标"}
                  。实际范围还需满足批准配置。
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
                  label="资源"
                  value={target}
                  onChange={(v) => {
                    setTarget(v);
                    setSubjectID("");
                  }}
                  options={targets}
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
                    <ChoiceSelect
                      value={subjectID}
                      onChange={setSubjectID}
                      label="授权对象"
                      options={subjectChoices}
                    />
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
            <Button type="submit" disabled={busy}>
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
  user,
  onClose,
  onComplete,
}: {
  agent: Agent;
  state: State;
  user: User;
  onClose: () => void;
  onComplete: () => void;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const mayApprove =
    (agent.status === "pending" || agent.pendingApproval) &&
    canManage(state, user, agent.workspaceId);
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent className="management-dialog" showCloseButton={!busy}>
        <DialogHeader>
          <span className="eyebrow">AGENT CAPABILITIES</span>
          <DialogTitle>
            {mayApprove ? "审核 Agent 能力" : "Agent 详情"}
          </DialogTitle>
          <DialogDescription>
            {agent.name} · {name(state, "workspaces", agent.workspaceId)} ·{" "}
            {name(state, "users", agent.userId)}
          </DialogDescription>
        </DialogHeader>
        <Tags items={agentRoles(agent)} />
        {agent.desired.serve.enabled && (
          <section className="review-section">
            <h3>本地服务</h3>
            {(agent.desired.serve.services ?? []).map((s) => (
              <div className="review-item" key={s.name}>
                <strong>{s.name}</strong>
                <span>{s.origin}</span>
              </div>
            ))}
          </section>
        )}
        {agent.desired.egress.enabled && (
          <section className="review-section">
            <h3>允许的出口目标</h3>
            {(agent.desired.egress.allow ?? []).length ? (
              (agent.desired.egress.allow ?? []).map((r, i) => (
                <div className="review-item" key={i}>
                  {rule(r)}
                </div>
              ))
            ) : (
              <p className="text-xs text-muted-foreground">未声明允许目标</p>
            )}
            {(agent.desired.egress.deny ?? []).length > 0 && (
              <>
                <h3>明确拒绝</h3>
                {(agent.desired.egress.deny ?? []).map((r, i) => (
                  <div className="review-item text-destructive" key={i}>
                    {rule(r)}
                  </div>
                ))}
              </>
            )}
          </section>
        )}
        {agent.desired.ingress.enabled && (
          <section className="review-section">
            <h3>本机访问范围</h3>
            {(agent.desired.ingress.routes ?? []).map((r, i) => (
              <div className="review-item" key={i}>
                {rule(r)}
              </div>
            ))}
          </section>
        )}
        <p className="text-xs text-muted-foreground">
          批准 Revision {agent.approval.revision} · 截止{" "}
          {time(agent.approval.expiresAt, true)}
        </p>
        {error && <Notice error>{error}</Notice>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            关闭
          </Button>
          {mayApprove && (
            <Button
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  result(
                    await sdk.approveAgent({
                      path: { id: agent.id },
                      signal: requestSignal(),
                    }),
                  );
                  onComplete();
                } catch (e) {
                  setError(message(e));
                } finally {
                  setBusy(false);
                }
              }}
            >
              {busy && <LoaderCircle className="animate-spin" />}批准声明的能力
            </Button>
          )}
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
