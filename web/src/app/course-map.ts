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
import { EXCLUDE_RADIUS, LatLon, lineColor, MapMarks, Step, stepText, Stop } from './plan';

// Leaflet writes path colors as SVG attributes, which can't read CSS variables,
// so these are literals from the sweat-equity palette in styles.css:
// start #00897b, finish and non-subway legs = --fg, walks black, excluded = --muted.
// Subway steps use the feed's line color, so the course is a bright blue on a
// white casing over grayscale tiles to stay distinct from transit colors.
export const DEFAULT_COURSE_COLOR = '#0a5cff';
const LEG = '#1d1d1f';
const WALK = '#000000';

/**
 * Leaflet map showing the course, the user's marks (start, pinned spots,
 * excluded areas, crossing points) and the planned stops. Clicks are emitted
 * so the parent decides what a click means.
 */
@Component({
  selector: 'app-course-map',
  template: '<div #map class="map"></div>',
  styles:
    ':host { display: block; } .map { height: 100%; min-height: 420px; border: 1px solid var(--border); border-radius: 8px; }',
})
export class CourseMap implements AfterViewInit, OnDestroy {
  readonly course = input<LatLon[]>([]);
  readonly marks = input<MapMarks>({ start: null, pinned: [], excluded: [], crossings: [] });
  readonly stops = input<Stop[]>([]);
  /** legs[i] is how to get to stops[i]; a missing or empty leg is drawn as a straight line. */
  readonly legs = input<Step[][]>([]);
  readonly courseColor = input(DEFAULT_COURSE_COLOR);
  readonly mapClick = output<LatLon>();

  private readonly el = viewChild.required<ElementRef<HTMLDivElement>>('map');
  private map?: L.Map;
  private readonly courseLayer = L.layerGroup();
  private readonly markLayer = L.layerGroup();
  private readonly stopLayer = L.layerGroup();
  private courseLine?: L.Polyline;

  constructor() {
    effect(() => this.drawCourse(this.course()));
    // Recolor in place so changing the color doesn't refit the map.
    effect(() => this.courseLine?.setStyle({ color: this.courseColor() }));
    effect(() => this.drawMarks(this.marks()));
    effect(() => this.drawStops(this.stops(), this.legs()));
  }

  ngAfterViewInit(): void {
    this.map = L.map(this.el().nativeElement, { center: [39.5, -98.35], zoom: 4 });
    const osm = (className: string) =>
      L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
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

  private drawStops(stops: Stop[], legs: Step[][]): void {
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
        .bindTooltip(`Stop ${i + 1}: mile ${(s.node.dist / 1609.344).toFixed(1)}`)
        .addTo(this.stopLayer);
      prev = here;
    });
  }

  /** Walks are dashed black lines; rides are solid in the line's color. */
  private drawStep(st: Step): void {
    const ends: L.LatLngExpression[] = [
      [st.fromPos.lat, st.fromPos.lon],
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
  }
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
