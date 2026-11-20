#!/usr/bin/env bash
# P1 gate: proves the first-increment completion evidence (AGENT_START.md)
# from a clean checkout. Every client step uses only the public HTTP API.
#
#   scripts/p1-gate.sh            # run, then tear down
#   KEEP=1 scripts/p1-gate.sh     # leave the stack up afterwards
#
# Needs: docker compose, go, python3. Writes evidence to $WORK/gate.log.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
WORK=${WORK:-$(mktemp -d)}
PORT=${PORT:-18181}
RPORT=$((PORT + 1))
P=yak-gate-$$
R=yak-gate-restore-$$
LOG=$WORK/gate.log
: > "$LOG"
say() { echo "== $*" | tee -a "$LOG"; }
ok() { echo "PASS: $*" | tee -a "$LOG"; }
die() { echo "FAIL: $*" | tee -a "$LOG"; exit 1; }

mkenv() { # project port
  printf 'YAK_DB_PASSWORD=%s\nYAK_PORT=%s\nYAK_BIND=127.0.0.1\nYAK_IMAGE=yak:gate\n' \
    "$(python3 -I -c 'import secrets;print(secrets.token_hex(24))')" "$2" > "$WORK/$1.env"
}
dc() { local proj=$1; shift; docker compose -p "$proj" --env-file "$WORK/$proj.env" -f deploy/compose.yaml "$@"; }
cleanup() {
  if [[ "${KEEP:-}" != 1 ]]; then
    dc "$P" down -v >/dev/null 2>&1 || true
    dc "$R" down -v >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT
waitready() {
  for _ in $(seq 1 60); do curl -fsS "http://127.0.0.1:$1/readyz" >/dev/null 2>&1 && return 0; sleep 1; done
  die "server on :$1 not ready"
}

say "build CLI and image ($(git rev-parse --short HEAD), $(go version | cut -d' ' -f3))"
go build -o "$WORK/yak" ./cmd/yak
A=$WORK/yak
mkenv "$P" "$PORT"; mkenv "$R" "$RPORT"
dc "$P" build -q >/dev/null 2>&1 || dc "$P" build
docker image inspect yak:gate --format 'image {{.Id}}' | tee -a "$LOG"

say "start clean stack (Postgres + migrate + Yak)"
dc "$P" up -d --quiet-pull 2>>"$LOG"
waitready "$PORT"
URL=http://127.0.0.1:$PORT
curl -fsS "$URL/v1/server" | python3 -I -c 'import sys,json;d=json.load(sys.stdin);print("discovery:",d["protocol"],d["implementation"])' | tee -a "$LOG"
code=$(curl -s -o /dev/null -w '%{http_code}' "$URL/v1/me"); [[ $code == 401 ]] || die "unauthenticated /v1/me returned $code"
ok "API requires authentication (401 without a token)"

say "explicit bootstrap (one-time; token printed once)"
dc "$P" run --rm -T yak admin bootstrap --handle admin > "$WORK/admin.token" 2>>"$LOG"
dc "$P" run --rm -T yak admin bootstrap --handle admin2 >/dev/null 2>&1 && die "second bootstrap must be refused"
ok "bootstrap created one administrator and refuses a second run"

say "client 1: CLI over HTTP (two actors, private channel)"
export YAK_CONFIG=$WORK/cli.json YAK_URL=$URL
YAK=$A ADMIN_TOKEN_FILE=$WORK/admin.token WORK=$WORK scripts/smoke.sh > "$WORK/smoke.out" 2>&1 || { cat "$WORK/smoke.out"; die "CLI smoke"; }
grep -q "bob post to private exit=4" "$WORK/smoke.out" || die "private channel write was not refused as not_found (AUTH-015)"
grep -q "secret" <(sed -n '/bob threads/,/bob post/p' "$WORK/smoke.out" | grep -v '^---') && die "private thread leaked to bob"
eval "$(tail -1 "$WORK/smoke.out" | tr ' ' '\n' | grep =)"
ok "CLI: two actors exchanged messages; private channel hidden and write-protected (board=$board)"

say "client 2: black-box conformance suite (independent raw HTTP client)"
YAK_CONFORMANCE_URL=$URL YAK_CONFORMANCE_ADMIN_TOKEN_FILE="$WORK/admin.token" \
  go test ./conformance/ -count=1 -v -timeout 600s > "$WORK/conformance.out" 2>&1 || { tail -40 "$WORK/conformance.out"; die "conformance"; }
grep -E '^(--- |ok|FAIL)' "$WORK/conformance.out" | tee -a "$LOG"
ok "conformance suite passed"

as() { local who=$1; shift; YAK_TOKEN_FILE="$WORK/$who.token" YAK_BOARD=$board "$A" "$@"; }

say "CT-001: API restart preserves state and safe event continuation"
before=$(as bob read "$thread" --json | python3 -I -c 'import sys,json;print(len(json.load(sys.stdin)["messages"]))')
cur=$(as bob events --from latest --json | python3 -I -c 'import sys,json;print(json.load(sys.stdin)["next_cursor"])')
dc "$P" restart yak >/dev/null 2>&1; waitready "$PORT"
after=$(as bob read "$thread" --json | python3 -I -c 'import sys,json;print(len(json.load(sys.stdin)["messages"]))')
[[ $before == "$after" ]] || die "message count changed across restart ($before -> $after)"
m=$(as alice reply "$thread" --text "after restart")
got=$(as bob events --cursor "$cur" --json | python3 -I -c 'import sys,json;e=json.load(sys.stdin)["events"];print(",".join(x["type"]+":"+x["resource"]["id"] for x in e))')
[[ $got == "message.created:$m" ]] || die "continuation after restart returned [$got], want message.created:$m"
ok "state survived API restart; old cursor continued with exactly the new event"

say "full stack restart (Postgres too) keeps data on the volume"
dc "$P" stop >/dev/null 2>&1; dc "$P" up -d >/dev/null 2>&1; waitready "$PORT"
n=$(as bob read "$thread" --json | python3 -I -c 'import sys,json;print(len(json.load(sys.stdin)["messages"]))')
[[ $n == $((after + 1)) ]] || die "stack restart lost data ($n)"
wc=$(as bob events --from latest --json | python3 -I -c 'import sys,json;print(json.load(sys.stdin)["next_cursor"])')
set +e; as bob wait --cursor "$wc" --timeout 1s >/dev/null 2>&1; rc=$?; set -e
[[ $rc == 7 ]] || die "wait timeout must exit 7, got $rc"
ok "stack restart preserved state; wait timeout is a distinct exit"

say "backup and restore drill (pg_dump -> fresh instance -> rotate epoch)"
cur=$(as bob events --from latest --json | python3 -I -c 'import sys,json;print(json.load(sys.stdin)["next_cursor"])')
dc "$P" exec -T postgres pg_dump -U abby -d abby -Fc > "$WORK/backup.dump"
ls -l "$WORK/backup.dump" | tee -a "$LOG"
dc "$R" up -d postgres >/dev/null 2>&1
for _ in $(seq 1 30); do dc "$R" exec -T postgres pg_isready -U abby -d abby >/dev/null 2>&1 && break; sleep 1; done
dc "$R" exec -T postgres pg_restore -U abby -d abby --no-owner < "$WORK/backup.dump"
dc "$R" up -d >/dev/null 2>&1; waitready "$RPORT"
n2=$(YAK_URL=http://127.0.0.1:$RPORT as bob read "$thread" --json | python3 -I -c 'import sys,json;print(len(json.load(sys.stdin)["messages"]))')
[[ $n2 == "$n" ]] || die "restored instance has $n2 messages, want $n"
dc "$R" run --rm -T yak admin rotate-epoch | tee -a "$LOG"
set +e; YAK_URL=http://127.0.0.1:$RPORT as bob events --cursor "$cur" > "$WORK/resync.out" 2>&1; rc=$?; set -e
[[ $rc == 8 ]] && grep -q resync_required "$WORK/resync.out" || { cat "$WORK/resync.out"; die "pre-restore cursor must require resync (exit $rc)"; }
ok "restore reproduced state; rotated history epoch forces explicit resync for old cursors"

say "P1 gate PASSED ($(date -u +%FT%TZ)); evidence in $LOG"
