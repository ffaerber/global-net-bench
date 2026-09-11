'use strict';

// A 2D equirectangular world map drawn on a canvas, with no external
// dependencies so the dashboard keeps working on a machine with no Internet
// access.
//
// The static parts (ocean, graticule, land) are rendered once into an offscreen
// canvas and blitted each frame. Without that the replay animation would refill
// several hundred land polygons sixty times a second.

const DEG = Math.PI / 180;

let landPromise = null;

function loadLand() {
  if (!landPromise) {
    landPromise = fetch('/land-110m.json')
      .then((r) => {
        if (!r.ok) throw new Error(`land data: HTTP ${r.status}`);
        return r.json();
      })
      .then(decodeTopology)
      .catch((err) => {
        // A map without land is still usable, so this must not take the rest of
        // the dashboard down with it.
        console.warn('map: land outline unavailable', err);
        return [];
      });
  }
  return landPromise;
}

// decodeTopology turns quantised TopoJSON into plain [lon, lat] rings. Arcs are
// stored as integer deltas against a scale and translate, and polygons
// reference arcs by index, negative meaning "traverse it backwards".
function decodeTopology(topo) {
  const { scale: [sx, sy], translate: [tx, ty] } = topo.transform;
  const arcs = topo.arcs.map((arc) => {
    let x = 0;
    let y = 0;
    return arc.map(([dx, dy]) => {
      x += dx;
      y += dy;
      return [x * sx + tx, y * sy + ty];
    });
  });

  const stitch = (ring) => {
    const points = [];
    for (const index of ring) {
      const arc = index < 0 ? arcs[~index].slice().reverse() : arcs[index];
      // The first point of each arc repeats the last of the previous one.
      for (const point of (points.length ? arc.slice(1) : arc)) points.push(point);
    }
    return unwrap(points);
  };

  const rings = [];
  for (const geometry of topo.objects.land.geometries) {
    const polygons = geometry.type === 'Polygon' ? [geometry.arcs] : geometry.arcs;
    for (const polygon of polygons) {
      for (const ring of polygon) rings.push(stitch(ring));
    }
  }
  return rings;
}

// unwrap removes antimeridian jumps by letting longitude run past ±180. A ring
// spanning the date line (Russia, Antarctica, Fiji) would otherwise draw a
// horizontal streak straight back across the map. The caller redraws each ring
// shifted by ±360° so whatever leaves one edge arrives at the other.
function unwrap(points) {
  const out = [];
  let offset = 0;
  for (let i = 0; i < points.length; i++) {
    const [lon, lat] = points[i];
    if (i > 0) {
      const previous = points[i - 1][0];
      if (lon - previous > 180) offset -= 360;
      else if (previous - lon > 180) offset += 360;
    }
    out.push([lon + offset, lat]);
  }
  return out;
}

function cssVar(name, fallback) {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return value || fallback;
}

function toVector(lon, lat) {
  const λ = lon * DEG;
  const φ = lat * DEG;
  const cosφ = Math.cos(φ);
  return [cosφ * Math.cos(λ), cosφ * Math.sin(λ), Math.sin(φ)];
}

function toLonLat([x, y, z]) {
  return [Math.atan2(y, x) / DEG, Math.asin(Math.max(-1, Math.min(1, z))) / DEG];
}

// greatCircle samples the shorter arc between two points. On an equirectangular
// map this shows up as a curve, which is what a long-haul path actually looks
// like -- a straight line between the two pixels would be a lie about geography.
function greatCircle(from, to, samples = 64) {
  const a = toVector(from[0], from[1]);
  const b = toVector(to[0], to[1]);
  const dot = Math.max(-1, Math.min(1, a[0] * b[0] + a[1] * b[1] + a[2] * b[2]));
  const omega = Math.acos(dot);
  if (omega < 1e-6) return [from.slice(), to.slice()];
  const sinOmega = Math.sin(omega);
  const points = [];
  for (let i = 0; i <= samples; i++) {
    const t = i / samples;
    const s1 = Math.sin((1 - t) * omega) / sinOmega;
    const s2 = Math.sin(t * omega) / sinOmega;
    points.push(toLonLat([
      s1 * a[0] + s2 * b[0],
      s1 * a[1] + s2 * b[1],
      s1 * a[2] + s2 * b[2],
    ]));
  }
  return unwrap(points);
}

// pointAlong walks a polyline by fraction of its total length, so a pulse moves
// at a steady speed rather than jumping between unevenly spaced samples.
function pointAlong(points, t) {
  if (points.length === 0) return null;
  if (points.length === 1) return points[0];
  const lengths = [];
  let total = 0;
  for (let i = 1; i < points.length; i++) {
    const dx = points[i][0] - points[i - 1][0];
    const dy = points[i][1] - points[i - 1][1];
    const d = Math.hypot(dx, dy);
    lengths.push(d);
    total += d;
  }
  if (total === 0) return points[0];
  let target = Math.max(0, Math.min(1, t)) * total;
  for (let i = 0; i < lengths.length; i++) {
    if (target <= lengths[i]) {
      const f = lengths[i] === 0 ? 0 : target / lengths[i];
      return [
        points[i][0] + (points[i + 1][0] - points[i][0]) * f,
        points[i][1] + (points[i + 1][1] - points[i][1]) * f,
      ];
    }
    target -= lengths[i];
  }
  return points[points.length - 1];
}

// placeOf renders the human-readable half of a geolocated position.
function placeOf(position) {
  if (!position) return '';
  return [position.city, position.country_name || position.country].filter(Boolean).join(', ');
}

const MAP_ASPECT = 2; // equirectangular spans 360° by 180°

class WorldMap {
  constructor(container) {
    this.container = container;
    this.canvas = document.createElement('canvas');
    this.canvas.className = 'map-canvas';
    this.ctx = this.canvas.getContext('2d');
    container.appendChild(this.canvas);

    this.tooltip = document.createElement('div');
    this.tooltip.className = 'map-tooltip';
    this.tooltip.hidden = true;
    container.appendChild(this.tooltip);

    this.land = [];
    this.regions = [];
    this.origin = null;
    this.route = null;
    this.legs = [];
    this.markers = [];
    this.labelBoxes = [];
    this.hover = null;
    this.base = null;
    this.animation = null;
    this.playing = true;
    this.clock = 0;
    this.lastFrame = 0;
    this.destroyed = false;
    this.onRegionClick = null;
    this.onProgress = null;

    this.resizeObserver = new ResizeObserver(() => this.resize());
    this.resizeObserver.observe(container);

    this.themeQuery = window.matchMedia('(prefers-color-scheme: dark)');
    this.onThemeChange = () => {
      this.base = null;
      this.render();
    };
    this.themeQuery.addEventListener('change', this.onThemeChange);

    // A hidden tab should not be burning a frame budget on an animation
    // nobody is looking at.
    this.onVisibility = () => this.syncAnimation();
    document.addEventListener('visibilitychange', this.onVisibility);

    this.bindPointer();
    this.resize();

    loadLand().then((land) => {
      if (this.destroyed) return;
      this.land = land;
      this.base = null;
      this.render();
    });
  }

  destroy() {
    this.destroyed = true;
    this.stopAnimation();
    this.resizeObserver.disconnect();
    this.themeQuery.removeEventListener('change', this.onThemeChange);
    document.removeEventListener('visibilitychange', this.onVisibility);
  }

  resize() {
    const rect = this.container.getBoundingClientRect();
    const width = Math.max(200, rect.width);
    const height = Math.max(120, rect.height || 360);

    // Fit a 2:1 map inside the box without distorting it.
    let mapW = width;
    let mapH = width / MAP_ASPECT;
    if (mapH > height) {
      mapH = height;
      mapW = height * MAP_ASPECT;
    }

    const dpr = window.devicePixelRatio || 1;
    this.canvas.width = Math.round(width * dpr);
    this.canvas.height = Math.round(height * dpr);
    this.canvas.style.width = `${width}px`;
    this.canvas.style.height = `${height}px`;
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    this.width = width;
    this.height = height;
    this.mapW = mapW;
    this.mapH = mapH;
    this.padX = (width - mapW) / 2;
    this.padY = (height - mapH) / 2;
    this.dpr = dpr;
    this.base = null;
    this.render();
  }

  project(lon, lat) {
    return [
      this.padX + ((lon + 180) / 360) * this.mapW,
      this.padY + ((90 - lat) / 180) * this.mapH,
    ];
  }

  bindPointer() {
    this.canvas.addEventListener('pointermove', (e) => this.updateHover(e));
    this.canvas.addEventListener('pointerleave', () => {
      this.hover = null;
      this.tooltip.hidden = true;
      this.render();
    });
    this.canvas.addEventListener('click', () => {
      if (this.hover && this.hover.kind === 'region' && this.onRegionClick) {
        this.onRegionClick(this.hover.id);
      }
    });
  }

  updateHover(e) {
    const rect = this.canvas.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
    let found = null;
    let best = 14 * 14;
    for (const marker of this.markers) {
      const d2 = (marker.x - x) ** 2 + (marker.y - y) ** 2;
      if (d2 < best) {
        best = d2;
        found = marker;
      }
    }
    const changed = (found && found.key) !== (this.hover && this.hover.key);
    this.hover = found;
    this.canvas.style.cursor = found && found.kind === 'region' ? 'pointer' : 'default';
    if (found) {
      this.tooltip.textContent = found.tooltip;
      this.tooltip.hidden = false;
      const tipWidth = this.tooltip.offsetWidth || 160;
      this.tooltip.style.left = `${Math.max(4, Math.min(this.width - tipWidth - 4, found.x + 12))}px`;
      this.tooltip.style.top = `${Math.max(4, found.y - 34)}px`;
    } else {
      this.tooltip.hidden = true;
    }
    if (changed) this.render();
  }

  // setRegions takes the plottable regions and, optionally, the origin the
  // server worked out -- a local region when one is configured with
  // coordinates, otherwise the public egress address located through GeoIP.
  // Deriving it from a local region here is the fallback for a server that
  // sends no origin at all.
  setRegions(regions, origin) {
    this.regions = (regions || []).filter((r) =>
      r.latitude !== undefined && r.latitude !== null &&
      r.longitude !== undefined && r.longitude !== null);
    const local = this.regions.find((r) => r.local);
    if (origin && Number.isFinite(origin.latitude) && Number.isFinite(origin.longitude)) {
      this.origin = {
        lon: origin.longitude,
        lat: origin.latitude,
        label: origin.label || 'This site',
        region: origin.region || null,
        source: origin.source,
        city: origin.city,
        country: origin.country_name || origin.country,
        ip: origin.ip,
      };
    } else if (local) {
      this.origin = { lon: local.longitude, lat: local.latitude, label: local.display_name, region: local.id };
    } else {
      this.origin = null;
    }
    this.rebuildLegs();
    this.render();
  }

  setRoute(hops) {
    this.route = hops && hops.length ? hops : null;
    this.rebuildLegs();
    this.syncAnimation();
    this.render();
  }

  setPlaying(playing) {
    this.playing = playing;
    this.syncAnimation();
    if (!playing) this.render();
  }

  // rebuildLegs precomputes each animated segment: its path and how long the
  // pulse should spend on it.
  //
  // A traceroute RTT is the round trip from here to that hop, so it is
  // cumulative -- the time attributable to a single leg is the difference
  // between consecutive hops. That difference can come out negative, because a
  // router under load will deprioritise the ICMP replies it generates itself
  // while still forwarding traffic promptly, so a later hop can report a
  // smaller RTT than its predecessor. Those are clamped to a floor rather than
  // dropped: the hop is still on the path, it just cannot be timed.
  rebuildLegs() {
    this.legs = [];
    this.totalMs = 0;
    if (!this.route) return;

    const located = this.route.filter((h) => h.geo &&
      h.geo.latitude !== undefined && h.geo.latitude !== null &&
      h.geo.longitude !== undefined && h.geo.longitude !== null);
    if (!located.length) return;

    const MIN_LEG_MS = 1;
    let previousRTT = 0;
    let previous = this.origin ? [this.origin.lon, this.origin.lat] : null;
    let previousHop = null;

    for (const hop of located) {
      const here = [hop.geo.longitude, hop.geo.latitude];
      const rtt = (hop.rtt_ms === undefined || hop.rtt_ms === null) ? previousRTT : hop.rtt_ms;
      const delta = Math.max(rtt - previousRTT, MIN_LEG_MS);

      if (previous) {
        this.legs.push({
          path: greatCircle(previous, here, 48),
          ms: delta,
          hop,
          // A gap in hop numbering means hops in between could not be placed,
          // so this leg is an assumption about the path, not an observation.
          contiguous: previousHop === null ? true : hop.hop === previousHop.hop + 1,
          cumulativeMs: rtt,
        });
        this.totalMs += delta;
      }
      previous = here;
      previousRTT = rtt;
      previousHop = hop;
    }
    this.located = located;
  }

  syncAnimation() {
    const shouldRun = this.playing && !this.destroyed && this.legs.length > 0 &&
      !document.hidden && this.container.offsetParent !== null;
    if (shouldRun && !this.animation) {
      this.lastFrame = performance.now();
      this.animation = requestAnimationFrame((t) => this.frame(t));
    } else if (!shouldRun && this.animation) {
      this.stopAnimation();
    }
  }

  stopAnimation() {
    if (this.animation) {
      cancelAnimationFrame(this.animation);
      this.animation = null;
    }
  }

  frame(now) {
    if (this.destroyed) return;
    const elapsed = Math.min(100, now - this.lastFrame);
    this.lastFrame = now;
    this.clock += elapsed;
    if (this.clock > this.cycleMs()) this.clock = 0;
    this.render();
    this.animation = requestAnimationFrame((t) => this.frame(t));
  }

  // The replay is stretched to a watchable length: a real 200 ms trace would
  // otherwise flash past in a fifth of a second. Legs keep their relative
  // durations, so a slow hop still visibly drags.
  playbackMs() { return 3200; }
  holdMs() { return 900; }
  cycleMs() { return this.playbackMs() + this.holdMs(); }

  render() {
    if (this.destroyed || !this.ctx) return;
    const colors = this.palette();
    const ctx = this.ctx;

    if (!this.base) this.buildBase(colors);

    ctx.clearRect(0, 0, this.width, this.height);
    if (this.base) ctx.drawImage(this.base, 0, 0, this.width, this.height);

    this.markers = [];
    this.labelBoxes = [];

    if (this.route) this.drawRoute(colors);
    else this.drawRegions(colors);
  }

  palette() {
    return {
      ocean: cssVar('--map-ocean', '#131b26'),
      land: cssVar('--map-land', '#33404e'),
      grid: cssVar('--map-grid', '#2a3542'),
      text: cssVar('--text', '#e6edf3'),
      muted: cssVar('--muted', '#8b98a5'),
      panel: cssVar('--panel', '#161b22'),
      accent: cssVar('--accent', '#4c9aff'),
      excellent: cssVar('--excellent', '#3fb950'),
      good: cssVar('--good', '#6bc46d'),
      fair: cssVar('--fair', '#d29922'),
      degraded: cssVar('--degraded', '#f0883e'),
      down: cssVar('--down', '#f85149'),
      unknown: cssVar('--unknown', '#6e7681'),
    };
  }

  // buildBase paints the parts that never change between frames.
  buildBase(colors) {
    const canvas = document.createElement('canvas');
    canvas.width = Math.round(this.width * this.dpr);
    canvas.height = Math.round(this.height * this.dpr);
    const ctx = canvas.getContext('2d');
    ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);

    ctx.fillStyle = colors.ocean;
    ctx.fillRect(this.padX, this.padY, this.mapW, this.mapH);

    ctx.save();
    ctx.beginPath();
    ctx.rect(this.padX, this.padY, this.mapW, this.mapH);
    ctx.clip();

    ctx.fillStyle = colors.land;
    // Rings are unwrapped, so a shape crossing the date line runs off one edge;
    // redrawing it shifted by a full turn brings the rest back on the other.
    for (const shift of [-360, 0, 360]) {
      for (const ring of this.land) {
        ctx.beginPath();
        for (let i = 0; i < ring.length; i++) {
          const [x, y] = this.project(ring[i][0] + shift, ring[i][1]);
          if (i === 0) ctx.moveTo(x, y);
          else ctx.lineTo(x, y);
        }
        ctx.closePath();
        ctx.fill();
      }
    }

    ctx.strokeStyle = colors.grid;
    ctx.globalAlpha = 0.5;
    ctx.lineWidth = 0.5;
    for (let lat = -60; lat <= 60; lat += 30) {
      const [, y] = this.project(0, lat);
      ctx.beginPath();
      ctx.moveTo(this.padX, y);
      ctx.lineTo(this.padX + this.mapW, y);
      ctx.stroke();
    }
    for (let lon = -150; lon <= 150; lon += 30) {
      const [x] = this.project(lon, 0);
      ctx.beginPath();
      ctx.moveTo(x, this.padY);
      ctx.lineTo(x, this.padY + this.mapH);
      ctx.stroke();
    }
    ctx.globalAlpha = 1;
    ctx.restore();

    ctx.strokeStyle = colors.grid;
    ctx.lineWidth = 1;
    ctx.strokeRect(this.padX + 0.5, this.padY + 0.5, this.mapW - 1, this.mapH - 1);

    this.base = canvas;
  }

  // strokePath draws an unwrapped lon/lat polyline. Because the points are
  // unwrapped, a path crossing the date line runs off one edge of the map;
  // drawing it again shifted by a full turn brings that part back on the other
  // side, and the clip keeps each pass inside the map. Simply dropping the
  // off-map points instead would leave a Hong Kong to San Francisco leg
  // vanishing at the right edge and never arriving.
  strokePath(points) {
    if (points.length < 2) return;
    const ctx = this.ctx;
    ctx.save();
    ctx.beginPath();
    ctx.rect(this.padX, this.padY, this.mapW, this.mapH);
    ctx.clip();
    for (const shift of [-360, 0, 360]) {
      ctx.beginPath();
      for (let i = 0; i < points.length; i++) {
        const [x, y] = this.project(points[i][0] + shift, points[i][1]);
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
      }
      ctx.stroke();
    }
    ctx.restore();
  }

  // normaliseLon folds a longitude that unwrapping pushed past ±180 back onto
  // the map, so the travelling pulse stays visible after the date line.
  normaliseLon(lon) {
    return ((((lon + 180) % 360) + 360) % 360) - 180;
  }

  statusColor(colors, status) { return colors[status] || colors.unknown; }

  drawLabel(text, x, y, colors) {
    const ctx = this.ctx;
    const width = ctx.measureText(text).width;
    const box = { x1: x - 2, y1: y - 7, x2: x + width + 2, y2: y + 7 };
    for (const placed of this.labelBoxes) {
      if (box.x1 < placed.x2 && box.x2 > placed.x1 && box.y1 < placed.y2 && box.y2 > placed.y1) return;
    }
    this.labelBoxes.push(box);
    ctx.lineWidth = 3;
    ctx.strokeStyle = colors.ocean;
    ctx.strokeText(text, x, y);
    ctx.fillStyle = colors.text;
    ctx.fillText(text, x, y);
  }

  drawRegions(colors) {
    const ctx = this.ctx;

    if (this.origin) {
      for (const region of this.regions) {
        if (this.isOriginRegion(region)) continue;
        ctx.strokeStyle = this.statusColor(colors, region.status);
        ctx.globalAlpha = 0.4;
        ctx.lineWidth = 1.4;
        this.strokePath(greatCircle([this.origin.lon, this.origin.lat], [region.longitude, region.latitude]));
      }
      ctx.globalAlpha = 1;
    }

    for (const region of this.regions) {
      const [x, y] = this.project(region.longitude, region.latitude);
      const color = this.statusColor(colors, region.status);
      const radius = region.local ? 6 : 5;
      // A position nobody configured was inferred from where the target's
      // address block is registered. It is drawn hollow, the same way an
      // approximate traceroute hop is, so the map never passes a guess off as
      // a fact.
      const inferred = region.position && region.position.source === 'geoip';

      ctx.beginPath();
      ctx.arc(x, y, radius + 3, 0, Math.PI * 2);
      ctx.fillStyle = color;
      ctx.globalAlpha = inferred ? 0.12 : 0.22;
      ctx.fill();
      ctx.globalAlpha = 1;

      ctx.beginPath();
      ctx.arc(x, y, radius, 0, Math.PI * 2);
      if (inferred) {
        ctx.fillStyle = color;
        ctx.globalAlpha = 0.3;
        ctx.fill();
        ctx.globalAlpha = 1;
        ctx.strokeStyle = color;
        ctx.lineWidth = 1.5;
        ctx.setLineDash([2, 2]);
        ctx.stroke();
        ctx.setLineDash([]);
      } else {
        ctx.fillStyle = color;
        ctx.fill();
      }
      if (region.local) {
        ctx.strokeStyle = colors.text;
        ctx.lineWidth = 1.5;
        ctx.stroke();
      }

      ctx.font = '11px system-ui, sans-serif';
      ctx.textBaseline = 'middle';
      this.drawLabel(region.display_name || region.id, x + radius + 5, y, colors);

      const bits = [region.display_name || region.id];
      if (region.has_data) {
        if (region.rtt_ms !== undefined && region.rtt_ms !== null) bits.push(`${region.rtt_ms.toFixed(1)} ms`);
        bits.push(`score ${Math.round(region.score)}`);
      } else {
        bits.push('no data yet');
      }
      if (inferred) bits.push(`≈ ${placeOf(region.position) || 'located by GeoIP'}`);
      this.markers.push({ kind: 'region', key: `region:${region.id}`, id: region.id, x, y, tooltip: bits.join(' · ') });
    }

    this.drawOrigin(colors);
  }

  // isOriginRegion reports whether a region is where the arcs already start, so
  // the map does not draw a zero-length arc from a point to itself.
  isOriginRegion(region) {
    if (!this.origin) return false;
    if (this.origin.region) return this.origin.region === region.id;
    return region.local;
  }

  // drawOrigin marks the vantage point when it is not one of the regions --
  // that is, when it came from locating the public egress address rather than
  // from a region marked local.
  drawOrigin(colors) {
    if (!this.origin || this.regions.some((r) => this.isOriginRegion(r))) return;
    const ctx = this.ctx;
    const [x, y] = this.project(this.origin.lon, this.origin.lat);

    ctx.beginPath();
    ctx.arc(x, y, 9, 0, Math.PI * 2);
    ctx.fillStyle = colors.text;
    ctx.globalAlpha = 0.15;
    ctx.fill();
    ctx.globalAlpha = 1;

    ctx.beginPath();
    ctx.arc(x, y, 5, 0, Math.PI * 2);
    ctx.fillStyle = colors.text;
    ctx.fill();
    ctx.strokeStyle = colors.ocean;
    ctx.lineWidth = 1.5;
    ctx.stroke();

    ctx.font = '11px system-ui, sans-serif';
    ctx.textBaseline = 'middle';
    this.drawLabel(this.origin.label, x + 10, y, colors);

    const bits = [this.origin.label];
    const place = placeOf(this.origin);
    if (place) bits.push(place);
    if (this.origin.ip) bits.push(this.origin.ip);
    if (this.origin.source === 'geoip') bits.push('located by GeoIP');
    this.markers.push({ kind: 'origin', key: 'origin', x, y, tooltip: bits.join(' · ') });
  }

  drawRoute(colors) {
    const ctx = this.ctx;

    // The whole path, dim, as the backdrop the pulse travels along.
    for (const leg of this.legs) {
      ctx.strokeStyle = colors.accent;
      ctx.globalAlpha = leg.contiguous ? 0.3 : 0.16;
      ctx.lineWidth = leg.contiguous ? 2 : 1.5;
      ctx.setLineDash(leg.contiguous ? [] : [4, 4]);
      this.strokePath(leg.path);
      ctx.setLineDash([]);
    }
    ctx.globalAlpha = 1;

    const progress = this.progress();
    this.drawTravelled(colors, progress);

    if (this.origin) {
      const [x, y] = this.project(this.origin.lon, this.origin.lat);
      ctx.beginPath();
      ctx.arc(x, y, 5, 0, Math.PI * 2);
      ctx.fillStyle = colors.text;
      ctx.fill();
    }

    for (const hop of (this.located || [])) {
      const [x, y] = this.project(hop.geo.longitude, hop.geo.latitude);
      const low = hop.geo.confidence === 'low';
      const reached = progress.reachedHops.has(hop.hop);

      if (reached) {
        // A brief flare as the pulse lands, fading over the following moments.
        const age = progress.reachedHops.get(hop.hop);
        const flare = Math.max(0, 1 - age / 600);
        if (flare > 0) {
          ctx.beginPath();
          ctx.arc(x, y, 5 + flare * 12, 0, Math.PI * 2);
          ctx.fillStyle = colors.accent;
          ctx.globalAlpha = flare * 0.35;
          ctx.fill();
          ctx.globalAlpha = 1;
        }
      }

      ctx.beginPath();
      ctx.arc(x, y, 4.5, 0, Math.PI * 2);
      if (low) {
        ctx.strokeStyle = colors.fair;
        ctx.lineWidth = 1.5;
        ctx.setLineDash([2, 2]);
        ctx.stroke();
        ctx.setLineDash([]);
      } else {
        ctx.fillStyle = reached ? colors.accent : colors.muted;
        ctx.fill();
      }

      const where = [hop.geo.city, hop.geo.country_name || hop.geo.country].filter(Boolean).join(', ');
      const bits = [`Hop ${hop.hop}`, hop.ip || '*'];
      if (where) bits.push(where);
      if (hop.geo.org) bits.push(hop.geo.org);
      if (hop.rtt_ms !== undefined && hop.rtt_ms !== null) bits.push(`${hop.rtt_ms.toFixed(1)} ms`);
      if (low) bits.push('approximate');
      this.markers.push({ kind: 'hop', key: `hop:${hop.hop}`, x, y, tooltip: bits.join(' · ') });
    }

    if (progress.head) {
      const [x, y] = this.project(this.normaliseLon(progress.head[0]), progress.head[1]);
      ctx.beginPath();
      ctx.arc(x, y, 10, 0, Math.PI * 2);
      ctx.fillStyle = colors.accent;
      ctx.globalAlpha = 0.25;
      ctx.fill();
      ctx.globalAlpha = 1;
      ctx.beginPath();
      ctx.arc(x, y, 4, 0, Math.PI * 2);
      ctx.fillStyle = colors.accent;
      ctx.fill();
    }

    if (this.onProgress) this.onProgress(progress);
  }

  // drawTravelled brightens the portion of the path the pulse has already
  // covered, so the route fills in as the replay runs.
  drawTravelled(colors, progress) {
    const ctx = this.ctx;
    ctx.strokeStyle = colors.accent;
    ctx.lineWidth = 2.5;
    ctx.globalAlpha = 0.95;
    for (let i = 0; i < this.legs.length; i++) {
      if (i > progress.legIndex) break;
      const leg = this.legs[i];
      if (i < progress.legIndex) {
        this.strokePath(leg.path);
      } else {
        const upto = Math.max(2, Math.ceil(leg.path.length * progress.legFraction));
        this.strokePath(leg.path.slice(0, upto));
      }
    }
    ctx.globalAlpha = 1;
  }

  // progress converts the clock into a position along the route.
  progress() {
    const reachedHops = new Map();
    if (!this.legs.length) return { legIndex: -1, legFraction: 0, head: null, reachedHops, hop: null, done: true };

    const playback = this.playbackMs();
    const t = Math.min(this.clock, playback) / playback; // 0..1 across the run
    const targetMs = t * this.totalMs;

    let accumulated = 0;
    let legIndex = this.legs.length - 1;
    let legFraction = 1;
    for (let i = 0; i < this.legs.length; i++) {
      const leg = this.legs[i];
      if (targetMs <= accumulated + leg.ms || i === this.legs.length - 1) {
        legIndex = i;
        legFraction = leg.ms === 0 ? 1 : Math.max(0, Math.min(1, (targetMs - accumulated) / leg.ms));
        break;
      }
      accumulated += leg.ms;
    }

    // Milliseconds of replay time since each already-passed hop was reached,
    // used to fade its flare.
    let elapsedMs = 0;
    for (let i = 0; i < this.legs.length; i++) {
      elapsedMs += this.legs[i].ms;
      if (i < legIndex || (i === legIndex && legFraction >= 1)) {
        const sinceMs = (targetMs - elapsedMs) / this.totalMs * playback;
        reachedHops.set(this.legs[i].hop.hop, Math.max(0, sinceMs));
      }
    }

    const leg = this.legs[legIndex];
    return {
      legIndex,
      legFraction,
      head: pointAlong(leg.path, legFraction),
      reachedHops,
      hop: leg.hop,
      legMs: leg.ms,
      cumulativeMs: leg.cumulativeMs,
      totalMs: this.totalMs,
      done: this.clock >= playback,
    };
  }
}

window.WorldMap = WorldMap;
