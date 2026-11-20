#!/usr/bin/env bash
# Quick CLI smoke run against a running server. Needs: YAK (binary path),
# YAK_URL, ADMIN_TOKEN_FILE, WORK (scratch dir). Uses only the public API.
set -euo pipefail
A=${YAK:-yak}
export YAK_CONFIG=$WORK/cfg.json
jid() { python3 -I -c 'import sys,json;d=json.load(sys.stdin);print(eval("d"+sys.argv[1]))' "$1"; }
sfx=$RANDOM
$A login < "$ADMIN_TOKEN_FILE"
ALICE=$($A actor create --handle "alice$sfx" --json | jid '["id"]')
BOB=$($A actor create --handle "bob$sfx" --json | jid '["id"]')
B=$($A board create "demo $sfx" --json | jid '["id"]')
export YAK_BOARD=$B
$A grant create --any --caps read,post,label,channel_manage >/dev/null
$A grant create --actor "$ALICE" --pattern sec --caps read,post >/dev/null
$A credential create --actor "$ALICE" --scopes '*' > "$WORK/alice.token"
$A credential create --actor "$BOB" --scopes '*' > "$WORK/bob.token"
$A channel create sec --private >/dev/null
as() { local who=$1; shift; YAK_TOKEN_FILE="$WORK/$who.token" "$A" "$@"; }
T=$(as alice post general --title hello --text "hi @bob$sfx")
M=$(as bob reply "$T" --text "hi alice")
as alice post sec --title secret --text classified >/dev/null
echo "--- bob threads (must not include 'secret')"; as bob threads
set +e; as bob post sec --title x --text y; echo "bob post to private exit=$?"; set -e
echo "--- read"; as bob read "$T"
echo "--- bob events"; as bob events --limit 20
echo "--- alice events"; as alice events --limit 20
echo "board=$B thread=$T msg=$M alice=$ALICE bob=$BOB"
