# 来源与第三方组件

- 本仓库迁自 [Bit-Precise/agentd](https://github.com/Bit-Precise/agentd) 的 `90306969ec21fda553475bf7bf374619a3bc2a19` 服务端实现。
- 仓库职责分层参考 Headscale `fc97593bcea76c92fb3ad41a5b95076684ddc85a`：cmd、control、types/db/state、servertest、配置和 packaging。使用 agents 自有协议/实体，不声明 Tailscale/Headscale 兼容。
- agentd SDK 由 go.mod 固定版本，包含配置、凭据、HTTP 注册契约、agents v2 connection 和修改版 XHTTP；客户端生命周期参考 cloudflared，XHTTP 的源码许可证和来源见该依赖仓库 transport/xhttp/LICENSE、NOTICE。
- 数据库使用 modernc.org/sqlite 与 pgx；YAML、IDNA 和日志使用 yaml.v3、golang.org/x/net、zerolog；版本由 go.mod/go.sum 锁定。
- distroless、Go 工具链与 Kubernetes/Helm 的许可证分别遵循其上游规定。镜像中不放入任何部署 Secret 或设备凭据。

参考仓库结构不授予对方商标、产品认证或代码签名身份。第三方组件各自的许可证继续适用。
