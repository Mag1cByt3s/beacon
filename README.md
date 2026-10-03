# beacon
Self-hosted focus tool on top of Radicale/CalDAV: shows exactly one task, catches distractions, and keeps your todo list in sight. Go server and CLI.

## Configure

beacon reads its settings from environment variables. Talking to Radicale
(the server, or `t` without a server):

```sh
export BEACON_CALDAV_URL=https://dav.example.org/
export BEACON_CALDAV_USER=pascal
export BEACON_CALDAV_PASSWORD_CMD="pass radicale"   # run without a shell
export BEACON_LISTS=Todo                            # comma-separated, default Todo
export BEACON_DEFAULT_LIST=Todo                     # where new tasks go, default Todo
```

The server additionally uses:

```sh
export BEACON_TOKEN_FILE=/usr/local/etc/beacon/token   # required (or BEACON_TOKEN)
export BEACON_LISTEN=127.0.0.1:8080                    # default
export BEACON_DB=/var/db/beacon/beacon.db              # focus state, default ./beacon.db
```

The token is at least 16 characters, for example from `openssl rand -hex 32`.
`BEACON_TOKEN_FILE` names a file holding it and takes precedence over
`BEACON_TOKEN`. The server refuses to start if other users can read the file
(use `chmod 600`).

`t` talks to the server when these are set, and to Radicale directly otherwise:

```sh
export BEACON_SERVER_URL=https://beacon.example.org
export BEACON_TOKEN_FILE=~/.config/beacon/token        # the same token as the server
```

## Run

```sh
nix build .#t .#beacon    # or: go build ./cmd/t ./cmd/beacon
beacon                    # start the server
```

To run the server in a FreeBSD jail, see [docs/deploy.md](docs/deploy.md).
`nix build .#beacon-freebsd-amd64` builds a static FreeBSD binary.

```sh
t                         # show the current task
t buy coffee              # add a task to the default list
t list                    # open tasks, current first
t focus                   # only the current task
t done                    # complete the current task, then show the next one
t skip                    # skip the current task for now (server only)
t add list the receipts   # add a task that starts with a command word
t prompt                  # the current task or nothing, within 300 ms
```

Order: overdue first, then by due date, then priority, then oldest first.
With the server, the current task stays current until it is done, skipped,
or completed or deleted elsewhere. Skipped tasks come back once nothing else
is left. Recurring tasks are left out and never completed; they are handled
on the phone.

`t done` never overwrites a task that was changed elsewhere (for example on
the phone). With the server it tells you what the task looks like now; run
`t done` again to complete it as it is.

### Offline captures

If the server or Radicale cannot be reached, `t buy coffee` saves the task in
`$XDG_STATE_HOME/beacon/queue.jsonl` (default `~/.local/state/beacon/`) and
prints `Saved offline, will sync later.` Every later `t` command first sends
saved tasks, quietly and for at most 2 seconds. Each saved task keeps the UID
it was given when first captured, so sending it twice never creates a
duplicate. Only captures are saved; `done` and `skip` never are.

### Shell hook

Show the current task whenever a new terminal opens (once per shell, not on
every prompt). Add one line to your shell's rc file:

```sh
eval "$(t hook zsh)"     # ~/.zshrc
eval "$(t hook bash)"    # ~/.bashrc
```

The hook runs `t prompt`, which gives up silently after 300 ms. With
`BEACON_SERVER_URL` set it only asks the server; without, it has to run the
password command and talk to Radicale, which is often too slow for 300 ms.

## API

Every endpoint except `/healthz` needs `Authorization: Bearer <BEACON_TOKEN>`.
`503` means Radicale could not be reached; trying again later may work.

| Endpoint | Does |
|---|---|
| `GET /healthz` | `ok`, no token needed |
| `GET /current` | the current task, or `null` |
| `GET /tasks` | open tasks in focus order |
| `POST /tasks` | add `{"summary": "buy coffee"}` to the default list; an optional `"uid"` makes retries safe (`409` if it exists) |
| `POST /current/done` | complete the current task; `409` if it changed since it became current |
| `POST /current/skip` | skip the current task and pick the next |

beacon keeps only its focus state (current task, skip order) in SQLite,
keyed by task UID. Tasks themselves stay in Radicale.
