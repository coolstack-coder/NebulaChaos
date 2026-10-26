### Campaign against `nebulaos-fixed`

| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |
|---|---|---|---|---|---|---|
| isolate-leader | 10 | 42852 | 20 | **0** |  | 0 |
| **total** | **10** | **42852** | **20** | **0** | | |

Write availability: longest gap between successful writes in the 10 s after each fault.

| fault | count | median stall | worst stall |
|---|---|---|---|
| partition | 20 | 1.00 s | 1.03 s |

_10 runs (re-checked)._
