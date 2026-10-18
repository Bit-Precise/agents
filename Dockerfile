# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS frontend
WORKDIR /src/console
COPY console/package.json console/package-lock.json ./
RUN npm ci
COPY console ./
COPY api /src/api
COPY scripts/collect-frontend-licenses.mjs /src/scripts/collect-frontend-licenses.mjs
RUN npm run generate && npm run typecheck && npm run build
RUN node /src/scripts/collect-frontend-licenses.mjs /src/console /out/frontend-licenses

FROM --platform=$BUILDPLATFORM golang:1.27.2-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ENV GOPRIVATE=github.com/bit-precise/agentd GOTOOLCHAIN=local
WORKDIR /src
COPY .github/github_known_hosts /root/.ssh/known_hosts
RUN git config --global url."ssh://git@github.com/".insteadOf "https://github.com/"
COPY go.mod go.sum ./
# The read-only SDK deploy key is forwarded by ssh-agent; no key enters a layer.
RUN --mount=type=ssh,required=true go mod download
COPY . .
COPY --from=frontend /src/control/assets/web ./control/assets/web
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w -X main.Version=$VERSION" -o /out/agents ./cmd/agents
RUN scripts/collect-licenses.sh /out/licenses

FROM gcr.io/distroless/static-debian13:nonroot AS runtime
LABEL org.opencontainers.image.source="https://github.com/Bit-Precise/agents"
COPY --from=build /out/agents /usr/local/bin/agents
COPY --from=build /out/licenses /usr/share/licenses
COPY --from=frontend /out/frontend-licenses /usr/share/licenses/console
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/agents"]

FROM runtime AS control
CMD ["control", "--config", "/etc/agents/config.yaml"]

FROM runtime AS edge
CMD ["edge", "--config", "/etc/agents/config.yaml"]

FROM runtime AS console
CMD ["console", "--config", "/etc/agents/config.yaml"]
