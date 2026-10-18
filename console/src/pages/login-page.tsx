import { useState, type FormEvent } from "react";
import { ShieldCheckIcon } from "lucide-react";
import * as sdk from "@/api/generated/sdk.gen";
import { message, oidcDestination, requestSignal, result } from "@/api/session";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldSeparator,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { useSession } from "@/lib/session";

export function Brand() {
  return (
    <div className="flex items-center gap-2 self-center font-medium">
      <div className="flex size-6 items-center justify-center rounded-md bg-primary text-sm text-primary-foreground">
        a
      </div>
      agents
    </div>
  );
}

export function LoginPage() {
  const { info, accept } = useSession();
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const oidc = info?.oidc.enabled === true;
  async function login(event: FormEvent) {
    event.preventDefault();
    if (busy || !token.trim()) return;
    setBusy(true);
    setError("");
    try {
      const session = result(
        await sdk.createSession({
          body: { token: token.trim() },
          signal: requestSignal(),
        }),
      );
      accept(session);
    } catch (e) {
      setError(message(e));
    } finally {
      setToken("");
      setBusy(false);
    }
  }
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-6 bg-muted p-6 md:p-10">
      <div className="flex w-full max-w-sm flex-col gap-6">
        <Brand />
        <Card>
          <CardHeader className="text-center">
            <CardTitle className="text-xl">登录控制台</CardTitle>
            <CardDescription>
              {oidc
                ? "使用单点登录进入你的工作空间。"
                : "使用管理员提供的访问令牌登录。"}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={login}>
              <FieldGroup>
                {oidc && (
                  <>
                    <Field>
                      <Button
                        type="button"
                        onClick={() => {
                          try {
                            location.assign(
                              oidcDestination(info.oidc.loginURL),
                            );
                          } catch (e) {
                            setError(message(e));
                          }
                        }}
                      >
                        <ShieldCheckIcon />
                        使用单点登录
                      </Button>
                    </Field>
                    <FieldSeparator className="*:data-[slot=field-separator-content]:bg-card">
                      或使用访问令牌
                    </FieldSeparator>
                  </>
                )}
                <Field>
                  <FieldLabel htmlFor="login-token">访问令牌</FieldLabel>
                  <Input
                    id="login-token"
                    type="password"
                    autoComplete="off"
                    maxLength={4096}
                    required
                    value={token}
                    onChange={(e) => setToken(e.target.value)}
                  />
                </Field>
                {error && (
                  <Alert variant="destructive">
                    <AlertDescription>{error}</AlertDescription>
                  </Alert>
                )}
                <Field>
                  <Button
                    type="submit"
                    variant={oidc ? "outline" : "default"}
                    disabled={busy}
                  >
                    {busy && <Spinner />}
                    登录
                  </Button>
                  <FieldDescription className="text-center">
                    令牌只用于本次登录，不会保存在浏览器中。
                  </FieldDescription>
                </Field>
              </FieldGroup>
            </form>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
