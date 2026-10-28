import {
  buildParams,
  formatClock,
  formatMinutes,
  legSteps,
  lineColor,
  lineLabel,
  parseClock,
  parseDuration,
  parseGpxPoints,
  PlanForm,
  PlanParams,
  PlanResponse,
  Step,
  stepDetail,
  stepMinutes,
  stepText,
  textOn,
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

describe('leg steps', () => {
  const a = { lat: 40.78, lon: -73.98 };
  const b = { lat: 40.74, lon: -74.0 };
  const walk: Step = { kind: 'walk', fromPos: a, toPos: b, seconds: 370 };
  const ride: Step = {
    kind: 'subway',
    line: '1',
    color: 'EE352E',
    from: '72 St',
    to: '14 St',
    fromPos: a,
    toPos: b,
    seconds: 540,
  };

  it('describes steps', () => {
    expect(stepText(walk)).toBe('Walk 6 min');
    expect(stepText(ride)).toBe('1 train · 72 St → 14 St · 9 min');
    expect(stepDetail(ride)).toBe('72 St → 14 St · 9 min');
    expect(stepText({ ...ride, line: 'Red Line', from: undefined, to: undefined })).toBe(
      'Red Line · 9 min',
    );
    expect(stepText({ ...walk, kind: 'bike', seconds: 20 })).toBe('Bike <1 min');
    expect(stepText({ ...walk, kind: 'drive', seconds: 3900 })).toBe('Drive 1 h 05 min');
  });

  it('names lines', () => {
    expect(lineLabel('A')).toBe('A train');
    expect(lineLabel('Orange Line')).toBe('Orange Line');
    expect(lineLabel(undefined)).toBe('Subway');
    expect(stepMinutes(0)).toBe('<1 min');
  });

  it('normalizes line colors and picks readable text', () => {
    expect(lineColor('EE352E')).toBe('#ee352e');
    expect(lineColor('#fc0')).toBe('#ffcc00');
    expect(lineColor('')).toBeNull();
    expect(lineColor('nope')).toBeNull();
    expect(textOn('#fccc0a')).toBe('#000000'); // MTA yellow
    // Mid-tone reds read better in black (5.3:1) than white (3.9:1).
    expect(textOn('#ee352e')).toBe('#000000'); // MTA red
    expect(textOn('#0039a6')).toBe('#ffffff'); // MTA blue
    expect(textOn('#ffffff')).toBe('#000000');
  });

  it('tolerates responses without legs', () => {
    const r = {
      course: { points: [], length: 0 },
      mode: 'transit',
      candidates: 0,
      plan: { stops: [], sightings: 0, value: 0 },
    } as PlanResponse;
    expect(legSteps(r, 0)).toEqual([]);
    expect(legSteps({ ...r, legs: [[walk]] }, 0)).toEqual([walk]);
    expect(legSteps({ ...r, legs: [[walk]] }, 3)).toEqual([]);
  });
});
