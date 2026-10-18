#!/usr/bin/env bash
# Source this in CI. The secret is a read-only deploy key for agentd, not a PAT.
set -euo pipefail
: "${AGENTD_DEPLOY_KEY:?AGENTD_DEPLOY_KEY is required to fetch the private agentd SDK}"
: "${RUNNER_TEMP:?RUNNER_TEMP must be set}"
umask 077
sdk_key=$(mktemp "$RUNNER_TEMP/agents-sdk.XXXXXX")
printf '%s\n' "$AGENTD_DEPLOY_KEY" > "$sdk_key"
unset AGENTD_DEPLOY_KEY
eval "$(ssh-agent -s)" >/dev/null
ssh-add "$sdk_key" >/dev/null 2>&1
rm "$sdk_key"
mkdir -p "$HOME/.ssh"
cp .github/github_known_hosts "$HOME/.ssh/known_hosts"
git config --global url."ssh://git@github.com/".insteadOf "https://github.com/"
export GOPRIVATE=github.com/bit-precise/agentd
if [[ -n "${GITHUB_ENV:-}" ]]; then
  printf 'SSH_AUTH_SOCK=%s\nSSH_AGENT_PID=%s\nGOPRIVATE=%s\n' "$SSH_AUTH_SOCK" "$SSH_AGENT_PID" "$GOPRIVATE" >> "$GITHUB_ENV"
fi
