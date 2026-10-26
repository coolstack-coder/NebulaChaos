### Campaign against `nebulaos-fix2-recovery`

| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |
|---|---|---|---|---|---|---|
| crash-elections | 10 | 70624 | 130 | **1** | write reported as failed took effect ×1 | 0 |
| **total** | **10** | **70624** | **130** | **1** | | |

Write availability: longest gap between successful writes in the 10 s after each fault.

| fault | count | median stall | worst stall |
|---|---|---|---|
| kill | 130 | 0.75 s | 1.34 s |

_10 runs (re-checked)._
