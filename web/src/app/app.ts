import { Component, computed, inject, OnInit, signal, viewChild } from '@angular/core';
import { NonNullableFormBuilder, ReactiveFormsModule } from '@angular/forms';
import { CourseMap, DEFAULT_COURSE_COLOR } from './course-map';
import {
  compactGpx,
  COMPACT_GPX_OVER,
  buildParams,
  convertDistance,
  DistanceUnit,
  formatDistance,
  formatClock,
  formatMinutes,
  LatLon,
  legSteps,
  lineColor,
  MapMarks,
  parseClock,
  parseGpxPoints,
  PlanForm,
  PlanResponse,
  Race,
  Step,
  stepDetail,
  stepText,
  textOn,
} from './plan';
import { PdfPlan, PlanPdf } from './pdf-export';
import { PlanService } from './plan.service';

type ClickMode = 'start' | 'pin' | 'exclude' | 'crossing';

const COURSE_COLOR_KEY = 'cheersquad.courseColor';
const UNIT_KEY = 'cheersquad.unit';

const MODE_TEXT: Record<PlanForm['mode'], string> = {
  walk: 'walking',
  bike: 'by bike',
  transit: 'by transit',
  drive: 'by car',
};

/** The remembered course color; storage can be blocked, so fall back quietly. */
function savedCourseColor(): string {
  try {
    const c = localStorage.getItem(COURSE_COLOR_KEY);
    if (c && /^#[0-9a-f]{6}$/i.test(c)) return c;
  } catch {}
  return DEFAULT_COURSE_COLOR;
}

/** The remembered distance unit, miles unless kilometers was chosen. */
function savedUnit(): DistanceUnit {
  try {
    if (localStorage.getItem(UNIT_KEY) === 'km') return 'km';
  } catch {}
  return 'mi';
}

/**
 * Puts the map back once the user next touches the page. Chrome fires
 * afterprint while its print dialog is still open and re-renders the preview
 * from the live page when a setting changes, so restoring on afterprint
 * printed a map whose tiles were still loading.
 */
function restoreOnNextInput(map: CourseMap | undefined): void {
  const events = ['pointerdown', 'keydown', 'wheel'] as const;
  const restore = () => {
    for (const e of events) window.removeEventListener(e, restore, true);
    map?.restoreAfterPrint();
  };
  for (const e of events) window.addEventListener(e, restore, { capture: true, passive: true });
}

@Component({
  selector: 'app-root',
  imports: [ReactiveFormsModule, CourseMap],
  templateUrl: './app.html',
  styleUrl: './app.css',
})
export class App implements OnInit {
  private readonly api = inject(PlanService);
  private readonly fb = inject(NonNullableFormBuilder);
  private readonly map = viewChild(CourseMap);
  private readonly pdf = inject(PlanPdf);

  readonly form = this.fb.group({
    goalFinish: '4:00:00',
    splitPercent: 0,
    startDelayMinutes: 0,
    uncertaintyPercent: 5,
    distance: 'marathon' as PlanForm['distance'],
    unit: savedUnit(),
    customDistance: this.fb.control<number | null>(null),
    gunTime: '07:00',
    spectatorStart: '06:45',
    mode: 'walk' as PlanForm['mode'],
    raceDay: 'sunday' as PlanForm['raceDay'],
    bufferMinutes: 3,
    maxStops: 0,
    lateFrom: this.fb.control<number | null>(null),
    lateWeight: 2,
  });

  readonly clickModes: { value: ClickMode; label: string }[] = [
    { value: 'start', label: 'My start point' },
    { value: 'pin', label: 'Pin a spot' },
    { value: 'exclude', label: 'Exclude a spot' },
    { value: 'crossing', label: 'Crossing point' },
  ];

  readonly races = signal<Race[]>([]);
  readonly raceId = signal('');
  readonly gpxFile = signal<File | null>(null);
  readonly course = signal<LatLon[]>([]);
  readonly marks = signal<MapMarks>({ start: null, pinned: [], excluded: [], crossings: [] });
  readonly clickMode = signal<ClickMode>('start');
  readonly result = signal<PlanResponse | null>(null);
  readonly error = signal<string | null>(null);
  readonly loading = signal(false);
  /** True while the map loads tiles at print size, before the print dialog opens. */
  readonly preparingPrint = signal(false);
  /** True while the PDF download is being drawn. */
  readonly exportingPdf = signal(false);
  /** Which course, the runner's goal, and how you travel, for the printed plan's header. */
  readonly printTitle = signal('');
  readonly courseColor = signal(savedCourseColor());
  readonly unit = signal<DistanceUnit>(this.form.controls.unit.value);
  readonly unitOptions: { value: DistanceUnit; label: string }[] = [
    { value: 'mi', label: 'Miles' },
    { value: 'km', label: 'Kilometers' },
  ];
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

  /** Switches the shown unit and converts distances already typed in. */
  setUnit(u: DistanceUnit): void {
    const from = this.unit();
    if (u === from) return;
    for (const c of [this.form.controls.customDistance, this.form.controls.lateFrom]) {
      if (c.value) c.setValue(convertDistance(c.value, from, u));
    }
    this.form.controls.unit.setValue(u);
    this.unit.set(u);
    try {
      localStorage.setItem(UNIT_KEY, u);
    } catch {}
  }

  ngOnInit(): void {
    // The race list is optional: on failure the dropdown just stays hidden.
    this.api.races().subscribe({
      next: (r) => this.races.set(r ?? []),
      error: () => this.races.set([]),
    });
  }

  onFile(event: Event): Promise<void> {
    const input = event.target as HTMLInputElement;
    this.raceId.set('');
    return this.loadCourse(input.files?.[0] ?? null);
  }

  onRace(event: Event): void {
    const id = (event.target as HTMLSelectElement).value;
    this.raceId.set(id);
    if (!id) return;
    const race = this.races().find((r) => r.id === id);
    this.form.controls.raceDay.setValue(race?.day ?? 'sunday');
    this.error.set(null);
    this.api.raceGpx(id).subscribe({
      next: (gpx) => {
        if (this.raceId() !== id) return; // superseded by a later choice
        void this.loadCourse(new File([gpx], `${id}.gpx`, { type: 'application/gpx+xml' }));
      },
      error: (e: Error) => {
        if (this.raceId() === id) this.error.set(e.message);
      },
    });
  }

  /**
   * Shared by uploads and preloaded races: sets the file and draws its course.
   * A new course drops the start point, pins, exclusions, crossings and the
   * old plan, which all belong to the previous map.
   */
  async loadCourse(file: File | null): Promise<void> {
    this.gpxFile.set(file);
    this.marks.set({ start: null, pinned: [], excluded: [], crossings: [] });
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
    // Send big recordings as just their points, which also fit the
    // server's upload limit.
    const upload =
      file.size > COMPACT_GPX_OVER
        ? new File([compactGpx(this.course())], file.name, { type: 'application/gpx+xml' })
        : file;
    this.error.set(null);
    this.loading.set(true);
    this.api.plan(upload, params).subscribe({
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

  /**
   * Prints the plan through the browser's print dialog (which can also save
   * a PDF). The print stylesheet keeps just the map and the sightings table.
   */
  async printPlan(): Promise<void> {
    if (this.preparingPrint()) return;
    this.printTitle.set(this.planSummary());
    this.preparingPrint.set(true);
    const map = this.map();
    try {
      await map?.fitForPrint();
    } finally {
      this.preparingPrint.set(false);
    }
    window.addEventListener('afterprint', () => restoreOnNextInput(map), { once: true });
    window.print();
  }

  /** Downloads the plan as a PDF file, drawn in the page without the print dialog. */
  async exportPdf(): Promise<void> {
    const plan = this.pdfPlan();
    if (!plan || this.exportingPdf()) return;
    this.exportingPdf.set(true);
    this.error.set(null);
    try {
      await this.pdf.save(plan);
    } catch {
      this.error.set("Couldn't make the PDF. Try Print and save as PDF instead.");
    } finally {
      this.exportingPdf.set(false);
    }
  }

  /** The current plan as the PDF's text and map, in the chosen units. */
  pdfPlan(): PdfPlan | null {
    const r = this.result();
    if (!r?.plan.stops.length) return null;
    const n = r.plan.sightings;
    const legs = this.legs();
    return {
      summary: this.planSummary(),
      heading: `${n} sighting${n === 1 ? '' : 's'}`,
      routing:
        [r.routing ? `Routing: ${r.routing}.` : '', r.noTransit ?? ''].join(' ').trim() ||
        undefined,
      columns: [
        '#',
        this.unit() === 'km' ? 'Km' : 'Mile',
        'Leave',
        'Arrive',
        'Runner passes',
        'Wait',
      ],
      rows: r.plan.stops.map((s, i) => ({
        cells: [
          String(i + 1),
          this.distance(s.node.dist),
          this.clock(s.depart),
          `${this.clock(s.arrive)} (${this.minutes(s.travel)} ${r.mode})`,
          `${this.clock(s.node.tExpected)} (${this.clock(s.node.tFast)}–${this.clock(s.node.tSlow)})`,
          this.minutes(s.wait),
        ],
        link: this.mapsLink(s.node.pos),
        steps: legs[i],
      })),
      finish: r.finish && [
        'Finish',
        this.distance(r.course.length),
        'Expected finish',
        '',
        `${this.clock(r.finish.tExpected)} (${this.clock(r.finish.tFast)}–${this.clock(r.finish.tSlow)})`,
        '',
      ],
      note:
        'Leave each spot as soon as your runner goes by. Times shown are for a runner on pace; each hop leaves enough time even if they run faster than expected.' +
        (r.routing ? '' : ' Travel times are straight-line estimates for now, not real routes.'),
      map: {
        course: this.course(),
        stops: r.plan.stops,
        legs,
        start: this.marks().start,
        courseColor: this.courseColor(),
        grayTiles: this.map()?.grayTiles() ?? true,
      },
    };
  }

  /** e.g. "Boston Marathon · goal 3:45:00 · gun 10:00 · by transit from 09:30". */
  planSummary(): string {
    const f = this.form.getRawValue();
    const race =
      this.races().find((r) => r.id === this.raceId())?.name ??
      this.gpxFile()?.name.replace(/\.gpx$/i, '') ??
      'Your plan';
    return [
      race,
      `goal ${f.goalFinish}`,
      `gun ${f.gunTime}`,
      `${MODE_TEXT[f.mode]} from ${f.spectatorStart}`,
    ].join(' · ');
  }

  // Display helpers.
  clock(sinceGun: number): string {
    return formatClock(sinceGun, parseClock(this.form.controls.gunTime.value));
  }
  minutes(s: number): string {
    return formatMinutes(s);
  }
  distance(meters: number): string {
    return formatDistance(meters, this.unit());
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
