.pragma library

// Display-only normalization. Fixes whitespace that would render wrong; never
// changes the text itself.

// Qt positions tabs against a fixed 80 px tab stop (the QTextOption default,
// unrelated to font size), which makes indentation inside code blocks look far
// too wide. TextEdit.tabStopDistance does not reliably apply to rich text (the
// <pre> that chroma emits), so tabs are expanded before display. Copying still
// uses the original text: CodeBlock copies root.code, not this output.
function expandTabs(s) {
    const t = (s === undefined || s === null) ? "" : String(s);
    return t.replace(/\t/g, "    ");
}
