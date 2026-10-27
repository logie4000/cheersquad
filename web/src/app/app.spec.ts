import { provideHttpClient } from '@angular/common/http';
import { TestBed } from '@angular/core/testing';
import { App } from './app';

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
});
