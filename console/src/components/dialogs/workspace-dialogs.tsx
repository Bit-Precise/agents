import { useState } from "react";
import { toast } from "sonner";
import * as sdk from "@/api/generated/sdk.gen";
import type { Membership, User } from "@/api/generated";
import { requestSignal, result } from "@/api/session";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { OptionSelect } from "@/components/data-table";
import { ResourcePicker } from "@/components/resource-picker";
import { useFeedback } from "@/components/feedback";
import { FormDialog } from "./form-dialog";
import { memberRoles, roleNames } from "@/lib/format";
import { navigate } from "@/lib/router";
import { paths } from "@/lib/routes";
import { useUser, useWorkspace } from "@/lib/session";

export function CreateWorkspaceDialog({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("");
  return (
    <FormDialog
      title="创建工作空间"
      description="你将成为新工作空间的所有者，可以邀请成员并注册设备。"
      submitLabel="创建"
      onClose={onClose}
      onSubmit={async () => {
        const workspace = result(
          await sdk.createWorkspace({
            body: { name: name.trim() },
            signal: requestSignal(),
          }),
        );
        toast.success("工作空间已创建");
        onClose();
        navigate(paths.workspace(workspace.id));
      }}
    >
      <Field>
        <FieldLabel htmlFor="workspace-name">工作空间名称</FieldLabel>
        <Input
          id="workspace-name"
          required
          autoFocus
          maxLength={63}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="例如 design-team"
        />
        <FieldDescription>
          小写字母、数字和连字符，创建后不可修改；它会出现在服务域名中。
        </FieldDescription>
      </Field>
    </FormDialog>
  );
}

export function CreateUserDialog({
  onClose,
  onDone,
}: {
  onClose: () => void;
  onDone: () => void;
}) {
  const [name, setName] = useState("");
  const { showSecret } = useFeedback();
  return (
    <FormDialog
      title="创建用户"
      description="新用户的登录令牌只在创建后显示一次。启用单点登录时，用户也可直接用 SSO 登录。"
      submitLabel="创建"
      onClose={onClose}
      onSubmit={async () => {
        const created = result(
          await sdk.createAdminUser({
            body: { name: name.trim() },
            signal: requestSignal(),
          }),
        );
        onClose();
        onDone();
        showSecret({
          title: "用户已创建",
          description: created.user.name,
          token: created.token,
          filename: "agents-user-token.txt",
        });
      }}
    >
      <Field>
        <FieldLabel htmlFor="user-name">用户名称</FieldLabel>
        <Input
          id="user-name"
          required
          autoFocus
          maxLength={90}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </Field>
    </FormDialog>
  );
}

// Adds a member or changes an existing member's role.
export function MemberDialog({
  workspaceId,
  member,
  onClose,
  onDone,
}: {
  workspaceId: string;
  member?: Membership;
  onClose: () => void;
  onDone: () => void;
}) {
  const viewer = useUser();
  // Owner and maintainer are offered only with members.manage_privileged.
  const privileged = useWorkspace().can("members.manage_privileged");
  const [userID, setUserID] = useState(member?.userId ?? "");
  const [userLabel, setUserLabel] = useState("");
  const [role, setRole] = useState<Membership["role"]>(
    member?.role ?? "developer",
  );
  return (
    <FormDialog
      title={member ? "修改成员角色" : "添加成员"}
      description="成员角色决定管理权限；业务访问仍需要资源授权。"
      onClose={onClose}
      onSubmit={async () => {
        if (!userID.trim()) throw new Error("请选择用户。");
        const options = {
          path: { workspaceId, userId: userID.trim() },
          body: { role },
          signal: requestSignal(),
        };
        result(
          viewer.admin
            ? await sdk.putAdminMembership(options)
            : await sdk.putMembership(options),
        );
        toast.success(member ? "角色已更新" : "成员已添加");
        onClose();
        onDone();
      }}
    >
      {!member && (
        <Field>
          <FieldLabel>用户</FieldLabel>
          {viewer.admin ? (
            <ResourcePicker
              value={userID}
              valueLabel={userLabel}
              placeholder="选择用户"
              onSelect={(c) => {
                setUserID(c.id);
                setUserLabel(c.label);
              }}
              search={async (q, signal) =>
                result(
                  await sdk.listAdminUsers({
                    query: {
                      q: q || undefined,
                      disabled: false,
                      pageSize: 20,
                      sortBy: "name",
                    },
                    signal,
                  }),
                ).items.map((u: User) => ({
                  id: u.id,
                  label: u.name,
                  description: u.email || u.id,
                }))
              }
            />
          ) : (
            <Input
              aria-label="用户 ID"
              required
              value={userID}
              onChange={(e) => setUserID(e.target.value)}
              placeholder="用户 ID"
            />
          )}
          {!viewer.admin && (
            <FieldDescription>
              请填写对方在控制台中看到的用户 ID。
            </FieldDescription>
          )}
        </Field>
      )}
      <Field>
        <FieldLabel htmlFor="member-role">角色</FieldLabel>
        <OptionSelect
          id="member-role"
          label="角色"
          value={role}
          onChange={(v) => setRole(v as Membership["role"])}
          options={memberRoles.map((value) => ({
            value,
            label: roleNames[value],
            disabled:
              !privileged && (value === "owner" || value === "maintainer"),
          }))}
          className="w-full"
        />
      </Field>
    </FormDialog>
  );
}
