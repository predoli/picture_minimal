#!/usr/bin/env bash
#
# Pre-seed a frame's Nextcloud credential, skipping the QR screen.
#
#     ./deploy/provision.sh livingroom --user alice --token <app-password>
#     ./deploy/provision.sh livingroom --user alice --keychain
#
# This is the fallback path from the plan's section 7. The normal way to link a
# frame is to scan the QR code it displays: that needs no admin, no secret
# handling, and binds the frame to whoever scans it. Use this only when setting
# up a frame for someone who will not be present, or standing several up at once.
#
# An admin can mint a token for another user without their password:
#
#     occ user:auth-tokens:add --name="picture-frame-livingroom" alice
#
# Such a token has limited capabilities. That is fine for reading files over
# WebDAV, but it will fail if server-side encryption is enabled, because
# decryption needs the login password.

set -euo pipefail

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

FRAME="${1:-}"
[[ -n "$FRAME" ]] || die "usage: $0 <frame-hostname> --user <login> (--token <secret> | --keychain)"
shift

USER_NAME=""
TOKEN=""
SERVER=""
USE_KEYCHAIN=false

while [[ $# -gt 0 ]]; do
	case "$1" in
		--user)     USER_NAME="${2:?}"; shift 2 ;;
		--token)    TOKEN="${2:?}"; shift 2 ;;
		--server)   SERVER="${2:?}"; shift 2 ;;
		--keychain) USE_KEYCHAIN=true; shift ;;
		*)          die "unknown argument: $1" ;;
	esac
done

[[ -n "$USER_NAME" ]] || die "--user is required"

# The Mac's Keychain is the source of truth for rebuilding a frame after an SD
# card dies: encrypted at rest, already backed up, and no extra tooling. Store a
# secret with:
#   security add-generic-password -s picture-frame -a livingroom -w
if [[ "$USE_KEYCHAIN" == true ]]; then
	TOKEN="$(security find-generic-password -s picture-frame -a "$FRAME" -w 2>/dev/null)" ||
		die "no keychain entry for '$FRAME' under service 'picture-frame'"
fi
[[ -n "$TOKEN" ]] || die "--token or --keychain is required"

# Connect as root rather than via sudo: the tailnet policy already grants root
# on tag:picture-frame, and install.sh's sudoers rule is deliberately scoped to
# picture-frame-update alone, so `sudo -n install` would be refused.
REMOTE="root@$FRAME"

if [[ -z "$SERVER" ]]; then
	log "Reading the server URL from $FRAME"
	SERVER="$(tailscale ssh "$REMOTE" -- \
		"grep -E '^[[:space:]]*server' /etc/picture-frame/config.toml | cut -d'\"' -f2")" ||
		die "could not read server from the frame's config; pass --server"
	SERVER="$(printf '%s' "$SERVER" | tr -d '\r\n')"
fi
[[ -n "$SERVER" ]] || die "server is empty; set it in the frame's config.toml or pass --server"

log "Writing credentials for $USER_NAME@$SERVER to $FRAME"

# Build the JSON locally so a quoting mistake fails here rather than writing a
# malformed credential the frame would silently treat as "never paired".
if command -v jq &>/dev/null; then
	PAYLOAD="$(jq -nc --arg s "$SERVER" --arg l "$USER_NAME" --arg p "$TOKEN" \
		'{server:$s, loginName:$l, appPassword:$p}')"
else
	case "$TOKEN$USER_NAME$SERVER" in
		*[\"\\]*) die "value contains a quote or backslash; install jq to handle it safely" ;;
	esac
	PAYLOAD="$(printf '{"server":"%s","loginName":"%s","appPassword":"%s"}' \
		"$SERVER" "$USER_NAME" "$TOKEN")"
fi

# Piped over stdin so the token never appears in the remote command line, where
# it would show up in the process table and in shell history.
printf '%s\n' "$PAYLOAD" |
	tailscale ssh "$REMOTE" -- install -m 0600 -o picture -g picture \
		/dev/stdin /var/lib/picture-frame/auth.json

log "Restarting the backend"
tailscale ssh "$REMOTE" -- systemctl restart picture-backend

log "Done. The frame should leave the pairing screen within a few seconds."
