// Checks the viewer's light orbit model against positions from the full
// SGP4 model on the server. Run: node --test web/
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { gmst, position, prepare } from "./orbit.js";

const dir = new URL("./data/", import.meta.url);
const catalog = JSON.parse(readFileSync(new URL("catalog.json", dir)));
const samples = JSON.parse(readFileSync(new URL("check.json", dir)));

test("light orbit model stays close to SGP4 for up to 40 hours", () => {
  const prepared = prepare(catalog);
  const index = new Map(catalog.id.map((id, i) => [id, i]));
  const out = new Float64Array(3);
  const errors = [];
  for (const sample of samples) {
    position(prepared, index.get(sample.id), sample.t, out, 0);
    errors.push(Math.hypot(out[0] - sample.r[0], out[1] - sample.r[1], out[2] - sample.r[2]));
  }
  errors.sort((a, b) => a - b);
  const median = errors[Math.floor(errors.length / 2)];
  const p95 = errors[Math.floor(errors.length * 0.95)];
  process.stdout.write(`  ${errors.length} objects: median ${median.toFixed(1)} km, 95% under ${p95.toFixed(1)} km, worst ${errors.at(-1).toFixed(1)} km\n`);
  assert.ok(errors.length >= 300, "too few reference positions");
  assert.ok(median < 30, `median error ${median} km`);
  assert.ok(p95 < 150, `95th percentile error ${p95} km`);
});

test("the Earth turns once per sidereal day", () => {
  const t = Date.UTC(2026, 9, 12) / 1000;
  const turned = (gmst(t + 86164.0905) - gmst(t) + 4 * Math.PI) % (2 * Math.PI);
  assert.ok(turned < 1e-4 || turned > 2 * Math.PI - 1e-4, `turned ${turned} rad`);
});
