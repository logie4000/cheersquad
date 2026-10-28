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
import { EXCLUDE_RADIUS, LatLon, MapMarks, Stop } from './plan';

// Leaflet writes path colors as SVG attributes, which can't read CSS variables,
// so these are literals from the sweat-equity palette in styles.css:
// course #c2185b, legs = --accent #2f5bd3, start #00897b, finish = --fg, excluded = --muted.

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
  readonly mapClick = output<LatLon>();

  private readonly el = viewChild.required<ElementRef<HTMLDivElement>>('map');
  private map?: L.Map;
  private readonly courseLayer = L.layerGroup();
  private readonly markLayer = L.layerGroup();
  private readonly stopLayer = L.layerGroup();

  constructor() {
    effect(() => this.drawCourse(this.course()));
    effect(() => this.drawMarks(this.marks()));
    effect(() => this.drawStops(this.stops()));
  }

  ngAfterViewInit(): void {
    this.map = L.map(this.el().nativeElement, { center: [39.5, -98.35], zoom: 4 });
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      maxZoom: 19,
      attribution:
        '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
    }).addTo(this.map);
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
    if (points.length < 2) return;
    const latlngs = points.map((p) => L.latLng(p.lat, p.lon));
    const line = L.polyline(latlngs, { color: '#c2185b', weight: 4 }).addTo(this.courseLayer);
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

  private drawStops(stops: Stop[]): void {
    this.stopLayer.clearLayers();
    const start = this.marks().start;
    let prev: L.LatLng | null = start ? L.latLng(start.lat, start.lon) : null;
    stops.forEach((s, i) => {
      const here = L.latLng(s.node.pos.lat, s.node.pos.lon);
      if (prev) {
        L.polyline([prev, here], { color: '#2f5bd3', weight: 3, dashArray: '6 6' }).addTo(
          this.stopLayer,
        );
      }
      L.marker(here, { icon: badge(String(i + 1), 'stop') })
        .bindTooltip(`Stop ${i + 1}: mile ${(s.node.dist / 1609.344).toFixed(1)}`)
        .addTo(this.stopLayer);
      prev = here;
    });
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
