### Campaign against `nebulaos-fixed`

| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |
|---|---|---|---|---|---|---|
| crash-elections | 10 | 69647 | 130 | **0** |  | 0 |
| **total** | **10** | **69647** | **130** | **0** | | |

Write availability: longest gap between successful writes in the 10 s after each fault.

| fault | count | median stall | worst stall |
|---|---|---|---|
| kill | 130 | 0.71 s | 1.60 s |

_10 runs (re-checked)._
