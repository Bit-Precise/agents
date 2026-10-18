import { useState } from "react";
import { toast } from "sonner";
import * as sdk from "@/api/generated/sdk.gen";
import type { ServiceAccount } from "@/api/generated";
import { requestSignal, result } from "@/api/session";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Field,
  FieldDescription,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { ResourcePicker } from "@/components/resource-picker";
import { useFeedback } from "@/components/feedback";
import { FormDialog } from "./form-dialog";
import { localTime, roleNames } from "@/lib/format";
import { useUser } from "@/lib/session";

export function ServiceAccountDialog({
  workspaceId,
  account,
  onClose,
  onDone,
}: {
  workspaceId: string;
  account?: ServiceAccount;
  onClose: () => void;
  onDone: () => void;
}) {
  const viewer = useUser();
  const [name, setName] = useState(account?.name ?? "");
  const [description, setDescription] = useState(account?.description ?? "");
  return (
    <FormDialog
      title={account ? "编辑服务账号" : "创建服务账号"}
      description="服务账号是工作空间内的机器身份。设备以它运行，不随创建者或操作人离开而失效。"
      submitLabel={account ? "保存" : "创建"}
      onClose={onClose}
      onSubmit={async () => {
        const signal = requestSignal();
        if (account) {
          const body = { description: description.trim() };
          result(
            viewer.admin
              ? await sdk.updateAdminServiceAccount({
                  path: { id: account.id },
                  body,
                  signal,
                })
              : await sdk.updateWorkspaceServiceAccount({
                  path: { workspaceId, serviceAccountId: account.id },
                  body,
                  signal,
                }),
          );
        } else {
          result(
            await sdk.createWorkspaceServiceAccount({
              path: { workspaceId },
              body: {
                name: name.trim(),
                description: description.trim() || undefined,
              },
              signal,
            }),
          );
        }
        toast.success(account ? "已保存" : "服务账号已创建");
        onClose();
        onDone();
      }}
    >
      <Field>
        <FieldLabel htmlFor="sa-name">服务账号名称</FieldLabel>
        <Input
          id="sa-name"
          required
          autoFocus={!account}
          disabled={!!account}
          maxLength={63}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="例如 ci-runner"
        />
        <FieldDescription>
          小写字母、数字和连字符；创建后不可修改。
        </FieldDescription>
      </Field>
      <Field>
        <FieldLabel htmlFor="sa-description">说明（可选）</FieldLabel>
        <Input
          id="sa-description"
          maxLength={256}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="例如 构建集群的运行节点"
        />
      </Field>
    </FormDialog>
  );
}

const allRoles = ["ingress", "serve", "egress"] as const;

export function EnrollmentKeyDialog({
  workspaceId,
  account,
  onClose,
  onDone,
}: {
  workspaceId: string;
  account?: ServiceAccount;
  onClose: () => void;
  onDone: () => void;
}) {
  const { showSecret } = useFeedback();
  const [accountID, setAccountID] = useState(account?.id ?? "");
  const [accountName, setAccountName] = useState(account?.name ?? "");
  const [roles, setRoles] = useState<(typeof allRoles)[number][]>(["ingress"]);
  const [uses, setUses] = useState("1");
  const [expires, setExpires] = useState(localTime(24));
  return (
    <FormDialog
      title="创建注册密钥"
      description="注册密钥让无人值守的设备以服务账号注册。个人设备请运行 agentd login。"
      submitLabel="生成密钥"
      onClose={onClose}
      onSubmit={async () => {
        if (!accountID || !roles.length)
          throw new Error("请选择服务账号和至少一种角色。");
        if (new Date(expires) <= new Date())
          throw new Error("失效时间必须晚于当前时间。");
        const created = result(
          await sdk.createServiceAccountEnrollmentKey({
            path: { workspaceId, serviceAccountId: accountID },
            body: {
              roles,
              maxUses: Number(uses),
              expiresAt: new Date(expires).toISOString(),
            },
            signal: requestSignal(),
          }),
        );
        onClose();
        onDone();
        showSecret({
          title: "注册密钥已生成",
          description: `服务账号 ${accountName}，用于 agentd enroll --key-file`,
          token: created.token,
          filename: "agents-enrollment.key",
        });
      }}
    >
      <Field>
        <FieldLabel>服务账号</FieldLabel>
        <ResourcePicker
          value={accountID}
          valueLabel={accountName}
          placeholder="选择服务账号"
          disabled={!!account}
          onSelect={(c) => {
            setAccountID(c.id);
            setAccountName(c.label);
          }}
          search={async (q, signal) =>
            result(
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
      </Field>
      <FieldSet>
        <FieldLegend variant="label">允许注册的角色</FieldLegend>
        <div className="flex gap-6">
          {allRoles.map((role) => (
            <Field key={role} orientation="horizontal" className="w-auto">
              <Checkbox
                id={`key-role-${role}`}
                checked={roles.includes(role)}
                onCheckedChange={(checked) =>
                  setRoles((previous) =>
                    checked
                      ? [...previous, role]
                      : previous.filter((r) => r !== role),
                  )
                }
              />
              <FieldLabel htmlFor={`key-role-${role}`} className="font-normal">
                {roleNames[role]}
              </FieldLabel>
            </Field>
          ))}
        </div>
      </FieldSet>
      <div className="grid grid-cols-2 gap-4">
        <Field>
          <FieldLabel htmlFor="key-uses">最多使用次数</FieldLabel>
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
        <Field>
          <FieldLabel htmlFor="key-expires">失效时间</FieldLabel>
          <Input
            id="key-expires"
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
