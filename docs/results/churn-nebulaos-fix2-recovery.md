### Campaign against `nebulaos-fix2-recovery`

| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |
|---|---|---|---|---|---|---|
| leader-churn | 15 | 148342 | 210 | **4** | value reappeared ×1, write reported as failed took effect ×3 | 0 |
| **total** | **15** | **148342** | **210** | **4** | | |

Write availability: longest gap between successful writes in the 10 s after each fault.

| fault | count | median stall | worst stall |
|---|---|---|---|
| kill | 210 | 0.80 s | 1.73 s |

_15 runs (re-checked)._
