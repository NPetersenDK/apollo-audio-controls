const $ = id => document.getElementById(id);
const logContainer = $('log');

// Prefix for every API call, so the app still works when the service is
// mounted under a path prefix behind a reverse proxy (e.g. /apollo).
const BASE = window.APOLLO_BASE || '';

// The session lives exactly as long as this EventSource.
let es = null;
let cfg = null;
let state = { session: false };
let pendingGain = null; // the slider's value until it has been sent

// --- small helpers ---

function esc(s) {
  return (s == null ? '' : String(s)).replace(/[&<>"']/g, c =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function nowTs() {
  return new Date().toLocaleTimeString('en-GB', { hour12: false });
}

function appendLog(message, level, ts) {
  const entry = document.createElement('div');
  entry.className = 'log-entry log-' + (level || 'debug');
  entry.textContent = '[' + (ts || nowTs()) + '] ' + message;
  logContainer.appendChild(entry);
  logContainer.scrollTop = logContainer.scrollHeight;
  while (logContainer.childElementCount > 500) logContainer.removeChild(logContainer.firstChild);
}
$('clearBtn').addEventListener('click', () => logContainer.innerHTML = '');

function showToast(message, kind = 'danger', title = 'Error') {
  const host = $('toastContainer');
  if (!host) return;
  const id = 'toast-' + Date.now() + '-' + Math.floor(Math.random() * 1000);
  const cls = kind === 'warning' ? 'text-bg-warning' : (kind === 'success' ? 'text-bg-success' : 'text-bg-danger');
  host.insertAdjacentHTML('beforeend', `<div id="${id}" class="toast ${cls} border-0" role="alert" aria-live="assertive" aria-atomic="true">
    <div class="d-flex">
      <div class="toast-body"><strong>${esc(title)}:</strong> ${esc(message)}</div>
      <button type="button" class="btn-close btn-close-white me-2 m-auto" data-bs-dismiss="toast" aria-label="Close"></button>
    </div>
  </div>`);
  const el = $(id);
  const t = new bootstrap.Toast(el, { delay: 5500 });
  el.addEventListener('hidden.bs.toast', () => el.remove());
  t.show();
}

async function apiJSON(url, opts = {}, actionLabel = 'Request') {
  let res;
  try {
    res = await fetch(url, opts);
  } catch (err) {
    showToast(err?.message || 'Network error', 'danger', actionLabel);
    throw err;
  }
  const payload = await res.json().catch(() => ({}));
  if (!res.ok) {
    showToast(payload.error || `${res.status} ${res.statusText}`, 'danger', actionLabel);
    throw new Error(payload.error || res.statusText);
  }
  return payload;
}

function post(path, body, label) {
  return apiJSON(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body || {})
  }, label);
}

// --- connect / disconnect ---

function connect() {
  if (es) return;
  const ip = $('deviceInput').value.trim();
  if (!ip) {
    showToast('Enter the IP address of the e1x first', 'warning', 'Connect');
    return;
  }
  localStorage.setItem('e1xDevice', ip);
  appendLog('connecting to ' + ip, 'debug');
  es = new EventSource(BASE + '/api/events?device=' + encodeURIComponent(ip));
  es.onmessage = ev => {
    let msg;
    try { msg = JSON.parse(ev.data); } catch { return; }
    if (msg.type === 'hello') {
      if (!cfg) cfg = msg.config;
      state = msg.state;
      if (state.device) $('deviceInput').value = state.device;
      if (state.session) appendLog('connected to ' + state.device, 'ok');
      buildFlagButtons();
      render();
      // Connect failed: let the error line arrive, then drop back.
      if (!state.session) setTimeout(() => { if (es && !state.session) disconnect(); }, 400);
    } else if (msg.type === 'state') {
      state = msg.state;
      render();
    } else if (msg.type === 'log') {
      appendLog(msg.msg, msg.level, msg.ts);
    }
  };
  es.onerror = () => {
    if (state.session) appendLog('the connection to the server dropped', 'warn');
    state = Object.assign({}, state, { session: false });
    render();
  };
}

function disconnect(reason) {
  if (!es) return;
  es.close();
  es = null;
  state = Object.assign({}, state, { session: false });
  if (reason !== false) appendLog(reason || 'disconnected', 'warn');
  render();
}

$('connectBtn').addEventListener('click', () => (es ? disconnect() : connect()));
$('deviceInput').addEventListener('keydown', ev => { if (ev.key === 'Enter' && !es) connect(); });
window.addEventListener('beforeunload', () => { if (es) es.close(); });

// --- rendering ---

function buildFlagButtons() {
  const host = $('flagButtons');
  if (!cfg || host.childElementCount) return;
  $('gainRange').min = cfg.gain_min;
  $('gainRange').max = cfg.gain_max;
  $('gainPending').textContent = `${cfg.gain_min}-${cfg.gain_max} dB, 1 dB per step`;
  cfg.flags.forEach(f => {
    const btn = document.createElement('button');
    btn.className = 'btn btn-sm btn-outline-secondary flag-btn';
    btn.id = 'flag-' + f.name;
    btn.title = f.hint || '';
    btn.innerHTML = `${esc(f.label)}<small>0x${f.mask.toString(16).padStart(2, '0')}</small>`;
    btn.addEventListener('click', () => onFlagClick(f));
    host.appendChild(btn);
  });
}

function flagOn(f) {
  return state.have_flags && (state.flags & f.mask) !== 0;
}

function render() {
  const live = !!state.session;

  const pill = $('sessionPill');
  pill.className = 'status-pill ' + (live ? 'status-pill-online' : 'status-pill-offline');
  pill.innerHTML = live
    ? '<i class="fa-solid fa-plug-circle-check"></i> connected'
    : '<i class="fa-solid fa-plug-circle-xmark"></i> disconnected';

  const btn = $('connectBtn');
  btn.className = 'btn btn-sm ' + (es ? 'btn-outline-warning' : 'btn-success');
  btn.innerHTML = es
    ? '<i class="fa-solid fa-link-slash"></i> Disconnect'
    : '<i class="fa-solid fa-link"></i> Connect';
  $('deviceInput').disabled = !!es;

  $('offlineBanner').classList.toggle('hidden', live);

  const inp = $('inputPill');
  if (!state.have_flags) {
    inp.className = 'status-pill status-pill-pending';
    $('inputText').textContent = 'input unknown';
  } else if (state.plugged) {
    inp.className = 'status-pill status-pill-online';
    $('inputText').textContent = 'XLR connected';
  } else {
    inp.className = 'status-pill status-pill-offline';
    $('inputText').textContent = 'nothing plugged in';
  }

  // Gain
  const shown = pendingGain != null ? pendingGain : (state.have_gain ? state.gain_db : null);
  $('gainValue').textContent = shown != null ? shown : '-';
  $('gainValue').parentElement.classList.toggle('stale', !live && state.have_gain);
  if (pendingGain != null && (!state.have_gain || pendingGain !== state.gain_db)) {
    $('gainMeta').textContent = 'not sent yet';
  } else if (state.have_gain) {
    $('gainMeta').textContent = 'reported ' + state.gain_at;
  } else {
    $('gainMeta').textContent = 'not read';
  }
  if (shown != null && document.activeElement !== $('gainRange')) $('gainRange').value = shown;

  // Switches
  $('flagsBadge').textContent = state.have_flags
    ? '0x' + state.flags.toString(16).padStart(2, '0') : '-';
  (cfg?.flags || []).forEach(f => {
    const b = $('flag-' + f.name);
    if (!b) return;
    const on = flagOn(f);
    let variant = 'btn-outline-secondary';
    if (on) variant = f.danger ? 'btn-danger' : (f.name === 'mute' ? 'btn-warning' : 'btn-success');
    b.className = 'btn btn-sm flag-btn ' + variant + (live ? '' : ' stale');
    b.disabled = !live || (f.danger && cfg.lock_48v && !on);
  });

  // Info tiles
  $('infoDetect').textContent = state.have_flags
    ? (state.plugged ? 'connected (0x' + state.detect.toString(16) + ')' : 'nothing plugged in (0x' + state.detect.toString(16) + ')')
    : '-';
  $('infoFlags').textContent = state.have_flags
    ? '0x' + state.flags.toString(16).padStart(2, '0') + '  cap=0x' + state.cap.toString(16) : '-';
  $('infoSeen').textContent = state.flags_at || '-';

  ['gainRange', 'gainApply', 'gainUp', 'gainDown', 'refreshBtn'].forEach(id => {
    $(id).disabled = !live;
  });
}

// --- actions ---

$('refreshBtn').addEventListener('click', async () => {
  try { await post(BASE + '/api/refresh', {}, 'Refresh'); } catch { /* shown as a toast */ }
});

$('gainRange').addEventListener('input', () => {
  pendingGain = parseInt($('gainRange').value, 10);
  $('gainValue').textContent = pendingGain;
  $('gainMeta').textContent = 'not sent yet';
});

// The slider sends on release, not while dragging.
$('gainRange').addEventListener('change', () => sendGain(parseInt($('gainRange').value, 10)));
$('gainApply').addEventListener('click', () => sendGain(parseInt($('gainRange').value, 10)));
$('gainUp').addEventListener('click', () => stepGain(+1));
$('gainDown').addEventListener('click', () => stepGain(-1));

function stepGain(delta) {
  const base = pendingGain != null ? pendingGain : (state.have_gain ? state.gain_db : 40);
  const next = Math.min(cfg.gain_max, Math.max(cfg.gain_min, base + delta));
  $('gainRange').value = next;
  sendGain(next);
}

async function sendGain(db) {
  pendingGain = db;
  render();
  try {
    await post(BASE + '/api/gain', { db }, 'Set gain');
    pendingGain = null;
    render();
  } catch {
    // The device did not confirm -- keep the value so it can be retried.
  }
}

let phantomModal = null;

function onFlagClick(f) {
  const on = !flagOn(f);
  if (f.danger && on) {
    phantomModal = phantomModal || new bootstrap.Modal($('phantomModal'));
    phantomModal.show();
    return;
  }
  sendFlag(f, on, false);
}

$('phantomConfirm').addEventListener('click', () => {
  phantomModal?.hide();
  const f = cfg.flags.find(x => x.danger);
  if (f) sendFlag(f, true, true);
});

async function sendFlag(f, on, yes) {
  try {
    await post(BASE + '/api/flag', { name: f.name, on, yes }, f.label);
  } catch {
    // Already shown; the device's own report decides what is displayed.
  }
}

// Prefill from the server; nothing reaches the device until Connect.
(async function init() {
  cfg = await apiJSON(BASE + '/api/config', {}, 'Startup').catch(() => null);
  if (!cfg) return;
  $('deviceInput').value = localStorage.getItem('e1xDevice') || cfg.device;
  buildFlagButtons();
  state = await apiJSON(BASE + '/api/state', {}, 'Startup').catch(() => ({ session: false }));
  state.session = false;
  render();
})();
