import type { Agent } from "@/api/generated";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Separator } from "@/components/ui/separator";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Name, PrincipalCell, RoleBadges } from "@/components/cells";
import { enabledRoles, rule, time } from "@/lib/format";
import { Link } from "@/lib/router";
import { paths } from "@/lib/routes";
import { useSession } from "@/lib/session";

function Code({ children }: { children: string }) {
  return (
    <pre className="overflow-x-auto rounded-lg bg-muted px-3 py-2 font-mono text-xs leading-relaxed">
      {children}
    </pre>
  );
}

export function RegisterDeviceDialog({
  workspaceId,
  workspaceName,
  onClose,
}: {
  workspaceId: string;
  workspaceName: string;
  onClose: () => void;
}) {
  const { info } = useSession();
  const server = info?.serverURL ?? location.origin;
  const edge = info?.edgeURL ?? `${server}/agent`;
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>注册设备</DialogTitle>
          <DialogDescription>
            在设备上准备 agentd 配置，然后运行 agentd
            login，在浏览器中确认工作空间和运行身份。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 text-sm">
          <p className="font-medium">1. config.yaml</p>
          <Code>{`apiVersion: agents/v1alpha1
kind: AgentConfig
instance: ${server}
workspace: ${workspaceName}
agent:
  name: my-device
  stateDir: ./state
transport:
  type: xhttp
  endpoint: ${edge}`}</Code>
          <p className="font-medium">2. 登录并注册</p>
          <Code>{`agentd login --config config.yaml
agentd run --config config.yaml`}</Code>
          {!info?.deviceAuthorization && (
            <Alert>
              <AlertDescription>
                此实例尚未启用 agentd login，请使用服务账号的注册密钥。
              </AlertDescription>
            </Alert>
          )}
          <p className="text-muted-foreground">
            无人值守的服务器请在{" "}
            <Link
              className="underline underline-offset-4"
              to={
                paths.workspace(workspaceId, "members") +
                "?tab=service-accounts"
              }
              onClick={onClose}
            >
              服务账号
            </Link>{" "}
            中创建注册密钥，再执行 agentd enroll --key-file。
          </p>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>完成</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function AgentSheet({
  agent,
  onClose,
}: {
  agent: Agent;
  onClose: () => void;
}) {
  const effective = agent.effective;
  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{agent.name}</SheetTitle>
          <SheetDescription className="font-mono text-xs">
            {agent.id}
          </SheetDescription>
        </SheetHeader>
        <div className="grid gap-5 px-4 pb-6 text-sm">
          <dl className="grid grid-cols-[6rem_1fr] gap-x-4 gap-y-3">
            <dt className="text-muted-foreground">工作空间</dt>
            <dd>
              <Name kind="workspaces" id={agent.workspaceId} />
            </dd>
            <dt className="text-muted-foreground">运行身份</dt>
            <dd>
              <PrincipalCell
                principal={agent.principal}
                workspaceId={agent.workspaceId}
              />
            </dd>
            {agent.createdBy && (
              <>
                <dt className="text-muted-foreground">注册人</dt>
                <dd>
                  <Name
                    kind="users"
                    id={agent.createdBy}
                    workspaceId={agent.workspaceId}
                  />
                </dd>
              </>
            )}
            <dt className="text-muted-foreground">声明能力</dt>
            <dd>
              <RoleBadges roles={enabledRoles(agent)} />
            </dd>
            <dt className="text-muted-foreground">注册时间</dt>
            <dd>{time(agent.createdAt, true)}</dd>
            <dt className="text-muted-foreground">生效版本</dt>
            <dd>
              {effective.revision}
              {effective.policy ? ` · 策略 ${effective.policy}` : ""}
            </dd>
          </dl>
          {agent.lastRejection && (
            <Alert variant="destructive">
              <AlertDescription>
                最近一次配置更新被拒绝（{agent.lastRejection.kind}）：
                {agent.lastRejection.message}
                。设备继续使用下面的生效配置。
              </AlertDescription>
            </Alert>
          )}
          <Separator />
          <section className="grid gap-2">
            <h3 className="font-medium">生效的服务</h3>
            {(effective.services ?? []).length ? (
              (effective.services ?? []).map((s) => (
                <div key={s.name} className="rounded-lg border px-3 py-2">
                  <div className="font-medium">{s.name}</div>
                  <div className="text-xs text-muted-foreground">
                    {s.hostname} → {s.origin}
                  </div>
                </div>
              ))
            ) : (
              <p className="text-muted-foreground">无</p>
            )}
          </section>
          <section className="grid gap-2">
            <h3 className="font-medium">生效的出口范围</h3>
            {(effective.egress ?? []).length ? (
              <div className="flex flex-wrap gap-1.5">
                {(effective.egress ?? []).map((r, i) => (
                  <code
                    key={i}
                    className="rounded-md bg-muted px-2 py-0.5 text-xs"
                  >
                    {rule(r)}
                  </code>
                ))}
              </div>
            ) : (
              <p className="text-muted-foreground">无</p>
            )}
          </section>
          <p className="text-muted-foreground">
            本机访问（Ingress）：{effective.ingress ? "已生效" : "未生效"}
          </p>
        </div>
      </SheetContent>
    </Sheet>
  );
}
