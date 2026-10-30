'use strict';

// --- helpers -----------------------------------------------------------------

const $ = (id) => document.getElementById(id);
// Every string from the API is escaped: Bluetooth names come from the air.
const esc = (v) => String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const rows = (pairs) => pairs.filter((p) => p && p[1] !== undefined && p[1] !== '' && p[1] !== null)
  .map(([k, v]) => `<div class="row"><span>${esc(k)}</span><span>${v}</span></div>`).join('');
const dot = (cls) => `<span class="dot ${cls}"></span>`;
const hex4 = (n) => '0x' + Number(n || 0).toString(16).toUpperCase().padStart(4, '0');

function uptime(s) {
  const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m`;
}
function ago(iso) {
  if (!iso) return '—';
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000);
  return s < 60 ? `${Math.round(s)}s ago` : s < 3600 ? `${Math.round(s / 60)}m ago` : `${Math.round(s / 3600)}h ago`;
}
function rate(b) {
  return b >= 1e6 ? (b / 1e6).toFixed(1) + ' MB/s' : b >= 1e3 ? (b / 1e3).toFixed(1) + ' kB/s' : Math.round(b) + ' B/s';
}
function pct(a, b) { return b ? Math.round(a * 100 / b) : 0; }
function toast(msg) {
  const t = $('toast'); t.textContent = msg; t.classList.add('on');
  clearTimeout(toast.t); toast.t = setTimeout(() => t.classList.remove('on'), 3500);
}
async function api(path, form) {
  const opt = form ? { method: 'POST', body: new URLSearchParams(form) } : {};
  const r = await fetch(path, opt);
  const ct = r.headers.get('content-type') || '';
  const body = ct.includes('json') ? await r.json() : await r.text();
  if (!r.ok) throw new Error(body.error || r.statusText);
  return body;
}

// --- tabs ----------------------------------------------------------------------

function showTab() {
  const tab = location.hash.slice(1) || 'overview';
  document.querySelectorAll('section').forEach((s) => s.classList.toggle('on', s.id === tab));
  document.querySelectorAll('#tabs a').forEach((a) => a.classList.toggle('on', a.dataset.tab === tab));
  if (tab === 'logs') loadLog();
  if (tab === 'bluetooth') loadBluetooth();
}
window.addEventListener('hashchange', showTab);

// --- charts --------------------------------------------------------------------

function chart(svg, series, opts = {}) {
  const W = 600, H = 150, L = 56, R = 8, T = 8, B = 18;
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  const n = Math.max(...series.map((s) => s.values.length), 0);
  if (n < 2) { svg.innerHTML = `<text x="${W / 2}" y="${H / 2}" text-anchor="middle">collecting data…</text>`; return; }
  let max = opts.max || Math.max(1, ...series.flatMap((s) => s.values));
  if (!opts.max) max *= 1.15;
  const x = (i) => L + i * (W - L - R) / (n - 1);
  const y = (v) => T + (H - T - B) * (1 - v / max);
  const fmt = opts.fmt || ((v) => Math.round(v));
  let g = '<g class="grid">';
  for (let k = 0; k <= 3; k++) {
    const v = max * k / 3;
    g += `<line x1="${L}" x2="${W - R}" y1="${y(v)}" y2="${y(v)}"/><text x="${L - 4}" y="${y(v) + 3}" text-anchor="end">${esc(fmt(v))}</text>`;
  }
  g += '</g>';
  const lines = series.map((s) => {
    const d = s.values.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('');
    const area = `${d}L${x(s.values.length - 1)},${y(0)}L${x(0)},${y(0)}Z`;
    return `<path d="${area}" fill="${s.color}" fill-opacity=".10"/><path d="${d}" fill="none" stroke="${s.color}" stroke-width="1.8" vector-effect="non-scaling-stroke"/>`;
  }).join('');
  svg.innerHTML = g + lines;
}

let range = '1h';
async function loadHistory() {
  try {
    const h = await api('/api/history?range=' + range);
    const p = h.points || [];
    const css = getComputedStyle(document.documentElement);
    const c = (v) => css.getPropertyValue(v).trim();
    chart($('ch-cpu'), [{ values: p.map((q) => q.cpu), color: c('--c1') }], { max: 100 });
    chart($('ch-mem'), [{ values: p.map((q) => q.mem), color: c('--c4') }], { max: state?.resources?.mem_total_mb });
    chart($('ch-net'), [{ values: p.map((q) => q.rx), color: c('--c1') }, { values: p.map((q) => q.tx), color: c('--c3') }], { fmt: rate });
    chart($('ch-ble'), [{ values: p.map((q) => q.ble), color: c('--c2') }]);
  } catch (e) { /* offline: keep the last chart */ }
}
$('range').addEventListener('click', (e) => {
  const r = e.target.dataset.r; if (!r) return;
  range = r;
  document.querySelectorAll('#range button').forEach((b) => b.classList.toggle('on', b.dataset.r === r));
  loadHistory();
});

// --- rendering -----------------------------------------------------------------

let state = null;

function render(s) {
  state = s;
  const sys = s.system, res = s.resources, net = s.network, zb = s.zigbee, ha = s.home_assistant;
  const bt = s.bluetooth.info || {};
  const zn = zb.network || {};
  const devices = zb.devices || [];

  $('host').textContent = s.hostname;
  const b = $('sysbadge');
  b.textContent = sys.userland ? 'OpenHub2' : sys.mode === 'openhub2' ? 'Stock · OpenHub2 mode' : 'Stock · Xiaomi mode';
  b.className = 'badge ' + (sys.userland ? 'ok' : 'warn');
  $('updated').textContent = 'up ' + uptime(sys.uptime_s);

  const zbOk = zn.status === 'joined', btOk = s.bluetooth.proxy === 'running';
  $('tiles').innerHTML = [
    `<div class="card"><h3>System</h3><div class="big">${sys.userland ? 'OpenHub2' : 'Stock'}</div>
      <div class="sub">slot ${esc(sys.slot)} · next boot ${esc(sys.next_boot)}${sys.image ? ' · ' + esc(sys.image) : ''}</div></div>`,
    `<div class="card"><h3>CPU</h3><div class="big">${Math.round(res.cpu)}%</div><div class="bar"><i style="width:${Math.min(100, res.cpu)}%"></i></div>
      <div class="sub">${res.cpu_mhz ? res.cpu_mhz + ' MHz · ' : ''}2 cores</div></div>`,
    `<div class="card"><h3>Memory</h3><div class="big">${res.mem_used_mb} <span class="sub">/ ${res.mem_total_mb} MB</span></div>
      <div class="bar"><i style="width:${pct(res.mem_used_mb, res.mem_total_mb)}%"></i></div><div class="sub">${pct(res.mem_used_mb, res.mem_total_mb)}% used</div></div>`,
    `<div class="card"><h3>Storage /data</h3><div class="big">${(res.data_free_kb / 1024).toFixed(1)} <span class="sub">MB free</span></div>
      <div class="bar"><i style="width:${pct(res.data_size_kb - res.data_free_kb, res.data_size_kb)}%"></i></div><div class="sub">of ${(res.data_size_kb / 1024).toFixed(0)} MB</div></div>`,
    `<div class="card"><h3>Network</h3><div class="big" style="font-size:20px">${esc(net.ip || '—')}</div>
      <div class="sub">${esc(net.ssid || 'no Wi-Fi')}${net.rssi ? ' · ' + net.rssi + ' dBm' : ''}${net.ethernet_link ? ' · Ethernet' : ''}</div></div>`,
    `<div class="card"><h3>Home Assistant</h3>
      <div>${dot(ha.zha ? 'ok' : 'bad')}ZHA ${ha.zha ? 'connected' : 'not connected'}</div>
      <div style="margin-top:6px">${dot(ha.esphome_clients ? 'ok' : 'bad')}Bluetooth proxy ${ha.esphome_clients ? 'connected' : 'not connected'}</div></div>`,
    `<div class="card"><h3>Zigbee</h3><div>${dot(zbOk ? 'ok' : zb.bridge === 'running' ? 'warn' : 'bad')}${esc(zn.status || zb.bridge)}</div>
      <div class="sub" style="margin-top:6px">${zbOk ? `channel ${zn.channel} · PAN ${hex4(zn.pan_id)} · ${devices.length} devices` : 'waiting for ZHA'}</div></div>`,
    `<div class="card"><h3>Bluetooth</h3><div>${dot(btOk ? 'ok' : 'bad')}${btOk ? 'proxy running' : 'proxy stopped'}</div>
      <div class="sub" style="margin-top:6px">${btOk ? `${bt.devices_5min ?? 0} devices in 5 min` : ''}</div></div>`,
  ].join('');

  // Zigbee tab
  $('zb-net').innerHTML = rows([
    ['Bridge', `${dot(zb.bridge === 'running' ? 'ok' : 'bad')}${esc(zb.bridge)} · tcp 8888`],
    ['Status', `<span class="${zbOk ? 'ok' : 'warn'}">${esc(zn.status || '—')}</span>`],
    zbOk && ['Channel', zn.channel],
    zbOk && ['PAN ID', hex4(zn.pan_id)],
    zn.ext_pan_id && ['Extended PAN ID', `<span class="mono">${esc(zn.ext_pan_id)}</span>`],
    zn.eui64 && ['Coordinator', `<span class="mono">${esc(zn.eui64)}</span>`],
    zn.ezsp_version && ['EZSP / stack', `v${zn.ezsp_version} / ${esc(zn.stack_version)}`],
    ['Updated', ago(zb.updated)],
  ]);
  const sock = `socket://${net.ip || '<hub-ip>'}:8888`;
  $('zb-ha').innerHTML = `<div>${dot(ha.zha ? 'ok' : 'bad')}${ha.zha ? `connected from <span class="mono">${esc(ha.zha_client)}</span> · ${ago(ha.zha_since)}` : 'not connected'}</div>
    <p class="note">In Home Assistant add <b>Zigbee Home Automation</b>, radio type <b>EZSP</b>, serial port path:</p>
    <div class="copy"><code class="mono">${esc(sock)}</code><button data-copy="${esc(sock)}">Copy</button></div>`;
  $('zb-count').textContent = devices.length ? devices.length + ' seen' : '';
  $('zb-devices').innerHTML = devices.length ? `<table><tr><th>IEEE address</th><th>NWK</th><th>LQI</th><th>RSSI</th><th>Seen</th></tr>${
    devices.map((d) => `<tr><td class="mono">${d.eui64 ? esc(d.eui64) : '<span class="off">unknown</span>'}</td><td class="mono">${hex4(d.short_id)}</td>
      <td class="${d.lqi >= 170 ? 'ok' : d.lqi >= 85 ? 'warn' : 'off'}">${d.lqi}</td><td>${d.rssi ? d.rssi + ' dBm' : '—'}</td><td>${ago(d.last_seen)}</td></tr>`).join('')}</table>`
    : '<div class="empty">No devices seen yet.</div>';

  // Bluetooth tab
  $('bt-info').innerHTML = rows([
    ['Proxy', `${dot(btOk ? 'ok' : 'bad')}${esc(s.bluetooth.proxy)}${btOk ? ' · tcp 6053' : ''}`],
    bt.name && ['ESPHome name', `<span class="mono">${esc(bt.name)}</span>`],
    bt.bluetooth_mac && ['Bluetooth MAC', `<span class="mono">${esc(bt.bluetooth_mac)}</span>`],
    bt.controller && ['Controller', esc(bt.controller)],
    btOk && ['Devices (5 min)', bt.devices_5min],
    btOk && ['Advertisements', bt.advertisements],
  ]);
  $('bt-ha').innerHTML = `<div>${dot(ha.esphome_clients ? 'ok' : 'bad')}${ha.esphome_clients ? 'connected' : 'not connected'}</div>
    <p class="note">Home Assistant discovers the proxy as an ESPHome device${bt.name ? ` named <b>${esc(bt.name)}</b>` : ''}. If it does not, add ESPHome manually: host <b>${esc(net.ip)}</b>, port <b>6053</b>, no encryption key. Sensors near the hub then show up under Discovered.</p>`;

  // Network tab
  $('net-info').innerHTML = rows([
    ['IP address', esc(net.ip || '—')],
    ['Wi-Fi network', esc(net.ssid || '—')],
    net.rssi && ['Signal', `${net.rssi} dBm`],
    ['MAC', `<span class="mono">${esc(net.mac)}</span>`],
    ['Ethernet', net.ethernet_link ? '<span class="ok">link up</span>' : '<span class="off">no link</span>'],
    ['NTP', net.ntp ? '<span class="ok">running</span>' : '<span class="off">stopped</span>'],
  ]);

  // System tab
  $('sys-info').innerHTML = rows([
    ['Running', sys.userland ? '<span class="ok">OpenHub2 userland (slot A)</span>' : '<span class="warn">Xiaomi stock (slot B)</span>'],
    sys.can_userland && ['Next boot', esc(sys.next_boot)],
    sys.image && ['Image', `<span class="mono">${esc(sys.image)}</span>`],
    sys.firmware && ['Stock firmware', esc(sys.firmware)],
    !sys.userland && ['Mode', esc(sys.mode)],
    !sys.userland && ['Stock Zigbee', esc(sys.stock_zigbee)],
    ['Uptime', uptime(sys.uptime_s)],
  ]);
  const acts = [];
  if (sys.userland) acts.push(`<button data-system="stock">Switch to stock</button>`);
  else if (sys.can_userland) acts.push(`<button class="primary" data-system="openhub2">Boot OpenHub2</button>`);
  if (!sys.userland) acts.push(sys.mode === 'xiaomi' ? `<button data-mode="openhub2">Switch to OpenHub2 mode</button>` : `<button data-mode="xiaomi">Switch to Xiaomi mode</button>`);
  $('sys-actions').innerHTML = acts.join('');

  const led = s.led;
  $('led-card').hidden = !led;
  if (led) {
    const color = { green: '#22c55e', blue: '#3b82f6', cyan: '#06b6d4', purple: '#a855f7', orange: '#f97316', white: '#f4f4f5', red: '#ef4444', off: '#3f3f46' };
    $('led').innerHTML = led.colors.map((c) => `<button class="swatch${c === led.color ? ' on' : ''}" data-led="${esc(c)}" title="${esc(c)}" style="background:${color[c] || '#888'}"></button>`).join('');
  }
}

async function loadStatus() {
  try {
    render(await api('/api/status'));
    $('live').classList.add('on');
  } catch (e) {
    $('live').classList.remove('on');
  }
}

let btDevices = [];
async function loadBluetooth() {
  try { btDevices = await api('/api/bluetooth'); } catch (e) { btDevices = []; }
  renderBluetooth();
}
function renderBluetooth() {
  const f = $('bt-filter').value.trim().toLowerCase();
  const list = btDevices.filter((d) => !f || [d.address, d.name, d.manufacturer, d.services].join(' ').toLowerCase().includes(f))
    .sort((a, b) => (!!b.name - !!a.name) || (b.rssi - a.rssi));
  $('bt-devices').innerHTML = list.length ? `<table><tr><th>Address</th><th>Name / type</th><th>RSSI</th><th>Seen</th></tr>${
    list.map((d) => `<tr><td class="mono">${esc(d.address)}${d.address_type === 1 ? ' <span class="off">rnd</span>' : ''}</td>
      <td>${d.name ? `<b>${esc(d.name)}</b> ` : ''}${d.services ? `<span class="tag ok">${esc(d.services)}</span>` : ''}${d.manufacturer ? `<span class="tag">${esc(d.manufacturer)}</span>` : ''}</td>
      <td class="${d.rssi > -70 ? 'ok' : d.rssi > -85 ? 'warn' : 'off'}">${d.rssi}</td><td>${ago(d.last_seen)}</td></tr>`).join('')}</table>`
    : '<div class="empty">No devices heard yet.</div>';
}
$('bt-filter').addEventListener('input', renderBluetooth);

// --- logs ----------------------------------------------------------------------

async function loadLog() {
  const sel = $('log-name');
  if (!sel.options.length) {
    const l = await api('/api/logs');
    sel.innerHTML = l.logs.map((n) => `<option>${esc(n)}</option>`).join('');
    sel.value = 'zbbridge';
  }
  try { $('log').textContent = await api('/api/logs?name=' + encodeURIComponent(sel.value)); } catch (e) { $('log').textContent = e.message; }
}
$('log-name').addEventListener('change', loadLog);
$('log-refresh').addEventListener('click', loadLog);

// --- actions -------------------------------------------------------------------

async function waitForReboot(msg) {
  $('overlay-msg').textContent = msg || 'Waiting for the hub to come back.';
  $('overlay').classList.add('on');
  await new Promise((r) => setTimeout(r, 15000));
  const start = Date.now();
  for (;;) {
    try { await api('/api/status'); location.reload(); return; } catch (e) { /* still down */ }
    if (Date.now() - start > 300000) {
      $('overlay-msg').textContent = 'The hub has not answered for 5 minutes. If it changed network or address, look for it there; on stock, Wi-Fi can take 3 minutes.';
    }
    await new Promise((r) => setTimeout(r, 3000));
  }
}

document.addEventListener('click', async (e) => {
  const t = e.target.closest('button');
  if (!t) return;
  try {
    if (t.dataset.copy) {
      await navigator.clipboard.writeText(t.dataset.copy); toast('Copied');
    } else if (t.dataset.led) {
      await api('/api/led', { color: t.dataset.led }); loadStatus();
    } else if (t.dataset.system) {
      const to = t.dataset.system;
      if (!confirm(to === 'stock' ? 'Boot the stock Xiaomi system? The hub reboots. Three button presses do the same.' : 'Boot OpenHub2? The hub reboots; a failed boot falls back to stock.')) return;
      await api('/api/system', { to }); waitForReboot();
    } else if (t.dataset.mode) {
      if (!confirm(`Switch to ${t.dataset.mode} mode? The hub reboots.`)) return;
      await api('/api/mode', { mode: t.dataset.mode }); waitForReboot();
    } else if (t.id === 'reboot') {
      if (!confirm('Reboot the hub?')) return;
      await api('/api/reboot', {}); waitForReboot();
    } else if (t.id === 'zb-refresh') {
      await api('/api/zigbee/refresh', {}); toast('Refresh requested'); setTimeout(loadStatus, 1500);
    } else if (t.id === 'wifi-scan') {
      t.disabled = true; t.textContent = 'Scanning…';
      const list = await api('/api/wifi/scan');
      $('wifi-list').innerHTML = list.length ? list.map((n) => `<div class="wifi-item" data-ssid="${esc(n.ssid)}"><span>${n.secure ? '🔒 ' : ''}${esc(n.ssid)}</span><span class="sub">${n.signal} dBm · ${n.mhz > 4000 ? '5' : '2.4'} GHz</span></div>`).join('') : '<div class="empty">No networks found.</div>';
    }
  } catch (err) {
    toast(err.message);
  } finally {
    if (t.id === 'wifi-scan') { t.disabled = false; t.textContent = 'Scan networks'; }
  }
});
$('wifi-list').addEventListener('click', (e) => {
  const it = e.target.closest('.wifi-item'); if (!it) return;
  $('ssid').value = it.dataset.ssid; $('password').focus();
});
$('wifi-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  if (!confirm(`Connect to "${$('ssid').value}"? The hub drops off this network if the new one is different.`)) return;
  try { toast((await api('/api/wifi', { ssid: $('ssid').value, password: $('password').value })).message); }
  catch (err) { toast(err.message); }
});

// --- start ---------------------------------------------------------------------

showTab();
loadStatus().then(loadHistory);
setInterval(loadStatus, 5000);
setInterval(loadHistory, 30000);
setInterval(() => { if (location.hash === '#bluetooth') loadBluetooth(); }, 10000);
