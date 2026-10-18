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
  let server, browser, hydra;
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
      await stop(hydra);
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
    // With AGENTS_TEST_HYDRA_BIN, a real Hydra serves `agentd login` too.
    let hydraConfig, hydraPublic, hydraAdmin;
    if (process.env.AGENTS_TEST_HYDRA_BIN) {
      const [publicPort, adminPort] = [await availablePort(), await availablePort()];
      hydraPublic = `http://127.0.0.1:${publicPort}`;
      hydraAdmin = `http://127.0.0.1:${adminPort}`;
      const hydraFile = join(work, "hydra.yaml");
      await writeFile(hydraFile, [
        "dsn: memory",
        `serve: {public: {host: 127.0.0.1, port: ${publicPort}}, admin: {host: 127.0.0.1, port: ${adminPort}}}`,
        `urls: {self: {issuer: "${hydraPublic}"}, login: "${base}/oauth/login", consent: "${base}/oauth/consent", error: "${base}/oauth/error", device: {verification: "${base}/oauth/device", success: "${base}/oauth/device/done"}}`,
        `secrets: {system: ["${randomBytes(24).toString("hex")}"]}`,
        "strategies: {access_token: opaque}",
        "oauth2: {device_authorization: {token_polling_interval: 1s}}",
        "",
      ].join("\n"), { mode: 0o600 });
      hydra = spawn(process.env.AGENTS_TEST_HYDRA_BIN, ["serve", "all", "--dev", "--config", hydraFile], { cwd: work, stdio: "ignore" });
      const ready = Date.now() + 15_000;
      while (true) {
        try {
          if ((await fetch(hydraAdmin + "/health/ready", { signal: AbortSignal.timeout(1_000) })).ok) break;
        } catch {}
        assert.ok(Date.now() < ready, "local Hydra readiness timed out");
        await delay(50);
      }
      hydraConfig = { enabled: true, publicURL: hydraPublic, adminURL: hydraAdmin };
    }
    const config = join(work, "agents.json");
    await writeFile(config, JSON.stringify({
      listenAddress: `127.0.0.1:${port}`,
      serverURL: base,
      edgeURL: base + "/agent",
      stateDir: state,
      adminTokenFile: tokenFile,
      developmentHTTP: true,
      database: { type: "sqlite", dsn: join(state, "agents.sqlite") },
      ...(hydraConfig ? { hydra: hydraConfig } : {}),
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
    // Unattended fixtures run as a Service Account of the user's own Workspace.
    const fleet = await api("POST", `/v1/workspaces/${workspace.id}/service-accounts`, { name: "ui-fleet", description: "UI fixtures" }, adminToken, 201);
    const enrollment = await api("POST", `/v1/workspaces/${workspace.id}/service-accounts/${fleet.id}/enrollment-keys`, {
      roles: ["ingress", "serve", "egress"], maxUses: 1,
    }, adminToken, 201);
    localTokens.add(enrollment.token);
    const enrolled = await api("POST", "/api/v1/enroll", {
      enrollmentKey: enrollment.token, instance: base, endpoint: base + "/agent", workspace: workspace.name, name: "ui-device",
      desired: {
        ingress: { enabled: true },
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
      await page.goto(base + "/");
      try { await page.getByLabel("访问令牌", { exact: true }).fill(token); }
      catch { throw new Error("Could not fill the local Console login field"); }
      const login = page.waitForResponse((response) => new URL(response.url()).pathname === "/v1/sessions" && response.request().method() === "POST");
      const [loggedIn] = await Promise.all([login, page.getByRole("button", { name: "登录", exact: true }).click()]);
      assert.equal(loggedIn.status(), 201);
      await page.getByRole("button", { name: "用户菜单" }).waitFor();
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
    const nav = (page, name) => page.getByRole("link", { name, exact: true }).click();

    const { page: admin } = await session(adminToken, "admin");
    // The administrator owns aaa-shared and lands there; the switcher opens the admin panel.
    await admin.waitForURL(base + `/workspaces/${sharedWorkspace.id}/overview`);
    await admin.getByRole("button", { name: "切换工作空间" }).click();
    await admin.getByRole("menuitem", { name: "进入管理员面板" }).click();
    await admin.waitForURL(base + "/admin/workspaces");
    const users = await apiAction(admin, () => nav(admin, "用户"), "/v1/admin/users");
    assert.equal(new URL(admin.url()).pathname, "/admin/users", "admin pages are addressable");
    assert.equal(users.pagination.total, userCount + 1);
    assert.equal(users.items.length, 20);
    const second = await apiAction(admin, () => admin.getByRole("button", { name: "下一页", exact: true }).click(), "/v1/admin/users", { page: 2 });
    assert.equal(second.items.length, userCount + 1 - 20);
    const found = await apiAction(admin, () => admin.getByLabel("筛选当前列表").fill("ui-user-00"), "/v1/admin/users", { q: "ui-user-00", page: 1 });
    assert.equal(found.pagination.total, 1);
    assert.equal(found.items[0].id, user.user.id);
    await apiAction(admin, () => admin.getByLabel("筛选当前列表").fill(""), "/v1/admin/users", { page: 1 });
    await admin.getByRole("combobox", { name: "状态筛选" }).click();
    const disabled = await apiAction(admin, () => admin.getByRole("option", { name: "已停用", exact: true }).click(), "/v1/admin/users", { disabled: true });
    assert.equal(disabled.pagination.total, 0);
    assert.deepEqual(disabled.items, []);
    await admin.getByRole("combobox", { name: "状态筛选" }).click();
    const enabled = await apiAction(admin, () => admin.getByRole("option", { name: "有效", exact: true }).click(), "/v1/admin/users", { disabled: false });
    assert.equal(enabled.pagination.total, userCount + 1);
    await apiAction(admin, () => nav(admin, "工作空间"), "/v1/admin/workspaces");
    await admin.getByRole("button", { name: "创建工作空间", exact: true }).click();
    await admin.getByLabel("工作空间名称").fill("ui-browser-created");
    const creation = admin.waitForResponse((response) => new URL(response.url()).pathname === "/v1/workspaces" && response.request().method() === "POST");
    const [created] = await Promise.all([creation, admin.getByRole("dialog").getByRole("button", { name: "创建", exact: true }).click()]);
    assert.equal(created.status(), 201);
    assert.ok(created.headers().location);
    const creationHeaders = await created.request().allHeaders();
    assert.ok(creationHeaders["x-csrf-token"], "browser creation must send CSRF");
    assert.ok(creationHeaders.cookie, "browser creation must use its session cookie");
    const createdWorkspace = await created.json();
    await admin.waitForURL(base + `/workspaces/${createdWorkspace.id}/overview`);
    t.diagnostic("Admin pagination, search, filters, addressable admin pages and cookie/CSRF creation passed.");

    const { page } = await session(user.token, "user");
    const userBase = `/v1/workspaces/${workspace.id}`;
    // A person lands on their personal Workspace; the URL names it.
    await page.waitForURL(base + `/workspaces/${workspace.id}/overview`);
    const agents = await apiAction(page, () => nav(page, "Agent 管理"), userBase + "/agents");
    assert.equal(new URL(page.url()).pathname, `/workspaces/${workspace.id}/agents`);
    assert.equal(agents.pagination.total, 1);
    assert.equal(agents.items[0].id, enrolled.agent.id);
    await page.getByText("ui-device", { exact: true }).waitFor();
    for (const [label, resource] of [["Ingress 管理", "ingresses"], ["Egress 管理", "egresses"]]) {
      const roles = await apiAction(page, () => nav(page, label), `${userBase}/${resource}`);
      assert.equal(roles.pagination.total, 1);
      assert.equal(roles.items[0].agentId, enrolled.agent.id);
      await page.getByText("ui-device", { exact: true }).waitFor();
      assert.deepEqual(errors, [], "page transitions must not render the previous resource DTO");
    }
    // Keep responses in flight while switching incompatible row shapes, then check the final page.
    await page.route(`${base}${userBase}/*`, async (route) => {
      if (/\/(agents|ingresses|egresses)$/.test(new URL(route.request().url()).pathname)) await delay(150);
      await route.continue().catch(() => {});
    });
    for (const label of ["Agent 管理", "Ingress 管理", "Egress 管理", "Agent 管理"]) await nav(page, label);
    const finalRoles = await apiAction(page, () => nav(page, "Ingress 管理"), userBase + "/ingresses");
    assert.equal(finalRoles.items[0].agentId, enrolled.agent.id);
    await page.getByText("ui-device", { exact: true }).waitFor();
    await page.unrouteAll({ behavior: "wait" });
    const servers = await apiAction(page, () => nav(page, "Server 管理"), userBase + "/servers");
    assert.equal(servers.pagination.total, 1);
    assert.equal(servers.items[0].name, "demo");
    await page.getByText("demo", { exact: true }).waitFor();
    const routes = await apiAction(page, () => nav(page, "路由管理"), userBase + "/routes");
    assert.ok(routes.items.some((item) => item.kind === "egress" && item.targetId === enrolled.agent.id));
    assert.ok(routes.items.some((item) => item.kind === "serve" && item.targetType === "server"));
    await page.getByText("ui-egress", { exact: true }).waitFor();
    // The switcher moves to another Workspace; the same page shows only its data.
    await page.getByRole("button", { name: "切换工作空间" }).click();
    await page.getByRole("menuitem", { name: /aaa-shared/ }).click();
    await page.waitForURL(base + `/workspaces/${sharedWorkspace.id}/overview`);
    const scoped = await apiAction(page, () => nav(page, "路由管理"), `/v1/workspaces/${sharedWorkspace.id}/routes`);
    assert.equal(scoped.pagination.total, 0);
    await page.getByText("ui-egress", { exact: true }).waitFor({ state: "hidden" });
    // The owner of the personal Workspace manages its Service Accounts.
    await page.goto(base + `/workspaces/${workspace.id}/members`);
    const accounts = await apiAction(page, () => page.getByRole("tab", { name: "服务账号", exact: true }).click(), userBase + "/service-accounts");
    assert.equal(new URL(page.url()).searchParams.get("tab"), "service-accounts", "the tab is part of the address");
    assert.equal(accounts.pagination.total, 1);
    await page.getByText("ui-fleet", { exact: true }).waitFor();
    await page.getByRole("button", { name: "创建服务账号", exact: true }).click();
    await page.getByLabel("服务账号名称").fill("ui-created");
    const accountCreation = page.waitForResponse((response) => new URL(response.url()).pathname === userBase + "/service-accounts" && response.request().method() === "POST");
    const [createdAccount] = await Promise.all([accountCreation, page.getByRole("dialog").getByRole("button", { name: "创建", exact: true }).click()]);
    assert.equal(createdAccount.status(), 201);
    await page.getByText("ui-created", { exact: true }).waitFor();
    t.diagnostic("Service Account management passed.");
    if (hydraConfig) {
      // `agentd login`: the device side runs here, the person confirms in the Console.
      const instance = await api("GET", "/v1/instance", undefined, "");
      const device = instance.deviceAuthorization;
      assert.ok(device, "instance must advertise Device Flow");
      let started;
      for (const until = Date.now() + 10_000; ; await delay(100)) {
        const response = await fetch(hydraPublic + "/oauth2/device/auth", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({ client_id: device.clientId, scope: device.scope, audience: device.audience }) });
        if (response.ok) { started = await response.json(); break; }
        assert.ok(Date.now() < until, "Control did not register the device client");
      }
      await page.goto(started.verification_uri_complete);
      await page.getByLabel("验证码").fill(started.user_code);
      await page.getByRole("button", { name: "继续", exact: true }).click();
      await page.waitForURL(/\/device\//);
      await page.getByText("注册 agentd 设备", { exact: true }).waitFor();
      await page.getByText(started.user_code, { exact: true }).waitFor();
      await page.getByRole("radio", { name: /个人空间/ }).click();
      await page.getByRole("radio", { name: /服务账号 ui-fleet/ }).waitFor();
      await page.getByRole("radio", { name: /我自己/ }).click();
      await page.getByRole("button", { name: "确认注册", exact: true }).click();
      await page.getByRole("heading", { name: "设备已授权" }).waitFor();
      let token;
      for (const until = Date.now() + 10_000; !token; await delay(1_100)) {
        const response = await fetch(hydraPublic + "/oauth2/token", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({ grant_type: "urn:ietf:params:oauth:grant-type:device_code", device_code: started.device_code, client_id: device.clientId }) });
        if (response.ok) token = (await response.json()).access_token;
        assert.ok(token || Date.now() < until, "the confirmed authorization did not issue a token");
      }
      localTokens.add(token);
      const registered = await api("POST", "/v1/agent-enrollments", {
        instance: base, endpoint: base + "/agent", workspace: workspace.name, name: "ui-login", desired: { ingress: { enabled: true } },
      }, token, 201);
      for (const [key, value] of Object.entries(registered.credentials)) if (key.endsWith("Token") && value) localTokens.add(value);
      assert.deepEqual(registered.agent.principal, { type: "user", id: user.user.id });
      t.diagnostic("Device Flow confirmation through the Console and a real Hydra passed.");
    }
    assert.equal(calls.some((call) => call.session === "user" && call.path.startsWith("/v1/admin/")), false, "ordinary pages must never request admin resources");
    assert.equal(calls.some((call) => /\/(?:api\/)?v1\/state$/.test(call.path)), false, "aggregate state requests are forbidden");
    assert.deepEqual(errors, [], "Console must not raise JavaScript errors");
    t.diagnostic("Personal-workspace default, addressable pages, rapid navigation, routes and workspace switching passed without aggregate/admin requests or JavaScript errors.");
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
