"""Decide when a satellite should dodge, and by how much.

A satellite is warned two days ahead that something will pass close to it.
The warning is vague: predictions from tracking data are uncertain by about
a kilometre per day of look-ahead, so most warnings turn out to be nothing.
Fresh tracking arrives every few hours and narrows the uncertainty.

The operator can fire the thruster along the direction of travel. A burn
made early moves the satellite a long way by the time of the pass for very
little fuel, but it is made on vague information and is often unnecessary.
A burn made late is well informed but has little time to act, so it needs
more fuel for the same shift. This module simulates that trade-off for
many warnings at once.

Units are km, seconds and km/s. Positions are in the satellite's own frame:
radial (away from the Earth), in-track (direction of travel), cross-track.

The uncertainty model below is an assumption, chosen to match the commonly
quoted growth of public tracking-data error in low orbit. It is not fitted
to any operator's data, and results depend on it.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np

# One-sigma position uncertainty of the predicted pass, both objects
# combined: a floor, plus growth per day of look-ahead.
SIGMA_FLOOR_KM = np.array([0.03, 0.10, 0.03])
SIGMA_PER_DAY_KM = np.array([0.10, 1.00, 0.10])

DV_MAX_KMS = 5e-5  # largest single burn: 5 cm/s
KEEP_OUT_KM = 0.2  # a pass closer than this counts as a failure
HARD_BODY_KM = 0.02  # closer than this is a collision
DECISION_HOURS = (48.0, 36.0, 24.0, 18.0, 12.0, 8.0, 5.0, 3.0)
DEADBAND = 0.05  # actions smaller than this mean "do nothing"

FUEL_COST = 1.0  # cost of one full-size burn
BURN_COST = 0.1  # fixed cost of interrupting operations for any burn
FAILURE_COST = 20.0  # cost of ending inside the keep-out distance


def sigma(hours_ahead: np.ndarray | float) -> np.ndarray:
    """One-sigma uncertainty (radial, in-track, cross-track) of a prediction."""
    hours_ahead = np.asarray(hours_ahead, dtype=float)
    return SIGMA_FLOOR_KM + SIGMA_PER_DAY_KM * hours_ahead[..., None] / 24.0


def burn_shift(
    dv: np.ndarray, seconds_before: np.ndarray, mean_motion: np.ndarray
) -> np.ndarray:
    """Where an in-track burn puts the satellite at the time of the pass.

    Returns the change in its (radial, in-track, cross-track) position, from the Clohessy-Wiltshire solution for a
    near-circular orbit. Speeding up raises the orbit, and a higher orbit is slower, so the satellite ends up above and
    behind where it would have been.
    """
    angle = mean_motion * seconds_before
    radial = 2 * dv / mean_motion * (1 - np.cos(angle))
    in_track = 4 * dv / mean_motion * np.sin(angle) - 3 * dv * seconds_before
    return np.stack([radial, in_track, np.zeros_like(radial)], axis=-1)


def encounter_plane(direction: np.ndarray) -> np.ndarray:
    """Two unit vectors spanning the plane square to the relative velocity.

    Only the part of the separation that lies in this plane matters: the part along the relative velocity changes when
    the pass happens, not how close it is. Returns an array of shape (..., 2, 3).
    """
    direction = direction / np.linalg.norm(direction, axis=-1, keepdims=True)
    helper = np.where(
        np.abs(direction[..., :1]) < 0.9, [1.0, 0.0, 0.0], [0.0, 1.0, 0.0]
    )
    first = np.cross(direction, helper)
    first /= np.linalg.norm(first, axis=-1, keepdims=True)
    second = np.cross(direction, first)
    return np.stack([first, second], axis=-2)


_NODES, _NODE_WEIGHTS = np.polynomial.legendre.leggauss(12)
_ANGLES = np.linspace(0, 2 * np.pi, 32, endpoint=False)
_COS, _SIN = np.cos(_ANGLES), np.sin(_ANGLES)
_CHUNK = 16384  # rows integrated at a time, to bound memory


def probability_within(
    mean: np.ndarray, covariance: np.ndarray, radius: float
) -> np.ndarray:
    """Chance that a 2D normal point lands within radius of the origin.

    mean has shape (n, 2) and covariance (n, 2, 2). The integral over the
    disc is done by quadrature in polar coordinates, a chunk of rows at a
    time.
    """
    r = 0.5 * radius * (_NODES + 1)
    weight = (0.5 * radius * _NODE_WEIGHTS * r * (2 * np.pi / len(_ANGLES)))[:, None]
    x, y = np.outer(r, _COS), np.outer(r, _SIN)  # (12, 32)
    out = np.empty(len(mean))
    for start in range(0, len(mean), _CHUNK):
        rows = slice(start, start + _CHUNK)
        a, b, d = covariance[rows, 0, 0], covariance[rows, 0, 1], covariance[rows, 1, 1]
        det = a * d - b * b
        dx = x - mean[rows, 0, None, None]
        dy = y - mean[rows, 1, None, None]
        exponent = (
            d[:, None, None] * dx * dx
            - 2 * b[:, None, None] * dx * dy
            + a[:, None, None] * dy * dy
        ) / det[:, None, None]
        density = np.exp(-0.5 * exponent) / (2 * np.pi * np.sqrt(det))[:, None, None]
        out[rows] = (density * weight).sum(axis=(1, 2))
    return np.clip(out, 0.0, 1.0)


@dataclass
class Warnings:
    """A batch of close-approach warnings being handled in parallel."""

    count: int
    rng: np.random.Generator
    mean_motion: np.ndarray = field(init=False)
    plane: np.ndarray = field(init=False)  # (count, 2, 3)
    truth: np.ndarray = field(init=False)  # true separation at the pass, km
    estimate: np.ndarray = field(init=False)  # latest estimate of it
    step: int = field(init=False, default=0)
    fuel: np.ndarray = field(init=False)  # total burn size, in units of DV_MAX
    burns: np.ndarray = field(init=False)

    def __post_init__(self) -> None:
        """Draw the warnings: orbits, pass geometry, first estimate, truth."""
        n, rng = self.count, self.rng
        period = rng.uniform(90, 105, n) * 60
        self.mean_motion = 2 * np.pi / period
        # Objects in low orbit cross each other almost horizontally.
        heading = rng.uniform(0, 2 * np.pi, n)
        direction = np.stack(
            [0.02 * rng.standard_normal(n), np.cos(heading), np.sin(heading)], axis=-1
        )
        self.plane = encounter_plane(direction)
        # Every warning starts out looking close: under a kilometre.
        distance = rng.uniform(0, 1.0, n)
        bearing = rng.uniform(0, 2 * np.pi, n)
        in_plane = np.stack(
            [distance * np.cos(bearing), distance * np.sin(bearing)], axis=-1
        )
        self.estimate = np.einsum("ni,nij->nj", in_plane, self.plane)
        # The truth is somewhere within the uncertainty of that first estimate.
        self.truth = self.estimate + rng.standard_normal((n, 3)) * sigma(
            DECISION_HOURS[0]
        )
        self.fuel = np.zeros(n)
        self.burns = np.zeros(n)
        self.step = 0

    @property
    def hours_ahead(self) -> float:
        """Hours until the pass at the current decision."""
        return DECISION_HOURS[self.step]

    def in_plane(self, separation: np.ndarray) -> np.ndarray:
        """Project a separation onto the plane that decides the miss distance."""
        return np.einsum("nij,nj->ni", self.plane, separation)

    def covariance(self) -> np.ndarray:
        """Uncertainty of the current estimate, in that plane."""
        variance = sigma(self.hours_ahead) ** 2
        return np.einsum("nij,j,nkj->nik", self.plane, variance, self.plane)

    def burn_effect(self) -> np.ndarray:
        """How a full-size forward burn now would move the estimate, in the plane."""
        shift = burn_shift(
            np.full(self.count, DV_MAX_KMS), self.hours_ahead * 3600.0, self.mean_motion
        )
        return self.in_plane(-shift)

    def risk(self, extra: np.ndarray | None = None) -> np.ndarray:
        """Chance, on current knowledge, of ending inside the keep-out distance.

        extra is an optional shift of the estimate in the plane, used to ask
        "what if I burned this much?".
        """
        mean = self.in_plane(self.estimate)
        if extra is not None:
            mean = mean + extra
        return probability_within(mean, self.covariance(), KEEP_OUT_KM)

    def observe(self) -> np.ndarray:
        """What the decision-maker sees: 10 numbers per warning."""
        mean = self.in_plane(self.estimate)
        cov = self.covariance()
        scale = np.sqrt(np.trace(cov, axis1=-2, axis2=-1) / 2)[:, None]
        effect = self.burn_effect()
        risk = self.risk()
        return np.concatenate(
            [
                mean / scale,
                effect / scale,
                np.stack(
                    [
                        cov[:, 0, 0] / scale[:, 0] ** 2,
                        cov[:, 0, 1] / scale[:, 0] ** 2,
                        np.log10(scale[:, 0]),
                        np.log10(np.maximum(risk, 1e-12)) / 6 + 1,
                        np.full(self.count, self.step / (len(DECISION_HOURS) - 1)),
                        self.fuel / 4,
                    ],
                    axis=-1,
                ),
            ],
            axis=-1,
        ).astype(np.float32)

    def act(self, action: np.ndarray) -> tuple[np.ndarray, bool]:
        """Apply one decision per warning: a burn in [-1, 1] of full size.

        Returns the reward for this step and whether the episode is over.
        """
        action = np.clip(np.asarray(action, dtype=float), -1, 1)
        action = np.where(np.abs(action) < DEADBAND, 0.0, action)
        shift = burn_shift(
            action * DV_MAX_KMS, self.hours_ahead * 3600.0, self.mean_motion
        )
        # The satellite moves, so the separation changes by the opposite amount.
        self.truth = self.truth - shift
        burned = action != 0
        self.fuel += np.abs(action)
        self.burns += burned
        reward = -(FUEL_COST * np.abs(action) + BURN_COST * burned)

        self.step += 1
        if self.step == len(DECISION_HOURS):
            self.step -= 1
            reward = reward - FAILURE_COST * (self.miss_distance() < KEEP_OUT_KM)
            return reward, True
        # Fresh tracking: a new, sharper estimate of where things really are.
        self.estimate = self.truth + self.rng.standard_normal((self.count, 3)) * sigma(
            self.hours_ahead
        )
        return reward, False

    def miss_distance(self) -> np.ndarray:
        """True distance of closest approach, km."""
        return np.linalg.norm(self.in_plane(self.truth), axis=-1)


def run(policy, count: int, seed: int) -> dict[str, float]:
    """Play count warnings through with policy and summarise the outcome."""
    warnings = Warnings(count, np.random.default_rng(seed))
    total = np.zeros(count)
    done = False
    while not done:
        reward, done = warnings.act(policy(warnings))
        total += reward
    miss = warnings.miss_distance()
    return {
        "cost": float(-total.mean()),
        "keep_out_failures_per_1000": float(1000 * (miss < KEEP_OUT_KM).mean()),
        "collisions_per_million": float(1e6 * (miss < HARD_BODY_KM).mean()),
        "fuel_cm_s": float(warnings.fuel.mean() * DV_MAX_KMS * 1e5),
        "burns_per_warning": float(warnings.burns.mean()),
        "warnings_with_a_burn": float((warnings.burns > 0).mean()),
    }


def never(warnings: Warnings) -> np.ndarray:
    """Do nothing, ever."""
    return np.zeros(warnings.count)


def threshold_rule(decide_at: int, act_above: float, aim_below: float):
    """Build the rule operators use: act once the estimated risk is high enough.

    From decision number decide_at on, if the estimated risk of ending inside the keep-out distance is above act_above,
    make the smallest burn that brings it below aim_below (or the most helpful burn, if none does).
    """
    candidates = np.linspace(-1, 1, 41)
    candidates = candidates[np.abs(candidates) >= DEADBAND]
    order = np.argsort(np.abs(candidates))
    candidates = candidates[order]

    def policy(warnings: Warnings) -> np.ndarray:
        action = np.zeros(warnings.count)
        if warnings.step < decide_at:
            return action
        needed = warnings.risk() > act_above
        if not needed.any():
            return action
        # Only the warnings that call for a burn are worked through.
        mean = warnings.in_plane(warnings.estimate)[needed]
        covariance = warnings.covariance()[needed]
        effect = warnings.burn_effect()[needed]
        risks = np.stack(
            [
                probability_within(mean + effect * c, covariance, KEEP_OUT_KM)
                for c in candidates
            ],
            axis=-1,
        )
        good_enough = risks <= aim_below
        smallest = np.where(
            good_enough.any(axis=-1), good_enough.argmax(axis=-1), risks.argmin(axis=-1)
        )
        action[needed] = candidates[smallest]
        return action

    return policy
