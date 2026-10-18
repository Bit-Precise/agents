import { useEffect, useState } from "react";
import { Bot, LoaderCircle, MonitorSmartphone, User } from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import type {
  DeviceAuthorizationView,
  Principal,
  User as Person,
} from "@/api/generated";
import { APIFailure, message, requestSignal, result } from "@/api/session";
import { Button } from "@/components/ui/button";
import { Notice, Tags } from "./controls";
import { roleNames, time } from "@/lib/model";

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

// One Device Flow confirmation: the signed-in person explicitly chooses the
// Workspace and whether the device runs as themselves or as a Service
// Account. The server rechecks every choice; this page only presents them.
export function DeviceAuthorizationPage({
  id,
  user,
}: {
  id: string;
  user: Person;
}) {
  const [view, setView] = useState<DeviceAuthorizationView | null>(null),
    [ending, setEnding] = useState<Ending | null>(null),
    [error, setError] = useState(""),
    [workspaceID, setWorkspaceID] = useState(""),
    [principal, setPrincipal] = useState<Principal | null>(null),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    const abort = new AbortController();
    void sdk
      .getDeviceAuthorization({
        path: { id },
        signal: AbortSignal.any([abort.signal, AbortSignal.timeout(15000)]),
      })
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
  const choice = view?.choices.find((c) => c.workspace.id === workspaceID);
  const workspaceLabel = (id?: string) => {
    const w = view?.choices.find((c) => c.workspace.id === id)?.workspace;
    return !w
      ? id || ""
      : w.name === "personal-" + user.id
        ? "个人空间"
        : w.name;
  };
  const authorized = view?.authorization.status === "authorized";
  const chosen = authorized ? view?.authorization.principal : principal;
  const chosenWorkspace = authorized
    ? view?.authorization.workspaceId
    : workspaceID;
  const roles = authorized
    ? view?.authorization.allowedRoles
    : chosen?.type === "user"
      ? choice?.selfRoles
      : ["ingress", "serve", "egress"];
  const chosenName =
    chosen?.type === "service_account"
      ? "服务账号 " +
        (view?.choices
          .flatMap((c) => c.serviceAccounts)
          .find((s) => s.id === chosen.id)?.name || chosen.id)
      : chosen
        ? `你自己（${user.name}）`
        : "";
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
    if (busy) return;
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
    <div className="device-page">
      <section className="device-card">
        <span className="eyebrow">AGENTD LOGIN</span>
        <h1>
          <MonitorSmartphone size={20} />
          注册 agentd 设备
        </h1>
        <p className="device-lead">
          当前登录：<strong>{user.name}</strong>
          。只在你自己刚刚运行了 agentd login 时继续。
        </p>
        {ending ? (
          <div className="device-ending" role="status">
            <h2>{endings[ending].title}</h2>
            <p>{endings[ending].text}</p>
          </div>
        ) : !view ? (
          error ? (
            <Notice error>{error}</Notice>
          ) : (
            <p className="device-loading">
              <LoaderCircle className="animate-spin" size={16} />
              正在读取授权…
            </p>
          )
        ) : (
          <>
            <div className="device-code">
              <span>验证码</span>
              <strong>{view.userCode || "—"}</strong>
              <small>
                请确认与终端显示一致 · 客户端 {view.authorization.clientId} ·
                有效期至 {time(view.authorization.expiresAt, true)}
              </small>
            </div>
            {authorized ? (
              <Notice>
                已确认以 {chosenName}{" "}
                注册。如果页面没有自动继续，请点击“确认注册”。
              </Notice>
            ) : view.choices.length === 0 ? (
              <Notice error>
                你没有可以注册设备的工作空间。请联系管理员把你加入工作空间。
              </Notice>
            ) : (
              <>
                <section className="device-step">
                  <h2>1 · 选择工作空间</h2>
                  <div
                    role="radiogroup"
                    aria-label="工作空间"
                    className="device-options"
                  >
                    {view.choices.map((c) => (
                      <button
                        type="button"
                        role="radio"
                        aria-checked={c.workspace.id === workspaceID}
                        key={c.workspace.id}
                        className="device-option"
                        onClick={() => {
                          setWorkspaceID(c.workspace.id);
                          setPrincipal(null);
                        }}
                      >
                        <strong>{workspaceLabel(c.workspace.id)}</strong>
                        <span>
                          {c.role ? roleNames[c.role] || c.role : "实例管理员"}
                        </span>
                      </button>
                    ))}
                  </div>
                </section>
                {choice && (
                  <section className="device-step">
                    <h2>2 · 选择设备的运行身份</h2>
                    <div
                      role="radiogroup"
                      aria-label="运行身份"
                      className="device-options"
                    >
                      {choice.self && (
                        <button
                          type="button"
                          role="radio"
                          aria-checked={chosen?.type === "user"}
                          className="device-option"
                          onClick={() =>
                            setPrincipal({ type: "user", id: user.id })
                          }
                        >
                          <strong>
                            <User size={14} />
                            我自己（{user.name}）
                          </strong>
                          <span>
                            随你的成员身份生效；离开工作空间时设备一并撤销
                          </span>
                        </button>
                      )}
                      {choice.serviceAccounts.map((s) => (
                        <button
                          type="button"
                          role="radio"
                          aria-checked={
                            chosen?.type === "service_account" &&
                            chosen.id === s.id
                          }
                          key={s.id}
                          className="device-option"
                          onClick={() =>
                            setPrincipal({ type: "service_account", id: s.id })
                          }
                        >
                          <strong>
                            <Bot size={14} />
                            服务账号 {s.name}
                          </strong>
                          <span>
                            {s.description || "团队设备，不随操作人离开而失效"}
                          </span>
                        </button>
                      ))}
                    </div>
                  </section>
                )}
              </>
            )}
            {chosen && chosenWorkspace && (
              <div className="device-summary">
                设备将以 <strong>{chosenName}</strong> 注册到工作空间{" "}
                <strong>{workspaceLabel(chosenWorkspace)}</strong>
                ，最多可启用：
                <Tags items={(roles ?? []).map((r) => roleNames[r] || r)} />
                实际启用的能力由设备配置声明，并由服务端自动校验。
              </div>
            )}
            {error && <Notice error>{error}</Notice>}
            <div className="device-actions">
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
                {busy && <LoaderCircle className="animate-spin" />}
                确认注册
              </Button>
            </div>
          </>
        )}
      </section>
    </div>
  );
}
