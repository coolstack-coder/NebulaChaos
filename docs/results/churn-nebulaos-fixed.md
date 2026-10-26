### Campaign against `nebulaos-fixed`

| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |
|---|---|---|---|---|---|---|
| leader-churn | 15 | 152916 | 210 | **0** |  | 0 |
| **total** | **15** | **152916** | **210** | **0** | | |

Write availability: longest gap between successful writes in the 10 s after each fault.

| fault | count | median stall | worst stall |
|---|---|---|---|
| kill | 210 | 0.80 s | 1.53 s |

_15 runs (re-checked)._
