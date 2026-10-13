#!/bin/sh
# lazyssh installer for Linux.
#
# Install or update to the latest build:
#   curl -fsSL https://raw.githubusercontent.com/venkatasidhartha/lazyssh/develop/install.sh | sh
#   wget -qO-  https://raw.githubusercontent.com/venkatasidhartha/lazyssh/develop/install.sh | sh
#
# Options (after "sh -s --" when piping):
#   --user          install into ~/.local/bin (no sudo)
#   --bin-dir DIR   install into DIR
#   --force         reinstall even if already up to date
#   --no-deps       do not install the OpenSSH client if it is missing
#   --uninstall     remove the lazyssh binary (keeps saved servers)
#   --purge         with --uninstall: also delete saved servers and keys
#   --ca-cert FILE  trust this CA certificate (corporate HTTPS inspection)
#   --insecure      skip HTTPS certificate checks (trusted networks only)
#
# Environment:
#   LAZYSSH_REF       branch or tag to install from (default: develop)
#   LAZYSSH_BASE_URL  override download location (directory with the binaries)
#   GITHUB_TOKEN      token for downloading from a private repository
#   LAZYSSH_CA_CERT   same as --ca-cert
#   LAZYSSH_INSECURE  set to 1 for the same effect as --insecure
#
# Behind a corporate proxy that inspects HTTPS ("self-signed certificate in
# certificate chain"), fetch the script the same way you pass the option:
#   curl -fsSL --cacert company-ca.crt URL/install.sh | sh -s -- --ca-cert company-ca.crt
#   curl -fsSLk URL/install.sh | sh -s -- --insecure
set -eu

REPO="venkatasidhartha/lazyssh"
REF="${LAZYSSH_REF:-develop}"
BASE_URL="${LAZYSSH_BASE_URL:-https://raw.githubusercontent.com/$REPO/$REF/dist}"
SCRIPT_URL="https://raw.githubusercontent.com/$REPO/$REF/install.sh"
BIN_NAME="lazyssh"
PATH_MARKER="# added by lazyssh installer"

USER_INSTALL=0
BIN_DIR=""
FORCE=0
NO_DEPS=0
UNINSTALL=0
PURGE=0
CA_CERT="${LAZYSSH_CA_CERT:-}"
INSECURE="${LAZYSSH_INSECURE:-0}"

# --- output -----------------------------------------------------------------

if [ -t 1 ]; then
	C_BLUE='\033[38;5;81m' C_GREEN='\033[38;5;120m' C_YELLOW='\033[38;5;214m' C_RED='\033[38;5;203m' C_BOLD='\033[1m' C_OFF='\033[0m'
else
	C_BLUE='' C_GREEN='' C_YELLOW='' C_RED='' C_BOLD='' C_OFF=''
fi
info() { printf "${C_BLUE}›${C_OFF} %s\n" "$*"; }
ok() { printf "${C_GREEN}✓${C_OFF} %s\n" "$*"; }
warn() { printf "${C_YELLOW}!${C_OFF} %s\n" "$*" >&2; }
die() {
	printf "${C_RED}✗ %s${C_OFF}\n" "$*" >&2
	exit 1
}

usage() {
	cat <<'USAGE'
lazyssh installer

Usage: install.sh [--user] [--bin-dir DIR] [--force] [--no-deps]
                  [--ca-cert FILE] [--insecure]
       install.sh --uninstall [--purge]

  --user          install into ~/.local/bin (no sudo)
  --bin-dir DIR   install into DIR
  --force         reinstall even if already up to date
  --no-deps       do not install the OpenSSH client if it is missing
  --uninstall     remove the lazyssh binary (keeps saved servers)
  --purge         with --uninstall: also delete saved servers and keys
  --ca-cert FILE  trust this CA certificate (corporate HTTPS inspection)
  --insecure      skip HTTPS certificate checks (trusted networks only)

Environment: LAZYSSH_REF (branch/tag, default develop), LAZYSSH_BASE_URL,
GITHUB_TOKEN (for a private repository), LAZYSSH_CA_CERT, LAZYSSH_INSECURE=1
USAGE
	exit 0
}

# --- arguments --------------------------------------------------------------

while [ $# -gt 0 ]; do
	case "$1" in
	--user) USER_INSTALL=1 ;;
	--bin-dir)
		[ $# -ge 2 ] || die "--bin-dir needs a directory"
		BIN_DIR="$2"
		shift
		;;
	--bin-dir=*) BIN_DIR="${1#*=}" ;;
	--force) FORCE=1 ;;
	--no-deps) NO_DEPS=1 ;;
	--insecure) INSECURE=1 ;;
	--ca-cert)
		[ $# -ge 2 ] || die "--ca-cert needs a file"
		CA_CERT="$2"
		shift
		;;
	--ca-cert=*) CA_CERT="${1#*=}" ;;
	--uninstall) UNINSTALL=1 ;;
	--purge) PURGE=1 ;;
	-h | --help) usage ;;
	*) die "unknown option: $1 (see --help)" ;;
	esac
	shift
done

# --- helpers ----------------------------------------------------------------

have() { command -v "$1" >/dev/null 2>&1; }

if [ "$(id -u)" -eq 0 ]; then
	SUDO=""
	IS_ROOT=1
else
	IS_ROOT=0
	if have sudo; then SUDO="sudo"; elif have doas; then SUDO="doas"; else SUDO=""; fi
fi

# as_root runs a command as root (directly, or via sudo/doas).
as_root() {
	if [ "$IS_ROOT" -eq 1 ]; then
		"$@"
	elif [ -n "$SUDO" ]; then
		$SUDO "$@"
	else
		return 1
	fi
}

# download URL DEST — on failure prints the real error and, for HTTPS
# certificate problems, how to fix them; then exits.
download() {
	_url="$1"
	_dest="$2"
	_err="$TMP/download.err"
	if have curl; then
		set -- -fsSL --retry 2 --connect-timeout 20
		[ "$INSECURE" = "1" ] && set -- "$@" --insecure
		[ -n "$CA_CERT" ] && set -- "$@" --cacert "$CA_CERT"
		[ -n "${GITHUB_TOKEN:-}" ] && set -- "$@" -H "Authorization: token $GITHUB_TOKEN"
		curl "$@" -o "$_dest" "$_url" 2>"$_err" && return 0
	elif have wget; then
		set -- -q -T 30 -t 2
		[ "$INSECURE" = "1" ] && set -- "$@" --no-check-certificate
		[ -n "$CA_CERT" ] && set -- "$@" "--ca-certificate=$CA_CERT"
		[ -n "${GITHUB_TOKEN:-}" ] && set -- "$@" --header "Authorization: token $GITHUB_TOKEN"
		wget "$@" -O "$_dest" "$_url" 2>"$_err" && return 0
		_rc=$?
		# wget exit code 5 = SSL verification failure (and -q prints nothing)
		[ "$_rc" -eq 5 ] && echo "wget: SSL certificate verification failed" >>"$_err"
		[ -s "$_err" ] || echo "wget: exit code $_rc" >"$_err"
	else
		die "curl or wget is required"
	fi

	printf "${C_RED}✗ download failed:${C_OFF} %s\n" "$_url" >&2
	sed 's/^/    /' "$_err" >&2
	if grep -qiE "certificate|ssl|tls" "$_err"; then
		cat >&2 <<HELP

  Your network intercepts HTTPS (corporate proxy or firewall such as
  FortiGate or Zscaler) and this machine does not trust its certificate.

  Best fix: get your company's root CA certificate from IT, then run:
      sudo cp company-ca.crt /usr/local/share/ca-certificates/
      sudo update-ca-certificates        # Debian / Ubuntu
      # (RHEL / Fedora: copy to /etc/pki/ca-trust/source/anchors/ and run update-ca-trust)

  Or tell the installer to trust it just for this install:
      curl -fsSL --cacert company-ca.crt $SCRIPT_URL | sh -s -- --ca-cert company-ca.crt

  Or skip the check, but only on a network you trust:
      curl -fsSLk $SCRIPT_URL | sh -s -- --insecure
HELP
	fi
	exit 1
}

sha256() {
	if have sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif have shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif have openssl; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		echo ""
	fi
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo "amd64" ;;
	aarch64 | arm64) echo "arm64" ;;
	armv7* | armv8l | armhf) echo "armv7" ;;
	i386 | i486 | i586 | i686) echo "386" ;;
	*) die "unsupported CPU architecture: $(uname -m)" ;;
	esac
}

config_dir() {
	echo "${LAZYSSH_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}/lazyssh}"
}

# sudo_usable reports whether we can really become root: having sudo
# installed is not enough (the user may not be in sudoers). Asks for the
# password once if a terminal is available. Clears SUDO when unusable.
sudo_usable() {
	[ "$IS_ROOT" -eq 1 ] && return 0
	[ -n "$SUDO" ] || return 1
	if [ -n "${SUDO_CHECKED:-}" ]; then
		[ -n "$SUDO" ]
		return
	fi
	SUDO_CHECKED=1
	if $SUDO -n true 2>/dev/null; then
		return 0
	fi
	if [ "$SUDO" = "sudo" ] && (: </dev/tty) 2>/dev/null; then
		info "administrator password needed to install into /usr/local/bin"
		if sudo -v </dev/tty 2>/dev/null; then
			return 0
		fi
	fi
	SUDO=""
	return 1
}

# pick_bin_dir sets BIN_DIR and NEED_ROOT.
pick_bin_dir() {
	NEED_ROOT=0
	if [ -n "$BIN_DIR" ]; then
		:
	elif [ "$USER_INSTALL" -eq 1 ]; then
		BIN_DIR="$HOME/.local/bin"
	elif sudo_usable; then
		BIN_DIR="/usr/local/bin"
	else
		BIN_DIR="$HOME/.local/bin"
		info "no admin rights (sudo) — installing for this user only into $BIN_DIR"
	fi
	if [ -d "$BIN_DIR" ] && [ -w "$BIN_DIR" ]; then
		return
	fi
	if [ ! -d "$BIN_DIR" ] && mkdir -p "$BIN_DIR" 2>/dev/null; then
		return
	fi
	sudo_usable || die "cannot write to $BIN_DIR (try --user)"
	NEED_ROOT=1
}

install_ssh_client() {
	have ssh && return 0
	if [ "$NO_DEPS" -eq 1 ] || ! sudo_usable; then
		warn "OpenSSH client not found; lazyssh will use its built-in client"
		return 0
	fi
	info "OpenSSH client not found — installing it"
	if have apt-get; then
		as_root apt-get update -qq >/dev/null && as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openssh-client >/dev/null
	elif have dnf; then
		as_root dnf install -y -q openssh-clients >/dev/null
	elif have yum; then
		as_root yum install -y -q openssh-clients >/dev/null
	elif have apk; then
		as_root apk add --no-cache -q openssh-client >/dev/null
	elif have pacman; then
		as_root pacman -Sy --noconfirm --needed openssh >/dev/null
	elif have zypper; then
		as_root zypper --non-interactive -q install openssh-clients >/dev/null
	else
		false
	fi || warn "could not install the OpenSSH client; lazyssh will use its built-in client"
	have ssh && ok "OpenSSH client installed"
	return 0
}

# ensure_path adds BIN_DIR to PATH in the user's shell startup files.
ensure_path() {
	case ":$PATH:" in
	*":$BIN_DIR:"*) return 0 ;;
	esac
	line="export PATH=\"$BIN_DIR:\$PATH\" $PATH_MARKER"
	updated=""
	for rc in "$HOME/.profile" "$HOME/.bashrc" "$HOME/.zshrc"; do
		case "$rc" in
		*/.profile) ;;                  # always: read by login shells
		*) [ -f "$rc" ] || continue ;; # only shells the user actually has
		esac
		if [ -f "$rc" ] && grep -qF "$PATH_MARKER" "$rc"; then
			continue
		fi
		printf '\n%s\n' "$line" >>"$rc"
		updated="$updated ${rc#"$HOME"/}"
	done
	if [ -n "$updated" ]; then
		ok "added $BIN_DIR to PATH in:$updated"
	fi
	PATH_CHANGED=1
}

remove_path_lines() {
	for rc in "$HOME/.profile" "$HOME/.bashrc" "$HOME/.zshrc"; do
		[ -f "$rc" ] && grep -qF "$PATH_MARKER" "$rc" || continue
		tmp="$(mktemp)"
		grep -vF "$PATH_MARKER" "$rc" >"$tmp" && cat "$tmp" >"$rc"
		rm -f "$tmp"
	done
}

installed_version() {
	if [ -x "$1" ]; then
		"$1" --version 2>/dev/null | awk '{print $2}'
	fi
}

# --- uninstall --------------------------------------------------------------

if [ "$UNINSTALL" -eq 1 ]; then
	removed=0
	for f in ${BIN_DIR:+"$BIN_DIR/$BIN_NAME"} "/usr/local/bin/$BIN_NAME" "$HOME/.local/bin/$BIN_NAME"; do
		[ -e "$f" ] || continue
		# Only remove our build, not the upstream lazyssh.
		"$f" --help 2>/dev/null | grep -q "LAZYSSH_HOME" || {
			warn "skipping $f (not this lazyssh build)"
			continue
		}
		if rm -f "$f" 2>/dev/null || as_root rm -f "$f"; then
			ok "removed $f"
			removed=1
		fi
	done
	remove_path_lines
	[ "$removed" -eq 1 ] || warn "lazyssh binary not found"
	if [ "$PURGE" -eq 1 ]; then
		rm -rf "$(config_dir)"
		ok "deleted saved servers and keys in $(config_dir)"
	else
		info "saved servers kept in $(config_dir) (use --uninstall --purge to delete)"
	fi
	exit 0
fi

# --- install ----------------------------------------------------------------

[ "$(uname -s)" = "Linux" ] || die "this installer supports Linux only (found $(uname -s))"

ARCH="$(detect_arch)"
ASSET="$BIN_NAME-linux-$ARCH"
pick_bin_dir
TARGET="$BIN_DIR/$BIN_NAME"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

info "checking latest version ($REPO@$REF)"
if [ -n "$CA_CERT" ] && [ ! -r "$CA_CERT" ]; then
	die "CA certificate not found: $CA_CERT"
fi
if [ "$INSECURE" = "1" ]; then
	warn "HTTPS certificate checks are disabled (--insecure)"
fi
download "$BASE_URL/VERSION" "$TMP/VERSION"
LATEST="$(tr -d ' \r\n' <"$TMP/VERSION")"
CURRENT="$(installed_version "$TARGET")"

if [ "$FORCE" -eq 0 ] && [ -n "$CURRENT" ] && [ "$CURRENT" = "$LATEST" ]; then
	ok "lazyssh $CURRENT is already up to date ($TARGET)"
	install_ssh_client
	exit 0
fi

info "downloading $ASSET $LATEST"
download "$BASE_URL/$ASSET" "$TMP/$BIN_NAME"
download "$BASE_URL/SHA256SUMS" "$TMP/SHA256SUMS"

want="$(awk -v f="$ASSET" '$2 == f || $2 == "*"f {print $1}' "$TMP/SHA256SUMS")"
got="$(sha256 "$TMP/$BIN_NAME")"
if [ -z "$got" ]; then
	warn "no sha256 tool found; skipping checksum verification"
elif [ "$want" != "$got" ]; then
	die "checksum mismatch for $ASSET (expected $want, got $got)"
else
	ok "checksum verified"
fi

chmod 0755 "$TMP/$BIN_NAME"
"$TMP/$BIN_NAME" --version >/dev/null 2>&1 || die "downloaded binary does not run on this system"

if [ "$NEED_ROOT" -eq 1 ]; then
	info "installing to $TARGET (needs sudo)"
	as_root mkdir -p "$BIN_DIR"
	as_root install -m 0755 "$TMP/$BIN_NAME" "$TARGET" 2>/dev/null ||
		{ as_root cp "$TMP/$BIN_NAME" "$TARGET" && as_root chmod 0755 "$TARGET"; }
else
	mkdir -p "$BIN_DIR"
	install -m 0755 "$TMP/$BIN_NAME" "$TARGET" 2>/dev/null ||
		{ cp "$TMP/$BIN_NAME" "$TARGET" && chmod 0755 "$TARGET"; }
fi

if [ -n "$CURRENT" ]; then
	ok "updated lazyssh $CURRENT → $LATEST ($TARGET)"
else
	ok "installed lazyssh $LATEST ($TARGET)"
fi

install_ssh_client
PATH_CHANGED=0
ensure_path

# Warn if another lazyssh (e.g. the upstream project) shadows ours.
other="$(command -v "$BIN_NAME" 2>/dev/null || true)"
if [ "$PATH_CHANGED" -eq 0 ] && [ -n "$other" ] && [ "$other" != "$TARGET" ]; then
	warn "another lazyssh at $other comes first in your PATH; remove it or run $TARGET"
fi

mkdir -p "$(config_dir)" && chmod 700 "$(config_dir)" 2>/dev/null || true

printf "\n${C_BOLD}🚀 lazyssh is ready.${C_OFF}\n"
if [ "$PATH_CHANGED" -eq 1 ]; then
	printf "   Open a new terminal (or run: ${C_BOLD}export PATH=\"%s:\$PATH\"${C_OFF}), then run ${C_BOLD}lazyssh${C_OFF}\n" "$BIN_DIR"
else
	printf "   Run: ${C_BOLD}lazyssh${C_OFF}\n"
fi
printf "   Update later by running this installer again.\n"
