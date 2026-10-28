#!/usr/bin/env bash
# Switch chat alerts on in a running demo release (ADR-018). Called by `make alerts-on`.
#
# Credentials come from the environment, not arguments, and go into a Secret created with kubectl
# — not through `helm --set`, which would store them in the release history Helm keeps in the
# cluster. The release is then pointed at that Secret with alerts.existingSecret.
#
#   TELEGRAM_BOT_TOKEN=123:abc TELEGRAM_CHAT_ID=-1001 make alerts-on
#   SLACK_WEBHOOK_URL=https://hooks.slack.com/services/... make alerts-on
#   TEAMS_WEBHOOK_URL=https://....logic.azure.com/... make alerts-on
set -euo pipefail

: "${KUBECTL:?}" "${HELM_K:?}" "${RELEASE:?}" "${CHART:?}" "${NAMESPACE:?}"
SECRET="${RELEASE}-chat-alerts"

# KEY=value lines, written by printf (a builtin) into a pipe: a credential is never an argument of
# any process, so it does not show in `ps`.
env_lines=""
[[ -n "${SLACK_WEBHOOK_URL:-}" ]] && env_lines+="slack-webhook-url=${SLACK_WEBHOOK_URL}"$'\n'
[[ -n "${TEAMS_WEBHOOK_URL:-}" ]] && env_lines+="teams-webhook-url=${TEAMS_WEBHOOK_URL}"$'\n'
if [[ -n "${TELEGRAM_BOT_TOKEN:-}" ]]; then
  : "${TELEGRAM_CHAT_ID:?TELEGRAM_BOT_TOKEN needs TELEGRAM_CHAT_ID: the chat the bot posts to}"
  env_lines+="telegram-bot-token=${TELEGRAM_BOT_TOKEN}"$'\n'
fi
if [[ -z "$env_lines" ]]; then
  echo "set at least one of SLACK_WEBHOOK_URL, TEAMS_WEBHOOK_URL, or TELEGRAM_BOT_TOKEN with TELEGRAM_CHAT_ID" >&2
  exit 2
fi

$KUBECTL -n "$NAMESPACE" create secret generic "$SECRET" \
  --from-env-file=<(printf '%s' "$env_lines") \
  --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
echo "secret/$SECRET written"

# The Secret's content is not in the rendered chart, so its checksum cannot roll the pods: an
# explicit restart picks up a changed credential.
$HELM_K upgrade "$RELEASE" "$CHART" --namespace "$NAMESPACE" --reuse-values \
  --set alerts.enabled=true \
  --set alerts.existingSecret="$SECRET" \
  --set-string alerts.telegram.chatId="${TELEGRAM_CHAT_ID:-}" \
  --set alerts.uiUrl="${ALERTS_UI_URL:-http://localhost:8080}" \
  --wait --timeout 4m
$KUBECTL -n "$NAMESPACE" rollout restart "deploy/${RELEASE}-visualizer-backend" >/dev/null
$KUBECTL -n "$NAMESPACE" rollout status "deploy/${RELEASE}-visualizer-backend" --timeout=120s
