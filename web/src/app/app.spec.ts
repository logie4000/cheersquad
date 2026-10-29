import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { By } from '@angular/platform-browser';
import { App } from './app';
import { CourseMap } from './course-map';
import { PlanPdf } from './pdf-export';
import { PlanResponse } from './plan';
import { PlanService } from './plan.service';

const GPX = `<gpx><trk><trkseg>
  <trkpt lat="42.0" lon="-71.0"/><trkpt lat="42.1" lon="-71.1"/>
</trkseg></trk></gpx>`;

describe('App', () => {
  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [App],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
  });

  it('records map clicks according to the selected mode', () => {
    const app = TestBed.createComponent(App).componentInstance;
    app.onMapClick({ lat: 1, lon: 1 });
    app.clickMode.set('pin');
    app.onMapClick({ lat: 2, lon: 2 });
    app.clickMode.set('crossing');
    app.onMapClick({ lat: 3, lon: 3 });
    expect(app.marks()).toEqual({
      start: { lat: 1, lon: 1 },
      pinned: [{ lat: 2, lon: 2 }],
      excluded: [],
      crossings: [{ lat: 3, lon: 3 }],
    });
    app.clearMarks();
    expect(app.marks().pinned).toEqual([]);
    expect(app.marks().start).toEqual({ lat: 1, lon: 1 });
  });

  it('asks for a GPX file before planning', () => {
    const app = TestBed.createComponent(App).componentInstance;
    app.submit();
    expect(app.error()).toContain('GPX');
  });

  describe('preloaded races', () => {
    let http: HttpTestingController;
    beforeEach(() => (http = TestBed.inject(HttpTestingController)));
    afterEach(() => http.verify());

    function render() {
      const fixture = TestBed.createComponent(App);
      fixture.detectChanges();
      return fixture;
    }

    it('lists race names in the Race dropdown', () => {
      const fixture = render();
      http.expectOne('api/races').flush([
        { id: 'boston', name: 'Boston Marathon' },
        { id: 'nyc', name: 'New York City Marathon' },
      ]);
      fixture.detectChanges();
      const select = (fixture.nativeElement as HTMLElement).querySelector('select')!;
      const labels = Array.from(select.options).map((o) => o.textContent!.trim());
      expect(labels).toEqual(['Select a route', 'Boston Marathon', 'New York City Marathon']);
    });

    it('loads the chosen race course from api/races/<id>', async () => {
      const fixture = render();
      http.expectOne('api/races').flush([{ id: 'boston', name: 'Boston Marathon' }]);
      fixture.detectChanges();
      const select = (fixture.nativeElement as HTMLElement).querySelector('select')!;
      select.value = 'boston';
      select.dispatchEvent(new Event('change'));
      const req = http.expectOne('api/races/boston');
      expect(req.request.responseType).toBe('text');
      req.flush(GPX);
      await fixture.whenStable();
      const app = fixture.componentInstance;
      expect(app.gpxFile()?.name).toBe('boston.gpx');
      expect(app.course()).toEqual([
        { lat: 42.0, lon: -71.0 },
        { lat: 42.1, lon: -71.1 },
      ]);
      expect(app.error()).toBeNull();
    });

    it("picks the race's timetable day, Sunday unless it says otherwise", () => {
      const fixture = render();
      http.expectOne('api/races').flush([
        { id: 'boston', name: 'Boston Marathon', day: 'weekday' },
        { id: 'nyc', name: 'New York City Marathon' },
      ]);
      fixture.detectChanges();
      const app = fixture.componentInstance;
      const select = (fixture.nativeElement as HTMLElement).querySelector('select')!;
      for (const [id, day] of [
        ['boston', 'weekday'],
        ['nyc', 'sunday'],
      ]) {
        select.value = id;
        select.dispatchEvent(new Event('change'));
        http.expectOne(`api/races/${id}`).flush(GPX);
        expect(app.form.controls.raceDay.value).toBe(day);
      }
    });

    it('shows an error when a race GPX fails to load', () => {
      const fixture = render();
      http.expectOne('api/races').flush([{ id: 'boston', name: 'Boston Marathon' }]);
      fixture.detectChanges();
      const select = (fixture.nativeElement as HTMLElement).querySelector('select')!;
      select.value = 'boston';
      select.dispatchEvent(new Event('change'));
      http.expectOne('api/races/boston').flush('nope', { status: 404, statusText: 'Not Found' });
      expect(fixture.componentInstance.error()).toContain('race');
      expect(fixture.componentInstance.gpxFile()).toBeNull();
    });

    it('hides the dropdown when there are no races', () => {
      const fixture = render();
      http.expectOne('api/races').flush([]);
      fixture.detectChanges();
      const el = fixture.nativeElement as HTMLElement;
      expect(el.textContent).not.toContain('Select a route');
      expect(el.querySelector('input[type=file]')).not.toBeNull();
    });

    it('hides the dropdown when the race list fails to load', () => {
      const fixture = render();
      http.expectOne('api/races').flush('boom', { status: 500, statusText: 'Server Error' });
      fixture.detectChanges();
      expect((fixture.nativeElement as HTMLElement).textContent).not.toContain('Select a route');
    });

    it('escapes race ids in the GPX URL', () => {
      TestBed.inject(PlanService).raceGpx('a/b c').subscribe();
      http.expectOne('api/races/a%2Fb%20c').flush(GPX);
    });
  });

  const stop = {
    node: {
      id: 7,
      pos: { lat: 40.74, lon: -74 },
      dist: 16093.44,
      tFast: 5000,
      tExpected: 5400,
      tSlow: 5800,
      weight: 1,
      pinned: false,
    },
    depart: 3000,
    travel: 900,
    arrive: 3900,
    wait: 1500,
  };
  const base: PlanResponse = {
    course: { points: [], length: 42195 },
    mode: 'transit',
    candidates: 10,
    plan: { stops: [stop], sightings: 1, value: 1 },
  };

  it('shows leg steps with line badges and the routing note', () => {
    const fixture = TestBed.createComponent(App);
    const a = { lat: 40.78, lon: -73.98 };
    fixture.componentInstance.result.set({
      ...base,
      routing: 'Subway: MTA New York City Transit (GTFS)',
      legs: [
        [
          { kind: 'walk', fromPos: a, toPos: a, seconds: 240 },
          {
            kind: 'subway',
            line: '1',
            color: 'EE352E',
            from: '72 St',
            to: '14 St',
            fromPos: a,
            toPos: stop.node.pos,
            seconds: 540,
          },
        ],
      ],
    });
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    const items = Array.from(el.querySelectorAll('.steps li')).map((li) =>
      li.textContent?.replace(/\s+/g, ' ').trim(),
    );
    expect(items).toEqual(['Walk 4 min', '1 72 St → 14 St · 9 min']);
    const badge = el.querySelector<HTMLElement>('.line')!;
    expect(badge.style.backgroundColor).toBe('rgb(238, 53, 46)');
    expect(el.querySelector('.routing')?.textContent).toContain('MTA');
  });

  it('renders plans from servers that send no legs', () => {
    const fixture = TestBed.createComponent(App);
    fixture.componentInstance.result.set(base);
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelectorAll('tbody tr').length).toBe(1);
    expect(el.querySelector('.steps')).toBeNull();
    expect(el.querySelector('.routing')).toBeNull();
  });

  describe('distance units', () => {
    beforeEach(() => {
      try {
        localStorage.removeItem('cheersquad.unit');
      } catch {}
    });

    it('switches shown distances and typed-in values to kilometers and remembers it', () => {
      const fixture = TestBed.createComponent(App);
      const app = fixture.componentInstance;
      app.form.controls.lateFrom.setValue(20);
      app.result.set(base);
      fixture.detectChanges();
      const el = fixture.nativeElement as HTMLElement;
      expect(el.querySelector('thead')?.textContent).toContain('Mile');
      expect(el.querySelector('tbody a')?.textContent?.trim()).toBe('10.0');
      expect(el.textContent).toContain('after mile');

      app.setUnit('km');
      fixture.detectChanges();
      expect(el.querySelector('thead')?.textContent).toContain('Km');
      expect(el.querySelector('tbody a')?.textContent?.trim()).toBe('16.1');
      expect(el.textContent).toContain('after km');
      expect(el.textContent).toContain('Marathon (42.2 km)');
      expect(app.form.controls.lateFrom.value).toBe(32.2);
      expect(localStorage.getItem('cheersquad.unit')).toBe('km');
      expect(TestBed.createComponent(App).componentInstance.unit()).toBe('km');
    });
  });
  describe('print and PDF export', () => {
    afterEach(() => vi.restoreAllMocks());

    it('shows Print and Export PDF only once there is a plan', () => {
      const fixture = TestBed.createComponent(App);
      fixture.detectChanges();
      const el = fixture.nativeElement as HTMLElement;
      expect(el.querySelector('button.export')).toBeNull();
      fixture.componentInstance.result.set(base);
      fixture.detectChanges();
      const labels = Array.from(el.querySelectorAll('button.export')).map((b) =>
        b.textContent?.trim(),
      );
      expect(labels).toEqual(['Print', 'Export PDF']);
    });

    it('frames the map for print, titles the page and opens the print dialog', async () => {
      const fit = vi.spyOn(CourseMap.prototype, 'fitForPrint').mockResolvedValue();
      const restore = vi.spyOn(CourseMap.prototype, 'restoreAfterPrint').mockReturnValue();
      const print = vi.spyOn(window, 'print').mockReturnValue();
      const fixture = TestBed.createComponent(App);
      const app = fixture.componentInstance;
      app.gpxFile.set(new File([GPX], 'Spring 10K.gpx'));
      app.form.patchValue({ goalFinish: '3:45:00', gunTime: '10:00', mode: 'transit' });
      app.result.set(base);
      fixture.detectChanges();

      await app.printPlan();
      fixture.detectChanges();
      expect(fit).toHaveBeenCalled();
      expect(print).toHaveBeenCalled();
      expect(app.preparingPrint()).toBe(false);
      const title = (fixture.nativeElement as HTMLElement).querySelector('.print-title p');
      expect(title?.textContent).toBe(
        'Spring 10K · goal 3:45:00 · gun 10:00 · by transit from 06:45',
      );
      // The map keeps its print layout until the dialog is gone and the user is back.
      window.dispatchEvent(new Event('afterprint'));
      expect(restore).not.toHaveBeenCalled();
      window.dispatchEvent(new Event('pointerdown'));
      window.dispatchEvent(new Event('keydown'));
      expect(restore).toHaveBeenCalledTimes(1);
    });

    it('exports the plan as text in the chosen units, with map data', async () => {
      const save = vi.spyOn(PlanPdf.prototype, 'save').mockResolvedValue();
      const app = TestBed.createComponent(App).componentInstance;
      app.result.set(base);
      app.setUnit('km');
      await app.exportPdf();
      const plan = save.mock.calls[0][0];
      expect(plan.heading).toBe('1 sighting');
      expect(plan.columns[1]).toBe('Km');
      expect(plan.rows[0].cells.slice(0, 3)).toEqual(['1', '16.1', '7:50 AM']);
      expect(plan.rows[0].link).toContain('40.74,-74');
      expect(plan.map.stops).toEqual([stop]);
      expect(app.exportingPdf()).toBe(false);
      expect(plan.map.grayTiles).toBe(true);
      try {
        localStorage.removeItem('cheersquad.unit');
      } catch {}
    });

    it('uses color tiles in the PDF when the color map is picked', async () => {
      const save = vi.spyOn(PlanPdf.prototype, 'save').mockResolvedValue();
      const fixture = TestBed.createComponent(App);
      fixture.componentInstance.result.set(base);
      fixture.detectChanges();
      fixture.debugElement.query(By.directive(CourseMap)).componentInstance.grayTiles.set(false);
      await fixture.componentInstance.exportPdf();
      expect(save.mock.calls[0][0].map.grayTiles).toBe(false);
    });

    it('suggests Print when the PDF fails', async () => {
      vi.spyOn(PlanPdf.prototype, 'save').mockRejectedValue(new Error('no canvas'));
      const app = TestBed.createComponent(App).componentInstance;
      app.result.set(base);
      await app.exportPdf();
      expect(app.error()).toContain('Print');
      expect(app.exportingPdf()).toBe(false);
    });
  });
});
