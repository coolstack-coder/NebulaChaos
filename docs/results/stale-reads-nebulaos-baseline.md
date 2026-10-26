### Campaign against `nebulaos-baseline`

| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |
|---|---|---|---|---|---|---|
| isolate-leader | 10 | 93654 | 20 | **10** | stale read ×40 | 0 |
| **total** | **10** | **93654** | **20** | **10** | | |

Write availability: longest gap between successful writes in the 10 s after each fault.

| fault | count | median stall | worst stall |
|---|---|---|---|
| partition | 20 | 1.00 s | 1.02 s |

_10 runs (re-checked)._
