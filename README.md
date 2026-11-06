# Dux Code

<div align="center">
  <img src="assets/com.nekolovo.dcode.svg" alt="Dux Code" width="160">

  <p>
    <a href="https://github.com/NekoLOvO/dux-code/actions/workflows/ci.yml"><img src="https://github.com/NekoLOvO/dux-code/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  </p>

### A native coding agent for the dux desktop

Built with [Quickshell](https://quickshell.org/) and [Go](https://go.dev/)

</div>

Dux Code is a coding agent that runs as part of your desktop instead of in a browser tab or a terminal window. A Go daemon, `dcode`, owns the conversation: it talks to the model provider, runs the tools and writes sessions to disk. A Quickshell UI draws it. Close the window and the daemon keeps working; `dcode send` from a shell sees exactly the same sessions.

The UI uses the same widget library and the same design language as Dux Files and Dux Calendar, and picks up Dux Shell's dynamic colors when DMS is running or Matugen is set up. With neither, it falls back to its own palette.

> **This is not a duxlabs-dev project.** Dux Code is a personal project by [NekoLOvO](https://github.com/NekoLOvO). It is not built, published, endorsed or supported by duxlabs-dev. It borrows the shared widget library and the shell structure described under [Relationship to upstream](#relationship-to-upstream), which is why it sits next to those apps so comfortably — but the daemon, the agent loop and the chat UI are written here, and the bugs are mine. It is Linux only, it expects Wayland, and it comes with no support promise.

## Features

**Chat**
Replies stream in as they arrive; the daemon merges deltas every 50 ms so a long answer does not become a thousand IPC lines. Cancel mid-answer, regenerate, edit a message and send it again, branch the conversation from a point, truncate from a point, or export the session as JSON under `$XDG_DATA_HOME/dcode/exports` — the session file's own shape, so another agent can read it without a conversion layer. A reply renders as Markdown: prose — paragraphs, headings h1–h6, nested lists with task boxes, nested quotes, tables with column alignment — is one text document per stretch, so a selection runs across block boundaries and a table is not a widget you cannot copy out of; hard breaks, escapes, reference links, footnotes and inline `sub`/`sup`/`mark`/`kbd` all survive. Code blocks are highlighted by the daemon with [chroma](https://github.com/alecthomas/chroma), so the UI does not carry a highlighter of its own.

**Tools**
Built in: `read_file`, `write_file`, `edit_file`, `glob`, `grep`, `bash`, `web_fetch`, `web_search`, `browser`, `ask_user`, plus the small ones (`echo`, `now`, `load_skill`). File tools are bounded by the session's working directory and `bash` runs there. A command that times out is killed as a process group, so its children do not survive it. Tool results are capped at 32 KB / 2000 lines before they go back into the conversation — one `cat` of a large file must not eat the context. `ask_user` turns into a dialog, so the model can ask a question instead of guessing. `AGENTS.md` in the working directory is re-read on every turn as project instructions, capped at 32 KB.

**Providers**
Six templates: Anthropic (native Messages API), DeepSeek, Google Gemini (native API, so built-in Google search works), OpenAI (Responses API), OpenRouter, and a generic OpenAI-compatible slot for Ollama, llama.cpp, vLLM or LM Studio. A key lives in `config.json` or in an environment variable, and the daemon only ever reports whether a key exists, never the key itself. Models are probed from the endpoint and enabled one by one, each with its own context window; reasoning depth (none to max) is sent per turn where the provider supports it, and a provider's own web search can be switched on separately.

**Long conversations**
When the history about to be sent passes the compaction threshold it is summarized once and the most recent 40% is kept. The transcript keeps every message and marks the line where the summary takes over, with the summary itself a click away; only what goes to the model is replaced. The threshold, the per-request output cap and the tool step limit are all settings, and all treat zero as off: no compaction, the provider's own default, no step cap. The output cap defaults to off, and a cap larger than the model can produce is lowered to the model's own ceiling rather than sent and rejected; a compaction threshold above what the model can read is brought down to fit for the same reason. Each model's window and ceiling come from the provider when it reports them (OpenRouter, DeepSeek and Gemini all do), can be written into `config.json` by hand, and — for the providers that report nothing — are learned from the first refusal: a 400 that names the model's maximum is recorded and the same step is resent with it.

**Sessions**
One JSON file per session under `$XDG_DATA_HOME/dcode/sessions`. They are readable and editable with any editor, a broken file is skipped with a warning instead of taking the rest down, and nothing is written until a turn ends. Pinned sessions sort first, search covers every session, and the title comes from the first message (`smart rename` asks the model for a better one).

**Tasks**
Run a prompt on a schedule: daily at a time, every N minutes, or weekly on a given weekday. `dcode` runs it itself and puts the result in a new session, so a task's output is an ordinary conversation you can read, continue or delete. A task can carry its own model, working directory and approval setting.

**Skills and MCP**
Skills are read from `~/.agents/skills/<name>/SKILL.md`, the cross-tool convention, so the same skill works in other agents too. Activating one loads its instructions and, if its frontmatter lists `allowed-tools`, narrows what the rest of that turn may call. External MCP servers over stdio or streamable HTTP add their tools to the same list; they are added, toggled and restarted from Settings.

**Desktop integration**
One window: launching `dcode ui` again raises the running one instead of starting a second shell. Tray icon, optional XDG autostart (tray only, no window at login), desktop entry, and dynamic colors from Dux Shell or Matugen.

## Installation

### Requirements

- Linux with a Wayland session
- Go 1.27 or newer to build
- [Quickshell](https://quickshell.org/) 0.3 or newer to draw the UI

### From source

```bash
git clone --recurse-submodules https://github.com/NekoLOvO/dux-code.git
cd dux-code
make
sudo make install
```

`make` builds `core/bin/dcode` with the whole QML tree embedded, so an installation is one binary plus the desktop entry and the icon. `sudo make install` puts them under `/usr/local`; `sudo make install PREFIX=/usr` puts them under `/usr` instead. `sudo make uninstall` removes them.

Cloned without submodules? `git submodule update --init` fetches the widget library the UI needs.

### Prebuilt binaries

Every `vX.Y.Z` tag is built by GitHub Actions for `amd64` and `arm64` and attached to the
[releases page](https://github.com/NekoLOvO/dux-code/releases), with a `.sha256` next to each archive.
Both are static binaries — the QML tree is embedded, so there is nothing else to install.

### From source, packaged

```bash
make release     # → dist/dcode-linux-amd64.tar.gz
ARCH=arm64 make release
```

The archive holds the binary, `assets/` and the install scripts:

```bash
tar xzf dcode-linux-amd64.tar.gz
sudo ./dcode-linux-amd64/scripts/install-release.sh dcode-linux-amd64/dcode
```

`PREFIX=/usr` works here too.

### Optional dependencies

| package | enables |
| --- | --- |
| `bubblewrap` | the kernel-level half of workspace mode; see [Permissions and the sandbox](#permissions-and-the-sandbox) |
| `agent-browser` | the `browser` tool. Without it the tool is not offered at all — Settings → Tools says why |

## Usage

The first run has no provider configured. Open Settings → Providers, add one with a key (or the provider's environment variable), and pick a default model — model calls go only to providers you set up here.

```bash
dcode ui                        # Open the window, starting the daemon if needed
dcode ui --hidden               # Tray only — what autostart uses
dcode send "explain this repo"  # One turn from the terminal, streaming
dcode send -s <session> -m <model> "and now the tests"
```

### Command line

| command | does |
| --- | --- |
| `dcode ui` | show the window, bringing the daemon up first |
| `dcode send <text>` | start a session (or continue one with `-s`) and stream the reply |
| `dcode sessions` | list sessions, newest first |
| `dcode new [title]` | create one |
| `dcode show` / `dcode toggle` | raise / show-or-hide the running window |
| `dcode ping` | daemon version, working directory, and every model with its key status |
| `dcode daemon` | run the daemon in the foreground |
| `dcode version` | print the build |

Everything except `ui`, `daemon` and `version` is a thin client on `$XDG_RUNTIME_DIR/dcode/daemon.sock`. If nothing is listening, they say so and exit — they do not quietly start a second daemon.

### Where things live

| path | holds |
| --- | --- |
| `~/.config/dcode/config.json` | providers, keys, defaults, tool switches |
| `$XDG_DATA_HOME/dcode/sessions/*.json` | one file per session |
| `$XDG_DATA_HOME/dcode/tasks.json` | scheduled tasks |
| `$XDG_RUNTIME_DIR/dcode/daemon.sock` | the socket (`DUXCODE_SOCKET` overrides it) |
| `$XDG_CACHE_HOME/dcode/ui/<build>` | the embedded UI unpacked for Quickshell — one tree per build, older ones are removed on start |
| `~/.agents/skills/*/SKILL.md` | skills |

## Permissions and the sandbox

A tool call that can change something goes through one policy point, and there are three modes:

| mode | behaviour |
| --- | --- |
| `ask` | every call waits for a nod (the default) |
| `workspace` | calls that clearly stay inside the working directory run; the rest ask |
| `auto` | nothing asks |

`workspace` is the useful one, and it is honestly two layers rather than one. The first layer is judgement: file paths are resolved and compared against the root, and `bash` commands are matched literally against `rm -rf /`, `cat ../x`, `curl -o /tmp/x` and the like. That can be fooled by a computed path, an `eval` or a download piped into a shell. The second layer is the kernel: in this mode an auto-approved `bash` command runs under `bwrap` when it is available, so a write outside the directory simply fails. `~/.cache` and the other build caches stay writable, otherwise `go build` and `npm install` would break. A command you were asked about runs unwrapped — you read it and agreed, and wrapping it would stop your own command from working.

Without `bubblewrap` only the first layer exists. The settings page notices and stops claiming that writes outside the directory are impossible.

## Relationship to upstream

Not part of duxlabs-dev, but standing on their shoulders:

| what | where it came from |
| --- | --- |
| QML shell | `duxlabs-dev/dux-calendar` at `a8feac8` (2026-10-28); the calendar modules were deleted and the chat layer written here |
| Widget library | `duxlabs-dev/dux-qml-common`, pinned as a submodule (currently `a0d6dee`) |
| Sidebar collapse animation | `duxlabs-dev/dux-files` at `2e96e2f` |
| Daemon, agent loop, tools, chat UI | written for this project; the NDJSON IPC shape follows the dux family convention, no code copied |

There is no upstream remote — the shell was copied, not cloned — so syncing is manual and written down in [UPSTREAM.md](UPSTREAM.md). `make check` compares the widget library's `Theme` contract and the app-level theme against dux-calendar, so drift shows up as a failing check instead of a subtly wrong color.

## Repository Structure

```
dux-code/
├── core/                  # Go daemon, the dcode CLI, and every tool
│   ├── cmd/dcode/         # ui, daemon, send, sessions, new, show, toggle, ping
│   └── internal/          # agent, llm, tools, mcp, skills, store, tasks, ipc, api
├── Common/                # Theme, settings data, i18n
├── Services/              # Bridges to the daemon (Backend, Sessions, Agent, Models)
├── Modules/               # Chat window, sidebar, composer, message list, blocks
├── Modals/                # Settings, provider, MCP, task, permission and search dialogs
├── Widgets/               # Settings rows, toasts, paste probe
├── DCommon -> dux-qml-common/DCommon   # Widget library, symlinked into the QML import path
├── dux-qml-common/        # The shared dux widget library (submodule)
├── translations/          # I18n catalogs and the extraction script
├── tools/                 # check-upstream.sh
├── .github/workflows/     # CI (check, vet, test, packaged build) and the tag-driven release
├── docs/                  # Design notes
└── assets/                # Icon and desktop entry
```

## Development

```bash
make run        # build and run against the QML in the repo
make hot        # the same, but a fresh shell each time (QML edits apply)
make check      # widget library and Theme contract
make test       # Go tests
make fmt / vet
make i18n       # re-extract I18n.tr() strings → translations/template.json
make i18n-status
```

`make run` builds without the embedded UI and launches `dcode ui -c .`, so QML edits apply on restart. `make hot` (i.e. `DUXCODE_ENABLE_HOTRELOAD=1`) skips the single-instance check and starts a new Quickshell — the one case where a second shell is what you want.

[AGENTS.md](AGENTS.md) is the working brief for coding agents: layout, commands, and the landmines this project has already stepped on. [docs/go-daemon.md](docs/go-daemon.md) explains why the backend looks the way it does. The Go side has tests; the UI does not — run it and look.

## AI contributions

Most of the code here was written by AI coding agents, directed and reviewed by a human. That is deliberate, and it is disclosed rather than hidden:

- The design, the trade-offs and the review are human. The typing is largely not.
- Every change is built, vetted, tested and launched before it lands. `make test`, `make vet`, `make check` and "does the window still work" are the bar, not "the diff looks plausible".
- No AI is credited as an author. Someone is responsible for every line in this repository, and it is a person.

If you want to send a change:

- Say which parts were AI-generated, and with what. A sentence in the PR is enough.
- Write the PR description yourself, and write your issues and comments yourself. A pasted model transcript is not a description; PRs that read like unreviewed output get closed.
- Run it before you report it. A bug report that was never reproduced against a build of this repository gets closed, and "the model said it would work" is not a reproduction.
- Match the code around you. The style is consistent on purpose, and a diff that ignores it is a diff someone has to rewrite.

## License

MIT License — see [LICENSE](LICENSE) for details. No CLA: contributions go in under the same MIT terms.
