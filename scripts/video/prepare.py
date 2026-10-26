#!/usr/bin/env python3
"""Turn a NebulaChaos run directory into the JSON the replay page animates.

    python3 scripts/video/prepare.py runs/<run> [key] > replay.json

Everything comes from the run's own artifacts: history.jsonl (client
operations), nemesis.json (faults), check.json (verdict and minimal
counterexample) and the node logs (leader elections and step-downs).
"""
import datetime
import glob
import json
import os
import re
import sys

run = sys.argv[1]
summary = json.load(open(os.path.join(run, "summary.json")))
check = json.load(open(os.path.join(run, "check.json")))
faults = json.load(open(os.path.join(run, "nemesis.json")))
ops = [json.loads(l) for l in open(os.path.join(run, "history.jsonl"))]
start_ms = summary.get("start_unix_ms", 0)

# One counterexample is shown: the key named on the command line, or the
# earliest violation in the run.
want = sys.argv[2] if len(sys.argv) > 2 else None
cands = [k for k in check["keys"] if k.get("minimal") and (want is None or k["key"] == want)]
cands.sort(key=lambda k: max(o["complete"] for o in k["minimal"]))
bad_ids, anomaly = set(), None
if cands:
    k = cands[0]
    bad_ids = {o["id"] for o in k["minimal"]}
    anomaly = {"key": k["key"], "kind": k["anomaly"], "explain": k["explain"], "ops": sorted(bad_ids)}

# Leadership changes from the node logs (absolute wall clock -> run time).
if not start_ms:  # runs recorded before start_unix_ms existed: approximate
    first = min(open(f).read().split("time=", 2)[1][:29] for f in glob.glob(os.path.join(run, "logs", "*.log")))
    start_ms = datetime.datetime.fromisoformat(first).timestamp() * 1000 + 600
events = []
pat = re.compile(r'time=(\S+) level=\w+ msg="?(became leader|stepping down|restored)"?.*? id=(n\d)(?: term=(\d+))?')
for path in glob.glob(os.path.join(run, "logs", "*.log")):
    for line in open(path, errors="replace"):
        m = pat.search(line)
        if not m:
            continue
        t = datetime.datetime.fromisoformat(m.group(1)).timestamp() * 1000 - start_ms
        kind = {"became leader": "leader", "stepping down": "follower", "restored": "boot"}[m.group(2)]
        events.append({"t": t / 1000, "node": m.group(3), "kind": kind, "term": int(m.group(4) or 0)})
events.sort(key=lambda e: e["t"])

def groups(detail):
    return [g.split() for g in re.findall(r"\[([^\]]*)\]", detail)]

out = {
    "name": summary["name"], "target": summary["target"], "seed": summary["seed"],
    "bugs": summary.get("bugs", ""), "valid": summary["valid"],
    "duration": summary["duration_s"], "ops_total": summary["ops"],
    "refused": summary.get("refused_to_start") or {},
    "events": events,
    "faults": [{"at": f["at"] / 1e9, "end": (f["end"] or f["at"]) / 1e9, "do": f["do"],
                "nodes": ["n%d" % n for n in (f.get("nodes") or [])], "detail": f["detail"],
                "groups": groups(f["detail"]) if f["do"] == "partition" else []} for f in faults],
    "ops": [{"id": o["id"], "c": o["client"], "p": o["process"], "n": o["node"], "k": o["kind"], "key": o["key"],
             "v": o.get("value", ""), "x": o.get("expect", ""), "f": o.get("found", False),
             "i": o["invoke"] / 1e9, "e": o["complete"] / 1e9, "s": o["status"],
             "bad": o["id"] in bad_ids} for o in ops],
    "anomaly": anomaly,
}
json.dump(out, sys.stdout, separators=(",", ":"))
