# 实体、状态机与关系（当前实现）

本文是 agents / agentd 当前实现中实体、状态与关系的唯一权威说明，以代码为准：`control/types`、`control/state`、`edge`，以及 agentd 的 `api`、`connection`、`orchestration`、`supervisor`。其他文档描述当前行为时应链接本文，不再各自重复字段、状态或时限。

- **身份与注册设计**：Service Account、typed principal 与 Hydra Device Flow 的设计理由、威胁边界和验收标准见 [身份与 Device Flow 设计](identity-and-device-flow.md)；当前实现以本文为准。
- **授权**：控制面 RBAC（角色 × 权限矩阵）与数据面 Grant（主体 × 资源）见 [授权模型](authorization.md)，代码在 `control/authz`。
- **远期架构**：Tunnel 分层、gateway 独立进程、分享邀请、证书 worker、多 Edge，见 agentd 仓库的 `docs/agents-agentd-architecture.md`。

设备接入和配置变更都没有人工审批，Control 自动校验后直接接受或拒绝。旧模型里的 `Approval`、`pendingApproval`、`/approval` 和 `autoApprove` 已经删除，存量数据由 DB v3 迁移转换（见第 6 节）。

## 1. 实体

| 规范名 | 代码 | 归属与持久化 | 要点 |
|---|---|---|---|
| User | `types.User` | 实例级；表 `users` | 身份唯一键是 OIDC `(issuer, subject)`，存于 `OIDCIdentity`。`Email`（仅已验证）与 `Picture`（仅 HTTPS）是每次 SSO 登录刷新的展示数据，不参与身份识别。`Admin` 即 instance admin；对邮件名单管理的用户，`Admin` 由 `AdminFromEmail` 派生。停用是单向操作 |
| Workspace | `types.Workspace` | 表 `workspaces` | 名称是不可变的 DNS label。新用户自动获得 `personal-<userID>`；任何用户都可以再创建 Workspace 并成为 owner（实例管理员以外最多拥有 20 个，`personal-` 前缀保留） |
| Membership | `types.Membership` | `(workspace, user)` 复合主键 | 角色为 `owner / maintainer / developer / guest`，各角色的控制面权限见 [授权模型](authorization.md)。删除即退出 Workspace，并级联撤销设备 |
| Service Account | `types.ServiceAccount` | 固定属于一个 Workspace；表 `service_accounts`，Workspace 内名称唯一 | 机器主体，不登录 Console，没有 user Token。名称和归属创建后不变，只能修改说明。由 maintainer/owner 或 instance admin 管理；撤销是终态 |
| Agent | `types.Agent`（持久记录）、`api.Agent`（API 视图） | Workspace 内名称唯一；主体 `Principal` 恰为一个 User 或一个同 Workspace 的 Service Account | `Principal`（`{type: user\|service_account, id}`）、`CreatedBy`（注册操作人，只用于展示和审计，从不参与权限判断）、`Status`、`Roles`（角色上限）、`Desired`（AgentSpec）、`Effective`（EffectiveConfig）、`LastRejection`、`RevokedAt`。设计文档称之为 AgentRegistration |
| AgentSpec | `api.AgentSpec` | 存于 Agent 的 `Desired` | 由 config.yaml 派生的服务端相关子集：各角色是否启用、serve 服务名与 origin、egress allow/deny。本地路径、监听地址、承载参数和 ingress routes 不上传 |
| EffectiveConfig | `api.EffectiveConfig` | 存于 Agent 的 `Effective` | `Desired ∩ 角色上限 ∩ Workspace 内置策略`，由 Control 计算，不是批准记录。`Revision` 是每个 Agent 自己的单调计数，只在内容变化时递增。`ExpiresAt` 在读取时填为当前时间加 24 小时，只限制 agentd 离线时还能用多久 |
| EnrollmentKey | `types.EnrollmentKey` | 表 `enrollment_keys`，与同 ID 的 enrollment Token 一起撤销 | 无人值守注册用。新密钥只绑定 `ServiceAccountID`，设备以该 SA 运行；`CreatedBy` 记录创建者。迁移保留的旧密钥只有 `UserID`（legacy，仍可用至到期）。另有角色上限、次数和期限 |
| EnrollmentAuthorization | `types.EnrollmentAuthorization` | 表 `enrollment_authorizations` | 一次 `agentd login`（Device Flow）的服务端记录：发起确认的人 `ActorUserID`、Hydra 登录 challenge、确认后固定的 Workspace、`Principal` 与 `AllowedRoles`，以及兑换出的 `AgentID`。有效期 10 分钟，结束 24 小时后清理；审计保留永久痕迹 |
| Token | `types.Token` | 只保存哈希 | 种类：`user / enrollment / control / provider / access`，用途互不替代。设备 Token 绑定 Agent 的主体：`UserID` 或 `ServiceAccountID` 恰有其一。SA 没有 user Token，不能登录 Console 或调用管理 API |
| Service | `types.Service` | `(workspace, name)` 唯一，hostname 全局唯一 | 固定属于一个 Agent。Desired 声明服务名时即预留名称，主机名为 `<service>.<workspace>.<base>`。`Enabled` 是管理员的发布开关。API 视图名为 server，route 视图里是 `kind=serve` |
| EgressRoute | `types.EgressRoute` | Workspace 内名称唯一 | 一条域名/端口/TLS 规则加优先级，固定指向一个出口 Agent。只决定选路，不授予访问权 |
| Grant | `types.Grant` | 资源和主体用 XOR 约束 | 数据面授权。资源为 Service 或 EgressRoute，主体为 `anonymous / authenticated / user / workspace / service_account`：`anonymous`（任何人）与 `authenticated`（本实例任何已登录用户）只能用于 Service 且不带主体 ID；`workspace` 只匹配人类成员，不包括其中的 SA；`service_account` 只能用于 EgressRoute 且须同 Workspace（浏览器 Serve 没有机器消费路径）。完整规则见 [授权模型](authorization.md)。有限时间窗，`Revoked` 不可恢复。`External` 表示经 `grants.share_external` 授权的对外分享，`CreatedBy` 记录创建者 |
| names | 表 `names` | 每次保存时从 Agents 和 Services 推导 | Agent 名和服务名共用同一个 Workspace DNS 命名空间 |
| Audit | `types.Audit` | 只追加 | `Detail` 记录原因，例如自动拒绝的理由、迁移前的旧审批快照 |

运行期实体只存在于内存，不持久化：

| 规范名 | 代码 | 生命周期 |
|---|---|---|
| Connector | agentd 进程启动时随机生成的 `ConnectorID` | 一个进程一个。同一 Agent 在 Edge 上同一时刻只有一个 provider Connector |
| provider / access 会话 | Edge 的 `provider` 结构体、`connection.Session` | 认证后建立；provider 会话**握手完成后**才写入目录，替换同一 Connector 的旧会话。会话本身不续期，最长 24 小时 |
| Flow | Edge 的 `flow`、两侧各一份 `connection.Stream` | 每条业务流独立一份租约（55 秒，过半续期）和一个硬上限（12 小时）；按内容逐条复核 |
| Decision | `types.Decision` | Control 对一次 Serve/Egress 请求做出的授权决定。`Consumer` 是使用资源的主体：Serve 为浏览器 User（匿名为空），Egress 为 ingress Agent 的主体（User 或 SA），从不替换为提供方或操作人；`Revision` 是当时出口/服务 Agent 的 Effective revision |

## 2. 关系

```mermaid
erDiagram
    User ||--o{ Membership : joins
    Workspace ||--o{ Membership : contains
    User ||--o{ OIDCIdentity : "issuer/subject"
    User ||--o{ Agent : "principal (runs as)"
    ServiceAccount ||--o{ Agent : "principal (runs as)"
    Workspace ||--o{ ServiceAccount : contains
    Workspace ||--o{ Agent : owns
    Workspace ||--o{ EnrollmentKey : issues
    ServiceAccount ||--o{ EnrollmentKey : "enrolls as"
    User ||--o{ EnrollmentAuthorization : confirms
    EnrollmentAuthorization |o--o| Agent : "exchanged for"
    ServiceAccount ||--o{ Grant : "egress subject"
    Agent ||--o{ Service : publishes
    Agent ||--o{ EgressRoute : "exit of"
    Service ||--o{ Grant : authorizes
    EgressRoute ||--o{ Grant : authorizes
    Agent ||--o{ Token : "control/provider/access"
    User ||--o{ Token : "user API"
```

- 主体与操作人分离：Agent、设备 Token 和 Egress 消费者都携带 typed principal（`user:<id>` 或 `service_account:<id>`）。developer 对"自己的设备"的判断是 `principal.type == user && principal.id == actor`；SA 设备由 manager 管理。创建者 `CreatedBy` 永远不参与这类判断，SA 设备也不继承其创建者或操作人的 Membership 和 Grant。
- 一个 Agent 同时可以承担 ingress、serve、egress 三种角色。serve 和 egress 共用 provider 会话，ingress 使用独立的 access 会话。
- 角色名与协议名的对应：本机角色 `ingress` = 会话角色 `access` = 流用途 `access` = `AccessToken`；本机角色 `serve` / `egress` = 会话角色 `provider` = 流用途 `serve` / `egress` = `ProviderToken`。`ingress` 只指本机访问角色；旧文档中的 IngressRule 现称 ServeOriginRule，是 Serve 源站规则。
- 代码中的 `gateway` 包负责组装一个可独立运行的 Edge 进程，不是设计文档里那个浏览器鉴权组件。

## 3. 状态机

### Agent

```mermaid
stateDiagram-v2
    [*] --> active: login 兑换或 enroll（主体、角色上限、名称、配置自动校验通过）
    active --> active: Desired 更新被接受 → Effective 重算，内容变化时 revision+1
    active --> active: Desired 更新被拒绝（400/403/409）→ 记录 LastRejection，Effective 保持不变
    active --> active: Membership 角色变化 → 重算 Effective；被裁掉的角色写入 LastRejection
    active --> revoked: 撤销 / 主体失效（User 移出 Workspace 或停用，SA 撤销）
    revoked --> [*]: 名称被回收（同一主体可立即回收，其他主体须等 7 天墓碑期）→ 清除记录
```

- 只有 `active / revoked` 两个状态，没有"待批准"。状态以存储值为准，所有主体变化都通过级联写入，不在读取时另行推导。`agentAllowed` 只是兜底的防御检查。
- `revoked` 是终态，同一条记录不会复活。重新接入时 agentd 执行 `login --replace` 或 `enroll --replace` 获得新的 Agent ID；旧记录及其 Token、Service、Grant、出口路由被一并清除，不会被新设备继承。
- 角色上限：User 设备随其 Membership（guest 只有 ingress，其余三种角色）；SA 设备为注册时确定的 `Roles`（login 选择 SA 时为三种角色，密钥注册时为密钥角色），不随任何人的 Membership 变化。
- 自动校验的内容：注册凭据可用（未用尽的密钥，或已确认且未兑换的 EnrollmentAuthorization）；主体有效（User 启用且是成员，或 SA 有效且属于该 Workspace）；Workspace 与 config.yaml 中的提示一致；Desired 启用的每个角色都在角色上限内；名称/服务名没有冲突；origin 与规则合法。任一项失败，整份配置被拒绝，不会进入任何等待队列。

### EnrollmentAuthorization（`agentd login`）

```mermaid
stateDiagram-v2
    [*] --> awaiting_user: Hydra 登录请求到达，且浏览器有已通过 SSO 的人类会话
    awaiting_user --> authorized: 本人确认 Workspace 与主体（重检权限）→ Hydra accept login(subject=主体)
    awaiting_user --> revoked: 本人拒绝
    authorized --> consumed: 兑换（introspection 后，在同一事务内重检权限、创建 Agent 和凭据）
    authorized --> revoked: 本人拒绝
    awaiting_user --> expired: 10 分钟
    authorized --> expired: 10 分钟
```

- `expired` 在读取时派生，不回写；`consumed`、`revoked`、`expired` 都是终态，一次授权最多兑换出一个 Agent，再次兑换返回 409。
- 选择规则：人只能选择自己（且须是该 Workspace 成员），或选择本人此刻是 maintainer/owner（或 instance admin）的 Workspace 中的有效 SA。确认、consent 和兑换三个时点各重检一次，期间被降权或移出则失败。
- 已确认的选择不可更改；重复提交同一选择是幂等的（Hydra 短暂不可用时可重试）。consent 只授予 `agents:enroll` 和本实例注册 audience，并核对 subject 与记录一致；记录通过 Control 写入登录 context 的 `authorizationId` 关联。
- 兑换只接受 Hydra 签发给 agentd client、scope 与 audience 精确匹配的 opaque access token，以 Admin introspection 验证；Console Cookie 和平台 Token 都不能代替。config.yaml 的 Workspace 必须与确认的 Workspace 相同，否则 403。

### EffectiveConfig

- 每次 Desired 被接受、成员角色变化或 Agent 撤销时，`refreshEffective` 重新计算一次；只有内容（服务、出口、拒绝规则、ingress、策略版本）变化时 revision 才加 1。
- Edge 和 agentd 都**按内容**复核每条流：revision 变化不等于撤销，无关的配置变化不会断开共享 provider 会话或上面的其他流。
- agentd 获取到的副本在 24 小时内有效，每次成功轮询都会续期。Control 不可达时，agentd 继续使用这份副本直到过期；只有凭据被明确拒绝（401）才停止。

### Token / EnrollmentKey / Grant

| 实体 | 状态与转换 |
|---|---|
| Token | 有效 → 撤销 / 过期，均为终态。设备的 control/provider/access Token 有效期 30 天；轮询时若剩余不足 7 天，自动续到 30 天，哈希和 ID 不变 |
| EnrollmentKey | 可用 → 用尽 / 过期 / 撤销。撤销时 Key 和它的 enrollment Token 同时撤销；SA 撤销会撤销它的密钥，legacy User 密钥随该 User 移出或停用而撤销。密钥失效不影响已经注册的设备 |
| Service Account | active → revoked（终态）。撤销时它的全部设备、Token 和未使用的密钥立即失效，SA Grant 停止匹配；创建者或操作人的变化不影响它 |
| Grant | 未开始 → 生效 → 过期，或任意时刻撤销。user 主体的授权若不是 `External`，只在主体仍是资源 Workspace 成员时匹配；service_account 主体只在 SA 有效且与资源同 Workspace 时匹配 |

服务首次发布时，Control 自动创建一条 Workspace 成员授权，默认有效 7 天，可用 Control 配置 `serviceGrantTTL` 调整，最长 720 小时。这条授权只创建一次：被撤销或过期后不会自动补建，迁移也不会创建或延长任何授权。

### 会话与流（Edge）

```text
provider：认证 HELLO（新鲜的 Control 决策）→ Welcome → 控制流就绪 → 写入目录（替换同一 Connector 的旧会话）
         → reconcile 每秒复核身份与 Agent 状态 → 关闭 → 按指针比较移除目录项
flow：OPEN → 授权 → pending → active → 租约过半续期 → FIN / RST / 到期 / 撤销
```

- 新会话握手失败不会顶掉已就绪的旧会话。不同 ConnectorID 的并行会话仍会被拒绝；只有旧会话断开后，新 Connector 才能接入。
- Edge 对 Control 采取失败关闭：RPC 失败不准入新会话和新流，复核失败会关闭流。Control 的写入事务不再持有读锁，所以慢提交不会再阻塞 Edge 的鉴权调用。
- 协议 v3 的接收方对对端时钟签发的租约额外容忍 30 秒偏差。已关闭 FlowID 的防重放记录保留 5 分钟后清理，长寿命会话不会耗尽 FlowID 记录。

### agentd 设备生命周期

```text
未注册 → login（浏览器确认 Workspace 与主体，一次兑换）或 enroll --key-file（SA 密钥）→ 运行
运行 + 本地 reload → 本地收紧立即生效；同时提交 Desired：被接受则 Effective 更新，被拒绝（400/403/409）则记录原因、不再重试
运行 + Control 不可达 → 继续使用 Effective 直到 ExpiresAt
运行 + 401 → 停止并提示重新注册（login --replace 或 enroll --replace）
--replace → 仅当 Control 确认旧凭据已被拒绝时，才备份旧凭据并重新注册
```

provider 和 access 会话只在"本地启用了该角色、持有对应凭据、且 Effective 包含该角色"时连接；否则空闲等待，不会被反复拒绝重连。

## 4. 级联

| 事件 | Agent | Token | EnrollmentKey | Service / Route / Grant |
|---|---|---|---|---|
| RevokeAgent | revoked，Effective 清空 | 该 Agent 的 Token 撤销 | — | 经该 Agent 出口的路由置为停用；Service 和 Grant 保留，Decision 返回 unavailable |
| RemoveMembership | 以该 User 为主体、在此 Workspace 的 Agent 撤销；该 User 创建或操作的 SA 设备不受影响 | 该 User 在此 Workspace 的设备 Token 撤销 | 该 User 在此 Workspace 的 legacy Key 撤销 | 非 External 的 user 授权停止匹配 |
| DisableUser | 以该 User 为主体的全部 Agent 撤销 | 全部撤销，包括 user Token | legacy Key 全部撤销 | 所有授权停止匹配 |
| RevokeServiceAccount | 以该 SA 为主体的全部 Agent 撤销 | 该 SA 的全部 Token 撤销 | 该 SA 的 Key 撤销 | SA 授权停止匹配 |
| SetMembership | 重算该 User 在此 Workspace 的 Agent 的 Effective | — | — | — |
| 名称回收 | 清除已撤销的旧 Agent | 一并删除 | — | 旧 Agent 的 Service、Route 及其上的 Grant 一并删除 |

## 5. 版本号

| 名称 | 含义 | 谁递增 | 谁使用 |
|---|---|---|---|
| 快照 revision | Control 全局快照版本，用于数据库 CAS | 每次非空写入 | DB 写入、Edge watch |
| Effective revision | 每个 Agent 的生效配置版本 | 内容变化时 | agentd 单调性检查、Decision、状态展示 |
| `OpenRequest.Revision` | 发起方做决定时依据的 Effective revision | — | 仅作信息与审计；接收方按内容授权 |
| generation | Edge 内存里每个 Agent 每种角色的会话计数 | 每次认证 | 状态展示 |
| `PolicyVersion` | 内置 Workspace 策略的版本，当前为 `builtin/1` | 策略规则变化时 | 写入 EffectiveConfig |

## 6. 兼容性与迁移

- DB v5 迁移为 grants 的主体约束加入 `authenticated`（与 `anonymous` 一样不带主体 ID、只能用于服务）。SQLite 重建 grants 表，PostgreSQL 替换 CHECK 约束；不改动任何已有数据。
- DB v4 迁移新增 `service_accounts` 和 `enrollment_authorizations`，agents / enrollment_keys / tokens 增加 `service_account_id` 列并以 CHECK 约束"恰有一个主体"、以复合外键约束 SA 与 Agent/Key 同 Workspace；grants 增加 `service_account` 主体。存量 Agent 的 `userId` 改写为 `principal: {type: user}`，存量 User 密钥保留为 legacy。SQLite 通过重建表完成，PostgreSQL 通过 ALTER 完成，结束前做外键检查。
- 线协议的认证结果携带 `principal_type / principal_id`，凭据文件携带 `principal`；agentd 与 agents 需要一起升级。

- 线协议升到 v3（HELLO 不再携带 revision 和服务声明），凭据文件升到 v2（`effective` 替代 `approval`）。v1 凭据会自动转换，下一次轮询时被替换。新旧版本的 agentd 与 agents 不能混用，需要一起升级。
- DB v3 迁移的内容：
  - 把旧 Desired（整份 config.yaml）转换为 AgentSpec，丢弃本地路径和监听地址；
  - 旧 Approval 作为 Effective 的起点，保留其 revision，并写入审计 `agent.migrate.approval_removed`；
  - `pending` 状态改为 `active`，然后由启动时的调和按当前策略重算，超出角色上限的部分写入 `LastRejection`；
  - 为 Grant 回填 `External` 和 `CreatedBy`。
- 数据库只写入有变化的行，审计只做追加。
