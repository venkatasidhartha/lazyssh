# 🚀 hopssh

A simple terminal SSH manager written in Go. Its look and key bindings follow
[lazyssh](https://github.com/adembc/lazyssh). It adds automatic password-to-key
login, which is useful when you manage many on-prem servers.

## Features

- **Fuzzy search** by name, IP address, user or tag (`/`). Type several words
  to narrow the list, e.g. `prod db`. Use `#tag` to match a tag exactly.
- **Add or edit servers** (`a` / `e`) with alias, IP, user, port, password,
  optional key file and tags.
- **One-key connect**: press `Enter`, or click a server that is already
  selected.
  - **First login** uses the password. hopssh then installs its own ed25519
    key into `~/.ssh/authorized_keys` and checks that a key-only login works.
    Later logins use the key.
  - **Servers that refuse keys** (`PubkeyAuthentication no`, restricted
    shells, read-only home): if the key cannot be installed or verified,
    hopssh removes it again. It then marks the server as password-only and
    logs in with the saved password every time. You don't need `sshpass`.
  - **A key removed from the server later**: hopssh notices, falls back to
    the password and installs the key again.
- **Password changed on the server?** If the saved password is rejected,
  hopssh asks for the new one (up to 3 tries) and saves it once a login
  succeeds. Leave the prompt empty to cancel.
- Pinning, sorting, ping, copying the ssh command, editing tags, and in-process
  port forwarding (Local, Remote and Dynamic/SOCKS5). Forwarding also works
  on password-only servers.

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

In the form: `Tab`/`↑↓` move between fields, `Ctrl+S` saves, `Esc` cancels.

## Auth modes

| Mode | Behaviour |
|---|---|
| **Auto** (default) | Uses the password on first login, then the key. Falls back to the password automatically. |
| **Password only** | Always uses the saved password. Never touches `authorized_keys`. |
| **Key only** | Uses the given key file, or ssh-agent and your default `~/.ssh` keys. |

## Data

Everything is stored in `~/.config/hopssh/`. Set `$HOPSSH_HOME` to use a
different directory.

| File | Purpose |
|---|---|
| `servers.json` | Server list. Written atomically with mode 0600. The previous version is kept as `servers.json.bak`. |
| `secret.key` | AES-256-GCM key that encrypts the saved passwords |
| `keys/id_ed25519` | The key hopssh manages and installs on servers |

Host keys are checked against `~/.ssh/known_hosts`, the same file OpenSSH
uses. New hosts are trusted on first use. A changed host key is rejected.

> The password encryption protects a copied `servers.json`. It does not protect
> against someone who can read your whole home directory, because
> `secret.key` lives there too.

## Build

```sh
make build      # → ./hopssh
make install    # → $GOPATH/bin/hopssh
make test
```

Requirements: Go 1.24+. You also need the OpenSSH client (`ssh`) for
key-based sessions. Without it, hopssh uses its built-in client.
