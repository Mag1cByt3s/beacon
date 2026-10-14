# The journal

beacon keeps a small daily journal next to the tasks: how you slept, how
productive the day was, what you got done, and whether you did sport. Unlike
tasks, which stay in Radicale, the journal belongs to beacon and lives in its
SQLite database (see the backup note in [deploy.md](deploy.md)).

A day has two parts:

- **Morning:** sleep quality (1–10) and optionally the hours slept. The
  morning part is done once `sleep_quality` is set.
- **Evening:** productivity (1–10), productive hours and time windows, the
  things done, sport. The evening part is done once `productivity` is set.

Everything else is optional and can be filled in bit by bit, from the
laptop or the phone.

## The journal day

A journal day runs from 04:00 to 04:00 in Europe/Berlin, so filling in the
evening at 00:30 still counts for the day before. Everywhere a date is
expected, `today` means the current journal day. Both can be changed on the
server:

| Variable | Default |
|---|---|
| `BEACON_JOURNAL_TZ` | `Europe/Berlin` |
| `BEACON_JOURNAL_DAY_START` | `04:00` |

The day is counted on the wall clock, so the days around the clock changes
are 23 and 25 hours long.

## On the laptop

All journal commands need the beacon server (`BEACON_SERVER_URL`).

```sh
t journal                    # asks for what is missing today
t journal --morning          # sleep
t journal --evening          # the evening part, even before 16:00
t journal show [date]        # a day, "today" or 2026-10-13
t journal status             # exit code 0: complete, 1: a part missing, 2: error
t did "HTB box" 90           # something done today, 90 minutes
t did Laufrunde 30 --offline # not at the computer
t timer start [label]        # focus timer; without a label, for the current task
t timer start Lesen --offline
t timer stop                 # asks "Was geschafft?", the label is the default
t timer stop "Box gerootet"  # or say it right away
t timer toggle               # stop the running timer or start one
t timer status [--label]     # the running timer and today's total
```

`t journal` asks for the morning part if it is missing, and from 16:00 on
also for the evening part. The evening starts with a suggestion:

- **Productive time** is the focus sessions of the day (one window each)
  plus the minutes of the things noted with `t did`. A session over 4 hours
  is marked as probably forgotten. If nothing was recorded at all, the
  suggestion falls back to ActivityWatch's measurement.
- **Things done** are the items of the day: stopped timers with a result,
  `t did` notes, and tasks completed with `t done`. Tasks completed elsewhere
  (on the phone, in Merkuro) are read from Radicale and offered as new
  items.

Press Enter to take a suggestion, or type your own value. Hours take a
comma or a point (`2,5`). Windows are typed like `9-12, 14:00-15:30 offline`.
Ctrl-D stops at any question and saves nothing.

A timer that is stopped with a result adds an item with the timer's minutes.
`t done` adds the completed task as an item too, with the minutes of the
timers that ran for it, and stops a timer still running for it.

## On the iPhone

Shortcuts ([ios-shortcuts.md](ios-shortcuts.md)): Geschafft notes
something done, Focus starts and stops a timer, Schlaf-Check asks for the
sleep quality, and Tages-Check asks for the productivity and sends the
day's steps from Health. At 09:30 and 23:30 a notification reminds you if
that part is missing; tapping it starts the check. Productive time is not
asked on the phone; `t journal` suggests it from what the laptop recorded.

## Reminders and the prompt (home-manager)

The beacon flake has a home-manager module:

```nix
{
  imports = [ inputs.beacon.homeManagerModules.default ];

  programs.beacon = {
    enable = true;
    serverUrl = "https://beacon.example.org";
    tokenFile = "/run/secrets/beacon-token";   # the laptop's token
    journal = {
      enable = true;
      terminal = [ "ghostty" "-e" ];
    };
  };
}
```

With `journal.enable` it sets up:

- **A reminder** at 09:45 and 23:45 (`journal.nudgeTimes`, also run after a
  missed time when the computer was off). Before 16:00 it checks the morning
  part, later the evening part. If that part is missing, a notification
  appears; its button "Eintragen" opens `t journal` in the terminal.
- **A prompt marker.** A timer runs `t journal status --write-cache` every
  15 minutes, which writes the missing parts to
  `~/.cache/beacon/journal-open`. The zsh hook (`t hook zsh-journal`) only
  checks whether that file is empty and sets `BEACON_JOURNAL_OPEN`, so the
  prompt never waits for the network. The module configures Starship to
  show `[journal offen]`; with a custom Starship `format`, add
  `${env_var.BEACON_JOURNAL_OPEN}` to it. Without Starship:
  `setopt prompt_subst; RPROMPT='${BEACON_JOURNAL_OPEN:+[$BEACON_JOURNAL_OPEN]}'`.
- **`beacon-timer-toggle`** for a KDE global shortcut (System Settings →
  Shortcuts → Add New → Command or Script, or with plasma-manager in
  `programs.plasma.hotkeys.commands`). It starts a timer, or asks
  "Was geschafft?" in a dialog and stops the running one. Cancel keeps the
  timer running; an empty answer stops it without a note.

## ActivityWatch

`beacon-aw-sync` reads the window events of a day from the local
ActivityWatch server (with [awatcher](https://github.com/2e3s/awatcher) on
KDE Wayland), keeps only the time you were not away from the keyboard, and
sorts the windows into categories. The time in the productive category is
added up; stretches less than 10 minutes apart are joined into windows, and
windows under 15 minutes are left out. The result is stored in the journal
as `aw_productive_minutes` and `aw_windows`, next to your own values, never
over them.

```nix
programs.beacon.awSync = {
  enable = true;
  tokenFile = "/run/secrets/beacon-aw-sync-token";   # the token of the client aw-sync
};
```

It runs hourly and at 23:15 and sends yesterday and today each time. By
hand: `beacon-aw-sync -date today -dry-run` prints what it would send.

It needs the token of the client `aw-sync`: the server lets only that client
upload ActivityWatch data, and lets it do nothing else.

### Categories

Without a categories file these rules apply (case-insensitive regular
expressions, matched anywhere in the app name or the window title):

| Category | App | Title |
|---|---|---|
| Arbeit | `konsole\|kitty\|alacritty\|wezterm\|ghostty\|codium\|code\|nvim\|burp\|ghidra\|wireshark\|obsidian` | |
| Arbeit | | `Hack The Box\|Proving Grounds\|OffSec\|HackTricks\|GitHub\|NixOS` |
| Arbeit > Lernvideos | | `IppSec\|0xdf` |
| Ablenkung | | `YouTube\|Reddit\|Twitch\|Netflix` |

Like in ActivityWatch, the deepest matching category wins: an IppSec video
on YouTube is Arbeit > Lernvideos, not Ablenkung. Among equally deep
categories the first rule wins.

Your own rules go into `awSync.categories` (or a JSON file named by
`BEACON_AW_CONFIG`):

```nix
programs.beacon.awSync.categories = {
  productive = "Arbeit";        # the top-level category that counts
  merge_gap_minutes = 10;
  min_window_minutes = 15;
  rules = [
    { category = [ "Arbeit" ]; app = "konsole|ghostty|codium|nvim"; }
    { category = [ "Arbeit" ]; title = "Hack The Box|GitHub"; }
    { category = [ "Arbeit" "Lernvideos" ]; title = "IppSec|0xdf"; }
    { category = [ "Ablenkung" ]; title = "YouTube|Reddit"; }
  ];
};
```

Other settings: `BEACON_AW_URL` (default `http://localhost:5600`), and the
same `BEACON_JOURNAL_TZ` and `BEACON_JOURNAL_DAY_START` as the server if you
changed them there.

## Tokens

Every client has its own token on the server, in a file named after the
client in `BEACON_TOKENS_DIR` (for example `tokens/laptop`, `tokens/iphone`,
`tokens/aw-sync`). The client name is logged and saved as `updated_by` with
journal changes. See [deploy.md](deploy.md).

## API

All endpoints need `Authorization: Bearer <token>`. Dates are `YYYY-MM-DD`
or `today`. Errors come as `{"error": "..."}` with status 400 for invalid
input.

| Endpoint | Does |
|---|---|
| `GET /api/journal/{date}` | the day and what is missing: `{"entry": {...}, "missing": ["morning", "evening"]}` |
| `PATCH /api/journal/{date}` | change the fields in the body; others stay, `null` clears one. Answers like GET |
| `GET /api/journal?from=&to=` | the days of a range, default the last 30 days |
| `GET /api/journal/{date}/items` | `{"items": [...]}` |
| `POST /api/journal/{date}/items` | add one item; answers `201` with it |
| `PUT /api/journal/{date}/items` | replace the day's items with `{"items": [...]}` |
| `GET /api/journal/{date}/suggestion` | the suggestion for the evening part |
| `PUT /api/journal/{date}/aw` | `{"minutes": 95, "windows": [...]}`, client `aw-sync` only |
| `GET /api/journal/export.csv?from=&to=` | the days as CSV |
| `GET /api/journal/items/export.csv?from=&to=` | the items as CSV |
| `POST /api/focus/start` | start a session: `{"label": "...", "kind": "offline"}`, both optional; `409` if one runs |
| `POST /api/focus/stop` | stop it: `{"result": "..."}` (optional) adds an item; `409` if none runs |
| `POST /api/focus/toggle` | stop the running session or start one; same body |
| `GET /api/focus?date=` | the running session and the day's sessions |

A journal day (`entry`), all fields optional:

```json
{
  "date": "2026-10-13",
  "sleep_quality": 7,
  "sleep_hours": 7.5,
  "productivity": 8,
  "productive_hours": 5.5,
  "productive_windows": [{"start": "09:00", "end": "12:30", "kind": "computer"}],
  "aw_productive_minutes": 312,
  "aw_windows": [{"start": "09:05", "end": "12:20", "kind": "aw"}],
  "sport": true,
  "sport_type": "Laufen",
  "sport_minutes": 30,
  "steps": 8400,
  "note": "",
  "updated_at": "2026-10-13T23:31:02+02:00",
  "updated_by": "iphone"
}
```

Ratings are 1–10, hours 0–24, minutes 0–1440. A window's `end` may be before
its `start` when it runs past midnight; `kind` is `computer`, `offline`, `aw`
or empty. Unknown fields are refused, so a typo is not silently lost.

An item:

```json
{"title": "HTB box", "minutes": 90, "kind": "computer", "source": "manual"}
```

`title` is required; `minutes` may be `null`; `kind` is `computer` (default)
or `offline`; `source` is `manual` (default), `focus` (a stopped session) or
`task` (a completed task, with its UID in `ref_id`). Items sent back in a
PUT keep their `created_at`.

The suggestion:

```json
{
  "date": "2026-10-13",
  "source": "sessions",
  "productive_minutes": 330,
  "productive_hours": 5.5,
  "productive_windows": [{"start": "09:00", "end": "12:30", "kind": "computer"}],
  "computer_minutes": 300,
  "offline_minutes": 30,
  "aw_minutes": 312,
  "suspicious": [],
  "items": [{"id": 4, "title": "HTB box", "minutes": 90, "kind": "computer", "source": "focus"},
            {"id": 0, "title": "Steuer", "minutes": null, "kind": "computer", "source": "task", "ref_id": "..."}]
}
```

`source` is `sessions` (focus sessions and items), `aw` (nothing recorded,
ActivityWatch's value) or `none`. Items with `"id": 0` are completed tasks
not recorded yet; to keep them, send them back with the other items in a
`PUT .../items`.
