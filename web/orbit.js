// Orbit maths for the viewer.
//
// The close approaches shown in the list are computed on the server with the
// full SGP4 model. Drawing 29,000 moving dots does not need that precision,
// so the browser uses a much lighter model: an ellipse whose orientation
// drifts at the rate the Earth's bulge (J2) causes. It stays within a few
// tens of kilometres of the full model over days, far less than a pixel.

export const MU = 398600.4418; // km^3/s^2
export const EARTH_RADIUS_KM = 6378.137;
const J2 = 1.08262668e-3;
const TWO_PI = 2 * Math.PI;

// prepare derives what is constant for each object, once.
export function prepare(cat) {
  const n = cat.id.length;
  const p = {
    count: n,
    a: new Float64Array(n),
    raanRate: new Float64Array(n),
    argpRate: new Float64Array(n),
    sinI: new Float64Array(n),
    cosI: new Float64Array(n),
    cat,
  };
  for (let i = 0; i < n; i++) {
    const motion = cat.n[i];
    const e = cat.e[i];
    const a = Math.cbrt(MU / (motion * motion));
    const semiLatus = a * (1 - e * e);
    const k = 1.5 * J2 * (EARTH_RADIUS_KM / semiLatus) ** 2 * motion;
    const sinI = Math.sin(cat.i[i]);
    const cosI = Math.cos(cat.i[i]);
    p.a[i] = a;
    p.sinI[i] = sinI;
    p.cosI[i] = cosI;
    p.raanRate[i] = -k * cosI;
    p.argpRate[i] = k * (2 - 2.5 * sinI * sinI);
  }
  return p;
}

// position writes the inertial position of object i at unix time t (seconds)
// into out[offset..offset+2], in km. meanAnomalyShift moves the object along
// its orbit and is used to draw the whole orbit as a ring.
export function position(p, i, t, out, offset, meanAnomalyShift = 0) {
  const cat = p.cat;
  const dt = t - cat.epoch[i];
  const e = cat.e[i];
  const mean = cat.m[i] + cat.n[i] * dt + meanAnomalyShift;
  let ecc = mean;
  for (let k = 0; k < 6; k++) {
    ecc -= (ecc - e * Math.sin(ecc) - mean) / (1 - e * Math.cos(ecc));
  }
  const a = p.a[i];
  const xOrbit = a * (Math.cos(ecc) - e);
  const yOrbit = a * Math.sqrt(1 - e * e) * Math.sin(ecc);

  const argp = cat.argp[i] + p.argpRate[i] * dt;
  const raan = cat.raan[i] + p.raanRate[i] * dt;
  const cosW = Math.cos(argp);
  const sinW = Math.sin(argp);
  const cosO = Math.cos(raan);
  const sinO = Math.sin(raan);
  const xNode = cosW * xOrbit - sinW * yOrbit;
  const yNode = sinW * xOrbit + cosW * yOrbit;
  out[offset] = cosO * xNode - sinO * p.cosI[i] * yNode;
  out[offset + 1] = sinO * xNode + cosO * p.cosI[i] * yNode;
  out[offset + 2] = p.sinI[i] * yNode;
}

// gmst is how far the Earth has turned at unix time t, in radians.
export function gmst(t) {
  const julian = t / 86400 + 2440587.5;
  const centuries = (julian - 2451545.0) / 36525.0;
  const seconds =
    -6.2e-6 * centuries ** 3 + 0.093104 * centuries ** 2 + (876600.0 * 3600 + 8640184.812866) * centuries + 67310.54841;
  const angle = (((seconds * Math.PI) / 43200) % TWO_PI) + TWO_PI;
  return angle % TWO_PI;
}

// period is the orbital period of object i in seconds.
export function period(p, i) {
  return TWO_PI / p.cat.n[i];
}
