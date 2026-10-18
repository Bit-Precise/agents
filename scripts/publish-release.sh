#!/usr/bin/env bash
set -euo pipefail
: "${RELEASE_TAG:?release tag required}"
: "${GH_TOKEN:?GitHub release token required}"
[[ "$RELEASE_TAG" =~ ^v([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$ ]] || { echo 'Expected a semantic vX.Y.Z tag' >&2; exit 1; }
chart_version=${BASH_REMATCH[1]}
release_flags=(--verify-tag)
if [[ "$chart_version" == *-* ]]; then
  release_flags+=(--prerelease --latest=false)
fi
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT
curl --fail --location --retry 3 --output "$work/helm-v4.3.1-linux-amd64.tar.gz" https://get.helm.sh/helm-v4.3.1-linux-amd64.tar.gz
curl --fail --location --retry 3 --output "$work/helm.sha256" https://get.helm.sh/helm-v4.3.1-linux-amd64.tar.gz.sha256sum
(cd "$work" && sha256sum -c helm.sha256)
tar -xzf "$work/helm-v4.3.1-linux-amd64.tar.gz" -C "$work"
helm_bin="$work/linux-amd64/helm"
mkdir -p dist
"$helm_bin" package charts/agents --version "$chart_version" --app-version "$RELEASE_TAG" --destination dist
printf '%s' "$GH_TOKEN" | "$helm_bin" registry login ghcr.io --username "${GITHUB_ACTOR:?}" --password-stdin
"$helm_bin" push "dist/agents-$chart_version.tgz" oci://ghcr.io/bit-precise/charts
for component in control edge console; do
  printf 'ghcr.io/bit-precise/agents-%s:%s\n' "$component" "$RELEASE_TAG"
done > dist/IMAGES
(cd dist && sha256sum "agents-$chart_version.tgz" IMAGES > SHA256SUMS && sha256sum --check SHA256SUMS)
cat > "$work/release.md" <<'BODY'
Agents Control, Edge and Console are separate images, each with linux/amd64 and linux/arm64 manifests. Exact image references are in IMAGES; the Helm chart and SHA256SUMS are attached.

Install with `helm install agents oci://ghcr.io/bit-precise/charts/agents --version <version> -f values.yaml`. Create the required existing Secret first. Control and Edge currently require one replica each; the chart does not claim distributed state or edge ownership.

See README.md and docs/helm.md for TLS ingress, DNS, PostgreSQL, private images and SDK provenance.
BODY
if gh release view "$RELEASE_TAG" >/dev/null 2>&1; then
  gh release upload "$RELEASE_TAG" "dist/agents-$chart_version.tgz" dist/IMAGES dist/SHA256SUMS --clobber
else
  gh release create "$RELEASE_TAG" "dist/agents-$chart_version.tgz" dist/IMAGES dist/SHA256SUMS "${release_flags[@]}" --title "agents $RELEASE_TAG" --notes-file "$work/release.md"
fi
