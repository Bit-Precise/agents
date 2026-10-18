import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { once } from "node:events";
import { access, chmod, mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { constants } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { delimiter, dirname, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import test from "node:test";
import { chromium } from "playwright-core";

const exec = promisify(execFile);
const repository = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

async function chromePath() {
  const candidates = process.env.CHROME_PATH
    ? [process.env.CHROME_PATH]
    : [
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
        "/usr/bin/google-chrome",
        "/usr/bin/google-chrome-stable",
        "/usr/bin/chromium",
        "/usr/bin/chromium-browser",
        ...String(process.env.PATH || "")
          .split(delimiter)
          .flatMap((directory) =>
            ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser"].map((name) => join(directory, name)),
          ),
      ];
  for (const candidate of candidates) {
    try {
      await access(candidate, constants.X_OK);
      return candidate;
    } catch {}
  }
  throw new Error("Chrome is required for the Console smoke test; set CHROME_PATH to its executable. No browser is downloaded automatically.");
}

async function availablePort() {
  const listener = createServer();
  listener.listen(0, "127.0.0.1");
  await once(listener, "listening");
  const port = listener.address().port;
  await new Promise((done, reject) => listener.close((error) => (error ? reject(error) : done())));
  return port;
}

async function stop(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, "exit");
  child.kill("SIGTERM");
  const timer = setTimeout(() => child.kill("SIGKILL"), 5_000);
  timer.unref();
  try {
    await exited;
  } finally {
    clearTimeout(timer);
  }
}

test("Console uses scoped REST resources across admin pagination and rapid role navigation", { timeout: 180_000 }, async (t) => {
  const executablePath = await chromePath();
  const work = await mkdtemp(join(tmpdir(), "agents-console-ui-"));
  await chmod(work, 0o700);
  let server, browser;
  const errors = [], calls = [];
  const localTokens = new Set();
  const redact = (value) => {
    let text = String(value);
    for (const token of localTokens) text = text.replaceAll(token, "[local credential]");
    return text;
  };
  t.after(async () => {
    try {
      await browser?.close();
    } finally {
      await stop(server);
      await rm(work, { recursive: true, force: true });
    }
  });

  try {
    const binary = join(work, "agents");
    await exec("go", ["build", "-mod=readonly", "-o", binary, "./cmd/agents"], { cwd: repository, timeout: 120_000 });
    const port = await availablePort();
    const base = `http://localhost:${port}`;
    const adminToken = randomBytes(32).toString("hex");
    localTokens.add(adminToken);
    const state = join(work, "state");
    await mkdir(state, { mode: 0o700 });
    const tokenFile = join(state, "admin.token");
    await writeFile(tokenFile, adminToken + "\n", { mode: 0o600 });
    const config = join(work, "agents.json");
    await writeFile(config, JSON.stringify({
      listenAddress: `127.0.0.1:${port}`,
      serverURL: base,
      edgeURL: base + "/agent",
      stateDir: state,
      adminTokenFile: tokenFile,
      developmentHTTP: true,
      database: { type: "sqlite", dsn: join(state, "agents.sqlite") },
    }), { mode: 0o600 });
    server = spawn(binary, ["serve", "--config", config], { cwd: work, stdio: "ignore" });
    let spawnError;
    server.on("error", (error) => { spawnError = error; });
    const deadline = Date.now() + 15_000;
    while (true) {
      if (spawnError) throw new Error("Could not start the isolated Control server");
      if (server.exitCode !== null) throw new Error("The isolated Control server exited before becoming ready");
      try {
        const ready = await fetch(base + "/v1/instance", { signal: AbortSignal.timeout(1_000) });
        if (ready.ok) break;
      } catch {}
      assert.ok(Date.now() < deadline, "isolated Control readiness timed out");
      await delay(50);
    }
    async function api(method, path, body, token = adminToken, expected = 200) {
      const response = await fetch(base + path, {
        method,
        headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(10_000),
      });
      assert.equal(response.status, expected, `${method} ${path}`);
      return response.status === 204 ? undefined : response.json();
    }

    // Use public APIs for all fixture creation; every database and credential is local to this run.
    const userCount = 25;
    let user;
    for (let index = 0; index < userCount; index++) {
      const created = await api("POST", "/v1/admin/users", { name: `ui-user-${String(index).padStart(2, "0")}`, admin: false }, adminToken, 201);
      localTokens.add(created.token);
      if (index === 0) user = created;
    }
    const ownWorkspaces = await api("GET", "/v1/workspaces?pageSize=100", undefined, user.token);
    const workspace = ownWorkspaces.items.find((item) => item.name === `personal-${user.user.id}`);
    assert.ok(workspace, "new users must have a personal workspace");
    const sharedWorkspace = await api("POST", "/v1/admin/workspaces", { name: "aaa-shared" }, adminToken, 201);
    await api("PUT", `/v1/admin/workspaces/${sharedWorkspace.id}/memberships/${user.user.id}`, { role: "developer" });
    const rule = { domain: { exact: "example.test" }, ports: [443], protocol: "tls" };
    const enrollment = await api("POST", "/v1/admin/enrollment-keys", {
      workspaceId: workspace.id, userId: user.user.id, roles: ["ingress", "serve", "egress"], autoApprove: true, maxUses: 1,
    }, adminToken, 201);
    localTokens.add(enrollment.token);
    const enrolled = await api("POST", "/api/v1/enroll", {
      enrollmentKey: enrollment.token,
      config: {
        apiVersion: "agents/v1alpha1", kind: "AgentConfig", instance: base, workspace: workspace.name,
        agent: { name: "ui-device", stateDir: "./state" },
        transport: { type: "xhttp", mode: "packet-up", endpoint: base + "/agent", allowHTTP: true },
        ingress: { enabled: true, http: { enabled: true, listen: "127.0.0.1:18080" }, routes: [rule], onUnmatched: "reject" },
        serve: { enabled: true, services: [{ name: "demo", origin: "http://127.0.0.1:18081" }] },
        egress: { enabled: true, allow: [rule] },
      },
    }, "", 201);
    for (const [key, value] of Object.entries(enrolled.credentials)) if (key.endsWith("Token") && value) localTokens.add(value);
    await api("POST", "/v1/admin/routes", { workspaceId: workspace.id, kind: "egress", agentId: enrolled.agent.id, name: "ui-egress", priority: 0, rule }, adminToken, 201);

    browser = await chromium.launch({ executablePath, headless: true });
    async function session(token, kind) {
      const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
      const page = await context.newPage();
      page.setDefaultTimeout(10_000);
      page.on("pageerror", (error) => errors.push(redact(error.message)));
      page.on("request", (request) => {
        const url = new URL(request.url());
        if (url.origin === base) calls.push({ session: kind, method: request.method(), path: url.pathname, query: url.search });
      });
      await page.goto(base + "/console/");
      try { await page.getByLabel("访问令牌", { exact: true }).fill(token); }
      catch { throw new Error("Could not fill the local Console login field"); }
      const login = page.waitForResponse((response) => new URL(response.url()).pathname === "/v1/sessions" && response.request().method() === "POST");
      const [loggedIn] = await Promise.all([login, page.getByRole("button", { name: "登录", exact: true }).click()]);
      assert.equal(loggedIn.status(), 201);
      await page.getByRole("button", { name: "退出登录" }).waitFor();
      return { context, page };
    }
    async function apiAction(page, action, path, query = {}) {
      const pending = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return response.request().method() === "GET" && url.pathname === path && Object.entries(query).every(([key, value]) => url.searchParams.get(key) === String(value));
      });
      const [response] = await Promise.all([pending, action()]);
      assert.equal(response.status(), 200, path);
      return response.json();
    }

    const { page: admin } = await session(adminToken, "admin");
    await admin.getByRole("link", { name: "权限与成员", exact: true }).click();
    const users = await apiAction(admin, () => admin.getByRole("tab", { name: "用户", exact: true }).click(), "/v1/admin/users");
    assert.equal(users.pagination.total, userCount + 1);
    assert.equal(users.items.length, 20);
    const second = await apiAction(admin, () => admin.getByRole("button", { name: "下一页", exact: true }).click(), "/v1/admin/users", { page: 2 });
    assert.equal(second.items.length, userCount + 1 - 20);
    const found = await apiAction(admin, () => admin.getByLabel("筛选当前列表").fill("ui-user-00"), "/v1/admin/users", { q: "ui-user-00", page: 1 });
    assert.equal(found.pagination.total, 1);
    assert.equal(found.items[0].id, user.user.id);
    await apiAction(admin, () => admin.getByLabel("筛选当前列表").fill(""), "/v1/admin/users", { page: 1 });
    await admin.getByRole("combobox", { name: "排序字段" }).click();
    const sorted = await apiAction(admin, () => admin.getByRole("option", { name: "按名称", exact: true }).click(), "/v1/admin/users", { sortBy: "name" });
    assert.equal(sorted.items[0].name, "admin");
    await admin.getByRole("combobox", { name: "状态筛选" }).click();
    const disabled = await apiAction(admin, () => admin.getByRole("option", { name: "已停用", exact: true }).click(), "/v1/admin/users", { disabled: true });
    assert.equal(disabled.pagination.total, 0);
    assert.deepEqual(disabled.items, []);
    await admin.getByRole("combobox", { name: "状态筛选" }).click();
    const enabled = await apiAction(admin, () => admin.getByRole("option", { name: "有效", exact: true }).click(), "/v1/admin/users", { disabled: false });
    assert.equal(enabled.pagination.total, userCount + 1);
    await apiAction(admin, () => admin.getByRole("tab", { name: "工作空间", exact: true }).click(), "/v1/admin/workspaces");
    await admin.getByRole("button", { name: "创建工作空间", exact: true }).click();
    await admin.getByLabel("工作空间名称").fill("ui-browser-created");
    const creation = admin.waitForResponse((response) => new URL(response.url()).pathname === "/v1/admin/workspaces" && response.request().method() === "POST");
    const [created] = await Promise.all([creation, admin.getByRole("dialog").getByRole("button", { name: "确认保存", exact: true }).click()]);
    assert.equal(created.status(), 201);
    assert.ok(created.headers().location);
    const creationHeaders = await created.request().allHeaders();
    assert.ok(creationHeaders["x-csrf-token"], "browser creation must send CSRF");
    assert.ok(creationHeaders.cookie, "browser creation must use its session cookie");
    await admin.getByRole("dialog").waitFor({ state: "hidden" });
    t.diagnostic("Admin pagination, search, sort, boolean filters, and cookie/CSRF creation passed.");

    const { page } = await session(user.token, "user");
    const userBase = `/v1/workspaces/${workspace.id}`;
    await page.getByRole("button", { name: new RegExp(workspace.name) }).waitFor();
    const agents = await apiAction(page, () => page.getByRole("link", { name: "Agents", exact: true }).click(), userBase + "/agents");
    assert.equal(agents.pagination.total, 1);
    assert.equal(agents.items[0].id, enrolled.agent.id);
    await page.getByText("ui-device", { exact: true }).waitFor();
    for (const [label, resource] of [["访问端", "ingresses"], ["出口提供方", "egresses"]]) {
      const roles = await apiAction(page, () => page.getByRole("tab", { name: label, exact: true }).click(), `${userBase}/${resource}`);
      assert.equal(roles.pagination.total, 1);
      assert.equal(roles.items[0].agentId, enrolled.agent.id);
      await page.getByText("ui-device", { exact: true }).waitFor();
      assert.deepEqual(errors, [], "role transitions must not render the previous resource DTO");
    }
    // Keep responses in flight while switching incompatible row shapes, then check the final active scope.
    await page.route(`${base}${userBase}/*`, async (route) => {
      if (/\/(agents|ingresses|egresses)$/.test(new URL(route.request().url()).pathname)) await delay(150);
      await route.continue().catch(() => {});
    });
    for (const label of ["设备", "访问端", "出口提供方", "设备"]) await page.getByRole("tab", { name: label, exact: true }).click();
    const finalRoles = await apiAction(page, () => page.getByRole("tab", { name: "访问端", exact: true }).click(), userBase + "/ingresses");
    assert.equal(finalRoles.items[0].agentId, enrolled.agent.id);
    await page.getByText("ui-device", { exact: true }).waitFor();
    await page.unrouteAll({ behavior: "wait" });
    const servers = await apiAction(page, () => page.getByRole("link", { name: "服务发布", exact: true }).click(), userBase + "/servers");
    assert.equal(servers.pagination.total, 1);
    assert.equal(servers.items[0].name, "demo");
    await page.getByText("demo", { exact: true }).waitFor();
    const routes = await apiAction(page, () => page.getByRole("link", { name: "路由规则", exact: true }).click(), userBase + "/routes");
    assert.ok(routes.items.some((item) => item.kind === "egress" && item.targetId === enrolled.agent.id));
    assert.ok(routes.items.some((item) => item.kind === "serve" && item.targetType === "server"));
    await page.getByText("ui-egress", { exact: true }).waitFor();
    await page.getByRole("button", { name: new RegExp(workspace.name) }).click();
    const picker = page.getByRole("dialog");
    const noChoices = await apiAction(page, () => picker.getByLabel("搜索工作空间").fill("no-such-workspace"), "/v1/workspaces", { q: "no-such-workspace" });
    assert.equal(noChoices.pagination.total, 0);
    await picker.getByRole("button", { name: /aaa-shared/ }).waitFor({ state: "hidden" });
    const choices = await apiAction(page, () => picker.getByLabel("搜索工作空间").fill("aaa-shared"), "/v1/workspaces", { q: "aaa-shared" });
    assert.equal(choices.pagination.total, 1);
    const scoped = await apiAction(page, () => picker.getByRole("button", { name: /aaa-shared/ }).click(), `/v1/workspaces/${sharedWorkspace.id}/routes`);
    assert.equal(scoped.pagination.total, 0);
    await page.getByText("ui-egress", { exact: true }).waitFor({ state: "hidden" });
    assert.equal(calls.some((call) => call.session === "user" && call.path.startsWith("/v1/admin/")), false, "ordinary pages must never request admin resources");
    assert.equal(calls.some((call) => /\/(?:api\/)?v1\/state$/.test(call.path)), false, "aggregate state requests are forbidden");
    assert.deepEqual(errors, [], "Console must not raise JavaScript errors");
    t.diagnostic("Personal-workspace default, rapid role tabs, routes, and workspace switching passed without aggregate/admin requests or JavaScript errors.");
  } catch (error) {
    if (browser) {
      const diagnostics = await mkdtemp(join(tmpdir(), "agents-console-ui-failure-"));
      await chmod(diagnostics, 0o700);
      let index = 0;
      for (const context of browser.contexts()) for (const page of context.pages()) {
        const screenshot = join(diagnostics, `page-${index++}.png`);
        await page.screenshot({ path: screenshot, fullPage: true, timeout: 5_000 }).then(() => chmod(screenshot, 0o600)).catch(() => {});
      }
      t.diagnostic(`Failure screenshots: ${diagnostics}`);
    }
    t.diagnostic(`JavaScript errors: ${JSON.stringify(errors)}`);
    t.diagnostic(`Last resource requests: ${JSON.stringify(calls.slice(-15))}`);
    throw new Error(redact(error?.message || error));
  }
});
