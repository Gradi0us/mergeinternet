'use strict';

/* =====================================================================
 * mergeinternet dashboard
 * Plain JS, no dependencies, works offline. All server-provided strings
 * (interface names, device names, log lines, errors) go in via textContent.
 * ===================================================================== */

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k === 'vars') for (const [n, val] of Object.entries(v)) el.style.setProperty(n, val);
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else if (k === 'value') el.value = v;
      else if (k === 'checked') el.checked = !!v;
      else if (v === true) el.setAttribute(k, '');
      else el.setAttribute(k, v);
    }
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

const SVGNS = 'http://www.w3.org/2000/svg';
function s(tag, attrs) {
  const el = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === 'class') el.setAttribute('class', v);
    else if (k === 'text') el.textContent = v;
    else if (k === 'fill' || k === 'stroke') el.style[k] = v;
    else el.setAttribute(k, v);
  }
  return el;
}

/* ---------- icons (constant markup, never user data) ---------- */
const ICONS = {
  logo: '<path d="M3 5c6 0 7 7 12 7h6"/><path d="M3 12h18"/><path d="M3 19c6 0 7-7 12-7"/>',
  power: '<path d="M18.36 6.64a9 9 0 1 1-12.73 0"/><line x1="12" y1="2" x2="12" y2="12"/>',
  spinner: '<path d="M21 12a9 9 0 1 1-6.22-8.56"/>',
  wifi: '<path d="M5 12.55a11 11 0 0 1 14.08 0"/><path d="M1.42 9a16 16 0 0 1 21.16 0"/><path d="M8.53 16.11a6 6 0 0 1 6.95 0"/><line x1="12" y1="20" x2="12.01" y2="20"/>',
  phone: '<rect x="5" y="2" width="14" height="20" rx="2"/><line x1="12" y1="18" x2="12.01" y2="18"/>',
  ethernet: '<rect x="16" y="16" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="9" y="2" width="6" height="6" rx="1"/><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3"/><path d="M12 12V8"/>',
  monitor: '<rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/>',
  plus: '<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>',
  sliders: '<line x1="4" y1="21" x2="4" y2="14"/><line x1="4" y1="10" x2="4" y2="3"/><line x1="12" y1="21" x2="12" y2="12"/><line x1="12" y1="8" x2="12" y2="3"/><line x1="20" y1="21" x2="20" y2="16"/><line x1="20" y1="12" x2="20" y2="3"/><line x1="1" y1="14" x2="7" y2="14"/><line x1="9" y1="8" x2="15" y2="8"/><line x1="17" y1="16" x2="23" y2="16"/>',
  lock: '<rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>',
  check: '<polyline points="20 6 9 17 4 12"/>',
  x: '<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>',
  minus: '<line x1="5" y1="12" x2="19" y2="12"/>',
  alert: '<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/>',
  alertCircle: '<circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>',
  info: '<circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/>',
  shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/><polyline points="9 12 11 14 15 10"/>',
  zap: '<polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/>',
  down: '<line x1="12" y1="5" x2="12" y2="19"/><polyline points="19 12 12 19 5 12"/>',
  up: '<line x1="12" y1="19" x2="12" y2="5"/><polyline points="5 12 12 5 19 12"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/>',
  moon: '<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/>',
  refresh: '<polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/>',
  eye: '<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>',
  eyeOff: '<path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/>',
  copy: '<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  server: '<rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/><line x1="6" y1="6" x2="6.01" y2="6"/><line x1="6" y1="18" x2="6.01" y2="18"/>',
  clock: '<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>',
  chevron: '<polyline points="6 9 12 15 18 9"/>',
  layers: '<polygon points="12 2 2 7 12 12 22 7 12 2"/><polyline points="2 17 12 22 22 17"/><polyline points="2 12 12 17 22 12"/>',
};
function icon(name) {
  const svg = document.createElementNS(SVGNS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('class', 'i');
  svg.setAttribute('aria-hidden', 'true');
  svg.innerHTML = ICONS[name] || '';
  return svg;
}

/* ---------- formatting (vi-VN: comma decimals) ---------- */
const nf0 = new Intl.NumberFormat('vi-VN', { maximumFractionDigits: 0 });
const nf1 = new Intl.NumberFormat('vi-VN', { minimumFractionDigits: 1, maximumFractionDigits: 1 });
const nf2 = new Intl.NumberFormat('vi-VN', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const sum = (a) => a.reduce((x, y) => x + (y || 0), 0);
const fmtNum = (v) => (v == null || !isFinite(v) ? '–' : v >= 100 ? nf0.format(v) : nf1.format(v));
const fmtTick = (v) => (Number.isInteger(v) ? nf0.format(v) : nf1.format(v));
function fmtBytes(b) {
  if (!b) return '0 MB';
  if (b < 1e6) return nf0.format(b / 1e3) + ' KB';
  if (b < 1e9) return nf1.format(b / 1e6) + ' MB';
  return nf2.format(b / 1e9) + ' GB';
}
function fmtDur(ms) {
  const t = Math.max(0, Math.floor(ms / 1000));
  const hh = Math.floor(t / 3600), mm = Math.floor((t % 3600) / 60), ss = t % 60;
  if (hh) return `${hh} giờ ${String(mm).padStart(2, '0')} phút`;
  if (mm) return `${mm} phút ${String(ss).padStart(2, '0')} giây`;
  return `${ss} giây`;
}
const fmtDate = (ms) => (ms > 0 ? new Date(ms).toLocaleDateString('vi-VN') : '');

/* Speed unit: MB/s (what download managers show) or Mbps (what speed tests show). */
const UNITS = { MBps: { label: 'MB/s', div: 8 }, Mbps: { label: 'Mbps', div: 1 } };
const unit = () => UNITS[state.unit];
const toUnit = (mbps) => mbps / unit().div;
const fromUnit = (v) => v * unit().div;
const fmtSpeed = (mbps) => `${fmtNum(toUnit(mbps))} ${unit().label}`;

const KIND = {
  wifi: { icon: 'wifi', label: 'Wi-Fi' },
  ethernet: { icon: 'ethernet', label: 'Cáp mạng' },
  android: { icon: 'phone', label: 'Android' },
  iphone: { icon: 'phone', label: 'iPhone' },
  virtual: { icon: 'layers', label: 'Card ảo' },
};
const VIA = { lan: 'LAN', wifi: 'Wi-Fi', usb: 'qua USB', hotspot: 'qua Wi-Fi hotspot' };
const isPhone = (x) => x.via === 'usb' || x.via === 'hotspot' || x.kind === 'android' || x.kind === 'iphone';
const nodeIcon = (x) => (isPhone(x) ? 'phone' : x.via === 'wifi' || x.kind === 'wifi' ? 'wifi' : 'ethernet');
function roleLine(x) {
  const via = VIA[x.via] || '';
  if (x.role === 'main') return `Đường chính của PC${via ? ' · ' + via.replace('qua ', '') : ''}`;
  return isPhone(x) ? `Thiết bị mở rộng${via ? ' · ' + via : ''}` : `Mạng mở rộng${via ? ' · ' + via : ''}`;
}
const seriesVar = (id) => `var(--s${(id % 8) + 1})`;
const MODE_LABEL = { aggregate: 'Gộp tốc độ', redundant: 'Ổn định tối đa' };

/* ---------- token & API ---------- */
const DEMO = new URLSearchParams(location.search).get('demo');
const TOKEN = (() => {
  const m = location.hash.match(/token=([0-9a-f]+)/);
  if (m) {
    try { sessionStorage.setItem('mi-token', m[1]); } catch { /* storage blocked */ }
    history.replaceState(null, '', location.pathname + location.search); // keep the token out of the address bar
    return m[1];
  }
  try { return sessionStorage.getItem('mi-token') || ''; } catch { return ''; }
})();

class ApiError extends Error {}

async function api(method, path, body) {
  if (DEMO !== null) return demoApi(method, path, body);
  const opts = { method, cache: 'no-store', headers: { 'X-Token': TOKEN } };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw new ApiError((data && data.error) || `Lỗi ${res.status}`);
  return data;
}

/* ---------- state ---------- */
const state = {
  tab: 'overview',
  status: null,
  unit: (() => { try { return localStorage.getItem('mi-unit') === 'Mbps' ? 'Mbps' : 'MBps'; } catch { return 'MBps'; } })(),
  advanced: (() => { if (new URLSearchParams(location.search).get('adv') === '1') return true; try { return localStorage.getItem('mi-adv') === '1'; } catch { return false; } })(),
  hoverBack: null,
  chart: null,
  dismissedErr: '',
  lossWindow: [],
  dismissedJob: 0,
  prefs: null,
  openPanel: null, // id of the source whose limit panel is open
  drafts: {}, // unsaved limit-panel values by id
  wires: new Map(), // id -> persistent svg elements (recreating them would restart the flow animation)
  // settings
  settingsLoaded: false,
  loadedConfig: null,
  form: null,
  savedSnapshot: '',
  devices: [],
  showAllIfs: false,
  // logs
  logSeq: 0,
  logLines: [],
};

/* ---------- toast ---------- */
let toastTimer = 0;
function toast(msg, opts = {}) {
  const el = $('#toast');
  el.replaceChildren(h('span', { text: msg }));
  el.classList.toggle('error', !!opts.error);
  if (opts.action) el.append(h('button', { type: 'button', text: opts.action.label, onclick: () => { el.hidden = true; opts.action.run(); } }));
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, opts.action ? 9000 : opts.error ? 6000 : 3000);
}

/* ---------- theme ---------- */
function currentTheme() {
  const t = document.documentElement.dataset.theme;
  if (t) return t;
  return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}
function applyThemeIcon() {
  $('#theme-toggle').replaceChildren(icon(currentTheme() === 'dark' ? 'sun' : 'moon'));
}
function initTheme() {
  try {
    const saved = localStorage.getItem('mi-theme');
    if (saved === 'light' || saved === 'dark') document.documentElement.dataset.theme = saved;
  } catch { /* storage blocked */ }
  applyThemeIcon();
  $('#theme-toggle').addEventListener('click', () => {
    const next = currentTheme() === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    try { localStorage.setItem('mi-theme', next); } catch { /* storage blocked */ }
    applyThemeIcon();
    renderCharts();
    drawWires();
  });
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { applyThemeIcon(); renderCharts(); });
}

/* ---------- tabs ---------- */
function switchTab(name, focus) {
  state.tab = name;
  for (const btn of $$('.tab')) {
    const on = btn.dataset.tab === name;
    btn.setAttribute('aria-selected', on);
    btn.tabIndex = on ? 0 : -1;
    if (on && focus) btn.focus();
  }
  for (const p of $$('.tabpanel')) p.hidden = p.id !== `tab-${name}`;
  if (name === 'settings' && !state.settingsLoaded) loadSettings();
  if (name === 'logs') pollLogs();
  if (name === 'overview') { renderCharts(); requestAnimationFrame(drawWires); }
}
function initTabs() {
  const tabs = $$('.tab');
  tabs.forEach((btn, i) => {
    btn.addEventListener('click', () => switchTab(btn.dataset.tab));
    btn.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
      const next = tabs[(i + (e.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length];
      switchTab(next.dataset.tab, true);
    });
  });
}

/* =====================================================================
 * OVERVIEW
 * ===================================================================== */

async function pollStatus() {
  try {
    state.status = await api('GET', '/api/status');
    $('#offline').hidden = true;
    renderOverview();
  } catch (e) {
    const box = $('#offline');
    box.hidden = false;
    if (e instanceof ApiError) box.lastElementChild.textContent = e.message;
  } finally {
    setTimeout(pollStatus, 1000);
  }
}

function renderOverview() {
  const st = state.status;
  if (!st) return;
  trackLoss(st);
  renderAlerts(st);
  renderHero(st);
  renderJob(st.job);
  const jobBusy = st.job && st.job.running && !st.job.quiet;
  $('#welcome').hidden = st.configured || jobBusy;
  placeVPSForms(st.configured);
  // The topology (sources -> PC) is always the main view. "Nâng cao" adds the
  // details around it: quota, limits and switches on each source card, and
  // the speed history charts. Before setup the one button does everything.
  for (const el of $$('.max-ext')) el.textContent = String(maxExt());
  const running = st.phase === 'running';
  const topo = $('#topo-card');
  topo.hidden = !st.configured;
  topo.classList.toggle('compact', !state.advanced);
  $('#live-card').hidden = !running || !state.advanced;
  const adv = $('#adv-toggle');
  adv.setAttribute('aria-expanded', String(state.advanced));
  adv.classList.toggle('open', state.advanced);
  if (!topo.hidden) renderTopology(st);
  renderCharts();
  renderTable();
}

function trackLoss(st) {
  if (st.phase !== 'running') { state.lossWindow = []; return; }
  state.lossWindow.push({ d: st.reorder.delivered, l: st.reorder.lost });
  if (state.lossWindow.length > 15) state.lossWindow.shift();
}
function recentLoss() {
  const w = state.lossWindow;
  if (w.length < 5) return null;
  const a = w[0], b = w[w.length - 1];
  const delivered = b.d - a.d, lost = b.l - a.l;
  if (delivered + lost < 500) return null;
  return lost / (delivered + lost);
}

function renderAlerts(st) {
  const box = $('#alerts');
  const loss = recentLoss();
  const highLoss = loss != null && loss > 0.02;
  // Only rebuild when the set of alerts changes, so focused buttons keep focus.
  const key = JSON.stringify([st.connection, st.elevated, st.wintun, st.error !== state.dismissedErr && st.error, highLoss && Math.round(loss * 1000)]);
  if (key === box.dataset.key) return;
  box.dataset.key = key;
  box.replaceChildren();
  const add = (level, title, text, ...extra) => box.append(
    h('div', { class: `alert ${level}`, role: level === 'crit' ? 'alert' : null },
      h('span', { class: 'alert-icon' }, icon(level === 'crit' ? 'alertCircle' : 'alert')),
      h('div', { class: 'alert-body' }, h('strong', { text: title }), text ? h('span', { text }) : null),
      ...extra));

  // Admin rights and wintun only matter for VPS mode.
  if (st.connection === 'vps' && !st.elevated) {
    add('warn', 'Chế độ VPS cần quyền Administrator', 'Bấm “Bật kết nối” rồi chọn Yes, hoặc chuyển sang chế độ không cần VPS trong Cài đặt.',
      h('button', { class: 'btn ghost small', type: 'button', onclick: () => elevate(false) }, icon('shield'), 'Mở lại với quyền Admin'));
  }
  if (st.connection === 'vps' && !st.wintun) {
    add('crit', 'Thiếu wintun.dll', 'Đặt wintun.dll cạnh mi-client.exe (chạy scripts\\get-wintun.ps1 một lần).');
  }
  if (st.error && st.error !== state.dismissedErr) {
    add('crit', 'Kết nối gặp lỗi', st.error,
      h('button', { class: 'x', type: 'button', 'aria-label': 'Ẩn thông báo', onclick: () => { state.dismissedErr = st.error; renderAlerts(st); } }, icon('x')));
  }
  if (highLoss) {
    add('warn', `Mất gói cao (${nf1.format(loss * 100)}%)`,
      'Tốc độ khai báo của một đường có thể cao hơn thực tế, hoặc một thiết bị đang chập chờn. Hãy đo lại và sửa trong Cài đặt.',
      h('button', { class: 'btn ghost small', type: 'button', onclick: () => switchTab('settings') }, 'Mở Cài đặt'));
  }
}

function renderHero(st) {
  const links = st.links || [];
  const usable = links.filter((l) => l.enabled && l.alive).length;
  let text, sub, dotClass = '';
  switch (st.phase) {
    case 'starting': text = 'Đang bật…'; dotClass = 'busy'; sub = `Đang tạo card mạng ảo và kết nối tới ${st.server}.`; break;
    case 'stopping': text = 'Đang tắt…'; dotClass = 'busy'; sub = 'Đang gỡ route, mạng sẽ trở lại như bình thường.'; break;
    case 'running':
      if (usable === 0) {
        text = 'Đã bật nhưng chưa thông'; dotClass = 'bad';
        sub = 'Không nguồn nào liên lạc được với VPS. Kiểm tra địa chỉ, key, và cổng UDP trên tường lửa VPS.';
      } else {
        text = 'Đã kết nối'; dotClass = 'on';
        sub = st.connection === 'vps'
          ? `Mọi kết nối IPv4 của máy đang đi qua VPS, gộp từ ${usable} nguồn.`
          : `Các kết nối mới được chia ra ${usable} nguồn (trình duyệt, IDM, Steam… dùng proxy hệ thống).`;
      }
      break;
    default:
      text = 'Đã tắt';
      sub = st.configured ? 'Mạng của máy đang chạy bình thường.' : 'Bấm “Bắt đầu ngay”: app tự tìm các nguồn mạng, đo tốc độ và bật.';
  }
  $('#status-text').textContent = text;
  $('#status-sub').textContent = sub;
  $('#status-dot').className = `status-dot ${dotClass}`;

  const meta = $('#status-meta');
  meta.replaceChildren();
  if (st.configured) {
    meta.append(st.connection === 'vps'
      ? h('span', { class: 'pill', title: 'Máy chủ VPS' }, icon('server'), `Qua VPS ${st.server}`)
      : h('span', { class: 'pill', title: 'Không cần VPS: chia theo từng kết nối' }, icon('layers'), 'Không cần VPS'));
  }
  if (st.mode && st.connection === 'vps') meta.append(h('span', { class: 'pill' }, icon(st.mode === 'redundant' ? 'shield' : 'zap'), MODE_LABEL[st.mode] || st.mode));
  if (st.phase === 'running' && st.startedAt) meta.append(h('span', { class: 'pill' }, icon('clock'), 'Đã chạy ' + fmtDur(Date.now() - st.startedAt)));

  const btn = $('#power-btn');
  const label = $('#power-label');
  const ic = btn.querySelector('.power-icon');
  btn.classList.remove('stop', 'loading');
  btn.disabled = false;
  let iconName = 'power';
  if (st.phase === 'starting' || st.phase === 'stopping') {
    btn.disabled = true; btn.classList.add('loading'); iconName = 'spinner';
    label.textContent = st.phase === 'starting' ? 'Đang bật…' : 'Đang tắt…';
  } else if (st.phase === 'running') {
    btn.classList.add('stop'); label.textContent = 'Tắt kết nối';
  } else if (!st.configured) {
    label.textContent = 'Bắt đầu ngay';
    btn.disabled = !!(st.job && st.job.running);
  } else {
    label.textContent = 'Bật kết nối';
    btn.disabled = st.connection === 'vps' && !st.wintun;
  }
  if (ic.dataset.name !== iconName) { ic.replaceChildren(icon(iconName)); ic.dataset.name = iconName; }
}

async function onPower() {
  const st = state.status;
  if (!st) return;
  try {
    if (st.phase === 'running') {
      st.phase = 'stopping'; renderHero(st);
      await api('POST', '/api/stop');
    } else if (!st.configured) {
      await setupLocal();
    } else {
      st.phase = 'starting'; st.error = ''; renderHero(st);
      const res = await api('POST', '/api/start');
      if (res && res.needAdmin) await elevate(true); // VPS mode: one click = UAC prompt + connect
    }
  } catch (e) {
    toast(e.message, { error: true });
  } finally {
    refreshNow();
  }
}

// elevate relaunches the app with admin rights; with start=true the new copy
// connects immediately, so the user never has to press the button twice.
async function elevate(start) {
  try {
    await api('POST', `/api/elevate${start ? '?start=1' : ''}`);
    toast('Chọn “Yes” trên hộp thoại của Windows. Bảng điều khiển sẽ mở lại trong tab mới.');
  } catch (e) {
    toast(e.message, { error: true });
  }
}

/* ---------- one-click setup + background jobs ---------- */

async function setupLocal() {
  try {
    await api('POST', '/api/setup/local');
    switchTab('overview');
    refreshNow();
  } catch (e) { toast(e.message, { error: true }); }
}

async function setupVPS(e) {
  e.preventDefault();
  const host = $('#v-host').value.trim();
  const password = $('#v-pass').value;
  if (!host || !password) { toast('Nhập IP và mật khẩu của VPS.', { error: true }); (host ? $('#v-pass') : $('#v-host')).focus(); return; }
  try {
    await api('POST', '/api/setup/vps', {
      host, password, user: $('#v-user').value.trim() || 'root', sshPort: Number($('#v-port').value) || 22,
    });
    $('#v-pass').value = ''; // the password is not kept anywhere
    switchTab('overview');
    refreshNow();
  } catch (err) { toast(err.message, { error: true }); }
}

async function setupCode(e) {
  e.preventDefault();
  const code = $('#v-code').value.trim();
  if (!code) { $('#v-code').focus(); return; }
  try {
    await api('POST', '/api/setup/code', { code });
    $('#v-code').value = '';
    switchTab('overview');
    refreshNow();
  } catch (err) { toast(err.message, { error: true }); }
}

const JOB_TITLE = { local: 'Đang thiết lập (không cần VPS)', vps: 'Đang cài VPS và thiết lập', code: 'Đang kết nối bằng mã', measure: 'Đang đo tốc độ' };
const STEP_ICON = { running: 'spinner', done: 'check', failed: 'x', skipped: 'minus' };

function renderJob(job) {
  const card = $('#job-card');
  const visible = job && (job.running || (!job.quiet && job.ended && job.ended !== state.dismissedJob));
  card.hidden = !visible;
  if (!visible) { card.dataset.key = ''; return; }
  const key = JSON.stringify(job);
  if (card.dataset.key === key) return;
  card.dataset.key = key;
  card.className = `card job-card${job.quiet ? ' quiet' : ''}`;
  if (job.quiet) { // background measurement after a phone was added
    const cur = (job.steps || []).filter((s) => s.status === 'running' && s.id !== 'measure').map((s) => s.title.replace(/^Đo /, '')).join(', ');
    card.replaceChildren(h('span', { class: 'chip flow' }, h('span', { class: 'dot' }), 'Đang đo tốc độ'), h('span', { text: cur || 'đường mới' }));
    return;
  }
  const steps = h('ol', { class: 'job-steps' }, (job.steps || []).map((s) => h('li', { class: s.status },
    h('span', { class: 'st' }, icon(STEP_ICON[s.status] || 'minus')),
    h('div', null, h('strong', { text: s.title }), s.detail ? h('small', { text: s.detail }) : null))));
  const head = h('div', { class: 'job-head' }, h('h2', { text: job.running ? JOB_TITLE[job.kind] || 'Đang xử lý' : (job.error ? 'Chưa xong' : 'Hoàn tất') }));
  const parts = [head, steps];
  if (!job.running) {
    const dismiss = h('button', { class: 'btn ghost small', type: 'button', onclick: () => { state.dismissedJob = job.ended; renderJob(job); } }, 'Đóng');
    if (job.error) {
      parts.push(h('div', { class: 'job-result err' }, icon('alertCircle'), h('span', { text: job.error }), dismiss));
    } else {
      const actions = job.needAdmin
        ? h('button', { class: 'btn primary small', type: 'button', onclick: () => elevate(true) }, icon('shield'), 'Bật kết nối (cấp quyền Admin)')
        : null;
      parts.push(h('div', { class: 'job-result' }, icon('check'), h('span', { text: job.message || 'Xong.' }), actions, dismiss));
    }
  }
  card.replaceChildren(...parts);
}

// The VPS forms live on the welcome card before setup, and in Settings after.
function placeVPSForms(configured) {
  const forms = $('#setup-vps');
  const home = configured ? $('#prefs-card') : $('#welcome');
  if (forms.parentElement !== home) home.append(forms);
}

/* ---------- topology: sources -> PC ---------- */

// isAway: a saved phone that is not connected right now (unplugged, hotspot
// off, or no answer while running). It is hidden from the overview and pops
// back in when it reconnects. The main line always stays: it is the PC's own.
function isAway(l, running) {
  if (l.role === 'main' || l.capped) return false;
  if (l.present === false) return true;
  return running && l.enabled && !l.alive;
}

// nodeState decides how a source looks: its chip, and how its wire is drawn.
function nodeState(l, running) {
  if (l.capped) return { cls: 'off', chip: ['warn', 'alert', 'Hết dung lượng'], wire: 'idle' };
  if (!running) {
    if (l.present === false) return { cls: 'absent', chip: ['idle', null, 'Chưa kết nối'], wire: 'idle' };
    return { cls: '', chip: ['idle', null, 'Chưa chạy'], wire: 'idle' };
  }
  if (!l.enabled) return { cls: 'off', chip: ['idle', 'minus', 'Đã ngắt'], wire: 'idle' };
  if (!l.alive) return { cls: 'dead', chip: ['crit', 'x', 'Mất kết nối'], wire: 'dead' };
  const flowing = l.rxMbps + l.txMbps > 0.05;
  if (l.sameNetAs) return { cls: '', chip: ['warn', 'alert', 'Trùng mạng'], wire: flowing ? 'flow' : 'ready' };
  if (flowing) return { cls: '', chip: ['flow', null, 'Đang truyền'], wire: 'flow' };
  return { cls: '', chip: ['good', 'check', 'Sẵn sàng'], wire: 'ready' };
}

function renderTopology(st) {
  const running = st.phase === 'running';
  const all = [...(st.links || [])].sort((a, b) => (a.role === 'main' ? 0 : 1) - (b.role === 'main' ? 0 : 1) || a.id - b.id);
  const links = all.filter((l) => !isAway(l, running));
  const away = all.filter((l) => isAway(l, running));
  const exts = all.filter((l) => l.role !== 'main'); // away phones still hold their slot
  const cands = (st.candidates || []).filter((c) => c.role === 'extension' || (c.role === 'main' && !links.some((l) => l.role === 'main')));
  const freeSlots = Math.max(0, st.maxExtensions - exts.length);
  const shownCands = cands.slice(0, links.some((l) => l.role === 'main') ? freeSlots : freeSlots + 1);

  // Live count: every source currently carrying the PC's traffic, main line included.
  const live = running ? links.filter((l) => l.enabled && l.alive).length : 0;
  $('#flow-key').hidden = !running;
  $('#live-badge').hidden = !running;
  $('#live-count').textContent = `${live} nguồn đang cấp mạng`;
  $('#topo-sub').textContent = topologyLine(links) + (st.connection === 'local' && links.length
    ? ' · không VPS: mỗi kết nối đi một đường' : '')
    + (away.length ? ` · ${away.map((l) => l.device || l.name).join(', ')} chưa kết nối (tự hiện lại khi cắm)` : '')
    // No empty placeholder cards: phones appear by themselves once connected.
    + (!links.some((l) => l.role !== 'main') && !away.length && freeSlots > 0
      ? ' · cắm điện thoại qua USB (hoặc bắt hotspot) là tự hiện ở đây' : '');

  const box = $('#topo-sources');
  const key = JSON.stringify([running, state.openPanel, state.unit,
    links.map((l) => [l.id, l.role, l.device, l.name, l.via, l.kind, l.enabled, l.capped, l.present]),
    shownCands.map((c) => [c.interface, c.name, c.via])]);
  if (key !== box.dataset.key) {
    box.dataset.key = key;
    const before = state.shownSrc;
    state.shownSrc = new Set(links.map((l) => l.id));
    box.replaceChildren(
      ...links.map((l) => {
        const n = sourceNode(l, running);
        if (before && !before.has(l.id)) n.classList.add('enter'); // just reconnected: pop in
        return n;
      }),
      ...shownCands.map((c) => candidateNode(c)));
  }
  for (const l of links) {
    const node = box.querySelector(`.src[data-id="${l.id}"]`);
    if (node) updateSourceNode(node, l, running);
  }
  renderPC(st, links, running, live);
  requestAnimationFrame(drawWires);
}

function topologyLine(links) {
  const main = links.find((l) => l.role === 'main');
  const ext = links.filter((l) => l.role !== 'main');
  if (!main && !ext.length) return `1 đường chính + tối đa ${maxExt()} thiết bị mở rộng (USB hoặc Wi-Fi hotspot)`;
  const parts = [];
  if (main) parts.push(`PC dùng ${main.via === 'wifi' ? 'Wi-Fi' : 'LAN'}`);
  const usb = ext.filter((l) => l.via === 'usb').length;
  const hot = ext.filter((l) => l.via === 'hotspot').length;
  if (usb) parts.push(`${usb} điện thoại qua USB`);
  if (hot) parts.push(`${hot} qua Wi-Fi hotspot`);
  const wifi = ext.filter((l) => l.via === 'wifi').length;
  const lan = ext.filter((l) => l.via === 'lan').length;
  if (wifi) parts.push(`${wifi} mạng Wi-Fi thêm`);
  if (lan) parts.push(`${lan} mạng LAN thêm`);
  const other = ext.length - usb - hot - wifi - lan;
  if (other > 0) parts.push(`${other} thiết bị khác`);
  return parts.join(' · ');
}

function chipEl([cls, ic, text]) {
  return h('span', { class: `chip ${cls}` }, cls === 'flow' ? h('span', { class: 'dot' }) : ic ? icon(ic) : null, text);
}

function sourceNode(l, running) {
  const isMain = l.role === 'main';
  const ns = nodeState(l, running);
  const node = h('article', { class: `src ${ns.cls}`, 'data-id': String(l.id), vars: { '--series': seriesVar(l.id) } },
    h('div', { class: 'src-head' },
      h('span', { class: 'src-icon' }, icon(nodeIcon(l))),
      h('div', { class: 'src-title' },
        h('strong', { text: l.device || l.name, title: l.name !== l.device ? `Tên trong cài đặt: ${l.name}` : null }),
        h('span', { text: roleLine(l), title: l.iface ? `Card mạng: ${l.iface}` : null })),
      h('span', { class: 'chip-slot' }, chipEl(ns.chip))),
    h('div', { class: 'src-speed' },
      h('span', { class: 'big' }, h('span', { 'data-f': 'rx' }), h('small', { 'data-f': 'unit' })),
      h('span', { class: 'meta', 'data-f': 'meta' })),
    h('p', { class: 'src-hint', 'data-f': 'hint', hidden: true }),
    isMain ? null : quotaBlock(),
    isMain ? null : (state.openPanel === l.id ? limitPanel(l) : null),
    h('div', { class: 'src-foot' },
      isMain
        ? h('span', { class: 'lock' }, icon('lock'), 'Luôn bật · đường gốc của PC')
        : h('span', { 'data-f': 'limit' }),
      isMain ? null : h('span', { class: 'src-actions' },
        h('button', { class: 'link-btn', type: 'button', 'aria-expanded': String(state.openPanel === l.id), onclick: () => togglePanel(l.id) },
          icon('sliders'), 'Giới hạn'),
        switchEl(l, running))));
  return node;
}

function quotaBlock() {
  return h('div', { class: 'quota' },
    h('div', { class: 'quota-row' }, h('span', { 'data-f': 'qlabel' }), h('strong', { 'data-f': 'qval' })),
    h('div', { class: 'meter', 'data-f': 'meter' }, h('div')));
}

function switchEl(l, running) {
  const input = h('input', { type: 'checkbox', role: 'switch', checked: l.enabled && !l.capped, disabled: !running || l.capped,
    'aria-label': `Dùng ${l.device || l.name}` });
  input.addEventListener('change', () => toggleLink(l, input));
  return h('label', { class: 'switch', title: !running ? 'Bật kết nối trước' : l.capped ? 'Đã hết dung lượng' : null },
    input, h('span', { class: 'track' }), 'Dùng');
}

function updateSourceNode(node, l, running) {
  const set = (f, v) => { const el = node.querySelector(`[data-f="${f}"]`); if (el && el.textContent !== v) el.textContent = v; };
  const ns = nodeState(l, running);
  const on = running && l.enabled && !l.capped;
  set('rx', on ? fmtNum(toUnit(l.rxMbps)) : '–');
  set('unit', unit().label);
  set('meta', on ? `↑ ${fmtSpeed(l.txMbps)}${l.alive && l.rttMs > 0 ? ` · ${nf0.format(l.rttMs)} ms` : ''}` : '');
  const slot = node.querySelector('.chip-slot');
  if (slot.dataset.k !== ns.chip.join()) { slot.replaceChildren(chipEl(ns.chip)); slot.dataset.k = ns.chip.join(); }
  node.className = `src ${ns.cls}${node.classList.contains('enter') ? ' enter' : ''}`;

  const hint = node.querySelector('[data-f="hint"]');
  let hintText = '';
  hint.classList.toggle('warn', !!l.sameNetAs);
  if (l.sameNetAs) {
    hintText = `Cùng đường internet với ${l.sameNetAs} (cùng router, cùng IP công cộng): không cộng thêm tốc độ. `
      + 'Hãy bắt Wi-Fi của nhà mạng khác, hoặc tắt “Dùng” cho đường này.';
  } else if (running && l.enabled && !l.alive && !l.capped) {
    hintText = l.via === 'usb' ? 'Kiểm tra cáp USB và chế độ “Chia sẻ Internet qua USB” trên điện thoại.'
      : l.via === 'hotspot' ? 'Kiểm tra PC còn bắt Wi-Fi hotspot của điện thoại không.'
        : 'Kiểm tra đường mạng này còn internet không.';
  } else if (!running && l.present === false) {
    hintText = isPhone(l) ? 'Chưa thấy thiết bị: cắm USB / bật hotspot rồi đợi vài giây.' : 'Card mạng này đang không có kết nối.';
  }
  hint.hidden = !hintText;
  if (hintText) hint.textContent = hintText;

  if (l.role === 'main') return;
  const quotaBytes = l.quotaGB * 1e9;
  if (quotaBytes > 0) {
    const frac = Math.min(1, l.usageBytes / quotaBytes);
    set('qlabel', l.capped ? 'Đã dùng hết dung lượng' : 'Dung lượng đã dùng');
    set('qval', `${fmtBytes(l.usageBytes)} / ${fmtBytes(quotaBytes)}`);
    const meter = node.querySelector('[data-f="meter"]');
    meter.hidden = false;
    meter.className = `meter${frac >= 1 ? ' full' : frac >= 0.8 ? ' warn' : ''}`;
    meter.firstElementChild.style.width = `${(frac * 100).toFixed(1)}%`;
  } else {
    set('qlabel', `Đã dùng${l.usageSince ? ' từ ' + fmtDate(l.usageSince) : ''}`);
    set('qval', `${fmtBytes(l.usageBytes)} · không giới hạn`);
    node.querySelector('[data-f="meter"]').hidden = true;
  }
  set('limit', l.limitMbps > 0 ? `Tối đa ${fmtSpeed(l.limitMbps)}` : 'Không giới hạn tốc độ');
}

function limitPanel(l) {
  const d = state.drafts[l.id] || {
    limit: l.limitMbps > 0 ? +toUnit(l.limitMbps).toFixed(2) : '',
    quota: l.quotaGB > 0 ? l.quotaGB : '',
  };
  state.drafts[l.id] = d;
  const limit = h('input', { type: 'number', min: '0', step: 'any', value: d.limit, placeholder: 'Không giới hạn' });
  const quota = h('input', { type: 'number', min: '0', step: 'any', value: d.quota, placeholder: 'Không giới hạn' });
  limit.addEventListener('input', () => { d.limit = limit.value; });
  quota.addEventListener('input', () => { d.quota = quota.value; });
  const apply = async () => {
    const lv = d.limit === '' ? 0 : Number(d.limit), qv = d.quota === '' ? 0 : Number(d.quota);
    if (!(lv >= 0) || !(qv >= 0)) { toast('Giá trị phải là số không âm.', { error: true }); return; }
    try {
      await api('POST', `/api/links/${l.id}`, { limitMbps: fromUnit(lv), quotaGB: qv });
      delete state.drafts[l.id];
      state.openPanel = null;
      toast(`Đã áp dụng cho “${l.device || l.name}”.`);
      refreshNow();
    } catch (e) { toast(e.message, { error: true }); }
  };
  return h('div', { class: 'limit-panel' },
    h('label', null, `Tốc độ tối đa (${unit().label})`, limit),
    h('label', null, 'Dung lượng tối đa (GB)', quota),
    h('p', { class: 'hint-line', text: 'Để trống = không giới hạn. Hết dung lượng thì thiết bị tự ngắt; số liệu được giữ qua các lần mở app.' }),
    h('div', { class: 'actions' },
      h('button', { class: 'btn ghost small spacer', type: 'button', onclick: () => resetUsage(l) }, 'Đặt lại số liệu'),
      h('button', { class: 'btn ghost small', type: 'button', onclick: () => togglePanel(l.id) }, 'Hủy'),
      h('button', { class: 'btn primary small', type: 'button', onclick: apply }, 'Áp dụng')));
}

function togglePanel(id) {
  state.openPanel = state.openPanel === id ? null : id;
  if (state.openPanel === null) delete state.drafts[id];
  renderTopology(state.status);
}

async function resetUsage(l) {
  try {
    await api('POST', `/api/links/${l.id}/reset-usage`);
    toast(`Đã đặt lại số liệu của “${l.device || l.name}”.`);
    refreshNow();
  } catch (e) { toast(e.message, { error: true }); }
}

async function toggleLink(l, input) {
  const want = input.checked;
  try {
    await api('POST', `/api/links/${l.id}`, { enabled: want });
    toast(want ? `Đã bật lại “${l.device || l.name}”` : `Đã ngắt “${l.device || l.name}”. VPS ngừng gửi qua thiết bị này sau vài giây.`);
    refreshNow();
  } catch (e) {
    input.checked = !want;
    toast(e.message, { error: true });
  }
}

function candidateNode(c) {
  const isMain = c.role === 'main';
  const add = async () => {
    try {
      // Speeds are measured automatically once the device is in use.
      const res = await api('POST', '/api/paths', { interface: c.interface, name: c.name, device: c.name, role: c.role });
      toast(res.live ? `Đã thêm “${c.name}”, đang đo tốc độ và bắt đầu truyền.` : `Đã thêm “${c.name}”. Bật kết nối để sử dụng.`);
      state.settingsLoaded = false; // settings must re-read the config
      refreshNow();
    } catch (e) { toast(e.message, { error: true }); }
  };
  return h('article', { class: 'src candidate', 'data-cand': c.interface },
    h('div', { class: 'src-head' },
      h('span', { class: 'src-icon' }, icon(nodeIcon(c))),
      h('div', { class: 'src-title' },
        h('strong', { text: c.name }),
        h('span', { text: `Phát hiện mới · ${isMain ? 'đường chính' : (VIA[c.via] || 'thiết bị mở rộng')} · ${c.interface}` })),
      h('button', { class: 'btn primary small', type: 'button', onclick: add }, icon('plus'), 'Thêm')));
}

function renderPC(st, links, running, live) {
  const pc = $('#topo-pc');
  if (!pc.firstElementChild) {
    pc.append(h('div', { class: 'pc' },
      h('div', { class: 'pc-head' }, h('span', { class: 'pc-icon' }, icon('monitor')),
        h('div', null, h('strong', { text: 'PC của bạn' }), h('span', { 'data-f': 'pc-sub' }))),
      h('div', { class: 'pc-totals' },
        h('div', { class: 'pc-dir' }, h('span', { class: 'pc-label' }, icon('down'), 'Tải xuống'),
          h('div', { class: 'pc-total' }, h('span', { 'data-f': 'pc-total' }), h('small', { 'data-f': 'pc-unit' }))),
        h('div', { class: 'pc-dir up' }, h('span', { class: 'pc-label' }, icon('up'), 'Tải lên'),
          h('div', { class: 'pc-total' }, h('span', { 'data-f': 'pc-uptotal' }), h('small', { 'data-f': 'pc-unit' })))),
      h('div', { class: 'pc-sub', 'data-f': 'pc-up' }),
      h('div', { class: 'pc-mix', 'data-f': 'pc-mix', role: 'img' }),
      h('div', { class: 'pc-legend', 'data-f': 'pc-legend' })));
  }
  const card = pc.firstElementChild;
  const set = (f, v) => { for (const el of card.querySelectorAll(`[data-f="${f}"]`)) if (el.textContent !== v) el.textContent = v; };
  const down = sum(links.map((l) => (linkOn(l, running) ? l.rxMbps : 0)));
  const up = sum(links.map((l) => (linkOn(l, running) ? l.txMbps : 0)));
  card.classList.toggle('idle', !running);
  card.classList.toggle('active', running && down + up > 0.05);
  set('pc-sub', running ? `${live} nguồn đang cấp mạng` : 'Đang dùng mạng bình thường');
  set('pc-total', running ? fmtNum(toUnit(down)) : '–');
  set('pc-uptotal', running ? fmtNum(toUnit(up)) : '–');
  set('pc-unit', unit().label);
  set('pc-up', running ? `Phiên này: ↓ ${fmtBytes(sum(links.map((l) => l.rxBytes)))} · ↑ ${fmtBytes(sum(links.map((l) => l.txBytes)))}` : 'Bật kết nối để gộp các nguồn');

  const mix = card.querySelector('[data-f="pc-mix"]');
  const legend = card.querySelector('[data-f="pc-legend"]');
  const parts = links.map((l) => ({ l, v: running && l.enabled ? l.rxMbps : 0 }));
  if (mix.childElementCount !== parts.length) {
    mix.replaceChildren(...parts.map(({ l }) => h('div', { vars: { background: seriesVar(l.id) } })));
    legend.replaceChildren(...parts.map(({ l }) => h('div', null,
      h('span', { class: 'sw', vars: { background: seriesVar(l.id) } }), h('span', { 'data-n': '' }), h('b'))));
  }
  parts.forEach(({ l, v }, i) => {
    mix.children[i].style.flexGrow = String(down > 0 ? v / down : 1 / parts.length);
    mix.children[i].style.opacity = down > 0 ? '1' : '0.25';
    const row = legend.children[i];
    row.children[1].textContent = l.device || l.name;
    row.lastElementChild.textContent = running ? `${down > 0 ? Math.round((v / down) * 100) : 0}%` : '–';
  });
  mix.setAttribute('aria-label', parts.map(({ l, v }) => `${l.device || l.name} ${down > 0 ? Math.round((v / down) * 100) : 0}%`).join(', '));
}

/* Wires are persistent SVG paths keyed by source id; only their geometry and
 * the packets' speed change, so the animation never restarts. When WebGL is
 * available (topo3d.js) the SVG only provides geometry and the wires, pulses
 * and glow are drawn by three.js instead. */
const cssVar = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
function samplePath(path, len) {
  const n = Math.max(2, Math.ceil(len / 5));
  return Array.from({ length: n + 1 }, (_, i) => { const p = path.getPointAtLength((len * i) / n); return [p.x, p.y]; });
}

function drawWires() {
  const body = $('#topo-body');
  const svg = $('#topo-svg');
  const pcEl = $('#topo-pc .pc');
  const st = state.status;
  if (!body || !pcEl || !st || body.offsetParent === null) return;
  const B = body.getBoundingClientRect();
  const P = pcEl.getBoundingClientRect();
  svg.setAttribute('viewBox', `0 0 ${B.width} ${B.height}`);
  const running = st.phase === 'running';
  const nodes = $$('#topo-sources .src[data-id], #topo-sources .src.candidate');
  const horizontal = nodes.length && P.left >= nodes[0].getBoundingClientRect().right - 2;
  const labels = $('#topo-labels');
  const seen = new Set();
  const linkById = new Map((st.links || []).map((l) => [String(l.id), l]));

  nodes.forEach((node, i) => {
    const key = node.dataset.id ?? `c:${node.dataset.cand}`;
    seen.add(key);
    const l = linkById.get(node.dataset.id);
    const ns = l ? nodeState(l, running) : { wire: 'idle' };
    const R = node.getBoundingClientRect();
    let d, mid, ex, ey;
    if (horizontal) {
      const sx = R.right - B.left, sy = R.top - B.top + R.height / 2;
      const spread = Math.min(34, P.height / (nodes.length + 1));
      ex = P.left - B.left;
      ey = P.top - B.top + P.height / 2 + (i - (nodes.length - 1) / 2) * spread;
      const c = (ex - sx) * 0.55;
      d = `M${sx},${sy} C${sx + c},${sy} ${ex - c},${ey} ${ex},${ey}`;
      mid = [(sx + ex) / 2, (sy + ey) / 2];
    } else {
      // Stacked layout: a rail on the left joins every source to the PC below.
      const rail = 14, sx = R.left - B.left, sy = R.top - B.top + Math.min(32, R.height / 2);
      ex = P.left - B.left;
      ey = P.top - B.top + 34 + i * 10;
      d = `M${sx},${sy} H${rail + 8} Q${rail},${sy} ${rail},${sy + 8} V${ey - 8} Q${rail},${ey} ${rail + 8},${ey} H${ex}`;
      mid = null;
    }

    let w = state.wires.get(key);
    if (!w) {
      w = { base: s('path', { class: 'wire' }), flow: s('path', { class: 'flow' }), port: s('circle', { class: 'port', r: 5 }), label: h('span', { class: 'wire-label' }, h('i'), h('span')) };
      svg.append(w.base, w.flow);
      w.pk = makePackets(svg);
      svg.append(w.port);
      labels.append(w.label);
      state.wires.set(key, w);
    }
    const color = l ? seriesVar(l.id) : 'var(--good)';
    if (w.d !== d) { w.d = d; w.base.setAttribute('d', d); w.len = w.base.getTotalLength(); w.pts = null; }
    w.base.setAttribute('class', `wire ${ns.wire === 'idle' ? 'idle' : ns.wire === 'dead' ? 'dead' : ''}`);
    w.base.style.stroke = color;
    w.flow.setAttribute('d', d);
    const flowing = ns.wire === 'flow';
    w.flowing = flowing;
    w.rx = flowing ? l.rxMbps : 0;
    w.tx = flowing ? l.txMbps : 0;
    w.pk.g.style.color = color;
    if (!flowing) w.pk.g.style.display = 'none'; // the loop stops when the connection does
    // The static dotted flow is only the reduced-motion fallback for the packets.
    w.flow.style.display = flowing && REDUCED.matches ? '' : 'none';
    w.flow.style.stroke = color;
    w.port.setAttribute('cx', ex.toFixed(1));
    w.port.setAttribute('cy', ey.toFixed(1));
    w.port.style.fill = ns.wire === 'idle' ? 'var(--axis)' : ns.wire === 'dead' ? 'var(--crit)' : color;
    // Speed pill on the wire, like the sketch: only where something flows.
    const showLabel = horizontal && flowing && mid;
    w.label.hidden = !showLabel;
    if (showLabel) {
      w.label.style.left = `${mid[0]}px`;
      w.label.style.top = `${mid[1]}px`;
      w.label.firstElementChild.style.background = color;
      w.label.lastElementChild.textContent = `↓ ${fmtNum(toUnit(l.rxMbps))} · ↑ ${fmtNum(toUnit(l.txMbps))} ${unit().label}`;
    }
    if (window.topo3d) {
      if (!w.pts) w.pts = samplePath(w.base, w.len);
      const mode = l ? ns.wire : 'idle';
      window.topo3d.setWire(key, {
        points: w.pts, mode, rx: w.rx, tx: w.tx,
        color: mode === 'idle' ? cssVar('--axis') : mode === 'dead' ? cssVar('--crit') : l ? cssVar(`--s${(l.id % 8) + 1}`) : cssVar('--good'),
      });
    }
  });
  if (window.topo3d) {
    for (const k of window.topo3d.keys()) if (!seen.has(k)) window.topo3d.removeWire(k);
    const shown = (st.links || []).filter((l) => seen.has(String(l.id)) && linkOn(l, running));
    window.topo3d.setPC({ x: P.left - B.left, y: P.top - B.top, w: P.width, h: P.height },
      sum(shown.map((l) => l.rxMbps + l.txMbps)), currentTheme() === 'dark');
  }
  for (const [key, w] of state.wires) {
    if (!seen.has(key)) { w.base.remove(); w.flow.remove(); w.pk.g.remove(); w.port.remove(); w.label.remove(); state.wires.delete(key); }
  }
}

function refreshNow() {
  api('GET', '/api/status').then((s) => { state.status = s; renderOverview(); }).catch(() => {});
}

function initTopology() {
  for (const b of $$('#unit-seg button')) {
    b.setAttribute('aria-checked', b.dataset.unit === state.unit);
    b.addEventListener('click', () => {
      state.unit = b.dataset.unit;
      try { localStorage.setItem('mi-unit', state.unit); } catch { /* storage blocked */ }
      for (const o of $$('#unit-seg button')) o.setAttribute('aria-checked', o === b);
      if (state.status) renderOverview();
    });
  }
  new ResizeObserver(() => requestAnimationFrame(drawWires)).observe($('#topo-body'));
}

const REDUCED = matchMedia('(prefers-reduced-motion: reduce)');
const linkOn = (l, running) => running && l.enabled && !l.capped;

/* ---------- motion: SVG wire packets (fallback when WebGL is unavailable) ---------- */
// flowSpeed maps traffic to a speed (path lengths per second) on a log scale,
// so 1 Mbps visibly moves and 800 Mbps is fast but still readable.
const flowSpeed = (mbps) => (mbps < 0.02 ? 0 : 0.16 + 0.1 * Math.log2(1 + mbps));
const ease = (cur, target, dt) => cur + (target - cur) * Math.min(1, dt * 2.5);
const anim = { last: 0 };

function animate(ts) {
  const dt = Math.min(0.1, (ts - (anim.last || ts)) / 1000);
  anim.last = ts;
  const st = state.status;
  if (st && st.phase === 'running' && !document.hidden && !REDUCED.matches) {
    stepPackets(dt);
  }
  requestAnimationFrame(animate);
}

// Packets on the topology wires: download heads travel source -> PC, smaller
// hollow upload packets travel PC -> source. Counts are fixed per wire so a
// change in traffic only changes speed and never makes packets jump.
const PK_DN = 6, PK_UP = 4;
function makePackets(svg) {
  const g = s('g', { class: 'packets' });
  g.style.display = 'none'; // shown by stepPackets once traffic flows
  const dn = Array.from({ length: PK_DN }, () => {
    const halo = s('circle', { class: 'pk-halo', r: 7 });
    const tail = s('circle', { class: 'pk-tail', r: 2.4 });
    const head = s('circle', { class: 'pk-dn', r: 3.6 });
    g.append(halo, tail, head);
    return { halo, tail, head };
  });
  const up = Array.from({ length: PK_UP }, () => {
    const c = s('circle', { class: 'pk-up', r: 2.6 });
    g.append(c);
    return c;
  });
  svg.append(g);
  return { g, dn, up, offDn: Math.random(), offUp: Math.random(), vDn: 0, vUp: 0 };
}

function stepPackets(dt) {
  if (window.topo3d) return; // three.js draws the packets
  const body = $('#topo-body');
  if (!body || body.offsetParent === null) return;
  for (const w of state.wires.values()) {
    if (!w.pk) continue;
    const show = w.flowing && w.len > 0;
    w.pk.g.style.display = show ? '' : 'none';
    if (!show) continue;
    const p = w.pk;
    p.vDn = ease(p.vDn, flowSpeed(w.rx), dt);
    p.vUp = ease(p.vUp, flowSpeed(w.tx), dt);
    p.offDn = (p.offDn + p.vDn * dt * 0.55) % 1;
    p.offUp = (p.offUp + p.vUp * dt * 0.55) % 1;
    const fade = (f) => Math.max(0, Math.min(1, f * 10, (1 - f) * 10));
    const at = (f) => w.base.getPointAtLength(Math.max(0, Math.min(1, f)) * w.len);
    const dnOn = w.rx > 0.02, upOn = w.tx > 0.02;
    p.dn.forEach((k, i) => {
      const f = (p.offDn + i / PK_DN) % 1;
      const a = at(f), b = at(f - 0.03);
      const o = dnOn ? fade(f) : 0;
      for (const c of [k.halo, k.head]) { c.setAttribute('cx', a.x.toFixed(1)); c.setAttribute('cy', a.y.toFixed(1)); }
      k.tail.setAttribute('cx', b.x.toFixed(1));
      k.tail.setAttribute('cy', b.y.toFixed(1));
      k.head.style.opacity = o;
      k.halo.style.opacity = o * 0.22;
      k.tail.style.opacity = o * 0.45;
    });
    p.up.forEach((c, i) => {
      const f = 1 - ((p.offUp + i / PK_UP) % 1);
      const a = at(f);
      c.setAttribute('cx', a.x.toFixed(1));
      c.setAttribute('cy', a.y.toFixed(1));
      c.style.opacity = upOn ? fade(f) : 0;
    });
  }
}

/* ---------- charts: download (main) and upload, each a stacked area ---------- */
const SPAN_MS = 119000;
// niceScale picks a round tick step (1, 2 or 5 x 10^n) giving about `ticks` gridlines.
function niceScale(v, ticks = 4) {
  const raw = v / ticks;
  const e = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 5, 10].map((m) => m * e).find((st) => st >= raw);
  const n = Math.max(1, Math.ceil(v / step));
  return { max: step * n, step, n };
}
const f1 = (v) => v.toFixed(1);
const pt = (p) => `${f1(p[0])},${f1(p[1])}`;

// monoSegs: monotone cubic (Fritsch-Carlson) segments through p, x increasing.
// Smooth, but never overshoots a sample, so the curve does not invent peaks.
function monoSegs(p) {
  const n = p.length;
  if (n < 2) return [];
  const dx = [], m = [], t = new Array(n);
  for (let i = 0; i < n - 1; i++) {
    dx[i] = p[i + 1][0] - p[i][0];
    m[i] = dx[i] ? (p[i + 1][1] - p[i][1]) / dx[i] : 0;
  }
  t[0] = m[0];
  t[n - 1] = m[n - 2];
  for (let i = 1; i < n - 1; i++) t[i] = m[i - 1] * m[i] <= 0 ? 0 : (m[i - 1] + m[i]) / 2;
  for (let i = 0; i < n - 1; i++) {
    if (m[i] === 0) { t[i] = 0; t[i + 1] = 0; continue; }
    const a = t[i] / m[i], b = t[i + 1] / m[i], q = a * a + b * b;
    if (q > 9) { const tau = 3 / Math.sqrt(q); t[i] = tau * a * m[i]; t[i + 1] = tau * b * m[i]; }
  }
  const out = [];
  for (let i = 0; i < n - 1; i++) {
    const d = dx[i] / 3;
    out.push([[p[i][0] + d, p[i][1] + t[i] * d], [p[i + 1][0] - d, p[i + 1][1] - t[i + 1] * d], p[i + 1], p[i]]);
  }
  return out;
}
const curve = (p) => `M${pt(p[0])}` + monoSegs(p).map(([c1, c2, e]) => `C${pt(c1)} ${pt(c2)} ${pt(e)}`).join('');
const curveBack = (p) => `L${pt(p[p.length - 1])}` + monoSegs(p).reverse().map(([c1, c2, , b]) => `C${pt(c2)} ${pt(c1)} ${pt(b)}`).join('');

const DIR_NAME = { rx: 'tải xuống', tx: 'tải lên' };
state.charts = {};
state.scaleMax = {};
state.lastT = {};

function renderCharts() {
  for (const dir of ['rx', 'tx']) renderPanel(dir);
  if (state.hoverBack != null) drawHover();
}

function renderPanel(dir) {
  const svg = $(`#chart-${dir}`);
  const wrap = svg && svg.parentElement;
  if (!wrap || wrap.clientWidth === 0 || wrap.offsetParent === null) return;
  const st = state.status;
  const W = wrap.clientWidth, H = wrap.clientHeight;
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  svg.replaceChildren();

  const running = st && st.phase === 'running';
  const links = st ? st.links || [] : [];
  const hist = running ? st.history || [] : [];
  const val = (p, l) => toUnit((p[dir] && p[dir][l.id]) || 0);
  const P = { l: 46, r: 14, t: 12, b: 24 };
  const iw = W - P.l - P.r, ih = H - P.t - P.b;
  const totals = hist.map((p) => sum(links.map((l) => val(p, l))));

  // The scale grows at once and shrinks gently, so a dip doesn't make the chart jump.
  const sk = `${dir}:${state.unit}`;
  const target = Math.max(toUnit(dir === 'rx' ? 1 : 0.5), ...totals) * 1.08;
  const prev = state.scaleMax[sk] || 0;
  const smooth = target >= prev ? target : Math.max(target, prev * 0.96);
  state.scaleMax[sk] = smooth;
  const scale = niceScale(smooth, dir === 'rx' ? 4 : 2);
  const y = (v) => P.t + ih - (Math.min(v, scale.max) / scale.max) * ih;
  const tEnd = hist.length ? hist[hist.length - 1].t : Date.now();
  const x = (t) => P.l + (1 - (tEnd - t) / SPAN_MS) * iw;

  const defs = s('defs');
  const clip = s('clipPath', { id: `clip-${dir}` });
  clip.append(s('rect', { x: P.l, y: 0, width: iw + 8, height: H - P.b + 1 }));
  defs.append(clip);
  for (const l of links) {
    const g = s('linearGradient', { id: `grad-${dir}-${l.id}`, gradientUnits: 'userSpaceOnUse', x1: 0, y1: P.t, x2: 0, y2: P.t + ih });
    g.append(s('stop', { offset: '0', style: `stop-color:${seriesVar(l.id)};stop-opacity:var(--band-top)` }),
      s('stop', { offset: '1', style: `stop-color:${seriesVar(l.id)};stop-opacity:var(--band-bottom)` }));
    defs.append(g);
  }
  svg.append(defs);

  for (let i = 0; i <= scale.n; i++) {
    const v = scale.step * i;
    const yy = Math.round(y(v)) + 0.5;
    svg.append(s('line', { class: i === 0 ? 'baseline' : 'gridline', x1: P.l, x2: W - P.r, y1: yy, y2: yy }));
    svg.append(s('text', { class: 'tick', x: P.l - 8, y: yy + 4, 'text-anchor': 'end', text: fmtTick(v) }));
  }
  const meta = wrap.previousElementSibling.lastElementChild;
  const metaText = dir === 'rx' ? `${unit().label} · 2 phút gần nhất · xếp chồng = tổng` : `${unit().label} · thang riêng`;
  if (meta.textContent !== metaText) meta.textContent = metaText;
  for (const [label, xx, anchor] of [['−2 phút', P.l, 'start'], ['−1 phút', P.l + iw / 2, 'middle'], ['bây giờ', W - P.r, 'end']]) {
    svg.append(s('text', { class: 'tick', x: xx, y: H - 6, 'text-anchor': anchor, text: label }));
  }
  if (!hist.length) {
    const msg = running ? 'Đang thu thập số liệu…' : 'Biểu đồ sẽ hiện khi kết nối đang chạy';
    svg.append(s('text', { class: 'empty', x: P.l + iw / 2, y: P.t + ih / 2 + 4, 'text-anchor': 'middle', text: msg }));
    state.charts[dir] = null;
    svg.setAttribute('aria-label', msg);
    return;
  }

  const plot = s('g', { 'clip-path': `url(#clip-${dir})` });
  const scroll = s('g', { class: 'scroll' });
  plot.append(scroll);
  svg.append(plot);
  const cum = hist.map(() => 0);
  const tops = [];
  const bands = [], lines = [];
  for (const l of links) {
    const lower = hist.map((p, i) => [x(p.t), y(cum[i])]);
    hist.forEach((p, i) => { cum[i] += val(p, l); });
    const upper = hist.map((p, i) => [x(p.t), y(cum[i])]);
    tops.push(cum.slice());
    bands.push(s('path', { class: 'band', d: `${curve(upper)}${curveBack(lower)}Z`, fill: `url(#grad-${dir}-${l.id})` }));
    lines.push(s('path', { class: 'band-line', d: curve(upper), stroke: seriesVar(l.id) }));
  }
  scroll.append(...bands, ...lines);
  // Live edge: a dot on every series, a pulsing ring on the total.
  const lastI = hist.length - 1;
  links.forEach((l, k) => scroll.append(s('circle', { class: 'edge-dot', cx: x(hist[lastI].t), cy: y(tops[k][lastI]), r: 3.5, fill: seriesVar(l.id) })));
  if (links.length) {
    const ty = y(totals[lastI]);
    scroll.append(s('circle', { class: 'edge-ring', cx: x(hist[lastI].t), cy: ty, r: 4, stroke: seriesVar(links[links.length - 1].id) }));
  }

  // Slide in the new second instead of jumping one step to the left.
  const prevT = state.lastT[dir];
  state.lastT[dir] = tEnd;
  if (prevT && tEnd > prevT && !REDUCED.matches && state.hoverBack == null) {
    const ms = Math.min(1200, tEnd - prevT);
    scroll.style.transform = `translateX(${f1(((tEnd - prevT) / SPAN_MS) * iw)}px)`;
    scroll.getBoundingClientRect(); // commit the start position
    scroll.style.transition = `transform ${ms}ms linear`;
    scroll.style.transform = 'translateX(0)';
  }

  svg.setAttribute('aria-label', `Biểu đồ ${DIR_NAME[dir]} 2 phút gần nhất, hiện tại tổng ${fmtNum(totals[lastI])} ${unit().label}`);
  state.charts[dir] = { hist, links, tops, x, y, P, W, H, val };
}

function clearHover() {
  for (const g of $$('.chart .hover')) g.remove();
  $('#tooltip').hidden = true;
}

function drawHover() {
  clearHover();
  const base = state.charts.rx || state.charts.tx;
  if (!base || state.hoverBack == null) return;
  const i = Math.max(0, base.hist.length - 1 - state.hoverBack);
  const p = base.hist[i];
  let xx = 0;
  for (const dir of ['rx', 'tx']) {
    const c = state.charts[dir];
    if (!c) continue;
    xx = Math.round(c.x(p.t)) + 0.5;
    const g = s('g', { class: 'hover' });
    g.append(s('line', { class: 'cross', x1: xx, x2: xx, y1: c.P.t, y2: c.H - c.P.b }));
    c.links.forEach((l, k) => g.append(s('circle', { class: 'dot', cx: xx, cy: c.y(c.tops[k][i]), r: 4, fill: seriesVar(l.id) })));
    $(`#chart-${dir}`).append(g);
  }

  const tip = $('#tooltip');
  const back = Math.round((base.hist[base.hist.length - 1].t - p.t) / 1000);
  const v = (dir, l) => (state.charts[dir] ? state.charts[dir].val(p, l) : 0);
  tip.replaceChildren(
    h('div', { class: 'tt-time' }, h('span', { text: back === 0 ? 'Bây giờ' : `${back} giây trước` }), h('span', { class: 'tt-h' }, icon('down')), h('span', { class: 'tt-h' }, icon('up'))));
  for (const l of base.links) {
    tip.append(h('div', { class: 'tt-row' },
      h('span', { class: 'tt-key', vars: { background: seriesVar(l.id) } }),
      h('span', { class: 'tt-name', text: l.device || l.name }),
      h('span', { class: 'tt-val', text: fmtNum(v('rx', l)) }),
      h('span', { class: 'tt-val', text: fmtNum(v('tx', l)) })));
  }
  tip.append(h('div', { class: 'tt-row tt-total' }, h('span'), h('span', { class: 'tt-name', text: `Tổng (${unit().label})` }),
    h('span', { class: 'tt-val', text: fmtNum(sum(base.links.map((l) => v('rx', l)))) }),
    h('span', { class: 'tt-val', text: fmtNum(sum(base.links.map((l) => v('tx', l)))) })));
  tip.hidden = false;
  const box = $('#charts');
  const tw = tip.offsetWidth;
  tip.style.left = `${xx + 16 + tw > box.clientWidth ? xx - 16 - tw : xx + 16}px`;
  tip.style.top = `${$('.chart-wrap.rx').offsetTop + 8}px`;
}

function initChart() {
  for (const dir of ['rx', 'tx']) {
    const svg = $(`#chart-${dir}`);
    svg.addEventListener('pointermove', (e) => {
      const c = state.charts[dir];
      if (!c) return;
      const px = e.clientX - svg.getBoundingClientRect().left;
      let best = 0, bestD = Infinity;
      c.hist.forEach((p, i) => { const d = Math.abs(c.x(p.t) - px); if (d < bestD) { bestD = d; best = i; } });
      state.hoverBack = c.hist.length - 1 - best;
      drawHover();
    });
    svg.addEventListener('pointerleave', () => { state.hoverBack = null; clearHover(); });
  }
  const main = $('#chart-rx');
  main.addEventListener('focus', () => { if (state.charts.rx) { state.hoverBack = 0; drawHover(); } });
  main.addEventListener('blur', () => { state.hoverBack = null; clearHover(); });
  main.addEventListener('keydown', (e) => {
    const c = state.charts.rx;
    if (!c || (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight')) return;
    e.preventDefault();
    const back = (state.hoverBack ?? 0) + (e.key === 'ArrowLeft' ? 1 : -1);
    state.hoverBack = Math.min(c.hist.length - 1, Math.max(0, back));
    drawHover();
  });
  new ResizeObserver(() => renderCharts()).observe($('#charts'));
  $('#table-view').addEventListener('toggle', renderTable);
  $('#adv-toggle').addEventListener('click', () => setAdvanced(!state.advanced));
  requestAnimationFrame(animate);
}

// The advanced view (quota / limits / switches on the source cards, speed
// charts, data table) is folded away so the topology stays the main view.
function setAdvanced(on) {
  state.advanced = on;
  try { localStorage.setItem('mi-adv', on ? '1' : '0'); } catch { /* storage blocked */ }
  if (state.status) renderOverview();
}

function renderTable() {
  const det = $('#table-view');
  const box = $('#chart-table');
  if (!det.open) return;
  const st = state.status;
  const hist = st && st.phase === 'running' ? st.history || [] : [];
  if (!hist.length) { box.replaceChildren(h('p', { class: 'muted-text', text: 'Chưa có số liệu.' })); return; }
  const rows = hist.slice(-10).reverse();
  const tEnd = hist[hist.length - 1].t;
  const v = (p, dir, l) => toUnit((p[dir] && p[dir][l.id]) || 0);
  const pair = (a, b) => `↓ ${fmtNum(a)} · ↑ ${fmtNum(b)}`;
  box.replaceChildren(h('table', { class: 'data' },
    h('thead', null, h('tr', null, h('th', { text: 'Thời điểm' }), st.links.map((l) => h('th', { text: `${l.device || l.name} (${unit().label})` })), h('th', { text: 'Tổng' }))),
    h('tbody', null, rows.map((p) => {
      const back = Math.round((tEnd - p.t) / 1000);
      return h('tr', null, h('td', { text: back ? `${back} giây trước` : 'Bây giờ' }),
        st.links.map((l) => h('td', { text: pair(v(p, 'rx', l), v(p, 'tx', l)) })),
        h('td', { text: pair(sum(st.links.map((l) => v(p, 'rx', l))), sum(st.links.map((l) => v(p, 'tx', l)))) }));
    }))));
}

/* =====================================================================
 * SETTINGS: one main line + up to N extension devices
 * ===================================================================== */
const ROLE_LABEL = { main: 'Chính', extension: 'Mở rộng', none: 'Không dùng' };

function formFromConfig(cfg) {
  const c = cfg || {};
  const tun = c.tun || {};
  return {
    server: c.server || '',
    key: c.key || '',
    mode: c.mode || 'aggregate',
    reorder: c.reorder_ms ?? 60,
    mtu: tun.mtu || 1360,
    dns: (tun.dns || ['1.1.1.1', '8.8.8.8']).join(', '),
    tunName: tun.name || 'mi0',
    tunAddr: tun.address || '10.66.0.2/24',
    tunGw: tun.gateway || '10.66.0.1',
    rows: (c.paths || []).map((p, i) => ({
      iface: p.interface, name: p.name, down: p.down_mbps, up: p.up_mbps,
      role: p.role || (i === 0 ? 'main' : 'extension'),
      device: p.device || '', limit: p.limit_mbps || 0, quota: p.quota_gb || 0,
      measured: p.measured_at || '', origDown: p.down_mbps, origUp: p.up_mbps,
    })),
    fresh: !cfg,
  };
}

const maxExt = () => (state.status && state.status.maxExtensions) || 5;

// mergeDevices adds every detected interface that is not configured yet; for a
// fresh config it pre-selects roles from detection (within the role limits).
function mergeDevices(form) {
  const known = new Set(form.rows.map((r) => r.iface));
  const byName = new Map(state.devices.map((d) => [d.interface, d]));
  for (const r of form.rows) {
    const d = byName.get(r.iface);
    if (d && d.name && isPhone(d)) r.device = d.name;
  }
  let hasMain = form.rows.some((r) => r.role === 'main');
  let exts = form.rows.filter((r) => r.role === 'extension').length;
  for (const d of state.devices) {
    if (known.has(d.interface)) continue;
    let role = 'none';
    if (form.fresh && d.usable && d.role === 'main' && !hasMain) { role = 'main'; hasMain = true; }
    else if (form.fresh && d.usable && d.role === 'extension' && exts < maxExt()) { role = 'extension'; exts++; }
    form.rows.push({ iface: d.interface, name: d.name || d.interface, down: '', up: '', role, device: isPhone(d) ? d.name : '', limit: 0, quota: 0 });
  }
}

function snapshot(f) { return JSON.stringify(toConfig(f)); }

// toConfig builds the config to save. It starts from the loaded config so
// fields edited elsewhere (preferences, VPS info, measurements) survive.
function toConfig(f) {
  const base = JSON.parse(JSON.stringify(state.loadedConfig || {}));
  const out = {
    ...base,
    server: f.server.trim(),
    key: f.key.trim(),
    mode: f.mode,
    reorder_ms: Number(f.reorder),
    tun: {
      ...(base.tun || {}),
      name: f.tunName,
      address: f.tunAddr.trim(),
      gateway: f.tunGw.trim(),
      mtu: Number(f.mtu),
      dns: f.dns.split(/[,\s]+/).map((x) => x.trim()).filter(Boolean),
    },
    paths: f.rows.filter((r) => r.role !== 'none').sort((a, b) => (a.role === 'main' ? -1 : 0) - (b.role === 'main' ? -1 : 0)).map((r) => {
      const p = { name: String(r.name).trim(), interface: r.iface, up_mbps: Number(r.up), down_mbps: Number(r.down), role: r.role };
      if (r.device) p.device = r.device;
      if (r.limit) p.limit_mbps = r.limit;
      if (r.quota) p.quota_gb = r.quota;
      // A hand-edited speed is no longer a measurement.
      if (r.measured && Number(r.down) === Number(r.origDown) && Number(r.up) === Number(r.origUp)) p.measured_at = r.measured;
      return p;
    }),
  };
  if (!out.server) delete out.server;
  if (!out.key) delete out.key;
  // Devices the user explicitly set to "Không dùng" are never auto-added again.
  const byName = new Map(state.devices.map((d) => [d.interface, d]));
  const used = new Set(out.paths.map((p) => p.interface));
  const ignored = new Set((base.ignored || []).filter((i) => !used.has(i)));
  for (const r of f.rows) {
    const d = byName.get(r.iface);
    if (r.role === 'none' && d && d.usable && d.role !== 'ignore') ignored.add(r.iface);
  }
  if (ignored.size) out.ignored = [...ignored]; else delete out.ignored;
  return out;
}

/* ---------- preferences (saved immediately, no "Lưu" needed) ---------- */
function fillPrefs(p) {
  state.prefs = p;
  for (const r of $$('input[name="conn"]')) {
    r.checked = r.value === p.connection;
    if (r.value === 'vps') r.disabled = !p.serverSet;
  }
  $('#vps-card-text').textContent = p.serverSet
    ? `Gộp từng gói qua máy chủ ${p.vpsHost || 'của bạn'}: mọi kết nối đều nhanh hơn. Cần quyền Admin.`
    : 'Chưa có VPS: bấm “Cài / nâng cấp VPS…” bên dưới. Gộp từng gói qua máy chủ, mọi kết nối đều nhanh hơn.';
  $('#p-autoconnect').checked = p.autoConnect;
  $('#p-startwin').checked = p.startWithWindows;
  $('#p-autoadd').checked = p.autoAddDevices;
  $('#p-sticky').checked = p.sticky;
  $('#p-sticky').disabled = p.connection !== 'local';
}

async function savePref(patch) {
  try {
    const res = await api('PUT', '/api/prefs', patch);
    fillPrefs(res.prefs);
    if (res.restartNeeded) toast('Đã đổi chế độ. Khởi động lại kết nối để áp dụng.', { action: { label: 'Khởi động lại', run: restartTunnel } });
    else toast('Đã lưu.');
    refreshNow();
  } catch (e) {
    toast(e.message, { error: true });
    loadPrefs();
  }
}

async function loadPrefs() {
  try { fillPrefs(await api('GET', '/api/prefs')); } catch (e) { toast(e.message, { error: true }); }
}

function initPrefs() {
  for (const r of $$('input[name="conn"]')) r.addEventListener('change', () => savePref({ connection: r.value }));
  $('#p-autoconnect').addEventListener('change', (e) => savePref({ autoConnect: e.target.checked }));
  $('#p-startwin').addEventListener('change', (e) => savePref({ startWithWindows: e.target.checked }));
  $('#p-autoadd').addEventListener('change', (e) => savePref({ autoAddDevices: e.target.checked }));
  $('#p-sticky').addEventListener('change', (e) => savePref({ sticky: e.target.checked }));
  $('#open-vps-setup').addEventListener('click', () => {
    const d = $('#setup-vps');
    d.open = true;
    d.scrollIntoView({ behavior: 'smooth', block: 'center' });
    $('#v-host').focus();
  });
  $('#measure-btn').addEventListener('click', async () => {
    try {
      await api('POST', '/api/measure', {});
      toast('Đang đo tốc độ, xem tiến trình ở tab Tổng quan.');
      state.settingsLoaded = false; // speeds will change
      switchTab('overview');
      refreshNow();
    } catch (e) { toast(e.message, { error: true }); }
  });
}

async function loadSettings() {
  loadPrefs();
  try {
    const [cfg, devices] = await Promise.all([api('GET', '/api/config'), api('GET', '/api/devices')]);
    state.devices = devices;
    state.loadedConfig = cfg.config;
    if (cfg.error) toast(cfg.error, { error: true });
    state.form = formFromConfig(cfg.config);
    mergeDevices(state.form);
    state.savedSnapshot = cfg.exists ? snapshot(formFromConfig(cfg.config)) : '';
    state.settingsLoaded = true;
    fillForm();
  } catch (e) {
    toast(e.message, { error: true });
  }
}

function fillForm() {
  const f = state.form;
  $('#f-server').value = f.server;
  $('#f-key').value = f.key;
  for (const r of $$('input[name="mode"]')) r.checked = r.value === f.mode;
  $('#f-reorder').value = f.reorder;
  $('#v-reorder').textContent = Number(f.reorder) === 0 ? 'tắt' : `${f.reorder} ms`;
  $('#f-mtu').value = f.mtu;
  $('#f-dns').value = f.dns;
  $('#f-tunaddr').value = f.tunAddr;
  $('#f-tungw').value = f.tunGw;
  clearErrors();
  renderRows();
  updateDirty();
}

function renderRoleSummary() {
  const rows = state.form.rows;
  const main = rows.find((r) => r.role === 'main');
  const exts = rows.filter((r) => r.role === 'extension');
  $('#role-summary').replaceChildren(
    h('span', { class: `pill${main ? '' : ' full'}` }, icon('monitor'), main ? `Đường chính: ${main.name || main.iface}` : 'Chưa chọn đường chính'),
    h('span', { class: `pill${exts.length >= maxExt() ? ' full' : ''}` }, icon('phone'), `Thiết bị mở rộng: ${exts.length}/${maxExt()}`));
}

function renderRows() {
  const f = state.form;
  const byName = new Map(state.devices.map((d) => [d.interface, d]));
  const rank = (r) => (r.role === 'main' ? 0 : r.role === 'extension' ? 1 : byName.get(r.iface)?.usable ? 2 : 3);
  const visible = f.rows
    .map((r, idx) => ({ r, idx, d: byName.get(r.iface) }))
    .filter(({ r, d }) => state.showAllIfs || r.role !== 'none' || (d && d.usable && d.role !== 'ignore'))
    .sort((a, b) => rank(a.r) - rank(b.r) || a.idx - b.idx);
  renderRoleSummary();
  const list = $('#iface-list');
  if (!visible.length) {
    list.replaceChildren(h('p', { class: 'muted-text', text: 'Không thấy card mạng nào có internet. Cắm điện thoại / bật chia sẻ Internet rồi bấm “Quét lại”.' }));
    return;
  }
  list.replaceChildren(...visible.map(({ r, d }) => ifaceRow(r, d)));
}

function setRole(r, role) {
  const rows = state.form.rows;
  if (role === 'main') {
    for (const o of rows) if (o !== r && o.role === 'main') o.role = 'none'; // only one main line
  }
  if (role === 'extension' && r.role !== 'extension' && rows.filter((o) => o.role === 'extension').length >= maxExt()) {
    toast(`Chỉ hỗ trợ tối đa ${maxExt()} thiết bị mở rộng. Chuyển một thiết bị khác sang “Không dùng” trước.`, { error: true });
    return;
  }
  r.role = role;
  renderRows();
  updateDirty();
  if (role !== 'none' && !(Number(r.down) > 0)) r.inputs?.dn.focus();
}

function ifaceRow(r, d) {
  const k = KIND[d ? d.kind : 'ethernet'] || KIND.ethernet;
  let detail;
  if (!d) detail = h('span', { class: 'bad', text: 'Không tìm thấy (chưa cắm / chưa kết nối?)' });
  else if (!d.usable) detail = h('span', null, `${k.label} · `, h('span', { class: 'bad', text: d.up ? 'chưa có IP' : 'đang tắt' }));
  else {
    const who = isPhone(d) ? `${d.name} · ${VIA[d.via] || ''}` : d.via === 'wifi' ? `Wi-Fi “${d.ssid || d.name}”` : (VIA[d.via] || k.label);
    detail = h('span', { text: `${who} · ${(d.ipv4 || []).join(', ')}`, title: d.description || '' });
  }
  const row = h('div', { class: `iface-row${r.role !== 'none' ? ' on' : ''}${d ? '' : ' missing'}` });
  const exts = state.form.rows.filter((o) => o.role === 'extension').length;
  const seg = h('div', { class: 'role-seg', role: 'radiogroup', 'aria-label': `Vai trò của ${r.iface}` },
    ...['main', 'extension', 'none'].map((role) => h('button', {
      type: 'button', role: 'radio', 'aria-checked': String(r.role === role),
      disabled: role === 'extension' && r.role !== 'extension' && exts >= maxExt(),
      title: role === 'extension' && r.role !== 'extension' && exts >= maxExt() ? `Đã đủ ${maxExt()} thiết bị mở rộng` : null,
      onclick: () => setRole(r, role),
    }, ROLE_LABEL[role])));
  const name = h('input', { type: 'text', value: r.name, 'aria-label': `Tên hiển thị cho ${r.iface}`, maxlength: '40' });
  const dn = h('input', { type: 'number', value: r.down, min: '0.1', step: 'any', placeholder: 'vd 100', 'aria-label': `Tốc độ tải xuống của ${r.iface} (Mbps)` });
  const up = h('input', { type: 'number', value: r.up, min: '0.1', step: 'any', placeholder: 'vd 40', 'aria-label': `Tốc độ tải lên của ${r.iface} (Mbps)` });
  name.addEventListener('input', () => { r.name = name.value; renderRoleSummary(); updateDirty(); });
  dn.addEventListener('input', () => { r.down = dn.value; dn.classList.remove('invalid'); updateDirty(); });
  up.addEventListener('input', () => { r.up = up.value; up.classList.remove('invalid'); updateDirty(); });
  r.inputs = { name, dn, up };
  row.append(
    h('div', { class: 'iface-info' },
      h('span', { class: 'ic' }, icon(d ? nodeIcon(d) : 'ethernet')),
      h('div', { class: 'txt' }, h('strong', { text: r.iface }), detail)),
    h('div', { class: 'role' }, h('span', { class: 'mobile-label', text: 'Vai trò' }), seg),
    h('div', { class: 'name-input' }, h('span', { class: 'mobile-label', text: 'Tên hiển thị' }), name),
    h('div', { class: 'dn' }, h('span', { class: 'mobile-label', text: 'Tải xuống' }), h('span', { class: 'unit-input' }, dn)),
    h('div', { class: 'up' }, h('span', { class: 'mobile-label', text: 'Tải lên' }), h('span', { class: 'unit-input' }, up)));
  return row;
}

function readForm() {
  const f = state.form;
  f.server = $('#f-server').value;
  f.key = $('#f-key').value;
  f.mode = ($('input[name="mode"]:checked') || {}).value || 'aggregate';
  f.reorder = $('#f-reorder').value;
  f.mtu = $('#f-mtu').value;
  f.dns = $('#f-dns').value;
  f.tunAddr = $('#f-tunaddr').value;
  f.tunGw = $('#f-tungw').value;
}

function updateDirty() {
  if (!state.form) return;
  readForm();
  const dirty = snapshot(state.form) !== state.savedSnapshot;
  const el = $('#save-state');
  el.textContent = dirty ? (state.savedSnapshot ? 'Có thay đổi chưa lưu' : 'Chưa lưu cấu hình') : 'Đã lưu';
  el.classList.toggle('dirty', dirty);
  $('#save-btn').disabled = !dirty;
  $('#reset-btn').disabled = !dirty || !state.savedSnapshot;
}

function clearErrors() {
  for (const e of $$('.field-error')) e.textContent = '';
  for (const i of $$('input.invalid')) i.classList.remove('invalid');
}

function validIPv4(a) {
  const m = a.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
  return !!m && m.slice(1).every((o) => Number(o) <= 255);
}

function validate() {
  clearErrors();
  const f = state.form;
  let first = null;
  let ok = true;
  const fail = (input, errId, msg) => {
    ok = false;
    if (input) input.classList.add('invalid');
    if (errId) $('#' + errId).textContent = msg;
    first = first || input;
  };
  // The VPS fields are only required in VPS mode, but must be valid if filled.
  const needServer = state.prefs && state.prefs.connection === 'vps';
  if (needServer || f.server.trim() || f.key.trim()) {
    const [host, port] = f.server.trim().split(':');
    if (!validIPv4(host || '') || !(Number(port) >= 1 && Number(port) <= 65535)) {
      fail($('#f-server'), 'e-server', 'Nhập dạng IP:cổng, ví dụ 203.0.113.10:51820');
    }
    let keyOk = false;
    try { keyOk = atob(f.key.trim()).length === 32; } catch { /* not base64 */ }
    if (!keyOk) fail($('#f-key'), 'e-key', 'Key là chuỗi 44 ký tự in ra bởi server-setup.sh trên VPS.');
  }

  const used = f.rows.filter((r) => r.role !== 'none');
  if (!used.length) fail(null, 'e-paths', 'Chọn ít nhất một đường mạng.');
  else if (!used.some((r) => r.role === 'main')) fail(null, 'e-paths', 'Chọn 1 đường chính của PC (LAN hoặc Wi-Fi nhà).');
  for (const r of used) {
    const bad = [[!String(r.name).trim(), r.inputs?.name], [!(Number(r.down) > 0), r.inputs?.dn], [!(Number(r.up) > 0), r.inputs?.up]];
    for (const [isBad, input] of bad) if (isBad) fail(input, 'e-paths', 'Điền tên và tốc độ (Mbps) cho mọi đường đã chọn.');
  }
  const mtu = Number(f.mtu);
  if (!(mtu >= 1200 && mtu <= 1500)) fail($('#f-mtu'), 'e-mtu', 'MTU từ 1200 đến 1500.');
  const dns = f.dns.split(/[,\s]+/).filter(Boolean);
  if (dns.some((d) => !validIPv4(d))) fail($('#f-dns'), 'e-dns', 'Nhập địa chỉ IPv4, cách nhau bởi dấu phẩy.');
  if (!ok && $('#e-server').textContent) $('#server-section').open = true;
  if (first) {
    if (first.closest('details')) first.closest('details').open = true;
    first.focus();
  }
  return ok;
}

async function saveSettings(e) {
  e.preventDefault();
  readForm();
  if (!validate()) { toast('Còn vài mục cần sửa.', { error: true }); return; }
  const cfg = toConfig(state.form);
  try {
    const res = await api('PUT', '/api/config', cfg);
    state.loadedConfig = cfg;
    state.form.fresh = false;
    state.savedSnapshot = snapshot(state.form);
    updateDirty();
    if (cfg.paths.length === 1) toast('Đã lưu. Lưu ý: chỉ có 1 đường nên chưa gộp được tốc độ.');
    else if (res.restartNeeded) toast('Đã lưu. Khởi động lại kết nối để áp dụng.', { action: { label: 'Khởi động lại', run: restartTunnel } });
    else toast('Đã lưu cấu hình.');
    refreshNow();
  } catch (err) {
    toast(err.message, { error: true });
  }
}

async function restartTunnel() {
  try {
    await api('POST', '/api/stop');
    await api('POST', '/api/start');
    switchTab('overview');
  } catch (e) {
    toast(e.message, { error: true });
  }
}

async function rescan() {
  const btn = $('#refresh-ifs');
  btn.disabled = true;
  try {
    state.devices = await api('POST', '/api/devices/refresh');
    mergeDevices(state.form);
    renderRows();
    const n = state.devices.filter((d) => d.usable && d.role !== 'ignore').length;
    toast(`Tìm thấy ${n} nguồn mạng có internet.`);
  } catch (e) {
    toast(e.message, { error: true });
  } finally {
    btn.disabled = false;
  }
}

function initSettings() {
  const form = $('#settings-form');
  form.addEventListener('submit', saveSettings);
  form.addEventListener('input', (e) => {
    if (e.target.id === 'f-reorder') $('#v-reorder').textContent = Number(e.target.value) === 0 ? 'tắt' : `${e.target.value} ms`;
    if (e.target.classList.contains('invalid') && e.target.id) {
      e.target.classList.remove('invalid');
      const err = $('#e-' + e.target.id.replace('f-', ''));
      if (err) err.textContent = '';
    }
    updateDirty();
  });
  form.addEventListener('change', updateDirty);
  $('#reset-btn').addEventListener('click', () => {
    state.form = formFromConfig(state.loadedConfig);
    mergeDevices(state.form);
    fillForm();
  });
  $('#refresh-ifs').addEventListener('click', rescan);
  $('#show-all-ifs').addEventListener('change', (e) => { state.showAllIfs = e.target.checked; renderRows(); });
  const keyBtn = $('#key-toggle');
  const setKeyIcon = () => {
    const shown = $('#f-key').type === 'text';
    keyBtn.replaceChildren(icon(shown ? 'eyeOff' : 'eye'));
    keyBtn.setAttribute('aria-label', shown ? 'Ẩn key' : 'Hiện key');
  };
  keyBtn.addEventListener('click', () => { const k = $('#f-key'); k.type = k.type === 'password' ? 'text' : 'password'; setKeyIcon(); });
  setKeyIcon();
}

/* =====================================================================
 * LOGS
 * ===================================================================== */
let logTimer = 0;
async function pollLogs() {
  clearTimeout(logTimer);
  if (state.tab !== 'logs') return;
  try {
    const lines = await api('GET', `/api/logs?since=${state.logSeq}`);
    if (lines.length) {
      state.logSeq = lines[lines.length - 1].seq;
      state.logLines.push(...lines.map((l) => l.text));
      if (state.logLines.length > 1000) state.logLines.splice(0, state.logLines.length - 1000);
      appendLogs(lines);
    } else if (!state.logLines.length) {
      $('#log-box').replaceChildren(h('div', { class: 'log-empty', text: 'Chưa có sự kiện nào.' }));
    }
  } catch { /* offline banner covers this */ }
  logTimer = setTimeout(pollLogs, 1500);
}

function appendLogs(lines) {
  const box = $('#log-box');
  $('.log-empty', box)?.remove();
  for (const l of lines) {
    const cls = /(error|fail|lỗi|cannot|no reply|retrying|DOWN|thiếu|quota reached)/i.test(l.text) ? 'err'
      : /(tunnel up|bound to|đã lưu|đã thêm|new client session|added while running)/i.test(l.text) ? 'ok' : '';
    box.append(h('div', { class: `log-line ${cls}`, text: l.text }));
  }
  while (box.childElementCount > 1000) box.firstElementChild.remove();
  if ($('#log-follow').checked) box.scrollTop = box.scrollHeight;
}

function initLogs() {
  $('#log-copy').addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(state.logLines.join('\n'));
      toast('Đã sao chép nhật ký.');
    } catch {
      toast('Trình duyệt không cho sao chép. Hãy bôi đen và nhấn Ctrl+C.', { error: true });
    }
  });
}

/* =====================================================================
 * DEMO MODE: preview without admin rights or a VPS.
 *   ?demo / ?demo=case3   PC Wi-Fi + 2 phones over USB
 *   ?demo=case1           PC LAN + phone over Wi-Fi hotspot + phone over USB
 *   ?demo=case2           PC LAN + 2 phones over USB
 *   ?demo=candidate       a phone just plugged in, not added yet
 *   ?demo=fresh | stopped | noadmin
 * ===================================================================== */
const DEMO_MAX_EXT = 5; // mirrors config.MaxExtensions
const demo = (() => {
  if (DEMO === null) return null;
  const variant = DEMO || 'case3';
  const now = Date.now();
  const DEV = {
    wifi: { interface: 'Wi-Fi', index: 23, description: 'Intel(R) Wi-Fi 6E AX210 160MHz', kind: 'wifi', via: 'wifi', role: 'main', name: 'Minhhoangg_5G', vendor: '', ssid: 'Minhhoangg_5G', gateway: '192.168.1.1', ipv4: ['192.168.1.175'], up: true, usable: true },
    lan: { interface: 'Ethernet', index: 9, description: 'Realtek PCIe GbE Family Controller', kind: 'ethernet', via: 'lan', role: 'main', name: 'Mạng LAN', vendor: '', ssid: '', gateway: '192.168.1.1', ipv4: ['192.168.1.20'], up: true, usable: true },
    iphoneUsb: { interface: 'Ethernet 8', index: 31, description: 'Apple Mobile Device Ethernet', kind: 'iphone', via: 'usb', role: 'extension', name: 'iPhone 18 Pro Max', vendor: 'Apple', ssid: '', gateway: '172.20.10.1', ipv4: ['172.20.10.2'], up: true, usable: true },
    iphoneHot: { interface: 'Wi-Fi', index: 23, description: 'Intel(R) Wi-Fi 6E AX210 160MHz', kind: 'wifi', via: 'hotspot', role: 'extension', name: 'iPhone 18 Pro Max', vendor: 'Apple', ssid: 'iPhone 18 Pro Max', gateway: '172.20.10.1', ipv4: ['172.20.10.3'], up: true, usable: true },
    wifiExtra: { interface: 'Wi-Fi', index: 23, description: 'Intel(R) Wi-Fi 6E AX210 160MHz', kind: 'wifi', via: 'wifi', role: 'extension', name: 'Viettel_5G', vendor: '', ssid: 'Viettel_5G', gateway: '192.168.0.1', ipv4: ['192.168.0.23'], up: true, usable: true, speed: [300, 90] },
    wifiSame: { interface: 'Wi-Fi', index: 23, description: 'Intel(R) Wi-Fi 6E AX210 160MHz', kind: 'wifi', via: 'wifi', role: 'extension', name: 'Minhhoangg_5G', vendor: '', ssid: 'Minhhoangg_5G', gateway: '192.168.1.1', ipv4: ['192.168.1.175'], up: true, usable: true, speed: [380, 160], sameNet: true },
    galaxy: { interface: 'Ethernet 9', index: 32, description: 'SAMSUNG Mobile USB Remote NDIS Network Device', kind: 'android', via: 'usb', role: 'extension', name: 'Galaxy S25 Ultra', vendor: 'Samsung', ssid: '', gateway: '192.168.42.129', ipv4: ['192.168.42.20'], up: true, usable: true },
    xiaomi: { interface: 'Ethernet 10', index: 33, description: 'Remote NDIS based Internet Sharing Device #2', kind: 'android', via: 'usb', role: 'extension', name: 'Xiaomi 15', vendor: 'Xiaomi', ssid: '', gateway: '192.168.43.1', ipv4: ['192.168.43.12'], up: true, usable: true },
    pixel: { interface: 'Ethernet 11', index: 34, description: 'Remote NDIS based Internet Sharing Device #3', kind: 'android', via: 'usb', role: 'extension', name: 'Pixel 9 Pro', vendor: 'Google', ssid: '', gateway: '10.42.0.1', ipv4: ['10.42.0.33'], up: true, usable: true },
    vivo: { interface: 'Ethernet 7', index: 30, description: 'Remote NDIS based Internet Sharing Device', kind: 'android', via: 'usb', role: 'extension', name: 'vivo X300 Pro', vendor: 'vivo', ssid: '', gateway: '10.179.105.67', ipv4: ['10.179.105.14'], up: true, usable: true },
  };
  const scen = {
    case1: { devs: [DEV.lan, DEV.iphoneHot, DEV.vivo], cfg: [DEV.lan, DEV.iphoneHot, DEV.vivo] },
    case2: { devs: [DEV.lan, DEV.iphoneUsb, DEV.vivo], cfg: [DEV.lan, DEV.iphoneUsb, DEV.vivo] },
    case3: { devs: [DEV.wifi, DEV.iphoneUsb, DEV.vivo], cfg: [DEV.wifi, DEV.iphoneUsb, DEV.vivo] },
    candidate: { devs: [DEV.wifi, DEV.iphoneUsb, DEV.vivo], cfg: [DEV.wifi, DEV.vivo] },
    fresh: { devs: [DEV.wifi, DEV.vivo], cfg: null },
    stopped: { devs: [DEV.wifi, DEV.iphoneUsb, DEV.vivo], cfg: [DEV.wifi, DEV.iphoneUsb, DEV.vivo] },
    noadmin: { devs: [DEV.wifi, DEV.vivo], cfg: [DEV.wifi, DEV.vivo] },
    max: { devs: [DEV.lan, DEV.iphoneHot, DEV.vivo, DEV.galaxy, DEV.xiaomi, DEV.pixel], cfg: [DEV.lan, DEV.iphoneHot, DEV.vivo, DEV.galaxy, DEV.xiaomi, DEV.pixel] },
    lanwifi: { devs: [DEV.lan, DEV.wifiExtra, DEV.vivo, DEV.galaxy, DEV.xiaomi], cfg: [DEV.lan, DEV.wifiExtra, DEV.vivo, DEV.galaxy, DEV.xiaomi] },
    samenet: { devs: [DEV.lan, DEV.wifiSame, DEV.vivo], cfg: [DEV.lan, DEV.wifiSame, DEV.vivo] },
    away: { devs: [DEV.wifi, DEV.vivo], cfg: [DEV.wifi, DEV.iphoneUsb, DEV.vivo] }, // iPhone saved but unplugged
  }[variant] || { devs: [DEV.wifi, DEV.iphoneUsb, DEV.vivo], cfg: [DEV.wifi, DEV.iphoneUsb, DEV.vivo] };
  // Speeds from the user's sketch: Wi-Fi 90 MB/s, iPhone 30 MB/s, vivo 15 MB/s.
  const speed = { 'Wi-Fi': [720, 160], Ethernet: [760, 180], 'Ethernet 8': [240, 40], 'Ethernet 7': [120, 24], 'Ethernet 9': [180, 30], 'Ethernet 10': [95, 18], 'Ethernet 11': [140, 26] };
  const pathOf = (dv) => ({ name: dv.role === 'main' ? (dv.via === 'wifi' ? 'Wi-Fi nhà' : 'LAN nhà') : dv.name, interface: dv.interface, role: dv.role,
    device: dv.role === 'main' ? '' : dv.name, down_mbps: (dv.speed || speed[dv.interface] || [200])[0], up_mbps: (dv.speed || speed[dv.interface] || [0, 40])[1] });
  const d = {
    phase: ['stopped', 'fresh', 'noadmin'].includes(variant) ? 'stopped' : 'running',
    startedAt: now - 754000,
    elevated: variant !== 'noadmin',
    devices: scen.devs,
    config: scen.cfg && {
      server: '203.0.113.10:51820', key: 'mGq3oR1s0Ff6Yb9w2kZ8cX4vN7tL5hP1aD3eQ0uJ6iM=', mode: 'aggregate', reorder_ms: 60,
      tun: { name: 'mi0', address: '10.66.0.2/24', gateway: '10.66.0.1', mtu: 1360, dns: ['1.1.1.1', '8.8.8.8'] },
      paths: scen.cfg.map(pathOf),
    },
    enabled: {}, capped: {}, usage: {}, bytes: {}, hist: [], logs: [], seq: 0,
    connection: variant === 'vps' ? 'vps' : 'local',
    serverSet: variant === 'vps',
    prefs: { autoConnect: false, autoAddDevices: true, sticky: false, startWithWindows: false },
    job: null,
  };
  if (d.config) {
    // Make the phones show limits and quotas.
    for (const p of d.config.paths) {
      if (p.interface === 'Ethernet 8' || (p.interface === 'Wi-Fi' && p.role === 'extension')) { p.quota_gb = 5; p.limit_mbps = 0; }
      if (p.interface === 'Ethernet 7') { p.limit_mbps = 160; p.quota_gb = 0; }
    }
  }
  const usageStart = { 'Ethernet 8': 3.6e9, 'Ethernet 7': 1.1e9, 'Wi-Fi': 0, Ethernet: 0 };
  const paths = () => (d.config ? d.config.paths : []);
  paths().forEach((p, k) => { d.enabled[k] = true; d.usage[k] = p.role === 'main' ? 0 : (p.interface === 'Wi-Fi' ? 3.6e9 : usageStart[p.interface] || 0); d.bytes[k] = [0, 0]; });
  const rate = (k, t, j) => {
    const p = paths()[k];
    const base = [p.down_mbps || 100, p.up_mbps || 20][j];
    const w = Math.sin(t / 9000 + k * 2) * 0.1 + Math.sin(t / 2300 + k) * 0.05 + (Math.random() - 0.5) * 0.06;
    return Math.max(0, base * (1 + w));
  };
  const sample = (t) => {
    const n = paths().length;
    const rx = [], tx = [];
    for (let k = 0; k < n; k++) {
      const on = d.enabled[k] && !d.capped[k];
      rx[k] = on ? +rate(k, t, 0).toFixed(2) : 0;
      tx[k] = on ? +rate(k, t, 1).toFixed(2) : 0;
    }
    return { t, rx, tx };
  };
  for (let i = 119; i >= 0; i--) d.hist.push(sample(now - i * 1000));
  const log = (text) => { d.logs.push({ seq: ++d.seq, text: `${new Date().toLocaleTimeString('vi-VN', { hour12: false })} ${text}` }); };
  log('dashboard: http://127.0.0.1:7878/');
  if (d.phase === 'running') {
    for (const p of paths()) log(`path ${p.name}: bound to "${p.interface}"`);
    log(`tunnel up: ${paths().length} paths -> 203.0.113.10:51820, mode=aggregate`);
  }
  let delivered = 120000, lost = 30;
  setInterval(() => {
    if (d.phase !== 'running') return;
    const smp = sample(Date.now());
    d.hist.push(smp);
    if (d.hist.length > 120) d.hist.shift();
    smp.rx.forEach((v, k) => {
      const b = (v + smp.tx[k]) * 125000;
      d.bytes[k][0] += v * 125000; d.bytes[k][1] += smp.tx[k] * 125000;
      d.usage[k] += b;
      const q = (paths()[k].quota_gb || 0) * 1e9;
      if (q > 0 && d.usage[k] >= q) d.capped[k] = true;
    });
    delivered += Math.round(sum(smp.rx) * 30); lost += Math.random() < 0.3 ? 1 : 0;
  }, 1000);
  d.reorder = () => ({ delivered, lost, late: 2 });
  return d;
})();

async function demoApi(method, path, body) {
  await new Promise((r) => setTimeout(r, 60));
  const d = demo;
  const route = `${method} ${path.split('?')[0]}`;
  const paths = () => (d.config ? d.config.paths : []);
  const devBy = new Map(d.devices.map((x) => [x.interface, x]));
  switch (route) {
    case 'GET /api/status': {
      const running = d.phase === 'running';
      const last = d.hist[d.hist.length - 1];
      const used = new Set(paths().map((p) => p.interface));
      return {
        connection: d.connection, serverSet: d.serverSet, job: d.job,
        phase: d.phase, error: '', elevated: d.elevated, wintun: true, configured: !!d.config, configPath: 'C:\\mergeinternet\\client.json',
        startedAt: running ? d.startedAt : 0, server: d.config ? d.config.server : '', mode: d.config ? d.config.mode : '', maxExtensions: DEMO_MAX_EXT,
        links: paths().map((p, k) => {
          const dv = devBy.get(p.interface) || {};
          return {
            id: k, name: p.name, device: p.role === 'main' ? (dv.via === 'wifi' ? dv.ssid : 'Mạng LAN') : (dv.name || p.device || p.name),
            iface: p.interface, kind: dv.kind || '', via: dv.via || (p.role === 'main' ? 'lan' : ''), role: p.role, present: !!dv.usable,
            alive: running && d.enabled[k] && !d.capped[k], enabled: d.enabled[k] && !d.capped[k], capped: !!d.capped[k],
            rttMs: [24, 47, 61][k] || 50, rxMbps: running ? last.rx[k] || 0 : 0, txMbps: running ? last.tx[k] || 0 : 0,
            rxBytes: running ? d.bytes[k][0] : 0, txBytes: running ? d.bytes[k][1] : 0,
            upMbps: p.up_mbps, downMbps: p.down_mbps, limitMbps: p.limit_mbps || 0, quotaGB: p.quota_gb || 0,
            usageBytes: d.usage[k] || 0, usageSince: Date.now() - 12 * 86400000,
            sameNetAs: dv.sameNet ? 'Mạng LAN' : '',
          };
        }),
        candidates: d.devices.filter((x) => x.usable && x.role !== 'ignore' && !used.has(x.interface)),
        reorder: running ? d.reorder() : { delivered: 0, lost: 0, late: 0 },
        dropped: 0,
        history: running ? d.hist : [],
      };
    }
    case 'POST /api/setup/local': {
      // Simulated one-click setup: detect -> measure each path -> connect.
      const devs = d.devices.filter((x) => x.usable && x.role !== 'ignore').slice(0, 3);
      d.job = { kind: 'local', running: true, steps: [{ id: 'devices', title: 'Tìm các nguồn mạng đang có', status: 'running', detail: '' }] };
      const step = (id, title, status, detail) => {
        const s = d.job.steps.find((x) => x.id === id);
        if (s) Object.assign(s, { status, detail }); else d.job.steps.push({ id, title, status, detail });
      };
      setTimeout(() => {
        step('devices', '', 'done', `${devs[0].name} + ${devs.length - 1} thiết bị mở rộng`);
        step('measure', 'Đo tốc độ từng đường', 'running', 'Mất khoảng 10 giây…');
        devs.forEach((x, k) => step(`m${k}`, `Đo ${x.name}`, 'running', ''));
      }, 1200);
      setTimeout(() => {
        devs.forEach((x, k) => step(`m${k}`, '', 'done', `↓ ${[812, 195, 140][k]} Mbps · ↑ ${[790, 10, 22][k]} Mbps`));
        step('measure', '', 'done', `Đã đo ${devs.length}/${devs.length} đường`);
        step('start', 'Bật kết nối', 'running', '');
        d.config = d.config || { mode: 'aggregate', connection: 'local', paths: devs.map(pathOf) };
        paths().forEach((_, k) => { d.enabled[k] = true; d.usage[k] = d.usage[k] || 0; d.bytes[k] = d.bytes[k] || [0, 0]; });
      }, 4200);
      setTimeout(() => {
        step('start', '', 'done', '');
        d.phase = 'running'; d.startedAt = Date.now();
        Object.assign(d.job, { running: false, message: 'Xong! Các nguồn mạng đang được gộp.', ended: Date.now() });
      }, 5200);
      return { started: true };
    }
    case 'POST /api/measure':
      d.job = { kind: 'measure', running: true, steps: [{ id: 'measure', title: 'Đo tốc độ từng đường', status: 'running', detail: '' }] };
      setTimeout(() => Object.assign(d.job, { running: false, message: 'Đã cập nhật tốc độ các đường.', ended: Date.now(),
        steps: [{ id: 'measure', title: 'Đo tốc độ từng đường', status: 'done', detail: `Đã đo ${paths().length}/${paths().length} đường` }] }), 2500);
      return { started: true };
    case 'POST /api/setup/vps':
    case 'POST /api/setup/code':
      throw new ApiError('Bản demo không cài được VPS thật.');
    case 'GET /api/prefs':
      return { connection: d.connection, serverSet: d.serverSet, vpsHost: d.serverSet ? '203.0.113.10' : '', ...d.prefs };
    case 'PUT /api/prefs':
      if (body.connection) d.connection = body.connection;
      for (const k of ['autoConnect', 'autoAddDevices', 'sticky', 'startWithWindows']) if (body[k] !== undefined) d.prefs[k] = body[k];
      return { prefs: { connection: d.connection, serverSet: d.serverSet, ...d.prefs }, restartNeeded: false };
    case 'POST /api/start':
      d.phase = 'starting';
      setTimeout(() => { d.phase = 'running'; d.startedAt = Date.now(); d.logs.push({ seq: ++d.seq, text: 'tunnel up (demo)' }); }, 1500);
      return { phase: 'starting' };
    case 'POST /api/stop':
      d.phase = 'stopping';
      setTimeout(() => { d.phase = 'stopped'; }, 800);
      return { phase: 'stopped' };
    case 'GET /api/config':
      return { exists: !!d.config, config: d.config, path: 'C:\\mergeinternet\\client.json' };
    case 'PUT /api/config':
      d.config = body;
      body.paths.forEach((_, k) => { if (d.enabled[k] === undefined) { d.enabled[k] = true; d.usage[k] = 0; d.bytes[k] = [0, 0]; } });
      return { saved: true, restartNeeded: d.phase === 'running' };
    case 'GET /api/devices':
    case 'POST /api/devices/refresh':
      return d.devices;
    case 'POST /api/paths': {
      if (!d.config) throw new ApiError('Hãy nhập và lưu thông tin máy chủ VPS trong Cài đặt trước.');
      if (body.role === 'extension' && paths().filter((p) => p.role === 'extension').length >= DEMO_MAX_EXT) throw new ApiError(`Chỉ hỗ trợ tối đa ${DEMO_MAX_EXT} thiết bị mở rộng. Bỏ bớt một thiết bị trước.`);
      const k = paths().length;
      paths().push(body);
      d.enabled[k] = true; d.usage[k] = 0; d.bytes[k] = [0, 0];
      return { saved: true, live: d.phase === 'running', id: k };
    }
    case 'GET /api/logs': {
      const since = Number(new URLSearchParams(path.split('?')[1]).get('since') || 0);
      return d.logs.filter((l) => l.seq > since);
    }
    case 'POST /api/elevate':
      d.elevated = true;
      return { relaunched: true };
    default: {
      let m = route.match(/^POST \/api\/links\/(\d+)$/);
      if (m) {
        const k = Number(m[1]);
        if (body.enabled !== undefined) {
          if (d.phase !== 'running') throw new ApiError('Kết nối chưa chạy.');
          if (body.enabled && d.capped[k]) throw new ApiError('đường này đã dùng hết dung lượng cho phép; tăng hạn mức hoặc đặt lại số liệu trước');
          d.enabled[k] = body.enabled;
        }
        if (body.limitMbps !== undefined) paths()[k].limit_mbps = body.limitMbps;
        if (body.quotaGB !== undefined) {
          paths()[k].quota_gb = body.quotaGB;
          d.capped[k] = body.quotaGB > 0 && d.usage[k] >= body.quotaGB * 1e9;
        }
        return { ok: true };
      }
      m = route.match(/^POST \/api\/links\/(\d+)\/reset-usage$/);
      if (m) { d.usage[Number(m[1])] = 0; d.capped[Number(m[1])] = false; return { ok: true }; }
      throw new ApiError('Không có trong bản demo.');
    }
  }
}

/* ---------- boot ---------- */
function init() {
  for (const el of $$('[data-icon]')) el.prepend(icon(el.dataset.icon));
  initTheme();
  initTabs();
  initTopology();
  initChart();
  initSettings();
  initPrefs();
  initLogs();
  window.addEventListener('topo3d-ready', () => requestAnimationFrame(drawWires));
  $('#power-btn').addEventListener('click', onPower);
  $('#setup-local').addEventListener('click', setupLocal);
  $('#vps-form').addEventListener('submit', setupVPS);
  $('#code-form').addEventListener('submit', setupCode);
  const tab = new URLSearchParams(location.search).get('tab');
  if (tab === 'settings' || tab === 'logs') switchTab(tab);
  pollStatus();
}
init();
