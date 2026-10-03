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
export BEACON_TOKEN=$(openssl rand -hex 32)   # required, at least 16 characters
export BEACON_LISTEN=127.0.0.1:8080           # default
export BEACON_DB=/var/db/beacon/beacon.db     # focus state, default ./beacon.db
```

`t` talks to the server when these are set, and to Radicale directly otherwise:

```sh
export BEACON_SERVER_URL=https://beacon.example.org
export BEACON_TOKEN=...                       # the same token as the server
```

## Run

```sh
nix build .#t .#beacon    # or: go build ./cmd/t ./cmd/beacon
beacon                    # start the server
```

```sh
t                         # show the current task
t buy coffee              # add a task to the default list
t list                    # open tasks, current first
t focus                   # only the current task
t done                    # complete the current task, then show the next one
t skip                    # skip the current task for now (server only)
t add list the receipts   # add a task that starts with a command word
```

Order: overdue first, then by due date, then priority, then oldest first.
With the server, the current task stays current until it is done, skipped,
or completed or deleted elsewhere. Skipped tasks come back once nothing else
is left. Recurring tasks are left out and never completed; they are handled
on the phone.

`t done` never overwrites a task that was changed elsewhere (for example on
the phone). With the server it tells you what the task looks like now; run
`t done` again to complete it as it is.

## API

Every endpoint except `/healthz` needs `Authorization: Bearer <BEACON_TOKEN>`.

| Endpoint | Does |
|---|---|
| `GET /healthz` | `ok`, no token needed |
| `GET /current` | the current task, or `null` |
| `GET /tasks` | open tasks in focus order |
| `POST /tasks` | add `{"summary": "buy coffee"}` to the default list |
| `POST /current/done` | complete the current task; `409` if it changed since it became current |
| `POST /current/skip` | skip the current task and pick the next |

beacon keeps only its focus state (current task, skip order) in SQLite,
keyed by task UID. Tasks themselves stay in Radicale.
