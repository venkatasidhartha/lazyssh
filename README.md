# 🚀 lazyssh

By **Venkata Sidhartha K**

A simple terminal SSH manager written in Go. Its look and key bindings follow
the original [lazyssh by Adembc](https://github.com/adembc/lazyssh). It adds automatic password-to-key
login, which is useful when you manage many on-prem servers.

## Install (any Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/venkatasidhartha/lazyssh/develop/install.sh | sh
```

If the system has no `curl`, use `wget` instead:

```sh
wget -qO- https://raw.githubusercontent.com/venkatasidhartha/lazyssh/develop/install.sh | sh
```

Then run `lazyssh`.

### Updating

Run the **same command** again. The installer finds your existing lazyssh
and replaces only the program file, in the same place it was installed.

- **Your saved servers, passwords and keys are never touched.** They live
  in `~/.config/lazyssh`. The installer also copies `servers.json` to
  `servers.json.before-<version>` before it updates.
- The new binary is swapped in with one atomic move, so updating works
  even while lazyssh is open in another terminal.
- If the new version does not start, the previous one is put back.
- If you already have the latest version, the installer says so and exits.

The installer:

- detects your CPU (amd64, arm64, armv7 or 386) and downloads the matching
  static binary from `dist/`
- checks the binary against `SHA256SUMS`
- installs it into `/usr/local/bin`. It does this as root, or with `sudo`
  when your user is allowed to use it. Otherwise it installs into
  `~/.local/bin` and adds that folder to your `PATH`.
- installs the OpenSSH client with the system package manager if it is
  missing (apt, dnf, yum, apk, pacman or zypper)

It works with any POSIX `sh`, including BusyBox on Alpine. Tested on Ubuntu
24.04, Debian 12, Fedora 40 and Alpine 3.20.

```sh
curl -fsSL .../install.sh | sh -s -- --user                # no sudo, ~/.local/bin
curl -fsSL .../install.sh | sh -s -- --uninstall           # keeps saved servers
curl -fsSL .../install.sh | sh -s -- --uninstall --purge   # deletes them too
```

Run `sh install.sh --help` to see every option.

### Company network: "self-signed certificate in certificate chain"

Some company firewalls and proxies inspect HTTPS (for example FortiGate or
Zscaler). The machine then does not trust the certificates it sees, so curl
fails and `wget -q` fails without printing anything. To fix it, pick one:

```sh
# 1. Best: trust your company's root CA system-wide (get the .crt from IT)
sudo cp company-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates
curl -fsSL https://raw.githubusercontent.com/venkatasidhartha/lazyssh/develop/install.sh | sh

# 2. Trust the CA only for this install
curl -fsSL --cacert company-ca.crt https://raw.githubusercontent.com/venkatasidhartha/lazyssh/develop/install.sh | sh -s -- --ca-cert company-ca.crt

# 3. Skip the certificate check (only on a network you trust)
curl -fsSLk https://raw.githubusercontent.com/venkatasidhartha/lazyssh/develop/install.sh | sh -s -- --insecure
```

On RHEL or Fedora, use `/etc/pki/ca-trust/source/anchors/` and `sudo update-ca-trust`
instead of the Debian/Ubuntu paths.

## Features

- **Fuzzy search** by name, IP address, user or tag (`/`). Type several words
  to narrow the list, e.g. `prod db`. Use `#tag` to match a tag exactly.
- **Table view** with NAME, HOST / IP, USER, AUTH, LAST SSH and TAGS
  columns. Pinned servers are marked 📌, and servers with active port
  forwards are marked F.
- **Add or edit servers** (`a` / `e`) with alias, IP, user, port, password,
  optional key file and tags. Press Ctrl+R in the form to show the
  password.
- **Tag picker**: type in the Tags field to search existing tags, or press
  ↓ to list them all. Enter picks the highlighted tag; Tab moves to the next
  field without picking anything. The SSH Key field works the same way,
  suggesting keys from `~/.ssh`. To add a new tag,
  type it and press `,`. Separate several tags with commas.
- **Show and copy passwords**: press `v` (or click **show** in Details) to
  show the selected server's password. It hides again after 30 seconds.
  Press `y` to copy it to the clipboard so you can share it with your team.
- **Live clock** in the header, with seconds and time zone.
- **Safe with several windows**: every change re-reads `servers.json` under
  a file lock, so two open lazyssh windows never overwrite each other. `r`
  and a 30-second auto-refresh pick up changes from other windows. If the
  file is damaged, lazyssh refuses to write to it instead of replacing it.
- **One-key connect**: press `Enter`, or click a server that is already
  selected.
  - **First login** uses the password. lazyssh then installs its own ed25519
    key into `~/.ssh/authorized_keys` and checks that a key-only login works.
    Later logins use the key.
  - **Servers that refuse keys** (`PubkeyAuthentication no`, restricted
    shells, read-only home): if the key cannot be installed or verified,
    lazyssh removes it again. It then marks the server as password-only and
    logs in with the saved password every time. You don't need `sshpass`.
  - **A key removed from the server later**: lazyssh notices, falls back to
    the password and installs the key again.
- **Password changed on the server?** If the saved password is rejected,
  lazyssh asks for the new one (up to 3 tries) and saves it once a login
  succeeds. Leave the prompt empty to cancel.
- Pinning, sorting, ping, copying the ssh command, editing tags, and in-process
  port forwarding (Local, Remote and Dynamic/SOCKS5). Forwarding also works
  on password-only servers.

## Workspace: split panes, popups and sessions that stay alive

Press `l` (or click **▶ Terminal**) for a local terminal, `w` to open the
selected server in the workspace, or `o` to add it next to the open ones.
With no servers saved, lazyssh starts straight in a local terminal.
Everything works with the mouse:

- **Buttons on every pane's title bar**: ✎ rename · ⇆ ⇅ split right / down ·
  ❐ pop out into a popup · ⤢ full screen (or double-click the title) · ✕ close.
  Drag the line between two panes to resize them; **⊟ Even sizes** resets them.
- **Popup windows** (❐ Popup): drag the title to move, drag ◢ to resize,
  ▁ minimize to the bottom bar, □ maximize, ⊞ dock back into the panes.
- **Like a normal terminal**: the wheel scrolls back (10,000 lines), drag
  selects and copies, double-click selects a word, right-click pastes.
  htop, vim, nvim, mc, less and other full-screen programs work, mouse
  included (Shift+drag selects text inside them).
- **★ Commands**: save commands you use often; one click types them into the
  session (▶ Type) or runs them (↵ Run). Add, edit and remove them there.
- **▣ Groups**: save the open sessions (servers, local shells, names, layout)
  as a group such as "prod monitoring" and open all of them with one click.
- **Sessions stay alive**: quitting lazyssh (⏻ Quit) keeps them running in
  the background — servers stay connected, programs keep running. The next
  time lazyssh opens, the same panes, popups, names and output are back.
  Choose "End sessions and quit" to close them instead.

```sh
lazyssh sessions          # list the sessions running in the background
lazyssh sessions stop     # end them all
LAZYSSH_NO_BACKGROUND=1 lazyssh   # sessions end when lazyssh closes
```

Keys are optional: Ctrl+C always goes to the program, Ctrl+A then a letter
gives keyboard shortcuts for everything (`?` shows them all).

## Keys

| Key | Action | Key | Action |
|---|---|---|---|
| `↑↓` / `j k` | Navigate | `/` | Search |
| `Enter` | SSH connect | `c` | Copy ssh command |
| `a` | Add server | `e` | Edit server |
| `d` | Delete | `t` | Edit tags |
| `p` | Pin / unpin | `s` / `S` | Sort field / reverse |
| `g` | Ping (TCP) | `r` | Refresh |
| `f` | Port forward | `x` | Stop forwards |
| `K` | Redo key setup | `q` | Quit |
| `v` | Show / hide password | `y` | Copy password |
| `l` | Local terminal | `w` / `o` | Workspace / open server in a pane |

In the form: `Tab`/`↑↓` move between fields, `Ctrl+S` saves, `Esc` cancels.

## Auth modes

| Mode | Behaviour |
|---|---|
| **Auto** (default) | Uses the password on first login, then the key. Falls back to the password automatically. |
| **Password only** | Always uses the saved password. Never touches `authorized_keys`. |
| **Key only** | Uses the given key file, or ssh-agent and your default `~/.ssh` keys. |

## Data

Everything is stored in `~/.config/lazyssh/`. Set `$LAZYSSH_HOME` to use a
different directory.

| File | Purpose |
|---|---|
| `servers.json` | Server list. Written atomically with mode 0600. The previous version is kept as `servers.json.bak`. |
| `secret.key` | AES-256-GCM key that encrypts the saved passwords |
| `keys/id_ed25519` | The key lazyssh manages and installs on servers |
| `workspace.json` | The workspace layout, to bring it back on the next start |
| `commands.json`, `groups.json` | Saved commands and session groups |
| `sessions.sock` | Socket of the background session service (only while it runs) |

Host keys are checked against `~/.ssh/known_hosts`, the same file OpenSSH
uses. New hosts are trusted on first use. A changed host key is rejected.

> The password encryption protects a copied `servers.json`. It does not protect
> against someone who can read your whole home directory, because
> `secret.key` lives there too.

## Build

```sh
make build      # → ./lazyssh
make install    # → $GOPATH/bin/lazyssh
make test
```

### Publishing a new version

```sh
VERSION=v1.0.1 scripts/build-release.sh   # rebuilds dist/ with SHA256SUMS and VERSION
git add dist && git commit -m "release v1.0.1" && git push
```

`install.sh` reads `dist/VERSION` from the `develop` branch. Every machine
picks up the new version the next time someone runs the installer.

Requirements: Go 1.24+. You also need the OpenSSH client (`ssh`) for
key-based sessions. Without it, lazyssh uses its built-in client.
