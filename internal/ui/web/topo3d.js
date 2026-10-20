/* =====================================================================
 * WebGL layer for the topology wires (three.js, bundled locally).
 *
 * Each wire is a wide ribbon drawn by a shader: a soft glowing body, comet
 * pulses travelling source -> PC for download in the middle lane, and smaller
 * pulses travelling PC -> source for upload in a side lane. Sparks ride the
 * wire, the ports glow, and an aura behind the PC breathes with the traffic.
 *
 * app.js keeps owning layout (it samples the SVG paths) and calls:
 *   topo3d.setWire(key, {points, color, mode, rx, tx})   mode: flow|ready|idle|dead
 *   topo3d.removeWire(key)
 *   topo3d.setPC({x, y, w, h}, totalMbps, dark)
 * If WebGL is unavailable this module never sets window.topo3d and the SVG
 * wires stay in use.
 * ===================================================================== */
import * as THREE from './three.module.min.js';

// Colors come straight from the CSS tokens (sRGB) and are drawn as given.
THREE.ColorManagement.enabled = false;

const VERT = /* glsl */ `
attribute float aAlong;
attribute float aAcross;
varying float vAlong;
varying float vAcross;
void main() {
  vAlong = aAlong;
  vAcross = aAcross;
  gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0);
}`;

const FRAG = /* glsl */ `
uniform vec3 uColor;
uniform vec3 uHi;        // highlight color (white in dark mode, deep tint in light mode)
uniform float uLen;      // ribbon length in px
uniform float uPhaseDn;  // pulse phases, advanced by JS so speed changes never jump
uniform float uPhaseUp;
uniform float uDn;       // pulse intensity 0..1.2
uniform float uUp;
uniform float uMode;     // 0 flow, 1 ready, 2 idle, 3 dead
uniform float uTime;
uniform float uDark;     // 1 on a dark page (additive glow), 0 on a light one
varying float vAlong;
varying float vAcross;

// comet: sharp head at u=1, long fading tail behind it
float comet(float u, float tail) {
  return pow(smoothstep(1.0 - tail, 1.0, u) * (1.0 - smoothstep(0.97, 1.0, u)), 1.6);
}

void main() {
  float a = abs(vAcross);
  float ends = smoothstep(0.0, 18.0, vAlong) * smoothstep(0.0, 10.0, uLen - vAlong);
  float body = exp(-a * a * 5.0);
  float core = exp(-a * a * 38.0);

  if (uMode > 1.5) {                      // idle (dashed, neutral) or dead (dotted, red)
    float dash = uMode < 2.5 ? step(0.45, fract(vAlong / 13.0)) : step(0.7, fract(vAlong / 9.0));
    float alpha = core * dash * (uMode < 2.5 ? 0.55 : 0.7) * ends;
    gl_FragColor = vec4(uColor, alpha);
    return;
  }

  float breathe = 0.85 + 0.15 * sin(uTime * 2.2 + vAlong * 0.02);
  float base = (0.16 * body + 0.32 * core) * (uMode > 0.5 ? breathe : 1.0) * mix(1.5, 1.0, uDark);

  // download: two staggered comet trains in the middle lane
  float laneD = exp(-pow(vAcross / 0.42, 2.0));
  float uD1 = fract(vAlong / 120.0 - uPhaseDn);
  float uD2 = fract(vAlong / 120.0 - uPhaseDn + 0.5);
  float dn = (comet(uD1, 0.55) + 0.55 * comet(uD2, 0.35)) * laneD * uDn;

  // upload: thinner trains travelling the other way in a side lane
  float laneU = exp(-pow((vAcross + 0.62) / 0.2, 2.0));
  float uU = fract(-vAlong / 80.0 - uPhaseUp);
  float up = comet(uU, 0.4) * laneU * uUp;

  // Dark page (additive): brightness is the color itself. Light page (normal
  // blending): keep the pure series color and let alpha carry the intensity,
  // otherwise dim pulses turn into grey smudges.
  vec3 glowCol = uColor * (base + 1.25 * dn) + uHi * (0.55 * dn * core + 0.9 * up);
  vec3 flatCol = mix(uColor, min(uColor * 1.12 + 0.04, 1.0), clamp(dn, 0.0, 1.0));
  vec3 col = mix(flatCol, glowCol, uDark);
  float alpha = clamp(base + 1.1 * dn + 0.95 * up, 0.0, 1.0) * ends;
  gl_FragColor = vec4(col, alpha);
}`;

function glowTexture() {
  const c = document.createElement('canvas');
  c.width = c.height = 64;
  const g = c.getContext('2d');
  const r = g.createRadialGradient(32, 32, 0, 32, 32, 32);
  r.addColorStop(0, 'rgba(255,255,255,1)');
  r.addColorStop(0.25, 'rgba(255,255,255,0.55)');
  r.addColorStop(1, 'rgba(255,255,255,0)');
  g.fillStyle = r;
  g.fillRect(0, 0, 64, 64);
  const t = new THREE.CanvasTexture(c);
  return t;
}

// Same mapping as the SVG fallback: log scale so 1 Mbps moves and 800 Mbps is fast but readable.
const speedOf = (mbps) => (mbps < 0.02 ? 0 : 0.35 + 0.16 * Math.log2(1 + mbps)); // pulses per second
const intensityOf = (mbps) => (mbps < 0.02 ? 0 : Math.min(1.2, 0.5 + 0.07 * Math.log2(1 + mbps)));
const ease = (cur, target, dt, k = 2.5) => cur + (target - cur) * Math.min(1, dt * k);
const MODE = { flow: 0, ready: 1, idle: 2, dead: 3 };
const HALF_WIDTH = 11; // px: the ribbon is 22px wide; the visible body is ~12px

function ribbon(points) {
  // points: [[x, y], ...] in CSS px along the wire, roughly evenly spaced.
  const n = points.length;
  const pos = new Float32Array(n * 2 * 3);
  const along = new Float32Array(n * 2);
  const across = new Float32Array(n * 2);
  const idx = [];
  const cum = [0];
  for (let i = 1; i < n; i++) cum[i] = cum[i - 1] + Math.hypot(points[i][0] - points[i - 1][0], points[i][1] - points[i - 1][1]);
  for (let i = 0; i < n; i++) {
    const p = points[Math.max(0, i - 1)], q = points[Math.min(n - 1, i + 1)];
    let tx = q[0] - p[0], ty = q[1] - p[1];
    const l = Math.hypot(tx, ty) || 1;
    tx /= l; ty /= l;
    const nx = -ty, ny = tx;
    for (let s = 0; s < 2; s++) {
      const side = s ? 1 : -1, k = i * 2 + s;
      pos[k * 3] = points[i][0] + nx * HALF_WIDTH * side;
      pos[k * 3 + 1] = points[i][1] + ny * HALF_WIDTH * side;
      pos[k * 3 + 2] = 0;
      along[k] = cum[i];
      across[k] = side;
    }
    if (i < n - 1) { const k = i * 2; idx.push(k, k + 1, k + 2, k + 1, k + 3, k + 2); }
  }
  const g = new THREE.BufferGeometry();
  g.setAttribute('position', new THREE.BufferAttribute(pos, 3));
  g.setAttribute('aAlong', new THREE.BufferAttribute(along, 1));
  g.setAttribute('aAcross', new THREE.BufferAttribute(across, 1));
  g.setIndex(idx);
  return { geometry: g, length: cum[n - 1], cum };
}

function pointAt(w, d) {
  // position at distance d along the sampled wire (linear between samples)
  const { cum, points } = w;
  if (d <= 0) return points[0];
  if (d >= w.length) return points[points.length - 1];
  let lo = 0, hi = cum.length - 1;
  while (hi - lo > 1) { const mid = (lo + hi) >> 1; if (cum[mid] < d) lo = mid; else hi = mid; }
  const t = (d - cum[lo]) / (cum[hi] - cum[lo] || 1);
  return [points[lo][0] + (points[hi][0] - points[lo][0]) * t, points[lo][1] + (points[hi][1] - points[lo][1]) * t];
}

function create(host) {
  const canvas = document.createElement('canvas');
  canvas.className = 'topo-gl';
  canvas.setAttribute('aria-hidden', 'true');
  let renderer;
  try {
    renderer = new THREE.WebGLRenderer({ canvas, alpha: true, antialias: true, premultipliedAlpha: true });
  } catch {
    return null; // no WebGL: keep the SVG wires
  }
  host.prepend(canvas);
  renderer.outputColorSpace = THREE.LinearSRGBColorSpace;
  renderer.setClearColor(0x000000, 0);
  const scene = new THREE.Scene();
  // Pixel-space orthographic camera with y pointing down, like the DOM.
  const camera = new THREE.OrthographicCamera(0, 1, 0, 1, -10, 10);
  const glow = glowTexture();
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');

  const aura = new THREE.Sprite(new THREE.SpriteMaterial({ map: glow, side: THREE.DoubleSide, transparent: true, depthWrite: false, opacity: 0 }));
  aura.renderOrder = -1;
  scene.add(aura);

  const wires = new Map();
  let dark = false;
  let pc = null, pcTotal = 0, auraLevel = 0;
  let W = 0, H = 0;
  let last = 0, time = 0;

  function applyBlending() {
    // Additive light reads as glow on a dark page; on a light page it would wash
    // out to white, so normal blending is used there.
    const b = dark ? THREE.AdditiveBlending : THREE.NormalBlending;
    for (const m of [aura.material, ...[...wires.values()].flatMap((w) => [w.mesh.material, w.sparks.material, w.portSprite.material])]) {
      if (m.blending !== b) { m.blending = b; m.needsUpdate = true; }
    }
    for (const w of wires.values()) {
      w.mesh.material.uniforms.uHi.value.set(dark ? 0xffffff : w.color);
      w.mesh.material.uniforms.uDark.value = dark ? 1 : 0;
    }
  }

  function resize() {
    const w = host.clientWidth, h = host.clientHeight;
    if (w === W && h === H) return;
    W = w; H = h;
    renderer.setPixelRatio(Math.min(2, window.devicePixelRatio || 1));
    renderer.setSize(W, H, false);
    camera.left = 0; camera.right = W; camera.top = 0; camera.bottom = H;
    camera.updateProjectionMatrix();
  }

  function makeWire(key) {
    const material = new THREE.ShaderMaterial({
      vertexShader: VERT, fragmentShader: FRAG, transparent: true, depthWrite: false, depthTest: false, side: THREE.DoubleSide,
      uniforms: {
        uColor: { value: new THREE.Color() }, uHi: { value: new THREE.Color(0xffffff) }, uLen: { value: 1 },
        uPhaseDn: { value: Math.random() }, uPhaseUp: { value: Math.random() }, uDn: { value: 0 }, uUp: { value: 0 },
        uMode: { value: 2 }, uTime: { value: 0 }, uDark: { value: 0 },
      },
    });
    const mesh = new THREE.Mesh(new THREE.BufferGeometry(), material);
    const N = 14;
    const sparkGeo = new THREE.BufferGeometry();
    sparkGeo.setAttribute('position', new THREE.BufferAttribute(new Float32Array(N * 3), 3));
    const sparks = new THREE.Points(sparkGeo, new THREE.PointsMaterial({
      map: glow, size: 9, sizeAttenuation: false, transparent: true, depthWrite: false, depthTest: false, opacity: 0,
    }));
    const seeds = Array.from({ length: N }, () => ({ f: Math.random(), j: (Math.random() - 0.5) * 9, v: 0.7 + Math.random() * 0.6, tw: Math.random() * 6 }));
    const portSprite = new THREE.Sprite(new THREE.SpriteMaterial({ map: glow, side: THREE.DoubleSide, transparent: true, depthWrite: false, depthTest: false, opacity: 0 }));
    scene.add(mesh, sparks, portSprite);
    const w = { key, mesh, sparks, seeds, portSprite, points: null, cum: [0], length: 0, color: new THREE.Color(), mode: 'idle', rx: 0, tx: 0, vDn: 0, vUp: 0, iDn: 0, iUp: 0 };
    wires.set(key, w);
    applyBlending();
    return w;
  }

  const api = {
    setWire(key, { points, color, mode, rx, tx }) {
      const w = wires.get(key) || makeWire(key);
      if (points && points !== w.points && points.length > 1) {
        const r = ribbon(points);
        w.mesh.geometry.dispose();
        w.mesh.geometry = r.geometry;
        w.points = points; w.cum = r.cum; w.length = r.length;
        w.mesh.material.uniforms.uLen.value = r.length;
      }
      if (color) {
        w.color.set(color);
        w.mesh.material.uniforms.uColor.value.copy(w.color);
        w.sparks.material.color.copy(w.color);
        w.portSprite.material.color.copy(w.color);
        w.mesh.material.uniforms.uHi.value.set(dark ? 0xffffff : w.color);
      }
      w.mode = mode;
      w.mesh.material.uniforms.uMode.value = MODE[mode] ?? 2;
      w.rx = rx || 0;
      w.tx = tx || 0;
    },
    removeWire(key) {
      const w = wires.get(key);
      if (!w) return;
      scene.remove(w.mesh, w.sparks, w.portSprite);
      w.mesh.geometry.dispose(); w.mesh.material.dispose();
      w.sparks.geometry.dispose(); w.sparks.material.dispose();
      w.portSprite.material.dispose();
      wires.delete(key);
    },
    keys: () => [...wires.keys()],
    setPC(rect, totalMbps, isDark) {
      pc = rect;
      pcTotal = totalMbps;
      if (isDark !== dark) { dark = isDark; applyBlending(); }
    },
  };

  function frame(ts) {
    requestAnimationFrame(frame);
    const dt = Math.min(0.1, (ts - (last || ts)) / 1000);
    last = ts;
    if (host.offsetParent === null || document.hidden) return; // folded away: draw nothing
    resize();
    const still = reduced.matches;
    if (!still) time += dt;

    for (const w of wires.values()) {
      const u = w.mesh.material.uniforms;
      const flowing = w.mode === 'flow';
      w.vDn = ease(w.vDn, flowing ? speedOf(w.rx) : 0, dt);
      w.vUp = ease(w.vUp, flowing ? speedOf(w.tx) : 0, dt);
      w.iDn = ease(w.iDn, flowing ? intensityOf(w.rx) : 0, dt, 3);
      w.iUp = ease(w.iUp, flowing ? intensityOf(w.tx) : 0, dt, 3);
      if (!still) { u.uPhaseDn.value = (u.uPhaseDn.value + w.vDn * dt) % 1; u.uPhaseUp.value = (u.uPhaseUp.value + w.vUp * dt) % 1; }
      u.uDn.value = still ? Math.min(w.iDn, 0.5) : w.iDn;
      u.uUp.value = still ? Math.min(w.iUp, 0.5) : w.iUp;
      u.uTime.value = time;

      // sparks ride the download lane, a little faster than the pulses, and twinkle
      const pos = w.sparks.geometry.attributes.position;
      const live = flowing && w.points && w.iDn > 0.02;
      w.sparks.material.opacity = live ? Math.min(0.9, w.iDn) : 0;
      if (live) {
        w.seeds.forEach((s, i) => {
          if (!still) s.f = (s.f + (w.vDn * 120 * s.v * dt) / w.length) % 1;
          const [x, y] = pointAt(w, s.f * w.length);
          const fade = Math.min(1, s.f * 8, (1 - s.f) * 8);
          pos.setXYZ(i, x, y + s.j * fade * (0.6 + 0.4 * Math.sin(time * 3 + s.tw)), 0);
        });
        pos.needsUpdate = true;
      }
      // port glow where the wire meets the PC
      if (w.points) {
        const [ex, ey] = w.points[w.points.length - 1];
        const k = flowing ? 0.35 + 0.65 * w.iDn : w.mode === 'ready' ? 0.25 : 0;
        const s = 26 + 10 * Math.sin(time * 3 + w.length) * k;
        w.portSprite.position.set(ex, ey, 0);
        w.portSprite.scale.set(s, s, 1);
        w.portSprite.material.opacity = k * (dark ? 0.9 : 0.55);
      }
    }

    // aura behind the PC card, breathing with the total traffic
    if (pc) {
      const colors = [...wires.values()].filter((w) => w.mode === 'flow');
      const target = pcTotal > 0.05 ? Math.min(1, 0.35 + 0.08 * Math.log2(1 + pcTotal)) : 0;
      auraLevel = ease(auraLevel, target, dt, 1.5);
      if (colors.length) {
        const c = new THREE.Color(0, 0, 0);
        for (const w of colors) c.add(w.color);
        aura.material.color.copy(c.multiplyScalar(1 / colors.length));
      }
      const pulse = 1 + 0.05 * Math.sin(time * 1.6);
      aura.position.set(pc.x + pc.w / 2, pc.y + pc.h / 2, 0);
      aura.scale.set(pc.w * 1.7 * pulse, pc.h * 1.5 * pulse, 1);
      aura.material.opacity = auraLevel * (dark ? 0.32 : 0.22);
    }
    renderer.render(scene, camera);
  }
  requestAnimationFrame(frame);
  return api;
}

const host = document.getElementById('topo-body');
const api = host && create(host);
if (api) {
  window.topo3d = api;
  document.documentElement.classList.add('gl-wires');
  window.dispatchEvent(new Event('topo3d-ready'));
}
