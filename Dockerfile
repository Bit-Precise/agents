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
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
# agentd is a public module: it comes from the Go module proxy, checked
# against go.sum and the checksum database like every other dependency.
RUN go mod download
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
