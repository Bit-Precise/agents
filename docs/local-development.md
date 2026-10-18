# agents / agentd 运行手册

本手册使用真实控制 API、数据库、控制台和 Edge，不使用测试 fixture。localhost HTTP 是明确的 loopback 开发模式，对外部署必须使用 HTTPS。

下面命令从 agents 仓库根目录运行，假设独立客户端源码在相邻的 `../agentd`。服务端镜像和 Helm chart 在本仓库发布，客户端安装包在 agentd 仓库发布。

## 1. 构建与初始化服务端

```sh
export GOPRIVATE=github.com/bit-precise/agentd
go mod download
make build
make -C ../agentd agentd
bin/agents init --config .run/agents.yaml
bin/agents serve --config .run/agents.yaml
```

Windows 使用对应 `.exe`。默认控制台为 `http://localhost:8080/`（页面地址即路由，例如 `/workspaces/<id>/agents`，实例管理员页面在 `/admin/...`），接入端点为 `http://localhost:8080/agent`；管理员 Token 位于 `.run/state/agents/admin.token`，SQLite 位于同目录的 `agents.db`。

`init` 只输出文件位置，不显示 Token、不覆盖已有配置或密钥。只在自己的终端/编辑器中查看 Token 并登录，不把它放进 URL、命令参数、截图或工单。管理员不隐式绕过业务授权。

在侧边栏顶部的工作空间菜单中创建 `demo` Workspace；任何用户都可以创建，创建者自动成为 owner（实例管理员以外的用户最多拥有 20 个，`personal-` 前缀保留给个人空间）。设备有两种注册方式，都没有人工审批步骤：

- **个人设备**用 `agentd login`：在浏览器里登录，确认 Workspace，并选择以自己还是以服务账号运行。需要本机 Hydra（下一小节）。
- **无人值守设备**用服务账号的注册密钥：在“成员管理 → 服务账号”创建服务账号（例如 `local-runner`），在其操作菜单中点“创建注册密钥”，选择 `ingress/serve/egress` 并设定有限期限与一次使用。密钥的角色就是该设备可启用角色的上限，完整密钥仅展示一次，可下载为文件。把它保存为 `.run/state/agents/demo.enrollment.key`。该目录由 init 建立为私有目录。Linux/macOS 文件权限设为 0600；Windows 要求仅当前账号、SYSTEM/Administrators 的私有 DACL，不能靠 Unix chmod mode 替代。

### 本机 Hydra（`agentd login` 需要）

下载 [Ory Hydra v26.2.0](https://github.com/ory/hydra/releases/tag/v26.2.0) 二进制，用示例配置在另一个终端启动。内存存储，重启后进行中的登录失效：

```sh
SECRETS_SYSTEM="$(openssl rand -hex 32)" hydra serve all --dev --config examples/hydra/local.yaml
```

在 `.run/agents.yaml` 中加入以下内容后重启 `agents serve`。Control 会自动在 Hydra 注册 `agentd` 公共客户端；没有 OIDC 时，只有 `developmentHTTP` 实例接受用令牌登录的控制台会话来确认设备：

```yaml
hydra:
  enabled: true
  publicURL: http://127.0.0.1:4444
  adminURL: http://127.0.0.1:4445
```

## 2. 注册与运行 Agent

另开终端启动源站：

```sh
mkdir -p .run/origin
printf 'hello through agents\n' > .run/origin/index.html
python3 -m http.server 8090 --bind 127.0.0.1 --directory .run/origin
```

准备客户端 profile：

```sh
cp ../agentd/examples/agentd/local.yaml .run/agentd.yaml
../agentd/bin/agentd config validate --config .run/agentd.yaml
```

个人设备执行 `agentd login`。它打开浏览器（`--no-browser` 只打印地址），终端和页面显示同一个验证码；在页面核对验证码、登录控制台、选择 `demo` 和“我自己”后确认，终端随即完成注册：

```sh
../agentd/bin/agentd login --config .run/agentd.yaml
```

无人值守设备改用注册密钥：

```sh
chmod 600 .run/state/agents/demo.enrollment.key  # Linux/macOS
../agentd/bin/agentd enroll --config .run/agentd.yaml --key-file .run/state/agents/demo.enrollment.key
```

config.yaml 的 Workspace 必须与浏览器中确认的（或密钥所属的）Workspace 一致，instance/endpoint 必须与服务端 serverURL/edgeURL 一致。端口变化时两侧一起修改。在浏览器中拒绝后 Hydra 不会通知终端，按 Ctrl+C 结束等待即可。

Control 自动校验 `local-agent` 声明的 `hello → http://127.0.0.1:8090` 和 `example.com:443` 出口范围：在角色上限内即生效（个人设备随你的成员角色，服务账号设备随确认或密钥给出的角色），越权、非法配置或名称冲突会让注册直接失败并给出原因。随后运行：

```sh
../agentd/bin/agentd run --config .run/agentd.yaml
```

login 和 enroll 都把持久注册 ID、运行主体、独立控制/访问/提供方 Token 及生效配置保存到 `.run/state/agentd/credentials.json`；Device Flow 的 access token 只用于一次兑换，不写入磁盘。重复注册不会覆盖现有身份；设备被撤销或凭据过期后，用 `agentd login --replace`（或 `enroll --replace`）在 Control 确认旧凭据失效后重新注册。

## 3. 验证 Serve

服务页面出现 `hello.demo.localhost`。新服务首次发布时创建有期限的本 Workspace 成员授权（默认 7 天，见 Control 配置 `serviceGrantTTL`）；之后不会因重载而恢复或延长。

用浏览器打开控制台提供的服务链接并完成服务登录跳转，应看到 `hello through agents`。服务使用自己的 host-only 会话，中央控制台 Cookie 不发送到源站。

如果系统不解析 `*.localhost`，在本机 hosts 中将 `hello.demo.localhost` 指向 `127.0.0.1`。也可明确为该服务添加短期匿名授权后执行下列检查；命令不会自动公开服务：

```sh
curl --noproxy '*' --resolve hello.demo.localhost:8080:127.0.0.1 http://hello.demo.localhost:8080/
```

没有匿名授权的非浏览器请求需要有效用户身份，401/403 不等于隧道故障。授权可以面向单用户、Workspace 或匿名访客，匿名仅适用于服务。删除所有有效授权不会退回全员访问。

## 4. 验证指定出口

创建出口路由：Workspace `demo`、Agent `local-agent`、精确域名 `example.com`、端口 443、TLS。再给运行该 profile 的主体添加有限期出口授权：个人设备授权给该用户或 demo 成员，服务账号设备授权给该服务账号（Workspace 成员授权不包括服务账号）。

```sh
curl --noproxy '' --proxy socks5h://127.0.0.1:1080 https://example.com/
curl --noproxy '' --proxy http://127.0.0.1:8088 https://example.com/
```

出口所在网络需能访问该网站，客户端继续验证网站原证书。创建路由、注册设备或打开 egress 都不会自动获得使用权。重定向到其他域名的新 TLS 连接仍需相应路由与授权。

未匹配请求被拒绝；Edge 不会改选其他 Agent；provider 再次核对生效配置与本地范围。loopback/link-local/metadata 等地址默认禁止，不能靠 DNS 将获准域名改指本机任意服务。

## 5. 更新、撤销与生命周期

- Linux/macOS 修改 Serve/Egress 配置或 ingress routes 后发 SIGHUP。先收紧本地，再提交 desired；网络失败会重试，400/403/409 拒绝不重试并记入设备的 `lastRejection`。Windows 修改后重启。监听、身份或 transport 的变更需重启，但不会阻止同次重载中的收紧。
- 扩展在 Control 自动校验通过后立即生效，无需任何人批准；被拒时上一份生效配置继续运行。局部收紧/撤销使用独立流取消，生效配置 revision 变化也不会重建共享连接。
- agentd 约每 2 秒刷新生效配置；凭据被拒（401）立即停止，Control 不可达时继续使用已同步配置至其 24 小时有效期结束。请求本身有超时，这些数值不是生产 SLO。
- 每条流受授权、策略与会话期限约束；短租约续期重新授权，失败则关闭，还有流/会话硬上限。重连不迁移旧流、不重放业务。最终期限见 [Edge](../edge/README.md) 与 [connection](https://github.com/Bit-Precise/agentd/blob/main/connection/README.md)。
- Token、注册密钥、授权各自有期限。在线 Agent 的 Token 剩余不足 7 天时可续为 30 天，保持 TokenID/hash，并不替换秘密。离线超过 30 天后重新执行 `login --replace` 或用新注册密钥执行 `enroll --replace`；同一主体可沿用原名称，但旧注册不会复活。
- 控制台用户页可更新本人 Token，管理员也可为其他用户重签。完整新 Token 仅展示一次，旧用户 Bearer 立即失效；已有浏览器会话仍按原到期时间生效。禁用用户与重签 Token 是不同操作。
- agents 停止会关闭连接目录与活动流；重启恢复 SQL 中注册身份与策略，Agent 重新连接。浏览器内存会话需要重新登录。

## 6. HTTPS、CDN 与数据库

[生产示例](../examples/agents/config.yaml) 使用 HTTPS。可执行 `agents init --development-http=false --server-url ... --edge-url ... --listen ... --tls-cert ... --tls-key ...` 初始化。证书与 DNS 由部署者提供；控制域、connect 域与服务域都需匹配，不自动申请 ACME。

CDN 属于信任边界，必须 HTTPS 回源。禁用认证/服务响应缓存，透传所需头和查询参数，认证回源来源，确保同一 packet-up 的 GET/POST 到同一 owner。WAF、长连接期限、流式响应和费用需针对实际 CDN 验收。

默认 SQLite，也可设置 `database.type: postgres` 与 DSN。pgx 和 pure-Go SQLite 无外部 SQLite 动态库依赖。DSN 若含秘密应保护配置文件；YAML 不执行环境变量插值。

更换数据库类型或 DSN 不会自动迁移原有身份、注册与授权。两种后端需要各自备份恢复，当前没有跨数据库导入迁移工具。

后端分为 `control/types → control/state → control/db`；HTTP/浏览器 broker 在 `control`，连接和流在 `edge`。这借鉴 Headscale 的职责分层，未引入其协议、API 或数据库 schema。每类实体有 SQL 表和组合约束，但当前在单进程维护状态快照，以事务和 revision 条件更新持久化；多个 agents 共享数据库不等于高可用。

先停服再备份 SQLite 与私有状态；运行时直接复制数据库文件不是一致性备份。PostgreSQL 的权限、备份、恢复和连接容量需部署验收。

## 7. 验证与尚未交付功能

```sh
go test -mod=readonly -race ./control/servertest ./control/state ./control/db ./edge
(cd ../agentd && go test -mod=readonly -race ./integration ./connection ./transport/xhttp)
```

Device Flow 测试默认使用模拟 Hydra。设置 `AGENTS_TEST_HYDRA_BIN` 指向 Hydra v26.2.0 二进制后，`go test ./control` 和控制台冒烟测试（`npm run test:ui`）会额外针对真实 Hydra 跑完整的登录、确认与兑换链路。

CI 的 PostgreSQL 任务设置 `AGENTS_TEST_POSTGRES_DSN`，运行 `TestPostgresPersistenceAndConstraints`。测试创建独立随机 schema 并清理，不删除公共 schema；测试账号需要 CREATE schema 权限。未提供 DSN 时仅该测试跳过，常规 SQLite 测试仍执行。

数据面测试不依赖公网目标，通过可信注入拨号器访问本机 TLS 服务；不意味着生产开放任意 loopback 出口。“在线”表示已认证且版本匹配的连接，不代表每个源站都已主动探测。

尚未交付：完整公钥签名快照/能力体系、TUN、WSS、分布式控制/Edge、自动证书、生产指标告警与生产容量承诺。

## 8. 独立组件与 OIDC

`agents control`、`agents edge`、`agents console` 分别启动各自的组件；`agents serve` 保留上面的开发方式。三组件生产部署请使用 [Helm 部署说明](helm.md)。公开服务域与控制域由 HTTPS ingress 的同一标准端口路由；内部组件监听端口不是浏览器公开 SSO 端口。

Control 独占身份和策略状态，Edge 通过受认证 metadata RPC 查询授权，业务请求体不发送到 Control。Control 不可达时 Edge 拒绝新流并关闭受影响业务；Edge 不可达时 Control 保留持久配置管理。

启用 OIDC 后，Console 的主登录入口跳转到配置的身份提供方。身份按 issuer + subject 绑定，不按 email 合并。新用户拥有仅自己为 owner 的 `personal-<userID>` Workspace，已有用户不补建；其他 Workspace 的访问仍需相应成员关系或授权。bootstrap 管理员 subject 必须显式配置；`oidc.adminEmails` 也可按已验证 ID token 的邮箱授予实例管理员，必须同时有布尔值 `email_verified: true`。名单移除后重启 Control 会撤销邮箱派生的管理员权限，加入名单的旧用户需重新 OIDC 登录才提升。自动化和应急管理仍可使用 Token。
