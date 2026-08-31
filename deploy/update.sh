#!/usr/bin/env bash
#
# Update the frame in place. Installed as /usr/local/bin/picture-frame-update
# and normally invoked from a Mac via deploy/remote-update.sh over Tailscale SSH.
#
#     picture-frame-update                     # latest release
#     picture-frame-update --version v1.4.0    # a specific release
#     picture-frame-update --from-file f.tgz   # a locally built tarball
#
# Verifies before unpacking, swaps atomically, health-checks, and rolls back to
# the previous release on any failure. Configuration and the paired Nextcloud
# credential are never touched.

set -euo pipefail

PREFIX=/opt/picture-frame
RELEASES="$PREFIX/releases"
CURRENT="$PREFIX/current"
CONFIG_DIR=/etc/picture-frame
SOCKET=/run/picture-frame/frontend.sock
KEEP_RELEASES=3
HEALTH_TIMEOUT=30

# Override in /etc/picture-frame/update.conf, which install.sh does not manage.
REPO="${PICTURE_FRAME_REPO:-predoli/picture_minimal}"
# shellcheck disable=SC1091
[[ -f "$CONFIG_DIR/update.conf" ]] && source "$CONFIG_DIR/update.conf"

VERSION=""
FROM_FILE=""

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
	case "$1" in
		--version)   VERSION="${2:?--version needs a value}"; shift 2 ;;
		--from-file) FROM_FILE="${2:?--from-file needs a path}"; shift 2 ;;
		-h|--help)   sed -n '2,14p' "$0" | sed 's/^# \?//'; exit 0 ;;
		*)           die "unknown argument: $1" ;;
	esac
done

[[ $EUID -eq 0 ]] || die "run with sudo"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The repository is public, so releases are fetched anonymously and no frame
# holds a GitHub credential. Keeping one fewer secret on a wall-mounted
# appliance is worth more than the privacy of a picture-frame build.
fetch() {
	curl -fsSL --retry 3 --retry-delay 2 -o "$2" "$1"
}

# --- work out what we are installing -----------------------------------------
if [[ -n "$FROM_FILE" ]]; then
	[[ -f "$FROM_FILE" ]] || die "no such file: $FROM_FILE"
	cp "$FROM_FILE" "$WORK/release.tgz"
	VERSION="local-$(date +%Y%m%d%H%M%S)"
	log "Installing local build as $VERSION"
else
	if [[ -z "$VERSION" ]]; then
		log "Looking up the latest release of $REPO"
		fetch "https://api.github.com/repos/$REPO/releases/latest" "$WORK/release.json" ||
			die "could not reach the GitHub releases API"
		VERSION="$(grep -m1 '"tag_name"' "$WORK/release.json" | cut -d'"' -f4)"
		[[ -n "$VERSION" ]] || die "could not determine the latest version"
	fi

	CURRENT_VERSION="$(basename "$(readlink -f "$CURRENT" 2>/dev/null || echo none)")"
	if [[ "$CURRENT_VERSION" == "$VERSION" ]]; then
		log "Already running $VERSION; nothing to do."
		exit 0
	fi

	TARBALL="picture-frame-$VERSION-aarch64.tar.gz"
	BASE="https://github.com/$REPO/releases/download/$VERSION"

	log "Downloading $TARBALL"
	fetch "$BASE/$TARBALL" "$WORK/release.tgz" || die "download failed"
	fetch "$BASE/SHA256SUMS" "$WORK/SHA256SUMS" || die "could not fetch SHA256SUMS"

	# Verify before unpacking. A corrupt or tampered archive must never reach the
	# filesystem, let alone the running service.
	log "Verifying checksum"
	EXPECTED="$(awk -v f="$TARBALL" '$2 == f || $2 == "*"f {print $1}' "$WORK/SHA256SUMS")"
	[[ -n "$EXPECTED" ]] || die "SHA256SUMS does not mention $TARBALL"
	ACTUAL="$(sha256sum "$WORK/release.tgz" | cut -d' ' -f1)"
	[[ "$EXPECTED" == "$ACTUAL" ]] ||
		die "checksum mismatch (expected $EXPECTED, got $ACTUAL); nothing was installed"
fi

# --- unpack -------------------------------------------------------------------
NEW_DIR="$RELEASES/$VERSION"
PREVIOUS="$(readlink -f "$CURRENT" 2>/dev/null || true)"

log "Unpacking to $NEW_DIR"
rm -rf "$NEW_DIR"
mkdir -p "$NEW_DIR/bin"
tar -xzf "$WORK/release.tgz" -C "$WORK" --strip-components=0
SRC="$(dirname "$(find "$WORK" -name picture-backend -type f -print -quit)")"
[[ -n "$SRC" ]] || die "archive does not contain picture-backend"
install -m 0755 "$SRC/picture-backend" "$SRC/picture-frontend" "$NEW_DIR/bin/"

# --- swap ---------------------------------------------------------------------
activate() {
	ln -sfn "$1" "$PREFIX/current.new"
	mv -Tf "$PREFIX/current.new" "$CURRENT"
}

rollback() {
	if [[ -z "$PREVIOUS" || ! -d "$PREVIOUS" ]]; then
		die "update failed and there is no previous release to roll back to"
	fi
	warn "Rolling back to $(basename "$PREVIOUS")"
	activate "$PREVIOUS"
	systemctl restart picture-backend picture-frontend || true
	echo "--- journal for the failed version ---" >&2
	journalctl -u picture-backend -u picture-frontend -n 40 --no-pager >&2 || true
	die "update to $VERSION failed; rolled back to $(basename "$PREVIOUS")"
}

# The frontend is the socket client, so stop it first and start it last.
log "Stopping services"
systemctl stop picture-frontend picture-backend || true

activate "$NEW_DIR"

log "Starting services"
if ! systemctl start picture-backend; then rollback; fi
if ! systemctl start picture-frontend; then rollback; fi

# --- health check -------------------------------------------------------------
# Being "active" is necessary but not sufficient; the socket has to answer. This
# is the same NDJSON exchange the frontend performs.
log "Health check"
healthy=false
for _ in $(seq "$HEALTH_TIMEOUT"); do
	sleep 1
	systemctl is-active --quiet picture-backend || continue
	systemctl is-active --quiet picture-frontend || continue
	[[ -S "$SOCKET" ]] || continue

	reply="$(printf '{"w":1,"h":1,"format":"rgb565","last":""}\n' |
		timeout 5 socat - "UNIX-CONNECT:$SOCKET" 2>/dev/null || true)"
	if [[ "$reply" == *'{'* ]]; then
		healthy=true
		break
	fi
done

if [[ "$healthy" != true ]]; then
	warn "the new version did not answer on $SOCKET within ${HEALTH_TIMEOUT}s"
	rollback
fi

# --- prune --------------------------------------------------------------------
log "Pruning old releases"
# shellcheck disable=SC2012
ls -1dt "$RELEASES"/*/ 2>/dev/null | tail -n +$((KEEP_RELEASES + 1)) | while read -r old; do
	[[ "$(readlink -f "$old")" == "$(readlink -f "$CURRENT")" ]] && continue
	rm -rf "$old"
done

log "Now running $VERSION"
