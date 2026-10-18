import { client } from "./generated/client.gen";

let csrfToken = "";
export function setCSRF(value: string) {
  csrfToken = value;
}

client.setConfig({
  baseUrl: window.location.origin,
  credentials: "same-origin",
  cache: "no-store",
});
// The generated client owns all HTTP operations. This is the only interceptor:
// credentials are cookies and the CSRF value lives only in this module's memory.
client.interceptors.request.use((request) => {
  if (!["GET", "HEAD", "OPTIONS"].includes(request.method) && csrfToken) {
    request.headers.set("X-CSRF-Token", csrfToken);
  }
  return request;
});

// An expired or revoked session ends at the sign-in page.
let unauthorized: () => void = () => {};
export function onUnauthorized(handler: () => void) {
  unauthorized = handler;
}

export class APIFailure extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}
export function result<T>(response: {
  data?: T;
  error?: unknown;
  response?: Response;
}): T {
  if (
    response.error !== undefined ||
    (response.response && !response.response.ok)
  ) {
    const detail = response.error as
      { error?: { message?: string } | string; message?: string } | undefined;
    const message =
      typeof detail?.error === "string"
        ? detail.error
        : detail?.error?.message || detail?.message;
    const status = response.response?.status || 0;
    if (status === 401) unauthorized();
    throw new APIFailure(message || "请求未完成，请检查连接后重试。", status);
  }
  return response.data as T;
}
export function message(error: unknown): string {
  if (error instanceof Error)
    return error.name === "TimeoutError"
      ? "请求超时，请稍后重试。"
      : error.message;
  return "请求未完成，请稍后重试。";
}
export function requestSignal() {
  return AbortSignal.timeout(15000);
}

// Browser navigation is intentionally distinct from JSON SDK calls. After
// sign-in the Console continues only to a service authorization or to an
// `agentd login` request; Control validates both again.
export function serviceReturnTo(): string | null {
  const value = new URLSearchParams(window.location.search).get("returnTo");
  const paths = ["/auth/authorize", "/oauth/login"];
  if (!paths.some((path) => value?.startsWith(path + "?"))) return null;
  try {
    const target = new URL(value!, window.location.origin);
    return target.origin === window.location.origin &&
      paths.includes(target.pathname)
      ? target.pathname + target.search
      : null;
  } catch {
    return null;
  }
}
export function oidcDestination(loginURL: string): string {
  const target = new URL(loginURL, window.location.origin);
  if (
    target.origin !== window.location.origin ||
    target.pathname !== "/auth/oidc/login"
  )
    throw new Error("身份登录地址无效，请联系管理员。");
  target.searchParams.set("returnTo", serviceReturnTo() || "/console/");
  return target.href;
}
