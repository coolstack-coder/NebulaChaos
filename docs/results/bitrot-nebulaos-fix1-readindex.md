### Campaign against `nebulaos-fix1-readindex`

| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |
|---|---|---|---|---|---|---|
| bitrot-quorum | 10 | 64595 | 60 | **9** | stale read ×20 | 0 |
| **total** | **10** | **64595** | **60** | **9** | | |

Write availability: longest gap between successful writes in the 10 s after each fault.

| fault | count | median stall | worst stall |
|---|---|---|---|
| flip-log | 10 | 0.94 s | 7.65 s |
| kill | 20 | 1.04 s | 7.75 s |
| partition | 10 | 1.01 s | 1.50 s |

_10 runs (re-checked)._
