# agents Helm chart

部署三个独立组件：Control 管理 SQL、身份与策略；Edge 持有 agentd 会话并转发业务；Console 只提供静态界面，由 Control 同源代理。默认镜像分别为 `ghcr.io/bit-precise/agents-control`、`agents-edge`、`agents-console`，镜像标签采用 `Chart.appVersion`（如 `v0.1.0`），支持 Linux amd64/arm64。

要求 Kubernetes 1.26+、Helm、能够终止 HTTPS 的 Ingress Controller，以及预先创建的凭据 Secret。Control 和 Edge 必须各一个副本，使用 `Recreate` 更新；Console 可以增加副本。多个 Control writer、多 Edge 连接迁移和无中断更新尚不支持。

## 安装

先在 release namespace 创建 `agents-credentials`，至少包含以下键。管理员令牌与组件令牌应分别生成，至少 32 字节随机数；不要放入 values 文件、命令行参数或 Git。

| Secret 键 | 用途 | 挂载组件 |
|---|---|---|
| `admin.token` | 初始管理员身份 | Control |
| `internal.token` | 组件间元数据 API 认证 | Control、Edge |
| `database-dsn` | PostgreSQL 连接字符串，仅 PostgreSQL 模式需要 | Control |
| 自选 OIDC 键 | 可选 confidential-client secret | Control |

```sh
kubectl create namespace agents
# 下面两个文件由密码管理器或其他安全流程提供，内容是一行令牌。
kubectl -n agents create secret generic agents-credentials \
  --from-file=admin.token=/secure/agents/admin.token \
  --from-file=internal.token=/secure/agents/internal.token

helm install agents oci://ghcr.io/bit-precise/charts/agents \
  --version 0.1.0 --namespace agents --values values-production.yaml
# 从源码安装：helm install agents ./charts/agents -n agents -f values-production.yaml
```

生产 values 示例：

```yaml
existingSecret: agents-credentials
public:
  serverURL: https://agents.example.com
  edgeURL: https://connect.agents.example.com/agent
ingress:
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
    nginx.ingress.kubernetes.io/proxy-request-buffering: "off"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
  serviceHosts:
    - "*.workspace1.agents.example.com"
  tls:
    - secretName: agents-public-tls
      hosts: [agents.example.com, connect.agents.example.com]
    - secretName: agents-workspace1-tls
      hosts: ["*.workspace1.agents.example.com"]
```

如果入口使用非标准 HTTPS 端口，例如 `30526`，将两个公开 URL 配置为 `https://agents.example.com:30526` 和 `https://connect.agents.example.com:30526/agent`。端口使用 `1–65535` 的十进制整数，不带前导零；省略时使用 HTTPS 默认端口 443。服务 URL 必须使用与控制台相同的公开端口，浏览器 SSO 不允许跨端口兑换。OIDC callback 自动保留这个端口。

`ingress.serviceHosts`、Ingress/TLS 的 host 仍只填写 DNS 名，不带端口。Chart 不创建入口监听器或 NodePort；外部 Istio Gateway 等入口须提供所配置的公开端口，并保留完整 Host/authority。复用已有 Gateway 时可设置 `ingress.enabled: false`，由部署仓库配置分流。

这些 TLS Secret 必须已经存在。Controller 注解依具体实现而异；示例仅适用于 ingress-nginx。无 `ingress.tls` 时依赖 Controller 的默认证书，chart 不申请证书，也不保证默认配置的示例域名可访问。

`*.agents.example.com` 不覆盖 `demo.workspace1.agents.example.com`。每个 Workspace 配置独立的一层 wildcard，或列出 `demo.workspace1.agents.example.com` 这样的精确服务名。创建新 Workspace 后，还需要更新 DNS、Ingress 和证书覆盖范围。wildcard 本身不授予资源访问权限。

## 主要配置

完整字段与校验约束见 [values.yaml](values.yaml) 和 [values.schema.json](values.schema.json)。

| 参数 | 默认值 / 行为 |
|---|---|
| `existingSecret` | `agents-credentials`；chart 从不生成 Secret |
| `secretKeys.*` | 映射现有 Secret 的 admin/internal/PostgreSQL 键 |
| `public.serverURL` | 公网 HTTPS 控制台 origin，可含显式端口 `1–65535` |
| `public.edgeURL` | 公网 HTTPS `/agent`，可含显式端口；可以和控制台共用 host |
| `image.tag` | 空值采用 `Chart.appVersion` |
| `image.pullPolicy` / `imagePullSecrets` | `IfNotPresent` / `[]` |
| `control.image.repository` / `edge.image.repository` / `console.image.repository` | 各组件独立 GHCR 镜像 |
| `control.replicaCount` / `edge.replicaCount` | 固定为 `1`，其它值校验失败 |
| `console.replicaCount` | `1`，可增加 |
| `database.type` | `sqlite` 或 `postgres` |
| `persistence.enabled` | SQLite 默认启用；PostgreSQL 不创建 PVC |
| `persistence.existingClaim` | 使用已有 PVC，不由 chart 创建或删除 |
| `persistence.retain` | `true`，保留 chart 创建的 PVC |
| `persistence.size` / `storageClass` | `1Gi` / cluster 默认；`"-"` 禁用动态 StorageClass |
| `*.resources` | 每组件独立 requests/limits |
| `podSecurityContext` / `securityContext` | UID/GID/fsGroup 65532、非 root、只读 rootfs、drop ALL |
| `serviceAccount.*` | 默认创建，无 RBAC 授权，不挂载 API Token |
| `probes.*` | HTTP `/healthz` 的 startup/readiness/liveness；可以调整 |
| `networkPolicy.enabled` | 默认 `false`；启用前匹配实际 Controller/DNS/数据库/IdP |
| `oidc.*` | 默认关闭；见下文 |

`/healthz` 表示进程存活，不证明数据库、Control、provider 或外部 IdP 都可达。业务不可用时应同时检查应用日志和控制台状态。

## 存储、OIDC 与运维

SQLite 只由 Control 读写。卷挂在 `/var/lib/agents`，进程创建自己的 `/var/lib/agents/state` 私有子目录；不能直接把卷根作为 stateDir。`fsGroupChangePolicy: OnRootMismatch` 避免重复修改既有私有目录权限。存储驱动需保留该子目录的 owner/0700 权限。生产环境保留 PVC 并单独备份数据库；关闭 persistence 仅用于测试，Pod 重建会丢失身份和策略。

PostgreSQL：将 DSN 放入现有 Secret 的 `database-dsn` 键，然后设置 `database.type: postgres`。ConfigMap 仅包含 `dsnFile: /etc/agents/secrets/database-dsn`，Edge 和 Console 不获取数据库秘密。生产连接应验证服务器 TLS；使用私有 CA 时需为 Control 配置信任根，例如使用包含该 CA 的自定义 Control 镜像。

```yaml
oidc:
  enabled: true
  issuer: https://id.example.com
  clientId: agents
  existingSecret: corporate-oidc
  clientSecretKey: client-secret
  scopes: [openid, profile, email]
  bootstrapAdminSubjects: ["exact-subject-from-this-issuer"]
  adminEmails: ["operator@example.com"]
```

IdP 注册的 callback 必须为 `https://agents.example.com/auth/oidc/callback`；chart 从 `public.serverURL` 自动生成。省略 `clientSecretKey` 使用 public-client PKCE；省略 `oidc.existingSecret` 则复用根 `existingSecret`。`bootstrapAdminSubjects` 只在用户首次创建时生效，不持续提升已有用户。新用户自动拥有仅自己为 owner 的 `personal-<userID>` Workspace；重复登录不会重复创建，已有用户不补建。实例管理员权限与 Workspace owner 权限独立。

`adminEmails` 从已验证 ID token 的 `email` 精确匹配实例管理员名单，要求 `email_verified` 为 JSON 布尔值 `true`。地址去除首尾空白并转小写，不支持域/后缀/别名匹配，不按邮箱合并账号；身份仍按 issuer + subject 绑定。非空名单会自动请求 `email` scope，但 IdP 仍须在 ID token 中发出这两个声明。新旧用户均须完成一次新 OIDC 登录才能获得名单权限；删除邮箱、切换 issuer 或关闭 OIDC 后，Control 启动时撤销邮箱派生的管理员权限，既有用户 Bearer token 同样生效。手动创建及 bootstrap 管理员不受名单移除影响。手工禁用最后一个有效管理员的保护不阻止配置策略撤权；即使这是最后一个邮箱管理员，名单或 verified 声明失效也会撤销权限。应保留独立 bootstrap 管理入口；必要时通过可信配置及新的已验证登录恢复邮箱管理员。

服务端不保存或刷新 IdP tokens，浏览器会话最长为 `min(ID token 到期时间, 8 小时)`。启动需要 issuer discovery 可达；登录 state 仅在当前单 Control 进程的内存中，重启会中断未完成的登录。

Secret 以 `0440` 投影；这里依赖服务端的 Kubernetes Secret reader，不改变 agentd 客户端私有凭据检查。外部 Secret 更新不会自动重启 Pod；协调更新 internal token 后重启 Control/Edge，期间会有中断。配置变化通过 checksum 触发 Deployment 更新。

更完整的 DNS/TLS、NetworkPolicy、升级与验证说明见 [docs/helm.md](../../docs/helm.md)。

## 检查

```sh
helm lint charts/agents --strict
python3 -m unittest discover -s charts/agents/tests
# 有 Docker、kubectl 和三个原生架构 CI 镜像 tar 时：
bash scripts/test-helm-deployment.sh images
```

Python 结构测试只使用标准库。kind 冒烟会实际运行三个组件；它通过本机 port-forward 检查后端，不替代真实 Ingress、TLS/CDN 或 NetworkPolicy enforcement 验收。
