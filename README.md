# agents

服务发布与 TLS 域名出口的服务端。设备客户端在独立仓库 [Bit-Precise/agentd](https://github.com/Bit-Precise/agentd)。本仓库按 Headscale 的命令入口、control、types/db/state、servertest、配置示例及 packaging 分层组织，使用 agents 自己的协议和实体。

## 组件

| 进程 / 镜像 | 职责 |
|---|---|
| `agents control` / `ghcr.io/bit-precise/agents-control` | SQL 持久化、注册与配置自动校验、授权、用户/浏览器身份和内部策略 API |
| `agents edge` / `ghcr.io/bit-precise/agents-edge` | XHTTP 接入、连接目录、Serve/Egress 数据转发；通过受认证元数据 API 查询 Control |
| `agents console` / `ghcr.io/bit-precise/agents-console` | 静态管理界面；Control 同源代理，浏览器凭据不会交给静态组件 |

三个镜像分别运行各自的子命令，不各自启动一份数据库/控制面。应用请求体和响应体仅通过 Edge 到 provider，不绕经 Control。`agents serve` 保留单进程本机开发模式。

## 构建与本机运行

需要 Go 1.27.2 和 Node.js 24。服务端固定依赖 agentd SDK 的提交版本；仓库为私有时需有该仓库的只读 Git 权限。

```sh
export GOPRIVATE=github.com/bit-precise/agentd
# 使用你已有的 GitHub SSH 身份，不要把令牌写入 go.mod 或镜像。
git config --global url."ssh://git@github.com/".insteadOf "https://github.com/"
go mod download
make build
bin/agents init --config .run/agents.yaml
bin/agents serve --config .run/agents.yaml
```

初始管理员令牌只写入私有文件。完整设备注册、Serve/Egress 验证见[本机运行手册](docs/local-development.md)。设备端配置、安装包和系统服务属于 agentd 仓库。

## Kubernetes

提供 [Helm chart](charts/agents) 和[部署说明](docs/helm.md)。三个 Deployment 各有独立 Service；Control 使用 SQLite PVC 或外部 PostgreSQL；Edge 不挂数据库卷。Control/Edge 当前必须单副本，升级会让连接重建，不支持多控制节点或 Edge 自动迁移。

管理员令牌、组件间令牌和 PostgreSQL DSN 使用预先创建的 Secret。`tlsOffload` 仅显式用于可信 HTTPS ingress 之后，组件间元数据 API 仍检查共享令牌；请限制后端端口访问。

服务域 `<service>.<workspace>.<instance-host>` 的 DNS/证书必须覆盖实际层级。单张 `*.agents.example.com` 证书不覆盖 `hello.demo.agents.example.com`。

## 发布

推送 main/PR 会执行 race、PostgreSQL、Helm 模板检查，构建三个组件的 linux/amd64 与 linux/arm64 镜像，并在 Kind 中安装 Helm chart。普通分支构建不发布版本。

推送语义版本 tag（例如 `v0.1.0`）后，相同检查通过才发布三个 GHCR 多架构镜像、OCI Helm chart 和 GitHub Release 的 chart/镜像索引/SHA256SUMS。镜像标签与 Helm appVersion 使用同一个 tag。

CI 的 `AGENTD_DEPLOY_KEY` 是仅有 agentd 读取权限的 deploy key；Docker 构建通过 BuildKit SSH 转发，密钥不进入镜像层。所有工作流动作固定到官方不可变提交。

```sh
docker buildx build --ssh default --platform linux/amd64,linux/arm64 --target control -t agents-control:dev .
# --target edge / --target console 分别生成其他组件
```

## 实现与验证

- [实体、状态机与关系（权威）](docs/model.md)
- [组件、路由与接口](docs/backend-implementation.md)
- [User / Service Account 与 Hydra Device Flow 设计](docs/identity-and-device-flow.md)（区分设备归属、运行主体与登录操作人，接入不设人工审批）
- [Edge 授权与租约](edge/README.md)
- [来源与依赖](THIRD_PARTY_NOTICES.md)

```sh
make test lint
make race
helm lint charts/agents --strict
```

Console 使用 React、TypeScript、shadcn/ui。前端 API 类型、客户端和操作函数由 [OpenAPI 契约](api/openapi.json) 生成；修改契约后执行 `make generate`，CI 会检查生成文件与构建产物是否同步。管理 API 按 `/v1/admin/*` 和 `/v1/workspaces/{workspaceId}/*` 区分实例管理与工作空间资源，所有集合支持编号分页和服务端筛选；旧聚合 `/api/v1/state` 已移除。设备控制、OAuth 和 XHTTP 协议保持兼容。

认证支持标准 OIDC discovery、授权码 + PKCE、state/nonce 和 ID token 校验，并按 issuer + subject 绑定用户。新用户获得仅自己为 owner 的 `personal-<userID>` Workspace，已有用户不补建。配置中的 bootstrap 管理员 subject 仅首次创建账户时生效；`oidc.adminEmails` 可按已验证邮箱授予实例管理员，要求 ID token 的 `email_verified` 为布尔值 `true`，移除名单后重启 Control 会撤销邮箱派生的管理员权限。Token 登录保留给自动化与应急管理。

Agent 属于 Workspace，运行主体恰为一个 User 或单 Workspace 的 Service Account。个人设备执行 `agentd login`：经 Hydra 的 OAuth Device Flow，本人在浏览器完成 SSO、选择 Workspace 与本人/SA 身份，再以限定用途、一次性的 access token 换取每设备独立凭据；无人值守设备使用服务账号的注册密钥。身份、权限与策略自动校验通过即接入，没有人工审批；配置变更也按策略直接接受或拒绝。SA 由 maintainer 及以上管理，不能登录 Console。实体与状态机见 [model](docs/model.md)。

授权分两层：控制面 RBAC 决定 owner / maintainer / developer / guest 能执行哪些管理操作；数据面 Grant 决定匿名访问者、所有已登录用户、指定用户、工作空间成员或服务账号能使用哪个服务或出口路由。两者的完整表格见 [授权模型](docs/authorization.md)。

目前没有真实 CDN 兼容性验收、自动秘密轮换或多 Edge 目录。OIDC（v1→v2）、取消审批（v2→v3）、Service Account（v3→v4）与 `authenticated` 授权主体（v4→v5）都是显式数据库迁移，保留现有用户、设备、授权和凭据。Control 启动时需要身份提供方 discovery 可达；浏览器会话不超过 ID token 到期时间及 8 小时，不持久化 IdP tokens。
