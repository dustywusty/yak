#!/usr/bin/env bash
# P2 gate: tasks and claims, from a clean checkout. Proves, through public
# interfaces only:
#   1. upgrade: a P1 deployment with live data migrates to P2; old threads and
#      pre-upgrade cursors keep working (BOOT-05 evidence)
#   2. the full black-box conformance suite passes on the upgraded stack
#   3. interruption recovery with the real CLI runner: a claimed worker is
#      killed (no release), a replacement claims after the lease lapses, the
#      dead attempt's proof cannot settle the new attempt (BOOT-04 dry run)
#
#   P1_IMAGE=yak:coord-<sha> scripts/p2-gate.sh   # image of the previous release
set -euo pipefail
cd "$(dirname "$0")/.."
WORK=${WORK:-$(mktemp -d)}
PORT=${PORT:-18281}
P=yak-p2gate-$$
P1_IMAGE=${P1_IMAGE:?set P1_IMAGE to the previous (P1) server image}
LOG=$WORK/gate.log
: > "$LOG"
say() { echo "== $*" | tee -a "$LOG"; }
ok() { echo "PASS: $*" | tee -a "$LOG"; }
die() { echo "FAIL: $*" | tee -a "$LOG"; exit 1; }
j() { python3 -I -c "import sys,json;d=json.load(sys.stdin);print($1)"; }
dc() { docker compose -p "$P" --env-file "$WORK/env" -f deploy/compose.yaml "$@"; }
cleanup() { [[ "${KEEP:-}" == 1 ]] || dc down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT
waitready() { for _ in $(seq 1 60); do curl -fsS "http://127.0.0.1:$PORT/readyz" >/dev/null 2>&1 && return 0; sleep 1; done; die "not ready"; }
setimg() { printf 'YAK_DB_PASSWORD=%s\nYAK_PORT=%s\nYAK_BIND=127.0.0.1\nYAK_IMAGE=%s\n' "$PW" "$PORT" "$1" > "$WORK/env"; }

say "build CLI and candidate image ($(git rev-parse --short HEAD))"
go build -o "$WORK/yak" ./cmd/yak
A=$WORK/yak
docker build -q -t yak:p2gate . >/dev/null
docker image inspect "$P1_IMAGE" --format "previous image $P1_IMAGE {{.Id}}" | tee -a "$LOG"
docker image inspect yak:p2gate --format 'candidate image yak:p2gate {{.Id}}' | tee -a "$LOG"
PW=$(python3 -I -c 'import secrets;print(secrets.token_hex(24))')

say "1. start the PREVIOUS release and create P1 data"
setimg "$P1_IMAGE"; dc up -d --quiet-pull 2>>"$LOG"; waitready
URL=http://127.0.0.1:$PORT
curl -fsS "$URL/v1/server" | j 'd["protocol"]["profiles"]' | tee -a "$LOG"
dc run --rm -T yak admin bootstrap --handle admin > "$WORK/admin.token" 2>>"$LOG"
export YAK_CONFIG=$WORK/cli.json YAK_URL=$URL
YAK=$A ADMIN_TOKEN_FILE=$WORK/admin.token WORK=$WORK scripts/smoke.sh > "$WORK/smoke.out" 2>&1 || { cat "$WORK/smoke.out"; die "P1 smoke on previous release"; }
eval "$(tail -1 "$WORK/smoke.out" | tr ' ' '\n' | grep =)"
as() { local who=$1; shift; YAK_TOKEN_FILE="$WORK/$who.token" YAK_BOARD=$board "$A" "$@"; }
before=$(as bob read "$thread" --json | j 'len(d["messages"])')
cur=$(as bob events --from latest --json | j 'd["next_cursor"]')
ok "previous release running with $before messages in thread $thread"

say "upgrade: switch image, run migrate, restart"
setimg yak:p2gate
dc up -d 2>>"$LOG"; waitready
dc logs migrate 2>/dev/null | grep -E 'applied|up to date' | tail -3 | tee -a "$LOG"
curl -fsS "$URL/v1/server" | j 'd["protocol"]["profiles"]' | tee -a "$LOG"
curl -fsS "$URL/v1/server" | j '"yak.p2.tasks" in d["protocol"]["profiles"]' | grep -q True || die "upgraded server does not advertise yak.p2.tasks"
after=$(as bob read "$thread" --json | j 'len(d["messages"])')
[[ $before == "$after" ]] || die "upgrade changed message count ($before -> $after)"
m=$(as alice reply "$thread" --text "after upgrade")
got=$(as bob events --cursor "$cur" --json | j '",".join(x["type"]+":"+x["resource"]["id"] for x in d["events"])')
[[ $got == "message.created:$m" ]] || die "pre-upgrade cursor returned [$got], want message.created:$m"
ok "P1 data and a pre-upgrade cursor survived the P2 migration"

say "2. black-box conformance on the upgraded stack"
YAK_CONFORMANCE_URL=$URL YAK_CONFORMANCE_ADMIN_TOKEN_FILE="$WORK/admin.token" \
  go test ./conformance/ -count=1 -v -timeout 900s > "$WORK/conformance.out" 2>&1 || { grep -E '^(--- FAIL|FAIL)|_test.go:[0-9]+:' "$WORK/conformance.out" | head -40; die "conformance"; }
grep -E '^(--- |ok)' "$WORK/conformance.out" | tee -a "$LOG"
[[ $(grep -c '^--- SKIP' "$WORK/conformance.out") == 0 ]] || die "conformance skipped tests on a P2 server"
ok "conformance suite passed with no skips"

say "3. interruption recovery with the real runner"
adm() { YAK_TOKEN_FILE="$WORK/admin.token" YAK_BOARD=$board "$A" "$@"; }
adm grant create --any --pattern "work" --caps read,post,claim,task_manage,channel_manage >/dev/null
task=$(as alice task create work --title "interruptible job" --text "sleep, then succeed" --json | j 'd["id"]')
# Worker 1 (bob): claims through the runner and gets killed with SIGKILL, so
# it cannot release. Its stdout report is lost with it.
rm -f "$WORK/w1.child.pid"
( YAK_TOKEN_FILE="$WORK/bob.token" YAK_BOARD=$board exec "$A" run "$task" --lease 15s --run-id worker1 -- sh -c 'echo $$ > "$1"; exec sleep 600' gate-child "$WORK/w1.child.pid" ) > "$WORK/w1.out" 2>&1 &
W1=$!
for _ in $(seq 1 30); do [[ $(as bob task show "$task" --json | j 'd["state"]') == claimed ]] && break; sleep 0.5; done
c1=$(as bob task show "$task" --json | j 'd["current_claim"]["id"]+" "+d["current_claim"]["execution_id"]+" "+str(d["epoch"])')
# Record the gate child explicitly; do not kill unrelated host processes.
for _ in $(seq 1 30); do [[ -s "$WORK/w1.child.pid" ]] && break; sleep 0.1; done
[[ -s "$WORK/w1.child.pid" ]] || die "worker1 child did not start"
kill -9 "$W1"
kill -9 "$(cat "$WORK/w1.child.pid")"
ok "worker1 claimed ($c1) and was killed without releasing"
# A manual claim by worker1's actor cannot take the task while the lease is live.
set +e; as bob task claim "$task" --proof-file "$WORK/stale.proof" >/dev/null 2>"$WORK/claim2.err"; rc=$?; set -e
[[ $rc == 9 ]] || die "second claim during a live lease must exit 9 (task_claimed), got $rc: $(cat "$WORK/claim2.err")"
# Worker 2 (alice, eligible: task is unassigned) waits on claim-next.
set +e
YAK_TOKEN_FILE="$WORK/alice.token" YAK_BOARD=$board "$A" run --channel work --wait 60s --run-id worker2 --json \
  --summary "completed after takeover" --commit "$(git rev-parse HEAD)" -- sh -c 'echo working; sleep 1' > "$WORK/w2.json" 2>"$WORK/w2.err"
rc=$?; set -e
[[ $rc == 0 ]] || { cat "$WORK/w2.err"; die "worker2 runner exit $rc"; }
python3 -I -c 'import json;d=json.load(open("'"$WORK/w2.json"'"));print("worker2 report:",{k:d[k] for k in ("task_id","claim_id","execution_id","epoch","run_id","outcome","renewals")})' | tee -a "$LOG"
final=$(as alice task show "$task" --json | j 'd["state"]+" epoch="+str(d["epoch"])')
[[ $final == completed* ]] || die "task state after takeover: $final"
# Replay worker1's stale ownership: build a request with worker1's claim ID.
# The proof itself died with worker1, so use the claims history to show the
# attempts and that only the second settled the task.
as alice task claims "$task" --json > "$WORK/claims.json"
python3 -I -c '
import json
for line in open("'"$WORK/claims.json"'"):
    c=json.loads(line)
    print(" claim",c["id"],"epoch",c["epoch"],"actor",c["actor_id"],"run",c.get("run_id"),"status",c["status"],"lease",c["lease_seconds"])
' | tee -a "$LOG"
ok "replacement claimed after lapse and completed; task $final"

say "stale proof cannot settle a newer attempt (explicit check)"
t2=$(as alice task create work --title "fencing check" --text "x" --json | j 'd["id"]')
as bob task claim "$t2" --lease 15s --proof-file "$WORK/old.proof" >/dev/null
for _ in $(seq 1 40); do [[ $(as bob task show "$t2" --json | j 'd["state"]') == ready ]] && break; sleep 1; done
as alice task claim "$t2" --proof-file "$WORK/new.proof" >/dev/null
set +e; as bob task complete "$t2" --proof-file "$WORK/old.proof" --summary stale > /dev/null 2>"$WORK/stale.err"; rc=$?; set -e
[[ $rc == 9 ]] && grep -q claim_lost "$WORK/stale.err" || die "stale proof must get claim_lost (exit 9), got $rc: $(cat "$WORK/stale.err")"
as alice task complete "$t2" --proof-file "$WORK/new.proof" --summary fresh >/dev/null
ok "old proof rejected with claim_lost; new attempt settled"

say "P2 gate PASSED ($(date -u +%FT%TZ)); evidence in $LOG"
