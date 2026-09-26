#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 The ucs-exporter authors
#
# SPDX-License-Identifier: GPL-3.0-only
"""Extract UCSM class metadata from ucsmsdk for the classes the exporter uses.

The output (classes.json) is used by TestSDKMetadata to check that every
class and attribute the modules query exists, since typos would otherwise
only show up against a live UCS Manager. The metadata is derived from Cisco's
ucsmsdk (Apache License 2.0).

Usage:
    pip download ucsmsdk==0.9.27 --no-deps -d /tmp/sdk && unzip /tmp/sdk/*.whl -d /tmp/sdk
    go run ./cmd/ucs-exporter explore classes --names-only | \
        python3 testdata/sdkmeta/generate.py /tmp/sdk/ucsmsdk > testdata/sdkmeta/classes.json
"""

import ast
import glob
import json
import os
import re
import sys


def load(sdk, cls):
    hits = glob.glob(os.path.join(sdk, "mometa", "*", cls[0].upper() + cls[1:] + ".py"))
    if not hits:
        return None
    src = open(hits[0]).read()
    m = re.search(r"mo_meta = MoMeta\((.*)\)\n", src)
    args = ast.literal_eval("(" + re.sub(r"VersionMeta\.(\w+)", r'"\1"', m.group(1)) + ")")
    attrs = {}
    for line in src.splitlines():
        lm = re.match(r'\s+"\w+": MoPropertyMeta\((.*)\),\s*$', line)
        if not lm:
            continue
        body = re.sub(r"VersionMeta\.(\w+)", r'"\1"', lm.group(1))
        body = re.sub(r"MoPropertyMeta\.(\w+)", r'"\1"', body)
        t = ast.literal_eval("(" + body + ")")
        name, access = t[1], t[4]
        # Per-interval and history variants of statistics are never used.
        if access == "INTERNAL" or re.search(r"(Delta(Avg|Min|Max)?|15MinH|1Day|1DayH|1Hour|1HourH|1Week|1WeekH|2Weeks|2WeeksH)$", name):
            continue
        attrs[name] = t[9] if t[9] else t[2]
    return {"rn": args[2], "parents": args[8], "attrs": attrs}


def main():
    sdk = sys.argv[1]
    out = {}
    for cls in sorted({line.strip() for line in sys.stdin if line.strip()}):
        meta = load(sdk, cls)
        if meta is None:
            print(f"warning: {cls} not found in ucsmsdk", file=sys.stderr)
            continue
        out[cls] = meta
    # One class per line keeps diffs readable.
    sys.stdout.write('{"source": "ucsmsdk 0.9.27 (Apache-2.0); attrs map to their enum values or type",\n "classes": {\n')
    sys.stdout.write(",\n".join(f"  {json.dumps(c)}: {json.dumps(out[c], sort_keys=True)}" for c in sorted(out)))
    sys.stdout.write("\n }\n}")
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
