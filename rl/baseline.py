"""Tune the operators' threshold rule so the learned policy faces a fair opponent.

Every combination of "when to start deciding", "how much risk triggers a
burn" and "how low to push it" is scored on the same warnings, and the
cheapest is kept. The winner is then scored again on fresh warnings.
"""

from __future__ import annotations

import itertools
import json
from pathlib import Path

import conjunction as cj

TUNE_SEED, TEST_SEED = 100, 200
TUNE_COUNT, TEST_COUNT = 20_000, 1_000_000


def main() -> None:
    """Grid-search the rule, then report the best one and doing nothing."""
    trials = []
    for decide_at, act_above, aim_below in itertools.product(
        range(len(cj.DECISION_HOURS)),
        (0.3, 0.1, 0.03, 0.01, 0.003, 0.001),
        (1e-2, 1e-3, 1e-4),
    ):
        if aim_below >= act_above:
            continue
        result = cj.run(
            cj.threshold_rule(decide_at, act_above, aim_below), TUNE_COUNT, TUNE_SEED
        )
        trials.append((result["cost"], decide_at, act_above, aim_below))
    trials.sort()
    _, decide_at, act_above, aim_below = trials[0]
    print(
        "best five settings on the tuning warnings (cost, first decision, act above, aim below):"
    )
    for cost, d, a, b in trials[:5]:
        print(
            f"  {cost:.4f}  from {cj.DECISION_HOURS[d]:g} h before  act above {a:g}  aim below {b:g}"
        )

    report = {
        "rule": {
            "first_decision_hours": cj.DECISION_HOURS[decide_at],
            "act_above": act_above,
            "aim_below": aim_below,
        },
        "do nothing": cj.run(cj.never, TEST_COUNT, TEST_SEED),
        "tuned rule": cj.run(
            cj.threshold_rule(decide_at, act_above, aim_below), TEST_COUNT, TEST_SEED
        ),
    }
    Path("runs").mkdir(exist_ok=True)
    Path("runs/baseline.json").write_text(json.dumps(report, indent=2))
    for name in ("do nothing", "tuned rule"):
        r = report[name]
        print(
            f"{name:<11} cost {r['cost']:.4f} | keep-out failures {r['keep_out_failures_per_1000']:.2f} per 1000 | "
            f"collisions {r['collisions_per_million']:.0f} per million | fuel {r['fuel_cm_s']:.3f} cm/s | "
            f"burned on {100 * r['warnings_with_a_burn']:.1f}% of warnings"
        )


if __name__ == "__main__":
    main()
