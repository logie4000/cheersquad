import { provideHttpClient } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { App } from './app';
import { PlanResponse } from './plan';

describe('App', () => {
  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [App],
      providers: [provideHttpClient()],
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
});
