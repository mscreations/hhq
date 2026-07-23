#!/usr/bin/env python3
"""Beautify a minified JS file to stdout, for diff-friendly comparisons.

Usage: prettify_js.py <file>
Requires: pip install jsbeautifier
"""
import sys

try:
    import jsbeautifier
except ImportError:
    print(
        "error: the 'jsbeautifier' package is required (pip install jsbeautifier)",
        file=sys.stderr,
    )
    sys.exit(1)

if len(sys.argv) != 2:
    print("usage: prettify_js.py <file>", file=sys.stderr)
    sys.exit(1)

opts = jsbeautifier.default_options()
opts.indent_size = 2

with open(sys.argv[1], "r", encoding="utf-8") as f:
    source = f.read()

print(jsbeautifier.beautify(source, opts))
