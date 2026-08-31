#!/usr/bin/env bash
#
# Update a frame from your Mac, over Tailscale.
#
#     ./deploy/remote-update.sh livingroom                  # latest release
#     ./deploy/remote-update.sh livingroom --version v1.4.0 # a specific release
#     ./deploy/remote-update.sh livingroom --local          # this working tree
#
# --local builds the aarch64 binaries with Nix, pushes them, and runs the same
# swap, health-check, and rollback path a real release takes. The risky code is
# exercised identically whether the bits came from CI or your laptop.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

FRAME="${1:-}"
[[ -n "$FRAME" ]] || die "usage: $0 <frame-hostname> [--version vX.Y.Z | --local]"
shift

LOCAL=false
ARGS=()
while [[ $# -gt 0 ]]; do
	case "$1" in
		--local)   LOCAL=true; shift ;;
		--version) ARGS+=(--version "${2:?--version needs a value}"); shift 2 ;;
		*)         die "unknown argument: $1" ;;
	esac
done

command -v tailscale &>/dev/null || die "tailscale not found in PATH"

# Fail early with a clear message rather than hanging on an SSH connect.
log "Checking that $FRAME is on the tailnet"
tailscale status --json 2>/dev/null | grep -q "\"$FRAME\"" ||
	echo "  (not found in tailscale status; trying anyway, MagicDNS may still resolve it)"

if [[ "$LOCAL" == true ]]; then
	command -v nix &>/dev/null || die "--local needs Nix to cross-compile for aarch64"

	log "Building aarch64 binaries from the working tree"
	STAGE="$(mktemp -d)"
	trap 'rm -rf "$STAGE"' EXIT
	mkdir -p "$STAGE/picture-frame/bin"

	FRONTEND_OUT="$(nix build "$REPO_ROOT#frontend-aarch64" --no-link --print-out-paths)"
	BACKEND_OUT="$(nix build "$REPO_ROOT#backend-aarch64" --no-link --print-out-paths)"

	install -m 0755 "$FRONTEND_OUT/bin/picture-frontend" "$STAGE/picture-frame/bin/"
	install -m 0755 "$BACKEND_OUT/bin/picture-backend" "$STAGE/picture-frame/bin/"
	tar -czf "$STAGE/local.tar.gz" -C "$STAGE" picture-frame

	log "Copying to $FRAME"
	tailscale file cp "$STAGE/local.tar.gz" "$FRAME:" 2>/dev/null ||
		scp "$STAGE/local.tar.gz" "$FRAME:/tmp/picture-frame-local.tar.gz"

	ARGS=(--from-file /tmp/picture-frame-local.tar.gz)
fi

log "Running picture-frame-update on $FRAME"
# sudo -n so a missing sudoers rule fails loudly instead of hanging on a password
# prompt that a non-interactive Tailscale SSH session can never satisfy.
exec tailscale ssh "$FRAME" -- sudo -n /usr/local/bin/picture-frame-update "${ARGS[@]}"
