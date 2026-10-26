### Campaign against `nebulaos-fixed`

| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |
|---|---|---|---|---|---|---|
| bitrot-quorum | 10 | 47962 | 60 | **0** |  | 10 |
| **total** | **10** | **47962** | **60** | **0** | | |

Write availability: longest gap between successful writes in the 10 s after each fault.

| fault | count | median stall | worst stall |
|---|---|---|---|
| flip-log | 10 | 7.69 s | 7.92 s |
| kill | 20 | 7.78 s | 8.02 s |
| partition | 10 | 0.89 s | 1.00 s |

_10 runs (re-checked)._
