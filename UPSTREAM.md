# Upstream

The QML shell here was cut out of **dux-calendar** — not written from scratch, and not grown out of the
earlier hand-written UI. Copying it instead of drawing our own is about what the app layer already does:
`Common/Theme.qml` is wired to DMS / Matugen (dynamic colours, font scaling), the sidebar's group rows,
section headers and keyboard focus ring are finished M3 components, and the settings pages could be
reused by changing their vocabulary.

| Project | What we took | Last read at |
| --- | --- | --- |
| `duxlabs-dev/dux-calendar` | the shell: all of `quickshell/` | `bd70d74` (2026-11-09) |
| `duxlabs-dev/dux-qml-common` | the component library itself; `DCommon/` is a symlink to `dux-qml-common/DCommon`. Pinned as a submodule | `137e850` (2026-11-09) |
| `duxlabs-dev/dux-files` | the sidebar collapse animation (`WindowSidebarHost.qml`), `core/internal/atomicfile`, the settings write debounce and the saved window size | `86627f5` (2026-11-09) |

## Sync log

Newest first, one entry per round of re-reading upstream. Each entry names what was taken *and* what
was left, so the next round does not re-examine the same commits. Update this when following upstream;
it is the record `make check` cannot produce.

### 2026-11-09 — the library to master, dcal and dfiles re-read

The library: `5911c1b` (`v2.1.1`, the end of `stable-2.1`) → `137e850` (master, where both apps track
it). Both branches fork at `01c14eb`, so this is not a fast-forward. Six fixes and one rewrite, all
inside `DCommon/`, none touching `Common/Theme.qml`:

| Change | Effect here |
| --- | --- |
| `DDialog`: `focus: embedded \|\| opened` | a closed overlay no longer holds the keyboard. Every modal in this tree goes through `DDialog` |
| `DSlider`: inset-icon animation removed | the eight sliders in the settings page no longer glitch on a jump |
| `StyledButton`: a right or middle press that moved is not a click | fewer accidental activations |
| `DActionButton`: focus ring on a filled background | keyboard navigation stays visible |
| `SpringMotion`: closed-form solution | correct under dropped frames; takes `Quickshell.springCoefficients` when the Quickshell build has it, `SpringMath.js` otherwise. Quickshell 0.3.2 does not, so the JS path is what runs — re-check after a Quickshell upgrade |
| `FilePreviewDrawer`: describes the current folder | not used here |

Two features at `v2.1.1` were already in this tree before the bump: `DButton` variants (`variant:
"filled" \| "tonal" \| "outlined" \| "text"` plus `danger`), and the window-opacity / blur settings.
Only dcal's Go blur probe was never taken, and still is not.

dcal, `a8feac8` → `bd70d74`: not taken. The UI delta is `5f88cd7 ui: use DButton variants`, which
this tree already did everywhere it applies (the seven remaining `backgroundColor` / `textColor` pairs
are component-specific tints, e.g. the toast action button on an inverse surface), and an
`add_account` layout fix for the calendar's own dialog. `Widgets/SettingsRow.qml` here is ahead of
dcal's: it uses the library's `StateLayer` where dcal still hand-rolls `Rectangle` + `DRipple`.

dfiles, `2e96e2f` → `86627f5`: taken, a write debounce and a saved window size.

- `Common/SettingsData.qml` — `onAdapterUpdated` wrote `ui-settings.json` immediately, so a slider drag
  serialized the whole document and renamed the file once per pixel. It now arms a 400 ms timer, and
  the self-write flag is set at the moment of writing rather than when the change was queued, so an
  unrelated `fileChanged` in between still reloads. `SettingsData.flush()` writes a pending change
  immediately, and the window's hide path calls it.
- `Modules/ChatWindow.qml` — the window was a fixed 1100x820; the size is now saved and restored, with
  dfiles' `backingWindowVisible` guard (before the compositor shows the window, width and height are
  still the defaults and saving them would overwrite the stored size on every launch).

`maximized` is deliberately **not** persisted, which is where this departs from dfiles. Under Hyprland
the window is tiled and Quickshell reports `maximized` true for a window that merely fills its tile, so
saving it would reopen the app in a state the user never chose.

dfiles' `Common/JsonStore.qml` (a general JSON store with the same debounce inside) was not taken: this
tree has one persisted store and it needs aliases and migrations, so the debounce was added to it
directly instead of swapping it for a generic class.

## How it was done

1. `rsync dux-calendar/quickshell/` into the repository root (including `DCommon/`; it was flattened into
   real files at first so the two trees could be diffed, and moved back to a submodule later)
2. delete the calendar-only Module / Widget / Modal / Service files
3. bring in the chat layer from the earlier hand-written build: `MessageList`, `MessageDelegate`,
   `Composer`, `EmptyState`, `Blocks/*`, `Chat.qml`, `Agent.qml`, `Models.qml`
4. write `ChatWindow` / `ChatSidebar` / `ChatContent` / `Services/Backend.qml` / `Services/Sessions.qml`

### Calendar vocabulary → chat vocabulary

The skeleton was not redrawn, only renamed:

| dux-calendar | dux-code |
| --- | --- |
| `+ Create event` | `+ New session` |
| `My calendars` (collapsible) | `Sessions` (collapsible) |
| calendar group headings | Today / This week / This month |
| calendar row (pick a calendar) | session row (pick a session) |
| the calendar in the middle | message stream + composer |
| `CalendarWindow` / `CalendarSidebar` / `CalendarContent` | `ChatWindow` / `ChatSidebar` / `ChatContent` |

## Syncing with upstream

No upstream remote is kept (the files were copied, not cloned), so syncing is a manual diff:

```bash
# without DUX_DCAL, the script looks in ../dux-calendar, ~/dux-calendar, ~/src/dux-calendar
DUX_DCAL=~/src/dux-calendar

diff -ru "$DUX_DCAL/quickshell"/{Common,Services,Modals,Widgets} \
        ./{Common,Services,Modals,Widgets} | less
```

`Common/Theme.qml`, `Services/{Log,ToastService,PortalService,FilesService}.qml`,
`Modals/Settings*.qml` and `Widgets/Settings*.qml` are **untouched upstream files** and can be
overwritten wholesale. `ChatWindow` / `ChatSidebar` / `ChatContent` are ours — do not overwrite them.

`tools/check-upstream.sh` automates what can be automated: whether the submodule is clean, whether the
Theme contract holds, and — when a dcal checkout is around — the app layer. A missing checkout prints one
line and skips, so a machine without dcal does not fail. A wrong `DUX_DCAL` is reported instead of being
silently searched past.

### The library is a submodule

Same shape as upstream `dux-calendar` / `dux-files`:

```
dux-qml-common/                     # submodule, URL in .gitmodules
DCommon -> dux-qml-common/DCommon   # symlink, so qs.DCommon.* resolves
```

The library's contents are decided by that commit; we do not keep its files. To update:

```bash
git submodule update --remote dux-qml-common   # follow origin/master
git add dux-qml-common && git commit            # the new commit is what gets recorded
./tools/check-upstream.sh && make run           # run it again and look
```

**Treat a version bump as a real change.** `git submodule update --remote` brings in everything upstream
did at once, and `DCommon` is the component library: Theme tokens and animation curves can move with it.
Run `make check` first after upgrading — it lists the tokens upstream has and we lack, and adding them to
`Common/Theme.qml` is part of the upgrade. (`Style.qml` falls back through `?? default`, so nothing
breaks, but the colours stop following the theme.)

**Be careful with symlinks when packaging.** `go:embed` does not follow symlinks, so `sync-shell` in
`core/Makefile` uses `tar -ch` to flatten `DCommon` into real files before embedding (upstream
`dux-calendar` does the same in its own `core/Makefile`). Drop the `-h` and the build produces a binary
with no component library: it opens blank, and nothing reports an error.

## Two Theme layers — do not mix them up

| File | Contents |
| --- | --- |
| `dux-qml-common/Common/Theme.qml` | the **reference app layer** shipped with the library: `expressiveDurations` hard-coded to 200/400/600/1000/350/500/650/150/200/300, 12 curves |
| `dux-calendar/quickshell/Common/Theme.qml` | the real app layer: `SettingsData.animationDuration` (250 by default) multiplied by factors, 9 curves |

This project was cut from the second one and matches it literally: 250 by default, factors
0.4 / 0.8 / 1.2 / 2.0 / 0.7 / 1.0 / 0.4 → 100 / 200 / 300 / 500 / 175 / 250 / 100.

The "upstream Theme" that `tools/check-upstream.sh` verifies is the first one, and it compares **tokens**
only (colours, radii, opacities) — it never looks at those two computed objects, so passing does not mean
the two animate at the same tempo.

To change the feel, change upstream (`SettingsData.animationDuration` or the factors). Do not grow a
second tempo inside our Theme.

### `DCommon/` and `Common/` are different things

`Common/` in the upstream repository **is not the library** — it is the library's minimal runnable
sample: `SettingsData.qml` is 28 lines (only the tokens the library reads), `I18n.qml` is a 13-line stub,
`Theme.qml` is a reference theme. Real apps (dms / dcal / dcode) each own a larger `Common/`.

So what is copyable is the **contract**, not the files:

| Contract | Contents | State |
| --- | --- | --- |
| Singletons DCommon imports directly | `I18n` (209 uses), `Paths` (13), `Anims` (3) — all under `qs.DCommon.Common` / `qs.Common` | present |
| `theme?.X` tokens read by `Style.qml` | 46 (properties + functions) | present |

**Never overwrite `Common/` (`Theme.qml` and friends) with the upstream copy.** It comes from
dux-calendar and has been modified (extra dcal tokens, missing tokens upstream added later). Add the one
upstream token you need, on its own.

## Deliberate divergences

`Common/` is not a copy. These are the deliberate differences; everything else follows upstream.

| token | upstream (dcal) | here | why |
| --- | --- | --- | --- |
| `blurLayersActive` | `BlurService.enabled` | `SettingsData.blurEnabled && translucentWindows` | no probe process here: asking the compositor to blur is a silent no-op without the protocol, so the setting is gated on the user's own choice rather than on a detection step |
| `dmsTypographyPath` | `~/.cache/dms/dms-typography.json` | Dux Shell 2.0's `~/.config/dms/settings.json` first, the old path only as a fallback | 2.0 no longer writes the old file; reading only the cache makes "automatic" fonts look permanently undetected |
| `outlineMedium` / `outlineStrong` | hard-coded `0.12` / `0.18` | `SettingsData.blurLayerOutlineOpacity` (`0.12` by default, `Strong` is `×1.5`) | same approach as dfiles, and that hairline is exactly what draws the layer separation |

Those three are also the `ALLOW` list in step ④ of `tools/check-upstream.sh` — **change one and you
change the other**.

A few more differ in value, at the app layer:

| Where | Upstream | Here | Why |
| --- | --- | --- | --- |
| `SettingsData.blurLayerOutlineOpacity` | `0` | `0.12` | the library separates layers with that 1px outline (upstream switched to elevation) |
| `SettingsData.blurBorderEnabled` | `false` | `true` | same reason; card borders are always on |
| `Theme.stateLayerFocus` / `stateLayerPressed` | `0.1` | `0.12` | slightly stronger hover / pressed feedback |
| `Theme.tooltipMaxWidth` | `300` | `500` | paths and model names are long |
| `Theme.clockFaceSize` / `clockHandleSize` / `sliderHandle*` | 256 / 48 / 52 / 36 | 250 / 40 / 44 / 44 | dcal leftovers; there is no clock face and no large slider here |

`shapeScale` and the nine `cornerRadius*` do not pass through `fixedRadius` (only `-1` is declared),
same shape as dfiles; the `popupTransparency` set takes `SettingsData.floatingWindow*` as its source.
Both produce the same result at default values — only the spelling is tidier.

## Ripple: two feels in one release

In v2.0.0 (`ebf5e6c`) `DRipple` is a single `rippleAnim`: expand → hold → fade, and `trigger(x, y)` has
no hold parameter, so it **always plays to the end**.

After `bc401e4 "Animation tweaks (#34)"` it is split into `growAnim` / `autoFade` / `releaseFade`, and
`trigger` takes a third parameter:

| Call | Who | Behaviour |
| --- | --- | --- |
| `trigger(x, y)`, hold defaults to false | `SettingsRow`, `SettingsSidebarItem` (drawn by the app layer) | takes `autoFade`; releasing changes nothing, it plays out |
| `trigger(x, y, true)` | `StateLayer` → `DButton` / `DButtonGroup` / `DIconButton` … | `releaseFade` on release, fading out while still growing |

So one master build of dcal already has two feels: the pills made by `SettingsRow` in the settings window
are never cut short, the `DButtonGroup` pills on the main page are. It is not a version difference —
upstream migrated only half of `StateLayer`.

This project copies the same shape (`Widgets/SettingsRow.qml` also calls the two-argument form), so the
behaviour matches dcal master. **Do not unify it on our side**: that is upstream's decision to settle,
and we follow when it does.

## Upstream quirks we do not fix

Fixing upstream's problems locally costs an argument at every sync, and leaves "the component library
that is different only here". The strategy is to copy the behaviour, write it down here, and wait.

1. **Two ripple feels** — `StateLayer` takes the hold path (fade out on release), but dcal's own
   `SettingsRow` / `SettingsSidebarItem` still call the two-argument `trigger()`. One release therefore
   has "settings rows are not interrupted, pills are".
2. **v2.0.0 and master ship different `DRipple` code** — 2.0.0 has a single `rippleAnim` and no hold, so
   the released version behaves differently from master.
3. **The library's reference Theme is out of step with dcal's app layer** — the first hard-codes 500 ms,
   10 durations and 12 curves; the second multiplies 250 ms and has 7 durations and 9 curves.
   `check-upstream.sh` checks tokens only, so it does not complain about the mismatch.

## Do not hand-roll what the library already has

Rule of thumb: **the library has it and upstream (dcal / dfiles) uses it** → switch to it. The library
has it but nobody upstream uses it (e.g. `DConfirmDialogContent`) → leave it alone, rather than become
the first to step on it.

Same-named files are not always duplicates: `Common/Paths.qml`, `Services/Log.qml` and
`Common/I18n.qml` share names with the library's three, but the library's are backend seams
(`property var backend`, degrading to no-ops) while ours are the implementation.
`Widgets/SettingsRow.qml` is dcal's file with two additions of our own (a `keywords` field for the
settings search, and a `highlighted` state for a search jump) and one substitution: the hover, press
and ripple layer comes from the library's `StateLayer`, where dcal still hand-rolls a `Rectangle` plus
`DRipple`. It is app-layer code, not a gap in the library.
