import { useState } from "react";
import { toast } from "sonner";
import * as sdk from "@/api/generated/sdk.gen";
import type { CreateWorkspaceGrant } from "@/api/generated";
import { requestSignal, result } from "@/api/session";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { OptionSelect } from "@/components/data-table";
import { ResourcePicker } from "@/components/resource-picker";
import { FormDialog } from "./form-dialog";
import { localTime, rule } from "@/lib/format";
import { useUser } from "@/lib/session";

export function RouteDialog({
  workspaceId,
  onClose,
  onDone,
}: {
  workspaceId: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const [agentID, setAgentID] = useState("");
  const [agentName, setAgentName] = useState("");
  const [scope, setScope] = useState("");
  const [name, setName] = useState("");
  const [match, setMatch] = useState("exact");
  const [domain, setDomain] = useState("");
  const [ports, setPorts] = useState("443");
  const [priority, setPriority] = useState("100");
  return (
    <FormDialog
      title="创建出口路由"
      description="把获准的 TLS 域名交给指定的 Egress 设备。创建后还需要添加使用授权。"
      submitLabel="创建"
      wide
      onClose={onClose}
      onSubmit={async () => {
        if (!agentID) throw new Error("请选择出口设备。");
        const values = ports.split(/[,，]/).map((v) => Number(v.trim()));
        if (
          values.some((v) => !Number.isInteger(v) || v < 1 || v > 65535) ||
          new Set(values).size !== values.length
        )
          throw new Error("端口必须是 1–65535 的整数且不能重复。");
        result(
          await sdk.createWorkspaceRoute({
            path: { workspaceId },
            body: {
              kind: "egress",
              agentId: agentID,
              name: name.trim(),
              rule: {
                domain: { [match]: domain.trim() },
                ports: values,
                protocol: "tls",
              },
              priority: Number(priority),
            },
            signal: requestSignal(),
          }),
        );
        toast.success("出口路由已创建");
        onClose();
        onDone();
      }}
    >
      <Field>
        <FieldLabel>出口设备</FieldLabel>
        <ResourcePicker
          value={agentID}
          valueLabel={agentName}
          placeholder="选择 Egress 设备"
          onSelect={(c) => {
            setAgentID(c.id);
            setAgentName(c.label);
            setScope(c.description ?? "");
          }}
          search={async (q, signal) =>
            result(
              await sdk.listWorkspaceAgents({
                path: { workspaceId },
                query: {
                  q: q || undefined,
                  status: "active",
                  pageSize: 50,
                  sortBy: "name",
                },
                signal,
              }),
            )
              .items.filter((a) => (a.effective.egress ?? []).length > 0)
              .map((a) => ({
                id: a.id,
                label: a.name,
                description: (a.effective.egress ?? []).map(rule).join("，"),
              }))
          }
        />
        {scope && (
          <FieldDescription>
            生效的出口范围：{scope}。路由只能使用此范围内的目标。
          </FieldDescription>
        )}
      </Field>
      <Field>
        <FieldLabel htmlFor="route-name">规则名称</FieldLabel>
        <Input
          id="route-name"
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="例如 company-oa"
        />
      </Field>
      <div className="grid grid-cols-[10rem_1fr] gap-4">
        <Field>
          <FieldLabel htmlFor="route-match">匹配方式</FieldLabel>
          <OptionSelect
            id="route-match"
            label="匹配方式"
            value={match}
            onChange={setMatch}
            className="w-full"
            options={[
              { value: "exact", label: "精确域名" },
              { value: "suffix", label: "域名及子域" },
            ]}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="route-domain">目标域名</FieldLabel>
          <Input
            id="route-domain"
            required
            value={domain}
            onChange={(e) => setDomain(e.target.value)}
            placeholder="oa.example.com"
          />
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-4">
        <Field>
          <FieldLabel htmlFor="route-ports">TCP 端口</FieldLabel>
          <Input
            id="route-ports"
            required
            value={ports}
            onChange={(e) => setPorts(e.target.value)}
          />
          <FieldDescription>多个端口以逗号分隔。</FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="route-priority">优先级</FieldLabel>
          <Input
            id="route-priority"
            type="number"
            min={0}
            max={1000000}
            required
            value={priority}
            onChange={(e) => setPriority(e.target.value)}
          />
        </Field>
      </div>
    </FormDialog>
  );
}

type Subject = CreateWorkspaceGrant["subjectType"];

export function GrantDialog({
  workspaceId,
  resource,
  manager,
  onClose,
  onDone,
}: {
  workspaceId: string;
  resource?: { type: "service" | "egress"; id: string; label: string };
  manager: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const viewer = useUser();
  const [resourceType, setResourceType] = useState<"service" | "egress">(
    resource?.type ?? "service",
  );
  const [resourceID, setResourceID] = useState(resource?.id ?? "");
  const [resourceLabel, setResourceLabel] = useState(resource?.label ?? "");
  const [subjectType, setSubjectType] = useState<Subject>("user");
  const [subjectID, setSubjectID] = useState("");
  const [subjectLabel, setSubjectLabel] = useState("");
  const [starts, setStarts] = useState(localTime());
  const [expires, setExpires] = useState(localTime(24 * 7));
  const subjects: { value: Subject; label: string; disabled?: boolean }[] = [
    { value: "user", label: "指定用户" },
    { value: "workspace", label: "工作空间成员" },
    {
      value: "service_account",
      label: "服务账号（仅出口）",
      disabled: resourceType !== "egress",
    },
    {
      value: "anonymous",
      label: "匿名访问者（仅服务）",
      disabled: resourceType !== "service" || !manager,
    },
  ];
  return (
    <FormDialog
      title="添加访问授权"
      description="授权只针对所选资源，在有效期内生效；多条授权取并集。"
      submitLabel="添加"
      wide
      onClose={onClose}
      onSubmit={async () => {
        const subject = subjectType === "workspace" ? workspaceId : subjectID;
        if (!resourceID || (!subject && subjectType !== "anonymous"))
          throw new Error("请选择资源与授权对象。");
        if (
          new Date(expires) <= new Date(starts) ||
          new Date(expires) <= new Date()
        )
          throw new Error("截止时间必须晚于开始时间和当前时间。");
        result(
          await sdk.createWorkspaceGrant({
            path: { workspaceId },
            body: {
              resourceType,
              resourceId: resourceID,
              subjectType,
              subjectId: subjectType === "anonymous" ? undefined : subject,
              startsAt: new Date(starts).toISOString(),
              expiresAt: new Date(expires).toISOString(),
            },
            signal: requestSignal(),
          }),
        );
        toast.success("授权已添加");
        onClose();
        onDone();
      }}
    >
      <div className="grid grid-cols-[10rem_1fr] gap-4">
        <Field>
          <FieldLabel htmlFor="grant-resource-type">资源类型</FieldLabel>
          <OptionSelect
            id="grant-resource-type"
            label="资源类型"
            value={resourceType}
            disabled={!!resource}
            className="w-full"
            onChange={(v) => {
              setResourceType(v as "service" | "egress");
              setResourceID("");
              setSubjectType("user");
            }}
            options={[
              { value: "service", label: "服务" },
              { value: "egress", label: "出口路由" },
            ]}
          />
        </Field>
        <Field>
          <FieldLabel>资源</FieldLabel>
          <ResourcePicker
            value={resourceID}
            valueLabel={resourceLabel}
            placeholder={
              resourceType === "service" ? "选择服务" : "选择出口路由"
            }
            disabled={!!resource}
            onSelect={(c) => {
              setResourceID(c.id);
              setResourceLabel(c.label);
            }}
            search={async (q, signal) =>
              resourceType === "service"
                ? result(
                    await sdk.listWorkspaceServers({
                      path: { workspaceId },
                      query: {
                        q: q || undefined,
                        enabled: true,
                        pageSize: 20,
                        sortBy: "name",
                      },
                      signal,
                    }),
                  ).items.map((s) => ({
                    id: s.serviceId,
                    label: s.name,
                    description: s.hostname,
                  }))
                : result(
                    await sdk.listWorkspaceRoutes({
                      path: { workspaceId },
                      query: {
                        q: q || undefined,
                        kind: "egress",
                        enabled: true,
                        pageSize: 20,
                        sortBy: "name",
                      },
                      signal,
                    }),
                  ).items.map((r) => ({
                    id: r.id,
                    label: r.name,
                    description: rule({ ...r.rule, protocol: "tls" }),
                  }))
            }
          />
        </Field>
      </div>
      <div className="grid grid-cols-[10rem_1fr] gap-4">
        <Field>
          <FieldLabel htmlFor="grant-subject-type">授权对象</FieldLabel>
          <OptionSelect
            id="grant-subject-type"
            label="授权对象类型"
            value={subjectType}
            className="w-full"
            onChange={(v) => {
              setSubjectType(v as Subject);
              setSubjectID("");
            }}
            options={subjects}
          />
        </Field>
        <Field>
          <FieldLabel>
            {subjectType === "anonymous" ? "范围" : "对象"}
          </FieldLabel>
          {subjectType === "anonymous" ? (
            <Input readOnly value="任何人，无需登录" />
          ) : subjectType === "workspace" ? (
            <Input readOnly value="本工作空间的全部成员（不含服务账号）" />
          ) : (
            <ResourcePicker
              value={subjectID}
              valueLabel={subjectLabel}
              placeholder={subjectType === "user" ? "选择用户" : "选择服务账号"}
              onSelect={(c) => {
                setSubjectID(c.id);
                setSubjectLabel(c.label);
              }}
              search={async (q, signal) =>
                subjectType === "user"
                  ? result(
                      viewer.admin && manager
                        ? await sdk.listAdminUsers({
                            query: {
                              q: q || undefined,
                              disabled: false,
                              pageSize: 20,
                              sortBy: "name",
                            },
                            signal,
                          })
                        : await sdk.listWorkspaceUsers({
                            path: { workspaceId },
                            query: {
                              q: q || undefined,
                              disabled: false,
                              pageSize: 20,
                              sortBy: "name",
                            },
                            signal,
                          }),
                    ).items.map((u) => ({
                      id: u.id,
                      label: u.name,
                      description: u.email || u.id,
                    }))
                  : result(
                      await sdk.listWorkspaceServiceAccounts({
                        path: { workspaceId },
                        query: {
                          q: q || undefined,
                          status: "active",
                          pageSize: 20,
                          sortBy: "name",
                        },
                        signal,
                      }),
                    ).items.map((s) => ({
                      id: s.id,
                      label: s.name,
                      description: s.description,
                    }))
              }
            />
          )}
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-4">
        <Field>
          <FieldLabel htmlFor="grant-starts">开始时间</FieldLabel>
          <Input
            id="grant-starts"
            type="datetime-local"
            required
            value={starts}
            onChange={(e) => setStarts(e.target.value)}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="grant-expires">截止时间</FieldLabel>
          <Input
            id="grant-expires"
            type="datetime-local"
            required
            value={expires}
            onChange={(e) => setExpires(e.target.value)}
          />
        </Field>
      </div>
    </FormDialog>
  );
}
