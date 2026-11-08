# Go daemon design

`dcode` is a separate process; the UI is only its display layer. This document is about *why* things
are the way they are. The authoritative API is in the code — `core/internal/api/router.go` for the
method list, `core/internal/agent/` for the events — and copying a table here would only make it stale.

## Why a daemon

ACP was a dead end: it needs an existing agent to reach an LLM. The goal here is for Dux Code to *be*
the agent — call the API itself, run the conversation loop itself. Given that, the backend should be a
separate Go process:

- a Quickshell restart does not lose the conversation (the UI is only a display layer)
- the CLI (`dcode send`) sees the same state as the window
- when tools arrive (read a file, run a command), the permission boundary has an obvious home: the daemon

## Transport

A Unix socket at `$XDG_RUNTIME_DIR/dcode/daemon.sock` (`DUXCODE_SOCKET` overrides it).
**Fixed path, no pid** — the UI has to reconnect to the same path after a daemon restart, and guessing a
pid every time makes the reconnect logic dirty.

One JSON object per line:

```
on connect, server sends {"apiVersion":1,"capabilities":[...],"version":"...","appName":"dcode"}
request                   {"id":3,"method":"session.list","params":{}}
response                  {"id":3,"result":{...}}      or {"id":3,"error":"..."}
event                     {"event":"agent","data":{...}}
reverse request           {"rid":5,"method":"permission.request","params":{...}}
reverse response          {"rid":5,"result":{...}}
```

`id` and `rid` are two independent sequences: the first is client-initiated, the second daemon-initiated.
That gives bidirectional request/response on one connection — which is how tool approval reaches the UI —
without opening a second socket.

The shape matches the rest of the dux family (dux-files / dux-calendar), so the QML side reuses `DSocket`
as-is (exponential-backoff reconnect, a fresh Socket per reconnect).

**Events are not queued and not replayed**: a late connection has to call `session.get` to catch up.

`delta` is coalesced in the daemon every 50 ms (`agent.flushInterval`). Without it a long answer turns
into thousands of IPC lines.

## Storage

`$XDG_DATA_HOME/dcode/sessions/<id>.json`, one file per session.

There is one rule: **only finished content is written**.

- user messages: written as they are sent (a daemon crash must not lose something the user asked)
- assistant messages: written on `done`, or on `cancelled` when content was produced
- anything mid-stream stays in memory

No SQLite, on purpose: sessions are hundreds, not millions; JSON can be opened in any editor, diffed, and
one corrupt file does not take the rest down (unreadable files are skipped with a warning).

The title comes from the first user message (`store.deriveTitle`, 42 runes, truncated).

Every one of these files is replaced, never truncated in place: `core/internal/atomicfile` writes a
temporary in the same directory, syncs it, renames it over the target and syncs the directory, so a crash
mid-write leaves the previous file intact and a power cut cannot lose the new one. That matters most for
`tasks.json` and `config.json` — one file holds every task, and the config holds every provider and key.

The two syncs are not decoration. Renaming publishes a name, not bytes: without the first, f2fs can
checkpoint an inode whose size and block count are complete while the blocks themselves are still
unwritten, and the file comes back with the right length and nothing but NUL bytes in it. A session went
that way — `ajyb8m095o.json` returned at 6048209 bytes of zeros, reported as binary, skipped on load as
unreadable. The second sync is what makes the *name* survive; the first is what makes the content.

## Tools

The line is "does it have side effects", not "is it important":

- **never asks**: `read_file`, `glob`, `grep`, `now`, `load_skill`
- **asks**: `write_file`, `edit_file`, `bash`, `browser` (only for actions with side effects)
- **depends on settings**: `web_search` / `web_fetch` / `browser` share the "network" switch in the
  composer; `web_search` additionally needs a search backend

File tools are confined to the working directory, and `bash` runs with it as its cwd. Tool results are
truncated before they go back into the conversation (32 KB / 2000 lines) — that is context protection,
not an output setting. Do not file it as "the model cannot see the whole file".

**Edits go through the file tools, and that is a request rather than a rule.** `bash` can write anything
(`sed -i`, redirection, `patch`, `git checkout`, a formatter over the tree), and in `auto` mode it is not
even sandboxed — so nothing can stop it. What it cannot do is *show* the change: `write_file` and
`edit_file` present the edit before it happens, and a shell write leaves only its output. So the system
prompt and the three tool descriptions say which is which, with formatters and code generation an
explicit exception (`gofmt -w` and `make` are the legitimate write-ish use of a shell, and forbidding
them would stop the agent from building what it just changed). Removing the exception means a real
restriction — a read-only workspace bind in `sandboxArgv` — and that breaks `go build -o`, `npm install`
and `make` along with it.

### Sandbox and approval: two layers

`workspace` mode can run things inside the directory without asking because there are two layers, not one:

1. **Judgement** (`guard.go`): paths are resolved and compared against the root; `bash` commands are
   matched literally against `rm -rf /`, `cat ../x`, `curl -o /tmp/x` and the like. It can always be
   fooled — computed paths, `eval`, `curl | sh` are invisible to it.
2. **The kernel** (`sandbox.go`): **only commands approved without asking** are wrapped in `bwrap`, where
   the outside is read-only and writes simply fail. A command that was asked about runs as typed — you
   read it and agreed, and wrapping it would only break your own command.

The boundary: the working directory and `/tmp` are writable, build caches (`~/.cache`, `~/.npm`,
`~/.cargo`, `~/go`, …) are writable because `go build` and `npm install` die on a read-only cache, and
everything else is read-only. **The network is not isolated**: `go mod download`, `npm install` and
`curl` all need it. Blocking exfiltration is a design of its own; `--unshare-net` breaks every build.

`SandboxAvailable()` actually starts one (`bwrap --ro-bind / / --dev /dev true`) instead of reading
`--version`: some distributions disable user namespaces, and a matching version does not prove it runs.
No sandbox means only the first layer exists, and the settings page says so instead of claiming writes
outside the directory are impossible.

### web_fetch or browser

Both reach the network, at very different cost:

| | `web_fetch` | `browser` |
| --- | --- | --- |
| what it does | fetch one HTML document, strip the tags | start a real Chrome |
| JS | no | yes |
| speed | hundreds of ms | seconds (the first call starts a browser) |
| good for | articles, docs, APIs | SPAs, logins, content that only appears after a click |

The tool description opens with "try `web_fetch` first", because choosing wrong costs time and tokens.

`browser` drives **agent-browser** (a compiled binary, not an npm package). It is found in three places,
in this order:

1. `browser.path` (the file picked in Settings → Tools)
2. the `DUXCODE_AGENT_BROWSER` environment variable
3. `agent-browser` on `PATH`

The settings path wins because the user picked it by hand, and a launch environment should not override
it. When it breaks it reports "the path you picked is not executable" and **does not** quietly fall back
to `PATH` — "I thought this was A, but it was B" is a hundred times harder to debug than "it does not
work".

It deliberately does not scan other apps' install directories (LobeHub's
`~/.config/LobeHub/bin/agent-browser/<version>/`, for example): that is someone else's internal layout,
and a version-directory rename breaks it. Pick the path in settings once, or drop a wrapper in
`~/.local/bin/`.

**Changing the path does not need a daemon restart**: the binary is resolved per call
(`Env.BrowserPath`), and "is this tool in the list" is computed per request (`toolAllowed`), so the next
message picks it up. When the binary is missing the tool simply **disappears from the list** (registered
unconditionally, filtered by `toolAllowed`): leaving a "please install me" shell around only makes the
model retry. The settings row says why.

**No separate switch**: it shares the "network" toggle with search and page fetching. Three toggles would
become "I turned networking off, why is it still fetching pages"; one unmanaged path makes the switch a
lie. The toggle stays pressable with no search backend — fetching and the browser do not need one.

Writing our own CDP is not about the protocol; it is about **accessibility tree → a snapshot that fits in
tokens → reusable refs**. A full Chrome a11y tree is thousands of nodes; compressed badly it eats tens of
thousands of tokens, and unstable refs make `click` hit the wrong thing. agent-browser has already done
that work.

Approval granularity:

- `navigate` / `click` / `fill` / `press` ask — they can send data out and change pages
- `snapshot` / `read` / `screenshot` / `scroll` / `back` do not — they change nothing, and asking only
  numbs people
- `fill` shows its content in the dialog: agreeing to an invisible input is signing blind

It uses its own session (`dcode`), does not touch the LobeHub one, and does not keep logins across
conversations.

### Attachments and images

`chat.send` carries `attachments`: absolute paths, recorded on the user message and nothing more. The
daemon decides what they mean at send time, and today it does exactly one thing with them — images.

An attachment is sent as an image when its extension looks like one **and** its header agrees
(`sniffImage`; png, jpeg, gif, webp). Extensions lie, headers do not, and a wrong guess is a 400 that
names neither the file nor the reason. A file that fails either test is skipped with a warning in the
log instead of failing the turn: losing one image beats losing the question.

Images ride on **every step** of the turn that attached them, not only the first: a tool loop re-sends
the request, and a model that lost the picture mid-loop would answer about a file it can no longer see.
Only the **last** user message's images are sent, so the next turn does not pay for them again — the
model forgetting the picture one turn later is the deliberate half of that trade. Caps: 4 images, 8 MB
each; base64 adds a third on top, and a 4K screenshot is thousands of tokens per step.

A 4xx with images attached is usually "this model cannot see images". That is said in the error and
**not** retried without them: silently dropping the picture leaves the model answering a question about
an image the user believes it saw.

The composer owns the pending list (absolute paths, thumbnails for images) and `PasteProbe` turns a
pasted file into an attachment rather than its path — a file manager puts both `text/uri-list` and
`text/plain` on the clipboard, and Quickshell only exposes the latter.

## Config

`$XDG_CONFIG_HOME/dcode/config.json` (0600, `~/.config` when the variable is unset), in the same
directory as the UI's `ui-settings.json` but separate: the first holds providers / models / keys and is
read only by the daemon; the second holds appearance and is read and written by QML directly. Fields
are defined in `core/internal/config/config.go`; these are the rules that do not change.

**Models live under providers**, because "can I use this model" depends on whether its provider has a key.
The global id is `provider:model`, and the UI knows only that string — changing a provider or an endpoint
does not invalidate saved sessions. `enabled` decides whether a model shows up in the model menu; newly
fetched models start disabled so the user chooses.

`reasoning` overrides the built-in reasoning-level table. The levels are our own vocabulary (`none` /
`minimal` / `low` / `medium` / `high` / `xhigh` / `max`), and each client translates them (OpenAI-compatible:
`reasoning_effort`; Anthropic: `thinking:{type:"adaptive"}` plus a top-level `effort`). Unset means use
the table, an empty array means this model does not support reasoning (table off), and a list means use
exactly those.

Gemini has two switches — `thinkingLevel` (`MINIMAL` / `LOW` / `MEDIUM` / `HIGH`) and `thinkingBudget`
(an integer; `0` is off, `-1` dynamic) — and both are used: `none` becomes `thinkingBudget:0` and every
other level becomes `thinkingLevel`, one per request (sending both is a 400). The levels offered:

| family | off | low / medium / high |
| --- | --- | --- |
| 3.x, 4.x, `gemini-flash-latest` | yes | yes |
| any `*-pro-*`, any `*-lite-*` | no | yes |
| 2.x, and the 40-odd non-chat models | — | — |

"off" is refused by exactly the pro and lite families — a pro answers "Budget 0 is invalid. This model
only works in thinking mode", a newer lite answers "invalid argument" — so that is the test, not a
version. 3.1-flash-lite does still accept the budget switch; leaving it off there costs one option on
one model, where being wrong on a pro costs the whole conversation. `MINIMAL` is offered nowhere: it is
a per-model accident in both directions (3-flash and 3.6 accept it, 3.7 and everything newer refuse it).

2.x is left alone: it has no `thinkingLevel` at all, and every level name is answered with a 400. The
rest of Gemini's 61 models are not conversations (embeddings, image generators, TTS, live,
transcription, computer use, robotics) and answer every level with 400. Any of this can be changed per
model with the `reasoning` override. Verified by sending every level the table offers to all twelve
chat models (2026-11): 0 rejections.

**A built-in tool next to function declarations needs `toolConfig`.** `google_search` and the agent's
tools go out as two `tools` entries, which Gemini answers 400 to —

```
Please enable tool_config.include_server_side_tool_invocations to use Built-in tools with Function calling.
```

— unless `toolConfig.includeServerSideToolInvocations` is set, so it is set whenever both are present.
The consequence is visible: the response then carries the search as parts of its own (`toolCall` with
`toolType: GOOGLE_SEARCH_WEB`, then the `toolResponse` with the results). They are not function calls —
Google ran them inside the same request, so there is nobody to answer — and the parser steps over them;
only the text is the answer.

**Gemini rejects any JSON Schema keyword it does not define**, with a 400 that names the key and the
tool — and MCP servers publish whatever they like. GitHub marks the argument that travels as an HTTP
header with `"x-mcp-header"`, which 38 of its 46 tools carry: the whole request dies with
`Unknown name "x-mcp-header" … Cannot find field`, reported to the user as "gemini-flash-latest returns
400" even though nothing is wrong with the model or the message. So a tool schema is filtered before it goes out
(`gemini_schema.go`): `properties` keys are argument names and are kept, while unknown keywords
(`x-*`, `additionalProperties`), tuple `items`, and `"type": [..., "null"]` unions (folded into one
type plus `nullable`) are dropped or rewritten — dropping is the safe direction, since an unknown
keyword fails the request and a missing one only loses a hint.

xAI is an `openai-responses` provider at `https://api.x.ai/v1` — its Responses API takes the same
nested `reasoning:{effort}` and the same `{"type":"web_search"}` server tool, so it needs no client of
its own. The levels come from the model name (see `grokLevels`): `low` / `medium` / `high` / `xhigh`
from Grok 4.6, three levels on 4.5, and nothing earlier because a Grok that always thinks at one
intensity has no level to pick. `none` is never offered there: reasoning cannot be turned off.

DeepSeek is decided by endpoint rather than by model name, for the same reason Gemini's aliases are
named by hand: the ids stopped carrying versions (`deepseek-flash` is v4.1 today, and pro was spelled
`deepseek-v4-pro` before that). The endpoint only serves those two branches and both take every level
from `none` to `max`, so every model on `api.deepseek.com` gets the same four — half of it measured:
`none` really switches the thinking off, while `minimal` / `medium` / `xhigh` are accepted but produce
no different result, which is why they are not offered.

`none` is not the shallowest setting on the same scale. OpenAI-compatible sends `reasoning_effort:"none"`
(measured working against deepseek, whose two branches are flash and pro — the retired reasoner is not
special-cased any more); Gemini sends `thinkingBudget:0`, and only where that switch works; Anthropic
switches to `thinking:{type:"disabled"}` and omits `effort` — there is no `none` value there, and sending
one is a 400.

"Auto" diverges too: for OpenAI / Gemini / xAI it means sending nothing and letting the provider default;
for Anthropic it means sending `thinking:{type:"adaptive"}` explicitly (and no `effort`). The difference
is that Anthropic's default is *not thinking*, so omitting the parameter turns reasoning off there. That
is why the request carries `reasoningAuto` next to `reasoning`: to tell "the user chose auto" apart from
"this model cannot reason at all".

**The summary request asks for the cheapest depth, not the turn's.** Compaction folds the earlier history
with `off` where the model's table offers it and `low` where it does not (`summaryReasoning` in
`context.go`); leaving it at "auto" made a fold of a few thousand messages spend most of its wall clock
thinking about a mechanical rewrite, and the summary request is not the turn's request — it is one
internal call the user never sees. A model with an empty table gets no parameter at all, which covers
every OpenAI-compatible endpoint we do not recognise: sending `reasoning_effort` to a server that has
never heard of it is a 400, and a failed compaction loses the summary. The escape hatch is the per-model
`reasoning` override — declaring `["none"]` there makes the summary ask for it. If a declared level is
refused anyway the chunk is retried without it, because the table is a guess.

With thinking on there are two follow-on rules: **no `temperature`** (it can only be 1, so it is omitted),
and **thinking blocks must be echoed back with their `signature`** inside a tool loop. Streaming collects
`thinking_delta` / `signature_delta` into `Result.ThinkingBlocks`, stores them as `thinking` on the
message, and replays them only for the message that is calling tools; older thinking is left out, since it
would only eat context. Skip this and thinking plus tools is an instant 400.

**Gemini signs its function calls, and the signature has to come back.** A `functionCall` part arrives
with a `thoughtSignature`, and sending that call back without it is a 400:

```
Function call is missing a thought_signature in functionCall parts. ... function call
`default_api:echo`, position 2.
```

It is per call rather than per turn, so it rides on `llm.ToolCall.Signature` and is persisted with the
message — which is what makes a tool loop resumed after a restart work. The API only enforces this for
the *last* model turn, so history written before signatures were captured still replays fine; the one
case it breaks is a session whose run died between the call and its result, which
`geminiDropUnsignedTail` repairs by dropping that exchange (the call and its results go together).

The built-in table can only guess — `/models` does not report this (only OpenRouter returns
`supported_parameters`). So it lists the families we are sure about and leaves the rest empty; guessing
wrong means a 400 the moment someone picks a level, and the conversation dies.

### MCP servers

Two ways to attach a server, depending on whether it runs locally:

```jsonc
"github": {
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-github"],
  "env": { "GITHUB_TOKEN": "..." }
},
"remote": {
  "url": "https://mcp.example.com/mcp",
  // a hosted server that wants a token: headers go out with every request (GitHub's endpoint
  // answers 401 without this one)
  "headers": { "Authorization": "Bearer …" },
  // twenty-something tools, only four wanted: an allowlist is easier to maintain than a denylist
  "includeTools": ["search_pages", "read_page", "create_issue", "list_issues"]
}
```

A non-empty `url` means streamable HTTP and `command` is not run (only one of the two is used).
`env` and `headers` are where a token lives, and which one depends on the kind of server: a local
server reads its token from the environment of the child process, a remote one has no process, so
the token travels in a header. Both are write-only towards the UI — the daemon reports the key names
and never the values — and a change to either counts as a different launch spec, so the client
reconnects instead of keeping a connection made with the old token.

`includeTools` is an allowlist and `excludeTools` a denylist; when both are set, keep then subtract.
Filtering happens when tools are handed to the model, so the count shown in the UI follows.

A 401 or 403 from a remote server is the one handshake failure the error text explains, because the
SDK only ever says "Unauthorized" (see `refusal` in `mcp/client.go`): the transport records the status
and whether a token was configured at all, and `startError` turns that into "refused token", "no token
yet" or "token without the scope" — three different next keystrokes that read identically otherwise.

A save can only answer "starting": a server takes seconds to a minute to come up (`npx` downloads on
first use), and the manager starts it in the background on purpose. So `Manager.OnChange` publishes
`mcp.changed` when that start finishes, fails, or the server announces a new tool list, and the
settings page refetches on it — otherwise the row keeps saying what the save reply said, and a
failure is never shown. `Restart` starts the server itself instead of going through `Sync`, which
only starts a server whose launch spec changed; a retry is the same spec by definition.

The protocol itself is the official `modelcontextprotocol/go-sdk`'s job, but three things are still ours:

- **process group**: the SDK only SIGTERMs the parent, so `npx`'s node becomes an orphan; `Close`
  follows with `kill(-pgid)`.
- **stderr tail**: SDK errors do not carry it, and "why it will not start" is almost always in there, so
  `cmd.Stderr` goes to a ring buffer and is appended to the error.
- **tool schema**: some servers omit `inputSchema`; forwarding that sends `parameters: null`, so an empty
  object schema is filled in.

### Where the settings page draws the line

Clicking a provider row opens that provider (endpoint, key, fetch models, per-model switches). Adding one
goes through `provider.probe` — no half-built provider is created first; saving writes the picks in one
go. Editing an existing provider applies immediately (`provider.setModel`).

Price and context window are not editable in the UI: they change rarely and have a strict format, so a
form invites mistakes. Colour preferences do not go through here either — `Services/PortalService.qml`
asks XDG Desktop Portal directly, because appearance should not disappear when the daemon is down.

## Scheduled tasks

A task is "a prompt plus when to run it". Each run opens a **new session**, so the result has somewhere to
live and can be continued; no separate result viewer.

Tasks live in `$XDG_DATA_HOME/dcode/tasks.json`, separate from `config.json`: tasks are content, not
settings.

### Catch-up semantics

The scheduler scans once at startup, then every 30 seconds. "Should this run" is measured from the **last
actual run**, not from `now`:

```
next = schedule.NextAfter(lastRun)
if next <= now → run
```

The difference shows up after downtime. Measured from `now`, two days off skips every missed run (nobody
notices an hourly task that ran one time fewer); measured from the last run, startup immediately computes
"this was due long ago" and catches up. Catch-up happens **once**: the run pushes `lastRunAt` to now, so
the next one is `now + interval` (or tomorrow's fixed time).

### Three schedules

`daily` (every day at HH:MM), `weekly` (a weekday at HH:MM), `every` (N minutes), plus `manual`. No cron
expressions: flexible, but nobody wants to write `0 9 * * 1-5` in a settings field and remember what it
means. When something genuinely needs complex scheduling, a systemd timer beats inventing one.

### Tools and approval

A task usually runs with no window open, so "nobody to ask" is the normal case rather than the exception:

- `autoApprove` off → tools follow the normal rules, and nobody to ask means refused. Such a task can
  only read (read files, search, fetch pages); it cannot write or run commands.
- `autoApprove` on → everything is allowed. The task acts on its own, with nobody having clicked.

Off by default, deliberately: better a task that cannot touch anything than one that decides for itself
that it may.

### Scope

There is no systemd timer, so **tasks do not run while the app is closed** (the next start catches up
once). Real unattended operation needs `dcode` to run a turn without a daemon, which is a different job.

## The UI side

`Services/Backend.qml` is the only seam:

- `DSocket` + `SplitParser`; the first line is the handshake, and a non-empty `apiVersion` means the
  connection is usable
- `sendRequest(method, params, callback)` pairs `id` with the response
- events are dispatched to `agentEvent(data)`, and `Agent.qml` turns them into `delta` / `finished`

`Agent.qml` keeps two run ids:

- **local rid**: created by `Chat.qml`, used to discard stale replies (a delta that arrives too late is
  dropped)
- **daemon runId**: used to match events

The daemon runId is adopted when the first event arrives, because `chat.send` is asynchronous and a delta
can beat the response.

## Testing the daemon without the UI

The daemon can be exercised entirely on its own:

```bash
cd core && go build -o bin/dcode ./cmd/dcode
DUXCODE_LOG=debug bin/dcode daemon &
bin/dcode ping
bin/dcode new "test"
bin/dcode send -s <id> -m <model> "hello"
```

Without an API key, a fake SSE server is enough: add a provider pointing at `http://127.0.0.1:18080/v1`
in `config.json`.

The UI can be driven over Quickshell's IPC instead of typing:

```bash
qs ipc -p /path/to/dux-code call dcode ping
qs ipc -p /path/to/dux-code call dcode send "hello"
```

(Typing with `wtype` gets fought by input methods and window focus; in practice the characters land in
another window.)

## Not done yet

- **A built-in system prompt**: `config.systemPrompt` exists, but leaving it empty means only the
  auto-added working directory and skill list — there is no default persona.
- **Unattended tasks**: scheduling rides on the daemon's ticker, so nothing runs while the app is closed.
- **Only two MCP transports**: stdio and streamable HTTP. The older SSE endpoint (`/sse`) is not exposed,
  and resources / prompts / sampling still return `-32601`; tools only.
- **The sandbox does not isolate the network**: see above; that is a deliberate trade.
- **Built-in search does search only**: the Anthropic / Gemini / OpenAI Responses server-side search tools
  are all wired up, but citations are not collected into a source list — they only appear in the body text.
