import {
  AfterViewInit,
  Component,
  effect,
  ElementRef,
  input,
  OnDestroy,
  output,
  viewChild,
} from '@angular/core';
import * as L from 'leaflet';
import {
  DistanceUnit,
  EXCLUDE_RADIUS,
  formatDistance,
  LatLon,
  lineColor,
  MapMarks,
  Step,
  stepText,
  Stop,
} from './plan';

// Leaflet writes path colors as SVG attributes, which can't read CSS variables,
// so these are literals from the sweat-equity palette in styles.css:
// start #00897b, finish and non-subway legs = --fg, walks black, excluded = --muted.
// Subway steps use the feed's line color, so the course is a bright blue on a
// white casing over grayscale tiles to stay distinct from transit colors.
export const DEFAULT_COURSE_COLOR = '#0a5cff';
export const LEG = '#1d1d1f';
export const WALK = '#000000';
export const TILE_URL = 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png';
export const TILE_ATTRIBUTION = '© OpenStreetMap contributors';
/** How long to wait for map tiles before printing anyway. */
const PRINT_TILE_WAIT_MS = 4000;

/**
 * Leaflet map showing the course, the user's marks (start, pinned spots,
 * excluded areas, crossing points) and the planned stops. Clicks are emitted
 * so the parent decides what a click means.
 */
@Component({
  selector: 'app-course-map',
  template: '<div #map class="map"></div>',
  // print-size is the printed map's size, applied on screen while tiles load
  // for the PDF so Leaflet lays out exactly what the page will show.
  styles: `
    :host {
      display: block;
    }
    .map {
      height: 100%;
      min-height: 420px;
      border: 1px solid var(--border);
      border-radius: 8px;
    }
    .map.print-size {
      width: 7in;
      height: 5in;
      min-height: 0;
    }
    @media print {
      .map {
        width: 7in;
        height: 5in;
        min-height: 0;
      }
    }
  `,
})
export class CourseMap implements AfterViewInit, OnDestroy {
  readonly course = input<LatLon[]>([]);
  readonly marks = input<MapMarks>({ start: null, pinned: [], excluded: [], crossings: [] });
  readonly stops = input<Stop[]>([]);
  /** legs[i] is how to get to stops[i]; a missing or empty leg is drawn as a straight line. */
  readonly legs = input<Step[][]>([]);
  readonly unit = input<DistanceUnit>('mi');
  readonly courseColor = input(DEFAULT_COURSE_COLOR);
  readonly mapClick = output<LatLon>();

  private readonly el = viewChild.required<ElementRef<HTMLDivElement>>('map');
  private map?: L.Map;
  private readonly courseLayer = L.layerGroup();
  private readonly markLayer = L.layerGroup();
  private readonly stopLayer = L.layerGroup();
  private courseLine?: L.Polyline;
  /** The on-screen view to return to after printing. */
  private screenView?: { center: L.LatLng; zoom: number };

  constructor() {
    effect(() => this.drawCourse(this.course()));
    // Recolor in place so changing the color doesn't refit the map.
    effect(() => this.courseLine?.setStyle({ color: this.courseColor() }));
    effect(() => this.drawMarks(this.marks()));
    effect(() => this.drawStops(this.stops(), this.legs(), this.unit()));
  }

  ngAfterViewInit(): void {
    this.map = L.map(this.el().nativeElement, { center: [39.5, -98.35], zoom: 4 });
    const osm = (className: string) =>
      L.tileLayer(TILE_URL, {
        maxZoom: 19,
        className,
        attribution:
          '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
      });
    // The same OSM tiles, desaturated by CSS (styles.css) so line colors stand out.
    const gray = osm('tiles-gray').addTo(this.map);
    L.control.layers({ 'Gray map': gray, 'Color map': osm('tiles-color') }).addTo(this.map);
    this.courseLayer.addTo(this.map);
    this.stopLayer.addTo(this.map);
    this.markLayer.addTo(this.map);
    this.map.on('click', (e: L.LeafletMouseEvent) =>
      this.mapClick.emit({ lat: e.latlng.lat, lon: e.latlng.lng }),
    );
    this.drawCourse(this.course());
  }

  ngOnDestroy(): void {
    this.map?.remove();
  }

  /**
   * Resizes the map to its printed size, frames the course and plan, and
   * resolves once the tiles have loaded (or after a short wait).
   */
  async fitForPrint(): Promise<void> {
    const map = this.map;
    if (!map) return;
    const el = this.el().nativeElement;
    if (!this.screenView) this.screenView = { center: map.getCenter(), zoom: map.getZoom() };
    el.classList.add('print-size');
    map.invalidateSize({ pan: false });
    const b = printBounds(this.course(), this.stops(), this.legs(), this.marks().start);
    // Whole zoom levels can leave the course filling under half the page, so
    // allow quarter steps just for this fit.
    const snap = map.options.zoomSnap;
    map.options.zoomSnap = 0.25;
    if (b) map.fitBounds(b, { padding: [24, 24], animate: false });
    map.options.zoomSnap = snap;
    await tilesLoaded(map, PRINT_TILE_WAIT_MS);
  }

  /** Puts the map back the way it was on screen before fitForPrint. */
  restoreAfterPrint(): void {
    const map = this.map;
    if (!map || !this.screenView) return;
    this.el().nativeElement.classList.remove('print-size');
    map.invalidateSize({ pan: false });
    map.setView(this.screenView.center, this.screenView.zoom, { animate: false });
    this.screenView = undefined;
  }

  private drawCourse(points: LatLon[]): void {
    this.courseLayer.clearLayers();
    this.courseLine = undefined;
    if (points.length < 2) return;
    const latlngs = points.map((p) => L.latLng(p.lat, p.lon));
    L.polyline(latlngs, { color: '#ffffff', weight: 8, opacity: 0.9 }).addTo(this.courseLayer);
    const line = L.polyline(latlngs, { color: this.courseColor(), weight: 4 }).addTo(
      this.courseLayer,
    );
    this.courseLine = line;
    L.circleMarker(latlngs[0], { radius: 6, color: '#00897b', fillOpacity: 1 })
      .bindTooltip('Start line')
      .addTo(this.courseLayer);
    L.circleMarker(latlngs[latlngs.length - 1], { radius: 6, color: '#1d1d1f', fillOpacity: 1 })
      .bindTooltip('Finish')
      .addTo(this.courseLayer);
    this.map?.fitBounds(line.getBounds(), { padding: [20, 20] });
  }

  private drawMarks(m: MapMarks): void {
    this.markLayer.clearLayers();
    if (m.start) {
      L.marker([m.start.lat, m.start.lon], { icon: badge('★', 'start') })
        .bindTooltip('You start here')
        .addTo(this.markLayer);
    }
    for (const p of m.pinned) {
      L.marker([p.lat, p.lon], { icon: badge('♥', 'pin') })
        .bindTooltip('Pinned spot')
        .addTo(this.markLayer);
    }
    for (const p of m.excluded) {
      L.circle([p.lat, p.lon], { radius: EXCLUDE_RADIUS, color: '#6b6b70', dashArray: '4' })
        .bindTooltip('Excluded')
        .addTo(this.markLayer);
    }
    for (const p of m.crossings) {
      L.marker([p.lat, p.lon], { icon: badge('✕', 'cross') })
        .bindTooltip('Crossing point')
        .addTo(this.markLayer);
    }
  }

  private drawStops(stops: Stop[], legs: Step[][], unit: DistanceUnit): void {
    this.stopLayer.clearLayers();
    const start = this.marks().start;
    let prev: L.LatLng | null = start ? L.latLng(start.lat, start.lon) : null;
    stops.forEach((s, i) => {
      const here = L.latLng(s.node.pos.lat, s.node.pos.lon);
      const steps = legs[i] ?? [];
      if (steps.length > 0) {
        for (const st of steps) this.drawStep(st);
      } else if (prev) {
        L.polyline([prev, here], { color: LEG, weight: 3, dashArray: '6 6' }).addTo(this.stopLayer);
      }
      L.marker(here, { icon: badge(String(i + 1), 'stop') })
        .bindTooltip(
          `Stop ${i + 1}: ${unit === 'km' ? 'km' : 'mile'} ${formatDistance(s.node.dist, unit)}`,
        )
        .addTo(this.stopLayer);
      prev = here;
    });
  }

  /** Walks are dashed black lines; rides are solid in the line's color. */
  private drawStep(st: Step): void {
    const via = st.via ?? [];
    // Rides follow the track when the feed has shapes, otherwise a straight
    // line through each stop the train makes on the way.
    const track = st.path && st.path.length >= 2 ? st.path : null;
    const ends: L.LatLngExpression[] = track
      ? track.map((p): L.LatLngExpression => [p.lat, p.lon])
      : [
          [st.fromPos.lat, st.fromPos.lon],
          ...via.map((v): L.LatLngExpression => [v.pos.lat, v.pos.lon]),
          [st.toPos.lat, st.toPos.lon],
        ];
    let style: L.PolylineOptions;
    switch (st.kind) {
      case 'walk':
        style = { color: WALK, weight: 3, dashArray: '3 5' };
        break;
      case 'subway':
        style = { color: lineColor(st.color) ?? LEG, weight: 4, opacity: 0.85 };
        break;
      default:
        style = { color: LEG, weight: 3, dashArray: '6 6' };
    }
    // Station names come from the feed; set them as text, not as tooltip HTML.
    const label = document.createElement('span');
    label.textContent = stepText(st);
    L.polyline(ends, style).bindTooltip(label).addTo(this.stopLayer);
    if (st.kind !== 'subway') return;
    const ring = style.color ?? LEG;
    const stops = [
      { name: st.from ?? '', pos: st.fromPos },
      ...via,
      { name: st.to ?? '', pos: st.toPos },
    ];
    for (const s of stops) {
      const name = document.createElement('span');
      name.textContent = s.name;
      const dot = L.circleMarker([s.pos.lat, s.pos.lon], {
        radius: 4,
        color: ring,
        weight: 2,
        fillColor: '#ffffff',
        fillOpacity: 1,
      }).addTo(this.stopLayer);
      if (s.name) dot.bindTooltip(name);
    }
  }
}

/** Everything worth showing on the printed map: course, stops, routes and start. */
export function printBounds(
  course: LatLon[],
  stops: Stop[],
  legs: Step[][],
  start: LatLon | null,
): L.LatLngBounds | null {
  const pts: LatLon[] = [...course, ...stops.map((s) => s.node.pos)];
  if (start) pts.push(start);
  for (const st of legs.flat()) pts.push(st.fromPos, st.toPos, ...(st.path ?? []));
  if (pts.length === 0) return null;
  return L.latLngBounds(pts.map((p) => L.latLng(p.lat, p.lon)));
}

/**
 * Resolves once no tile layer is loading, or after ms. Polls rather than
 * waiting for 'load', because Leaflet requests the tiles for a new view a
 * frame or so after the view changes, and a layer still finishing the old
 * view's tiles would fire 'load' too early.
 */
function tilesLoaded(map: L.Map, ms: number): Promise<void> {
  const busy = () => {
    let loading = false;
    map.eachLayer((l) => {
      if (l instanceof L.TileLayer && l.isLoading()) loading = true;
    });
    return loading;
  };
  const deadline = Date.now() + ms;
  return new Promise((resolve) => {
    const check = () => {
      if (!busy() || Date.now() >= deadline) resolve();
      else setTimeout(check, 100);
    };
    setTimeout(check, 100);
  });
}

/** A round text badge; avoids Leaflet's default image icons, which need asset paths. */
function badge(text: string, kind: string): L.DivIcon {
  return L.divIcon({
    className: `badge badge-${kind}`,
    html: `<span>${text}</span>`,
    iconSize: [26, 26],
    iconAnchor: [13, 13],
  });
}
