# Translations

## The source language is English, so there is no English catalog

QML code calls `I18n.tr(term, context)` with the English sentence as `term`, and `tr()` returns `term`
when nothing matches. `Common/I18n.qml` short-circuits `en*` locales to an empty path
(`useLocale(cand, cand.startsWith("en") ? "" : …)`), so no file is opened for them at all.

`template.json` is **not** an English catalog — it is the list of strings that need translating, and
`translation` is empty in it by design (see below).

## Files, and which one the app actually reads

| File | What it is | Read at runtime | In git |
| --- | --- | --- | --- |
| `template.json` | the work list: `term` / `translation` (empty) / `context` (a note for translators). Written by `make i18n` from every `I18n.tr` in this repo | ✗ | ✓ |
| `poexports/<locale>.json` | **the real catalog, and the one the app loads**. Shape: `context → term → translation` | ✓ | ✓ |
| `overrides/<locale>.json` | flat `term → translation`, for component-library strings the library itself has not translated. Merged into `poexports` by `apply`; our own entry wins over the library's | ✗ | ✓ |
| `extract_translations.py` | builds `template.json`, `seed`s a work file, `apply`s it back | ✗ | ✓ |
| `work/<locale>.json` | scratch file, created by `seed` and deleted by `apply` | ✗ | ✗ |

`template.json` is committed on purpose: when upstream adds a string or the wording changes, the diff
after `make i18n` shows which strings are new, so a review cannot miss them.

## The library has a catalog of its own

`dux-qml-common/DCommon/translations/poexports/` is not ours and does not live here. `I18n.qml` loads
it into `commonTranslations` and only consults it after our own catalog misses.

For `zh_TW` it holds 115 entries of which 39 are translated. The other 76 are stored as **empty
strings**, which `tr()` treats as a miss (it tests truthiness), so they fall back to the English term.
That is why a component can read English while everything around it is translated.

Do not fix that by editing `dux-qml-common/` — a dirty submodule turns `tools/check-upstream.sh` red.
Put the string in `overrides/<locale>.json` instead: `tr()` looks in our catalog first, so a hit there
shadows the library's. `apply` merges overrides into `poexports` (our `template.json` wins when the same
term appears in both). If the library renames a term or changes its context, the stale override simply
stops matching — nothing breaks.

So seeing two `zh_TW.json` files is expected: one is ours (`poexports/`, the one the app reads), the
other belongs to the library (`dux-qml-common/…`). Both are in use; neither is a duplicate.

## Workflow

```sh
make i18n                                    # re-extract → template.json, then print coverage per locale

# translating (zh_TW as the example)
python3 translations/extract_translations.py seed zh_TW
#   → translations/work/zh_TW.json, already-translated strings filled in, blanks are what is left

$EDITOR translations/work/zh_TW.json         # fill in `translation` only; never touch `term`

python3 translations/extract_translations.py apply translations/work/zh_TW.json zh_TW
#   → written back to translations/poexports/zh_TW.json
```

- `make i18n-status` shows how many strings each locale is missing
- `seed` rebuilds the work file from `poexports/`, so an interrupted session loses nothing already applied
- `apply` skips entries whose `translation` is blank — an untranslated string is never written as `""`
- `apply` deletes the work file on success, so the directory never holds a second file that looks like a catalog
- `apply` merges `overrides/<locale>.json` on every run, so re-run it after editing overrides

## Only zh_TW for now

`make i18n-status` prints the live numbers. Anything missing — new strings, other locales — renders as
the English term; the UI never shows a blank label for a missing translation.

## The source term must be English

The first argument to `I18n.tr()` is the English sentence, and it doubles as the fallback, so a Chinese
term would surface as Chinese in the English UI. `make i18n` checks for it and fails on CJK characters,
printing the offending term:

```
✗ 1 term is not English (the source language must be English):
    '<the term is quoted here>'
```

## What gets scanned

`extract_translations.py` scans this repository's own QML and skips:

- `dux-qml-common/` — the library's strings belong to upstream (it has its own
  `DCommon/translations/poexports/`, loaded separately and used as a fallback)
- `core/internal/uibundle/dist/` — the packaged copy of the whole QML tree
- `dist/`, `core/bin/`

The list lives in the script's `skip_prefixes`. `DCommon` is a symlink, and `rglob` does not follow it,
so the library is only excluded by not walking `dux-qml-common/`.
