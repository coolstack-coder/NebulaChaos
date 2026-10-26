| planted bug | what it breaks | fault schedule | control run (no bug) | caught? | anomaly | ops checked |
|---|---|---|---|---|---|---|
| `leader-local-reads` | leader answers reads without confirming it still leads | leader-alone-repeated | ✓ linearizable | **yes**, attempt 2 | stale read | 22656 |
| `ack-before-commit` | leader acknowledges a write before a majority has it | isolate-leader | ✓ linearizable | **yes**, attempt 1 | stale read | 8228 |
| `leader-skips-fsync` | leader counts its own log as durable without fsync | powerloss-cluster | ✓ linearizable | **yes**, attempt 4 | stale read | 33525 |
| `forget-vote` | vote is not persisted; a restarted node can vote twice in a term | crash-elections | ✓ linearizable | not in 5 attempts | — | 36277 |
