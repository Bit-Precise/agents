#!/usr/bin/env bash
set -euo pipefail
output=${1:?output directory required}
mkdir -p "$output/go" "$output/agents"
goroot=$(go env GOROOT)
go_license=""
for candidate in "$goroot/LICENSE" "$goroot/../LICENSE"; do
  if [[ -f "$candidate" ]]; then go_license=$candidate; break; fi
done
[[ -n "$go_license" ]] || { echo 'Go toolchain LICENSE missing' >&2; exit 1; }
cp "$go_license" "$output/go/LICENSE"
cp THIRD_PARTY_NOTICES.md go.mod go.sum "$output/agents/"
while IFS=$'\t' read -r module directory; do
  [[ -n "$module" && -n "$directory" ]] || continue
  destination="$output/modules/$module"
  mkdir -p "$destination"
  for document in "$directory"/LICENSE* "$directory"/COPYING* "$directory"/NOTICE*; do
    [[ -f "$document" ]] || continue
    cp "$document" "$destination/"
  done
done < <(go list -mod=readonly -deps -f '{{with .Module}}{{.Path}}{{"\t"}}{{.Dir}}{{end}}' ./cmd/agents | sort -u)
sdk=$(go list -mod=readonly -m -f '{{.Dir}}' github.com/bit-precise/agentd)
mkdir -p "$output/agentd-xhttp/source"
cp "$sdk/transport/xhttp/LICENSE" "$sdk/transport/xhttp/NOTICE" "$output/agentd-xhttp/"
# Ship the MPL-covered adapter source with the image, including our modifications.
cp "$sdk/transport/xhttp/"*.go "$output/agentd-xhttp/source/"
go list -mod=readonly -m github.com/bit-precise/agentd > "$output/agentd-xhttp/SDK_VERSION"
