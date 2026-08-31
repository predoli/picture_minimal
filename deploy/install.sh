#!/usr/bin/env bash
#
# Install the picture frame on a fresh Raspberry Pi OS Lite (64-bit).
#
# Run from an unpacked release tarball:
#
#     sudo TS_AUTHKEY=tskey-auth-... ./install.sh
#
# Idempotent: safe to re-run on a live frame. Existing configuration and the
# paired Nextcloud credential are never overwritten.

set -euo pipefail

VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo dev)}"
PREFIX=/opt/picture-frame
RELEASE_DIR="$PREFIX/releases/$VERSION"
CONFIG_DIR=/etc/picture-frame
STATE_DIR=/var/lib/picture-frame
FRAME_USER=picture
ADMIN_USER="${SUDO_USER:-$(id -un)}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "run with sudo"
[[ -f bin/picture-backend && -f bin/picture-frontend ]] ||
	die "run this from an unpacked release tarball (bin/picture-backend not found)"

# --- packages -----------------------------------------------------------------
# libheif1 lets the HEIC decoder take its native path instead of the much slower
# WebAssembly fallback. The binaries are otherwise static. socat is used by
# update.sh to probe the socket during its health check.
log "Installing runtime packages"
apt-get update -qq
apt-get install -y --no-install-recommends libheif1 ca-certificates curl socat

# --- user and directories -----------------------------------------------------
if ! id "$FRAME_USER" &>/dev/null; then
	log "Creating system user $FRAME_USER"
	useradd --system --home-dir "$STATE_DIR" --shell /usr/sbin/nologin "$FRAME_USER"
fi
# video and render grant access to the DRM device nodes.
usermod -aG video,render "$FRAME_USER"

install -d -m 0755 "$PREFIX/releases"
install -d -m 0750 -o "$FRAME_USER" -g "$FRAME_USER" "$STATE_DIR"
install -d -m 0755 "$CONFIG_DIR"

# --- binaries -----------------------------------------------------------------
# Versioned directory plus a symlink. Swapping a symlink is atomic, and keeping
# the previous release is what makes update.sh's rollback free.
log "Installing $VERSION to $RELEASE_DIR"
install -d -m 0755 "$RELEASE_DIR/bin"
install -m 0755 bin/picture-backend bin/picture-frontend "$RELEASE_DIR/bin/"
ln -sfn "$RELEASE_DIR" "$PREFIX/current.new"
mv -Tf "$PREFIX/current.new" "$PREFIX/current"

install -m 0755 update.sh /usr/local/bin/picture-frame-update

# --- configuration ------------------------------------------------------------
if [[ ! -f "$CONFIG_DIR/config.toml" ]]; then
	log "Writing starter config to $CONFIG_DIR/config.toml"
	install -m 0644 config.example.toml "$CONFIG_DIR/config.toml"
	NEEDS_CONFIG=1
else
	log "Keeping existing $CONFIG_DIR/config.toml"
fi

# --- update permissions -------------------------------------------------------
# Tailscale SSH sessions are frequently non-interactive, so a password prompt
# would hang remote-update.sh. Scope the rule to exactly one binary.
log "Granting $ADMIN_USER passwordless access to picture-frame-update"
cat >/etc/sudoers.d/picture-frame-update <<EOF
$ADMIN_USER ALL=(root) NOPASSWD: /usr/local/bin/picture-frame-update
EOF
chmod 0440 /etc/sudoers.d/picture-frame-update
visudo -cf /etc/sudoers.d/picture-frame-update >/dev/null ||
	die "generated sudoers rule is invalid"

# --- services -----------------------------------------------------------------
log "Installing systemd units"
install -m 0644 picture-backend.service picture-frontend.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable picture-backend.service picture-frontend.service
systemctl restart picture-backend.service picture-frontend.service

# --- tailscale ----------------------------------------------------------------
if ! command -v tailscale &>/dev/null; then
	log "Adding the Tailscale apt repository"
	curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.noarmor.gpg |
		tee /usr/share/keyrings/tailscale-archive-keyring.gpg >/dev/null
	curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.tailscale-keyring.list |
		tee /etc/apt/sources.list.d/tailscale.list >/dev/null
	apt-get update -qq
	apt-get install -y tailscale
fi

if [[ -n "${TS_AUTHKEY:-}" ]]; then
	# Joining as a tagged node is what makes this a one-time setup: key expiry is
	# disabled by default for tagged devices, so the frame never needs
	# re-authenticating once it is on the tailnet.
	log "Joining the tailnet as tag:picture-frame"
	tailscale up --ssh \
		--advertise-tags=tag:picture-frame \
		--hostname="$(hostname)" \
		--authkey="$TS_AUTHKEY"
else
	log "TS_AUTHKEY not set; skipping Tailscale join"
	echo "    To join later:  sudo tailscale up --ssh --advertise-tags=tag:picture-frame"
fi

# --- next steps ---------------------------------------------------------------
echo
log "Installed."
if [[ -n "${NEEDS_CONFIG:-}" ]]; then
	echo "  1. Set 'server' in $CONFIG_DIR/config.toml, then:"
	echo "       sudo systemctl restart picture-backend"
	echo "  2. Scan the QR code on the screen and approve access in Nextcloud."
else
	echo "  Scan the QR code on the screen if the frame is not yet paired."
fi
echo "  Update later from your Mac with:  ./deploy/remote-update.sh $(hostname)"
