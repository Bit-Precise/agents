import { useEffect, useState } from "react";
import { BotIcon, UserIcon } from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type { DeviceAuthorizationView, Principal } from "@/api/generated";
import { APIFailure, message, requestSignal, result } from "@/api/session";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldLabel,
  FieldLegend,
  FieldSet,
  FieldTitle,
} from "@/components/ui/field";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { RoleBadges } from "@/components/cells";
import { Brand } from "./login-page";
import { roleNames, time, workspaceLabel } from "@/lib/format";
import { useUser } from "@/lib/session";

type Ending = "declined" | "expired" | "consumed" | "missing";
const endings: Record<Ending, { title: string; text: string }> = {
  declined: {
    title: "已拒绝这次注册",
    text: "设备没有获得任何凭据。请在终端按 Ctrl+C 结束 agentd login。",
  },
  expired: {
    title: "授权已过期",
    text: "请在终端重新运行 agentd login，并在 10 分钟内完成确认。",
  },
  consumed: {
    title: "设备已注册",
    text: "这次授权已经用于注册设备，可以关闭此页面。",
  },
  missing: {
    title: "找不到这次授权",
    text: "它不属于当前浏览器会话，或已经结束。请在终端重新运行 agentd login。",
  },
};
const key = (p: Principal) => `${p.type}:${p.id}`;

// The person explicitly chooses the Workspace and whether the device runs as
// themselves or as a Service Account; the server rechecks every choice.
export function DevicePage({ id }: { id: string }) {
  const user = useUser();
  const [view, setView] = useState<DeviceAuthorizationView | null>(null);
  const [ending, setEnding] = useState<Ending | null>(null);
  const [error, setError] = useState("");
  const [workspaceID, setWorkspaceID] = useState("");
  const [principal, setPrincipal] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    const abort = new AbortController();
    sdk
      .getDeviceAuthorization({ path: { id }, signal: abort.signal })
      .then((response) => {
        const v = result(response);
        setView(v);
        const status = v.authorization.status;
        if (status === "revoked") setEnding("declined");
        if (status === "expired" || status === "consumed") setEnding(status);
        if (v.choices.length === 1) setWorkspaceID(v.choices[0].workspace.id);
      })
      .catch((e) => {
        if (abort.signal.aborted) return;
        if (e instanceof APIFailure && e.status === 404) setEnding("missing");
        else setError(message(e));
      });
    return () => abort.abort();
  }, [id]);
  const authorized = view?.authorization.status === "authorized";
  const choice = view?.choices.find((c) => c.workspace.id === workspaceID);
  const options: {
    principal: Principal;
    title: string;
    description: string;
    roles: string[];
  }[] = choice
    ? [
        ...(choice.self
          ? [
              {
                principal: { type: "user" as const, id: user.id },
                title: `我自己（${user.name}）`,
                description: "随你的成员身份生效；离开工作空间时设备一并撤销。",
                roles: choice.selfRoles ?? [],
              },
            ]
          : []),
        ...choice.serviceAccounts.map((s) => ({
          principal: { type: "service_account" as const, id: s.id },
          title: `服务账号 ${s.name}`,
          description: s.description || "团队设备，不随操作人离开而失效。",
          roles: ["ingress", "serve", "egress"],
        })),
      ]
    : [];
  const chosen = authorized
    ? view?.authorization.principal
    : options.find((o) => key(o.principal) === principal)?.principal;
  const chosenWorkspace = authorized
    ? view?.authorization.workspaceId
    : workspaceID;
  async function confirm() {
    if (!chosen || !chosenWorkspace || busy) return;
    setBusy(true);
    setError("");
    try {
      const next = result(
        await sdk.confirmDeviceAuthorization({
          path: { id },
          body: { workspaceId: chosenWorkspace, principal: chosen },
          signal: requestSignal(),
        }),
      );
      const target = new URL(next.redirectTo);
      if (!["http:", "https:"].includes(target.protocol))
        throw new Error("授权服务返回了无效的地址。");
      location.assign(target.href);
    } catch (e) {
      setError(message(e));
      setBusy(false);
    }
  }
  async function decline() {
    setBusy(true);
    setError("");
    try {
      result(
        await sdk.rejectDeviceAuthorization({
          path: { id },
          signal: requestSignal(),
        }),
      );
      setEnding("declined");
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-6 bg-muted p-6 md:p-10">
      <div className="flex w-full max-w-lg flex-col gap-6">
        <Brand />
        <Card>
          <CardHeader>
            <CardTitle className="text-xl">注册 agentd 设备</CardTitle>
            <CardDescription>
              当前登录：{user.name}。只在你自己刚刚运行了 agentd login 时继续。
            </CardDescription>
          </CardHeader>
          <CardContent className="grid gap-6">
            {ending ? (
              <Alert>
                <AlertDescription>
                  <span className="font-medium text-foreground">
                    {endings[ending].title}
                  </span>
                  <br />
                  {endings[ending].text}
                </AlertDescription>
              </Alert>
            ) : !view ? (
              error ? (
                <Alert variant="destructive">
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              ) : (
                <Skeleton className="h-32 w-full" />
              )
            ) : (
              <>
                <div className="grid gap-1 rounded-lg border bg-muted/50 p-4">
                  <span className="text-xs text-muted-foreground">验证码</span>
                  <span className="font-mono text-2xl tracking-widest">
                    {view.userCode || "—"}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    请确认与终端显示一致 · 客户端 {view.authorization.clientId}{" "}
                    · 有效期至 {time(view.authorization.expiresAt, true)}
                  </span>
                </div>
                {authorized ? (
                  <Alert>
                    <AlertDescription>
                      已确认这次注册。如果页面没有自动继续，请点击“确认注册”。
                    </AlertDescription>
                  </Alert>
                ) : view.choices.length === 0 ? (
                  <Alert variant="destructive">
                    <AlertDescription>
                      你没有可以注册设备的工作空间。请联系管理员把你加入工作空间。
                    </AlertDescription>
                  </Alert>
                ) : (
                  <>
                    <FieldSet>
                      <FieldLegend variant="label">选择工作空间</FieldLegend>
                      <RadioGroup
                        aria-label="工作空间"
                        value={workspaceID}
                        onValueChange={(v) => {
                          setWorkspaceID(String(v));
                          setPrincipal("");
                        }}
                      >
                        {view.choices.map((c) => (
                          <FieldLabel
                            key={c.workspace.id}
                            htmlFor={`ws-${c.workspace.id}`}
                          >
                            <Field orientation="horizontal">
                              <FieldContent>
                                <FieldTitle>
                                  {workspaceLabel(c.workspace.name, user.id)}
                                </FieldTitle>
                                <FieldDescription>
                                  {c.role ? roleNames[c.role] : "实例管理员"}
                                </FieldDescription>
                              </FieldContent>
                              <RadioGroupItem
                                value={c.workspace.id}
                                id={`ws-${c.workspace.id}`}
                              />
                            </Field>
                          </FieldLabel>
                        ))}
                      </RadioGroup>
                    </FieldSet>
                    {choice && (
                      <FieldSet>
                        <FieldLegend variant="label">
                          选择设备的运行身份
                        </FieldLegend>
                        <RadioGroup
                          aria-label="运行身份"
                          value={principal}
                          onValueChange={(v) => setPrincipal(String(v))}
                        >
                          {options.map((o) => (
                            <FieldLabel
                              key={key(o.principal)}
                              htmlFor={`p-${key(o.principal)}`}
                            >
                              <Field orientation="horizontal">
                                {o.principal.type === "user" ? (
                                  <UserIcon className="size-4" />
                                ) : (
                                  <BotIcon className="size-4" />
                                )}
                                <FieldContent>
                                  <FieldTitle>{o.title}</FieldTitle>
                                  <FieldDescription>
                                    {o.description}
                                  </FieldDescription>
                                </FieldContent>
                                <RadioGroupItem
                                  value={key(o.principal)}
                                  id={`p-${key(o.principal)}`}
                                />
                              </Field>
                            </FieldLabel>
                          ))}
                        </RadioGroup>
                        {chosen && (
                          <FieldDescription className="flex flex-wrap items-center gap-1.5">
                            最多可启用：
                            <RoleBadges
                              roles={
                                options.find(
                                  (o) => key(o.principal) === principal,
                                )?.roles ?? []
                              }
                            />
                          </FieldDescription>
                        )}
                      </FieldSet>
                    )}
                  </>
                )}
                {error && (
                  <Alert variant="destructive">
                    <AlertDescription>{error}</AlertDescription>
                  </Alert>
                )}
              </>
            )}
          </CardContent>
          {view && !ending && (
            <CardFooter className="justify-end gap-2">
              {!authorized && (
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => void decline()}
                >
                  拒绝
                </Button>
              )}
              <Button
                disabled={busy || !chosen || !chosenWorkspace}
                onClick={() => void confirm()}
              >
                {busy && <Spinner />}
                确认注册
              </Button>
            </CardFooter>
          )}
        </Card>
      </div>
    </div>
  );
}
