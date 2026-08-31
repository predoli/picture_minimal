#!/usr/bin/env bash
#
# Run the whole frame on your Mac: backend, frontend, and an SDL window.
#
#     ./deploy/run-local.sh --fake                        # no server needed
#     ./deploy/run-local.sh --server https://cloud.example.com
#
# --fake starts a stub Nextcloud that auto-approves pairing and serves two
# generated photos, so you can see the slideshow without an account. With
# --server you get the real thing: a QR code appears in the window, you scan it
# with your phone, approve, and your tagged photos appear.
#
# State lives in ./.dev and is thrown away with --reset. Ctrl-C stops everything.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

DEV_DIR="$REPO_ROOT/.dev"
FAKE_ADDR=127.0.0.1:8631
SERVER=""
USE_FAKE=false
RESET=false
WIDTH=1024
HEIGHT=600

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
	case "$1" in
		--fake)   USE_FAKE=true; shift ;;
		--server) SERVER="${2:?--server needs a URL}"; shift 2 ;;
		--reset)  RESET=true; shift ;;
		--size)   WIDTH="${2%%x*}"; HEIGHT="${2##*x}"; shift 2 ;;
		-h|--help) sed -n '2,14p' "$0" | sed 's/^# \?//'; exit 0 ;;
		*)        die "unknown argument: $1" ;;
	esac
done

if [[ "$USE_FAKE" == true ]]; then
	SERVER="http://$FAKE_ADDR"
fi
[[ -n "$SERVER" ]] || die "pass --fake or --server <url>"

if [[ "$RESET" == true ]]; then
	log "Clearing $DEV_DIR (this unpairs the frame)"
	rm -rf "$DEV_DIR"
fi
mkdir -p "$DEV_DIR/state"

# Unix socket paths cap at ~104 bytes, and a deep checkout can exceed that.
SOCKET="$(mktemp -d /tmp/pf.XXXX)/f.sock"

# --- build --------------------------------------------------------------------
# The SDL window size is fixed at compile time, so a --size change needs a
# reconfigure. DRM on the Pi reports the real panel geometry at runtime instead.
log "Building frontend (${WIDTH}x${HEIGHT})"
cmake -B build -G Ninja \
	-DFRAME_DISPLAY=sdl \
	-DFRAME_SDL_WIDTH="$WIDTH" \
	-DFRAME_SDL_HEIGHT="$HEIGHT" >/dev/null
cmake --build build >/dev/null

log "Building backend"
export GOTOOLCHAIN=auto
(cd backend && go build -o "$DEV_DIR/picture-backend" ./cmd/picture-backend)
if [[ "$USE_FAKE" == true ]]; then
	(cd backend && go build -o "$DEV_DIR/fake-nextcloud" ./cmd/fake-nextcloud)
fi

# --- config -------------------------------------------------------------------
cat >"$DEV_DIR/config.toml" <<EOF
server = "$SERVER"
selection = "tag"
tag = "Frame"
socket_path = "$SOCKET"
state_dir = "$DEV_DIR/state"
hostname = "$(hostname -s)-dev"
sync_interval = "30s"
verbose = true
EOF

PIDS=()
cleanup() {
	for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
	rm -rf "$(dirname "$SOCKET")"
}
trap cleanup EXIT INT TERM

# --- run ----------------------------------------------------------------------
if [[ "$USE_FAKE" == true ]]; then
	log "Starting fake Nextcloud on $FAKE_ADDR"
	"$DEV_DIR/fake-nextcloud" -addr "$FAKE_ADDR" &
	PIDS+=($!)
fi

log "Starting backend"
"$DEV_DIR/picture-backend" --config "$DEV_DIR/config.toml" &
PIDS+=($!)

for _ in $(seq 100); do
	[[ -S "$SOCKET" ]] && break
	sleep 0.1
done
[[ -S "$SOCKET" ]] || die "backend did not create $SOCKET"

if [[ "$USE_FAKE" != true ]]; then
	echo
	log "A QR code will appear in the window. Scan it, sign in, and approve."
	log "Then tag photos \"Frame\" in Nextcloud; they appear within 30s."
fi

log "Starting frontend (Ctrl-C to stop everything)"
FRAME_SOCKET="$SOCKET" FRAME_INTERVAL_MS=8000 FRAME_FADE_MS=800 \
	./build/picture-frontend
