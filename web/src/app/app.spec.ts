import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { App } from './app';
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
      expect(labels).toEqual(['Upload my own GPX…', 'Boston Marathon', 'New York City Marathon']);
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
      expect(el.textContent).not.toContain('Upload my own GPX');
      expect(el.querySelector('input[type=file]')).not.toBeNull();
    });

    it('hides the dropdown when the race list fails to load', () => {
      const fixture = render();
      http.expectOne('api/races').flush('boom', { status: 500, statusText: 'Server Error' });
      fixture.detectChanges();
      expect((fixture.nativeElement as HTMLElement).textContent).not.toContain('Upload my own GPX');
    });

    it('escapes race ids in the GPX URL', () => {
      TestBed.inject(PlanService).raceGpx('a/b c').subscribe();
      http.expectOne('api/races/a%2Fb%20c').flush(GPX);
    });
  });
});
