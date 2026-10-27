import {
  buildParams,
  formatClock,
  formatMinutes,
  parseClock,
  parseDuration,
  parseGpxPoints,
  PlanForm,
  PlanParams,
} from './plan';

const form: PlanForm = {
  goalFinish: '3:45:00',
  splitPercent: 2,
  startDelayMinutes: 12,
  uncertaintyPercent: 5,
  distance: 'marathon',
  customMiles: null,
  gunTime: '07:00',
  spectatorStart: '06:30',
  mode: 'bike',
  bufferMinutes: 3,
  maxStops: 0,
  lateMilesFrom: 20,
  lateMilesWeight: 2,
};
const marks = {
  start: { lat: 1, lon: 2 },
  pinned: [],
  excluded: [{ lat: 3, lon: 4 }],
  crossings: [],
};

describe('time parsing', () => {
  it('parses durations', () => {
    expect(parseDuration('3:45:00')).toBe(13500);
    expect(parseDuration('3:45')).toBe(13500);
    expect(parseDuration('90')).toBe(5400);
    expect(parseDuration('3:4x')).toBeNaN();
  });

  it('parses and formats clock times', () => {
    expect(parseClock('07:05')).toBe(7 * 3600 + 300);
    expect(parseClock('25:00')).toBeNaN();
    expect(formatClock(0, parseClock('07:00'))).toBe('7:00 AM');
    expect(formatClock(-1800, parseClock('07:00'))).toBe('6:30 AM');
    expect(formatClock(6 * 3600, parseClock('07:00'))).toBe('1:00 PM');
    expect(formatMinutes(3900)).toBe('1h 05m');
    expect(formatMinutes(720)).toBe('12m');
  });
});

describe('buildParams', () => {
  it('converts form units to API units', () => {
    const p = buildParams(form, marks) as PlanParams;
    expect(p.runner).toEqual({
      finishSeconds: 13500,
      split: 0.02,
      startDelaySeconds: 720,
      uncertainty: 0.05,
    });
    expect(p.spectator.startTimeSeconds).toBe(-1800);
    expect(p.spectator.bufferSeconds).toBe(180);
    expect(p.certifiedMeters).toBe(42195);
    expect(p.excluded).toEqual([{ center: { lat: 3, lon: 4 }, radius: 150 }]);
    expect(p.mileWeights?.['20']).toBe(2);
    expect(p.mileWeights?.['19']).toBeUndefined();
  });

  it('reports missing inputs', () => {
    expect(buildParams({ ...form, goalFinish: '' }, marks)).toContain('finish');
    expect(buildParams(form, { ...marks, start: null })).toContain('start');
    expect(buildParams({ ...form, distance: 'custom' }, marks)).toContain('miles');
  });
});

describe('parseGpxPoints', () => {
  it('reads namespaced track points', () => {
    const gpx = `<gpx xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>
      <trkpt lat="1.5" lon="2.5"/><trkpt lat="1.6" lon="2.6"/></trkseg></trk></gpx>`;
    expect(parseGpxPoints(gpx)).toEqual([
      { lat: 1.5, lon: 2.5 },
      { lat: 1.6, lon: 2.6 },
    ]);
  });

  it('returns nothing for invalid XML', () => {
    expect(parseGpxPoints('<gpx')).toEqual([]);
  });
});
