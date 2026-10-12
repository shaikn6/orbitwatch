"""Train a dodge policy with PPO and score it against the tuned rule.

The policy sees the ten numbers of ``Warnings.observe`` and makes two
choices at each decision: whether to burn at all, and if so how hard and in
which direction. It is trained on freshly drawn warnings every iteration
and scored, with exploration switched off, on the same million test
warnings as the baselines.
"""

from __future__ import annotations

import argparse
import json
import time
from pathlib import Path

import numpy as np
import torch
from torch import nn

import conjunction as cj
from baseline import TEST_COUNT, TEST_SEED

OBSERVATION_SIZE = 10
STEPS = len(cj.DECISION_HOURS)


class Policy(nn.Module):
    """Shared body with three heads: burn or not, burn size, and value."""

    def __init__(self, hidden: int = 64) -> None:
        """Build the network; it starts out preferring not to burn."""
        super().__init__()
        self.body = nn.Sequential(
            nn.Linear(OBSERVATION_SIZE, hidden),
            nn.Tanh(),
            nn.Linear(hidden, hidden),
            nn.Tanh(),
        )
        self.gate = nn.Linear(hidden, 1)
        self.size = nn.Linear(hidden, 1)
        self.value = nn.Linear(hidden, 1)
        self.log_std = nn.Parameter(torch.tensor(-1.0))
        nn.init.constant_(self.gate.bias, -2.0)

    def forward(
        self, obs: torch.Tensor
    ) -> tuple[torch.Tensor, torch.Tensor, torch.Tensor]:
        """Return the burn logit, the mean burn size and the state value."""
        hidden = self.body(obs)
        return (
            self.gate(hidden).squeeze(-1),
            self.size(hidden).squeeze(-1),
            self.value(hidden).squeeze(-1),
        )

    def log_prob(self, obs: torch.Tensor, burn: torch.Tensor, size: torch.Tensor):
        """Log-probability of a (burn, size) choice, its entropy and the value."""
        logit, mean, value = self(obs)
        gate = torch.distributions.Bernoulli(logits=logit)
        amount = torch.distributions.Normal(mean, self.log_std.exp())
        # The size only matters when a burn happens.
        log_prob = gate.log_prob(burn) + burn * amount.log_prob(size)
        entropy = gate.entropy() + gate.probs * amount.entropy()
        return log_prob, entropy, value

    @torch.no_grad()
    def decide(self, warnings: cj.Warnings) -> np.ndarray:
        """Act without exploration: burn when it is the likelier choice."""
        logit, mean, _ = self(torch.from_numpy(warnings.observe()))
        return torch.where(logit > 0, mean.clamp(-1, 1), torch.zeros_like(mean)).numpy()


def collect(policy: Policy, count: int, rng: np.random.Generator):
    """Play one batch of warnings to the end with exploration on."""
    warnings = cj.Warnings(count, rng)
    obs, burns, sizes, log_probs, values, rewards = [], [], [], [], [], []
    done = False
    while not done:
        o = torch.from_numpy(warnings.observe())
        with torch.no_grad():
            logit, mean, value = policy(o)
            burn = torch.bernoulli(torch.sigmoid(logit))
            size = mean + policy.log_std.exp() * torch.randn_like(mean)
            log_prob, _, _ = policy.log_prob(o, burn, size)
        action = (burn * size.clamp(-1, 1)).numpy()
        # A sampled burn inside the deadband would be silently dropped; push
        # it to the edge so that "burn" always means a burn.
        action = np.where(
            (burn.numpy() > 0) & (np.abs(action) < cj.DEADBAND),
            np.sign(action + 1e-9) * cj.DEADBAND,
            action,
        )
        reward, done = warnings.act(action)
        obs.append(o)
        burns.append(burn)
        sizes.append(size)
        log_probs.append(log_prob)
        values.append(value)
        rewards.append(torch.from_numpy(reward).float())
    return (torch.stack(x) for x in (obs, burns, sizes, log_probs, values, rewards))


def train(seed: int, iterations: int, batch: int, log_every: int = 25) -> Policy:
    """Run PPO and return the trained policy."""
    torch.manual_seed(seed)
    rng = np.random.default_rng(seed)
    policy = Policy()
    optimizer = torch.optim.Adam(policy.parameters(), lr=3e-4)
    # The step size shrinks to a tenth by the end, so late training refines
    # the policy instead of jittering around it.
    schedule = torch.optim.lr_scheduler.LinearLR(optimizer, start_factor=1.0, end_factor=0.1, total_iters=iterations)
    for iteration in range(1, iterations + 1):
        obs, burns, sizes, old_log_probs, values, rewards = collect(policy, batch, rng)
        # Generalised advantage estimation over the eight decisions; episodes
        # are short and always end, so nothing is discounted.
        advantages = torch.zeros_like(rewards)
        running = torch.zeros(batch)
        for t in reversed(range(STEPS)):
            next_value = values[t + 1] if t + 1 < STEPS else torch.zeros(batch)
            delta = rewards[t] + next_value - values[t]
            running = delta + 0.95 * running
            advantages[t] = running
        returns = advantages + values

        flat = lambda x: x.reshape(STEPS * batch, *x.shape[2:])
        obs, burns, sizes, old_log_probs, advantages, returns = map(
            flat, (obs, burns, sizes, old_log_probs, advantages, returns)
        )
        advantages = (advantages - advantages.mean()) / (advantages.std() + 1e-8)
        for _ in range(4):
            for index in torch.randperm(STEPS * batch).split(8192):
                log_prob, entropy, value = policy.log_prob(
                    obs[index], burns[index], sizes[index]
                )
                ratio = (log_prob - old_log_probs[index]).exp()
                clipped = ratio.clamp(0.8, 1.2)
                policy_loss = -torch.min(
                    ratio * advantages[index], clipped * advantages[index]
                ).mean()
                value_loss = (value - returns[index]).pow(2).mean()
                loss = policy_loss + 0.5 * value_loss - 0.003 * entropy.mean()
                optimizer.zero_grad()
                loss.backward()
                nn.utils.clip_grad_norm_(policy.parameters(), 0.5)
                optimizer.step()
        schedule.step()
        if iteration % log_every == 0 or iteration == iterations:
            print(
                f"seed {seed} iteration {iteration:4d}: cost while exploring {-rewards.sum(0).mean():.4f}, "
                f"burn rate {burns.mean():.3f}",
                flush=True,
            )
    return policy


def main() -> None:
    """Train one policy per seed and score each on the test warnings."""
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--seeds", type=int, default=3)
    parser.add_argument("--iterations", type=int, default=400)
    parser.add_argument("--batch", type=int, default=8192)
    parser.add_argument("--first-seed", type=int, default=1)
    parser.add_argument("--tag", default="ppo", help="name of the results file in runs/")
    args = parser.parse_args()
    torch.set_num_threads(4)

    report = {"args": vars(args), "seeds": {}}
    Path("runs").mkdir(exist_ok=True)
    for seed in range(args.first_seed, args.first_seed + args.seeds):
        began = time.time()
        policy = train(seed, args.iterations, args.batch)
        torch.save(policy.state_dict(), f"runs/{args.tag}_seed{seed}.pt")
        result = cj.run(policy.decide, TEST_COUNT, TEST_SEED)
        result["training_minutes"] = (time.time() - began) / 60
        report["seeds"][seed] = result
        print(
            f"seed {seed} on the test warnings: cost {result['cost']:.4f} | keep-out failures "
            f"{result['keep_out_failures_per_1000']:.2f} per 1000 | collisions {result['collisions_per_million']:.0f} per million | "
            f"fuel {result['fuel_cm_s']:.3f} cm/s | burned on {100 * result['warnings_with_a_burn']:.1f}% of warnings",
            flush=True,
        )
    costs = [r["cost"] for r in report["seeds"].values()]
    report["cost_mean"], report["cost_std"] = (
        float(np.mean(costs)),
        float(np.std(costs)),
    )
    Path(f"runs/{args.tag}.json").write_text(json.dumps(report, indent=2))
    print(
        f"learned policy cost: {report['cost_mean']:.4f} ± {report['cost_std']:.4f} over {args.seeds} seeds"
    )


if __name__ == "__main__":
    main()
