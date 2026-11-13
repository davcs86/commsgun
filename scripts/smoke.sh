#!/usr/bin/env bash
# Runs both images together on the fake provider and asserts the public contract end to end.
set -euo pipefail

API_IMAGE="${1:?usage: smoke.sh <api-image> <ui-image>}"
UI_IMAGE="${2:?usage: smoke.sh <api-image> <ui-image>}"

API_MEM_LIMIT_MIB=60
UI_MEM_LIMIT_MIB=140
ENQUEUE_PORT="${SMOKE_ENQUEUE_PORT:-9200}"
UI_PORT="${SMOKE_UI_PORT:-9202}"

SUFFIX="$(head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
API="commsgun-smoke-api-${SUFFIX}"
UI="commsgun-smoke-ui-${SUFFIX}"
NETWORK="commsgun-smoke-${SUFFIX}"
VOLUME="commsgun-smoke-${SUFFIX}"
WORK="$(mktemp -d)"

APP_KEY="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
ADMIN_API_TOKEN="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
SESSION_SECRET="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
ADMIN_PASSWORD="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"

ENQUEUE="http://127.0.0.1:${ENQUEUE_PORT}"
UI_URL="http://127.0.0.1:${UI_PORT}"
ORIGIN="http://localhost:${UI_PORT}"
FORWARDED_HOST="localhost:${UI_PORT}"

RCPT_A="smoke-a@example.com"
RCPT_B="smoke-b@example.com"
SENDER="Smoke <smoke@example.com>"

CONFIG='{"apps":[{"id":"smoke","key":"'"$APP_KEY"'","channels":{
  "email":{"per_minute":10,"per_day":80,"senders":["'"$SENDER"'"],"default_sender":"'"$SENDER"'"},
  "sms":{"per_minute":5,"per_day":50}}}],
 "providers":{"email":{"name":"fake","per_minute":60,"per_day":100},"sms":null}}'

cleanup() {
  docker rm -f "$UI" "$API" >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
  docker volume rm "$VOLUME" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

pass() { echo "PASS $1"; }

fail() {
  echo "FAIL $1${2:+: $2}" >&2
  docker logs "$API" >&2 || true
  docker logs "$UI" >&2 || true
  exit 1
}

expect_eq() { # name expected actual
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "expected $2, got $3"; fi
}

port_busy() {
  (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}

wait_ok() { # name url
  local _
  for _ in $(seq 1 30); do
    if curl -fsS "$2" >/dev/null 2>&1; then
      pass "$1"
      return 0
    fi
    sleep 1
  done
  fail "$1" "no 200 from $2 within 30 s"
}

# Requests to the UI must carry the proxy headers the server derives its origin from.
ui_curl() {
  curl -sS -H 'X-Forwarded-Proto: http' -H "X-Forwarded-Host: ${FORWARDED_HOST}" \
    -H 'X-Forwarded-For: 203.0.113.9' "$@"
}

enqueue() { # idempotency-key key body -> prints "<body>\n<status>"
  curl -sS -w '\n%{http_code}' -X POST "$ENQUEUE/v1/messages" \
    -H 'X-Commsgun-App: smoke' -H "Authorization: Bearer $2" \
    -H "Idempotency-Key: $1" -H 'Content-Type: application/json' -d "$3"
}

mem_mib() { # container -> resident MiB (integer, rounded up)
  docker stats --no-stream --format '{{.MemUsage}}' "$1" | awk '{
    v = $1; u = v; sub(/[0-9.]+/, "", u); sub(/[A-Za-z]+$/, "", v)
    if (u == "B") v /= 1048576; else if (u == "KiB") v /= 1024
    else if (u == "GiB") v *= 1024; else if (u != "MiB") v = 99999
    printf "%d\n", (v == int(v)) ? v : int(v) + 1 }'
}

for p in "$ENQUEUE_PORT" "$UI_PORT"; do
  if port_busy "$p"; then
    echo "FAIL port $p is busy; set SMOKE_ENQUEUE_PORT / SMOKE_UI_PORT" >&2
    exit 1
  fi
done

docker network create "$NETWORK" >/dev/null
docker volume create "$VOLUME" >/dev/null

docker run -d --name "$API" --network "$NETWORK" \
  --read-only --cap-drop ALL --security-opt no-new-privileges --memory 256m \
  -v "$VOLUME":/data -p "127.0.0.1:${ENQUEUE_PORT}:9200" \
  -e COMMSGUN_CONFIG="$CONFIG" -e COMMSGUN_ALLOW_FAKE=1 \
  -e ADMIN_API_TOKEN="$ADMIN_API_TOKEN" \
  "$API_IMAGE" >/dev/null
docker run -d --name "$UI" --network "$NETWORK" \
  --read-only --cap-drop ALL --security-opt no-new-privileges --memory 256m \
  -p "127.0.0.1:${UI_PORT}:9202" \
  -e API_BASE_URL="http://${API}:9201" -e ORIGIN="$ORIGIN" \
  -e ADMIN_API_TOKEN="$ADMIN_API_TOKEN" -e SESSION_SECRET="$SESSION_SECRET" \
  -e ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  "$UI_IMAGE" >/dev/null

wait_ok "api healthz" "$ENQUEUE/healthz"
wait_ok "ui healthz" "$UI_URL/healthz"

email_body='{"channel":"email","to":["'"$RCPT_A"'","'"$RCPT_B"'"],"subject":"smoke","text":"smoke"}'
out="$(enqueue smoke-1 "$APP_KEY" "$email_body")"
expect_eq "enqueue email 202" 202 "${out##*$'\n'}"
out="$(enqueue smoke-1 "$APP_KEY" "$email_body")"
expect_eq "idempotent repeat 200" 200 "${out##*$'\n'}"
case "$out" in
  *'"duplicate":true'*) pass "idempotent repeat is a duplicate" ;;
  *) fail "idempotent repeat is a duplicate" "body lacks \"duplicate\":true" ;;
esac
out="$(enqueue smoke-2 "wrong-${APP_KEY}" "$email_body")"
expect_eq "wrong key 401" 401 "${out##*$'\n'}"
out="$(enqueue smoke-3 "$APP_KEY" '{"channel":"sms","to":["+15551234567"],"body":"smoke"}')"
expect_eq "enqueue sms 202" 202 "${out##*$'\n'}"

code="$(ui_curl -o /dev/null -w '%{http_code}' "$UI_URL/")"
expect_eq "ui unauthenticated GET / is 303" 303 "$code"
location="$(ui_curl -o /dev/null -w '%{redirect_url}' "$UI_URL/")"
case "$location" in
  */login*) pass "ui redirects to /login" ;;
  *) fail "ui redirects to /login" "Location was $location" ;;
esac

code="$(curl -sS -o /dev/null -w '%{http_code}' "$ENQUEUE/admin/v1/stats")"
expect_eq "admin API not on enqueue port" 404 "$code"

ui_curl -D "$WORK/login.headers" -o /dev/null -X POST "$UI_URL/login" \
  -H "Origin: ${ORIGIN}" --data-urlencode 'username=admin' \
  --data-urlencode "password=${ADMIN_PASSWORD}"
SESSION="$(grep -i '^set-cookie: commsgun_session=' "$WORK/login.headers" |
  sed -E 's/^[^=]*=([^;]*).*/\1/' | tr -d '\r')"
[ -n "$SESSION" ] || fail "ui login" "no commsgun_session cookie in the response"
pass "ui login"

ui_page() { # path
  ui_curl -H "Cookie: commsgun_session=${SESSION}" "$UI_URL$1"
}

sent_page_lists_both() {
  local page
  page="$(ui_page '/messages?state=sent' || true)"
  case "$page" in
    *"$RCPT_A"*"$RCPT_B"* | *"$RCPT_B"*"$RCPT_A"*) return 0 ;;
    *) return 1 ;;
  esac
}

overview_shows_held() {
  case "$(ui_page / || true)" in *'1 held'*) return 0 ;; *) return 1 ;; esac
}

wait_for() { # name function
  local _
  for _ in $(seq 1 30); do
    if "$2"; then
      pass "$1"
      return 0
    fi
    sleep 1
  done
  fail "$1" "not seen within 30 s"
}

wait_for "ui lists both recipients as sent" sent_page_lists_both
wait_for "ui overview shows the held sms" overview_shows_held

docker restart "$API" >/dev/null
wait_ok "api healthz after restart" "$ENQUEUE/healthz"
wait_for "sent messages persisted across api restart" sent_page_lists_both

for c in "$API" "$UI"; do
  rd="$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$c")"
  expect_eq "read-only rootfs $c" true "$rd"
done
api_user="$(docker inspect --format '{{.Config.User}}' "$API")"
expect_eq "api user" 10001:10001 "$api_user"
ui_uid="$(docker exec "$UI" id -u)"
expect_eq "ui uid" 10001 "$ui_uid"

api_mem="$(mem_mib "$API")"
ui_mem="$(mem_mib "$UI")"
echo "memory api: ${api_mem} MiB (limit ${API_MEM_LIMIT_MIB})"
echo "memory ui:  ${ui_mem} MiB (limit ${UI_MEM_LIMIT_MIB})"
if [ "$api_mem" -le "$API_MEM_LIMIT_MIB" ]; then pass "api memory"; else fail "api memory" "${api_mem} MiB"; fi
if [ "$ui_mem" -le "$UI_MEM_LIMIT_MIB" ]; then pass "ui memory"; else fail "ui memory" "${ui_mem} MiB"; fi

echo "smoke: ok"
