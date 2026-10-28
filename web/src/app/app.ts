import { Component, computed, inject, signal } from '@angular/core';
import { NonNullableFormBuilder, ReactiveFormsModule } from '@angular/forms';
import { CourseMap, DEFAULT_COURSE_COLOR } from './course-map';
import {
  buildParams,
  formatClock,
  formatMinutes,
  LatLon,
  legSteps,
  lineColor,
  MapMarks,
  METERS_PER_MILE,
  parseClock,
  parseGpxPoints,
  PlanForm,
  PlanResponse,
  Step,
  stepDetail,
  stepText,
  textOn,
} from './plan';
import { PlanService } from './plan.service';

type ClickMode = 'start' | 'pin' | 'exclude' | 'crossing';

const COURSE_COLOR_KEY = 'cheersquad.courseColor';

/** The remembered course color; storage can be blocked, so fall back quietly. */
function savedCourseColor(): string {
  try {
    const c = localStorage.getItem(COURSE_COLOR_KEY);
    if (c && /^#[0-9a-f]{6}$/i.test(c)) return c;
  } catch {}
  return DEFAULT_COURSE_COLOR;
}

@Component({
  selector: 'app-root',
  imports: [ReactiveFormsModule, CourseMap],
  templateUrl: './app.html',
  styleUrl: './app.css',
})
export class App {
  private readonly api = inject(PlanService);
  private readonly fb = inject(NonNullableFormBuilder);

  readonly form = this.fb.group({
    goalFinish: '4:00:00',
    splitPercent: 0,
    startDelayMinutes: 0,
    uncertaintyPercent: 5,
    distance: 'marathon' as PlanForm['distance'],
    customMiles: this.fb.control<number | null>(null),
    gunTime: '07:00',
    spectatorStart: '06:45',
    mode: 'walk' as PlanForm['mode'],
    bufferMinutes: 3,
    maxStops: 0,
    lateMilesFrom: this.fb.control<number | null>(null),
    lateMilesWeight: 2,
  });

  readonly clickModes: { value: ClickMode; label: string }[] = [
    { value: 'start', label: 'My start point' },
    { value: 'pin', label: 'Pin a spot' },
    { value: 'exclude', label: 'Exclude a spot' },
    { value: 'crossing', label: 'Crossing point' },
  ];

  readonly gpxFile = signal<File | null>(null);
  readonly course = signal<LatLon[]>([]);
  readonly marks = signal<MapMarks>({ start: null, pinned: [], excluded: [], crossings: [] });
  readonly clickMode = signal<ClickMode>('start');
  readonly result = signal<PlanResponse | null>(null);
  readonly error = signal<string | null>(null);
  readonly loading = signal(false);
  readonly courseColor = signal(savedCourseColor());
  readonly stops = computed(() => this.result()?.plan.stops ?? []);
  /** Index-aligned with stops; empty legs when the server sends none. */
  readonly legs = computed(() => {
    const r = this.result();
    return r ? r.plan.stops.map((_, i) => legSteps(r, i)) : [];
  });

  setCourseColor(color: string): void {
    this.courseColor.set(color);
    try {
      localStorage.setItem(COURSE_COLOR_KEY, color);
    } catch {}
  }

  async onFile(event: Event): Promise<void> {
    const file = (event.target as HTMLInputElement).files?.[0] ?? null;
    this.gpxFile.set(file);
    this.result.set(null);
    this.error.set(null);
    if (!file) {
      this.course.set([]);
      return;
    }
    const points = parseGpxPoints(await file.text());
    this.course.set(points);
    if (points.length < 2) this.error.set('That file has no track points. Is it a GPX course?');
  }

  onMapClick(p: LatLon): void {
    this.marks.update((m) => {
      switch (this.clickMode()) {
        case 'start':
          return { ...m, start: p };
        case 'pin':
          return { ...m, pinned: [...m.pinned, p] };
        case 'exclude':
          return { ...m, excluded: [...m.excluded, p] };
        case 'crossing':
          return { ...m, crossings: [...m.crossings, p] };
      }
    });
  }

  clearMarks(): void {
    this.marks.update((m) => ({ start: m.start, pinned: [], excluded: [], crossings: [] }));
  }

  submit(): void {
    const file = this.gpxFile();
    if (!file) {
      this.error.set('Choose a GPX file first.');
      return;
    }
    const params = buildParams(this.form.getRawValue(), this.marks());
    if (typeof params === 'string') {
      this.error.set(params);
      return;
    }
    this.error.set(null);
    this.loading.set(true);
    this.api.plan(file, params).subscribe({
      next: (r) => {
        this.result.set(r);
        this.loading.set(false);
        if (r.plan.stops.length === 0) {
          this.error.set(
            'No spot is reachable in time. Try setting off earlier or a faster travel mode.',
          );
        }
      },
      error: (e: Error) => {
        this.error.set(e.message);
        this.loading.set(false);
      },
    });
  }

  // Display helpers.
  clock(sinceGun: number): string {
    return formatClock(sinceGun, parseClock(this.form.controls.gunTime.value));
  }
  minutes(s: number): string {
    return formatMinutes(s);
  }
  mile(meters: number): string {
    return (meters / METERS_PER_MILE).toFixed(1);
  }
  stepText(s: Step): string {
    return stepText(s);
  }
  stepDetail(s: Step): string {
    return stepDetail(s);
  }
  /** Badge colors for a subway line; null leaves the CSS fallback (accent). */
  lineBg(s: Step): string | null {
    return lineColor(s.color);
  }
  lineFg(s: Step): string | null {
    const bg = lineColor(s.color);
    return bg ? textOn(bg) : null;
  }
  mapsLink(p: LatLon): string {
    return `https://www.google.com/maps/search/?api=1&query=${p.lat},${p.lon}`;
  }
}
