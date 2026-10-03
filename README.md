# beacon
Self-hosted focus tool on top of Radicale/CalDAV: shows exactly one task, catches distractions, and keeps your todo list in sight. Go server, CLI and PWA.

## Configure

beacon reads its settings from environment variables:

```sh
export BEACON_CALDAV_URL=https://dav.example.org/
export BEACON_CALDAV_USER=pascal
export BEACON_CALDAV_PASSWORD_CMD="pass radicale"   # run without a shell
export BEACON_LISTS=Todo                            # comma-separated, default Todo
```

## Run

```sh
go build -o t ./cmd/t
./t list    # open tasks, best first
./t focus   # only the next task
```

Order: overdue first, then by due date, then priority, then oldest first.
Recurring tasks are left out; they are handled on the phone.
