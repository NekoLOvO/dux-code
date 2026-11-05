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
   translation data (`translations/poexports/`, `translations/overrides/`).
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
| `tools/check-upstream.sh` | Theme + submodule contract check |
| `tools/check-windows.py` | every `FloatingWindow` instance names a parent window |
| `docs/`, `UPSTREAM.md` | design notes (English) |

## Commands

```bash
make dev                 # build without the embedded UI
make run                 # build + upstream check + `dcode ui -c .`   ← normal dev loop
make test vet fmt
make check               # submodule, Theme contract, window parents; green before committing UI work
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
  learned output ceiling).
- **State on disk** — `config.json` under `$XDG_CONFIG_HOME/dcode/`; sessions and `tasks.json` under
  `$XDG_DATA_HOME/dcode/`; the extracted UI under `~/.cache/dcode/ui/<version>`.
- **One instance** — `dcode ui` starts the daemon, then *raises* an existing window instead of
  opening a second one. `make hot` (i.e. `DUXCODE_ENABLE_HOTRELOAD=1`) is the exception, because
  that flow needs a fresh shell on the tree being edited.
- **Run loop** — a turn is up to N model calls ("steps"), with tools in between. The final step is a
  wrap-up with no tools, so the model can say what it did instead of dying mid-loop.
  `chat.cancel` aborts through the context.
- **Compaction** — runs once at the start of a turn, never mid-loop. It measures the history *about to
  be sent* — the summary plus the messages after the cut, not the stored transcript — cuts on a `user`
  boundary (never splitting `tool_calls` from `tool_result`), and replaces the dropped part with a
  summary. The stored transcript is untouched; only the copy sent to the model changes.
- **Config versioning** — `version` in `config.json` is not decoration. Bump it only together with a
  migration in `config.FixDefaults`, or old files silently change meaning (that is how `0` became a
  legal value for the three limits).

## Landmines

- **Every modal window needs `parentWindow`.** A `FloatingWindow` with no parent is a plain
  xdg_toplevel, so a tiling compositor tiles it edge to edge instead of floating it over the window
  that opened it. There is no default — Quickshell cannot infer the owner — and `FileBrowserModal`
  spells the same property `parentModal`. `check-windows.py` fails when an instance forgets it.
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
- **A model question can be put aside, and only another question brings it back.** The dialog is modal
  and the daemon is blocked inside the tool call until it gets an answer, so "hide" cannot mean
  "dismiss" — that is what Skip does, and it changes the conversation. `QuestionDialog.minimize()`
  closes the window and sets `Chat.questionHidden`; the question stays pending. The way back is a
  button **inside the ask_user tool card**, below its body and deliberately outside `toolExpanded`:
  that body follows the display setting, and a folded card would hide the only route back to a dialog
  the daemon is still blocked on. It is not in the composer because a half-screen tiling layout makes
  that row the scarcest space in the window. No id plumbing is needed to find the right card — the
  daemon publishes `tool.finished` only after the answer, so the blocked call stays `running`. The
  dialog keeps owning itself (it listens for `Chat.questionRestoreRequested` the way it already
  listens to `Backend`), so nothing has to reach into it. A *new* question clears the flag, or a
  leftover from the previous one would hide a question the user has never seen. The button also asks
  that a question is still pending, and `Chat._settle` marks every tool of a finished run not running:
  a turn cancelled at the question, or a question voided with the connection, would otherwise leave
  the card spinning and offer a way back to a dialog nothing is waiting on. Esc is still Skip —
  different decisions, and the buttons say so.
- **A window that loads mid-reply adopts the turn instead of ignoring it.** The daemon runs the turn;
  only the streaming state lived in the QML process, so a hot reload (or a fresh `dcode ui`) left a
  composer saying "send" while text was still arriving, because every event was dropped at the
  `Agent.localRunId` guard. `session.get` already reports `busy`, and that is the signal to adopt:
  `Chat._adoptRunningTurn` asks `Agent.adopt`, which takes the runId from the first event (the daemon
  does not hand it out) and opens one streaming row for the text still to come. What that row cannot
  show is the prefix already written before the window loaded — it arrives with `run.finished`, which
  carries the persisted text. Three consequences worth keeping: an adopted turn is the **daemon's**, so
  leaving the session releases it (`Agent.release`) rather than cancelling — otherwise switching away
  would kill a reply the user is waiting for; a stop before any event has named a runId goes through
  `chat.cancelSession`, since there is no id to cancel; and because "busy" is a snapshot, a turn that
  ends in between would leave a spinner forever, so one `session.get` 1.5s later settles it.

  Both APIs reject a request where an assistant message's `tool_calls` are not followed immediately
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
- **Loading a session is per-row insertion, and it has to be spread out.** Measured on a
  1,900-message session (940 rows): the daemon reads it in 141ms and the row builder takes 17ms, but
  making those rows the list's model took **8.5 seconds**, blocked end to end — long enough for
  Hyprland to put "application not responding" over the window. An attempt to fix the responsiveness
  instead of the total: insert `Chat._loadChunk` rows, hand the event loop back with `Qt.callLater`,
  repeat. The cost per row does not change, but the window answers pings between chunks. Two things
  ride on the same flag and matter as much: `Modules/MessageList.qml` must not `stickToEnd` while
  `Chat.loading` (that is a layout pass and a fresh delegate per row), and it scrolls once on
  `rowsLoaded`, twice, because the first jump reveals rows that were never measured. Anything that
  writes to `messages` mid-load has to call `Chat._finishRows()` first (see `Chat.send`), or the rows
  still queued land underneath what was just written.
  Still open: the total is seconds on a very long session, so the real answer is paging (render the
  last N rows, load more at the top). That is a feature, not a tweak — it has to handle the scroll
  anchor, search jumps into unloaded rows, edit/regenerate addressing by message id, and a compaction
  seam outside the window.
- **A model reset is not the accelerator it looks like.** Inserting 1,121 rows into a detached model
  then assigning it (`Chat.messages = fresh`) measured 119ms; appending the same rows with no view
  attached measured 10ms; and both gave an identical `contentHeight` (474160) once the view was
  pointed at them. The earlier note here claimed a reset was 40x faster and produced a different
  height — that comparison was between two different row sets, and the height difference was an
  artifact of the harness, not of the model. The cost was never in the model: it is the view's
  layout-and-create-a-delegate per inserted row, which is why the chunked insert plus the suppressed
  follow is what fixed the freeze. Do not reach for a reset as a shortcut; measure both paths on the
  same array first.
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
- **A compaction is a seam in the transcript, not a chip in the composer.** The stored transcript keeps
  every message, so the UI shows where the summary takes over: `context.compacted` carries `upToId`,
  `count` and `summary`, `Chat.compactionRow` finds the row by message id (rows merge tool results and
  drop empty assistant messages, so an index would not survive), and `MessageDelegate` draws
  `CompactionDivider` above that row. The seam lives inside the row rather than as a row of its own, so
  edit/regenerate addressing, search jumps and the model's indices never move when a summary appears.
  `count` is how many messages the *current* summary stands in for — one summary is in play at a time,
  and a later compaction recomputes the cut against the whole history — not a sum over every compaction.
  `Store.dropStaleCompaction` forgets the record when editing or regenerating removes its cut point;
  a record whose boundary is gone is a summary and a count describing nothing.
- **Compaction measures the request, not the transcript.** `maybeCompact` compares
  `sentCost(hist, from, record)` — the summary message plus the messages after the cut — against the
  threshold. Measuring `hist` instead looks equivalent and is not: the stored transcript never shrinks,
  so a session that crossed the threshold once would re-summarize on **every** later turn, paying for a
  summary identical to the one it already has. That was the behaviour until it was measured; the 40%
  keep ratio only means anything if the tail is what is judged. `summaryMessage` builds the sent text
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
- **The appearance page changes things, not just settings.** Three rows there are only worth having
  because the plumbing reaches the windows: `density` → `Theme.densityLevel` → `SettingsMetrics` (and
  `Style`, which reads the same level for control heights); `floatingWindowTransparency` →
  `Theme.windowSurface`, which every `FloatingWindow` paints with and sets `surfaceFormat.opaque:
  false` for; and `blurEnabled` → `Theme.blurLayersActive` → `WindowBlur`. A new window must use
  `Theme.windowSurface` and carry a `WindowBlur`, or the opacity and blur settings silently do nothing
  in it. `blurLayersActive` has no probe process here — without the compositor protocol it is a silent
  no-op, so the toggle is what gates it and the description says so.
- **A translated term is keyed by its English text, not by its context.** `translations/poexports/<locale>.json`
  is a flat `{english: translation}` map (`write_locale`), so the context in `I18n.tr("Scale", "…")` is
  documentation for translators and nothing more. Two rows that reuse the same English word share one
  translation — which is fine, and saves work, when they mean the same thing (`Off`, `On`, `Remove`,
  `Theme`), and wrong only when the two places need to read differently. That is why the appearance
  page says "Interface scale" and "Font scale" instead of "Scale" twice: with one shared slot, both
  would have rendered with the same word. If two places genuinely need different translations, word them
  differently; do not add context support to the loader for it.
- **The settings search lives in the sidebar, not in the page.** The field is the first child of
  `SettingsSidebar`'s nav column (above the tabs, like dux-files), and the sidebar owns the text;
  `SettingsContent.searchQuery` is bound to it, and the content pane renders the hits as a
  `SettingsPage` over the pages with the header reading "Search results". Per-page hit counts show on
  the sidebar tabs. Two consequences of that ownership: a property bound from a parent **cannot be
  assigned from the child**, so the result list asks for a page (`pageRequested`) and for the field to
  be emptied (`searchDismissed`) rather than setting `currentIndex` or `searchQuery` itself — doing it
  the direct way silently does nothing and leaving a query in the box while the page under it changes.
- **The settings search reads the widget tree, and only works because of `visible` semantics.** There is
  no index to keep in sync: `_collectRows` walks the live rows on every keystroke, which is affordable
  because a settings page is a few dozen widgets. Two consequences that are easy to undo by accident:
  (1) pages are kept **visible** during a search and moved off-screen inside a clipping parent — hiding
  them makes `child.visible` read false all the way down, and the walk then finds nothing; (2) a Loader
  that only just became active has no widgets yet, so `_rebuildSearch` re-asks itself (bounded) instead
  of relying on a signal, and it also waits for `I18n.translationsLoaded`. Rows are already translated
  at that point, so `I18n.englishFor()` (the reverse map built from the poexport) is what lets an
  English query match a Chinese UI. `keywords` on `SettingsRow` covers the rest — only where the
  on-screen wording is not what someone would type ("frosted" for background blur); the title, its
  card and its page name are indexed anyway.
- Settings dropdowns must keep values that are outside their list visible — otherwise the next click
  silently overwrites a hand-edited config.
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
- **Version and tags**: only `vX.Y.Z` tags reach the binary — `core/Makefile` matches `v[0-9]*`, so a
  marker tag such as `ui-backup-…` can never leak into Settings → About. A build reports
  `<tag>+git<count>.<hash>`; tag the release before `make release`, and bump the tag rather than the
  number in a file. Pushing the tag is the release: `.github/workflows/release.yml` builds amd64 and
  arm64 and attaches them to the GitHub release, so nothing is uploaded by hand.
- **UI text**: `I18n.tr(term, context)`. The term is English, the context is a short note for
  translators; both go into `translations/template.json`.

## Where to read more

- `docs/go-daemon.md` — daemon design: transport, storage, sandbox, config rules, tasks (English)
- `UPSTREAM.md` — the component library, Theme token contract, what we deliberately diverge on
- `translations/README.md` — extraction, coverage, and the component-library override mechanism
