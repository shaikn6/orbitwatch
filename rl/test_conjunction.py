"""Checks of the simulator against independent calculations."""

import itertools

import numpy as np
import pytest

import conjunction as cj

MU = 398600.4418  # km^3/s^2


def integrate(position, velocity, seconds, steps=4000):
    """Fly a point mass around the Earth with fourth-order Runge-Kutta."""
    state = np.concatenate([position, velocity])
    h = seconds / steps

    def rate(s):
        r = s[:3]
        return np.concatenate([s[3:], -MU * r / np.linalg.norm(r) ** 3])

    for _ in range(steps):
        k1 = rate(state)
        k2 = rate(state + 0.5 * h * k1)
        k3 = rate(state + 0.5 * h * k2)
        k4 = rate(state + h * k3)
        state = state + h / 6 * (k1 + 2 * k2 + 2 * k3 + k4)
    return state[:3], state[3:]


@pytest.mark.parametrize("hours", [0.75, 1.5, 6.0, 12.0, 30.0])
def test_burn_shift_matches_a_numerical_orbit(hours):
    radius = 6378.137 + 550
    speed = np.sqrt(MU / radius)
    n = speed / radius
    dv = 3e-5  # 3 cm/s
    start, heading = np.array([radius, 0.0, 0.0]), np.array([0.0, speed, 0.0])
    seconds = hours * 3600

    plain_r, plain_v = integrate(start, heading, seconds)
    burned_r, _ = integrate(start, heading + np.array([0.0, dv, 0.0]), seconds)
    radial_axis = plain_r / np.linalg.norm(plain_r)
    track_axis = plain_v / np.linalg.norm(plain_v)
    actual = np.array(
        [(burned_r - plain_r) @ radial_axis, (burned_r - plain_r) @ track_axis]
    )

    predicted = cj.burn_shift(np.array([dv]), np.array([seconds]), np.array([n]))[0, :2]
    assert np.linalg.norm(actual - predicted) < 0.01 * np.linalg.norm(actual) + 1e-4, (
        actual,
        predicted,
    )
    # The headline effect: 3 cm/s half a day ahead moves the satellite by kilometres.
    if hours == 12.0:
        assert 3.5 < abs(actual[1]) < 4.5


def test_probability_matches_the_exact_answer_for_a_round_centred_spread():
    for spread, radius in [(0.5, 0.2), (0.1, 0.2), (0.05, 0.02), (2.0, 0.2)]:
        got = cj.probability_within(
            np.zeros((1, 2)), np.eye(2)[None] * spread**2, radius
        )[0]
        exact = 1 - np.exp(-(radius**2) / (2 * spread**2))
        assert got == pytest.approx(exact, rel=2e-3), (spread, radius)


def test_probability_matches_sampling_for_a_skewed_offset_spread():
    rng = np.random.default_rng(0)
    mean = np.array([0.25, -0.1])
    cov = np.array([[0.30, 0.12], [0.12, 0.08]])
    samples = rng.multivariate_normal(mean, cov, size=2_000_000)
    sampled = (np.linalg.norm(samples, axis=1) < cj.KEEP_OUT_KM).mean()
    got = cj.probability_within(mean[None], cov[None], cj.KEEP_OUT_KM)[0]
    assert got == pytest.approx(sampled, rel=0.02)


def test_encounter_plane_is_square_to_the_relative_velocity():
    rng = np.random.default_rng(1)
    direction = rng.standard_normal((500, 3))
    plane = cj.encounter_plane(direction)
    unit = direction / np.linalg.norm(direction, axis=1, keepdims=True)
    assert np.abs(np.einsum("nij,nj->ni", plane, unit)).max() < 1e-12
    assert np.allclose(np.einsum("nij,nkj->nik", plane, plane), np.eye(2), atol=1e-12)


def test_estimates_sharpen_as_the_pass_gets_closer():
    warnings = cj.Warnings(50_000, np.random.default_rng(2))
    errors = []
    done = False
    while not done:
        errors.append(np.linalg.norm(warnings.estimate - warnings.truth, axis=1).mean())
        _, done = warnings.act(np.zeros(warnings.count))
    assert all(later < earlier for earlier, later in itertools.pairwise(errors)), errors
    assert errors[0] > 1.5 and errors[-1] < 0.3, errors


def test_the_stated_risk_is_honest():
    """Among warnings rated about 10% at the last decision, about 10% fail."""
    warnings = cj.Warnings(400_000, np.random.default_rng(3))
    for _ in range(len(cj.DECISION_HOURS) - 1):
        warnings.act(np.zeros(warnings.count))
    risk = warnings.risk()
    failed = warnings.miss_distance() < cj.KEEP_OUT_KM
    for low, high in [(0.02, 0.05), (0.08, 0.12), (0.3, 0.4)]:
        chosen = (risk > low) & (risk < high)
        assert chosen.sum() > 800
        assert failed[chosen].mean() == pytest.approx(risk[chosen].mean(), rel=0.12), (
            low,
            high,
        )


def test_a_burn_moves_the_truth_by_the_predicted_amount_and_costs_fuel():
    warnings = cj.Warnings(1000, np.random.default_rng(4))
    before = warnings.in_plane(warnings.truth)
    effect = warnings.burn_effect()
    reward, done = warnings.act(np.full(warnings.count, 0.5))
    assert not done
    assert np.allclose(
        warnings.in_plane(warnings.truth) - before, 0.5 * effect, atol=1e-9
    )
    assert np.allclose(reward, -(0.5 * cj.FUEL_COST + cj.BURN_COST))
    # Inside the deadband nothing happens and nothing is charged.
    still = warnings.in_plane(warnings.truth).copy()
    reward, _ = warnings.act(np.full(warnings.count, 0.01))
    assert np.allclose(warnings.in_plane(warnings.truth), still) and np.all(reward == 0)


def test_runs_are_repeatable_and_the_rule_beats_doing_nothing():
    rule = cj.threshold_rule(decide_at=4, act_above=0.01, aim_below=0.001)
    first, again = cj.run(rule, 30_000, seed=5), cj.run(rule, 30_000, seed=5)
    assert first == again
    idle = cj.run(cj.never, 30_000, seed=5)
    assert idle["fuel_cm_s"] == 0 and idle["keep_out_failures_per_1000"] > 5
    assert (
        first["keep_out_failures_per_1000"] < 0.3 * idle["keep_out_failures_per_1000"]
    )
    assert first["cost"] < idle["cost"]
