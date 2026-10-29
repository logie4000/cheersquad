import { Injectable } from '@angular/core';
import { LEG, TILE_ATTRIBUTION, TILE_URL, WALK } from './course-map';
import { LatLon, lineColor, Step, Stop } from './plan';

// A downloadable PDF of the plan, drawn without the browser's print dialog:
// the map is painted onto a canvas from OSM tiles plus our own course, legs
// and stops, and the sightings table is real text. jsPDF loads on first use
// so it stays out of the initial bundle.

/** What the map image shows; the same inputs as the on-screen map. */
export interface MapScene {
  course: LatLon[];
  stops: Stop[];
  legs: Step[][];
  start: LatLon | null;
  courseColor: string;
}

/** The plan as text, already in the user's units and clock times. */
export interface PdfPlan {
  summary: string;
  heading: string;
  routing?: string;
  columns: string[];
  rows: { cells: string[]; link?: string; steps?: string }[];
  note: string;
  map: MapScene;
}

/** A Web Mercator view: zoom (may be fractional) and the center in zoom-0 pixels. */
export interface MapView {
  zoom: number;
  cx: number;
  cy: number;
  width: number;
  height: number;
}

const TILE = 256;
const MAX_ZOOM = 18;
const TILE_WAIT_MS = 8000;
// Page layout, in points (US Letter with half-inch margins).
const MARGIN = 36;
const MAP_W = 540;
const MAP_H = 385;
/** Map image resolution: CSS pixels per point, and canvas pixels per CSS pixel. */
const PX_PER_PT = 96 / 72;
const DENSITY = 2;
// Colors from styles.css and course-map.ts.
const BRAND = '#00008b';
const MUTED = '#6b6b70';
const ACCENT = '#2f5bd3';
const START = '#e8710a';
const STOP = '#00bfff';
const FG = '#1d1d1f';
const BORDER = '#d9d9de';

/** Zoom-0 pixel position of a point (256 px world). */
export function project(p: LatLon): { x: number; y: number } {
  const lat = Math.max(-85.0511, Math.min(85.0511, p.lat));
  const s = Math.sin((lat * Math.PI) / 180);
  return {
    x: ((p.lon + 180) / 360) * TILE,
    y: (0.5 - Math.log((1 + s) / (1 - s)) / (4 * Math.PI)) * TILE,
  };
}

/** The view that fits all points in width x height pixels with padding. */
export function fitView(points: LatLon[], width: number, height: number, pad = 24): MapView {
  const xy = points.map(project);
  const xs = xy.map((p) => p.x);
  const ys = xy.map((p) => p.y);
  const [x0, x1, y0, y1] = [Math.min(...xs), Math.max(...xs), Math.min(...ys), Math.max(...ys)];
  const fit = Math.min((width - 2 * pad) / (x1 - x0), (height - 2 * pad) / (y1 - y0));
  const zoom = Number.isFinite(fit) ? Math.max(0, Math.min(MAX_ZOOM, Math.log2(fit))) : 15;
  return { zoom, cx: (x0 + x1) / 2, cy: (y0 + y1) / 2, width, height };
}

/** Where a point lands on the view's canvas, in pixels from its top left. */
export function toPixel(v: MapView, p: LatLon): { x: number; y: number } {
  const q = project(p);
  const k = 2 ** v.zoom;
  return { x: (q.x - v.cx) * k + v.width / 2, y: (q.y - v.cy) * k + v.height / 2 };
}

/** The tiles covering a view: whole-zoom tiles, drawn at size px each. */
export function tilesFor(v: MapView): { z: number; size: number; tiles: TilePos[] } {
  const z = Math.min(MAX_ZOOM, Math.ceil(v.zoom - 1e-9));
  const size = TILE * 2 ** (v.zoom - z);
  const n = 2 ** z;
  // The canvas's top left, in pixels of the tile zoom.
  const left = v.cx * n - (v.width / 2 / size) * TILE;
  const top = v.cy * n - (v.height / 2 / size) * TILE;
  const tiles: TilePos[] = [];
  for (let ty = Math.floor(top / TILE); ty * TILE < top + (v.height / size) * TILE; ty++) {
    if (ty < 0 || ty >= n) continue;
    for (let tx = Math.floor(left / TILE); tx * TILE < left + (v.width / size) * TILE; tx++) {
      tiles.push({
        x: ((tx % n) + n) % n,
        y: ty,
        left: ((tx * TILE - left) / TILE) * size,
        top: ((ty * TILE - top) / TILE) * size,
      });
    }
  }
  return { z, size, tiles };
}

interface TilePos {
  x: number;
  y: number;
  /** Where the tile's top left goes on the canvas. */
  left: number;
  top: number;
}

/**
 * Standard PDF fonts only cover Latin-1, so swap the few symbols we use for
 * plain ones and drop anything else they can't show.
 */
export function pdfText(s: string): string {
  return s
    .replace(/→/g, 'to')
    .replace(/[–—]/g, '-')
    .replace(/›/g, '>')
    .replace(/[^\n\x20-\x7e\xa0-\xff]/g, '');
}

/** Every point worth fitting on the map: course, stops, routes and start. */
function scenePoints(m: MapScene): LatLon[] {
  const pts: LatLon[] = [...m.course, ...m.stops.map((s) => s.node.pos)];
  if (m.start) pts.push(m.start);
  for (const st of m.legs.flat()) pts.push(st.fromPos, st.toPos, ...(st.path ?? []));
  return pts;
}

function loadTile(url: string): Promise<HTMLImageElement | null> {
  return new Promise((resolve) => {
    const img = new Image();
    // OSM sends CORS headers, so the canvas stays exportable.
    img.crossOrigin = 'anonymous';
    img.onload = () => resolve(img);
    img.onerror = () => resolve(null);
    img.src = url;
  });
}

/** Draws the map at w x h CSS pixels and returns it as a JPEG data URL. */
export async function renderMap(m: MapScene, w: number, h: number): Promise<string> {
  const canvas = document.createElement('canvas');
  canvas.width = Math.round(w * DENSITY);
  canvas.height = Math.round(h * DENSITY);
  const ctx = canvas.getContext('2d')!;
  ctx.scale(DENSITY, DENSITY);
  ctx.fillStyle = '#dddddd';
  ctx.fillRect(0, 0, w, h);

  const pts = scenePoints(m);
  const view = fitView(pts.length ? pts : [{ lat: 39.5, lon: -98.35 }], w, h);
  const { z, size, tiles } = tilesFor(view);
  const timeout = new Promise<null>((r) => setTimeout(() => r(null), TILE_WAIT_MS));
  const images = await Promise.all(
    tiles.map((t, i) =>
      Promise.race([
        loadTile(
          TILE_URL.replace('{s}', 'abc'[i % 3])
            .replace('{z}', String(z))
            .replace('{x}', String(t.x))
            .replace('{y}', String(t.y)),
        ),
        timeout,
      ]),
    ),
  );
  // The same gray base map as on screen, so line colors stand out.
  ctx.filter = 'grayscale(1) brightness(1.05) contrast(0.9)';
  images.forEach((img, i) => {
    // Overlap by a hair so tile seams don't show.
    if (img) ctx.drawImage(img, tiles[i].left, tiles[i].top, size + 0.5, size + 0.5);
  });
  ctx.filter = 'none';

  drawScene(ctx, view, m);
  drawAttribution(ctx, w, h);
  return canvas.toDataURL('image/jpeg', 0.9);
}

function drawScene(ctx: CanvasRenderingContext2D, v: MapView, m: MapScene): void {
  const px = (p: LatLon) => toPixel(v, p);
  const line = (pts: LatLon[], color: string, width: number, dash: number[] = [], alpha = 1) => {
    if (pts.length < 2) return;
    ctx.save();
    ctx.globalAlpha = alpha;
    ctx.strokeStyle = color;
    ctx.lineWidth = width;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.setLineDash(dash);
    ctx.beginPath();
    pts.map(px).forEach((q, i) => (i ? ctx.lineTo(q.x, q.y) : ctx.moveTo(q.x, q.y)));
    ctx.stroke();
    ctx.restore();
  };
  const dot = (p: LatLon, r: number, fill: string, ring: string, ringWidth: number) => {
    const q = px(p);
    ctx.beginPath();
    ctx.arc(q.x, q.y, r, 0, 2 * Math.PI);
    ctx.fillStyle = fill;
    ctx.fill();
    ctx.lineWidth = ringWidth;
    ctx.strokeStyle = ring;
    ctx.stroke();
  };
  const badge = (p: LatLon, text: string, fill: string, color: string) => {
    dot(p, 12, fill, '#ffffff', 2);
    const q = px(p);
    ctx.fillStyle = color;
    ctx.font = '600 12px system-ui, sans-serif';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(text, q.x, q.y + 0.5);
  };

  // Course on a white casing, as on screen.
  line(m.course, '#ffffff', 8, [], 0.9);
  line(m.course, m.courseColor, 4);
  if (m.course.length >= 2) {
    dot(m.course[0], 5, '#00897b', '#00897b', 2);
    dot(m.course[m.course.length - 1], 5, FG, FG, 2);
  }

  // Legs: walks dashed black, rides in the line's color, straight dashes otherwise.
  let prev = m.start;
  m.stops.forEach((s, i) => {
    const steps = m.legs[i] ?? [];
    if (steps.length === 0 && prev) line([prev, s.node.pos], LEG, 3, [6, 6]);
    for (const st of steps) {
      const via = (st.via ?? []).map((x) => x.pos);
      const pts = st.path && st.path.length >= 2 ? st.path : [st.fromPos, ...via, st.toPos];
      if (st.kind === 'walk') line(pts, WALK, 3, [3, 5]);
      else if (st.kind === 'subway') {
        const c = lineColor(st.color) ?? LEG;
        line(pts, c, 4, [], 0.85);
        for (const p of [st.fromPos, ...via, st.toPos]) dot(p, 4, '#ffffff', c, 2);
      } else line(pts, LEG, 3, [6, 6]);
    }
    prev = s.node.pos;
  });

  if (m.start) badge(m.start, '★', START, '#ffffff');
  m.stops.forEach((s, i) => badge(s.node.pos, String(i + 1), STOP, FG));
}

function drawAttribution(ctx: CanvasRenderingContext2D, w: number, h: number): void {
  ctx.font = '10px system-ui, sans-serif';
  const tw = ctx.measureText(TILE_ATTRIBUTION).width;
  ctx.fillStyle = 'rgba(255, 255, 255, 0.85)';
  ctx.fillRect(w - tw - 10, h - 16, tw + 10, 16);
  ctx.fillStyle = FG;
  ctx.textAlign = 'right';
  ctx.textBaseline = 'middle';
  ctx.fillText(TILE_ATTRIBUTION, w - 5, h - 8);
}

/** Builds the PDF and hands it to the browser as a download. */
@Injectable({ providedIn: 'root' })
export class PlanPdf {
  async save(plan: PdfPlan, filename = 'cheersquad-plan.pdf'): Promise<void> {
    const [{ jsPDF }, { autoTable }, map] = await Promise.all([
      import('jspdf'),
      import('jspdf-autotable'),
      renderMap(plan.map, MAP_W * PX_PER_PT, MAP_H * PX_PER_PT),
    ]);
    const doc = new jsPDF({ unit: 'pt', format: 'letter' });
    let y = MARGIN;

    doc.setFont('helvetica', 'bold').setFontSize(22).setTextColor(BRAND);
    doc.text('Cheer Squad', MARGIN, y + 16);
    y += 30;
    doc.setFont('helvetica', 'normal').setFontSize(10).setTextColor(FG);
    doc.text(pdfText(plan.summary), MARGIN, y + 8);
    y += 18;

    doc.addImage(map, 'JPEG', MARGIN, y, MAP_W, MAP_H);
    doc.link(MARGIN + MAP_W - 130, y + MAP_H - 12, 130, 12, {
      url: 'https://www.openstreetmap.org/copyright',
    });
    y += MAP_H + 24;

    doc.setFont('helvetica', 'bold').setFontSize(16).setTextColor(FG);
    doc.text(pdfText(plan.heading), MARGIN, y);
    y += 8;
    if (plan.routing) {
      doc.setFont('helvetica', 'normal').setFontSize(8).setTextColor(MUTED);
      doc.text(pdfText(plan.routing), MARGIN, y + 8);
      y += 12;
    }

    // Each stop is a row, with how to get there in a full-width row under it.
    const body: { content: string; colSpan?: number; styles?: object }[][] = [];
    const links: (string | undefined)[] = [];
    const ruled: boolean[] = [];
    for (const r of plan.rows) {
      body.push(r.cells.map((c) => ({ content: pdfText(c) })));
      links.push(r.link);
      ruled.push(!r.steps);
      if (r.steps) {
        body.push([
          { content: '' },
          {
            content: pdfText(r.steps),
            colSpan: r.cells.length - 1,
            styles: { fontSize: 8, textColor: MUTED },
          },
        ]);
        links.push(undefined);
        ruled.push(true);
      }
    }
    autoTable(doc, {
      startY: y + 4,
      margin: { left: MARGIN, right: MARGIN },
      head: [plan.columns.map(pdfText)],
      body,
      theme: 'plain',
      styles: { font: 'helvetica', fontSize: 9, textColor: FG, cellPadding: 4 },
      headStyles: {
        textColor: MUTED,
        fontStyle: 'normal',
        lineWidth: { bottom: 0.5 },
        lineColor: BORDER,
      },
      rowPageBreak: 'avoid',
      didParseCell: (d) => {
        if (d.section !== 'body') return;
        // A divider after each stop and its steps, as on screen.
        if (ruled[d.row.index]) {
          d.cell.styles.lineWidth = { bottom: 0.5 };
          d.cell.styles.lineColor = BORDER;
        }
        // The distance links to Google Maps, as on screen.
        if (d.column.index === 1 && links[d.row.index]) d.cell.styles.textColor = ACCENT;
      },
      didDrawCell: (d) => {
        const url = d.section === 'body' && d.column.index === 1 && links[d.row.index];
        if (url) doc.link(d.cell.x, d.cell.y, d.cell.width, d.cell.height, { url });
      },
    });
    const end = (doc as unknown as { lastAutoTable: { finalY: number } }).lastAutoTable.finalY;
    doc.setFont('helvetica', 'normal').setFontSize(8).setTextColor(MUTED);
    const note = doc.splitTextToSize(pdfText(plan.note), MAP_W);
    if (end + 24 > doc.internal.pageSize.getHeight() - MARGIN) {
      doc.addPage();
      doc.text(note, MARGIN, MARGIN + 8);
    } else doc.text(note, MARGIN, end + 16);

    doc.save(filename);
  }
}
