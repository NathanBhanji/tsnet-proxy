/* tsnet-proxy management UI
 * Vanilla JS, no build step. Talks to the handlers in internal/ui/handler.go:
 *   GET    /api/services          list services (+ healthy flag)
 *   POST   /api/services          add service
 *   DELETE /api/services/{name}   remove service
 *   GET    /api/health            map[name]bool
 *   GET/PUT /api/config           raw configs/services.yaml (PUT applies + hot-reloads)
 *   GET    /api/settings          global config (secrets masked) + tailnet MagicDNS suffix
 *   GET    /api/devices           tsnet devices owned by this proxy (ip, online)
 *   GET    /api/activity          recent proxied requests, newest first
 *   GET    /api/metrics           Prometheus text exposition
 * Every endpoint beyond the first four is optional — the UI degrades gracefully if it 404s.
 */

const POLL_MS = 5000;
const HISTORY = 26;         // sparkline buckets
const state = {
  view: 'services',
  services: [],
  health: {},
  devices: null,
  settings: null,
  metrics: null,            // { name: { rpm, p95, errRate } }
  history: {},              // name -> [values]
  counters: {},             // name -> last cumulative request count
  rng: {},                  // name -> demo LCG state
  activity: [],
  activityLive: false,      // /api/activity responded
  selected: null,
  demo: false,
};

/* ---------------- helpers ---------------- */

const $ = sel => document.querySelector(sel);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
};
const fmt = n => n === null || n === undefined ? '—' : n.toLocaleString('en-US');
const codeClass = c => c >= 500 ? 'code-5' : c >= 400 ? 'code-4' : 'code-2';

async function api(path, opts) {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...opts });
  if (!res.ok) throw new Error((await res.text()) || res.statusText);
  const type = res.headers.get('content-type') || '';
  return type.includes('json') ? res.json() : res.text();
}

let toastTimer;
function toast(msg, isErr) {
  clearTimeout(toastTimer);
  document.querySelectorAll('.toast').forEach(t => t.remove());
  const t = el('div', 'toast' + (isErr ? ' err' : ''), msg);
  document.body.appendChild(t);
  toastTimer = setTimeout(() => t.remove(), 4000);
}

/* ---------------- data ---------------- */

async function load() {
  try {
    const services = await api('/api/services');
    state.services = Array.isArray(services) ? services : [];
    state.demo = false;
  } catch (e) {
    if (!state.demo) { state.demo = true; state.services = DEMO.services; }
  }
  // Keep card order stable across polls regardless of API ordering.
  state.services.sort((a, b) => a.name.localeCompare(b.name));

  await Promise.all([
    api('/api/health').then(h => { state.health = h || {}; }).catch(() => {}),
    api('/api/devices').then(d => { state.devices = d; }).catch(() => { state.devices = null; }),
    api('/api/settings').then(s => { state.settings = s; }).catch(() => { state.settings = null; }),
    api('/api/metrics').then(t => { ingestMetrics(t); }).catch(() => { state.metrics = state.demo ? DEMO.metrics : null; }),
    api('/api/activity?limit=40').then(a => { state.activity = Array.isArray(a) ? a : []; state.activityLive = true; })
      .catch(() => { state.activity = []; state.activityLive = false; }),
  ]);

  // sparkline history: request rate when metrics exist, otherwise health over time
  state.services.forEach(s => {
    const h = state.history[s.name] || [];
    const m = state.metrics && state.metrics[s.name];
    let v = m && m.rpm !== null && m.rpm !== undefined ? m.rpm : (isHealthy(s) ? 1 : 0);
    if (state.demo) {
      // no backend to poll — fill the strip once so the layout reads true
      if (!h.length) for (let i = 0; i < HISTORY - 1; i++) h.push(jitter(s.name, v));
      v = jitter(s.name, v);
    }
    h.push(v);
    while (h.length > HISTORY) h.shift();
    state.history[s.name] = h;
  });

  render();
}

/* Deterministic wobble for demo sparklines. The LCG carries state forward per
 * service so successive buckets don't repeat on a short period. */
function jitter(name, base) {
  let x = state.rng[name];
  if (x === undefined) {
    x = 7919;
    for (let i = 0; i < name.length; i++) x = (x * 31 + name.charCodeAt(i)) % 233280;
  }
  x = (x * 9301 + 49297) % 233280;
  state.rng[name] = x;
  if (base === 0) return 0;
  return Math.round(base * (0.5 + (x / 233280) * 0.85));
}

/* Parse Prometheus text exposition into per-service rates, p95 and error rate.
 * Series come from internal/metrics/metrics.go:
 *   tsnet_proxy_requests_total{service,method,status}
 *   tsnet_proxy_request_duration_seconds_bucket{service,method,le}
 *   tsnet_proxy_service_health{service}
 */
function ingestMetrics(text) {
  if (typeof text !== 'string') return;
  const totals = {}, errors = {}, healthy = {}, buckets = {};

  text.split('\n').forEach(line => {
    if (!line || line[0] === '#') return;
    const m = line.match(/^(\w+)\{([^}]*)\}\s+([0-9.eE+-]+)/);
    if (!m) return;
    const [, metric, labels, raw] = m;
    const val = parseFloat(raw);
    const svc = (labels.match(/service="([^"]*)"/) || [])[1];
    if (!svc || !isFinite(val)) return;

    if (metric === 'tsnet_proxy_requests_total') {
      totals[svc] = (totals[svc] || 0) + val;
      const status = parseInt((labels.match(/status="([^"]*)"/) || [])[1], 10);
      if (status >= 500) errors[svc] = (errors[svc] || 0) + val;
    } else if (metric === 'tsnet_proxy_request_duration_seconds_bucket') {
      const le = (labels.match(/le="([^"]*)"/) || [])[1];
      if (le === undefined) return;
      const b = buckets[svc] || (buckets[svc] = {});
      b[le] = (b[le] || 0) + val;
    } else if (metric === 'tsnet_proxy_service_health') {
      healthy[svc] = val === 1;
    }
  });

  const now = Date.now();
  const out = {};
  Object.keys(totals).forEach(svc => {
    const prev = state.counters[svc];
    let rpm = null, errRate = null;
    const errTotal = errors[svc] || 0;
    if (prev && now > prev.t) {
      const dReq = totals[svc] - prev.v;
      const dErr = errTotal - prev.err;
      rpm = Math.max(0, Math.round(dReq / ((now - prev.t) / 60000)));
      if (dReq > 0) errRate = (dErr / dReq) * 100;
    }
    if (errRate === null && totals[svc] > 0) errRate = (errTotal / totals[svc]) * 100;
    state.counters[svc] = { v: totals[svc], err: errTotal, t: now };
    out[svc] = {
      rpm,
      healthy: healthy[svc],
      errRate: errRate === null ? undefined : errRate.toFixed(2) + '%',
      p95: p95FromBuckets(buckets[svc]),
    };
  });
  state.metrics = out;
}

/* 95th percentile estimate: smallest bucket boundary covering 95% of observations. */
function p95FromBuckets(b) {
  if (!b) return undefined;
  const bounds = Object.keys(b)
    .map(le => ({ le, n: le === '+Inf' ? Infinity : parseFloat(le), c: b[le] }))
    .filter(x => isFinite(x.c))
    .sort((x, y) => x.n - y.n);
  if (!bounds.length) return undefined;
  const total = bounds[bounds.length - 1].c;
  if (!total) return undefined;
  const target = total * 0.95;
  const hit = bounds.find(x => x.c >= target);
  if (!hit || !isFinite(hit.n)) return '>' + fmtDur(bounds[bounds.length - 2] ? bounds[bounds.length - 2].n : 0);
  return fmtDur(hit.n);
}

function fmtDur(sec) {
  if (sec >= 1) return sec.toFixed(sec >= 10 ? 0 : 1) + 's';
  return Math.round(sec * 1000) + 'ms';
}

const isHealthy = s => (state.health[s.name] !== undefined ? state.health[s.name] : s.healthy) !== false;

/* ---------------- render ---------------- */

function render() {
  const views = { services: '#view-services', detail: '#view-detail', config: '#view-config', settings: '#view-settings' };
  Object.entries(views).forEach(([k, sel]) => { $(sel).hidden = state.view !== k; });
  document.querySelectorAll('#nav button').forEach(b => {
    const active = b.dataset.view === state.view || (b.dataset.view === 'services' && state.view === 'detail');
    active ? b.setAttribute('aria-current', 'page') : b.removeAttribute('aria-current');
  });

  const up = state.services.filter(isHealthy).length;
  const down = state.services.length - up;
  $('#health-summary').innerHTML =
    `<span style="color:var(--green)">${up}</span> healthy / <span style="color:${down ? 'var(--red)' : 'var(--dim)'}">${down}</span> down`;
  $('#global-dot').className = 'dot ' + (down ? 'down' : 'up');
  $('#tailnet').textContent = (state.settings && state.settings.tailnet) || (state.demo ? DEMO.tailnet : 'your-tailnet.ts.net');
  $('#refresh-note').textContent = state.demo ? 'demo data · api unreachable' : `refreshing ${POLL_MS / 1000}s`;

  renderStatStrip();
  renderGrid();
  renderActivity();
  renderDevices();
  if (state.view === 'detail') renderDetail();
  if (state.view === 'settings') renderSettings();
}

function statTile(label, value, unit) {
  const s = el('div', 'stat');
  s.appendChild(el('div', 'stat-k', label));
  const v = el('div', 'stat-v', value);
  if (unit) v.appendChild(el('span', null, unit));
  s.appendChild(v);
  return s;
}

function renderStatStrip() {
  const strip = $('#stat-strip');
  strip.textContent = '';
  const m = state.metrics || {};
  const rates = Object.values(m).map(x => x.rpm).filter(x => x !== null && x !== undefined);
  const total = rates.length ? rates.reduce((a, b) => a + b, 0) : null;
  const online = state.devices ? state.devices.filter(d => d.online).length : state.services.filter(isHealthy).length + 1;
  const totalDev = state.devices ? state.devices.length : state.services.length + 1;
  strip.appendChild(statTile('requests / min', total === null ? '—' : fmt(total)));
  strip.appendChild(statTile('services', String(state.services.length)));
  strip.appendChild(statTile('unhealthy', String(state.services.filter(s => !isHealthy(s)).length)));
  strip.appendChild(statTile('devices online', String(online), `/ ${totalDev}`));
}

/* Bars for whatever history we have — flex stretches them to fill the strip,
 * so an 8-poll-old UI looks like a coarse chart, not a broken one. */
function barStrip(name, healthy, height, cls) {
  const vals = (state.history[name] || []).slice();
  if (vals.length < 2) {
    const note = el('div', cls, 'collecting…');
    note.style.cssText = `height:${height}px;display:flex;align-items:flex-end;color:var(--faint);font-size:11px`;
    return note;
  }
  const wrap = el('div', cls + (healthy ? '' : ' down'));
  const max = Math.max(1, ...vals);
  vals.forEach((v, i) => {
    const bar = el('i');
    bar.style.height = Math.max(2, Math.round((v / max) * height)) + 'px';
    if (v === 0) bar.style.background = '#3a2f2b';
    if (i === vals.length - 1) bar.style.background = healthy ? 'var(--green)' : 'var(--red)';
    wrap.appendChild(bar);
  });
  return wrap;
}

function renderGrid() {
  const grid = $('#service-grid');
  grid.textContent = '';
  $('#services-empty').hidden = state.services.length > 0;

  state.services.forEach(svc => {
    const healthy = isHealthy(svc);
    const m = (state.metrics && state.metrics[svc.name]) || {};
    const card = el('article', 'card');

    const head = el('div', 'card-head');
    head.appendChild(el('span', 'dot ' + (healthy ? 'up' : 'down')));
    const idbox = el('div');
    idbox.style.cssText = 'flex:1;min-width:0';
    const host = el('div', 'host');
    host.appendChild(document.createTextNode(svc.name));
    host.appendChild(el('span', null, '.' + $('#tailnet').textContent));
    idbox.appendChild(host);
    idbox.appendChild(el('div', 'backend truncate', '→ ' + svc.backend));
    head.appendChild(idbox);
    const badge = el('span', 'badge ' + (healthy ? 'up' : 'down'),
      healthy ? 'healthy' : `unhealthy · ${svc.healthCheck?.unhealthyThreshold || 3}/${svc.healthCheck?.unhealthyThreshold || 3}`);
    head.appendChild(badge);
    card.appendChild(head);

    card.appendChild(barStrip(svc.name, healthy, 38, 'spark'));

    const row = el('div', 'metrics-row');
    [['req/min', m.rpm === null || m.rpm === undefined ? '—' : fmt(m.rpm), ''],
     ['p95', m.p95 || '—', ''],
     ['errors', m.errRate === undefined ? '—' : m.errRate, m.errRate && m.errRate !== '0.00%' ? (healthy ? 'warn' : 'bad') : '']
    ].forEach(([k, v, cls]) => {
      const c = el('div');
      c.appendChild(el('div', 'k', k));
      c.appendChild(el('div', 'v' + (cls ? ' ' + cls : ''), v));
      row.appendChild(c);
    });
    card.appendChild(row);

    const tags = el('div', 'tags');
    if (svc.paths && svc.paths.length) tags.appendChild(el('span', 'tag accent', svc.paths.join(' ')));
    else tags.appendChild(el('span', 'tag', 'all paths'));
    if (svc.stripPrefix) tags.appendChild(el('span', 'tag', 'stripPrefix'));
    if (svc.tls && svc.tls.enabled) tags.appendChild(el('span', 'tag' + (svc.tls.skipVerify ? ' danger' : ''), svc.tls.skipVerify ? 'tls · skipVerify' : 'tls'));
    tags.appendChild(el('span', 'spacer'));

    const inspect = el('button', 'btn-ghost', 'inspect');
    inspect.onclick = () => { state.selected = svc.name; state.view = 'detail'; render(); };
    const remove = el('button', 'btn-ghost danger', 'remove');
    remove.onclick = () => removeService(svc.name);
    tags.appendChild(inspect);
    tags.appendChild(remove);
    card.appendChild(tags);

    grid.appendChild(card);
  });
}

function renderActivity() {
  const log = $('#activity-log');
  log.textContent = '';
  const note = el('div');
  note.style.cssText = 'color:var(--faint);font-size:11px;line-height:1.6';

  if (!state.activityLive) {
    $('#activity-state').textContent = state.demo ? 'demo' : 'no stream';
    $('#activity-state').style.color = 'var(--faint)';
    note.textContent = state.demo
      ? 'Sample entries — connect the UI to the proxy to see live requests.'
      : '/api/activity is unavailable on this build of the proxy.';
    log.appendChild(note);
    if (state.demo) DEMO.activity.forEach(a => log.appendChild(activityRow(a)));
    return;
  }

  $('#activity-state').textContent = 'live';
  $('#activity-state').style.color = 'var(--green)';
  if (!state.activity.length) {
    note.textContent = 'No requests proxied yet — the last 200 will appear here as they arrive.';
    log.appendChild(note);
    return;
  }
  state.activity.slice(0, 20).forEach(a => log.appendChild(activityRow(a)));
}

const fmtTime = ts => {
  const d = new Date(ts);
  return isNaN(d) ? '—' : d.toTimeString().slice(0, 8);
};

function activityRow(a) {
  const row = el('div', 'log-row');
  row.appendChild(el('span', 't', a.time || fmtTime(a.ts)));
  const mid = el('span', 'truncate');
  mid.appendChild(el('span', 's', a.service));
  mid.appendChild(document.createTextNode(' '));
  mid.appendChild(el('span', 'p', a.method ? `${a.method} ${a.path}` : a.path));
  if (a.durationMs !== undefined) mid.title = `${a.durationMs.toFixed(1)} ms`;
  row.appendChild(mid);
  row.appendChild(el('span', codeClass(a.status), String(a.status)));
  return row;
}

function renderDevices() {
  const list = $('#device-list');
  list.textContent = '';
  const devices = state.devices || state.services.map(s => ({ name: s.name, ip: '—', online: isHealthy(s) }));
  devices.forEach(d => {
    const row = el('div', 'device');
    row.appendChild(el('span', 'dot ' + (d.online ? 'up' : 'idle')));
    const n = el('span', 'truncate', d.name);
    n.style.flex = '1';
    row.appendChild(n);
    row.appendChild(el('span', 'ip', d.ip || '—'));
    list.appendChild(row);
  });
  if (!state.devices) {
    const note = el('div');
    note.style.cssText = 'color:var(--faint);font-size:11px;margin-top:8px;line-height:1.6';
    note.textContent = 'IPs unavailable — add an /api/devices handler backed by the Tailscale API for addresses and last-seen.';
    list.appendChild(note);
  }
}

function renderDetail() {
  const svc = state.services.find(s => s.name === state.selected);
  if (!svc) { state.view = 'services'; render(); return; }
  const healthy = isHealthy(svc);
  const m = (state.metrics && state.metrics[svc.name]) || {};

  $('#detail-dot').className = 'dot ' + (healthy ? 'up' : 'down');
  $('#detail-host').innerHTML = `${svc.name}<span style="color:var(--faint);font-weight:400">.${$('#tailnet').textContent}</span>`;
  const badge = $('#detail-badge');
  badge.className = 'badge ' + (healthy ? 'up' : 'down');
  badge.textContent = healthy ? 'healthy' : 'unhealthy';
  $('#detail-backend').textContent = 'proxying to ' + svc.backend;

  const stats = $('#detail-stats');
  stats.textContent = '';
  stats.appendChild(statTile('req/min', m.rpm === null || m.rpm === undefined ? '—' : fmt(m.rpm)));
  stats.appendChild(statTile('p95 latency', m.p95 || '—'));
  stats.appendChild(statTile('error rate', m.errRate || '—'));
  stats.appendChild(statTile('checks', svc.healthCheck?.enabled ? 'on' : 'off'));

  const oldChart = $('#detail-chart');
  const chart = barStrip(svc.name, healthy, 120, 'chart');
  chart.id = 'detail-chart';
  oldChart.replaceWith(chart);
  const vals = state.history[svc.name] || [];
  const max = Math.max(1, ...vals);
  $('#detail-peak').textContent = max > 1 ? 'peak ' + fmt(max) : `${vals.length} of ${HISTORY} buckets`;

  const hc = svc.healthCheck || {};
  kvList('#detail-hc', [
    ['enabled', String(!!hc.enabled)],
    ['path', hc.path || '—'],
    ['interval', hc.interval || '—'],
    ['timeout', hc.timeout || '—'],
    ['unhealthyThreshold', String(hc.unhealthyThreshold ?? '—')],
  ]);
  kvList('#detail-route', [
    ['backend', svc.backend],
    ['paths', svc.paths && svc.paths.length ? svc.paths.join(', ') : 'all'],
    ['stripPrefix', String(!!svc.stripPrefix)],
    ['tls.enabled', String(!!(svc.tls && svc.tls.enabled))],
    ['tls.skipVerify', String(!!(svc.tls && svc.tls.skipVerify))],
  ]);
}

function kvList(sel, rows) {
  const box = $(sel);
  box.textContent = '';
  rows.forEach(([k, v]) => {
    const r = el('div', 'kv');
    r.appendChild(el('span', null, k));
    r.appendChild(el('span', null, v));
    box.appendChild(r);
  });
}

function renderSettings() {
  const s = state.settings || {};
  const groups = [
    ['Tailscale', 'Credentials are read from the environment at startup.', [
      ['authKey', s.authKeyMasked || 'set via $TS_AUTHKEY'],
      ['apiKey', s.apiKeyMasked || (s.apiKeySet === false ? 'not set — devices clean up via ephemeral mode' : 'set via $TS_API_KEY')],
      ['tailnet', s.tailnet || '$TS_TAILNET'],
      ['stateDir', s.stateDir || '/data/tsnet'],
    ]],
    ['Management UI', 'This interface. Runs as its own tailnet device.', [
      ['hostname', (s.managementUI && s.managementUI.hostname) || 'tsnet-proxy-docker'],
      ['url', (s.managementUI && s.managementUI.url) || '—'],
      ['backend state', s.backendState || '—'],
    ]],
    ['Metrics', 'Prometheus scrape endpoint.', [
      ['enabled', String(s.metrics ? !!s.metrics.enabled : true)],
      ['port', String((s.metrics && s.metrics.port) || 9090)],
    ]],
  ];
  const host = $('#settings-groups');
  host.textContent = '';
  groups.forEach(([title, note, fields]) => {
    const sec = el('section', 'group');
    const head = el('header');
    head.appendChild(el('div', null, title));
    head.appendChild(el('div', 'note', note));
    sec.appendChild(head);
    const body = el('div');
    fields.forEach(([k, v]) => {
      const row = el('label', 'settings-row');
      row.appendChild(el('span', null, k));
      const input = el('input');
      input.type = 'text';
      input.value = v;
      input.readOnly = true;
      row.appendChild(input);
      body.appendChild(row);
    });
    sec.appendChild(body);
    host.appendChild(sec);
  });
  if (!state.settings) {
    const note = el('p');
    note.style.cssText = 'color:var(--faint);font-size:11px;line-height:1.6';
    note.textContent = 'Values shown are defaults. Add a GET /api/settings handler returning the parsed config (with secrets masked) to show live values.';
    host.appendChild(note);
  }
}

/* ---------------- config editor ---------------- */

async function loadConfig() {
  const ta = $('#yaml');
  try {
    const text = await api('/api/config');
    ta.value = typeof text === 'string' ? text : JSON.stringify(text, null, 2);
    ta.readOnly = false;
    validate([['✓', 'Loaded configs/services.yaml from disk', 'var(--green)']]);
  } catch (e) {
    ta.value = yamlFromServices(state.services);
    ta.readOnly = true;
    validate([
      ['!', 'Read-only preview generated from /api/services', 'var(--amber)'],
      ['!', 'Add GET/PUT /api/config to edit the file from here', 'var(--amber)'],
    ]);
  }
  syncGutter();
}

function yamlFromServices(services) {
  const lines = ['services:'];
  if (!services.length) lines[0] = 'services: []';
  services.forEach(s => {
    lines.push(`  - name: ${s.name}`);
    lines.push(`    backend: ${s.backend}`);
    if (s.paths && s.paths.length) { lines.push('    paths:'); s.paths.forEach(p => lines.push(`      - ${p}`)); }
    else lines.push('    paths: []');
    lines.push(`    stripPrefix: ${!!s.stripPrefix}`);
    const hc = s.healthCheck || {};
    lines.push('    healthCheck:');
    lines.push(`      enabled: ${!!hc.enabled}`);
    if (hc.enabled) {
      lines.push(`      path: ${hc.path}`);
      lines.push(`      interval: ${hc.interval}`);
      lines.push(`      timeout: ${hc.timeout}`);
      lines.push(`      unhealthyThreshold: ${hc.unhealthyThreshold}`);
    }
    lines.push('    tls:');
    lines.push(`      enabled: ${!!(s.tls && s.tls.enabled)}`);
    if (s.tls && s.tls.skipVerify) lines.push('      skipVerify: true');
    lines.push('');
  });
  return lines.join('\n');
}

function validate(rows) {
  const box = $('#validation');
  box.textContent = '';
  rows.forEach(([icon, text, color]) => {
    const r = el('div');
    r.style.cssText = 'display:flex;gap:9px;padding:8px 0;border-bottom:1px solid var(--edge-soft);font-size:12px';
    const i = el('span', null, icon);
    i.style.color = color;
    r.appendChild(i);
    const t = el('span', null, text);
    t.style.color = 'var(--fg-mid)';
    r.appendChild(t);
    box.appendChild(r);
  });
}

function syncGutter() {
  const n = $('#yaml').value.split('\n').length;
  $('#gutter').textContent = Array.from({ length: n }, (_, i) => i + 1).join('\n');
  $('#gutter').style.whiteSpace = 'pre';
}

/* ---------------- mutations ---------------- */

async function removeService(name) {
  if (!confirm(`Remove ${name}? Its Tailscale device will be deleted from your tailnet.`)) return;
  try {
    await api('/api/services/' + encodeURIComponent(name), { method: 'DELETE' });
    toast(`${name} removed`);
    if (state.selected === name) { state.view = 'services'; state.selected = null; }
    load();
  } catch (e) { toast('Failed to remove: ' + e.message, true); }
}

async function submitAdd() {
  const f = $('#add-form');
  if (!f.reportValidity()) return;
  const d = new FormData(f);
  const paths = String(d.get('paths') || '').split(',').map(p => p.trim()).filter(Boolean);
  const hcOn = !!d.get('hcEnabled');
  const body = {
    name: String(d.get('name')).trim(),
    backend: String(d.get('backend')).trim(),
    paths,
    stripPrefix: !!d.get('stripPrefix'),
    healthCheck: {
      enabled: hcOn,
      path: String(d.get('hcPath') || '/health'),
      interval: String(d.get('hcInterval') || '30s'),
      timeout: String(d.get('hcTimeout') || '5s'),
      unhealthyThreshold: Number(d.get('hcThreshold') || 3),
    },
    tls: { enabled: !!d.get('tlsEnabled'), skipVerify: !!d.get('tlsSkipVerify') },
  };
  try {
    await api('/api/services', { method: 'POST', body: JSON.stringify(body) });
    $('#add-scrim').hidden = true;
    f.reset();
    toast(`${body.name} created — joining the tailnet`);
    load();
  } catch (e) { toast('Failed to add: ' + e.message, true); }
}

/* ---------------- wiring ---------------- */

document.querySelectorAll('#nav button').forEach(b => {
  b.onclick = () => {
    state.view = b.dataset.view;
    if (state.view === 'config') loadConfig();
    render();
  };
});
$('#detail-back').onclick = () => { state.view = 'services'; render(); };
$('#open-add').onclick = () => { $('#add-scrim').hidden = false; $('#add-form').querySelector('input').focus(); };
$('#close-add').onclick = $('#cancel-add').onclick = () => { $('#add-scrim').hidden = true; };
$('#add-scrim').onclick = e => { if (e.target === $('#add-scrim')) $('#add-scrim').hidden = true; };
$('#submit-add').onclick = submitAdd;
$('#f-hc').onchange = e => { $('#hc-fields').hidden = !e.target.checked; };
$('#yaml').oninput = () => { $('#dirty-flag').hidden = false; syncGutter(); };
$('#apply-config').onclick = async () => {
  const btn = $('#apply-config');
  btn.disabled = true;
  try {
    const res = await api('/api/config', { method: 'PUT', headers: { 'Content-Type': 'application/yaml' }, body: $('#yaml').value });
    $('#dirty-flag').hidden = true;
    showApplyResult(res);
    toast(applySummary(res));
    load();
  } catch (e) {
    // A partial apply returns 500 with the same JSON body; surface its details.
    let res = null;
    try { res = JSON.parse(e.message); } catch (_) { /* plain error text */ }
    if (res && Array.isArray(res.errors)) {
      showApplyResult(res);
      toast('Config saved but some services failed — see validation', true);
    } else {
      toast('Apply failed: ' + e.message, true);
    }
  } finally { btn.disabled = false; }
};

function applySummary(r) {
  const parts = [];
  if (r.added && r.added.length) parts.push(`+${r.added.length} added`);
  if (r.removed && r.removed.length) parts.push(`−${r.removed.length} removed`);
  if (r.updated && r.updated.length) parts.push(`~${r.updated.length} restarted`);
  if (!parts.length) parts.push('no service changes');
  let msg = 'Config applied · ' + parts.join(', ');
  if (r.restartRequired && r.restartRequired.length) msg += ' · restart required for ' + r.restartRequired.join(', ');
  return msg;
}

function showApplyResult(r) {
  const rows = [];
  const list = (label, xs) => xs && xs.length && rows.push(['✓', `${label}: ${xs.join(', ')}`, 'var(--green)']);
  list('added', r.added); list('removed', r.removed); list('restarted', r.updated);
  if (r.unchanged) rows.push(['·', `${r.unchanged} unchanged`, 'var(--dim)']);
  (r.restartRequired || []).forEach(f => rows.push(['!', `${f} changed — restart the proxy to apply`, 'var(--amber)']));
  (r.errors || []).forEach(err => rows.push(['✗', err, 'var(--red)']));
  if (!rows.length) rows.push(['✓', 'Config applied', 'var(--green)']);
  validate(rows);
}
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') $('#add-scrim').hidden = true;
});

/* ---------------- demo fallback (browsing static/ without the proxy running) -- */

const DEMO = {
  tailnet: 'your-tailnet.ts.net',
  services: [
    { name: 'grafana', backend: 'http://grafana:3000', paths: [], stripPrefix: false, healthy: true,
      healthCheck: { enabled: true, path: '/api/health', interval: '30s', timeout: '5s', unhealthyThreshold: 3 }, tls: { enabled: false } },
    { name: 'api', backend: 'http://api-server:8080', paths: ['/api'], stripPrefix: true, healthy: true,
      healthCheck: { enabled: true, path: '/health', interval: '10s', timeout: '3s', unhealthyThreshold: 2 }, tls: { enabled: false } },
    { name: 'prometheus', backend: 'http://prometheus:9090', paths: [], stripPrefix: false, healthy: true,
      healthCheck: { enabled: true, path: '/-/healthy', interval: '30s', timeout: '5s', unhealthyThreshold: 3 }, tls: { enabled: false } },
    { name: 'minio', backend: 'https://minio:9000', paths: [], stripPrefix: false, healthy: true,
      healthCheck: { enabled: true, path: '/minio/health/live', interval: '30s', timeout: '5s', unhealthyThreshold: 3 }, tls: { enabled: true, skipVerify: true } },
    { name: 'home-assistant', backend: 'http://homeassistant:8123', paths: [], stripPrefix: false, healthy: false,
      healthCheck: { enabled: true, path: '/', interval: '30s', timeout: '5s', unhealthyThreshold: 3 }, tls: { enabled: false } },
  ],
  metrics: {
    grafana: { rpm: 142, p95: '38ms', errRate: '0.00%' },
    api: { rpm: 2140, p95: '91ms', errRate: '0.41%' },
    prometheus: { rpm: 88, p95: '24ms', errRate: '0.00%' },
    minio: { rpm: 410, p95: '62ms', errRate: '0.12%' },
    'home-assistant': { rpm: 0, p95: '—', errRate: '100%' },
  },
  activity: [
    { time: '18:04:12', service: 'api', path: 'GET /api/users', status: 200 },
    { time: '18:04:12', service: 'grafana', path: 'GET /d/abc123', status: 200 },
    { time: '18:04:11', service: 'home-assistant', path: 'GET /', status: 503 },
    { time: '18:04:11', service: 'api', path: 'POST /api/session', status: 201 },
    { time: '18:04:10', service: 'minio', path: 'PUT /uploads/x.png', status: 200 },
    { time: '18:04:09', service: 'api', path: 'GET /api/users/42', status: 404 },
    { time: '18:04:08', service: 'prometheus', path: 'GET /api/v1/query', status: 200 },
    { time: '18:04:04', service: 'api', path: 'GET /api/feed', status: 500 },
  ],
};

load();
setInterval(load, POLL_MS);
