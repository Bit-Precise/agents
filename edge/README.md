# Single-owner Edge

`New(Backend, Config)` constructs the authenticated XHTTP handler and the in-memory
session/flow directory. Mount `Handler()` on a separate HTTPS endpoint and run
`Run(ctx)` once. The browser gateway calls `ServeHTTP(w, r, verifiedUserID)`;
empty user IDs remain anonymous and must be allowed by control. `Snapshot()`
contains no tokens and requires explicit caller filtering: ordinary users see
their own Agent connections or connections in Workspaces they manage; flows are
visible to their consumer user or Workspace managers, not merely to the provider
Agent owner. Instance administrators can inspect the complete runtime view.
`Shutdown(ctx)` closes accepted and pending connections, active and opening flows,
request bodies, and waits for Edge workers. The surrounding HTTP server is owned
by the embedding application.

The Backend implements Identity, Agent, ResolveServe, ResolveEgress and Subscribe.
HTTP authorization returns the verified TokenID; `xhttp.AuthenticatedPrincipal`
exposes that immutable metadata to the agents handshake. Hello.Token must resolve
to the same TokenID, role, Workspace and Agent. Control/user/enrollment tokens are
never accepted by this public transport. Hello carries no revision or service
declarations (protocol v3): every flow is authorized by a fresh Control Decision,
which already requires the service or destination to be in the Agent's effective
configuration, and again by the provider's local policy.

One Agent has one active Connector. A different Connector cannot evict an active
one; a reconnect of the same Connector advances a server-side generation and
replaces its prior session only once its handshake completes, so a failed
reconnect never displaces a ready session. Cleanup uses pointer/session identity comparisons, so
an old close event cannot remove its replacement. Registration and generation
state are local to this single Edge process; distributed Edge ownership is not
implemented here.

Incoming access OPEN first resolves its authenticated user's domain route. Only
after logical admission does Edge inspect bounded TLS ClientHello bytes. It
registers a cancellable pending flow before provider OPEN, checks control again,
chooses only the Decision's exact Agent and capability, and replays inspected
bytes unchanged. No direct dial, wider-rule retry, or alternate-Agent fallback
exists. Serve uses a separate provider stream and HTTP transport for each request;
browser keep-alive never reuses a resource authorization or provider stream.

State notifications and a bounded periodic recheck reevaluate session tokens and
each flow's original resource/user/target. A new effective-configuration revision
alone does not terminate an unrelated authorized flow. Resource/Agent changes,
revocation, session failure and absolute deadlines close affected flows, including
pending OPEN and request bodies. Ongoing streams use a renewable authorization
lease separate from their immutable hard deadline. The default lease is 55 seconds
(receiver maximum 60 seconds), checked again around its midpoint. Every extension
resolves the same user/resource/Agent/destination and rechecks both access and
provider identities. Provider local policy is checked again during the RENEW
exchange. Edge commits its new lease only after both relevant peers acknowledge.
Grant revocation, route/Agent change, local denial, missing ACK or expiry cancels
the original stream; it never migrates to a different exit.

Default hard limits are 12 hours per flow and 24 hours per Edge session, further
bounded by caller context, credential and negotiated session
expiry. These are maximum lifetimes, not renewal periods. Once a hard bound is
reached the application reconnects; renewal never extends it. The provider's
existing Manager context uses this hard Deadline, while Stream.Context propagates
short-lease expiry and revocation to its origin socket. No extra Supervisor or
AuthorizeRenew callback is required.

An authorization-check failure stops renewal immediately. If the control path
cannot deliver cancellation, endpoints stop no later than their last accepted
short lease (at most 60 seconds plus the 30-second clock-skew allowance receivers
grant to peer-stamped leases). Edge fails closed: an unreachable Control admits
no new session or flow and a failed recheck closes the flow. XHTTP has a 2-minute *idle* timeout, not a fixed session TTL;
yamux's 30-second keepalive maintains healthy otherwise-idle transports.

The handler uses the agentd SDK's XHTTP defaults, which follow Xray-core
packet-up: POSTs of up to 1,000,000 bytes, 30 buffered packets per session, and
POSTs that wait for a slot instead of being rejected. Upload buffering per
session is bounded by roughly (MaxPostsPerSession + 2 × MaxBufferedPosts) ×
MaxEachPostBytes, about 90 MB at the defaults, so size the Edge memory limit to
the expected number of busy sessions. Version 1 agentd clients remain accepted;
deploy Edge before upgrading agentd.

“Connected” means an authenticated session of an active Agent. It is not an active probe of each HTTP origin. Missing providers
return unavailable; provider HTTP errors return 502/503. Control remains the only
policy authority and the root application owns browser authentication/cookies.

Tests use real TLS XHTTP and agents sessions to cover exact multi-provider routes,
ClientHello replay, cross-token/user rejection, revoked agents, revision changes
that keep sessions, clone rejection, failed-reconnect safety and reconnect cleanup, per-request HTTP
isolation, body errors, expiry, resource-local revocation, and repeated short-lease renewal
through the actual provider Manager:

```sh
go test -race ./edge ./transport/xhttp
```
