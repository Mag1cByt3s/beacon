# iPhone shortcuts for the journal

Four shortcuts for the Shortcuts app (Kurzbefehle) that talk to the journal
API (see [journal.md](journal.md)):

1. **Geschafft:** notes something done, from the home screen or Siri.
2. **Focus:** starts and stops a focus timer.
3. **Schlaf-Check:** every morning at 09:30, asks how you slept.
4. **Tages-Check:** every evening at 23:30, asks how productive the day was
   and sends the day's steps.

The phone asks as little as possible. Productive time is not asked at all:
the laptop records it (focus timers, `t did`, ActivityWatch) and suggests it
in `t journal`. Walking shows up in the steps, so there is no sport
question either.

Action names are those of a German iPhone (iOS 18); the English name follows
in parentheses, as names differ slightly between versions. `«Name»` means:
insert the variable or the result of the action with that name.

## Working in the editor

A few things that are easy to get wrong:

- **New actions land at the bottom.** Hold one down and drag it to its
  place.
- **Picking a variable:** tap the field, then *Variable auswählen* (Select
  Variable) above the keyboard and tap the action whose result you want.
  When several results have the same name (two «Wörterbuchwert»), this is
  the only safe way. In a date field, hold the field down to get there.
- **Watch where the cursor is.** A variable inserted while a key or URL field
  is still active ends up in that field (`label«Wörterbuchwert»`). Keys and
  URL paths are typed text only.
- **Number fields in a JSON body:** add the field with the type *Zahl*
  (Number) first, then insert the variable. Changing a Text field to Zahl
  after the variable is in it may not stick, and the server refuses
  `"minutes": "5"` (a string).
- **Deleting a *Wenn* (If) or a menu deletes everything inside it.** To keep
  an action, drag it out of the block first.
- **A failed request does not stop a shortcut.** It carries on with the
  error (`{"error": "unauthorized"}`) as its result. To see what the server
  answered, put **Ergebnis anzeigen** (Show Result) after a request. The
  server log names every rejected request (`status=401`).

## The token

You need the iPhone's own token (the file `tokens/iphone` on the server, see
[deploy.md](deploy.md)). Every shortcut starts with the same three actions,
so the URL and the token are in one place each:

```
Text                  https://beacon.example.org
Text                  <the token, nothing else>
Variable festlegen    Token  to «Text»  (the token's Text)
```

Every request is an **Inhalte von URL abrufen** (Get Contents of URL) with
one header, and no empty header row (an empty one makes iOS fail with "The
network connection was lost"):

| Key | Value |
|---|---|
| `Authorization` | `Bearer «Token»` (one space after Bearer) |

URLs are `«Text»` (the server URL) followed by the typed path, like
`«Text»/api/focus`.

**Do not share these shortcuts.** An iCloud link contains the token, and a
link cannot be revoked. To let someone check a shortcut, duplicate it,
replace the token in the copy with `XXX`, and share the copy.

To change the token, in the jail (it ends the old one right away):

```sh
(umask 077 && openssl rand -hex 32 > /usr/local/etc/beacon/tokens/iphone)
chown beacon:beacon /usr/local/etc/beacon/tokens/iphone
service beacon restart   # tokens are read at startup
```

Then paste the new token into the token Text of all four shortcuts.

## 1. Geschafft

```
Text                      https://beacon.example.org
Text                      <token>
Variable festlegen        Token
Nach Eingabe fragen       Text, "Was geschafft?"
Nach Eingabe fragen       Zahl, "Minuten (leer = unbekannt)"
Aus Menü auswählen        "Wo?"
    Computer:  Text  computer
    Offline:   Text  offline
Ende Menü
Wenn «Bereitgestellte Eingabe» (of the minutes question) hat einen Wert
    Zahl                      «Bereitgestellte Eingabe» (of the minutes question)
    Inhalte von URL abrufen   POST «Text»/api/journal/today/items, JSON:
                                title   Text  «Bereitgestellte Eingabe» (of "Was geschafft?")
                                minutes Zahl  «Zahl»
                                kind    Text  «Menüergebnis»
Sonst
    Inhalte von URL abrufen   POST «Text»/api/journal/today/items, JSON:
                                title   Text  «Bereitgestellte Eingabe» (of "Was geschafft?")
                                kind    Text  «Menüergebnis»
Ende Wenn
Ergebnis anzeigen         «Wenn-Ergebnis» (If Result)
```

It sends `{"title": "Writeup", "minutes": 45, "kind": "offline"}`, or
without `minutes` (unknown) when the question is left empty. The server
answers `201` with the stored item.

## 2. Focus

For the home screen, the action button or a widget. It starts an offline
timer, or asks what was done and stops the running one:

```
Text                      https://beacon.example.org
Text                      <token>
Variable festlegen        Token
Inhalte von URL abrufen   GET «Text»/api/focus
Wörterbuchwert abrufen    running  in «Inhalte von URL»
Wenn «Wörterbuchwert» hat einen Wert
    Wörterbuchwert abrufen    label  in the «Wörterbuchwert» of running
    Nach Eingabe fragen       Text, "Was geschafft?", Standardantwort «Wörterbuchwert» (label)
    Inhalte von URL abrufen   POST «Text»/api/focus/stop, JSON:
                                result  Text  «Bereitgestellte Eingabe»
    Wörterbuchwert abrufen    minutes  in the «Inhalte von URL» of the stop request
    Mitteilung anzeigen       Gestoppt: «Wörterbuchwert» min
Sonst
    Inhalte von URL abrufen   POST «Text»/api/focus/start, JSON:
                                kind  Text  offline
    Mitteilung anzeigen       Timer läuft.
Ende Wenn
```

A timer started without a label is for the current task. A result becomes
an item with the timer's minutes; an empty answer stops the timer without
one.

`POST /api/focus/toggle` does start and stop in one request, but cannot ask
in between what was done; it suits a quick toggle without a note.

## 3. Schlaf-Check

Asks for the sleep quality only; that is all the morning part needs. It
asks nothing if the morning is already filled in (on the laptop, for
example).

```
Text                      https://beacon.example.org
Text                      <token>
Variable festlegen        Token
Inhalte von URL abrufen   GET «Text»/api/journal/today
Wörterbuchwert abrufen    missing  in «Inhalte von URL»
Text kombinieren          «Wörterbuchwert»  with Leerzeichen (Spaces)
Wenn «Kombinierter Text» enthält nicht (does not contain)  morning
    Kurzbefehl stoppen
Sonst
Ende Wenn
Nach Eingabe fragen       Zahl, "Wie hast du geschlafen? (1-10)",
                          no decimals, no negative numbers
Inhalte von URL abrufen   PATCH «Text»/api/journal/today, JSON:
                            sleep_quality  Zahl  «Bereitgestellte Eingabe»
Mitteilung anzeigen       Gespeichert.
```

It sends `{"sleep_quality": 7}`. If the GET fails (a wrong token), there is
no `missing` and the shortcut stops without asking.

Automation: *Automation* tab → **+** → *Tageszeit* (Time of Day) → 09:30,
*Täglich* (Daily) → *Sofort ausführen* (Run Immediately) → Schlaf-Check. If
the question does not show while the phone is locked, use *Vor dem
Ausführen bestätigen* (Confirm Before Running) instead.

## 4. Tages-Check

The same as Schlaf-Check, for the evening part, plus the day's steps from
Health (the iPhone counts them without a watch):

```
Text                      https://beacon.example.org
Text                      <token>
Variable festlegen        Token
Inhalte von URL abrufen   GET «Text»/api/journal/today
Wörterbuchwert abrufen    missing  in «Inhalte von URL»
Text kombinieren          «Wörterbuchwert»  with Leerzeichen
Wenn «Kombinierter Text» enthält nicht  evening
    Kurzbefehl stoppen
Sonst
Ende Wenn
Nach Eingabe fragen       Zahl, "Wie produktiv war der Tag? (1-10)",
                          no decimals, no negative numbers
Datum anpassen            4 Stunden abziehen from Aktuelles Datum
Health-Proben suchen      Typ ist Schritte, Startdatum is on «Angepasstes Datum»,
                          Unit count, Group By Day, Limit off
Statistiken berechnen     Summe of «Health-Proben»
Zahl runden               «Summe», Einer (Ones)
Inhalte von URL abrufen   PATCH «Text»/api/journal/today, JSON:
                            productivity  Zahl  «Bereitgestellte Eingabe»
                            steps         Zahl  «Gerundete Zahl»
Mitteilung anzeigen       Gespeichert.
```

For the start date, choose *is on* and hold the date field down to insert
«Angepasstes Datum». *Group By Day* adds the day up to one value, so steps
from two sources (iPhone and a watch) are not counted twice.

It sends `{"productivity": 7, "steps": 8400}`. The date four hours ago is
the journal day's date, so a run at 00:30 still counts the steps of the day
before. The first run asks for access to the steps in Health.

Saving `productivity` completes the evening part. Productive time and
windows stay with the laptop: `t journal` suggests them from the focus
timers, `t did` and ActivityWatch.

Automation: *Tageszeit* 23:30, daily, *Sofort ausführen*. Optionally a
second one at 00:30: it stops right away if the evening is already done.

## Testing

`today` in a URL is the current journal day, which changes at 04:00. When
today's part is already filled in, Schlaf-Check and Tages-Check stop right
away. To test one anyway, put a past date in both URLs
(`«Text»/api/journal/2026-10-12`), run it, check the day with
`t journal show 2026-10-12`, and change the URLs back to `today`. Clear the
test value with a PATCH of `{"sleep_quality": null}` (or `productivity`).
