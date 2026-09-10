'use strict';

// An orthographic globe drawn on a canvas, with no external dependencies so the
// dashboard keeps working on a machine with no Internet access.
//
// Canvas rather than SVG because rotating the globe rewrites every land path on
// every frame, which SVG handles poorly at this polygon count.

const DEG = Math.PI / 180;

// The land outline is fetched once and shared by every globe on the page.
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
        // A globe without land is still a usable globe, so failure here must
        // not take the rest of the dashboard down with it.
        console.warn('globe: land outline unavailable', err);
        return [];
      });
  }
  return landPromise;
}

// decodeTopology turns quantised TopoJSON into plain [lon, lat] rings. The
// format stores each arc as integer deltas against a scale and translate, and
// polygons reference arcs by index, negative meaning "traverse it backwards".
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
      // The first point of each arc repeats the last point of the previous one.
      for (const point of (points.length ? arc.slice(1) : arc)) points.push(point);
    }
    return points;
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

function cssVar(name, fallback) {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return value || fallback;
}

// Converts lon/lat degrees to a unit vector, so great circles can be walked by
// interpolating between two vectors rather than between two angles.
function toVector(lon, lat) {
  const λ = lon * DEG;
  const φ = lat * DEG;
  const cosφ = Math.cos(φ);
  return [cosφ * Math.cos(λ), cosφ * Math.sin(λ), Math.sin(φ)];
}

function toLonLat([x, y, z]) {
  return [Math.atan2(y, x) / DEG, Math.asin(Math.max(-1, Math.min(1, z))) / DEG];
}

// greatCircle samples the shorter arc of the great circle joining two points,
// which is the path a signal would take if routing followed geography.
function greatCircle(from, to, samples = 64) {
  const a = toVector(from[0], from[1]);
  const b = toVector(to[0], to[1]);
  let dot = a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
  dot = Math.max(-1, Math.min(1, dot));
  const omega = Math.acos(dot);
  const points = [];
  if (omega < 1e-6) return [from, to];
  const sinOmega = Math.sin(omega);
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
  return points;
}

class Globe {
  constructor(container) {
    this.container = container;
    this.canvas = document.createElement('canvas');
    this.canvas.className = 'globe-canvas';
    this.ctx = this.canvas.getContext('2d');
    container.appendChild(this.canvas);

    this.tooltip = document.createElement('div');
    this.tooltip.className = 'globe-tooltip';
    this.tooltip.hidden = true;
    container.appendChild(this.tooltip);

    this.land = [];
    this.regions = [];
    this.origin = null;
    this.route = null;
    this.markers = [];
    this.labelBoxes = [];
    this.hover = null;
    this.rotation = { lambda: -10, phi: 25 };
    this.dragging = false;
    this.animation = null;
    this.destroyed = false;
    this.onRegionClick = null;

    this.resizeObserver = new ResizeObserver(() => this.resize());
    this.resizeObserver.observe(container);

    this.bindPointer();
    this.resize();

    loadLand().then((land) => {
      if (this.destroyed) return;
      this.land = land;
      this.render();
    });
  }

  destroy() {
    this.destroyed = true;
    if (this.animation) cancelAnimationFrame(this.animation);
    this.resizeObserver.disconnect();
  }

  resize() {
    const rect = this.container.getBoundingClientRect();
    const width = Math.max(200, rect.width);
    const height = Math.max(200, rect.height || 420);
    const dpr = window.devicePixelRatio || 1;
    this.canvas.width = Math.round(width * dpr);
    this.canvas.height = Math.round(height * dpr);
    this.canvas.style.width = `${width}px`;
    this.canvas.style.height = `${height}px`;
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.width = width;
    this.height = height;
    this.cx = width / 2;
    this.cy = height / 2;
    this.radius = Math.min(width, height) / 2 - 18;
    this.render();
  }

  bindPointer() {
    let lastX = 0;
    let lastY = 0;

    this.canvas.addEventListener('pointerdown', (e) => {
      this.dragging = true;
      this.stopAnimation();
      lastX = e.clientX;
      lastY = e.clientY;
      this.canvas.setPointerCapture(e.pointerId);
      this.canvas.classList.add('dragging');
    });

    this.canvas.addEventListener('pointermove', (e) => {
      if (this.dragging) {
        const dx = e.clientX - lastX;
        const dy = e.clientY - lastY;
        lastX = e.clientX;
        lastY = e.clientY;
        this.rotation.lambda += dx * 0.35;
        // Clamped so the globe cannot tip past the poles and turn upside down.
        this.rotation.phi = Math.max(-89, Math.min(89, this.rotation.phi + dy * 0.35));
        this.render();
        return;
      }
      this.updateHover(e);
    });

    const endDrag = (e) => {
      if (!this.dragging) return;
      this.dragging = false;
      this.canvas.classList.remove('dragging');
      if (e.pointerId !== undefined && this.canvas.hasPointerCapture(e.pointerId)) {
        this.canvas.releasePointerCapture(e.pointerId);
      }
    };
    this.canvas.addEventListener('pointerup', endDrag);
    this.canvas.addEventListener('pointercancel', endDrag);
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
      const dx = marker.x - x;
      const dy = marker.y - y;
      const d2 = dx * dx + dy * dy;
      if (d2 < best) {
        best = d2;
        found = marker;
      }
    }
    const changed = (found && found.key) !== (this.hover && this.hover.key);
    this.hover = found;
    this.canvas.style.cursor = found && found.kind === 'region' ? 'pointer' : 'grab';
    if (found) {
      this.tooltip.textContent = found.tooltip;
      this.tooltip.hidden = false;
      // Kept inside the container so a marker near the edge stays readable.
      const tipWidth = this.tooltip.offsetWidth || 160;
      this.tooltip.style.left = `${Math.max(4, Math.min(this.width - tipWidth - 4, found.x + 12))}px`;
      this.tooltip.style.top = `${Math.max(4, found.y - 34)}px`;
    } else {
      this.tooltip.hidden = true;
    }
    if (changed) this.render();
  }

  setRegions(regions) {
    this.regions = (regions || []).filter((r) => r.latitude !== undefined && r.latitude !== null &&
      r.longitude !== undefined && r.longitude !== null);
    const local = this.regions.find((r) => r.local);
    this.origin = local ? { lon: local.longitude, lat: local.latitude, label: local.display_name } : null;
    this.render();
  }

  setRoute(route) {
    this.route = route;
    this.render();
  }

  // focusOn turns the globe to face a point, once, as an introduction. It is
  // skipped when the viewer has asked for reduced motion.
  focusOn(lon, lat) {
    // Centring a point needs rotation.lambda = -lon and rotation.phi = +lat:
    // solving project() for x = cx, y = cy gives those two directly.
    const target = { lambda: -lon, phi: lat };
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      this.rotation = target;
      this.render();
      return;
    }
    this.stopAnimation();
    const start = { ...this.rotation };
    // Take the short way round rather than spinning most of the way backwards.
    let deltaLambda = ((target.lambda - start.lambda + 540) % 360) - 180;
    const deltaPhi = target.phi - start.phi;
    const duration = 900;
    const began = performance.now();

    const step = (now) => {
      if (this.destroyed || this.dragging) return;
      const t = Math.min(1, (now - began) / duration);
      const eased = t < 0.5 ? 2 * t * t : 1 - ((-2 * t + 2) ** 2) / 2;
      this.rotation.lambda = start.lambda + deltaLambda * eased;
      this.rotation.phi = start.phi + deltaPhi * eased;
      this.render();
      if (t < 1) this.animation = requestAnimationFrame(step);
      else this.animation = null;
    };
    this.animation = requestAnimationFrame(step);
  }

  stopAnimation() {
    if (this.animation) {
      cancelAnimationFrame(this.animation);
      this.animation = null;
    }
  }

  // project maps lon/lat to canvas coordinates, returning null when the point
  // is on the far side of the globe and must not be drawn.
  project(lon, lat) {
    const λ = (lon + this.rotation.lambda) * DEG;
    const φ = lat * DEG;
    const φ0 = this.rotation.phi * DEG;
    const cosφ = Math.cos(φ);
    const sinφ = Math.sin(φ);
    const cosλ = Math.cos(λ);
    const cosc = Math.sin(φ0) * sinφ + Math.cos(φ0) * cosφ * cosλ;
    if (cosc < 0) return null;
    return [
      this.cx + this.radius * cosφ * Math.sin(λ),
      this.cy - this.radius * (Math.cos(φ0) * sinφ - Math.sin(φ0) * cosφ * cosλ),
    ];
  }

  render() {
    if (this.destroyed || !this.ctx) return;
    const ctx = this.ctx;
    const colors = {
      ocean: cssVar('--globe-ocean', '#131b26'),
      land: cssVar('--globe-land', '#33404e'),
      grid: cssVar('--globe-grid', '#2a3542'),
      text: cssVar('--text', '#e6edf3'),
      muted: cssVar('--muted', '#8b98a5'),
      accent: cssVar('--accent', '#4c9aff'),
      excellent: cssVar('--excellent', '#3fb950'),
      good: cssVar('--good', '#6bc46d'),
      fair: cssVar('--fair', '#d29922'),
      degraded: cssVar('--degraded', '#f0883e'),
      down: cssVar('--down', '#f85149'),
      unknown: cssVar('--unknown', '#6e7681'),
    };

    ctx.clearRect(0, 0, this.width, this.height);
    this.markers = [];
    this.labelBoxes = [];

    ctx.save();
    ctx.beginPath();
    ctx.arc(this.cx, this.cy, this.radius, 0, Math.PI * 2);
    // Everything is clipped to the sphere, which hides the chords left behind
    // where a landmass is cut off by the limb.
    ctx.clip();

    ctx.fillStyle = colors.ocean;
    ctx.fillRect(0, 0, this.width, this.height);

    this.drawGraticule(colors.grid);
    this.drawLand(colors.land);

    ctx.restore();

    ctx.strokeStyle = colors.grid;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.arc(this.cx, this.cy, this.radius, 0, Math.PI * 2);
    ctx.stroke();

    if (this.route) this.drawRoute(colors);
    else this.drawRegions(colors);
  }

  drawGraticule(color) {
    const ctx = this.ctx;
    ctx.strokeStyle = color;
    ctx.globalAlpha = 0.5;
    ctx.lineWidth = 0.5;
    for (let lat = -60; lat <= 60; lat += 30) {
      this.strokePath(this.sample((lon) => [lon, lat], -180, 180, 4));
    }
    for (let lon = -180; lon < 180; lon += 30) {
      this.strokePath(this.sample((lat) => [lon, lat], -90, 90, 4));
    }
    ctx.globalAlpha = 1;
  }

  sample(fn, from, to, step) {
    const points = [];
    for (let v = from; v <= to; v += step) points.push(fn(v));
    return points;
  }

  // strokePath draws only the runs of a path that are on the near side, so a
  // line passing behind the globe is broken rather than cutting across it.
  strokePath(lonLats) {
    const ctx = this.ctx;
    let drawing = false;
    ctx.beginPath();
    for (const [lon, lat] of lonLats) {
      const point = this.project(lon, lat);
      if (!point) {
        drawing = false;
        continue;
      }
      if (drawing) ctx.lineTo(point[0], point[1]);
      else {
        ctx.moveTo(point[0], point[1]);
        drawing = true;
      }
    }
    ctx.stroke();
  }

  drawLand(color) {
    const ctx = this.ctx;
    ctx.fillStyle = color;
    for (const ring of this.land) {
      let drawing = false;
      ctx.beginPath();
      for (const [lon, lat] of ring) {
        const point = this.project(lon, lat);
        if (!point) {
          drawing = false;
          continue;
        }
        if (drawing) ctx.lineTo(point[0], point[1]);
        else {
          ctx.moveTo(point[0], point[1]);
          drawing = true;
        }
      }
      ctx.closePath();
      ctx.fill();
    }
  }

  statusColor(colors, status) {
    return colors[status] || colors.unknown;
  }

  // drawLabel writes a marker's name, skipping it when it would collide with
  // one already placed. Two overlapping labels are less readable than one.
  drawLabel(text, x, y, colors) {
    const ctx = this.ctx;
    const width = ctx.measureText(text).width;
    const box = { x1: x - 2, y1: y - 7, x2: x + width + 2, y2: y + 7 };
    for (const placed of this.labelBoxes) {
      if (box.x1 < placed.x2 && box.x2 > placed.x1 && box.y1 < placed.y2 && box.y2 > placed.y1) return;
    }
    this.labelBoxes.push(box);

    // A halo in the background colour keeps the text legible over coastlines.
    ctx.lineWidth = 3;
    ctx.strokeStyle = cssVar('--panel', '#161b22');
    ctx.strokeText(text, x, y);
    ctx.fillStyle = colors.text;
    ctx.fillText(text, x, y);
  }

  drawRegions(colors) {
    const ctx = this.ctx;

    if (this.origin) {
      for (const region of this.regions) {
        if (region.local) continue;
        const path = greatCircle([this.origin.lon, this.origin.lat], [region.longitude, region.latitude]);
        ctx.strokeStyle = this.statusColor(colors, region.status);
        ctx.globalAlpha = 0.45;
        ctx.lineWidth = 1.5;
        this.strokePath(path);
      }
      ctx.globalAlpha = 1;
    }

    for (const region of this.regions) {
      const point = this.project(region.longitude, region.latitude);
      if (!point) continue;
      const color = this.statusColor(colors, region.status);
      const isLocal = !!region.local;
      const radius = isLocal ? 6 : 5;

      ctx.beginPath();
      ctx.arc(point[0], point[1], radius + 3, 0, Math.PI * 2);
      ctx.fillStyle = color;
      ctx.globalAlpha = 0.22;
      ctx.fill();
      ctx.globalAlpha = 1;

      ctx.beginPath();
      ctx.arc(point[0], point[1], radius, 0, Math.PI * 2);
      ctx.fillStyle = color;
      ctx.fill();
      if (isLocal) {
        ctx.strokeStyle = colors.text;
        ctx.lineWidth = 1.5;
        ctx.stroke();
      }

      const label = region.display_name || region.id;
      ctx.font = '11px system-ui, sans-serif';
      ctx.textBaseline = 'middle';
      this.drawLabel(label, point[0] + radius + 5, point[1], colors);

      const bits = [label];
      if (region.has_data) {
        if (region.rtt_ms !== undefined && region.rtt_ms !== null) bits.push(`${region.rtt_ms.toFixed(1)} ms`);
        bits.push(`score ${Math.round(region.score)}`);
      } else {
        bits.push('no data yet');
      }
      this.markers.push({
        kind: 'region',
        key: `region:${region.id}`,
        id: region.id,
        x: point[0],
        y: point[1],
        tooltip: bits.join(' · '),
      });
    }
  }

  drawRoute(colors) {
    const ctx = this.ctx;
    const located = this.route.filter((hop) => hop.geo && hop.geo.latitude !== undefined &&
      hop.geo.latitude !== null && hop.geo.longitude !== undefined && hop.geo.longitude !== null);

    if (this.origin) {
      const first = located[0];
      if (first) {
        ctx.strokeStyle = colors.accent;
        ctx.globalAlpha = 0.5;
        ctx.lineWidth = 1.5;
        ctx.setLineDash([4, 4]);
        this.strokePath(greatCircle([this.origin.lon, this.origin.lat], [first.geo.longitude, first.geo.latitude]));
        ctx.setLineDash([]);
        ctx.globalAlpha = 1;
      }
      const point = this.project(this.origin.lon, this.origin.lat);
      if (point) {
        ctx.beginPath();
        ctx.arc(point[0], point[1], 5, 0, Math.PI * 2);
        ctx.fillStyle = colors.text;
        ctx.fill();
      }
    }

    for (let i = 1; i < located.length; i++) {
      const a = located[i - 1];
      const b = located[i];
      // A gap in hop numbers means hops in between could not be placed, so the
      // segment is a guess about the path rather than an observation of it.
      const contiguous = b.hop === a.hop + 1;
      ctx.strokeStyle = colors.accent;
      ctx.globalAlpha = contiguous ? 0.85 : 0.4;
      ctx.lineWidth = contiguous ? 2 : 1.5;
      ctx.setLineDash(contiguous ? [] : [4, 4]);
      this.strokePath(greatCircle([a.geo.longitude, a.geo.latitude], [b.geo.longitude, b.geo.latitude], 48));
      ctx.setLineDash([]);
    }
    ctx.globalAlpha = 1;

    for (const hop of located) {
      const point = this.project(hop.geo.longitude, hop.geo.latitude);
      if (!point) continue;
      const low = hop.geo.confidence === 'low';
      ctx.beginPath();
      ctx.arc(point[0], point[1], 4.5, 0, Math.PI * 2);
      if (low) {
        // Hollow and dashed: the country is known, the position within it is not.
        ctx.strokeStyle = colors.fair;
        ctx.lineWidth = 1.5;
        ctx.setLineDash([2, 2]);
        ctx.stroke();
        ctx.setLineDash([]);
      } else {
        ctx.fillStyle = colors.accent;
        ctx.fill();
      }

      const where = [hop.geo.city, hop.geo.country_name || hop.geo.country].filter(Boolean).join(', ');
      const bits = [`Hop ${hop.hop}`, hop.ip || '*'];
      if (where) bits.push(where);
      if (hop.geo.org) bits.push(hop.geo.org);
      if (hop.rtt_ms !== undefined && hop.rtt_ms !== null) bits.push(`${hop.rtt_ms.toFixed(1)} ms`);
      if (low) bits.push('approximate');
      this.markers.push({
        kind: 'hop',
        key: `hop:${hop.hop}`,
        x: point[0],
        y: point[1],
        tooltip: bits.join(' · '),
      });
    }
  }
}

window.Globe = Globe;
