#!/usr/bin/env python3
"""Run a command and record its output line by line with timestamps, so
the video can replay the real output (sped up) in a terminal window.

    python3 scripts/video/capture.py out.json -- ./bin/nebulachaos run ...
"""
import json
import subprocess
import sys
import time

out = sys.argv[1]
cmd = sys.argv[sys.argv.index("--") + 1:]
shown = " ".join(c if " " not in c else repr(c) for c in cmd).replace("./bin/nebulachaos", "nebulachaos")
start = time.time()
lines = []
p = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)
for line in p.stdout:
    line = line.rstrip("\n")
    print(line, flush=True)
    lines.append({"t": round(time.time() - start, 3), "s": line})
p.wait()
json.dump({"cmd": shown, "lines": lines, "exit": p.returncode}, open(out, "w"))
