'use strict';

const state = {
  snapshot: null,
  status: null,
  targets: [],
  selectedRegion: null,
  maps: {},
  replayPlaying: true,
};

// Maps are built on first use so the land outline is only fetched by someone
// who actually opens a page showing one.
function mapFor(id, containerId) {
  if (!state.maps[id]) {
    const container = $(containerId);
    if (!container) return null;
    const worldMap = new WorldMap(container);
    worldMap.onRegionClick = (regionId) => {
      state.selectedRegion = regionId;
      showPage('regions');
    };
    state.maps[id] = worldMap;
  }
  return state.maps[id];
}

const $ = (id) => document.getElementById(id);

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined && text !== null) node.textContent = String(text);
  return node;
}

function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}

function fmtMs(value, digits = 1) {
  if (value === null || value === undefined) return '—';
  return `${value.toFixed(digits)} ms`;
}

function fmtPct(ratio, digits = 2) {
  if (ratio === null || ratio === undefined) return '—';
  return `${(ratio * 100).toFixed(digits)} %`;
}

function fmtScore(value) {
  if (value === null || value === undefined) return '--';
  return Number.isInteger(value) ? String(value) : value.toFixed(1);
}

function fmtTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  return d.toLocaleString(undefined, { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

function relative(iso) {
  if (!iso) return 'never';
  const seconds = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h ago`;
  return `${Math.round(seconds / 86400)}d ago`;
}

async function api(path, options) {
  const response = await fetch(path, options);
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const body = await response.json();
      if (body && body.error) message = body.error;
    } catch (_) { /* keep the status line */ }
    throw new Error(message);
  }
  return response.json();
}

function showBanner(message, tone) {
  const banner = $('banner');
  if (!message) {
    banner.hidden = true;
    return;
  }
  banner.textContent = message;
  banner.hidden = false;
  banner.style.borderColor = tone === 'error' ? 'var(--down)' : 'var(--fair)';
}

/* ---------- navigation ---------- */

// The dashboard is one page with tabs, so the current view has to live in the
// URL: without it a reload always lands back on Overview, and there is no way
// to send someone a link to the thing you are looking at. It goes in the hash
// rather than the path because this is served as static files -- a real path
// would 404 on reload until the server learned to rewrite it.
//
//   #/regions/eu-central
//   #/routes?target=fra-s3&family=ipv4
//   #/history?target=lhr-ec2&protocol=icmp&family=ipv4&since=24h
//
// Overview with no selection keeps the URL bare, so the plain hostname stays
// the address of the dashboard.

const PAGES = ['overview', 'regions', 'routes', 'history', 'events', 'targets'];

// The controls each page restores from a link. Values are read from and written
// to the <select>s themselves, so a link reproduces the exact query.
const PAGE_FILTERS = {
  routes: { target: 'route-target', family: 'route-family' },
  history: {
    target: 'history-target',
    protocol: 'history-protocol',
    family: 'history-family',
    since: 'history-since',
  },
  events: { type: 'event-type', since: 'event-since' },
};

const pageLoaders = {};

function activePage() {
  const active = document.querySelector('.page.active');
  return active ? active.id.replace(/^page-/, '') : 'overview';
}

function parseHash() {
  const [path, query] = location.hash.replace(/^#\/?/, '').split('?');
  const segments = path.split('/').filter(Boolean).map(decodeURIComponent);
  return {
    page: PAGES.includes(segments[0]) ? segments[0] : 'overview',
    detail: segments[1] || null,
    params: new URLSearchParams(query || ''),
  };
}

function currentHash() {
  const page = activePage();
  if (page === 'overview') return '';

  let path = page;
  if (page === 'regions' && state.selectedRegion) path += `/${encodeURIComponent(state.selectedRegion)}`;

  const params = new URLSearchParams();
  for (const [key, id] of Object.entries(PAGE_FILTERS[page] || {})) {
    const value = $(id).value;
    if (value) params.set(key, value);
  }
  const query = params.toString();
  return `#/${path}${query ? `?${query}` : ''}`;
}

// syncHash rewrites the address bar to match what is on screen. pushState is
// used rather than assigning location.hash so that no hashchange fires back at
// us; `replace` is for changes that are adjustments rather than navigation --
// stepping Back through every dropdown fiddle would be useless.
function syncHash({ replace = false } = {}) {
  const hash = currentHash();
  const url = hash || location.pathname + location.search;
  if (hash === location.hash) return;
  if (replace) history.replaceState(null, '', url);
  else history.pushState(null, '', url);
}

// applyHash is the other direction: the URL wins. It runs on load and whenever
// the history moves, so Back and Forward step through views the way they do on
// a site with real pages.
function applyHash() {
  const { page, detail, params } = parseHash();

  if (page === 'regions') state.selectedRegion = detail;
  for (const [key, id] of Object.entries(PAGE_FILTERS[page] || {})) {
    const value = params.get(key);
    if (value === null) continue;
    const select = $(id);
    // A link naming a target this deployment does not have would otherwise
    // select nothing and silently widen the query to everything.
    if ([...select.options].some((option) => option.value === value)) select.value = value;
  }

  showPage(page, { url: false });
  // Normalise: a link can name a filter this deployment does not have, and the
  // address bar should say what is actually on screen rather than what was
  // asked for.
  syncHash({ replace: true });
}

function showPage(name, { url = true, replace = false } = {}) {
  document.querySelectorAll('.tab').forEach((tab) => tab.classList.toggle('active', tab.dataset.page === name));
  document.querySelectorAll('.page').forEach((page) => page.classList.toggle('active', page.id === `page-${name}`));
  // A map on a hidden page must stop animating; nothing else notices the page
  // switch, and an off-screen replay would burn a frame budget for nobody.
  for (const worldMap of Object.values(state.maps)) worldMap.syncAnimation();
  if (url) syncHash({ replace });
  if (pageLoaders[name]) pageLoaders[name]();
}

$('tabs').addEventListener('click', (event) => {
  const tab = event.target.closest('.tab');
  if (tab) showPage(tab.dataset.page);
});

// popstate covers Back and Forward; hashchange covers someone typing a hash
// into the address bar, which pushState navigation never triggers.
window.addEventListener('popstate', applyHash);
window.addEventListener('hashchange', applyHash);

/* ---------- overview ---------- */

function renderSnapshot(snapshot) {
  state.snapshot = snapshot;

  $('global-score').textContent = fmtScore(snapshot.global_score);
  $('global-score').className = `score-value text-${snapshot.global_status || 'unknown'}`;
  $('global-status').textContent = snapshot.global_status || 'unknown';

  if (snapshot.local_score !== undefined && snapshot.local_score !== null) {
    $('local-score').textContent = fmtScore(snapshot.local_score);
    $('local-score').className = `score-value text-${snapshot.local_status || 'unknown'}`;
    $('local-status').textContent = snapshot.local_status || 'unknown';
  } else {
    $('local-score').textContent = '--';
    $('local-status').textContent = 'mark a region as local';
  }

  const setFamily = (id, health) => {
    const node = $(id);
    node.textContent = health.enabled ? `${health.status} (${health.success}/${health.total})` : 'disabled';
    node.className = `stat-value text-${health.enabled ? health.status : 'disabled'}`;
  };
  setFamily('ipv4-status', snapshot.ipv4);
  setFamily('ipv6-status', snapshot.ipv6);

  $('packet-loss').textContent = fmtPct(snapshot.packet_loss_ratio);
  $('route-changes').textContent = snapshot.route_changes_today ?? 0;

  renderRegionMap(snapshot.regions || []);
  renderWorldMap(snapshot.regions || [], snapshot.origin || null);
  renderResolvers(snapshot.resolvers || []);
  renderRegionsTable(snapshot.regions || []);

  if (snapshot.stale_regions > 0) {
    showBanner(`${snapshot.stale_regions} region(s) have no recent measurements. The scheduler may still be warming up.`);
  } else {
    showBanner(null);
  }
}

function renderRegionMap(regions) {
  const container = $('region-map');
  clear(container);
  if (!regions.length) {
    container.appendChild(el('div', 'empty', 'No regions configured.'));
    return;
  }

  for (const region of regions) {
    const card = el('div', `region-card status-${region.status}`);
    card.addEventListener('click', () => {
      state.selectedRegion = region.id;
      showPage('regions');
    });

    const name = el('div', 'name');
    name.appendChild(el('span', null, region.display_name));
    name.appendChild(el('span', `score text-${region.status}`, region.has_data ? fmtScore(region.score) : '--'));
    card.appendChild(name);

    const parts = [];
    parts.push(region.rtt_ms !== undefined && region.rtt_ms !== null ? fmtMs(region.rtt_ms) : 'no rtt');
    if (region.loss_ratio !== undefined && region.loss_ratio !== null) parts.push(`loss ${fmtPct(region.loss_ratio, 1)}`);
    if (region.jitter_ms !== undefined && region.jitter_ms !== null) parts.push(`jitter ${fmtMs(region.jitter_ms)}`);
    card.appendChild(el('div', 'metrics', parts.join('  ·  ')));
    card.appendChild(el('div', 'metrics', region.updated_at ? `updated ${relative(region.updated_at)}` : 'never measured'));

    container.appendChild(card);
  }
}

// renderWorldMap plots the regions and says plainly where each dot came from.
// A position taken from the configuration is a statement of fact; one derived
// from GeoIP is an inference about where an address block is registered, and
// the caption has to say so rather than letting the two look identical.
function renderWorldMap(regions, origin) {
  const worldMap = mapFor('overview', 'overview-map');
  if (!worldMap) return;
  worldMap.setRegions(regions, origin);

  const plotted = regions.filter((r) => r.latitude !== null && r.latitude !== undefined &&
    r.longitude !== null && r.longitude !== undefined);
  const inferred = plotted.filter((r) => r.position && r.position.source === 'geoip');
  const note = $('overview-map-note');
  const missing = regions.length - plotted.length;
  const parts = [];

  if (missing === regions.length) {
    parts.push('No region could be placed on the map. Either add latitude and longitude to each region in the configuration, or enable geoip and point it at a city database so regions can be placed from the addresses their targets resolve to.');
  } else if (missing > 0) {
    parts.push(`${missing} of ${regions.length} regions could not be placed and are not shown. Add latitude and longitude to plot them.`);
  }

  if (inferred.length > 0) {
    parts.push(inferred.length === 1
      ? '1 region, shown hollow, was placed from a GeoIP database rather than configured coordinates.'
      : `${inferred.length} regions, shown hollow, were placed from a GeoIP database rather than configured coordinates.`);
    parts.push('That is where the address block is registered, which is not always where the hardware is; configured coordinates always win over it.');
  }

  if (origin && origin.source === 'geoip') {
    const place = [origin.city, origin.country_name || origin.country].filter(Boolean).join(', ');
    parts.push(`Arcs start from your public address${origin.ip ? ` (${origin.ip})` : ''}, located${place ? ` in ${place}` : ''} by GeoIP. Mark a region local: true with coordinates to set the origin yourself.`);
  } else if (!origin && plotted.length > 0) {
    parts.push('No arcs are drawn because the map has no origin: mark a region local: true and give it coordinates, or enable geoip so the public address can be located.');
  }

  note.textContent = parts.join(' ');
  note.hidden = parts.length === 0;
}

function renderResolvers(resolvers) {
  const container = $('resolver-list');
  clear(container);
  if (!resolvers.length) {
    container.appendChild(el('div', 'empty', 'No DNS resolvers configured.'));
    return;
  }
  for (const resolver of resolvers) {
    const card = el('div', 'resolver');
    card.appendChild(el('div', 'name', resolver.name));
    const status = resolver.success ? fmtMs(resolver.latency_ms) : 'failed';
    const detail = el('div', 'detail', `${resolver.resolver} · ${status}`);
    if (!resolver.success) detail.classList.add('text-down');
    card.appendChild(detail);
    container.appendChild(card);
  }
}

/* ---------- regions ---------- */

function renderRegionsTable(regions) {
  const tbody = document.querySelector('#regions-table tbody');
  clear(tbody);

  if (!regions.length) {
    const row = tbody.insertRow();
    const cell = row.insertCell();
    cell.colSpan = 10;
    cell.textContent = 'No regions configured.';
    return;
  }

  for (const region of regions) {
    const row = tbody.insertRow();
    row.className = 'clickable';
    row.addEventListener('click', () => {
      state.selectedRegion = state.selectedRegion === region.id ? null : region.id;
      syncHash({ replace: true });
      renderRegionDetail();
    });

    const cells = [
      [region.display_name, null],
      [region.has_data ? fmtScore(region.score) : '--', `num text-${region.status}`],
      [fmtMs(region.rtt_ms), 'num'],
      [fmtMs(region.jitter_ms), 'num'],
      [fmtPct(region.loss_ratio, 2), 'num'],
      [fmtMs(region.tcp_connect_ms), 'num'],
      [fmtMs(region.ttfb_ms), 'num'],
      [region.ipv4.enabled ? region.ipv4.status : 'off', `text-${region.ipv4.enabled ? region.ipv4.status : 'disabled'}`],
      [region.ipv6.enabled ? region.ipv6.status : 'off', `text-${region.ipv6.enabled ? region.ipv6.status : 'disabled'}`],
      [region.status, `text-${region.status}`],
    ];
    for (const [text, className] of cells) {
      const cell = row.insertCell();
      cell.textContent = text;
      if (className) cell.className = className;
    }
  }

  renderRegionDetail();
}

// The regions table is drawn from the snapshot, but the detail panel follows a
// selection that a link can carry, so opening #/regions/<id> has to render it.
pageLoaders.regions = () => renderRegionDetail();

function renderRegionDetail() {
  const container = $('region-detail');
  clear(container);
  if (!state.selectedRegion || !state.snapshot) return;

  const region = (state.snapshot.regions || []).find((r) => r.id === state.selectedRegion);
  if (!region) return;

  container.appendChild(el('h2', null, `${region.display_name} — targets`));

  for (const target of region.targets || []) {
    const card = el('div', 'route');
    const head = el('div', 'route-head');
    head.appendChild(el('strong', null, `${target.target} (${target.hostname})`));
    head.appendChild(el('span', `fp text-${target.status}`, target.has_data ? `${fmtScore(target.score)} / 100` : 'no data'));
    card.appendChild(head);

    for (const family of target.families || []) {
      const line = el('div', 'hop');
      line.appendChild(el('span', 'ttl', family.address_family === 'ipv6' ? 'v6' : 'v4'));

      const details = [];
      details.push(family.reachable ? 'reachable' : 'unreachable');
      if (family.rtt_ms !== null && family.rtt_ms !== undefined) details.push(`rtt ${fmtMs(family.rtt_ms)}`);
      if (family.baseline_rtt_ms !== null && family.baseline_rtt_ms !== undefined) details.push(`baseline ${fmtMs(family.baseline_rtt_ms)}`);
      if (family.loss_ratio !== null && family.loss_ratio !== undefined) details.push(`loss ${fmtPct(family.loss_ratio, 1)}`);
      if (family.jitter_ms !== null && family.jitter_ms !== undefined) details.push(`jitter ${fmtMs(family.jitter_ms)}`);
      if (family.tls_ms !== null && family.tls_ms !== undefined) details.push(`tls ${fmtMs(family.tls_ms)}`);
      if ((family.penalties || []).length) {
        details.push(`− ${family.penalties.map((p) => `${p.reason} (${p.points})`).join(', ')}`);
      }
      line.appendChild(el('span', null, details.join('  ·  ')));
      line.appendChild(el('span', null, fmtScore(family.score)));
      card.appendChild(line);
    }
    container.appendChild(card);
  }
}

/* ---------- events ---------- */

function renderEvents(container, events) {
  clear(container);
  if (!events.length) {
    container.appendChild(el('div', 'empty', 'No events recorded. That is good news.'));
    return;
  }
  for (const event of events) {
    const node = el('div', `event severity-${event.severity}`);
    node.appendChild(el('span', 'time', fmtTime(event.timestamp)));
    node.appendChild(el('span', 'type', event.type.replace(/_/g, ' ')));
    node.appendChild(el('span', null, event.message));
    container.appendChild(node);
  }
}

pageLoaders.events = async () => {
  const container = $('events-content');
  const params = new URLSearchParams({ limit: '200' });
  const since = $('event-since').value;
  if (since) params.set('since', since);
  const type = $('event-type').value;
  if (type) params.set('type', type);

  try {
    renderEvents(container, await api(`/api/v1/events?${params}`));
  } catch (error) {
    clear(container);
    container.appendChild(el('div', 'empty', `Failed to load events: ${error.message}`));
  }
};

/* ---------- routes ---------- */

function resetReplayReadout() {
  $('replay-readout').textContent = '—';
  $('replay-fill').style.width = '0%';
}

// updateReplayReadout is called on every animation frame, so it touches the DOM
// only when a value actually changed.
let lastReadout = '';
function updateReplayReadout(progress) {
  const fill = $('replay-fill');
  const readout = $('replay-readout');
  if (!fill || !readout) return;

  const pct = progress.totalMs ? (progress.cumulativeMs / progress.totalMs) * 100 : 0;
  fill.style.width = `${Math.max(0, Math.min(100, pct)).toFixed(1)}%`;

  const hop = progress.hop;
  const where = hop && hop.geo
    ? [hop.geo.city, hop.geo.country_name || hop.geo.country].filter(Boolean).join(', ')
    : '';
  const text = hop
    ? `hop ${hop.hop} · ${where || hop.ip || '*'} · +${progress.legMs.toFixed(1)} ms · ${progress.cumulativeMs.toFixed(1)} ms total`
    : '—';
  if (text !== lastReadout) {
    readout.textContent = text;
    lastReadout = text;
  }
}

// renderRouteMap plots the most recent trace and replays it as an animation. It
// is deliberately explicit about how much of the path it could not place: an
// unlabelled gap would read as a route that went nowhere, and a confidently
// drawn line through a mislocated backbone router is worse than no line at all.
function renderRouteMap(route) {
  const worldMap = mapFor('routes', 'route-map');
  if (!worldMap) return;
  const note = $('route-map-note');

  const hops = (route && route.hops) || [];
  const located = hops.filter((h) => h.geo && h.geo.latitude !== null && h.geo.latitude !== undefined &&
    h.geo.longitude !== null && h.geo.longitude !== undefined);
  worldMap.setRegions(state.snapshot ? state.snapshot.regions || [] : [], state.snapshot ? state.snapshot.origin || null : null);
  worldMap.onProgress = updateReplayReadout;
  worldMap.setRoute(located.length ? located : null);
  worldMap.setPlaying(state.replayPlaying);
  if (!located.length) resetReplayReadout();

  if (!hops.length) {
    note.textContent = 'No trace to plot yet.';
    note.hidden = false;
    return;
  }
  if (!located.length) {
    note.textContent = 'None of these hops could be placed on the map. Enable geoip in the configuration and point it at a city database to locate them.';
    note.hidden = false;
    return;
  }

  const parts = [`Showing ${located.length} of ${hops.length} hops.`];
  const unplaced = hops.length - located.length;
  if (unplaced > 0) {
    parts.push(`${unplaced} could not be located (private addresses, hops that did not answer, or addresses missing from the database); dashed segments span those gaps.`);
  }
  const approximate = located.filter((h) => h.geo.confidence === 'low').length;
  if (approximate > 0) {
    parts.push(approximate === 1
      ? '1 hop, shown hollow, is a country-level guess only.'
      : `${approximate} hops, shown hollow, are country-level guesses only.`);
  }
  parts.push('Positions come from a GeoIP database, not from the network itself, and backbone routers often resolve to where their address block is registered rather than where the hardware is.');
  note.textContent = parts.join(' ');
  note.hidden = false;
}

function renderRoutes(routes) {
  const container = $('routes-content');
  clear(container);
  if (!routes.length) {
    renderRouteMap(null);
    container.appendChild(el('div', 'empty', 'No routes recorded yet. Route tests need a raw ICMP socket (CAP_NET_RAW).'));
    return;
  }
  renderRouteMap(routes[0]);

  // Group by target and family so repeated traces collapse into one card each.
  const seen = new Set();
  for (const route of routes) {
    const key = `${route.target}|${route.address_family}`;
    if (seen.has(key)) continue;
    seen.add(key);

    const card = el('div', 'route');
    const head = el('div', 'route-head');
    head.appendChild(el('strong', null, `${route.target} · ${route.address_family}`));
    head.appendChild(el('span', 'fp', `${route.hop_count} hops · ${route.complete ? 'complete' : 'incomplete'} · fingerprint ${route.fingerprint} · ${relative(route.timestamp)}`));
    card.appendChild(head);

    for (const hop of route.hops || []) {
      const line = el('div', 'hop');
      line.appendChild(el('span', 'ttl', hop.hop));
      line.appendChild(el('span', hop.ip ? null : 'silent', hop.ip || '* no reply'));
      line.appendChild(el('span', null, hop.rtt_ms !== null && hop.rtt_ms !== undefined ? fmtMs(hop.rtt_ms, 2) : ''));
      if (hop.geo) {
        const where = [hop.geo.city, hop.geo.country_name || hop.geo.country].filter(Boolean).join(', ');
        const bits = [];
        if (where) bits.push(hop.geo.confidence === 'low' ? `${where} (approx)` : where);
        if (hop.geo.org) bits.push(hop.geo.org);
        else if (hop.geo.asn) bits.push(`AS${hop.geo.asn}`);
        if (bits.length) line.appendChild(el('span', 'hop-geo', bits.join(' · ')));
      }
      card.appendChild(line);
    }
    container.appendChild(card);
  }
}

pageLoaders.routes = async () => {
  const params = new URLSearchParams({ limit: '100' });
  const target = $('route-target').value;
  if (target) params.set('target', target);
  const family = $('route-family').value;
  if (family) params.set('address_family', family);

  try {
    renderRoutes(await api(`/api/v1/routes?${params}`));
  } catch (error) {
    const container = $('routes-content');
    clear(container);
    container.appendChild(el('div', 'empty', `Failed to load routes: ${error.message}`));
  }
};

/* ---------- history charts ---------- */

const SVG_NS = 'http://www.w3.org/2000/svg';

function svgEl(name, attrs) {
  const node = document.createElementNS(SVG_NS, name);
  for (const [key, value] of Object.entries(attrs || {})) node.setAttribute(key, value);
  return node;
}

function drawChart(title, points, unit) {
  const card = el('div', 'chart');
  card.appendChild(el('h3', null, title));

  const values = points.filter((p) => p.value !== null && p.value !== undefined);
  if (!values.length) {
    card.appendChild(el('div', 'empty', 'No data in this window.'));
    return card;
  }

  const width = 800;
  const height = 160;
  const padding = { top: 10, right: 8, bottom: 18, left: 46 };
  const plotWidth = width - padding.left - padding.right;
  const plotHeight = height - padding.top - padding.bottom;

  const times = points.map((p) => p.time);
  const minTime = Math.min(...times);
  const maxTime = Math.max(...times);
  const timeSpan = maxTime - minTime || 1;

  let minValue = Math.min(...values.map((p) => p.value));
  let maxValue = Math.max(...values.map((p) => p.value));
  if (maxValue === minValue) { maxValue += 1; minValue = Math.max(0, minValue - 1); }
  const valueSpan = maxValue - minValue;

  const x = (t) => padding.left + ((t - minTime) / timeSpan) * plotWidth;
  const y = (v) => padding.top + plotHeight - ((v - minValue) / valueSpan) * plotHeight;

  const svg = svgEl('svg', { viewBox: `0 0 ${width} ${height}`, preserveAspectRatio: 'none' });

  for (let i = 0; i <= 2; i++) {
    const value = minValue + (valueSpan * i) / 2;
    const lineY = y(value);
    svg.appendChild(svgEl('line', { class: 'grid-line', x1: padding.left, x2: width - padding.right, y1: lineY, y2: lineY }));
    const label = svgEl('text', { class: 'axis', x: 4, y: lineY + 3 });
    label.textContent = value.toFixed(value < 10 ? 2 : 0);
    svg.appendChild(label);
  }

  // Failures are drawn as vertical marks so an outage is visible even though it
  // has no value to plot.
  for (const point of points) {
    if (point.failed) {
      svg.appendChild(svgEl('line', { class: 'fail', x1: x(point.time), x2: x(point.time), y1: padding.top, y2: padding.top + plotHeight }));
    }
  }

  const path = values.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p.time).toFixed(1)},${y(p.value).toFixed(1)}`).join(' ');
  const areaPath = `${path} L${x(values[values.length - 1].time).toFixed(1)},${(padding.top + plotHeight).toFixed(1)} L${x(values[0].time).toFixed(1)},${(padding.top + plotHeight).toFixed(1)} Z`;
  svg.appendChild(svgEl('path', { class: 'area', d: areaPath }));
  svg.appendChild(svgEl('path', { class: 'series', d: path }));

  const startLabel = svgEl('text', { class: 'axis', x: padding.left, y: height - 5 });
  startLabel.textContent = new Date(minTime).toLocaleString(undefined, { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' });
  svg.appendChild(startLabel);

  const endLabel = svgEl('text', { class: 'axis', x: width - padding.right, y: height - 5, 'text-anchor': 'end' });
  endLabel.textContent = new Date(maxTime).toLocaleString(undefined, { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' });
  svg.appendChild(endLabel);

  card.appendChild(svg);

  const last = values[values.length - 1];
  const summary = el('div', 'metrics');
  summary.style.color = 'var(--muted)';
  summary.style.fontFamily = 'var(--mono)';
  summary.style.fontSize = '12px';
  summary.textContent = `latest ${last.value.toFixed(2)} ${unit} · min ${minValue.toFixed(2)} · max ${maxValue.toFixed(2)} · ${values.length} samples`;
  card.appendChild(summary);

  return card;
}

pageLoaders.history = async () => {
  const container = $('history-charts');
  const target = $('history-target').value;
  if (!target) {
    clear(container);
    container.appendChild(el('div', 'empty', 'No targets configured.'));
    return;
  }

  const params = new URLSearchParams({
    target,
    protocol: $('history-protocol').value,
    address_family: $('history-family').value,
    since: $('history-since').value,
    limit: '5000',
  });

  try {
    const measurements = await api(`/api/v1/measurements?${params}`);
    const ordered = measurements.slice().reverse();
    clear(container);

    if (!ordered.length) {
      container.appendChild(el('div', 'empty', 'No measurements in this window.'));
      return;
    }

    const series = (pick) => ordered.map((m) => ({
      time: new Date(m.timestamp).getTime(),
      value: m.success ? (pick(m) ?? null) : null,
      failed: !m.success,
    }));

    const charts = [
      ['Latency', (m) => m.latency_ms, 'ms'],
      ['Jitter', (m) => m.jitter_ms, 'ms'],
      ['Packet loss', (m) => (m.loss_ratio !== undefined && m.loss_ratio !== null ? m.loss_ratio * 100 : null), '%'],
      ['TCP connect', (m) => m.connect_ms, 'ms'],
      ['TLS handshake', (m) => m.tls_ms, 'ms'],
      ['Time to first byte', (m) => m.ttfb_ms, 'ms'],
      ['DNS resolution', (m) => m.dns_ms, 'ms'],
    ];

    let drawn = 0;
    for (const [title, pick, unit] of charts) {
      const points = series(pick);
      if (!points.some((p) => p.value !== null && p.value !== undefined)) continue;
      container.appendChild(drawChart(title, points, unit));
      drawn++;
    }
    if (!drawn) container.appendChild(el('div', 'empty', 'This protocol records no numeric series.'));
  } catch (error) {
    clear(container);
    container.appendChild(el('div', 'empty', `Failed to load history: ${error.message}`));
  }
};

/* ---------- targets ---------- */

pageLoaders.targets = () => {
  const tbody = document.querySelector('#targets-table tbody');
  clear(tbody);
  for (const target of state.targets) {
    const row = tbody.insertRow();
    const cells = [
      target.id,
      target.region,
      target.hostname,
      target.ipv4 ? 'yes' : 'no',
      target.ipv6 ? 'yes' : 'no',
      (target.capabilities || []).join(', '),
      (target.ports || []).join(', '),
    ];
    for (const value of cells) row.insertCell().textContent = value;
  }

  const schedules = $('schedule-list');
  clear(schedules);
  const status = state.status;
  if (status) {
    for (const [name, interval] of Object.entries(status.intervals || {})) {
      const stat = el('div', 'stat');
      stat.appendChild(el('span', 'stat-label', `${name} every`));
      stat.appendChild(el('span', 'stat-value', interval));
      const last = (status.last_runs || {})[name];
      stat.appendChild(el('span', 'stat-label', `last ${relative(last)}`));
      schedules.appendChild(stat);
    }

    const caps = $('capabilities');
    clear(caps);
    const entries = [
      ['ICMP', status.capabilities.icmp ? 'available' : 'unavailable'],
      ['Raw sockets', status.capabilities.icmp_privileged ? 'yes' : 'no'],
      ['Traceroute', status.capabilities.traceroute ? 'available' : 'unavailable'],
    ];
    for (const [label, value] of entries) {
      const stat = el('div', 'stat');
      stat.appendChild(el('span', 'stat-label', label));
      stat.appendChild(el('span', 'stat-value', value));
      caps.appendChild(stat);
    }
    if (status.capabilities.note) {
      const stat = el('div', 'stat');
      stat.appendChild(el('span', 'stat-label', 'note'));
      stat.appendChild(el('span', 'stat-value', status.capabilities.note));
      caps.appendChild(stat);
    }
  }
};

/* ---------- bootstrap ---------- */

function populateTargetSelects() {
  for (const id of ['route-target', 'history-target']) {
    const select = $(id);
    const previous = select.value;
    clear(select);
    if (id === 'route-target') select.appendChild(new Option('all targets', ''));
    for (const target of state.targets) {
      select.appendChild(new Option(`${target.id} (${target.hostname})`, target.id));
    }
    if (previous) select.value = previous;
  }
}

function populateEventTypes() {
  const select = $('event-type');
  const types = [
    'connectivity_lost', 'target_recovered', 'packet_loss_spike', 'latency_spike',
    'jitter_spike', 'route_change', 'dns_failure', 'ipv6_failure', 'public_ip_change',
  ];
  for (const type of types) select.appendChild(new Option(type.replace(/_/g, ' '), type));
}

function applyStatus(status) {
  state.status = status;
  $('site-label').textContent = `${status.site_name} · site ${status.site} · v${status.version}`;

  const v4 = (status.public_addresses || []).find((a) => a.address_family === 'ipv4');
  const v6 = (status.public_addresses || []).find((a) => a.address_family === 'ipv6');
  $('public-v4').textContent = v4 ? (v4.ip || 'unavailable') : '—';
  $('public-v6').textContent = v6 ? (v6.ip || 'unavailable') : '—';

  if (status.snapshot) renderSnapshot(status.snapshot);
}

function connectStream() {
  const indicator = $('live-indicator');
  const source = new EventSource('/api/v1/events/stream');

  source.onopen = () => {
    indicator.className = 'live connected';
    $('live-text').textContent = 'live';
  };

  source.onerror = () => {
    indicator.className = 'live error';
    $('live-text').textContent = 'reconnecting';
  };

  source.addEventListener('snapshot', (event) => {
    renderSnapshot(JSON.parse(event.data));
    if (document.querySelector('#page-overview').classList.contains('active')) {
      loadOverviewEvents();
    }
  });

  source.addEventListener('event', () => {
    loadOverviewEvents();
    if (document.querySelector('#page-events').classList.contains('active')) pageLoaders.events();
  });

  source.addEventListener('run', (event) => {
    const summary = JSON.parse(event.data);
    $('run-btn').textContent = 'Run global benchmark';
    $('run-btn').disabled = false;
    showBanner(`${summary.mode} run finished: ${summary.measurements} measurements, ${summary.routes} routes, ${summary.events} events in ${Math.round(summary.duration_ms)} ms`);
    setTimeout(() => showBanner(null), 6000);
  });
}

async function loadOverviewEvents() {
  try {
    const events = await api('/api/v1/events?limit=12&since=24h');
    renderEvents($('overview-events'), events);
  } catch (_) { /* the events page surfaces the error in detail */ }
}

$('run-btn').addEventListener('click', async () => {
  const button = $('run-btn');
  button.disabled = true;
  button.textContent = 'Running…';
  try {
    await api('/api/v1/tests/run', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: 'full' }),
    });
  } catch (error) {
    showBanner(`Benchmark failed: ${error.message}`, 'error');
  } finally {
    button.disabled = false;
    button.textContent = 'Run global benchmark';
  }
});

$('route-refresh').addEventListener('click', () => pageLoaders.routes());
$('history-refresh').addEventListener('click', () => pageLoaders.history());
$('event-refresh').addEventListener('click', () => pageLoaders.events());
for (const id of ['history-target', 'history-protocol', 'history-family', 'history-since']) {
  $(id).addEventListener('change', () => { syncHash({ replace: true }); pageLoaders.history(); });
}
for (const id of ['route-target', 'route-family']) {
  $(id).addEventListener('change', () => { syncHash({ replace: true }); pageLoaders.routes(); });
}
for (const id of ['event-type', 'event-since']) {
  $(id).addEventListener('change', () => { syncHash({ replace: true }); pageLoaders.events(); });
}

$('replay-toggle').addEventListener('click', () => {
  state.replayPlaying = !state.replayPlaying;
  const button = $('replay-toggle');
  button.textContent = state.replayPlaying ? 'Pause' : 'Play';
  button.setAttribute('aria-pressed', String(state.replayPlaying));
  if (state.maps.routes) state.maps.routes.setPlaying(state.replayPlaying);
});

async function init() {
  populateEventTypes();
  try {
    const [status, targets] = await Promise.all([api('/api/v1/status'), api('/api/v1/targets')]);
    state.targets = targets || [];
    populateTargetSelects();
    applyStatus(status);
  } catch (error) {
    showBanner(`Failed to load status: ${error.message}`, 'error');
  }
  // Last, so that a link naming a target is applied to a populated select
  // rather than an empty one.
  applyHash();
  loadOverviewEvents();
  connectStream();
}

init();
