#!/usr/bin/env python3
import ast
import re
import json
import sys
from pathlib import Path
from collections import defaultdict


def decode_string_literal(content, quote):
    try:
        return ast.literal_eval(f"{quote}{content}{quote}")
    except (ValueError, SyntaxError):
        return content


def spans_overlap(a, b):
    return a[0] < b[1] and b[0] < a[1]


def extract_qstr_strings(root_dir):
    translations = defaultdict(lambda: {'contexts': set(), 'occurrences': []})
    qstr_patterns = [
        (re.compile(r'qsTr\(\s*"((?:\\.|[^"\\])*)"\s*\)'), '"'),
        (re.compile(r"qsTr\(\s*'((?:\\.|[^'\\])*)'\s*\)"), "'")
    ]
    i18n_context_patterns = [
        (
            re.compile(r'I18n\.tr\(\s*"((?:\\.|[^"\\])*)"\s*,\s*"((?:\\.|[^"\\])*)"\s*\)'),
            '"'
        ),
        (
            re.compile(r"I18n\.tr\(\s*'((?:\\.|[^'\\])*)'\s*,\s*'((?:\\.|[^'\\])*)'\s*\)"),
            "'"
        )
    ]
    i18n_simple_patterns = [
        (re.compile(r'I18n\.tr\(\s*"((?:\\.|[^"\\])*)"\s*\)'), '"'),
        (re.compile(r"I18n\.tr\(\s*'((?:\\.|[^'\\])*)'\s*\)"), "'")
    ]

    # Only this repo's own QML is scanned. Three things are excluded:
    #   dux-qml-common/            the component library, whose strings belong upstream (rglob
    #                              walks into it; a symlink does not stop it, so it is excluded)
    #   core/internal/uibundle/dist the QML copied at packaging time; scanning it only yields
    #                              two spellings of every reference
    #   dist/ core/bin/            other build output
    skip_prefixes = (
        'dux-qml-common/',
        'core/internal/uibundle/dist/',
        'dist/',
        'core/bin/',
    )
    for qml_file in Path(root_dir).rglob('*.qml'):
        relative_path = qml_file.relative_to(root_dir)
        if str(relative_path).startswith(skip_prefixes):
            continue

        with open(qml_file, 'r', encoding='utf-8') as f:
            for line_num, line in enumerate(f, 1):
                for pattern, quote in qstr_patterns:
                    for match in pattern.finditer(line):
                        term = decode_string_literal(match.group(1), quote)
                        translations[term]['occurrences'].append({
                            'file': str(relative_path),
                            'line': line_num
                        })

                context_spans = []
                for pattern, quote in i18n_context_patterns:
                    for match in pattern.finditer(line):
                        term = decode_string_literal(match.group(1), quote)
                        context = decode_string_literal(match.group(2), quote)
                        translations[term]['contexts'].add(context)
                        translations[term]['occurrences'].append({
                            'file': str(relative_path),
                            'line': line_num
                        })
                        context_spans.append(match.span())

                for pattern, quote in i18n_simple_patterns:
                    for match in pattern.finditer(line):
                        if any(spans_overlap(match.span(), span) for span in context_spans):
                            continue
                        term = decode_string_literal(match.group(1), quote)
                        translations[term]['occurrences'].append({
                            'file': str(relative_path),
                            'line': line_num
                        })

    return translations


# ─────────────────────────────────────────────────────────────
# Output: template.json / poexports / work
# ─────────────────────────────────────────────────────────────

def template_entries(translations):
    """Shape every string into template.json.

    term is the source text (English: the literal in the code, never present in a translation file),
    context is the note for translators (I18n.tr's second argument), translation is left empty.
    No file:line on purpose — it changes with every edited line, and the diff fills with line numbers.
    """
    entries = []
    for term, data in sorted(translations.items()):
        if not term.strip():
            continue
        entries.append({
            'term': term,
            'translation': '',
            'context': ' | '.join(sorted(data['contexts'])),
        })
    return entries


CJK = re.compile(r'[\u3400-\u4dbf\u4e00-\u9fff\u3040-\u30ff\uac00-\ud7af]')


def suspect_terms(entries):
    """term must be English (it is the fallback when no translation matches and shows in the English UI).
    A CJK character means someone wrote a translation or a note as the term in I18n.tr."""
    return [entry['term'] for entry in entries if CJK.search(entry['term'])]


def load_locale(path):
    """poexports/<locale>.json → {term: translation}

    The file is {context: {term: translation}}; in practice context always equals term
    (that is what I18n.tr does without a context), so it flattens to term -> translation.
    """
    if not path.exists():
        return {}
    data = json.loads(path.read_text(encoding='utf-8'))
    flat = {}
    for terms in data.values():
        for term, translation in terms.items():
            if translation:
                flat[term] = translation
    return flat


def dump(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, 'w', encoding='utf-8') as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
        f.write('\n')


def write_locale(path, pairs):
    """{term: translation} -> the {term: {term: translation}} shape I18n.qml reads"""
    dump(path, {term: {term: translation} for term, translation in sorted(pairs.items())})


def coverage(entries, pairs):
    missing = [entry['term'] for entry in entries if entry['term'] not in pairs]
    return len(entries) - len(missing), missing


def print_coverage(entries, pairs, label):
    done, missing = coverage(entries, pairs)
    if missing:
        print(f'  {label}: {done}/{len(entries)} translated, {len(missing)} missing')
    else:
        print(f'  {label}: {done}/{len(entries)} complete')


# ─────────────────────────────────────────────────────────────
# Commands
# ─────────────────────────────────────────────────────────────

def cmd_extract(script_dir):
    translations = extract_qstr_strings(script_dir.parent)
    entries = template_entries(translations)
    dump(script_dir / 'template.json', entries)

    print(f'Extracted {len(translations)} strings -> translations/template.json')
    for locale_file in sorted((script_dir / 'poexports').glob('*.json')):
        print_coverage(entries, load_locale(locale_file), locale_file.stem)

    bad = suspect_terms(entries)
    if bad:
        print(f'\n✗ {len(bad)} terms are not English (the source language must be English):')
        for term in bad:
            print(f'    {term!r}')
        print('  Change the first I18n.tr argument to English and put the translation in poexports/.')
        return False

    print()
    print('Translating (zh_TW as the example):')
    print('  1. python3 translations/extract_translations.py seed zh_TW')
    print('     -> translations/work/zh_TW.json, pre-filled with what is translated')
    print('  2. edit that file and fill in the empty translation fields')
    print('  3. python3 translations/extract_translations.py apply translations/work/zh_TW.json zh_TW')
    print('     -> writes back to translations/poexports/zh_TW.json')
    return True


def cmd_seed(script_dir, locale):
    entries = template_entries(extract_qstr_strings(script_dir.parent))
    pairs = load_locale(script_dir / 'poexports' / f'{locale}.json')

    work = []
    for entry in entries:
        item = dict(entry)
        item['translation'] = pairs.get(entry['term'], '')
        work.append(item)

    target = script_dir / 'work' / f'{locale}.json'
    dump(target, work)

    done, missing = coverage(entries, pairs)
    print(f'{target}: {done}/{len(work)} translated, {len(missing)} missing')
    for term in missing[:10]:
        print(f'    missing: {term}')
    if len(missing) > 10:
        print(f'    …and {len(missing) - 10} more')


def load_overrides(script_dir, locale):
    """{term: translation} for component-library strings we translate ourselves.

    The component library is a submodule and its own translations are upstream's; editing them would
    dirty the submodule and fail the upstream check. Our poexport is consulted first, so translating
    the same term here overrides it without touching the library.
    """
    path = script_dir / 'overrides' / f'{locale}.json'
    if not path.exists():
        return {}
    return json.loads(path.read_text(encoding='utf-8'))


def cmd_apply(script_dir, work_path, locale):
    work_path = Path(work_path)
    work = json.loads(work_path.read_text(encoding='utf-8'))
    pairs = {item['term']: item['translation'] for item in work if item.get('translation')}
    # Our own strings win; overrides only fill in what this repo does not define.
    overrides = load_overrides(script_dir, locale)
    added = 0
    for term, translation in overrides.items():
        if term not in pairs:
            pairs[term] = translation
            added += 1
    target = script_dir / 'poexports' / f'{locale}.json'
    write_locale(target, pairs)
    print(f'{target}: wrote {len(pairs)} entries ({added} component-library overrides)')

    # Drop the work file once used: keeping it only leaves a zh_TW.json that looks like a translation file
    work_path.unlink(missing_ok=True)
    print(f'{work_path}: deleted (seed again to continue translating)')


def cmd_status(script_dir):
    entries = template_entries(extract_qstr_strings(script_dir.parent))
    print(f'template.json holds {len(entries)} strings')
    files = sorted((script_dir / 'poexports').glob('*.json'))
    if not files:
        print('  (no translation files under poexports/)')
    for locale_file in files:
        print_coverage(entries, load_locale(locale_file), locale_file.stem)


USAGE = """
Usage:
  extract_translations.py                 re-extract strings -> translations/template.json
  extract_translations.py status          how many strings each locale is missing
  extract_translations.py seed <locale>   create the work file translations/work/<locale>.json
  extract_translations.py apply <work> <locale>
                                          write the work file back to translations/poexports/<locale>.json
                                          (the work file is deleted afterwards)
"""


def main():
    script_dir = Path(__file__).parent
    argv = sys.argv[1:]
    cmd = argv[0] if argv else 'extract'

    if cmd in ('-h', '--help', 'help'):
        print(USAGE)
    elif cmd == 'extract':
        if not cmd_extract(script_dir):
            sys.exit(1)
    elif cmd == 'status' and len(argv) == 1:
        cmd_status(script_dir)
    elif cmd == 'seed' and len(argv) == 2:
        cmd_seed(script_dir, argv[1])
    elif cmd == 'apply' and len(argv) == 3:
        cmd_apply(script_dir, argv[1], argv[2])
    else:
        print(USAGE)
        sys.exit(2)


if __name__ == '__main__':
    main()
