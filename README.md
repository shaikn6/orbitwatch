# Orbit Watch

Every tracked object in Earth orbit on a live 3D globe, with the closest predicted passes of the next 48 hours.

![The viewer: 28,748 objects, with two satellites 44 seconds from their closest pass](docs/viewer.png)

There are about 29,000 catalogued objects in orbit: working satellites, dead ones, spent rocket stages and fragments. Orbit Watch downloads the public orbit data for all of them, predicts where each will be, finds every pair that will pass within 5 km of each other, and shows the result on a globe you can spin and replay.

## What has been measured

Run on an Apple M2 Pro laptop with public data fetched on 12 October 2026.

| Check                                                                 | Result                                                                                    |
| --------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| Orbit model against the official verification set (AIAA 2006-6753)    | all 666 positions of 31 objects agree to within 0.0001 m                                  |
| Screening against a brute-force search of every pair, every second    | 152 of 152 close approaches found, same time to 10 ms and distance to 1 m                 |
| Work avoided by the screening grid in that test                       | 99.86% of pair checks                                                                     |
| Full catalog, all pairs, 7 days                                       | 29,019 objects in 3 min 24 s                                                              |
| Full catalog, all pairs, 48 hours                                     | 28,748 objects in 57 s                                                                    |
| Agreement with CelesTrak SOCRATES where both used the same orbit data | 357 of 358 events found; time within 15 ms and miss distance within 0.5 m for 99% of them |

The 48-hour run found 205,612 passes under 5 km, 11,255 under 1 km and 511 under 200 m, plus 937 pairs that fly together on purpose (docked modules, formations), which are counted separately.

## Read this before quoting a number

**These are screening candidates, not near-collisions.** Public orbit data is accurate to roughly a kilometre, and less than that a few days ahead. A predicted miss of 10 m means "these two will be in the same small region at the same time", not "these two will pass 10 m apart". Operators decide on avoidance manoeuvres using their own, far more precise tracking. Nothing here should be used for that decision.

The comparison with SOCRATES covers 358 events only, because that is how many were computed from the same orbit data on both sides: my data was fetched about 22 hours after the report was produced, and most objects had been updated in between. One of the 358 was not found; for that pair my data puts the objects more than 50 km apart, which points to a reissued data set rather than a screening error, but I have not proven that. A matched snapshot is needed to repeat this at a larger scale.

## Run it

Requires Go 1.26. The viewer loads three.js and the Earth image from a CDN.

```sh
go build -o bin/orbitwatch ./cmd/orbitwatch
bin/orbitwatch fetch              # download the public orbit data (cached for 2 hours)
bin/orbitwatch screen -hours 48   # find close approaches, write web/data
bin/orbitwatch serve              # open http://127.0.0.1:8080
```

To repeat the checks:

```sh
go test ./...                     # orbit model, catalog, screening, fetching
node --test web/orbit.test.mjs    # the viewer's orbit maths against the server's
go build -o bin/validate ./cmd/validate && bin/validate   # against a SOCRATES report in data/raw
```

## How it works

**Orbit model (`internal/sgp4`).** Public orbit data is published as "mean elements" that only make sense with one specific model, SGP4. The numerical core is adapted from [go-satellite](https://github.com/joshuaferrara/go-satellite), with three faults found and fixed while checking it against the official test set: a wrong constant in the branch for very low orbits (128 where the reference has 120), orbit reference times rounded down to whole seconds (worth up to 7 km of position), and a height calculation that failed above the poles.

**Screening (`internal/screen`).** 29,000 objects make 420 million pairs, so checking every pair at every instant is out of the question. Time is cut into 15-second steps. At each step every object is dropped into a grid of cubes sized so that two objects in cubes that do not touch cannot possibly meet before the next step. Only objects in the same or touching cubes are compared. For those, treating both as moving in straight lines for 15 seconds gives the time and distance of closest approach in closed form, and the few pairs that pass are refined with the real orbit model.

**Data (`internal/fetch`, `internal/catalog`).** Element sets are cached. A download replaces the cached copy only after it has been read completely and parsed, so an error page or a dropped connection never destroys good data. Sources are merged with the newest element set per object winning, except that anything dated in the future is rejected, and element sets older than 14 days are left out.

**Viewer (`web/`).** The browser animates all objects with a lighter orbit model (an ellipse that drifts the way the Earth's bulge makes it drift). It is checked against SGP4 in the tests: the typical object is within 13 km over up to 40 hours, which is far less than a pixel. The list, distances and times come from the server's full-precision results.

## Not built yet

- A learned avoidance-manoeuvre planner, compared against the standard analytic rule.
- A streaming pipeline (Kafka) so results update as new orbit data arrives, instead of on demand.
- A second data source alongside CelesTrak.
- Cloud deployment.

## Data and credits

- Orbit data and the SOCRATES conjunction report: [CelesTrak](https://celestrak.org), Dr. T.S. Kelso. CelesTrak asks that each data set be requested no more than once per update; `orbitwatch fetch` waits two hours between requests.
- SGP4 reference code and verification set: Vallado, Crawford, Hujsak and Kelso, "Revisiting Spacetrack Report #3", AIAA 2006-6753.
- SGP4 Go port: Joshua Ferrara's go-satellite, BSD 2-Clause (licence in `internal/sgp4/LICENSE.go-satellite`).
- Rendering: [three.js](https://threejs.org), MIT. Earth image from the three.js examples.

## Licence

MIT. See `LICENSE`.
