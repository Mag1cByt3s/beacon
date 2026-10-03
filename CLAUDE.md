# beacon

One task at a time. A self-hosted focus layer on top of CalDAV tasks.

## Why this exists

beacon is a personal productivity tool. The problems it solves:

- **Forgetting** tasks and forgetting to check the todo list at all.
- **Working through tasks one after another** instead of jumping around.
- **Staying focused** without getting pulled away by every random thought.

Design principles (apply them to every feature):

1. **Capture in under 2 seconds.** No required fields beyond the title.
2. **The list comes to the user.** The current task shows up where he already looks (terminal, KDE panel, phone).
3. **Exactly one task visible in focus mode.** Actions: done, skip, too big (split).
4. **Catch distractions, don't follow them.** Random thoughts go to an inbox, focus continues.
5. **Reliable before smart.** Core features must work even if other services are down. No feature may depend on an LLM.

## Decisions (settled, don't revisit without asking)

- **Language: Go.** The owner is new to Go: keep code idiomatic, simple and readable. Prefer the standard library. Add short comments where a Go idiom might be unfamiliar. No clever abstractions.
- **Source of truth: Radicale (CalDAV), already running.** Tasks are `VTODO` items. Apple Reminders (iPhone), todoman and Merkuro (KDE) already sync with it directly. beacon must never break those clients.
- **beacon does not own task data.** It reads and writes VTODOs in Radicale and only keeps its own *focus state* (current task, skip order) separately.
- **Remote access:** an existing reverse proxy with its own subdomain terminates TLS. The beacon API requires a bearer token on every request.
- **iPhone:** native Reminders handles capture and lists. beacon will later serve a PWA for the focus view only.

## Architecture

```
iPhone Reminders ─┐
todoman (CLI)    ─┼──── CalDAV ────► Radicale (FreeBSD jail on "odin")
Merkuro (KDE)    ─┘                       ▲
                                          │ CalDAV
t (beacon CLI) ──── HTTPS + token ──► beacon server (Go, FreeBSD jail)
                                          └── SQLite: focus state, keyed by VTODO UID
```

Until the server milestone, the CLI talks to Radicale directly through the same internal package.

### Repository layout

```
cmd/t/            CLI binary "t"
cmd/beacon/       server binary (from milestone 3)
internal/caldav/  CalDAV access: list, create, complete VTODOs
internal/focus/   ordering + focus logic (pure, no I/O, well tested)
internal/config/  config from environment variables
```

### Dependencies

- `github.com/emersion/go-webdav` (CalDAV client)
- `github.com/emersion/go-ical` (iCalendar parsing)
- SQLite from milestone 3: `modernc.org/sqlite` (pure Go, so it cross-compiles to FreeBSD without cgo)
- Ask before adding anything else.

## Important constraints

- **Never modify VTODO fields that other clients rely on** beyond what an action requires. To complete a task, set `STATUS:COMPLETED`, `COMPLETED`, `PERCENT-COMPLETE:100` and update `LAST-MODIFIED`/`DTSTAMP`. Preserve all other properties, including unknown `X-` ones.
- **Do not store focus state in `X-` properties** on VTODOs. iOS Reminders may strip unknown properties when it edits a reminder. Keep focus state in beacon's own storage, keyed by UID.
- **Recurring tasks (`RRULE`):** v1 ignores them in focus mode and never completes them. They are handled on the iPhone.
- **Lists:** which collections count is configurable. The default is `Todo`. The collections on the server currently are `Reminders`, `Todo`, `Groceries` (plus a calendar and others; only collections that support VTODO matter).
- **Ordering for "next task"** (in `internal/focus`, pure function): overdue first, then due date ascending, then priority (1 = highest, 0 = none counts as lowest), then created ascending.
- **Secrets never go into the repo.** No passwords or tokens in code, tests, fixtures or commit history.

## Configuration (environment variables)

| Variable | Purpose | Example |
|---|---|---|
| `BEACON_CALDAV_URL` | Radicale base URL | `https://dav.example.org/` |
| `BEACON_CALDAV_USER` | CalDAV user | `pascal` |
| `BEACON_CALDAV_PASSWORD_CMD` | command whose stdout is the password | `pass radicale` |
| `BEACON_LISTS` | comma-separated list names used for focus | `Todo` |
| `BEACON_DEFAULT_LIST` | list for new captures | `Todo` |
| `BEACON_SERVER_URL` | beacon API URL (from milestone 3) | `https://beacon.example.org` |
| `BEACON_TOKEN` | API bearer token (from milestone 3) | read from env only |
| `BEACON_LISTEN` | server listen address (from milestone 3) | `127.0.0.1:8080` (default) |
| `BEACON_DB` | server SQLite file for focus state (from milestone 3) | `/var/db/beacon/beacon.db` |

Run the password command without a shell (split into argv), trim the trailing newline, and never log it.

## Milestones

Work on **one milestone at a time**. When it's done: build, run the tests, summarize what changed, and **stop**. Don't start the next one unasked.

1. **Read.** `internal/caldav` lists open VTODOs from the configured lists. `internal/focus` picks the next task. CLI: `t list` (open tasks, ordered) and `t focus` (only the next task, one line). Talks to Radicale directly.
2. **Write.** `t "buy coffee"` creates a VTODO in the default list (title only). `t done` completes the current focus task.
3. **Server.** `cmd/beacon`: HTTP JSON API (`GET /current`, `GET /tasks`, `POST /tasks`, `POST /current/done`, `POST /current/skip`), bearer token auth, SQLite focus state (current task, skip order), structured logging. The CLI switches to the API when `BEACON_SERVER_URL` is set.
4. **Resilience.** The CLI queues captures in a local file when the server is unreachable and flushes them later. Shell hook snippet (bash/zsh) that prints the current task when a terminal opens, fast and silent on errors.
5. **Deploy.** Cross-compile for FreeBSD (`GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0`), an `rc.d` script for the jail, and a short `docs/deploy.md`.

Later, out of scope for now: KDE plasmoid and KRunner plugin, PWA focus view, focus extras (DND, site blocking), and an MCP server for Hermes Agent.

## Development rules

- Go version: latest stable. `gofmt` and `go vet` must pass.
- Tests: `go test ./...` must pass. `internal/focus` gets table-driven unit tests. CalDAV code is tested against an `httptest` server or a small fake behind an interface; tests never contact the real Radicale.
- Error messages are short, human, and say what to do ("BEACON_CALDAV_URL is not set").
- CLI output is calm and minimal: one line for `t focus`, no colors required, nothing printed on success where nothing is useful.
- Small commits with clear messages. Never commit secrets or a `.env` file (add it to `.gitignore`).
- Keep `README.md` short: what beacon is, how to configure it, how to run it.
