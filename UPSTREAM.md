# Upstream

The QML shell here was cut out of **dux-calendar** — not written from scratch, and not grown out of the
earlier hand-written UI. Copying it instead of drawing our own is about what the app layer already does:
`Common/Theme.qml` is wired to DMS / Matugen (dynamic colours, font scaling), the sidebar's group rows,
section headers and keyboard focus ring are finished M3 components, and the settings pages could be
reused by changing their vocabulary.

| Project | Version | What |
| --- | --- | --- |
| `duxlabs-dev/dux-calendar` | `a8feac89ec4edf711cb46f632e412dc5cc59df9e` (2026-10-28) | source of the shell: all of `quickshell/` |
| `duxlabs-dev/dux-qml-common` | submodule, pinned at `v2.1.1` (`5911c1b`) | the component library itself; `DCommon/` is a symlink to `dux-qml-common/DCommon` |
| `duxlabs-dev/dux-files` | `2e96e2fd126239f23bedfd13f3a7be0c5db3a727` | reference for the sidebar collapse animation only (`WindowSidebarHost.qml`) |

Both apps were re-read at `v2.1.1` when the library moved there. dcal's `v2.1.1` delta in `quickshell/`
is `DButton` variants (this tree already used them) and the window-opacity / blur settings (already
ported, including the one divergence noted below); the only part not taken is its Go blur probe. dfiles'
`v2.1.1` is mostly its own vocabulary; only `core/internal/atomicfile` came over.

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
`Widgets/SettingsRow.qml` is byte-identical to dcal's — that is the app layer's own file, not a gap in
the library.
