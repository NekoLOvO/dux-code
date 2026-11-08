# AGENTS.md

Instructions for AI agents working in this repository. This file is also loaded into this project's
own agent runs as "project instructions", and the reader caps it at 32 KB — keep it short and keep it
accurate.

## What this is

Dux Code is a desktop coding agent for the Dux Shell (Quickshell) desktop. Two halves:

- `core/` — a Go daemon, `dcode`. It owns the conversation: providers, model calls, tools, sessions,
  config, scheduled tasks. It talks to LLM APIs directly; no external agent runtime is involved.
- The repository root — QML, drawn by Quickshell. No webview, no Electron, no JS bundler.

The UI and the CLI talk to the daemon over the same Unix socket, so what the window shows and what
`dcode` reports can never disagree.

Linux only, and Wayland in practice. The window is an ordinary xdg_toplevel (`FloatingWindow`), not a
layer-shell surface: the compositor places it like any other app window, so tiling, floating and
decoration are the compositor's business. That is a design choice, not a TODO.

## Non-negotiables

1. **Never edit `dux-qml-common/`.** It is a git submodule shared with dux-calendar and dux-files.
   `make check` fails when it is dirty. Fix things on our side; if the library is genuinely wrong,
   that is an upstream change.
2. **No CJK in source or documents.** Comments, log lines and error messages inside `.go`, `.qml` and
   `.sh` are English, and so is every `.md` in the repository. The only Chinese in the tree is
   translation data (`translations/poexports/`, `translations/overrides/`) and the localized keys of
   `assets/*.desktop` — the desktop entry carries its own translations as `Name[zh_TW]`-style keys,
   which is the only mechanism the desktop environment reads. `Name` is left in English on purpose:
   it is the product's name, and `GenericName`/`Comment`/`Keywords` are what a translated menu draws.
   `make i18n` exits non-zero if a QML `I18n.tr()` term is not English, and `make check` runs
   `tools/check-english.py`, which fails on any CJK ideograph in a
   `.go`/`.qml`/`.js`/`.sh`/`.md` file. Full-width punctuation counts too: the first release shipped
   error messages with full-width colons and parentheses, and nothing noticed. The check flags
   ideographs only, because a cutset of CJK quote marks (used to strip the quotes a model wraps a
   suggested title in) is data rather than prose.
   `translations/overrides/<locale>.json` is only for component-library strings upstream has not
   translated. It is a stopgap, not a place to keep favourites: an override for a term **we** also
   define never takes effect (`cmd_apply` fills overrides after our own strings), and one for a term
   upstream *has* translated would silently freeze an old wording. Upstream's `zh_TW.json` ships 91 of
   130 terms empty, which is why 62 overrides exist; the fix is to send them upstream, not to grow
   this file.
3. **Daemon strings are English and stay English.** They do not go through the translation layer;
   `I18n.tr()` is for QML text only.
4. **Never hand-write generated output** into `core/internal/uibundle/dist/`, `core/bin/` or `dist/`.
5. **Do not add a webview, Electron, or a Node build step.** QML draws, Go thinks. That is the point.

## Layout

| Path | Contents |
|---|---|
| `shell.qml` | entry point: window loader, tray, `IpcHandler` with `target: "dcode"` |
| `Common/` | app singletons — `I18n`, `Theme`, `SettingsData`, `Paths`, `Blockizer` |
| `Services/Backend.qml` | the only IPC client on the QML side; every daemon call and cached field lives here |
| `Modules/`, `Widgets/`, `Modals/` | screens, small widgets, dialogs |
| `DCommon/` | symlink to `dux-qml-common/DCommon` — component library, read-only |
| `translations/` | extractor, `template.json`, `poexports/<locale>.json`, `overrides/<locale>.json` |
| `core/cmd/dcode/` | cobra commands: `daemon`, `ui`, `new`, `send`, `sessions`, `show`, `toggle`, `ping`, `version` |
| `core/internal/api/` | IPC method implementations (`router.go`) and approval plumbing |
| `core/internal/agent/` | the run loop, context compaction, vision, skills wiring |
| `core/internal/llm/` | provider clients (openai, anthropic, gemini, responses) and error classification |
| `core/internal/tools/` | tool implementations, path guard, sandbox, approval gate |
| `core/internal/{store,config,tasks,mcp,skills,search,ipc}/` | state and plumbing |
| `core/internal/atomicfile/` | replace-by-rename, file and directory synced, for every file the daemon rewrites whole |
| `tools/check-upstream.sh` | Theme + submodule contract check |
| `tools/check-windows.py` | every `FloatingWindow` instance names a parent window |
| `tools/check-signal-arity.py` | no QML signal is called with fewer arguments than it declares |
| `tools/check-desktop.py` | the .desktop asset, including the spelling of its localized keys |
| `docs/`, `UPSTREAM.md` | design notes (English) |

## Commands

```bash
make dev                 # build without the embedded UI
make run                 # build + upstream check + `dcode ui -c .`   ← normal dev loop
make test vet fmt
make check               # submodule, Theme contract, window parents, signal arity, desktop entry,
                         # qmllint; green before committing UI work
make check-static        # the same without qmllint — what CI runs (see the Landmines note)
make i18n                # re-extract I18n.tr() -> template.json, print per-locale coverage

core/bin/dcode daemon    # daemon alone, no window
core/bin/dcode send "hi" # one turn from the terminal, streams the reply
```

QML smoke test without a display (this is how to catch a broken binding without disturbing the
running app):

```bash
XDG_CONFIG_HOME=/tmp/dx/config XDG_RUNTIME_DIR=/tmp/dx/rt XDG_CACHE_HOME=/tmp/dx/cache \
  QT_QPA_PLATFORM=offscreen timeout 10 qs -p .
```

**Measuring QML from that shell.** Two things waste an afternoon if you do not know them:

- `console.log` is not a reliable channel. It reaches stdout on some runs and only lands in
  `$XDG_RUNTIME_DIR/quickshell/by-id/<id>/log.log` on others, and by the time you notice you are
  reading an empty grep. Write results where you can always find them:
  `Quickshell.execDetached(["sh", "-c", "echo 'result …' >> /tmp/m.txt"])`.
- `Qt.quit()` does not reliably end a `Window`-rooted config, and a load that never finishes leaves
  the process waiting; the tool call hangs until its own timeout. Always run under
  `timeout -s KILL N` and put a watchdog `Timer` in the harness that writes a line and quits.

The second half: a harness that drives `Chat.loadSession` must wait for `Backend.connected`, not for a
fixed delay — a load issued before the socket is up fails silently and reports zero rows.

## How the pieces fit

- **IPC** — one NDJSON line per message over `$XDG_RUNTIME_DIR/dcode/daemon.sock`. Requests are
  `{id, method, params}`; replies are `{id, result}` or `{id, error}`. The daemon also pushes events:
  `delta`, `reasoning.delta`, `run.started|step|usage|finished|failed`, `tool.started|finished`,
  `context.compacted`, `session.renamed`, `config.changed` (the daemon rewrote config.json — today only a
  learned output ceiling), `mcp.changed` (an MCP server finished starting or failed, or its tool list
  changed; the settings row is the only thing that knows, and it learns it from this).
  `run.step` and `run.finished` both carry a message id (`previousMessageId`, `message.id`): a live
  transcript row is created without one, and every message action is addressed by id. Drop them and
  branch/edit/regenerate on a reply that has just finished answers "that message is still being saved"
  until the session is reloaded — the bug that made a restart the workaround.
- **State on disk** — all XDG, nothing hard-coded to `~` except the skills directory (a cross-tool
  convention): `config.json` and `ui-settings.json` under `$XDG_CONFIG_HOME/dcode/`, sessions /
  `tasks.json` / `exports/` under `$XDG_DATA_HOME/dcode/`, `daemon.log` under `$XDG_STATE_HOME/dcode/`,
  the extracted UI and `imagecache/` under `$XDG_CACHE_HOME/dcode/`.
- **One instance** — `dcode ui` starts the daemon, then *raises* an existing window instead of
  opening a second one. `make hot` (i.e. `DUXCODE_ENABLE_HOTRELOAD=1`) is the exception, because
  that flow needs a fresh shell on the tree being edited.
- **Run loop** — a turn is up to N model calls ("steps"), with tools in between. The final step is a
  wrap-up with no tools, so the model can say what it did instead of dying mid-loop.
  `chat.cancel` aborts through the context.
- **Compaction** — runs once at the start of a turn, never mid-loop. It measures the history *about to
  be sent* — the summary plus the messages after the cut, not the stored transcript — cuts on a `user`
  boundary (never splitting `tool_calls` from `tool_result`), and replaces the dropped part with a
  summary. The stored transcript is untouched; only the copy sent to the model changes. The cut is the
  **current turn's own message**: everything before it folds, so the divider lands where the user
  expects it. Two rules keep the summary honest: the previous summary is carried into the next one, and
  the folded range is read in chunks bounded by the model's window. When the chunk cap is reached the
  summary says how much went unread instead of reading as complete. Folding is announced
  (`context.compacting`, with the count) *before* the first summary request, because it is several
  model calls during which the turn sends no text of its own — the UI shows "compressing" instead of
  "working" and a toast, rather than looking hung. The summary request asks for no thinking depth (`off`
  where the model table offers it, `low` otherwise): summarizing is a mechanical rewrite, and "auto" on
  a model with adaptive thinking is the slowest possible choice for the least useful work. If the depth
  is refused anyway the chunk is retried without it — the table is a guess, and a 400 here would cost
  the summary rather than one option.
- **Config versioning** — `version` in `config.json` is not decoration. Bump it only together with a
  migration in `config.FixDefaults`, or old files silently change meaning (that is how `0` became a
  legal value for the three limits).

## Landmines

- **`qmllint` resolves `qs.*` imports only through a generated index.** Quickshell answers
  `import qs.Common` itself, by synthesizing a `qmldir` in memory at run time; the QML module system
  wants a real one, and this tree has none. `tools/qmllint-farm.py` builds that index under
  `$XDG_CACHE_HOME/dcode-qmllint` (symlinks plus a `qmldir` per module, `singleton` taken from
  `pragma Singleton`), `make qmllint` passes it with `-I`, and `.vscode/settings.json` gives the same
  path to qmlls. Without it every import fails and the diagnostics say nothing about the code: 3228
  instead of 255.
- **`qmllint` on PATH must be Qt 6's.** `/usr/local/bin/qmllint` is a symlink to
  `/usr/lib/qt6/bin/qmllint`; the `qmllint` target refuses any binary that does not report version 6.
  CI does not run it — `make check-static` is everything else — because the verdict is
  version-shaped: Ubuntu 24.04 packages Qt 6.4, which cannot parse `pragma ComponentBehavior`, and
  the official 6.12 Linux binaries link `libicui18n.so.73` against a runner that has ICU 74, so they
  exit 127 before reading a file. A missing linter is not a passing lint, so the target fails loudly
  rather than skipping; the lint runs where the UI is written.
- **`qmllint`'s text output drops most of what it counts.** On one file it printed 20 of 68
  diagnostics, on another 0 of 4, while `--max-warnings` refused the run because of the ones it never
  showed. So `make qmllint` goes through `tools/qmllint-report.py`, which parses `--json` and fails on
  any warning. Do not judge a run by the printed lines alone.
- **A QML signal called with too few arguments throws, and nothing checks it.** Qt 6 raises
  `TypeError: Insufficient arguments` at the call site; qmllint's `BadSignalHandlerParameters` covers
  handler parameters, not calls, so the mistake is invisible until that code path runs — and if it is
  the path that ends a turn, the UI stays in "running" with the turn already stopped.
  `tools/check-signal-arity.py` (in `make check`) compares every unqualified call against the
  declaration in the same file.
- **Every modal window needs `parentWindow`.** A `FloatingWindow` with no parent is a plain
  xdg_toplevel, so a tiling compositor tiles it edge to edge instead of floating it over the window
  that opened it. There is no default — Quickshell cannot infer the owner — and `FileBrowserModal`
  spells the same property `parentModal`. `check-windows.py` fails when an instance forgets it.
  **And every one of them needs `onClosed: hide()`.** Closing a window from the compositor (super+q, a
  close button) only emits `closed`; `visible` stays true, the window is unmapped but still "visible"
  to QML, and the next `show()` brings back a window the user had closed — an MCP editor closed that
  way came back together with the settings window it belongs to.
- **A dialog behind a `Loader` is not listening until it is active.** Search and tasks are built on
  first open, and so are the permission and question dialogs — those two are not click-driven, so
  `ChatWindow`'s own `Connections` activates their loaders from `Backend`, plus once on window creation
  for a request that arrived while the window was closed. Both then open themselves in
  `Component.onCompleted`, which only works because `Backend` appends to the request list *before* it
  emits. A third daemon-driven dialog needs the same three cases or it silently never appears.
- **A background start has to announce its own ending.** The `mcp.save` reply can only say
  `starting: true`, so `mcp.Manager.OnChange` → `mcp.changed` is what turns the settings row into
  "Running · N tools" (or into the reason it failed). Before it existed the row said "Starting…"
  until the page happened to be rebuilt by a tab switch. `Restart` deliberately does not go through
  `Sync`: Sync starts a server whose launch spec *changed*, and a retry is the same spec, so the
  retry button only ever stopped the server.
- **A singleton that loads from a one-shot signal has to ask on creation too.** `Sessions` is created
  on first use, and its list arrives via `Backend.sessionsStale` — emitted once per handshake.
  A hidden start (login autostart keeps the window in an inactive `Loader`) means nothing reads
  `Sessions` until the user finally opens the window, so that emission goes to nobody and the sidebar
  says "No sessions yet" until the next `session.*` call. `Component.onCompleted` plus a
  `Backend.connected` check is the second entry point; `Skills` and `Sessions` both have it. The same
  shape bites the daemon-driven dialogs above, and `Sessions.loaded` exists so that "not asked yet"
  cannot be drawn as "really empty" — an empty state that lies is how this one survived testing.
- **Never name an `IpcHandler` function `show`.** `qs ipc` has subcommands `show|call|wait|listen|prop`;
  a trailing `show` is eaten by the CLI, which prints the target list and exits 0. The window raiser
  is called `raise` for exactly this reason.
- **The token chip is a session bill, and the daemon owns it.** `Session.TotalUsage` accumulates what
  the provider reported for every request — each tool step of a turn, plus the compaction-summary and
  title requests — and only ever grows; editing, truncating or compacting does not refund it. Events
  carry it as an absolute `sessionUsage`, so the UI assigns and never adds: the same total arrives on
  every step and again on `run.finished`, and a listener that summed deltas would double it. It is not
  "how full the context is" (that is `lastUsage.input`, one request) and it is not an estimate.
  Sessions written before the field existed have it recovered from their messages on load.
- **A reply is several TextEdits, and only one may show a selection.** Prose is grouped into
  one TextEdit per stretch (a selection inside a stretch is Qt's own and crosses paragraphs,
  headings, lists and tables), and a code card is another. Qt scopes selection to the editor it
  happens in, so without help two paragraphs would both stay lit — reading as one selection when it
  was two. Every selectable block reports to `Services/Selection.qml`; the newest claim deselects
  the previous owner. `Selection._clearing` exists because `deselect()` moves that editor's cursor,
  which fires the very signals that ask to claim, and the block that just lost the selection would
  take it straight back. Claims key off `selectedText` and `activeFocus`, never `cursorPosition`: a
  streaming reply rewrites its own text and the cursor moves with it, which would pull the selection
  away from whatever the user was reading further up the transcript. A table is part of the run
  rather than a widget for the same reason: it is text, so a selection crosses it. Qt's rich text
  draws a table grid only from the *table's* `border` plus `border-collapse` — a border on a cell
  paints nothing, there is no per-side border, and a cell background is inset by `cellpadding`.
  Hence the bold header instead of a shaded one.
- **Markdown is one document, so structure has to be HTML — and only what Qt draws counts.** Measured,
  not guessed: `<br/>`, `<ol start>`, nested `<ul>`, `<sub>`, `<sup>`, `<u>`, `<s>` and a block-level
  `background-color` work; a `border` on a block element paints **nothing** (a quote is a tint, not a
  bar) and a table cell's background covers only its **first line**. Check such a claim offscreen and
  read pixels. A fence inside a quote is a monospace `<pre>` in place, not a card — a card is a widget,
  and the quote holding together matters more than its highlight. Task boxes are ☑/☐ glyphs, footnote
  markers are not links, an image alone in its paragraph becomes a card (`ImageBlock`), while one
  inline in a sentence stays `[image: alt]` — splitting a paragraph around a widget would break the
  selection Qt gives us across a prose run.
- **Trailing whitespace is a hard break, so a paragraph line loses its indent and nothing else.**
  `replace(/^[ \t]+/, "")`, never `trim()`: two trailing spaces mean `<br/>`. Same for `/\\$/`. And a
  blank-line branch that jumps the index must sit in a `while`, not a `for` — the `continue` re-applies
  the `i++` and skips the line it just moved to (a footnote's second paragraph went missing that way).
- **Inline placeholders nest, so restoring them takes a loop.** A code span inside a link label is a
  token inside a token; one pass leaves `\u0000N\u0000` in the document. Each value refers only to
  tokens created before it, so repeating terminates. A link label also needs the emphasis pass before
  it is held, or `[**bold**](url)` draws its asterisks.
- **A background inside `<pre>` does not stay on its line, so a painted line gets its own block.**
  Qt paints a span's background across the line break: a diff card's red run coloured the *tail of the
  next line*. Closing the span before the newline does not help — the newline carries the paint — so
  `highlight.renderLines` gives every line with a background its own `<pre>`. Invariant, asserted by a
  test: **no `<span>` carries a background**; the colour moves to the block (as its text colour), or a
  nested span without one would be unreadable. And `chroma.Colour.String()` prints `#-00001` for an
  unset colour, so check `IsSet()` first.
- **Inline placeholders nest, so restoring them takes a loop.** A code span inside a link label is a
  token inside a token; one pass leaves a literal `\u0000N\u0000` in the document. Each value only
  refers to tokens created before it, so repeating terminates. A link label also needs the emphasis
  pass applied before it is held, or `[**bold**](url)` draws its asterisks.
- **A Theme token that does not exist fails at run time, silently.** An invented
  `Theme.secondaryContainerText` passed every gate (`check-upstream.sh` compares the *contract*, never
  our own names) and arrived as `Unable to assign [undefined] to QColor`.
  `tools/check-theme-tokens.py` resolves every `Theme.x` (it found two).
- **A row appended from one ListModel to another copies nothing.** `archived.append(messages.get(i))`
  gave rows with no `rawText`: `get()` returns an element. Copy roles (`Chat._rowCopy`).
- **A model question can be put aside, and only another question brings it back.** The dialog is modal
  and the daemon is blocked inside the tool call until it gets an answer, so "hide" cannot mean
  "dismiss" — that is Skip, and it changes the conversation. `QuestionDialog.minimize()` closes the
  window and sets `Chat.questionHidden`; the way back is a button **inside the ask_user tool card**,
  outside `toolExpanded` (a folded card would hide the only route back to a dialog the daemon is still
  blocked on) and not in the composer (a half-screen tiling layout makes that row the scarcest space).
  The dialog owns itself; a *new* question clears the flag, and `Chat._settle` marks every tool of a
  finished run not running, or a turn cancelled at the question leaves the card spinning.
- **Every turn belongs to the daemon, so leaving a session detaches from it — never cancels it.**
  Only the streaming state ever lived in the QML process, so a hot reload (or a fresh `dcode ui`) left
  a composer saying "send" while text was still arriving, because every event was dropped at the
  `Agent.localRunId` guard. `session.get` reports `busy`, and that is the signal to adopt:
  `Chat._adoptRunningTurn` asks `Agent.adopt`, which takes the runId from the first event (the daemon
  does not hand it out) and opens one streaming row for the text still to come.
  `Chat.loadSession` calls `Agent.detach()` — **no `chat.cancel`** — and not only for turns started
  elsewhere: switching away from a reply this window started used to cancel it, so opening another
  conversation threw away the answer being read. Stopping a turn is the stop button's job. Coming back
  re-adopts (the daemon is still writing); a stop before any event named a runId goes through
  `chat.cancelSession`, since there is no id to cancel; and because "busy" is a snapshot, a turn that
  ends in between would spin forever, so one `session.get` 1.5s later settles it.
- **A running turn's age comes from the daemon, and it is per session.** The sidebar pill counted
  with one global `busySince` that only followed the *active* session, so looking at another
  conversation froze the number and coming back restarted it at 0s. `Runner.StartedAt` reports the
  run's start (in `run.started`, `session.list` and `session.get`) and `Sessions.runStarts` maps
  session → start; `elapsedFor(id)` reads a `clockTick` it can be re-evaluated by, so every busy row
  counts on its own. Merged with `Math.min`, never rebuilt: a list reply can be older than the
  `run.started` event it races, and taking the start back is exactly the "restart at 1s" bug. The
  per-message badge uses the same number when a turn is adopted, instead of pretending the turn
  began when the window noticed.
- **The step in flight is not in the transcript, so `session.get` carries it.** "Only finished content
  is written" left a returning window with the user message and an empty bubble until
  `run.finished`. `Runner.RunningText` holds the current *attempt*'s text and thinking (reset by
  `beginAttempt`, or a retry hands the same sentence out twice) and `session.get` returns it as
  `runningText` / `runningReasoning`; `_adoptRunningTurn` seeds the bubble with it. It comes from the
  daemon rather than a UI cache on purpose: persisted rows end at the last finished step, so a
  session-keyed cache would duplicate the prefix whenever the turn passed a tool step while away.
- **A transcript whose `tool_calls` do not pair is permanently unsendable.** Both APIs reject a request
  where an assistant message's `tool_calls` are not followed immediately
  by one `tool` message per id, and the rejection is not retried: a stored transcript with a gap fails
  every later turn. `historyMessages` therefore *pairs while building* — a call with no result gets
  `toolInterrupted`, a result whose call is not waiting gets dropped, empty output becomes
  `toolNoOutput` — and it does this without writing to the file, because the transcript is history.
  `Runner.stopSession` is the other half: `Send`, `Regenerate`, `session.truncate` and
  `session.rewrite` stop the session's running turn and **wait** for it to stop writing before they
  touch the transcript, because a turn persists its assistant message first and each result as the
  tool returns. A new user message written inside that window lands between the calls and their
  results — that is how a session becomes permanently unsendable. It refuses (rather than writing
  anyway) if the turn will not stop within `runStopTimeout`.
- **Chunked insertion resumes where the chunk ended, not where it started.** `_insertRows` appends
  `_loadChunk` rows and leaves `_pendingFrom` at the *end* of that chunk; `_finishRows` appends from
  `_pendingFrom` to the end, so leaving it at the old value made the final chunk land twice — every load
  duplicated up to twelve rows of its transcript. The same rule covers the other half: two loads of one
  session (a reload on connect racing a click) both pass the `id === activeId` check, so the callback
  also compares `_loadToken` or the transcript is installed twice.
- **Loading a session is per-row insertion, and it has to be spread out.** Measured on 940 rows: the
  daemon read them in 141ms, building them took 17ms, and making them the list's model took **8.5
  seconds** blocked end to end — long enough for Hyprland to overlay "application not responding".
  `Chat._loadChunk` rows go in per turn of the event loop with `Qt.callLater`: the cost per row does not
  change, the window answers pings in between. `MessageList` must not `stickToEnd` while
  `Chat.loading` (a layout pass and a fresh delegate per row) and scrolls twice on `rowsLoaded`, because
  the first jump reveals rows never measured. Anything writing to `messages` mid-load calls
  `Chat._finishRows()` first. Still open for the *unfolded* part: paging.
- **A model reset is not the accelerator it looks like.** 1,121 rows into a detached model then
  assigned: 119ms; appended with no view attached: 10ms; identical `contentHeight` once pointed at
  them. The cost is the view's layout-and-create-a-delegate per row, which is why chunked insertion
  plus a suppressed follow is the fix. An earlier note claimed 40x and a different height; that
  compared two different row sets.
- **`0` is a real value** for `maxOutputTokens`, `maxToolSteps` and `compactAt` — it means "off".
  Do not reintroduce `<= 0 → default`; the migration pins v2 zeros instead. The output cap's default
  is 0: a cap set too low truncates replies where the user cannot see why.
- **A cap chosen for a big model has to be lowered for a small one, never sent as-is.** Providers
  reject `max_tokens` above the model's maximum with a 400, so `Config.OutputCap` returns
  `min(user, model.MaxOutput)` (0 stays 0 — "off" is not replaced by a guess). The ceiling comes from
  the provider's own model list when it states one (OpenRouter, DeepSeek and Gemini all do, in three
  different spellings — see `parseModels`), from `ModelLimits` for the families whose numbers are known
  for sure, and from `maxOutput` / `contextWindow` in config.json when the user sets it. The system
  prompt is told the effective number, not the setting, or the model budgets for a reply it will never
  be allowed to finish.
- **A reply with no text is not a reply.** Models do stop silently — most often right after a tool
  result, when the work is done and they write no summary. The turn is technically a success, so
  nothing fails; the user sees a green tool card and an empty bubble, which reads as a crash. Left
  alone it was also *stored*: an assistant message with `text: ""`, which `store.History` then filtered
  out of later requests (accidentally harmless) and the UI drew as a blank row. `execute` now treats
  silence as its own outcome: one retry with `emptyReplyNote` appended (tools still in hand, because
  the model may have stopped mid-task rather than at the end), and if that is silent too,
  `failEmptyReply` reports it and stores nothing. An empty reply that also hit the output cap is not
  retried — the budget went to thinking, so the message names the cap and the reasoning depth instead.
  The note is a parameter of `send`, not shared state: a note that outlives its step silently rewrites
  every later request in the turn.
- **A 400 that names the model's output ceiling is learned, not just shown.** `llm.OutputCeilingOf`
  parses the number out of the provider's own words (DeepSeek's `valid range … [1, N]`, OpenAI's
  `supports at most N completion tokens`, Anthropic's `max_tokens: X > N, …`), `Runner.recordOutputCeiling`
  writes it to that model's `maxOutput` in config.json, and the step is sent **once** more with it — the
  user's cap is satisfied by `min(chosen, ceiling)` rather than failing the turn. Only 400/422 are read,
  and every pattern names the output parameter: a context overflow is the same shape of 400 carrying the
  user's own numbers, and learning one of those as a ceiling would write a wrong limit that outlives the
  mistake. The write publishes `config.changed` on the agent topic so the settings row stops stating the
  old number.
- **The compaction threshold is a ceiling to stay under, not a promise.** `Config.CompactThreshold`
  lowers a configured `compactAt` to 80% of the model's context window: a threshold above what the model
  can read never fires in time, and the overflow that follows is a 400 that is not retried. The daemon
  publishes the lowered value as `effectiveCompactAt` so the settings row can say what will really
  happen instead of repeating the rule in QML.
- **The compacted prefix is folded, not rendered.** It is what the model no longer reads and most of a
  long session's rows: a 460-message session went from 460 rows to 60. `Chat.loadSession` builds rows
  from the cut; the card's second tab builds the rest on demand in `_loadChunk` chunks (400 rows:
  ~600 ms) in its own bounded list, so they stay virtualized. The card is not a row of its own — it
  rides on the first row still sent verbatim, so edit/regenerate addressing and the model's indices did
  not move. `_locateCompaction` falls back to row 0 only when the id is not found *and* the prefix is
  folded; checked first it pinned every later compaction to row 0. `_foldAboveSeam` folds a compaction
  that fires mid-session (one `messages.remove(0, k)`) and moves `liveIndex`/`pendingUserRow` with it. A
  search hit inside the fold sets `Chat.archiveJump`. The archive must render **tool rows**: 302 of 371
  archived rows in a real session were calls, so a count-only delegate left bare labels. **A live fold is invisible without help**: the
  seam becomes row 0, ~70k px above a view that follows the reply, so the toast ("Folded N earlier
  messages", action → `Chat.seamRevealRequested`) is the only thing that says so — and reveal it with
  `scrollToTop`, not `jumpRequested(0)`, which ListView centres below zero. A fold is not a reload, so
  the list keeps a stale `contentY`: hence `Chat.seamFoldStarting`/`seamFolded`.
- **"The summary is empty" is a reasoning model out of budget, not a broken request.** `summarize` sent
  `MaxTokens: 900`, which a reasoning model can spend entirely on thinking and then emit no content —
  seen on deepseek-flash, where the turn then skipped compaction without saying so. The budget now
  covers both (3000), the nominal depth is `off`/`low` rather than the turn's own, and an empty answer
  is retried once. `count` is how many messages the
  *current* summary stands in for — one summary is in play; a later compaction recomputes the cut
  against the whole history.
- **Compaction measures the request, not the transcript.** `maybeCompact` compares
  `sentCost(hist, from, record)` against the threshold. Measuring `hist` instead looks equivalent and is not: the stored transcript never shrinks,
  so a session that crossed the threshold once would re-summarize on **every** later turn, paying for a
  summary identical to the one it already has. `summaryMessage` builds the sent text
  once for both the request and the measurement — two spellings of it is how the threshold comes to
  describe a request that is never made.
- Anthropic requires `max_tokens`, so "off" still sends 4096 there (a real ceiling arrives as its own
  number); the other clients omit the field.
- **A silent stream is a dead stream, and only `openStream` notices.** `http.Client.Timeout` looks
  like the way to bound a reply and is the wrong tool: it caps the whole exchange, so a healthy
  thirty-minute stream dies at the limit while a stalled one is bounded only in theory. Every client
  goes through `openStream`, which arms a watchdog fed by each byte that arrives (SSE keep-alive
  comments included) and cancels the request after `streamIdleTimeout` of silence, so the blocked
  read returns and `streamErr` reports `ErrStreamStalled` — which is retryable and visibly different
  from the user pressing stop. Reading `resp.Body` directly instead of the returned guard bypasses
  the feed and brings the hang back.
- **The font source is three options, not two.** `auto` follows Dux Shell's `settings.json`, `system`
  uses Qt's own font, `custom` uses the families picked in settings. All three exist in
  dux-calendar and dux-files, and `FontSource` has always supported `"system"` — dropping it here
  meant also deleting the `"system" → "auto"` migration, since a value the picker cannot show would
  otherwise be silently rewritten on every start. Under `system` the three family dropdowns are
  hidden: `FontSource` maps it to `Fonts.system` / `Fonts.systemMono` / the bundled DM Serif, so a
  chosen family would be a control with no effect. Weight and scale still apply in all three modes.
- **The appearance page changes things, not just settings.** `density` → `Theme.densityLevel` →
  `SettingsMetrics` (and `Style`, which reads the same level for control heights);
  `floatingWindowTransparency` → `Theme.windowSurface`; `blurEnabled` → `Theme.blurLayersActive` →
  `WindowBlur`. A new window must use `Theme.windowSurface` and carry a `WindowBlur`, or those two
  settings silently do nothing in it. `blurLayersActive` has no probe process — without the compositor
  protocol it is a silent no-op — so the toggle is what gates it, and the description says so.
- **A translated term is keyed by its English text, not by its context.** `poexports/<locale>.json` is a
  flat `{english: translation}` map, so the context in `I18n.tr("Scale", "…")` documents intent for
  translators and nothing more: two rows reusing a word share one translation. Usually fine (`Off`,
  `On`, `Remove`), wrong only where the two places must read differently — which is why the appearance
  page says "Interface scale" and "Font scale" rather than "Scale" twice. Word them differently; do
  not add context support to the loader.
- **Settings search lives in the sidebar.** The sidebar owns the text, `SettingsContent.searchQuery`
  binds to it, and a property bound from a parent **cannot be assigned from the child** — so the result
  list asks for a page (`pageRequested`) and for the field to be emptied (`searchDismissed`) instead of
  assigning `currentIndex` or `searchQuery`, which silently does nothing.
- **The settings search reads the widget tree, and only works because of `visible` semantics.** There is
  no index: `_collectRows` walks the live rows per keystroke, affordable because a page is a few dozen
  widgets. Two things are easy to undo by accident: pages are kept **visible** during a search and moved
  off-screen inside a clipping parent (hiding them makes `child.visible` read false all the way down, so
  the walk finds nothing), and a Loader that just became active has no widgets yet, so `_rebuildSearch`
  re-asks itself (bounded) and waits for `I18n.translationsLoaded`. Rows are translated by then, so
  `I18n.englishFor()` is what lets an English query match a Chinese UI; `keywords` on `SettingsRow`
  covers wording nobody would type ("frosted" for background blur).
- Settings dropdowns must keep values outside their list visible, or the next click silently
  overwrites a hand-edited config.
- `Backend.qml:_applyConfig` must not filter falsy values (`> 0`); 0 is meaningful now.
- Tool output is truncated per message (32 KB / 2000 lines, head + tail) before it reaches the model.
  That is not the context limit and does not trigger compaction.
- `sync-shell` uses `tar -h` on purpose: `DCommon` is a symlink and `go:embed` does not follow
  symlinks. Dropping `-h` produces a blank window with no build error.
- Use `Theme.*` tokens only. `check-upstream.sh` verifies every token in both directions; hard-coded
  colours fail review.
- Retries cover 429, 5xx and broken streams. A 400 is never retried — a context overflow must
  surface, not loop.
- Tests assert exact user-facing strings. When you reword a message, update the test that checks it
  in the same commit.

## Conventions

- **Comments**: a file header saying what the file is; `// --- section ---` dividers; one-liners that
  state a contract or a platform quirk. No history narration ("we used to…"), no restating the code.
- **Go**: exported symbols have doc comments that say *why*. Errors are lowercase English sentences
  without a trailing period, wrapped with `%w`.
- **Tests**: table-driven, next to the code, fakes over network. Anything that touches the internet
  lives in `*_live_test.go` behind `DUXCODE_LIVE=1`.
- **Commits**: English, imperative, Conventional Commits — `type(scope): subject`, e.g.
  `fix(ipc): report scanner errors when the client disconnects`. Types: `feat fix perf refactor docs
  test build ci chore`. Scope names the area (`ui`, `ipc`, `agent`, `tools`, `providers`, `tasks`,
  `translations`, `build`). Subject under 72 characters, no trailing period; the body explains *why*,
  wrapped at 72. The type is not decoration: `gh release create --generate-notes` and any changelog
  tooling read it. One concern per commit; build and tests green before committing.
- **Version and tags**: only `vX.Y.Z` tags reach the binary — `scripts/version.sh` matches `v[0-9]*`,
  so a marker tag such as `ui-backup-…` can never leak into Settings → About. A build at the tag reports
  the tag alone (`2.1.2`); `n` commits past it, `2.1.2+gitn.<hash>`; either with uncommitted changes,
  `-dirty` on the end. So tag the release before `make release`, and bump the tag rather than the number
  in a file. `tools/test-version.sh` pins the shapes (`make check` runs it) — the count is the distance
  from the tag, not the size of the repository. Pushing the tag is the
  release: `.github/workflows/release.yml` builds amd64 and arm64 and attaches them to the GitHub
  release, so nothing is uploaded by hand.
- **UI text**: `I18n.tr(term, context)`. The term is English, the context is a short note for
  translators; both go into `translations/template.json`.

## Where to read more

- `docs/go-daemon.md` — daemon design: transport, storage, sandbox, config rules, tasks (English)
- `UPSTREAM.md` — the component library, Theme token contract, what we deliberately diverge on
- `translations/README.md` — extraction, coverage, and the component-library override mechanism
