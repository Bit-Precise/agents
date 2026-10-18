# Console

管理界面使用 React、TypeScript、Vite 和 Tailwind CSS。需要 Node.js 22.12 或更新版本，仓库推荐 Node.js 24；依赖版本与完整性固定在 `package-lock.json`。

从本目录执行：

```sh
npm ci
npm run generate
npm run typecheck
npm run build
npm run check:generated
```

`build` 已包含类型检查。`check:generated` 重新生成 SDK，并检查已跟踪的生成文件是否相对 Git 有差异；修改契约后应一起提交生成结果，使 CI 能检查同步状态。

## API 与组件来源

唯一 API 契约是 [`../api/openapi.json`](../api/openapi.json)。[`openapi-ts.config.ts`](openapi-ts.config.ts) 使用固定版本的 `@hey-api/openapi-ts` 生成 `src/api/generated/` 下的类型、客户端和操作函数。所有 JSON 请求都调用生成的 SDK；修改 API 时更新契约并重新生成，不手写请求封装或修改生成文件。OIDC 等浏览器导航与 JSON 请求分开处理。

`src/components/ui/` 是官方 shadcn/ui CLI 生成的真实组件源码，使用 `base-nova` Base UI 预设，配置见 [`components.json`](components.json)。CLI 版本、配套 CSS、许可证和添加组件的方法见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

## 构建产物与验证

Vite 将生产产物写入 [`../control/assets/web/`](../control/assets/web/)，由 [`../control/assets/assets.go`](../control/assets/assets.go) 嵌入 Go 程序。前端源码修改后需要重新构建并一并提交该目录；普通 Go 构建与测试使用已提交产物，不需要 npm。

浏览器验收推荐使用同源生产构建：先完成上述构建，再从仓库根目录按照[本机运行手册](../docs/local-development.md) 构建并运行 `agents serve`，访问 `http://localhost:8080/console/`。已有配置时无需重复初始化。

`npm run dev` 提供 Vite 热更新，并将 `/api`、`/auth` 代理到 `http://localhost:8080`。Vite 页面的 Origin 与后端不同，写操作可能被后端 Origin/CSRF 检查拒绝，认证跳转也需要同源验证。开发代理不代表生产认证行为；使用同源服务完成登录、写操作和 OIDC 验收，不放宽后端校验。

## 浏览器安全边界

[`src/api/session.ts`](src/api/session.ts) 配置生成客户端使用 `credentials: 'same-origin'`。唯一自定义请求拦截器为写操作注入内存中的 CSRF 值，退出登录时清空。登录令牌与一次性展示的秘密仅保留在界面内存中，不写入 `localStorage` 或 `sessionStorage`；登录提交结束或秘密对话框关闭后清空。复制或下载秘密只在用户点击时发生。

嵌入资源处理器的 CSP 限制脚本为 `script-src 'self'`，禁止内联脚本与 `eval`。样式允许 `style-src 'self' 'unsafe-inline'`，用于 Base UI 弹层定位；该例外仅适用于样式。生产浏览器验收应经过此处理器，覆盖对话框、下拉框和窄屏布局。
