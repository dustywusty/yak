#!/usr/bin/env bash
# Idempotent seed for the yak-dev coordination board (BOOT-01, BOOT-06).
# Uses only the public API through the yak CLI. Safe to rerun: every step
# resolves existing resources (by handle, board name, channel path, grant
# shape, or the root label `seed`) before creating anything, and never edits
# or deletes existing discussion. It does not depend on idempotency receipts,
# so it stays correct after they expire.
#
# Required env:
#   YAK_URL           coordination endpoint (e.g. http://127.0.0.1:8090)
#   ADMIN_TOKEN_FILE   server-admin token (kept outside Yak and runners)
#   SECRETS_DIR        where per-actor tokens are written (0700; not in git)
# Optional: YAK (CLI path), BOARD_NAME (default yak-dev)
set -euo pipefail
A=${YAK:-yak}
BOARD_NAME=${BOARD_NAME:-yak-dev}
: "${YAK_URL:?}" "${ADMIN_TOKEN_FILE:?}" "${SECRETS_DIR:?}"
mkdir -p "$SECRETS_DIR" && chmod 700 "$SECRETS_DIR"
export YAK_CONFIG=$SECRETS_DIR/seed-cli.json
unset YAK_TOKEN YAK_BOARD
adm() { YAK_TOKEN_FILE="$ADMIN_TOKEN_FILE" "$A" "$@"; }
py() { python3 -I -c "$1" "${@:2}"; }

# ---- actors: one per role (roles are conventions, enforced by grants) ----
# bash 3.2 compatible (macOS): role=id pairs in a file, read with actor().
# (actors.env keys keep hyphens; use actor <role>, do not source it.)
: > "$SECRETS_DIR/actors.env"
actor() { grep "^$1=" "$SECRETS_DIR/actors.env" | cut -d= -f2; }
for spec in coordinator:human implementer-a:agent implementer-b:agent reviewer:agent viewer:service; do
  h=${spec%%:*}; kind=${spec##*:}
  id=$(adm actor list --json | py 'import sys,json
for a in json.load(sys.stdin)["actors"]:
    if a["handle"]==sys.argv[1]: print(a["id"])' "abby-$h")
  if [[ -z $id ]]; then
    id=$(adm actor create --kind "$kind" --handle "abby-$h" --display-name "yak $h" --json | py 'import sys,json;print(json.load(sys.stdin)["id"])')
    echo "created actor abby-$h $id"
  fi
  echo "$h=$id" >> "$SECRETS_DIR/actors.env"
done

# ---- board ----
B=$(adm board list --json | py 'import sys,json
for b in json.load(sys.stdin)["boards"]:
    if b["name"]==sys.argv[1]: print(b["id"]); break' "$BOARD_NAME")
if [[ -z $B ]]; then
  B=$(adm board create "$BOARD_NAME" --json | py 'import sys,json;print(json.load(sys.stdin)["id"])')
  echo "created board $BOARD_NAME $B"
fi
export YAK_BOARD=$B

# ---- grants (explicit; created only if an identical live grant is missing) ----
ensure_grant() { # subject-actor-id-or-any pattern caps
  local subj=$1 pat=$2 caps=$3
  local have
  have=$(adm grant list --json | py 'import sys,json
subj,pat,caps=sys.argv[1],sys.argv[2],sorted(sys.argv[3].split(","))
for g in json.load(sys.stdin)["grants"]:
    s=g["subject"]
    sid="any" if s["type"]=="any_actor" else s.get("actor_id")
    if sid==subj and g["pattern"]==pat and sorted(g["capabilities"])==caps and g["effect"]=="allow":
        print("yes"); break' "$subj" "$pat" "$caps")
  if [[ -z $have ]]; then
    if [[ $subj == any ]]; then adm grant create --any --pattern "$pat" --caps "$caps" >/dev/null
    else adm grant create --actor "$subj" --pattern "$pat" --caps "$caps" >/dev/null; fi
    echo "granted $caps on $pat to $subj"
  fi
}
for role in coordinator implementer-a implementer-b reviewer; do
  ensure_grant "$(actor $role)" "*" "read,post,label"
done
for role in implementer-a implementer-b reviewer; do
  ensure_grant "$(actor $role)" "*" "read,post,label,claim"
done
ensure_grant "$(actor coordinator)" "*" "read,post,label,channel_manage,task_manage"
ensure_grant "$(actor viewer)" "*" "read"
ADMIN_ID=$(adm me --json | py 'import sys,json;print(json.load(sys.stdin)["actor"]["id"])')
ensure_grant "$ADMIN_ID" "*" "read,post,label,channel_manage"

# ---- channels ----
existing=$(adm channel list --json | py 'import sys,json;print(" ".join(c["path"] for c in json.load(sys.stdin)["channels"]))')
for c in roadmap spec server cli review ops; do
  [[ " $existing " == *" $c "* ]] || { adm channel create "$c" >/dev/null; echo "created channel $c"; }
done

# ---- per-actor credentials ----
# Issued once. If an existing token is missing a required scope (for example
# after P2 added `claim`), a replacement is issued first and the old
# credential is revoked; nothing else changes.
scopes_for() {
  case $1 in
    coordinator) echo read,post,label,channel_manage,task_manage ;;
    implementer-a|implementer-b) echo read,post,label,claim ;;
    reviewer) echo read,post,label,claim ;;
    viewer) echo read ;;
  esac
}
for role in coordinator implementer-a implementer-b reviewer viewer; do
  f=$SECRETS_DIR/$role.token
  want=$(scopes_for $role)
  old_cred=""
  if [[ -s $f ]]; then
    have=$(YAK_TOKEN_FILE="$f" "$A" me --json 2>/dev/null | py 'import sys,json
d=json.load(sys.stdin); print(d["credential"]["id"]+" "+",".join(d["credential"]["scopes"]))' || true)
    missing=$(py 'import sys
have=set(sys.argv[2].split(",")) if sys.argv[2] else set()
print(",".join(s for s in sys.argv[1].split(",") if s not in have and "*" not in have))' "$want" "${have#* }")
    [[ -n $have && -z $missing ]] && continue
    old_cred=${have%% *}
  fi
  adm credential create --actor "$(actor $role)" --scopes "$want" --boards "$B" --label "build-yak $role" --expires 2160h > "$f.new"
  chmod 600 "$f.new" && mv "$f.new" "$f"
  echo "issued credential for $role ($want) -> $f"
  if [[ -n $old_cred ]]; then adm credential revoke "$old_cred" >/dev/null; echo "revoked superseded credential $old_cred"; fi
done

# ---- seed threads (found by root label seed=<key>; never edited) ----
ensure_thread() { # key channel title text
  local key=$1 ch=$2 title=$3 text=$4 tid
  tid=$(adm threads --label "seed:$key" --json | py 'import sys
for line in sys.stdin:
    import json; print(json.loads(line)["id"]); break')
  if [[ -z $tid ]]; then
    tid=$(YAK_TOKEN_FILE="$SECRETS_DIR/coordinator.token" "$A" post "$ch" --title "$title" --text "$text" --set-label "seed=$key")
    echo "created thread $key $tid"
  fi
  # Shell-sourceable: keys use underscores (p1-gate -> p1_gate).
  printf '%s=%s\n' "$(echo "$key" | tr - _)" "$tid" >> "$SECRETS_DIR/threads.env.tmp"
}
: > "$SECRETS_DIR/threads.env.tmp"
ensure_thread welcome roadmap "Welcome to yak-dev" \
"This board coordinates Yak's own development (design §15.2). Use threads for design questions, progress, blockers, decisions, and evidence links. Until P2 tasks pass their gate, the plan of record stays in the repository (PROGRESS.md); a message saying someone took work does not create a lease."
ensure_thread p1-gate roadmap "P1 gate evidence" \
"Post P1 gate runs here: commit, command, outcome, and the gate log location. Requirement traceability: spec/protocol.md §10."
ensure_thread p2-plan roadmap "P2 plan: tasks and claims" \
"Discussion of the P2 work package (task lifecycle, claims and fencing, dependencies, claim-next, batches, runner). ADR-0004 and ADR-0008 must be accepted first."
ensure_thread viewer spec "View-only board browser (examples/viewer)" \
"Decision 2026-11-19: build a view-only browser demo after the P1 gate. Public API only, read-scoped token, snapshot then long-poll events with resync handling."
ensure_thread recovery-grants spec "Server-admin recovery grants (ADR-0002 open issue)" \
"Server administrators have no API to recover a board whose owner denied themselves board_admin. Proposal: audited POST /v1/boards/{id}/recovery-grants with a reason."
mv "$SECRETS_DIR/threads.env.tmp" "$SECRETS_DIR/threads.env"

echo "board=$B"
sed 's/^/actor /' "$SECRETS_DIR/actors.env"
cat "$SECRETS_DIR/threads.env"
