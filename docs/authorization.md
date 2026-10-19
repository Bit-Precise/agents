# 授权模型

agents 的授权分成两层，分别回答两个不同的问题，互不读取对方的输入：

| | 控制面：RBAC | 数据面：Grant |
|---|---|---|
| 回答的问题 | 谁能执行哪些管理操作 | 谁能使用哪个已发布的资源 |
| 依据 | 工作空间角色（Membership）与实例管理员 | Grant：主体、资源、有效期 |
| 检查时机 | 每个管理 API 调用（Console 的所有操作都走它） | Edge 上的每个 Serve 请求和每条 Egress 流 |
| 代码 | `control/authz/rbac.go` | `control/authz/grants.go` |

两层只在两处衔接，而且都发生在控制面一侧：

1. **创建、撤销 Grant 本身是管理操作**，受 RBAC 约束：谁能给什么资源授权、能不能对外分享，由角色决定。
2. **设备能提供什么**（Serve、Egress）是控制面策略：由运行主体的角色和注册时的角色上限决定。Control 把结果算进设备的生效配置（EffectiveConfig），数据面只读生效配置，从不读取工作空间角色。

`control/authz/authz_test.go` 校验本文的两张表与代码一致，改动权限时两边必须同时修改。

## 1. 控制面 RBAC

### 角色

- **owner / maintainer / developer / guest**：用户在某个工作空间里的 Membership 角色，每个工作空间独立。
- **实例管理员**：在所有工作空间拥有下表全部权限，另有实例级操作：管理用户、查看全局列表和审计日志。
- **非成员**：在该工作空间没有任何权限。
- **服务账号**没有角色，不能登录 Console，也不能调用管理 API；它只作为设备的运行身份和 Grant 的主体出现。

### 权限矩阵

<!-- rbac-matrix -->
| 权限 | 包含的操作 | owner | maintainer | developer | guest |
|---|---|---|---|---|---|
| `workspace.view` | 查看工作空间、成员、服务和出口路由；查看、撤销自己的设备，查看自己发起的流 | ✓ | ✓ | ✓ | ✓ |
| `members.manage` | 添加、修改、移除 developer 和 guest 成员 | ✓ | ✓ | | |
| `members.manage_privileged` | 任何涉及 owner 或 maintainer 角色的成员变更 | ✓ | | | |
| `service_accounts.manage` | 查看和管理服务账号及其注册密钥；以服务账号身份注册设备 | ✓ | ✓ | | |
| `agents.manage` | 查看和撤销本工作空间的所有设备，以及它们的连接和流 | ✓ | ✓ | | |
| `agents.provide` | 自己的设备可以提供 Serve 和 Egress；没有这项权限时只能运行 Ingress | ✓ | ✓ | ✓ | |
| `services.manage` | 启用、停用所有服务，管理它们的授权 | ✓ | ✓ | | |
| `services.manage_own` | 对自己设备发布的服务做同样的事 | ✓ | ✓ | ✓ | |
| `egress_routes.manage` | 创建、启用、停用出口路由，管理它们的授权 | ✓ | ✓ | | |
| `grants.share_external` | 把服务分享到工作空间之外：匿名访问者、所有已登录用户、其他工作空间或非成员 | ✓ | ✓ | | |
<!-- /rbac-matrix -->

developer 有 `services.manage_own` 但没有 `grants.share_external`，所以可以把自己的服务授权给本工作空间的成员，不能设为匿名或对外分享。

### 本人规则

下面几条与角色无关，只看“是不是本人”：

- **自己的设备**指运行身份恰好是本人 User 的设备。本人创建或操作的服务账号设备不算；`createdBy` 永远不参与授权判断。
- 任何人都可以撤销自己的设备，查看自己作为消费方的流。
- 迁移保留的旧注册密钥如果绑定本人，本人可以撤销。
- 以本人身份注册设备必须是该工作空间的成员。实例管理员也一样，不能借管理员身份成为某个工作空间的设备主体。
- 任何启用的用户都可以创建工作空间并成为 owner：实例管理员以外每人最多拥有 20 个，`personal-` 前缀保留。用户可以轮换自己的令牌。

### 可见即可管理

列表和详情只返回调用者能管理的对象，再加上 `workspace.view` 覆盖的目录：成员、服务、出口路由。看不到的资源详情返回 404。授权（Grant）只对能管理它的人可见。

### 设备的角色上限

设备能运行哪些角色由 `authz.DeviceRoles` 决定：

- **本人设备**：注册时允许的角色；运行主体的角色没有 `agents.provide` 时只剩 Ingress，不再是成员时什么都不能运行。
- **服务账号设备**：注册时允许的角色，由管理者在注册密钥或 Device Flow 中确定。

角色变化（降为 guest、移出工作空间）会立即重算设备的生效配置，并把被收回的角色记录为拒绝原因。

### Console

`GET /v1/workspaces/{workspaceId}/permissions` 返回调用者在该工作空间的角色和权限列表。Console 据此显示或隐藏操作；Control 对每个操作仍会独立检查。

## 2. 数据面 Grant

### 资源

- **service**：Serve 发布的服务，由浏览器访问。
- **egress**：出口路由，由 ingress 设备使用。

### 主体

<!-- grant-subjects -->
| 主体 | 含义 | 服务 | 出口 |
|---|---|---|---|
| `anonymous` | 任何人，不需要登录 | ✓ | |
| `authenticated` | 本实例任何已登录、未停用的用户 | ✓ | |
| `user` | 指定的一个用户 | ✓ | ✓ |
| `workspace` | 某个工作空间的人类成员，不含其中的服务账号 | ✓ | ✓ |
| `service_account` | 资源所在工作空间的一个服务账号 | | ✓ |
<!-- /grant-subjects -->

- 服务账号没有浏览器凭据，不能访问 Serve 服务。
- 出口只能在工作空间内部使用：`user` 主体必须是成员，`workspace` 主体只能是本工作空间，服务账号必须属于本工作空间。
- **对外分享**：`anonymous`、`authenticated`、其他工作空间、非成员的 `user` 都算对外分享。它们只能用于服务，创建时需要 `grants.share_external`；出口不能对外分享。`Grant.External` 记录创建时是否为对外分享。

### 求值规则

- 每次请求都按当时的状态现算。工作空间内部的 `user` 授权只在该用户仍是成员时生效，离开后不会变成对外分享；`workspace` 授权跟随当前成员关系。
- 一种主体不会顶替另一种：服务账号不能使用其创建者的 User 授权，`workspace` 授权不覆盖其中的服务账号。
- 多条有效授权取并集，到期时间取最晚的一条；撤销其中一条不影响其他。每条授权都有有限的时间窗，最长 30 天。
- **默认授权**：服务首次发布时自动创建一条“本工作空间成员”授权，默认 7 天（Control 配置 `serviceGrantTTL`，最长 720h），之后不再自动补建或延长。出口没有默认授权。

### 数据面检查的内容

- **Serve 请求**：服务存在且已启用 → 消费者（浏览器里的用户，或匿名）命中有效授权 → 提供方设备有效，且服务在它的生效配置中。
- **Egress 流**：消费者（ingress 设备的运行主体）属于该工作空间 → 选中匹配的出口路由 → 命中有效授权 → 出口设备有效，且生效配置允许该目标。
- **设备凭据**：provider 凭据要求生效配置中有服务或出口规则，access 凭据要求生效配置启用了 Ingress。agentd 也按同样的条件决定是否建立这两种会话。

数据面从不读取工作空间角色。角色对设备的影响已经由控制面算进生效配置。

## 3. 示例

developer Alice 的笔记本发布了 `grafana` 服务：

1. **控制面**：Alice 有 `agents.provide`，她的设备可以提供 Serve；服务首次发布时，Control 创建服务记录和一条默认成员授权。Alice 有 `services.manage_own`，可以把服务再授权给本工作空间的某个成员；她没有 `grants.share_external`，所以不能设为匿名或所有已登录用户，这需要 maintainer 来做。
2. **数据面**：同一工作空间的 Bob 打开 `grafana.<workspace>.<domain>`，命中默认的工作空间授权，放行。
3. **角色变化**：Alice 被降为 guest。控制面重算她设备的生效配置，`grafana` 被移出，Edge 随即拒绝这个服务的请求，provider 凭据也不再被接受。

## 4. 代码位置

| 位置 | 内容 |
|---|---|
| `control/authz/rbac.go` | 角色、权限、矩阵、本人规则、`DeviceRoles` |
| `control/authz/grants.go` | 资源与主体类型、`Accepts`、`External`、`Deadline`、默认授权 |
| `control/state` | 从快照取事实，每个变更都调用 `authz`；`SnapshotForUser` 按同样的规则裁剪可见范围；`ResolveServe` / `ResolveEgress` 是数据面决策入口 |
| `edge` | 只调用 `ResolveServe` / `ResolveEgress`，自己不做授权判断 |
