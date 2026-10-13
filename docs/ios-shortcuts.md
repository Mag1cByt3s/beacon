# iPhone shortcuts for the journal

Four shortcuts for the Shortcuts app (Kurzbefehle) that talk to the journal
API (see [journal.md](journal.md)):

1. **Schlaf-Check:** every morning at 09:30, asks how you slept.
2. **Tages-Check:** every evening at 23:30, the evening part.
3. **Geschafft:** notes something done, from the home screen or Siri.
4. **Focus:** starts and stops a focus timer.

Action names are those of a German iPhone (iOS 18); the English name follows
in parentheses, as names differ slightly between versions. In the steps,
`«Name»` means: insert the variable or magic variable with that name (tap
the field, then pick it from the bar above the keyboard).

## Before you start

You need the iPhone's own token (the file `tokens/iphone` on the server, see
[deploy.md](deploy.md)). It ends up inside the shortcuts, so do not share
them with anyone; if you do, make a new token on the server.

Every shortcut starts with the same two steps. Build them once in the first
shortcut and duplicate it for the others:

1. **Text** (Text): `https://beacon.example.org`
   then **Variable festlegen** (Set Variable): `Server`
2. **Text** (Text): the token
   then **Variable festlegen** (Set Variable): `Token`

Every request is a **Inhalte von URL abrufen** (Get Contents of URL) action
with the method and URL given in the step, and under *Header* (Headers):

| Key | Value |
|---|---|
| `Authorization` | `Bearer «Token»` (with a space after Bearer) |

For a body, choose *Anfragetext: JSON* (Request Body: JSON) and add the
fields with the type given (*Zahl* Number, *Text* Text, *Boolescher Wert*
Boolean). Where a step says *Anfragetext: Datei* (Request Body: File), pick
the dictionary variable named there and also add the header
`Content-Type: application/json`.

`today` in a URL is the current journal day, which changes at 04:00, so a
check at 00:30 still fills in the day before.

To see what the server answered while building, put **Ergebnis anzeigen**
(Show Result) after a request. Errors come back as `{"error": "..."}`.

## 1. Schlaf-Check

New shortcut "Schlaf-Check":

1. The two setup steps (`Server`, `Token`).
2. **Inhalte von URL abrufen**: GET `«Server»/api/journal/today`
3. **Wörterbuchwert abrufen** (Get Dictionary Value): *Wert* for key
   `missing` in «Inhalte von URL»
4. **Text kombinieren** (Combine Text): «Wörterbuchwert» with *Leerzeichen*
   (Spaces)
5. **Wenn** (If): «Kombinierter Text» *enthält nicht* (does not contain)
   `morning`
   - **Kurzbefehl stoppen** (Stop This Shortcut)
   - *Ende Wenn* (End If)
6. **Nach Eingabe fragen** (Ask for Input): *Zahl* (Number), prompt
   `Wie hast du geschlafen? (1–10)`, *Dezimalzahlen erlauben* (Allow
   Decimals) off, *Negative Zahlen erlauben* off.
   then **Variable festlegen**: `Schlaf`
7. Sleep hours, choose one:
   - **Ask:** **Nach Eingabe fragen**: *Zahl*, prompt
     `Wie viele Stunden?`, decimals allowed. **Variable festlegen**:
     `Stunden`
   - **From Health** (needs sleep data from a watch or app):
     **Health-Proben suchen** (Find Health Samples): *Typ* Schlafanalyse
     (Sleep Analysis), *Wert* ist Geschlafen (Asleep), *Startdatum* in den
     letzten 1 Tagen (in the last 1 day). Then **Details von
     Health-Proben abrufen** (Get Details of Health Sample): *Dauer*
     (Duration). Then **Statistiken berechnen** (Calculate Statistics):
     *Summe* (Sum). Then **Berechnen** (Calculate): `÷ 3600` if the sum
     is in seconds (check once with Ergebnis anzeigen), and **Zahl runden**
     (Round Number) to 1 decimal. **Variable festlegen**: `Stunden`
8. **Inhalte von URL abrufen**: PATCH `«Server»/api/journal/today`,
   *Anfragetext: JSON*:

   | Key | Type | Value |
   |---|---|---|
   | `sleep_quality` | Zahl | «Schlaf» |
   | `sleep_hours` | Zahl | «Stunden» |

   which sends

   ```json
   {"sleep_quality": 7, "sleep_hours": 7.5}
   ```

9. Optional: **Mitteilung anzeigen** (Show Notification): `Gespeichert.`

Automation: *Automation* tab → **+** → *Tageszeit* (Time of Day) → 09:30,
*Täglich* (Daily) → *Sofort ausführen* (Run Immediately) → Schlaf-Check.

## 2. Tages-Check

New shortcut "Tages-Check". It saves the productivity last, so if anything
fails on the way, the evening part still counts as missing and the
reminder comes back.

**Check and suggestion**

1. The two setup steps.
2. **Inhalte von URL abrufen**: GET `«Server»/api/journal/today`
3. **Wörterbuchwert abrufen**: `missing` in «Inhalte von URL»
4. **Text kombinieren**: with *Leerzeichen*
5. **Wenn** «Kombinierter Text» *enthält nicht* `evening`:
   **Kurzbefehl stoppen**, *Ende Wenn*
6. **Inhalte von URL abrufen**: GET
   `«Server»/api/journal/today/suggestion`
   then **Variable festlegen**: `Vorschlag`

**Productive time**

7. **Wörterbuchwert abrufen**: `productive_hours` in «Vorschlag»
8. **Nach Eingabe fragen**: *Zahl*, prompt `Produktive Stunden`,
   *Standardantwort* (Default Answer) «Wörterbuchwert», decimals allowed.
   Enter takes the suggestion.
   then **Variable festlegen**: `Stunden`
9. **Wörterbuchwert abrufen**: `productive_windows` in «Vorschlag»,
   then **Variable festlegen**: `Fenster`
10. **Aus Menü auswählen** (Choose from Menu), prompt
    `Zeitfenster übernehmen?`, options `Ja` and `Nein`:
    - *Ja*:
      **Wörterbuch** (Dictionary), empty;
      **Wörterbuchwert festlegen** (Set Dictionary Value): key
      `productive_windows`, value «Fenster», in «Wörterbuch»;
      **Inhalte von URL abrufen**: PATCH `«Server»/api/journal/today`,
      *Anfragetext: Datei* with «Wörterbuch», header
      `Content-Type: application/json`. This sends
      `{"productive_windows": [{"start": "09:00", "end": "12:30", "kind": "computer"}]}`.
    - *Nein*: nothing (windows can be corrected with `t journal` on the
      laptop).
    - *Ende Menü*

**Things done**

11. **Wörterbuchwert abrufen**: `items` in «Vorschlag»,
    then **Variable festlegen**: `Items`
12. **Mit jedem wiederholen** (Repeat with Each) «Items»:
    **Wörterbuchwert abrufen**: `title` in «Wiederholungsobjekt» (Repeat
    Item); **Zu Variable hinzufügen** (Add to Variable): `Titel`.
    *Ende Wiederholen*
13. **Aus Liste auswählen** (Choose from List): «Titel», prompt
    `Was stimmt? (abwählen = löschen)`, *Mehrere auswählen* (Select
    Multiple) on, *Alle zu Beginn auswählen* (Select All Initially) on.
    then **Variable festlegen**: `Gewählt`
14. **Mit jedem wiederholen** «Items»:
    **Wörterbuchwert abrufen**: `title` in «Wiederholungsobjekt»;
    **Wenn** «Gewählt» *enthält* «Wörterbuchwert»:
    **Zu Variable hinzufügen**: «Wiederholungsobjekt» to `Behalten`;
    *Ende Wenn*. *Ende Wiederholen*
15. "Noch was vergessen?" as a loop (Shortcuts has no while loop, so the
    loop runs at most 10 times and a variable ends it):
    **Wiederholen** (Repeat) 10 times:
    - **Wenn** «Fertig» *ist nicht* (is not) `ja`:
      - **Aus Menü auswählen**, prompt `Noch was vergessen?`, options
        `Ja`, `Nein`:
        - *Ja*:
          **Nach Eingabe fragen**: *Text*, `Was?` → **Variable
          festlegen**: `Was`;
          **Nach Eingabe fragen**: *Zahl*, `Minuten (0 = unbekannt)` →
          **Variable festlegen**: `Minuten`;
          **Aus Menü auswählen**, `Wo?`, options `Computer`, `Offline`:
          in *Computer* a **Text** `computer`, in *Offline* a **Text**
          `offline`; after *Ende Menü* **Variable festlegen**: `Art`
          (the menu's result);
          **Wörterbuch**: `title` (Text) «Was», `kind` (Text) «Art»,
          `source` (Text) `manual`;
          **Wenn** «Minuten» *ist größer als* (is greater than) `0`:
          **Wörterbuchwert festlegen**: `minutes` = «Minuten» in
          «Wörterbuch» (otherwise leave it out: unknown). *Ende Wenn*;
          **Zu Variable hinzufügen**: «Wörterbuch» to `Behalten`
          (if the Wenn changed it, add the result of Wörterbuchwert
          festlegen instead).
        - *Nein*: **Text** `ja` → **Variable festlegen**: `Fertig`
        - *Ende Menü*
      - *Ende Wenn*
    - *Ende Wiederholen*
16. **Wörterbuch**, empty; **Wörterbuchwert festlegen**: key `items`,
    value «Behalten», in «Wörterbuch»;
    **Inhalte von URL abrufen**: PUT `«Server»/api/journal/today/items`,
    *Anfragetext: Datei* with the result, header
    `Content-Type: application/json`. This replaces the day's list:

    ```json
    {"items": [
      {"id": 4, "title": "HTB box", "minutes": 90, "kind": "computer", "source": "focus"},
      {"title": "Laufrunde", "minutes": 30, "kind": "offline", "source": "manual"}
    ]}
    ```

    Items that came from the suggestion are sent back as they were
    (completed tasks with `"id": 0` are recorded this way); deselected
    ones are gone.

**Sport and steps**

17. **Aus Menü auswählen**, `Sport gemacht?`, `Ja` / `Nein`:
    - *Ja*: **Nach Eingabe fragen**: *Text*, `Was?` → `SportArt`;
      **Nach Eingabe fragen**: *Zahl*, `Minuten` → `SportMinuten`;
      **Inhalte von URL abrufen**: PATCH `«Server»/api/journal/today`,
      *Anfragetext: JSON*: `sport` (Boolescher Wert) on, `sport_type`
      (Text) «SportArt», `sport_minutes` (Zahl) «SportMinuten».
      Sends `{"sport": true, "sport_type": "Laufen", "sport_minutes": 30}`.
    - *Nein*: **Inhalte von URL abrufen**: PATCH, JSON: `sport`
      (Boolescher Wert) off. Sends `{"sport": false}`.
    - *Ende Menü*
18. Steps of the journal day. The journal day's date is the date of four
    hours ago, which also works at 00:30:
    **Datum anpassen** (Adjust Date): *4 Stunden abziehen* (Subtract 4
    hours) from «Aktuelles Datum» (Current Date) → `Journaltag`;
    **Health-Proben suchen**: *Typ* Schritte (Steps), *Startdatum* ist
    (is) «Journaltag»;
    **Statistiken berechnen**: *Summe* → **Zahl runden**: to whole
    numbers → `Schritte`;
    **Inhalte von URL abrufen**: PATCH, JSON: `steps` (Zahl) «Schritte».

**Productivity, last**

19. **Nach Eingabe fragen**: *Zahl*, `Produktivität heute (1–10)` →
    `Produktivität`
20. **Inhalte von URL abrufen**: PATCH `«Server»/api/journal/today`,
    *Anfragetext: JSON*:

    | Key | Type | Value |
    |---|---|---|
    | `productivity` | Zahl | «Produktivität» |
    | `productive_hours` | Zahl | «Stunden» |

21. **Mitteilung anzeigen**: `Tag gespeichert.`

Automation: *Tageszeit* 23:30, daily, *Sofort ausführen*. Optionally a
second one at 00:30: it stops right away if the evening is already done.

## 3. Geschafft

New shortcut "Geschafft" (also good as a home screen icon or for Siri):

1. The two setup steps.
2. **Nach Eingabe fragen**: *Text*, `Was geschafft?` → `Was`
3. **Nach Eingabe fragen**: *Zahl*, `Minuten (0 = unbekannt)` → `Minuten`
4. **Aus Menü auswählen**, `Wo?`: *Computer* → **Text** `computer`;
   *Offline* → **Text** `offline`; after *Ende Menü* **Variable
   festlegen**: `Art`
5. **Wenn** «Minuten» *ist größer als* `0`:
   - **Inhalte von URL abrufen**: POST
     `«Server»/api/journal/today/items`, *Anfragetext: JSON*: `title`
     (Text) «Was», `minutes` (Zahl) «Minuten», `kind` (Text) «Art».
     Sends `{"title": "Writeup", "minutes": 45, "kind": "offline"}`.
   - *Sonst* (Otherwise): the same request without `minutes`.
   - *Ende Wenn*
6. **Mitteilung anzeigen**: `Notiert.`

The server answers `201` with the stored item.

## 4. Focus

New shortcut "Focus" (for the home screen, the action button or a widget).
It starts an offline timer, or asks what was done and stops the running
one:

1. The two setup steps.
2. **Inhalte von URL abrufen**: GET `«Server»/api/focus`
3. **Wörterbuchwert abrufen**: `running` in «Inhalte von URL» →
   `Läuft`
4. **Wenn** «Läuft» *hat einen Wert* (has any value):
   - **Wörterbuchwert abrufen**: `label` in «Läuft»
   - **Nach Eingabe fragen**: *Text*, `Was geschafft?`,
     *Standardantwort* «Wörterbuchwert». Empty means: stop without a note.
     → `Ergebnis`
   - **Inhalte von URL abrufen**: POST `«Server»/api/focus/stop`,
     *Anfragetext: JSON*: `result` (Text) «Ergebnis».
     Sends `{"result": "Kapitel 3 gelesen"}`; a result becomes an item with
     the timer's minutes.
   - **Wörterbuchwert abrufen**: `minutes` in «Inhalte von URL»
   - **Mitteilung anzeigen**: `Gestoppt: «Wörterbuchwert» min`
   - *Sonst*:
   - **Inhalte von URL abrufen**: POST `«Server»/api/focus/start`,
     *Anfragetext: JSON*: `kind` (Text) `offline`, and optionally `label`
     (Text) from a **Nach Eingabe fragen**. Without a label the session
     is for the current task.
     Sends `{"kind": "offline"}`.
   - **Mitteilung anzeigen**: `Timer läuft.`
   - *Ende Wenn*

`POST /api/focus/toggle` does start and stop in one request (with the
same body), but cannot ask in between what was done; it suits a quick
toggle without a note.
