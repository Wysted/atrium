"""Checks the Markdown docs; run by local/lint.sh (docs mode) in a pinned Python image.

Usage: python3 check-docs.py <repo_root> <markdown file>...
  1. Every relative link in the given files points to an existing file, and every #anchor to a heading of the target.
  2. Every page under docs/, and every given file outside it except UNTRANSLATED, has its Spanish copy.
  3. Every Spanish copy has its English original and the same fenced code blocks (``` or ~~~, also indented).
     docs/es/X maps to docs/X; docs/es/repo/X maps to X (docs that live outside docs/).
"""

import os
import re
import sys
import unicodedata

LINK = re.compile(r"\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
FENCE = re.compile(r"^\s*(```|~~~)")
ES = os.path.join("docs", "es")
# Tracked docs outside docs/ that have no Spanish copy on purpose.
UNTRANSLATED = {"TODO.md"}


def lines(path):
    """Yields (line number, line, inside a fenced code block); fence lines count as inside."""
    fenced = False
    with open(path, encoding="utf-8") as f:
        for no, line in enumerate(f, 1):
            fence = bool(FENCE.match(line))
            fenced ^= fence
            yield no, line, fenced or fence


def prose_lines(path):
    return ((no, line) for no, line, code in lines(path) if not code)


def code_lines(path):
    return [line for _, line, code in lines(path) if code]


def slug(text):
    """GitHub heading anchor: lowercase, letters/digits/'-'/'_' kept, spaces to '-', the rest dropped."""
    text = re.sub(r"`|\*|_(?=\W)|\[|\]\([^)]*\)", "", text).lower()
    return "".join(
        "-" if ch == " " else ch for ch in text if ch in " -_" or unicodedata.category(ch)[0] in "LN"
    )


def anchors(path, cache={}):
    if path not in cache:
        seen = {}
        for _, line in prose_lines(path):
            m = HEADING.match(line)
            if m:
                s = slug(m.group(2))
                seen[s] = seen.get(s, -1) + 1
                cache.setdefault(path, set()).add(s if seen[s] == 0 else f"{s}-{seen[s]}")
        cache.setdefault(path, set())
    return cache[path]


def check_links(root, files):
    problems = []
    for md in files:
        for no, line in prose_lines(md):
            for target in LINK.findall(re.sub(r"`[^`]*`", "", line)):
                if re.match(r"^[a-z]+:", target):
                    continue
                file_part, _, frag = target.partition("#")
                dest = os.path.normpath(os.path.join(os.path.dirname(md), file_part)) if file_part else md
                if os.path.relpath(dest, root).startswith(".."):
                    problems.append(f"{md}:{no}: link leaves the repo: {target}")
                elif not os.path.exists(dest):
                    problems.append(f"{md}:{no}: missing file: {target}")
                elif frag and dest.endswith(".md") and frag not in anchors(dest):
                    problems.append(f"{md}:{no}: missing anchor: {target}")
    return problems


def md_under(path):
    for d, _, names in os.walk(path):
        yield from (os.path.join(d, n) for n in names if n.endswith(".md"))


def check_translations(files):
    problems = []
    outside = [f for f in files if not f.startswith("docs" + os.sep) and f not in UNTRANSLATED]
    for en in [*md_under("docs"), *outside]:
        if not en.startswith(ES + os.sep):
            rel = os.path.relpath(en, "docs") if en.startswith("docs" + os.sep) else os.path.join("repo", en)
            es = os.path.join(ES, rel)
            if not os.path.exists(es):
                problems.append(f"{en}: no Spanish copy at {es}")
    for es in md_under(ES):
        rel = os.path.relpath(es, ES)
        en = rel[len("repo" + os.sep):] if rel.startswith("repo" + os.sep) else os.path.join("docs", rel)
        if not os.path.exists(en):
            problems.append(f"{es}: no English original at {en}")
            continue
        if code_lines(en) != code_lines(es):
            problems.append(f"{es}: code blocks differ from {en} (keep them identical)")
    return problems


def main():
    root, files = sys.argv[1], sys.argv[2:]
    os.chdir(root)
    files = [os.path.relpath(f, root) for f in files]
    problems = check_links(".", files) + check_translations(files)
    print("\n".join(problems) or f"docs: {len(files)} files OK")
    sys.exit(1 if problems else 0)


main()
