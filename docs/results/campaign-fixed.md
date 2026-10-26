### Campaign against `nebulaos-fixed`

| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |
|---|---|---|---|---|---|---|
| partitions | 12 | 47916 | 40 | **0** |  | 0 |
| pauses | 12 | 51342 | 42 | **0** |  | 0 |
| crashes | 12 | 61757 | 52 | **0** |  | 0 |
| disk | 12 | 65241 | 96 | **0** |  | 12 |
| mixed | 12 | 58476 | 51 | **0** |  | 6 |
| **total** | **60** | **284732** | **281** | **0** | | |

Write availability: longest gap between successful writes in the 10 s after each fault.

| fault | count | median stall | worst stall |
|---|---|---|---|
| corrupt-snapshot | 4 | 0.84 s | 0.85 s |
| delay | 4 | 3.06 s | 3.75 s |
| delete-snapshot | 3 | 0.77 s | 1.57 s |
| flip-log | 6 | 0.80 s | 3.45 s |
| kill | 39 | 0.66 s | 1.49 s |
| partition | 56 | 0.75 s | 3.76 s |
| pause | 48 | 0.96 s | 1.08 s |
| powerloss | 116 | 0.73 s | 1.58 s |
| snapshot-header | 5 | 0.81 s | 0.95 s |

_60 runs (re-checked)._
