#!/bin/sh
# install.sh: install asterism-agent as a hardened systemd service.
#
# Download this script, read it, then run it. It never pipes remote code
# into a shell: the only things it downloads are the release tarball, the
# systemd unit and checksums.txt, and it refuses to install anything whose
# SHA-256 does not match checksums.txt.
#
#   curl -fsSLo install.sh https://github.com/prabhavalabs/asterism/releases/latest/download/install.sh
#   sh install.sh --hub https://asterism.example.com --token-file ./agent.token [--host prod-1]
#   sh install.sh --dry-run ...   # print every step, change nothing
#
# What it does:
#   1. Detects the architecture (amd64 or arm64).
#   2. Downloads asterism-agent-linux-<arch>.tar.gz, asterism-agent.service
#      and checksums.txt for the release, and verifies the SHA-256 of each
#      (plus `cosign verify-blob` of checksums.txt when cosign is installed
#      and the release carries a signature).
#   3. Creates the system user and group asterism-agent.
#   4. Installs /usr/local/bin/asterism-agent, /etc/asterism/agent.yaml (a
#      skeleton, only if absent) and /etc/asterism/agent.token (0640
#      root:asterism-agent).
#   5. Installs /etc/systemd/system/asterism-agent.service, runs
#      `asterism-agent validate`, then `systemctl enable --now`.
#
# The token is read from a file or from the terminal, never from the
# command line, so it stays out of shell history and `ps`.
#
# Uninstall:
#   sh install.sh --uninstall           # stop and remove the unit and binary
#   sh install.sh --uninstall --purge   # also remove /etc/asterism and the user
# By hand, the same steps are:
#   systemctl disable --now asterism-agent
#   rm -f /etc/systemd/system/asterism-agent.service /usr/local/bin/asterism-agent
#   systemctl daemon-reload
#   rm -rf /etc/asterism && userdel asterism-agent   # optional (purge)
# Then revoke the agent on the hub by deleting its [[agents]] entry.

set -eu

REPO="prabhavalabs/asterism"
VERSION="latest"
BASE_URL=""
HUB_URL=""
TOKEN_FILE=""
HOST_NAME=""
LOCAL_BINARY=""
LOCAL_UNIT=""
DOCKER_GROUP="auto"
DRY_RUN=0
UNINSTALL=0
PURGE=0
START=1

PREFIX="${ASTERISM_INSTALL_PREFIX:-}"
BIN="$PREFIX/usr/local/bin/asterism-agent"
CONF_DIR="$PREFIX/etc/asterism"
UNIT="$PREFIX/etc/systemd/system/asterism-agent.service"
USER_NAME="asterism-agent"

usage() {
	cat <<'USAGE'
Usage: sh install.sh [options]

Install:
  --hub URL           hub URL written to a new agent.yaml (https://...)
  --token-file PATH   file holding the asta_ token (read from the terminal if omitted)
  --host NAME         host name the token is bound to (default: hostname)
  --version vX.Y.Z    release to install (default: latest)
  --base-url URL      download from this mirror instead of GitHub releases
                      (must serve the same asset names and checksums.txt)
  --binary PATH       install this local binary instead of downloading
  --unit PATH         install this local unit file instead of downloading
  --docker-group      add the docker group (raw socket access; default: auto)
  --no-docker-group   do not add the docker group (use a socket proxy)
  --no-start          install and validate, but do not enable or start
  --dry-run           print every step and change nothing

Uninstall:
  --uninstall         stop and remove the service and binary
  --purge             with --uninstall, also remove /etc/asterism and the user

  -h, --help          show this help
USAGE
}

say() { printf '%s\n' "asterism-agent: $*" >&2; }
die() {
	say "error: $*"
	exit 1
}

# run prints a command in dry-run mode and executes it otherwise.
run() {
	if [ "$DRY_RUN" = 1 ]; then
		printf '+ %s\n' "$*" >&2
	else
		"$@"
	fi
}

need_arg() { [ $# -ge 2 ] && [ -n "$2" ] || die "$1 needs a value"; }

while [ $# -gt 0 ]; do
	case "$1" in
	--hub) need_arg "$@"; HUB_URL=$2; shift 2 ;;
	--token-file) need_arg "$@"; TOKEN_FILE=$2; shift 2 ;;
	--host) need_arg "$@"; HOST_NAME=$2; shift 2 ;;
	--version) need_arg "$@"; VERSION=$2; shift 2 ;;
	--base-url) need_arg "$@"; BASE_URL=$2; shift 2 ;;
	--binary) need_arg "$@"; LOCAL_BINARY=$2; shift 2 ;;
	--unit) need_arg "$@"; LOCAL_UNIT=$2; shift 2 ;;
	--docker-group) DOCKER_GROUP=yes; shift ;;
	--no-docker-group) DOCKER_GROUP=no; shift ;;
	--no-start) START=0; shift ;;
	--dry-run) DRY_RUN=1; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	--purge) PURGE=1; shift ;;
	--token | --token=*) die "pass the token with --token-file or on the terminal, never on the command line" ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

if [ "$DRY_RUN" = 0 ] && [ -z "$PREFIX" ] && [ "$(id -u)" != 0 ]; then
	die "run as root (or with --dry-run to preview)"
fi

have() { command -v "$1" >/dev/null 2>&1; }

group_exists() {
	if have getent; then getent group "$1" >/dev/null 2>&1; else grep -q "^$1:" /etc/group 2>/dev/null; fi
}

# ---------------------------------------------------------------------------
# Uninstall
# ---------------------------------------------------------------------------
if [ "$UNINSTALL" = 1 ]; then
	if have systemctl && [ -z "$PREFIX" ]; then
		run systemctl disable --now asterism-agent || true
	fi
	run rm -f "$UNIT" "$BIN"
	if have systemctl && [ -z "$PREFIX" ]; then
		run systemctl daemon-reload || true
	fi
	if [ "$PURGE" = 1 ]; then
		run rm -rf "$CONF_DIR"
		if id "$USER_NAME" >/dev/null 2>&1 && [ -z "$PREFIX" ]; then
			if have userdel; then
				run userdel "$USER_NAME" || true
			elif have deluser; then
				run deluser "$USER_NAME" || true
			fi
		fi
		say "removed; also delete this agent's [[agents]] entry on the hub to revoke its token"
	else
		say "removed the service and binary; kept $CONF_DIR (use --purge to remove it)"
	fi
	exit 0
fi

# ---------------------------------------------------------------------------
# Platform and downloads
# ---------------------------------------------------------------------------
[ "$(uname -s)" = Linux ] || [ "$DRY_RUN" = 1 ] || [ -n "$PREFIX" ] || die "asterism-agent runs on Linux only"
case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported architecture $(uname -m) (supported: amd64, arm64)" ;;
esac

if [ -z "$BASE_URL" ]; then
	if [ "$VERSION" = latest ]; then
		BASE_URL="https://github.com/$REPO/releases/latest/download"
	else
		BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
	fi
fi
TARBALL="asterism-agent-linux-$ARCH.tar.gz"

sha256_of() {
	if have sha256sum; then
		sha256sum "$1" | cut -d' ' -f1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "need sha256sum or shasum to verify downloads"
	fi
}

fetch() { # fetch URL DEST
	if have curl; then
		case "$1" in
		https://*) curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$2" "$1" ;;
		*) curl -fsSL --retry 3 -o "$2" "$1" ;; # an explicit --base-url mirror
		esac
	elif have wget; then
		wget -q -O "$2" "$1"
	else
		die "need curl or wget"
	fi
}

# verify FILE NAME: NAME's line in checksums.txt must match FILE's digest.
verify() {
	want=$(awk -v n="$2" '$2 == n || $2 == "*" n { print $1 }' "$WORK/checksums.txt")
	[ -n "$want" ] || die "$2 is not listed in checksums.txt"
	got=$(sha256_of "$1")
	[ "$want" = "$got" ] || die "checksum mismatch for $2 (want $want, got $got); refusing to install"
	say "verified $2 ($got)"
}

WORK=""
cleanup() { if [ -n "$WORK" ]; then rm -rf "$WORK"; fi; }
trap cleanup EXIT INT TERM

SRC_BIN=$LOCAL_BINARY
SRC_UNIT=$LOCAL_UNIT
if [ -z "$SRC_BIN" ] || [ -z "$SRC_UNIT" ]; then
	if [ "$DRY_RUN" = 1 ]; then
		say "would download $BASE_URL/{checksums.txt,$TARBALL,asterism-agent.service} and verify SHA-256"
		[ -n "$SRC_BIN" ] || SRC_BIN="<downloaded>/asterism-agent"
		[ -n "$SRC_UNIT" ] || SRC_UNIT="<downloaded>/asterism-agent.service"
	else
		WORK=$(mktemp -d)
		case "$BASE_URL" in
		https://*) ;;
		*) say "warning: $BASE_URL is not https; integrity rests on checksums.txt from the same mirror" ;;
		esac
		say "downloading from $BASE_URL"
		fetch "$BASE_URL/checksums.txt" "$WORK/checksums.txt"
		if have cosign; then
			if fetch "$BASE_URL/checksums.txt.sig" "$WORK/checksums.txt.sig" 2>/dev/null &&
				fetch "$BASE_URL/checksums.txt.pem" "$WORK/checksums.txt.pem" 2>/dev/null; then
				cosign verify-blob --certificate "$WORK/checksums.txt.pem" --signature "$WORK/checksums.txt.sig" \
					--certificate-identity-regexp "^https://github.com/$REPO/" \
					--certificate-oidc-issuer https://token.actions.githubusercontent.com \
					"$WORK/checksums.txt" >/dev/null || die "cosign could not verify checksums.txt; refusing to install"
				say "verified checksums.txt signature with cosign"
			else
				say "release has no cosign signature; relying on SHA-256 checksums"
			fi
		fi
		if [ -z "$SRC_BIN" ]; then
			fetch "$BASE_URL/$TARBALL" "$WORK/$TARBALL"
			verify "$WORK/$TARBALL" "$TARBALL"
			tar -xzf "$WORK/$TARBALL" -C "$WORK" asterism-agent
			SRC_BIN="$WORK/asterism-agent"
		fi
		if [ -z "$SRC_UNIT" ]; then
			fetch "$BASE_URL/asterism-agent.service" "$WORK/asterism-agent.service"
			verify "$WORK/asterism-agent.service" asterism-agent.service
			SRC_UNIT="$WORK/asterism-agent.service"
		fi
	fi
fi
if [ "$DRY_RUN" = 0 ]; then
	[ -f "$SRC_BIN" ] || die "binary $SRC_BIN not found"
	[ -f "$SRC_UNIT" ] || die "unit $SRC_UNIT not found"
fi

# ---------------------------------------------------------------------------
# User, binary, config, token, unit
# ---------------------------------------------------------------------------
if [ -z "$PREFIX" ] && ! id "$USER_NAME" >/dev/null 2>&1; then
	NOLOGIN=/usr/sbin/nologin
	[ -x "$NOLOGIN" ] || NOLOGIN=/sbin/nologin
	if have useradd || [ "$DRY_RUN" = 1 ]; then
		run useradd --system --user-group --no-create-home --home-dir /nonexistent --shell "$NOLOGIN" "$USER_NAME"
	elif have adduser && have addgroup; then # busybox (Alpine)
		group_exists "$USER_NAME" || run addgroup -S "$USER_NAME"
		run adduser -S -D -H -h /nonexistent -s "$NOLOGIN" -G "$USER_NAME" "$USER_NAME"
	else
		die "need useradd or adduser to create the $USER_NAME user"
	fi
fi
OWNER="root:$USER_NAME"
[ -z "$PREFIX" ] || OWNER="$(id -u):$(id -g)"

run install -d -m 0755 "$(dirname "$BIN")" "$(dirname "$UNIT")"
run install -d -m 0750 "$CONF_DIR"
run chown "$OWNER" "$CONF_DIR"
run install -m 0755 "$SRC_BIN" "$BIN"

if [ -z "$HOST_NAME" ]; then
	HOST_NAME=$(hostname 2>/dev/null | tr '[:upper:]' '[:lower:]')
fi
CONF="$CONF_DIR/agent.yaml"
if [ -e "$CONF" ]; then
	say "keeping existing $CONF"
else
	[ -n "$HUB_URL" ] || die "--hub is required when $CONF does not exist"
	case "$HUB_URL" in
	https://* | http://127.0.0.1* | http://localhost* | "http://[::1]"*) ;;
	*) die "--hub must be https:// (plain http is accepted only for a loopback hub)" ;;
	esac
	if [ "$DRY_RUN" = 1 ]; then
		say "would write $CONF (hub $HUB_URL, host $HOST_NAME)"
	else
		umask 027
		cat >"$CONF" <<YAML
# asterism-agent configuration. Every key is optional except hub.url and
# a token; see the examples shipped with the release, and run
#   asterism-agent discover
# for suggested sources and projects on this host.
version: 1
hub:
  url: $HUB_URL
  token_file: /etc/asterism/agent.token
host:
  name: $HOST_NAME
# Read-only by default. See the actions section of the documentation
# before enabling container actions.
actions:
  enabled: false
YAML
		chown "$OWNER" "$CONF"
		chmod 0640 "$CONF"
	fi
fi

TOKEN_DST="$CONF_DIR/agent.token"
if [ -n "$TOKEN_FILE" ]; then
	[ "$DRY_RUN" = 1 ] || [ -r "$TOKEN_FILE" ] || die "cannot read $TOKEN_FILE"
	run install -m 0640 -o "${OWNER%%:*}" -g "${OWNER##*:}" "$TOKEN_FILE" "$TOKEN_DST"
elif [ ! -e "$TOKEN_DST" ]; then
	if [ "$DRY_RUN" = 1 ]; then
		say "would read the token from the terminal into $TOKEN_DST"
	elif [ -t 0 ]; then
		printf 'Paste the asta_ agent token (input hidden): ' >&2
		stty -echo 2>/dev/null || true
		IFS= read -r TOKEN || true
		stty echo 2>/dev/null || true
		printf '\n' >&2
		case "$TOKEN" in asta_*) ;; *) die "that does not look like an agent token (asta_...)" ;; esac
		umask 027
		printf '%s\n' "$TOKEN" >"$TOKEN_DST"
		TOKEN=""
		chown "$OWNER" "$TOKEN_DST"
		chmod 0640 "$TOKEN_DST"
	else
		die "no token: pass --token-file or run interactively"
	fi
fi

# SupplementaryGroups may only name groups that exist on this host.
GROUPS_LINE=""
case "$DOCKER_GROUP" in
yes) group_exists docker || die "--docker-group: there is no docker group"; GROUPS_LINE="docker" ;;
auto) if group_exists docker; then GROUPS_LINE="docker"; fi ;;
esac
if group_exists systemd-journal; then GROUPS_LINE="$GROUPS_LINE systemd-journal"; fi
GROUPS_LINE=$(echo "$GROUPS_LINE" | sed 's/^ *//')
if [ "$DRY_RUN" = 1 ]; then
	say "would install $UNIT with SupplementaryGroups=$GROUPS_LINE"
else
	if [ -n "$GROUPS_LINE" ]; then
		sed "s/^SupplementaryGroups=.*/SupplementaryGroups=$GROUPS_LINE/" "$SRC_UNIT" >"$UNIT.tmp"
	else
		sed '/^SupplementaryGroups=/d' "$SRC_UNIT" >"$UNIT.tmp"
	fi
	chmod 0644 "$UNIT.tmp"
	mv "$UNIT.tmp" "$UNIT"
fi

# ---------------------------------------------------------------------------
# Validate and start
# ---------------------------------------------------------------------------
if [ -n "$PREFIX" ]; then
	# A staged install (tests, image builds): the token lives under PREFIX.
	ASTERISM_AGENT_TOKEN_FILE="$TOKEN_DST"
	export ASTERISM_AGENT_TOKEN_FILE
fi
if [ "$DRY_RUN" = 1 ]; then
	run "$BIN" validate --config "$CONF"
elif [ -z "$PREFIX" ] && have runuser; then
	runuser -u "$USER_NAME" -- "$BIN" validate --config "$CONF" || die "asterism-agent validate failed; fix $CONF and re-run"
else
	"$BIN" validate --config "$CONF" || die "asterism-agent validate failed; fix $CONF and re-run"
fi

if [ -n "$PREFIX" ] || ! have systemctl; then
	say "installed under ${PREFIX:-/}; start it with your service manager"
	exit 0
fi
run systemctl daemon-reload
if [ "$START" = 1 ]; then
	run systemctl enable --now asterism-agent
	say "started; check with: systemctl status asterism-agent && journalctl -u asterism-agent -f"
else
	say "installed; start with: systemctl enable --now asterism-agent"
fi
