#!/usr/bin/env bash
# Run only against a newly created, uniquely named kind cluster. No existing
# kubeconfig/context or external Kubernetes cluster is modified.
set -euo pipefail
umask 077

if [[ "${1:-}" == "--help" ]]; then
  cat <<'HELP'
Usage: scripts/test-helm-deployment.sh [image-directory]

Requires Docker (running), kubectl, curl, tar and Python 3. Downloads and verifies
kind v0.33.0 and Helm v4.3.1 in a temporary directory. The image directory defaults
to images/ and must contain agents-{control,edge,console}.tar, each tagged
agents-{component}:ci for the host's native Linux architecture.

The script creates its own kind cluster, installs an ephemeral Helm release,
checks runtime permissions, health, console assets, login and internal metadata
authentication, then deletes only that cluster and its temporary files.
HELP
  exit 0
fi
if [[ $# -gt 1 ]]; then
  printf 'Expected at most one image-directory argument.\n' >&2
  exit 2
fi

agents_repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
agents_image_dir="${1:-${AGENTS_IMAGE_DIR:-$agents_repo_dir/images}}"
for agents_tool in docker kubectl curl tar python3; do
  if ! command -v "$agents_tool" >/dev/null 2>&1; then
    printf 'Required tool not found: %s\n' "$agents_tool" >&2
    exit 1
  fi
done
docker info >/dev/null
# Node permission checks below use Docker, so do not inherit a different kind
# provider selected in the caller's shell.
export KIND_EXPERIMENTAL_PROVIDER=docker
for agents_component in control edge console; do
  if [[ ! -f "$agents_image_dir/agents-$agents_component.tar" ]]; then
    printf 'Missing native image artifact: %s/agents-%s.tar\n' "$agents_image_dir" "$agents_component" >&2
    exit 1
  fi
done

agents_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64 | amd64) agents_arch=amd64 ;;
  aarch64 | arm64) agents_arch=arm64 ;;
  *) printf 'Unsupported native architecture.\n' >&2; exit 1 ;;
esac
case "$agents_os-$agents_arch" in
  linux-amd64)
    agents_kind_sha=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
    agents_helm_sha=0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b ;;
  linux-arm64)
    agents_kind_sha=20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2
    agents_helm_sha=8cf8f66ecbc252ac8f7f06cca68aba88508ddfde3a6a34feb1678ad1694b4f88 ;;
  darwin-amd64)
    agents_kind_sha=5a99f26f57246dc9319dd294803313197a0f34d33c525b3ea8b655db5916ece0
    agents_helm_sha=34bec4eba761ba5c16d1965ee6b8f5b0abe5993974a802274e2a4f0418893d89 ;;
  darwin-arm64)
    agents_kind_sha=0c8c7dbe5e23594a198b786c4bc13dacc101fa6196b0cb0b23a1ca44e61f4b4f
    agents_helm_sha=6b4e61c8cd27d99e5a801e63ff25dd5c70b36875c2cd659d3e9ce6b76fc23198 ;;
  *) printf 'Supported hosts: Linux or macOS, amd64 or arm64.\n' >&2; exit 1 ;;
esac

agents_work_dir="$(mktemp -d "${TMPDIR:-/tmp}/agents-helm-smoke.XXXXXX")"
agents_cluster="agents-helm-$(python3 -c 'import secrets; print(secrets.token_hex(6))')"
agents_kubeconfig="$agents_work_dir/kubeconfig"
agents_kind_bin="$agents_work_dir/bin/kind"
agents_helm_bin="$agents_work_dir/bin/helm"
agents_namespace=agents-smoke
agents_cluster_owned=false
agents_forward_pids=()

kube() {
  kubectl --kubeconfig "$agents_kubeconfig" --context "kind-$agents_cluster" "$@"
}

cleanup() {
  agents_status=$?
  trap - EXIT INT TERM
  for agents_pid in "${agents_forward_pids[@]:-}"; do
    [[ -n "$agents_pid" ]] || continue
    kill "$agents_pid" >/dev/null 2>&1 || true
    wait "$agents_pid" >/dev/null 2>&1 || true
  done
  if [[ "$agents_cluster_owned" == true ]]; then
    if [[ "$agents_status" -ne 0 ]]; then
      kube -n "$agents_namespace" get pods,deployments,services -o wide || true
      kube -n "$agents_namespace" describe pods || true
      for agents_component in control edge console; do
        kube -n "$agents_namespace" logs "deployment/agents-$agents_component" --tail=100 || true
      done
    fi
    "$agents_kind_bin" delete cluster --name "$agents_cluster" --kubeconfig "$agents_kubeconfig" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$agents_work_dir"
  exit "$agents_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

verify_sha256() {
  python3 - "$1" "$2" <<'PY'
import hashlib
import hmac
import sys

digest = hashlib.sha256()
with open(sys.argv[1], 'rb') as stream:
    for chunk in iter(lambda: stream.read(1024 * 1024), b''):
        digest.update(chunk)
if not hmac.compare_digest(digest.hexdigest(), sys.argv[2]):
    raise SystemExit('Downloaded tool failed its pinned SHA256 check')
PY
}

mkdir -p "$agents_work_dir/bin" "$agents_work_dir/helm"
curl --fail --silent --show-error --location --retry 3 --connect-timeout 15 --max-time 180 \
  "https://github.com/kubernetes-sigs/kind/releases/download/v0.33.0/kind-$agents_os-$agents_arch" \
  --output "$agents_kind_bin"
verify_sha256 "$agents_kind_bin" "$agents_kind_sha"
chmod 0700 "$agents_kind_bin"
curl --fail --silent --show-error --location --retry 3 --connect-timeout 15 --max-time 180 \
  "https://get.helm.sh/helm-v4.3.1-$agents_os-$agents_arch.tar.gz" \
  --output "$agents_work_dir/helm.tar.gz"
verify_sha256 "$agents_work_dir/helm.tar.gz" "$agents_helm_sha"
tar -xzf "$agents_work_dir/helm.tar.gz" -C "$agents_work_dir/helm"
mv "$agents_work_dir/helm/$agents_os-$agents_arch/helm" "$agents_helm_bin"
chmod 0700 "$agents_helm_bin"

# The random cluster name is never derived from an existing kubeconfig or an
# operator-provided cluster name. Mark it owned before creation for failure cleanup.
agents_cluster_owned=true
"$agents_kind_bin" create cluster --name "$agents_cluster" --kubeconfig "$agents_kubeconfig" --wait 180s
for agents_component in control edge console; do
  "$agents_kind_bin" load image-archive "$agents_image_dir/agents-$agents_component.tar" --name "$agents_cluster"
done
kube create namespace "$agents_namespace"

python3 - "$agents_work_dir" <<'PY'
import json
import secrets
import sys
from pathlib import Path

directory = Path(sys.argv[1])
admin = secrets.token_hex(32)
(directory / 'admin.token').write_text(admin + '\n')
(directory / 'internal.token').write_text(secrets.token_hex(32) + '\n')
(directory / 'login.json').write_text(json.dumps({'token': admin}))
# Keep bearer credentials out of process arguments and shell tracing.
(directory / 'authenticated.curl').write_text(
    'header = "Authorization: Bearer ' + admin + '"\n'
    'header = "Host: agents.example.test"\n'
)
PY
kube -n "$agents_namespace" create secret generic agents-credentials \
  --from-file="admin.token=$agents_work_dir/admin.token" \
  --from-file="internal.token=$agents_work_dir/internal.token"

"$agents_helm_bin" install agents "$agents_repo_dir/charts/agents" \
  --kubeconfig "$agents_kubeconfig" --kube-context "kind-$agents_cluster" \
  --namespace "$agents_namespace" --wait --timeout 300s \
  --set fullnameOverride=agents \
  --set persistence.enabled=false \
  --set ingress.enabled=false \
  --set networkPolicy.enabled=false \
  --set public.serverURL=https://agents.example.test \
  --set public.edgeURL=https://connect.agents.example.test/agent \
  --set image.tag=ci --set image.pullPolicy=Never \
  --set control.image.repository=agents-control \
  --set edge.image.repository=agents-edge \
  --set console.image.repository=agents-console

for agents_component in control edge console; do
  kube -n "$agents_namespace" rollout status "deployment/agents-$agents_component" --timeout=180s
done
kube -n "$agents_namespace" wait --for=condition=Ready pods \
  -l app.kubernetes.io/instance=agents --timeout=180s

forward_component() {
  local component="$1"
  local log_file="$agents_work_dir/forward-$component.log"
  kube -n "$agents_namespace" port-forward "service/agents-$component" :8080 \
    --address=127.0.0.1 >"$log_file" 2>&1 &
  local forward_pid=$!
  agents_forward_pids+=("$forward_pid")
  agents_forwarded_port=""
  for ((agents_attempt = 0; agents_attempt < 100; agents_attempt++)); do
    if ! kill -0 "$forward_pid" >/dev/null 2>&1; then
      cat "$log_file" >&2
      return 1
    fi
    agents_forwarded_port="$(python3 - "$log_file" <<'PY'
import re
import sys
from pathlib import Path
match = re.search(r'Forwarding from 127\.0\.0\.1:(\d+) -> 8080', Path(sys.argv[1]).read_text())
print(match.group(1) if match else '')
PY
)"
    if [[ -n "$agents_forwarded_port" ]]; then
      return 0
    fi
    sleep 0.1
  done
  printf 'Timed out starting port-forward for %s\n' "$component" >&2
  return 1
}

forward_component control
agents_control_port="$agents_forwarded_port"
forward_component edge
agents_edge_port="$agents_forwarded_port"
forward_component console
agents_console_port="$agents_forwarded_port"
for agents_port in "$agents_control_port" "$agents_edge_port" "$agents_console_port"; do
  curl --fail --silent --show-error --max-time 10 "http://127.0.0.1:$agents_port/healthz" --output /dev/null
done

curl --fail --silent --show-error --max-time 10 --retry 5 --retry-delay 1 --retry-max-time 40 \
  --header 'Host: agents.example.test' \
  "http://127.0.0.1:$agents_control_port/console/" --output "$agents_work_dir/console.html"
python3 - "$agents_work_dir" <<'PY'
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urljoin, urlsplit
import sys

directory = Path(sys.argv[1])
html = (directory / 'console.html').read_text()
assert '<html' in html.lower(), 'Control did not proxy the Console HTML'
class Assets(HTMLParser):
    paths = []
    def handle_starttag(self, tag, pairs):
        attributes = dict(pairs)
        value = attributes.get('src') if tag == 'script' else None
        if tag == 'link' and attributes.get('rel') == 'stylesheet':
            value = attributes.get('href')
        if value:
            parsed = urlsplit(urljoin('https://agents.example.test/console/', value))
            assert parsed.netloc == 'agents.example.test', 'Unexpected cross-origin console asset'
            self.paths.append(parsed.path + ('?' + parsed.query if parsed.query else ''))
parser = Assets()
parser.feed(html)
assert parser.paths, 'Console did not reference a compiled asset'
(directory / 'assets.txt').write_text('\n'.join(parser.paths) + '\n')
PY
while IFS= read -r agents_asset; do
  curl --fail --silent --show-error --max-time 10 --header 'Host: agents.example.test' \
    "http://127.0.0.1:$agents_control_port$agents_asset" --dump-header "$agents_work_dir/asset-headers" --output /dev/null
  python3 - "$agents_asset" "$agents_work_dir/asset-headers" <<'PY'
from pathlib import Path
from urllib.parse import urlsplit
import sys
path = urlsplit(sys.argv[1]).path
headers = Path(sys.argv[2]).read_text().lower()
content_types = [line.split(':', 1)[1].strip() for line in headers.splitlines() if line.startswith('content-type:')]
if path.endswith('.js'):
    assert any('javascript' in value for value in content_types), 'JavaScript asset returned an HTML fallback'
elif path.endswith('.css'):
    assert any('text/css' in value for value in content_types), 'Stylesheet asset returned an HTML fallback'
PY
done <"$agents_work_dir/assets.txt"

curl --fail --silent --show-error --max-time 10 \
  --header 'Host: agents.example.test' --header 'Origin: https://agents.example.test' \
  --header 'Content-Type: application/json' --data-binary "@$agents_work_dir/login.json" \
  "http://127.0.0.1:$agents_control_port/api/v1/login" --output "$agents_work_dir/login-response.json"
for ((agents_attempt = 0; agents_attempt < 20; agents_attempt++)); do
  curl --fail --silent --show-error --max-time 10 --config "$agents_work_dir/authenticated.curl" \
    "http://127.0.0.1:$agents_control_port/api/v1/state" --output "$agents_work_dir/state.json"
  if python3 - "$agents_work_dir/state.json" <<'PY'
import json
import sys
with open(sys.argv[1]) as stream:
    state = json.load(stream)
sys.exit(0 if state.get('edgeAvailable') is True else 1)
PY
  then
    break
  fi
  sleep 0.5
done
python3 - "$agents_work_dir" <<'PY'
import json
import sys
from pathlib import Path
directory = Path(sys.argv[1])
login = json.loads((directory / 'login-response.json').read_text())
assert login['user']['admin'] is True and login['csrfToken'], 'Bootstrap login failed'
state = json.loads((directory / 'state.json').read_text())
assert state['user']['admin'] is True, 'Authenticated API identity mismatch'
assert state['edgeAvailable'] is True, 'Control could not authenticate to Edge metadata API'
PY

agents_status="$(curl --silent --show-error --max-time 10 --output "$agents_work_dir/metadata-denied.json" \
  --write-out '%{http_code}' --header 'Content-Type: application/json' \
  --data '{"token":"not-a-credential"}' \
  "http://127.0.0.1:$agents_control_port/internal/v1/identity")"
[[ "$agents_status" == 401 ]] || { printf 'Unauthenticated Control metadata returned %s, expected 401\n' "$agents_status" >&2; exit 1; }
agents_status="$(curl --silent --show-error --max-time 10 --output "$agents_work_dir/edge-denied.json" \
  --write-out '%{http_code}' --header 'Content-Type: application/json' --data '{}' \
  "http://127.0.0.1:$agents_edge_port/internal/v1/snapshot")"
[[ "$agents_status" == 401 ]] || { printf 'Unauthenticated Edge metadata returned %s, expected 401\n' "$agents_status" >&2; exit 1; }

# Inspect kubelet's actual ephemeral volume on our own kind node; the application
# images are distroless and need no diagnostic shell or privileged sidecar.
agents_pod_uid="$(kube -n "$agents_namespace" get pods -l app.kubernetes.io/component=control -o 'jsonpath={.items[0].metadata.uid}')"
agents_node="$(kube -n "$agents_namespace" get pods -l app.kubernetes.io/component=control -o 'jsonpath={.items[0].spec.nodeName}')"
agents_volume_root="/var/lib/kubelet/pods/$agents_pod_uid/volumes"
docker exec "$agents_node" stat -Lc '%a %u %g' \
  "$agents_volume_root/kubernetes.io~empty-dir/state/state" >"$agents_work_dir/state-mode.txt"
docker exec "$agents_node" stat -Lc '%a %u %g' \
  "$agents_volume_root/kubernetes.io~projected/credentials/admin.token" >"$agents_work_dir/secret-mode.txt"
python3 - "$agents_work_dir" <<'PY'
import sys
from pathlib import Path
directory = Path(sys.argv[1])
mode, uid, gid = (directory / 'state-mode.txt').read_text().split()
assert int(mode, 8) & 0o777 == 0o700 and uid == '65532', 'State subdirectory is not private and non-root-owned'
mode, uid, gid = (directory / 'secret-mode.txt').read_text().split()
assert int(mode, 8) & 0o777 == 0o440 and gid == '65532', 'Projected Secret did not retain 0440/fsGroup access'
PY

printf 'Helm smoke passed: three components, private state, projected secrets, console assets, login, and metadata authentication.\n'
