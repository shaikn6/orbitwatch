import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";
import { EARTH_RADIUS_KM, describe, gmst, period, position, prepare, sunDirection } from "./orbit.js";

const KM = 1 / EARTH_RADIUS_KM; // the scene's unit is one Earth radius
const TEXTURES = "https://cdn.jsdelivr.net/gh/mrdoob/three.js@r160/examples/textures/planets/";
const KINDS = {
  "active satellite": { color: [0.62, 0.86, 1.0], css: "k-active", label: "active satellite" },
  debris: { color: [1.0, 0.6, 0.24], css: "k-debris", label: "debris" },
  "rocket body": { color: [1.0, 0.9, 0.5], css: "k-rocket", label: "rocket body" },
  inactive: { color: [0.6, 0.64, 0.72], css: "k-inactive", label: "inactive satellite" },
};
const HOME = new THREE.Vector3(2.5, 1.35, 2.7);
const calm = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

const $ = (id) => document.getElementById(id);
const canvas = $("globe");
const renderer = new THREE.WebGLRenderer({ canvas, antialias: true });
renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
const scene = new THREE.Scene();
const camera = new THREE.PerspectiveCamera(40, 1, 0.005, 600);
camera.position.copy(HOME);
const controls = new OrbitControls(camera, canvas);
controls.enableDamping = true;
controls.dampingFactor = 0.08;
controls.rotateSpeed = 0.55;
controls.zoomSpeed = 0.8;
controls.minDistance = 1.12;
controls.maxDistance = 40;
controls.enablePan = false;
controls.autoRotate = !calm;
controls.autoRotateSpeed = 0.35;

// The scene uses the inertial frame the orbits are given in, with north up:
// scene x = inertial x, scene y = inertial z, scene z = -inertial y. The
// Earth then simply turns about the vertical axis.
const toScene = (x, y, z, target) => target.set(x * KM, z * KM, -y * KM);

/* ---------- Earth, air and stars ---------- */

const blank = new THREE.DataTexture(new Uint8Array([10, 22, 40, 255]), 1, 1);
blank.needsUpdate = true;
const earthUniforms = {
  dayMap: { value: blank },
  nightMap: { value: blank },
  sunDir: { value: new THREE.Vector3(1, 0, 0) },
};
const earthGroup = new THREE.Group();
scene.add(earthGroup);
earthGroup.add(
  new THREE.Mesh(
    new THREE.SphereGeometry(1, 128, 96),
    new THREE.ShaderMaterial({
      uniforms: earthUniforms,
      vertexShader: `
        varying vec2 vUv; varying vec3 vNormalW; varying vec3 vPosW;
        void main() {
          vUv = uv;
          vNormalW = normalize(mat3(modelMatrix) * normal);
          vec4 world = modelMatrix * vec4(position, 1.0);
          vPosW = world.xyz;
          gl_Position = projectionMatrix * viewMatrix * world;
        }`,
      fragmentShader: `
        uniform sampler2D dayMap; uniform sampler2D nightMap; uniform vec3 sunDir;
        varying vec2 vUv; varying vec3 vNormalW; varying vec3 vPosW;
        void main() {
          vec3 n = normalize(vNormalW);
          float lit = dot(n, sunDir);
          float day = smoothstep(-0.10, 0.20, lit);
          vec3 surface = texture2D(dayMap, vUv).rgb;
          vec3 dayColor = surface * (0.30 + 0.85 * max(lit, 0.0));
          vec3 nightColor = texture2D(nightMap, vUv).rgb * vec3(1.0, 0.82, 0.55) * 1.5 + surface * 0.03;
          vec3 color = mix(nightColor, dayColor, day);
          // A thin band of warm light where day turns to night.
          color += vec3(1.0, 0.45, 0.18) * 0.10 * smoothstep(0.18, 0.0, abs(lit));
          float rim = pow(1.0 - max(dot(normalize(cameraPosition - vPosW), n), 0.0), 3.2);
          color += vec3(0.28, 0.55, 1.0) * rim * (0.18 + 0.82 * day);
          gl_FragColor = vec4(color, 1.0);
          #include <tonemapping_fragment>
          #include <colorspace_fragment>
        }`,
    }),
  ),
);
const loader = new THREE.TextureLoader();
for (const [file, uniform] of [
  ["earth_atmos_2048.jpg", "dayMap"],
  ["earth_lights_2048.png", "nightMap"],
]) {
  loader.load(TEXTURES + file, (texture) => {
    texture.colorSpace = THREE.SRGBColorSpace;
    texture.anisotropy = 8;
    earthUniforms[uniform].value = texture;
  });
}
scene.add(
  new THREE.Mesh(
    new THREE.SphereGeometry(1.045, 96, 64),
    new THREE.ShaderMaterial({
      side: THREE.BackSide,
      transparent: true,
      blending: THREE.AdditiveBlending,
      depthWrite: false,
      vertexShader: `varying vec3 vNormal; void main() { vNormal = normalize(normalMatrix * normal); gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0); }`,
      fragmentShader: `varying vec3 vNormal; void main() { float glow = pow(max(0.72 - dot(vNormal, vec3(0.0, 0.0, 1.0)), 0.0), 3.0); gl_FragColor = vec4(0.30, 0.58, 1.0, 1.0) * glow; }`,
    }),
  ),
);
{
  const count = 5000;
  const positions = new Float32Array(count * 3);
  const colors = new Float32Array(count * 3);
  const v = new THREE.Vector3();
  for (let i = 0; i < count; i++) {
    v.randomDirection().multiplyScalar(300);
    positions.set([v.x, v.y, v.z], i * 3);
    const shade = 0.25 + 0.75 * Math.random() ** 3;
    colors.set([shade, shade, shade * (0.9 + 0.2 * Math.random())], i * 3);
  }
  const geometry = new THREE.BufferGeometry();
  geometry.setAttribute("position", new THREE.BufferAttribute(positions, 3));
  geometry.setAttribute("color", new THREE.BufferAttribute(colors, 3));
  scene.add(
    new THREE.Points(
      geometry,
      new THREE.PointsMaterial({ size: 1.4, sizeAttenuation: false, vertexColors: true, depthWrite: false }),
    ),
  );
}

/* ---------- state ---------- */

const state = {
  prepared: null,
  catalog: null,
  indexOf: new Map(),
  events: [],
  marked: [], // the passes drawn on the globe
  shown: [],
  eventsOf: new Map(), // catalogue number -> events it takes part in
  selection: null, // {type: "event", event} or {type: "object", index}
  follow: false,
  replayUntil: 0,
  windowStart: 0,
  windowEnd: 0,
  simTime: Date.now() / 1000,
  speed: 1,
  paused: false,
  lastFrame: performance.now(),
  lastTouch: performance.now(),
  hidden: new Set(),
  tween: null,
};
let points, pointPositions, pointShow, hotspots, hotLocal;
const eci = { all: null };

/* ---------- objects ---------- */

const pointMaterial = new THREE.ShaderMaterial({
  uniforms: { size: { value: 2.3 * renderer.getPixelRatio() } },
  transparent: true,
  depthWrite: false,
  blending: THREE.AdditiveBlending,
  vertexShader: `
    attribute vec3 color; attribute float show; uniform float size;
    varying vec3 vColor; varying float vShow;
    void main() {
      vColor = color; vShow = show;
      vec4 mv = modelViewMatrix * vec4(position, 1.0);
      gl_Position = projectionMatrix * mv;
      gl_PointSize = show * size * clamp(2.6 / -mv.z, 0.8, 5.0);
    }`,
  fragmentShader: `
    varying vec3 vColor; varying float vShow;
    void main() {
      if (vShow < 0.5) discard;
      float d = length(gl_PointCoord - 0.5);
      gl_FragColor = vec4(vColor, smoothstep(0.5, 0.05, d) * 0.95);
    }`,
});

function buildPoints(catalog) {
  const n = catalog.id.length;
  pointPositions = new Float32Array(n * 3);
  pointShow = new Float32Array(n).fill(1);
  const colors = new Float32Array(n * 3);
  for (let i = 0; i < n; i++) {
    colors.set((KINDS[catalog.kind[i]] ?? KINDS.inactive).color, i * 3);
    state.indexOf.set(catalog.id[i], i);
  }
  const geometry = new THREE.BufferGeometry();
  geometry.setAttribute("position", new THREE.BufferAttribute(pointPositions, 3));
  geometry.setAttribute("color", new THREE.BufferAttribute(colors, 3));
  geometry.setAttribute("show", new THREE.BufferAttribute(pointShow, 1));
  points = new THREE.Points(geometry, pointMaterial);
  points.frustumCulled = false;
  scene.add(points);
  eci.all = new Float64Array(n * 3);
}

// Where each listed pass happens, pinned to the ground below it so the
// marks turn with the Earth.
function buildHotspots(events) {
  hotLocal = new Float32Array(events.length * 3);
  const colors = new Float32Array(events.length * 3);
  events.forEach((event, i) => {
    const lat = (event.lat * Math.PI) / 180;
    const lon = (event.lon * Math.PI) / 180;
    const r = 1 + event.alt_km * KM;
    hotLocal.set([r * Math.cos(lat) * Math.cos(lon), r * Math.sin(lat), -r * Math.cos(lat) * Math.sin(lon)], i * 3);
    colors.set(event.miss_km < 0.2 ? [1.0, 0.3, 0.18] : [1.0, 0.86, 0.8], i * 3);
  });
  const geometry = new THREE.BufferGeometry();
  geometry.setAttribute("position", new THREE.BufferAttribute(hotLocal, 3));
  geometry.setAttribute("color", new THREE.BufferAttribute(colors, 3));
  geometry.setAttribute("show", new THREE.BufferAttribute(new Float32Array(events.length).fill(1), 1));
  hotspots = new THREE.Points(
    geometry,
    new THREE.ShaderMaterial({
      uniforms: { size: { value: 11 * renderer.getPixelRatio() }, pulse: { value: 0 } },
      transparent: true,
      depthWrite: false,
      vertexShader: `
        attribute vec3 color; uniform float size; varying vec3 vColor;
        void main() { vColor = color; vec4 mv = modelViewMatrix * vec4(position, 1.0); gl_Position = projectionMatrix * mv; gl_PointSize = size * clamp(2.6 / -mv.z, 0.7, 2.2); }`,
      fragmentShader: `
        uniform float pulse; varying vec3 vColor;
        void main() {
          // A hollow ring, so a pass is never mistaken for an object, with a
          // second ring that spreads outwards and fades.
          float d = length(gl_PointCoord - 0.5) * 2.0;
          float ring = smoothstep(0.13, 0.0, abs(d - 0.42));
          float wave = smoothstep(0.10, 0.0, abs(d - (0.42 + 0.5 * pulse))) * (1.0 - pulse);
          gl_FragColor = vec4(vColor, max(ring, wave * 0.7));
        }`,
    }),
  );
  hotspots.frustumCulled = false;
  earthGroup.add(hotspots);
}

function makeRing(color) {
  const geometry = new THREE.BufferGeometry();
  geometry.setAttribute("position", new THREE.BufferAttribute(new Float32Array(361 * 3), 3));
  const line = new THREE.Line(geometry, new THREE.LineBasicMaterial({ color, transparent: true, opacity: 0.9 }));
  line.visible = false;
  line.frustumCulled = false;
  scene.add(line);
  return line;
}
const ringA = makeRing(0xff4d2e);
const ringB = makeRing(0xffffff);
const marks = new THREE.Points(
  new THREE.BufferGeometry().setAttribute("position", new THREE.BufferAttribute(new Float32Array(6), 3)),
  new THREE.PointsMaterial({ color: 0xffffff, size: 9, sizeAttenuation: false, depthWrite: false }),
);
marks.frustumCulled = false;
marks.visible = false;
scene.add(marks);
const tether = new THREE.Line(
  new THREE.BufferGeometry().setAttribute("position", new THREE.BufferAttribute(new Float32Array(6), 3)),
  new THREE.LineBasicMaterial({ color: 0xffb000, transparent: true, opacity: 0.9 }),
);
tether.frustumCulled = false;
tether.visible = false;
scene.add(tether);

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

const scenePosition = (index, target = new THREE.Vector3()) => target.fromArray(pointPositions, index * 3);

/* ---------- camera moves ---------- */

function flyTo(position, target, seconds = 1.4) {
  controls.autoRotate = false;
  if (calm) seconds = 0;
  state.tween = {
    from: camera.position.clone(),
    to: position.clone(),
    targetFrom: controls.target.clone(),
    targetTo: target.clone(),
    start: performance.now(),
    seconds,
  };
}

function stepTween(now) {
  const tween = state.tween;
  if (!tween) return;
  const k = tween.seconds ? Math.min((now - tween.start) / (tween.seconds * 1000), 1) : 1;
  const eased = k < 0.5 ? 4 * k * k * k : 1 - (-2 * k + 2) ** 3 / 2;
  // Swing round the globe rather than cutting through it.
  const radius = THREE.MathUtils.lerp(tween.from.length(), tween.to.length(), eased);
  camera.position.lerpVectors(tween.from, tween.to, eased);
  if (tween.targetTo.lengthSq() < 1e-6 && tween.targetFrom.lengthSq() < 1e-6) camera.position.setLength(radius);
  controls.target.lerpVectors(tween.targetFrom, tween.targetTo, eased);
  if (k === 1) state.tween = null;
}

// A viewpoint a little to one side of a place above the Earth, so that
// orbits through it are not seen edge-on.
function viewOf(place, distance) {
  const side = new THREE.Vector3().crossVectors(place, new THREE.Vector3(0, 1, 0));
  if (side.lengthSq() < 1e-6) side.set(1, 0, 0);
  return place.clone().normalize().addScaledVector(side.normalize(), 0.5).normalize().multiplyScalar(distance);
}

/* ---------- formatting ---------- */

const number = new Intl.NumberFormat("en-US");
const formatMiss = (km) => (km < 1 ? `${Math.round(km * 1000)} m` : `${km.toFixed(2)} km`);
const tcaOf = (event) => Date.parse(event.tca) / 1000;
function formatCountdown(seconds) {
  const sign = seconds < 0 ? "+" : "−";
  const s = Math.abs(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const rest = (s % 60).toFixed(s < 120 ? 1 : 0).padStart(s < 120 ? 4 : 2, "0");
  return `T${sign}${h ? `${h}:${String(m).padStart(2, "0")}` : m}:${rest}`;
}
function formatWhen(event) {
  const hours = (tcaOf(event) - state.simTime) / 3600;
  const size = Math.abs(hours);
  const text = size < 1 ? `${Math.round(size * 60)} min` : `${size.toFixed(1)} h`;
  return hours >= 0 ? `in ${text}` : `${text} ago`;
}
const nameOf = (index) => state.catalog.name[index];
function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

/* ---------- list, search and layers ---------- */

function renderList() {
  const query = $("search").value.trim().toLowerCase();
  const byTime = $("sort").value === "time";
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
  if (byTime) events = events.filter((e) => tcaOf(e) > state.simTime - 60);
  events = [...events].sort((x, y) => (byTime ? tcaOf(x) - tcaOf(y) : x.miss_km - y.miss_km));
  state.shown = events.slice(0, 300);
  const selected = state.selection?.type === "event" ? state.selection.event : null;
  const list = $("list");
  list.replaceChildren(
    ...state.shown.map((event, i) => {
      const item = element("li", event === selected ? "sel" : "");
      item.tabIndex = 0;
      item.dataset.index = String(i);
      item.style.animationDelay = `${Math.min(i, 14) * 18}ms`;
      const names = element("div", "names");
      for (const [name, kind] of [
        [event.name_a, event.kind_a],
        [event.name_b, event.kind_b],
      ]) {
        const row = element("div");
        row.append(element("i", `dot ${(KINDS[kind] ?? KINDS.inactive).css}`), name);
        names.append(row);
      }
      item.append(element("div", "miss", formatMiss(event.miss_km)), names, element("div", "when", formatWhen(event)));
      return item;
    }),
  );
  if (!state.shown.length) list.append(element("li", "empty", "No listed close pass matches."));
}

function renderMatches() {
  const query = $("search").value.trim().toLowerCase();
  const box = $("matches");
  if (query.length < 2) {
    box.style.display = "none";
    return;
  }
  const found = [];
  const { name, id, kind } = state.catalog;
  for (let i = 0; i < name.length && found.length < 8; i++) {
    if (String(id[i]) === query || name[i].toLowerCase().includes(query)) found.push(i);
  }
  box.replaceChildren(
    ...found.map((index) => {
      const item = element("li");
      item.dataset.object = String(index);
      item.setAttribute("role", "option");
      const left = element("span");
      left.append(element("i", `dot ${(KINDS[kind[index]] ?? KINDS.inactive).css}`), name[index]);
      item.append(left, element("small", "", String(id[index])));
      return item;
    }),
  );
  box.style.display = found.length ? "block" : "none";
}

function applyLayers() {
  const kinds = state.catalog.kind;
  for (let i = 0; i < kinds.length; i++) pointShow[i] = state.hidden.has(kinds[i]) ? 0 : 1;
  points.geometry.attributes.show.needsUpdate = true;
}

/* ---------- selection ---------- */

function row(label, content) {
  const dd = element("dd");
  dd.append(content);
  return [element("dt", "", label), dd];
}
function link(text, action) {
  const button = element("button", "", text);
  button.addEventListener("click", action);
  return button;
}
function action(text, run, on = false) {
  const button = element("button", on ? "btn on" : "btn", text);
  button.addEventListener("click", run);
  return button;
}

function clearSelection(resetCamera = true) {
  state.selection = null;
  state.follow = false;
  state.replayUntil = 0;
  ringA.visible = ringB.visible = marks.visible = tether.visible = false;
  $("inspector").style.display = "none";
  $("hud").style.display = "none";
  controls.minDistance = 1.12;
  for (const item of $("list").children) item.classList.remove("sel");
  if (resetCamera) flyTo(HOME.clone().setLength(Math.max(camera.position.length(), 3.2)), new THREE.Vector3());
}

function selectEvent(event, move = true) {
  const a = state.indexOf.get(event.a);
  const b = state.indexOf.get(event.b);
  if (a === undefined || b === undefined) return;
  state.selection = { type: "event", event, a, b };
  state.follow = false;
  state.replayUntil = 0;
  controls.minDistance = 1.12;
  $("hud").style.display = "none";
  const tca = tcaOf(event);
  ringA.material.color.set(0xff4d2e);
  drawRing(ringA, a, tca);
  drawRing(ringB, b, tca);
  marks.visible = true;
  tether.visible = false;

  $("i-kind").textContent = "Predicted closest pass";
  $("i-big").textContent = formatMiss(event.miss_km);
  $("i-big").style.color = "var(--alert)";
  $("i-title").textContent = "";
  $("i-title").style.display = "none";
  $("i-sub").textContent = "";
  const ns = event.lat >= 0 ? "N" : "S";
  const ew = event.lon >= 0 ? "E" : "W";
  $("i-rows").replaceChildren(
    ...row(
      "Object 1",
      link(event.name_a, () => selectObject(a)),
    ),
    ...row(
      "Object 2",
      link(event.name_b, () => selectObject(b)),
    ),
    ...row("When (UTC)", event.tca.replace("T", " ").slice(0, 19)),
    ...row("Closing speed", `${event.rel_speed_km_s.toFixed(2)} km/s`),
    ...row(
      "Over",
      `${Math.abs(event.lat).toFixed(1)}°${ns} ${Math.abs(event.lon).toFixed(1)}°${ew}, ${Math.round(event.alt_km)} km up`,
    ),
  );
  $("i-actions").replaceChildren(
    action("Watch the pass", () => replay(event), true),
    action("Go to this moment", () => {
      state.simTime = tca;
      state.paused = true;
      $("pause").textContent = "Resume";
    }),
  );
  $("inspector").style.display = "block";
  for (const item of $("list").children)
    item.classList.toggle("sel", state.shown[Number(item.dataset.index)] === event);

  if (move) {
    const out = new Float64Array(3);
    position(state.prepared, a, tca, out, 0);
    const place = toScene(out[0], out[1], out[2], new THREE.Vector3());
    flyTo(viewOf(place, Math.min(Math.max(camera.position.length(), 2.4), 3.4)), new THREE.Vector3());
  }
}

function selectObject(index, move = true) {
  state.selection = { type: "object", index };
  state.replayUntil = 0;
  $("hud").style.display = "none";
  ringA.material.color.set(0xffb000);
  drawRing(ringA, index, state.simTime);
  ringB.visible = tether.visible = false;
  marks.visible = true;
  const cat = state.catalog;
  const kind = KINDS[cat.kind[index]] ?? KINDS.inactive;
  $("i-kind").textContent = kind.label;
  $("i-big").style.color = "var(--amber)";
  $("i-title").textContent = cat.name[index];
  $("i-title").style.display = "block";
  $("i-sub").textContent = `Catalogue number ${cat.id[index]}`;
  const passes = state.eventsOf.get(cat.id[index]) ?? [];
  const closest = passes.reduce((best, e) => (!best || e.miss_km < best.miss_km ? e : best), null);
  $("i-rows").replaceChildren(
    ...row("Speed", element("span", "live-speed", "–")),
    ...row("One orbit takes", `${(period(state.prepared, index) / 60).toFixed(1)} min`),
    ...row("Orbit tilt", `${((cat.i[index] * 180) / Math.PI).toFixed(1)}°`),
    ...row(
      "Close passes listed",
      closest ? link(`${passes.length}, closest ${formatMiss(closest.miss_km)}`, () => selectEvent(closest)) : "none",
    ),
  );
  const followButton = action("Follow", () => {
    state.follow = !state.follow;
    followButton.classList.toggle("on", state.follow);
    if (state.follow) {
      const here = scenePosition(index);
      controls.minDistance = 0.05;
      flyTo(here.clone().multiplyScalar(1.0).add(viewOf(here, 0.9)), here, 1.2);
    } else {
      controls.minDistance = 1.12;
      flyTo(viewOf(scenePosition(index), 3), new THREE.Vector3());
    }
  });
  $("i-actions").replaceChildren(
    followButton,
    action("Show its passes", () => {
      $("search").value = String(cat.id[index]);
      renderList();
    }),
  );
  $("inspector").style.display = "block";
  for (const item of $("list").children) item.classList.remove("sel");
  state.follow = false;
  controls.minDistance = 1.12;
  if (move)
    flyTo(viewOf(scenePosition(index), Math.min(Math.max(camera.position.length(), 2.2), 3.2)), new THREE.Vector3());
}

// Replay: jump to a minute before the pass, ride along with the pair and
// count down. The gap shown comes from the viewer's light orbit model and is
// labelled as such; the predicted miss distance is the server's.
function replay(event) {
  if (state.selection?.event !== event) selectEvent(event, false);
  const tca = tcaOf(event);
  state.simTime = tca - 75;
  state.paused = false;
  $("pause").textContent = "Pause";
  setSpeed(1);
  state.replayUntil = tca + 25;
  updatePositions(state.simTime);
  const { a, b } = state.selection;
  const middle = scenePosition(a).add(scenePosition(b)).multiplyScalar(0.5);
  controls.minDistance = 0.03;
  flyTo(middle.clone().add(viewOf(middle, 0.42)), middle, 1.6);
  tether.visible = true;
  $("hud").style.display = "block";
}

/* ---------- picking ---------- */

const projected = new THREE.Vector3();
const toPoint = new THREE.Vector3();
function hiddenByEarth(p) {
  toPoint.subVectors(p, camera.position);
  const t = -camera.position.dot(toPoint) / toPoint.lengthSq();
  return t > 0 && t < 1 && projected.copy(camera.position).addScaledVector(toPoint, t).lengthSq() < 0.985;
}

function pick(clientX, clientY) {
  if (!state.prepared) return null;
  const rect = canvas.getBoundingClientRect();
  const view = new THREE.Matrix4().multiplyMatrices(camera.projectionMatrix, camera.matrixWorldInverse);
  const p = new THREE.Vector3();
  let best = null;
  let bestDistance = 11; // pixels
  const consider = (candidate) => {
    projected.copy(p).applyMatrix4(view);
    if (projected.z > 1 || projected.z < -1) return;
    const dx = ((projected.x + 1) / 2) * rect.width - (clientX - rect.left);
    const dy = ((1 - projected.y) / 2) * rect.height - (clientY - rect.top);
    const distance = Math.hypot(dx, dy) - candidate.bonus;
    if (distance < bestDistance && !hiddenByEarth(p)) {
      bestDistance = distance;
      best = candidate;
    }
  };
  const angle = earthGroup.rotation.y;
  const cos = Math.cos(angle);
  const sin = Math.sin(angle);
  for (let i = 0; i < state.marked.length; i++) {
    const x = hotLocal[i * 3];
    const z = hotLocal[i * 3 + 2];
    p.set(x * cos + z * sin, hotLocal[i * 3 + 1], -x * sin + z * cos);
    consider({ type: "event", event: state.marked[i], bonus: 3 });
  }
  for (let i = 0; i < state.prepared.count; i++) {
    if (!pointShow[i]) continue;
    p.fromArray(pointPositions, i * 3);
    consider({ type: "object", index: i, bonus: 0 });
  }
  return best;
}

function showTip(found, x, y) {
  const tip = $("tip");
  if (!found) {
    tip.style.display = "none";
    canvas.classList.remove("pointing");
    return;
  }
  canvas.classList.add("pointing");
  const title = element("b");
  const detail = element("span");
  if (found.type === "object") {
    const kind = KINDS[state.catalog.kind[found.index]] ?? KINDS.inactive;
    const { altitudeKm } = describe(state.prepared, found.index, state.simTime);
    title.textContent = nameOf(found.index);
    detail.textContent = `${kind.label} · ${Math.round(altitudeKm)} km up`;
  } else {
    title.textContent = `${formatMiss(found.event.miss_km)} pass, ${formatWhen(found.event)}`;
    detail.textContent = `${found.event.name_a} × ${found.event.name_b}`;
  }
  tip.replaceChildren(title, detail);
  tip.style.display = "block";
  const width = tip.offsetWidth;
  tip.style.left = `${Math.min(x + 14, window.innerWidth - width - 8)}px`;
  tip.style.top = `${y + 16}px`;
}

/* ---------- time ---------- */

function setSpeed(speed) {
  state.speed = speed;
  for (const button of document.querySelectorAll("[data-speed]"))
    button.classList.toggle("on", Number(button.dataset.speed) === speed);
}

const timeline = $("timeline");
function drawTimeline() {
  const ratio = Math.min(window.devicePixelRatio, 2);
  const width = timeline.clientWidth;
  const height = timeline.clientHeight;
  if (timeline.width !== width * ratio) {
    timeline.width = width * ratio;
    timeline.height = height * ratio;
  }
  const g = timeline.getContext("2d");
  g.setTransform(ratio, 0, 0, ratio, 0, 0);
  g.clearRect(0, 0, width, height);
  const span = state.windowEnd - state.windowStart;
  const x = (t) => ((t - state.windowStart) / span) * width;
  g.fillStyle = "rgba(190,205,230,0.05)";
  g.fillRect(0, 0, width, height);
  g.strokeStyle = "rgba(190,205,230,0.14)";
  g.lineWidth = 1;
  for (let h = 0; h <= span / 3600; h += 6) {
    const at = Math.round(x(state.windowStart + h * 3600)) + 0.5;
    g.beginPath();
    g.moveTo(at, height - (h % 24 === 0 ? 12 : 6));
    g.lineTo(at, height);
    g.stroke();
  }
  // One mark per listed pass: the closer the pass, the taller the mark.
  for (const event of state.events) {
    const close = Math.max(0, 1 - Math.log10(event.miss_km * 1000 + 1) / 3);
    g.fillStyle = event.miss_km < 0.2 ? "rgba(255,77,46,0.75)" : "rgba(255,176,0,0.35)";
    g.fillRect(x(tcaOf(event)), height - 4 - close * (height - 8), 1, 4 + close * (height - 8));
  }
  if (state.selection?.type === "event") {
    const at = x(tcaOf(state.selection.event));
    g.fillStyle = "#ffffff";
    g.fillRect(at - 1, 0, 2, height);
  }
  const now = Math.min(Math.max(x(state.simTime), 0), width);
  g.fillStyle = "#ffb000";
  g.fillRect(now - 1, 0, 2, height);
  g.beginPath();
  g.moveTo(now - 5, 0);
  g.lineTo(now + 5, 0);
  g.lineTo(now, 6);
  g.fill();
}
function scrub(clientX) {
  const rect = timeline.getBoundingClientRect();
  const share = Math.min(Math.max((clientX - rect.left) / rect.width, 0), 1);
  state.simTime = state.windowStart + share * (state.windowEnd - state.windowStart);
  state.replayUntil = 0;
  $("hud").style.display = "none";
}

/* ---------- frame ---------- */

function updatePositions(t) {
  const p = state.prepared;
  const all = eci.all;
  for (let i = 0; i < p.count; i++) {
    position(p, i, t, all, i * 3);
    pointPositions[i * 3] = all[i * 3] * KM;
    pointPositions[i * 3 + 1] = all[i * 3 + 2] * KM;
    pointPositions[i * 3 + 2] = -all[i * 3 + 1] * KM;
  }
  points.geometry.attributes.position.needsUpdate = true;
  earthGroup.rotation.y = gmst(t);
  const sun = sunDirection(t);
  earthUniforms.sunDir.value.set(sun[0], sun[2], -sun[1]);
}

const previousTarget = new THREE.Vector3();
let lastSlow = 0;
function frame(now) {
  const elapsed = Math.min((now - state.lastFrame) / 1000, 0.25);
  state.lastFrame = now;
  if (!state.paused) state.simTime += elapsed * state.speed;
  if (state.prepared) {
    updatePositions(state.simTime);
    const selection = state.selection;
    const array = marks.geometry.attributes.position.array;
    if (selection?.type === "event") {
      array.set(pointPositions.subarray(selection.a * 3, selection.a * 3 + 3), 0);
      array.set(pointPositions.subarray(selection.b * 3, selection.b * 3 + 3), 3);
      marks.geometry.setDrawRange(0, 2);
      marks.geometry.attributes.position.needsUpdate = true;
      if (state.replayUntil) {
        tether.geometry.attributes.position.array.set(array);
        tether.geometry.attributes.position.needsUpdate = true;
        const a = scenePosition(selection.a);
        const b = scenePosition(selection.b);
        const middle = a.clone().add(b).multiplyScalar(0.5);
        if (!state.tween) {
          camera.position.add(previousTarget.subVectors(middle, controls.target));
          controls.target.copy(middle);
        }
        const left = tcaOf(selection.event) - state.simTime;
        $("hud-count").textContent = formatCountdown(left);
        const gap = a.distanceTo(b) * EARTH_RADIUS_KM;
        $("hud-gap").textContent =
          `about ${gap < 10 ? gap.toFixed(1) : Math.round(gap)} km apart in this view · predicted miss ${formatMiss(selection.event.miss_km)}`;
        if (state.simTime > state.replayUntil) {
          state.replayUntil = 0;
          tether.visible = false;
          $("hud").style.display = "none";
        }
      }
    } else if (selection?.type === "object") {
      array.set(pointPositions.subarray(selection.index * 3, selection.index * 3 + 3), 0);
      marks.geometry.setDrawRange(0, 1);
      marks.geometry.attributes.position.needsUpdate = true;
      if (state.follow && !state.tween) {
        const here = scenePosition(selection.index);
        camera.position.add(previousTarget.subVectors(here, controls.target));
        controls.target.copy(here);
      }
    }
    hotspots.material.uniforms.pulse.value = calm ? 0.3 : (now / 1600) % 1;

    if (now - lastSlow > 250) {
      lastSlow = now;
      if (selection?.type === "object") {
        const { altitudeKm, speedKmS } = describe(state.prepared, selection.index, state.simTime);
        $("i-big").textContent = `${number.format(Math.round(altitudeKm))} km`;
        const speed = document.querySelector(".live-speed");
        if (speed) speed.textContent = `${speedKmS.toFixed(2)} km/s`;
        drawRing(ringA, selection.index, state.simTime);
      } else if (selection?.type === "event") {
        $("i-sub").textContent = formatCountdown(tcaOf(selection.event) - state.simTime);
      }
      drawTimeline();
    }
  }
  const date = new Date(state.simTime * 1000).toISOString();
  $("time").textContent = date.slice(11, 19);
  $("date").textContent = `${date.slice(0, 10)} UTC`;
  if (!controls.autoRotate && !calm && !state.selection && now - state.lastTouch > 25000) controls.autoRotate = true;
  stepTween(now);
  controls.update();
  renderer.render(scene, camera);
  requestAnimationFrame(frame);
}

function resize() {
  const { clientWidth: width, clientHeight: height } = canvas;
  renderer.setSize(width, height, false);
  camera.aspect = width / height;
  // Keep the globe clear of the list on wide screens.
  camera.setViewOffset(width, height, width > 980 ? -178 : 0, 0, width, height);
  camera.updateProjectionMatrix();
}

/* ---------- wiring ---------- */

async function load() {
  const [catalog, conjunctions] = await Promise.all(
    ["data/catalog.json", "data/conjunctions.json"].map(async (url) => {
      const response = await fetch(url);
      if (!response.ok) throw new Error(`${url}: ${response.status}`);
      return response.json();
    }),
  );
  state.catalog = catalog;
  state.prepared = prepare(catalog);
  buildPoints(catalog);
  state.events = conjunctions.events ?? [];
  for (const event of state.events) {
    for (const id of [event.a, event.b]) {
      if (!state.eventsOf.has(id)) state.eventsOf.set(id, []);
      state.eventsOf.get(id).push(event);
    }
  }
  // Marks on the globe for the closest passes only; the list has them all.
  state.marked = [...state.events].sort((x, y) => x.miss_km - y.miss_km).slice(0, 250);
  buildHotspots(state.marked);
  state.windowStart = Date.parse(conjunctions.window_start) / 1000;
  state.windowEnd = state.windowStart + conjunctions.window_hours * 3600;
  // If the data is older than its own window, start inside the window so
  // there is something to see.
  if (state.simTime > state.windowEnd || state.simTime < state.windowStart - 3600) state.simTime = state.windowStart;

  $("s-objects").textContent = number.format(conjunctions.objects);
  $("s-1km").textContent = number.format(conjunctions.approaches_under_1_km);
  $("s-200m").textContent = number.format(conjunctions.approaches_under_200_m);
  const kinds = conjunctions.objects_by_kind ?? {};
  for (const button of document.querySelectorAll(".layer"))
    button.querySelector("b").textContent = number.format(kinds[button.dataset.kind] ?? 0);
  $("subtitle").textContent =
    `The ${number.format(state.events.length)} closest predicted passes of ${conjunctions.window_hours} hours`;
  const stamp = (t) => new Date(t * 1000).toISOString().slice(5, 16).replace("T", " ");
  $("t-start").textContent = stamp(state.windowStart);
  $("t-end").textContent = stamp(state.windowEnd);
  $("status").remove();
  updatePositions(state.simTime);
  renderList();
  drawTimeline();

  const params = new URLSearchParams(location.search);
  if (Number(params.get("t")) > 0) {
    state.simTime = Number(params.get("t"));
    state.paused = true;
    $("pause").textContent = "Resume";
    controls.autoRotate = false;
  }
  if (params.has("event")) {
    const sorted = [...state.events].sort((x, y) => x.miss_km - y.miss_km);
    const chosen = sorted[Number(params.get("event")) || 0];
    if (chosen) selectEvent(chosen);
  }
}

let press = null;
canvas.addEventListener("pointerdown", (e) => {
  press = { x: e.clientX, y: e.clientY };
  state.lastTouch = performance.now();
  state.tween = null;
  controls.autoRotate = false;
  canvas.classList.add("dragging");
});
window.addEventListener("pointerup", (e) => {
  canvas.classList.remove("dragging");
  if (!press || e.target !== canvas) {
    press = null;
    return;
  }
  const moved = Math.hypot(e.clientX - press.x, e.clientY - press.y);
  press = null;
  if (moved > 5) return;
  const found = pick(e.clientX, e.clientY);
  if (found?.type === "object") selectObject(found.index, false);
  else if (found?.type === "event") selectEvent(found.event, false);
});
let hoverQueued = false;
canvas.addEventListener("pointermove", (e) => {
  if (press || hoverQueued) return;
  hoverQueued = true;
  requestAnimationFrame(() => {
    hoverQueued = false;
    showTip(pick(e.clientX, e.clientY), e.clientX, e.clientY);
  });
});
canvas.addEventListener("pointerleave", () => showTip(null));
canvas.addEventListener("wheel", () => {
  state.lastTouch = performance.now();
  state.tween = null;
  controls.autoRotate = false;
});

$("list").addEventListener("click", (e) => {
  const item = e.target.closest("li[data-index]");
  if (item) selectEvent(state.shown[Number(item.dataset.index)]);
});
$("list").addEventListener("keydown", (e) => {
  const item = e.target.closest("li[data-index]");
  if (item && (e.key === "Enter" || e.key === " ")) {
    e.preventDefault();
    selectEvent(state.shown[Number(item.dataset.index)]);
  }
});
$("search").addEventListener("input", () => {
  renderList();
  renderMatches();
});
$("search").addEventListener("keydown", (e) => {
  const first = $("matches").querySelector("li");
  if (e.key === "Enter" && first) chooseMatch(Number(first.dataset.object));
  if (e.key === "Escape") e.target.blur();
});
$("search").addEventListener("blur", () => setTimeout(() => ($("matches").style.display = "none"), 150));
$("matches").addEventListener("pointerdown", (e) => {
  const item = e.target.closest("li[data-object]");
  if (item) chooseMatch(Number(item.dataset.object));
});
// Picking an object from the search clears the filter again, so the list of
// passes stays whole; "Show its passes" in the inspector narrows it on request.
function chooseMatch(index) {
  $("search").value = "";
  $("matches").style.display = "none";
  renderList();
  selectObject(index);
}
$("sort").addEventListener("change", renderList);
for (const button of document.querySelectorAll(".layer")) {
  button.addEventListener("click", () => {
    const kind = button.dataset.kind;
    if (state.hidden.has(kind)) state.hidden.delete(kind);
    else state.hidden.add(kind);
    button.setAttribute("aria-pressed", String(!state.hidden.has(kind)));
    applyLayers();
  });
}
const togglePause = () => {
  state.paused = !state.paused;
  $("pause").textContent = state.paused ? "Resume" : "Pause";
};
$("pause").addEventListener("click", togglePause);
for (const button of document.querySelectorAll("[data-speed]"))
  button.addEventListener("click", () => setSpeed(Number(button.dataset.speed)));
$("now").addEventListener("click", () => {
  state.simTime = Math.min(Math.max(Date.now() / 1000, state.windowStart), state.windowEnd);
  state.replayUntil = 0;
  $("hud").style.display = "none";
  setSpeed(1);
});
$("close").addEventListener("click", () => clearSelection());
let scrubbing = false;
timeline.addEventListener("pointerdown", (e) => {
  scrubbing = true;
  timeline.setPointerCapture(e.pointerId);
  scrub(e.clientX);
});
timeline.addEventListener("pointermove", (e) => scrubbing && scrub(e.clientX));
timeline.addEventListener("pointerup", () => {
  scrubbing = false;
  renderList();
});
window.addEventListener("keydown", (e) => {
  const typing = e.target instanceof HTMLInputElement || e.target instanceof HTMLSelectElement;
  if (e.key === "/" && !typing) {
    e.preventDefault();
    $("search").focus();
  } else if (e.key === " " && !typing && !(e.target instanceof HTMLButtonElement) && !e.target.closest?.("li")) {
    e.preventDefault();
    togglePause();
  } else if (e.key === "Escape" && !typing) {
    clearSelection();
  } else if ((e.key === "ArrowLeft" || e.key === "ArrowRight") && !typing) {
    state.simTime += (e.key === "ArrowLeft" ? -1 : 1) * 600;
  }
});
window.addEventListener("resize", resize);
setInterval(() => state.prepared && !document.hidden && $("sort").value === "time" && renderList(), 30000);

resize();
requestAnimationFrame(frame);
load().catch((error) => {
  $("status").textContent = `Could not load the data (${error.message}). Run "orbitwatch screen" first.`;
});
