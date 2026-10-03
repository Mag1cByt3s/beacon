# beacon
Self-hosted focus tool on top of Radicale/CalDAV: shows exactly one task, catches distractions, and keeps your todo list in sight. Go server, CLI and PWA.

## Configure

beacon reads its settings from environment variables:

```sh
export BEACON_CALDAV_URL=https://dav.example.org/
export BEACON_CALDAV_USER=pascal
export BEACON_CALDAV_PASSWORD_CMD="pass radicale"   # run without a shell
export BEACON_LISTS=Todo                            # comma-separated, default Todo
export BEACON_DEFAULT_LIST=Todo                     # where new tasks go, default Todo
```

## Run

```sh
go build -o t ./cmd/t     # or: nix build
t                         # show the next task
t buy coffee              # add a task to the default list
t list                    # open tasks, best first
t focus                   # only the next task
t done                    # complete the next task, then show the one after it
t add list the receipts   # add a task that starts with a command word
```

Order: overdue first, then by due date, then priority, then oldest first.
Recurring tasks are left out and never completed; they are handled on the phone.

`t done` will not overwrite a task that was changed elsewhere (for example on
the phone) since `t` read it. Run `t focus` and try again.
