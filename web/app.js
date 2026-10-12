import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";
import { EARTH_RADIUS_KM, gmst, period, position, prepare } from "./orbit.js";

const KM = 1 / EARTH_RADIUS_KM; // the scene's unit is one Earth radius
const COLORS = {
  "active satellite": [0.5, 0.83, 1.0],
  debris: [1.0, 0.6, 0.24],
  "rocket body": [1.0, 0.89, 0.48],
  inactive: [0.6, 0.64, 0.72],
};
const KIND_CLASS = {
  "active satellite": "k-active",
  debris: "k-debris",
  "rocket body": "k-rocket",
  inactive: "k-inactive",
};

const $ = (id) => document.getElementById(id);
const canvas = $("globe");
const renderer = new THREE.WebGLRenderer({ canvas, antialias: true });
renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
const scene = new THREE.Scene();
const camera = new THREE.PerspectiveCamera(42, 1, 0.01, 400);
camera.position.set(2.4, 1.3, 2.6);
const controls = new OrbitControls(camera, canvas);
controls.enableDamping = true;
controls.minDistance = 1.15;
controls.maxDistance = 30;
controls.enablePan = false;

// The scene uses the inertial frame the orbits are given in, with north up:
// scene x = inertial x, scene y = inertial z, scene z = -inertial y. The
// Earth then simply turns about the vertical axis.
const toScene = (x, y, z, target) => target.set(x * KM, z * KM, -y * KM);

const earth = new THREE.Mesh(
  new THREE.SphereGeometry(1, 96, 64),
  new THREE.MeshPhongMaterial({ color: 0x1a3350, shininess: 6 }),
);
scene.add(earth);
new THREE.TextureLoader().load(
  "https://cdn.jsdelivr.net/gh/mrdoob/three.js@r160/examples/textures/planets/earth_atmos_2048.jpg",
  (texture) => {
    texture.colorSpace = THREE.SRGBColorSpace;
    earth.material.map = texture;
    earth.material.color.set(0xffffff);
    earth.material.needsUpdate = true;
  },
);
scene.add(new THREE.AmbientLight(0xffffff, 1.5));
const sun = new THREE.DirectionalLight(0xffffff, 1.4);
sun.position.set(5, 2, 3);
scene.add(sun);
const glow = new THREE.Mesh(
  new THREE.SphereGeometry(1.015, 64, 48),
  new THREE.MeshBasicMaterial({ color: 0x4a8fd6, transparent: true, opacity: 0.08, side: THREE.BackSide }),
);
scene.add(glow);

const state = {
  prepared: null,
  indexOf: new Map(),
  events: [],
  shown: [],
  selected: null,
  simTime: Date.now() / 1000,
  speed: 1,
  paused: false,
  lastFrame: performance.now(),
  eci: null,
};

let points;
let pointPositions;
const marker = new THREE.Mesh(
  new THREE.RingGeometry(0.018, 0.024, 48),
  new THREE.MeshBasicMaterial({ color: 0xff5d4d, side: THREE.DoubleSide, transparent: true, depthTest: false }),
);
marker.visible = false;
marker.renderOrder = 3;
scene.add(marker);
const ringA = makeRing(0xff5d4d);
const ringB = makeRing(0xffffff);
const pairDots = new THREE.Points(
  new THREE.BufferGeometry().setAttribute("position", new THREE.BufferAttribute(new Float32Array(6), 3)),
  new THREE.PointsMaterial({ color: 0xffffff, size: 7, sizeAttenuation: false, depthTest: false }),
);
pairDots.visible = false;
pairDots.renderOrder = 4;
scene.add(pairDots);

function makeRing(color) {
  const geometry = new THREE.BufferGeometry();
  geometry.setAttribute("position", new THREE.BufferAttribute(new Float32Array(361 * 3), 3));
  const line = new THREE.Line(geometry, new THREE.LineBasicMaterial({ color, transparent: true, opacity: 0.85 }));
  line.visible = false;
  line.frustumCulled = false;
  scene.add(line);
  return line;
}

function drawRing(line, index, t) {
  const array = line.geometry.attributes.position.array;
  const v = new THREE.Vector3();
  const out = new Float64Array(3);
  for (let k = 0; k <= 360; k++) {
    position(state.prepared, index, t, out, 0, (k / 360) * 2 * Math.PI);
    toScene(out[0], out[1], out[2], v);
    array.set([v.x, v.y, v.z], k * 3);
  }
  line.geometry.attributes.position.needsUpdate = true;
  line.visible = true;
}

function buildPoints(catalog) {
  const n = catalog.id.length;
  pointPositions = new Float32Array(n * 3);
  const colors = new Float32Array(n * 3);
  for (let i = 0; i < n; i++) {
    colors.set(COLORS[catalog.kind[i]] ?? COLORS.inactive, i * 3);
    state.indexOf.set(catalog.id[i], i);
  }
  const geometry = new THREE.BufferGeometry();
  geometry.setAttribute("position", new THREE.BufferAttribute(pointPositions, 3));
  geometry.setAttribute("color", new THREE.BufferAttribute(colors, 3));
  points = new THREE.Points(
    geometry,
    new THREE.PointsMaterial({
      size: 1.7,
      sizeAttenuation: false,
      vertexColors: true,
      transparent: true,
      opacity: 0.9,
    }),
  );
  points.frustumCulled = false;
  scene.add(points);
  state.eci = new Float64Array(n * 3);
}

function updatePositions(t) {
  const p = state.prepared;
  const eci = state.eci;
  for (let i = 0; i < p.count; i++) {
    position(p, i, t, eci, i * 3);
    pointPositions[i * 3] = eci[i * 3] * KM;
    pointPositions[i * 3 + 1] = eci[i * 3 + 2] * KM;
    pointPositions[i * 3 + 2] = -eci[i * 3 + 1] * KM;
  }
  points.geometry.attributes.position.needsUpdate = true;
  earth.rotation.y = gmst(t);
}

const number = new Intl.NumberFormat("en-US");
function formatMiss(km) {
  return km < 1 ? `${Math.round(km * 1000)} m` : `${km.toFixed(2)} km`;
}
function formatWhen(event) {
  const seconds = (Date.parse(event.tca) - Date.now()) / 1000;
  const hours = Math.abs(seconds) / 3600;
  const text = hours < 1 ? `${Math.round(hours * 60)} min` : `${hours.toFixed(1)} h`;
  return seconds >= 0 ? `in ${text}` : `${text} ago`;
}

function renderList() {
  const query = $("search").value.trim().toLowerCase();
  const sort = $("sort").value;
  let events = state.events;
  if (query) {
    events = events.filter(
      (e) =>
        e.name_a.toLowerCase().includes(query) ||
        e.name_b.toLowerCase().includes(query) ||
        String(e.a) === query ||
        String(e.b) === query,
    );
  }
  events = [...events].sort((x, y) =>
    sort === "time" ? Date.parse(x.tca) - Date.parse(y.tca) : x.miss_km - y.miss_km,
  );
  state.shown = events.slice(0, 400);
  const list = $("list");
  list.replaceChildren(
    ...state.shown.map((event, i) => {
      const item = document.createElement("li");
      item.tabIndex = 0;
      item.dataset.index = String(i);
      if (event === state.selected) item.className = "sel";
      const miss = document.createElement("div");
      miss.className = "miss";
      miss.textContent = formatMiss(event.miss_km);
      const when = document.createElement("div");
      when.className = "when";
      when.textContent = formatWhen(event);
      const names = document.createElement("div");
      names.className = "names";
      for (const [name, kind] of [
        [event.name_a, event.kind_a],
        [event.name_b, event.kind_b],
      ]) {
        const row = document.createElement("div");
        const dot = document.createElement("i");
        dot.className = `dot ${KIND_CLASS[kind] ?? "k-inactive"}`;
        row.append(dot, name);
        names.append(row);
      }
      item.append(miss, names, when);
      return item;
    }),
  );
  if (!state.shown.length) {
    const empty = document.createElement("li");
    empty.textContent = "No close approaches match.";
    empty.style.cursor = "default";
    list.append(empty);
  }
}

function select(event, moveCamera = true) {
  state.selected = event;
  const tca = Date.parse(event.tca) / 1000;
  const a = state.indexOf.get(event.a);
  const b = state.indexOf.get(event.b);
  if (a === undefined || b === undefined) return;
  drawRing(ringA, a, tca);
  drawRing(ringB, b, tca);

  const out = new Float64Array(3);
  position(state.prepared, a, tca, out, 0);
  const where = toScene(out[0], out[1], out[2], new THREE.Vector3());
  marker.position.copy(where);
  marker.lookAt(0, 0, 0);
  marker.visible = true;
  pairDots.visible = true;

  $("d-miss").textContent = formatMiss(event.miss_km);
  $("d-a").textContent = `${event.name_a} (${event.a})`;
  $("d-b").textContent = `${event.name_b} (${event.b})`;
  $("d-when").textContent = event.tca.replace("T", " ").slice(0, 19);
  $("d-speed").textContent = `${event.rel_speed_km_s.toFixed(2)} km/s`;
  const ns = event.lat >= 0 ? "N" : "S";
  const ew = event.lon >= 0 ? "E" : "W";
  $("d-where").textContent =
    `${Math.abs(event.lat).toFixed(1)}°${ns} ${Math.abs(event.lon).toFixed(1)}°${ew}, ${Math.round(event.alt_km)} km up`;
  $("detail").style.display = "block";

  if (moveCamera) {
    // Look from a little to one side, so the two orbits are not seen edge-on.
    const distance = Math.max(camera.position.length(), 2.2);
    const up = new THREE.Vector3(0, 1, 0);
    const side = new THREE.Vector3().crossVectors(where, up);
    if (side.lengthSq() < 1e-6) side.set(1, 0, 0);
    const direction = where.clone().normalize().addScaledVector(side.normalize(), 0.55).normalize();
    camera.position.copy(direction.multiplyScalar(distance));
  }
  for (const item of $("list").children)
    item.classList.toggle("sel", state.shown[Number(item.dataset.index)] === event);
}

function setSpeed(speed) {
  state.speed = speed;
  for (const button of document.querySelectorAll("[data-speed]"))
    button.classList.toggle("on", Number(button.dataset.speed) === speed);
}

function frame(now) {
  const elapsed = Math.min((now - state.lastFrame) / 1000, 0.25);
  state.lastFrame = now;
  if (!state.paused) state.simTime += elapsed * state.speed;
  if (state.prepared) {
    updatePositions(state.simTime);
    if (state.selected) {
      const array = pairDots.geometry.attributes.position.array;
      for (const [slot, id] of [state.selected.a, state.selected.b].entries()) {
        const i = state.indexOf.get(id);
        array.set(pointPositions.subarray(i * 3, i * 3 + 3), slot * 3);
      }
      pairDots.geometry.attributes.position.needsUpdate = true;
      marker.material.opacity = 0.55 + 0.45 * Math.sin(now / 250);
    }
  }
  $("time").textContent = `${new Date(state.simTime * 1000).toISOString().replace("T", " ").slice(0, 19)} UTC`;
  controls.update();
  renderer.render(scene, camera);
  requestAnimationFrame(frame);
}

function resize() {
  const { clientWidth: width, clientHeight: height } = canvas;
  renderer.setSize(width, height, false);
  camera.aspect = width / height;
  // Keep the globe clear of the side panel on wide screens.
  camera.setViewOffset(width, height, width > 860 ? -190 : 0, 0, width, height);
  camera.updateProjectionMatrix();
}

async function load() {
  const [catalog, conjunctions] = await Promise.all(
    ["data/catalog.json", "data/conjunctions.json"].map(async (url) => {
      const response = await fetch(url);
      if (!response.ok) throw new Error(`${url}: ${response.status}`);
      return response.json();
    }),
  );
  state.prepared = prepare(catalog);
  buildPoints(catalog);
  state.events = conjunctions.events ?? [];
  $("s-objects").textContent = number.format(conjunctions.objects);
  $("s-1km").textContent = number.format(conjunctions.approaches_under_1_km);
  $("s-200m").textContent = number.format(conjunctions.approaches_under_200_m);
  $("subtitle").textContent =
    `Closest predicted approaches over ${conjunctions.window_hours} hours from ${conjunctions.window_start.slice(0, 16).replace("T", " ")} UTC`;
  $("status").remove();
  renderList();
  // Open on the closest upcoming pass, or the closest overall.
  const upcoming = state.events.filter((e) => Date.parse(e.tca) > Date.now());
  const first = (upcoming.length ? upcoming : state.events).reduce(
    (best, e) => (!best || e.miss_km < best.miss_km ? e : best),
    null,
  );
  if (first) select(first);
}

$("list").addEventListener("click", (e) => {
  const item = e.target.closest("li[data-index]");
  if (item) select(state.shown[Number(item.dataset.index)]);
});
$("list").addEventListener("keydown", (e) => {
  const item = e.target.closest("li[data-index]");
  if (item && (e.key === "Enter" || e.key === " ")) {
    e.preventDefault();
    select(state.shown[Number(item.dataset.index)]);
  }
});
$("search").addEventListener("input", renderList);
$("sort").addEventListener("change", renderList);
$("pause").addEventListener("click", () => {
  state.paused = !state.paused;
  $("pause").textContent = state.paused ? "Resume" : "Pause";
});
for (const button of document.querySelectorAll("[data-speed]"))
  button.addEventListener("click", () => setSpeed(Number(button.dataset.speed)));
$("now").addEventListener("click", () => {
  state.simTime = Date.now() / 1000;
  setSpeed(1);
});
$("replay").addEventListener("click", () => {
  if (!state.selected) return;
  state.simTime = Date.parse(state.selected.tca) / 1000 - 90;
  state.paused = false;
  $("pause").textContent = "Pause";
  setSpeed(1);
  select(state.selected);
});
window.addEventListener("resize", resize);

// ?t=<unix seconds> pins the clock, which makes screenshots repeatable.
const pinned = Number(new URLSearchParams(location.search).get("t"));
if (pinned > 0) {
  state.simTime = pinned;
  state.paused = true;
}

resize();
requestAnimationFrame(frame);
load().catch((error) => {
  $("status").textContent = `Could not load the data (${error.message}). Run "orbitwatch screen" first.`;
});
