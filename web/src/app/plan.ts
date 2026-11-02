// Types for the /api/plan contract and the unit conversions at the UI edge.
// The API works in meters and seconds since the gun; the UI shows miles and
// clock times.

export interface LatLon {
  lat: number;
  lon: number;
}

export type Mode = 'walk' | 'bike' | 'transit' | 'drive';

/** Which day's transit timetable to use; Boston's Monday race is 'weekday'. */
export type RaceDay = 'sunday' | 'saturday' | 'weekday';

export interface Area {
  center: LatLon;
  radius: number;
}

export interface PlanParams {
  runner: {
    finishSeconds: number;
    split: number;
    startDelaySeconds: number;
    uncertainty: number;
  };
  spectator: {
    start: LatLon;
    startTimeSeconds: number;
    mode: Mode;
    bufferSeconds: number;
    raceDay: RaceDay;
  };
  certifiedMeters?: number;
  spacingMeters?: number;
  pinned?: LatLon[];
  excluded?: Area[];
  crossings?: LatLon[];
  mileWeights?: Record<string, number>;
  maxStops?: number;
}

export interface PlanNode {
  id: number;
  pos: LatLon;
  dist: number;
  tFast: number;
  tExpected: number;
  tSlow: number;
  weight: number;
  pinned: boolean;
}

export interface Stop {
  node: PlanNode;
  depart: number;
  travel: number;
  arrive: number;
  wait: number;
}

export type StepKind = 'walk' | 'subway' | 'transit' | 'bike' | 'drive';

/** One part of a travel leg, e.g. a walk to a station or a subway ride. */
export interface Step {
  kind: StepKind;
  line?: string; // subway line name, e.g. "1" or "Red Line"
  color?: string; // line color as hex without '#'
  from?: string; // station name
  to?: string;
  fromPos: LatLon;
  toPos: LatLon;
  /** Stations the train stops at between from and to (subway steps only). */
  via?: { name: string; pos: LatLon }[];
  /** Track geometry of a subway ride, when the feed has shapes. */
  path?: LatLon[];
  seconds: number;
}

export interface PlanResponse {
  course: { points: LatLon[]; length: number };
  mode: Mode;
  candidates: number;
  plan: { stops: Stop[]; sightings: number; value: number };
  /** How travel was routed, e.g. "Subway: MTA ... (GTFS), walking: straight-line estimate". */
  routing?: string;
  /** legs[i] is how to get to stop i from the previous stop (or the start). */
  legs?: Step[][];
  /** Pinned spots the plan could not visit: off the course or not reachable in time. */
  missedPins?: LatLon[];
  /** Set in transit mode when no subway or rail data covers the course: why there is no plan. */
  noTransit?: string;
}

/** A preloaded race course from GET api/races. */
export interface Race {
  id: string;
  name: string;
  /** Set when the race is not on a Sunday. */
  day?: RaceDay;
}

export const METERS_PER_MILE = 1609.344;

/** The distance unit the UI shows and reads distances in. */
export type DistanceUnit = 'mi' | 'km';

/** Meters in one of the given unit. */
export function unitMeters(u: DistanceUnit): number {
  return u === 'km' ? 1000 : METERS_PER_MILE;
}

/** Formats meters in the given unit to one decimal, e.g. "13.1". */
export function formatDistance(meters: number, u: DistanceUnit): string {
  return (meters / unitMeters(u)).toFixed(1);
}

/** Converts a value entered in one unit to another, to one decimal. */
export function convertDistance(v: number, from: DistanceUnit, to: DistanceUnit): number {
  return Math.round(((v * unitMeters(from)) / unitMeters(to)) * 10) / 10;
}

/** Parses "h:mm:ss", "h:mm" or "mm" into seconds; NaN if malformed. */
export function parseDuration(s: string): number {
  const parts = s.trim().split(':');
  if (parts.length > 3 || parts.some((p) => !/^\d+$/.test(p))) {
    return NaN;
  }
  const n = parts.map(Number);
  if (n.length === 1) return n[0] * 60;
  if (n.length === 2) return n[0] * 3600 + n[1] * 60;
  return n[0] * 3600 + n[1] * 60 + n[2];
}

/** Parses a 24-hour "HH:MM" clock time into seconds after midnight. */
export function parseClock(s: string): number {
  const m = /^(\d{1,2}):(\d{2})$/.exec(s.trim());
  if (!m || +m[1] > 23 || +m[2] > 59) return NaN;
  return +m[1] * 3600 + +m[2] * 60;
}

/** Formats seconds since the gun as a clock time, given the gun's clock time. */
export function formatClock(sinceGun: number, gunClock: number): string {
  // Round to whole minutes before splitting into hours and minutes, so
  // 7:59:45 shows as 8:00 rather than 7:60.
  const day = 24 * 60;
  const t = ((Math.round((sinceGun + gunClock) / 60) % day) + day) % day;
  const h = Math.floor(t / 60);
  const m = t % 60;
  const h12 = h % 12 === 0 ? 12 : h % 12;
  return `${h12}:${String(m).padStart(2, '0')} ${h < 12 ? 'AM' : 'PM'}`;
}

/** Formats a number of seconds as "1h 05m" or "12m". */
export function formatMinutes(seconds: number): string {
  const total = Math.max(0, Math.round(seconds / 60));
  const h = Math.floor(total / 60);
  const m = total % 60;
  return h > 0 ? `${h}h ${String(m).padStart(2, '0')}m` : `${m}m`;
}

/** The steps for reaching stop i; empty if the server sent no legs. */
export function legSteps(r: PlanResponse, i: number): Step[] {
  const leg = r.legs?.[i];
  return Array.isArray(leg) ? leg : [];
}

/** Formats a step's duration as "6 min", "<1 min" or "1 h 05 min". */
export function stepMinutes(seconds: number): string {
  const m = Math.round(seconds / 60);
  if (m < 1) return '<1 min';
  if (m < 60) return `${m} min`;
  return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, '0')} min`;
}

/** Names a subway line the way riders say it: "1 train", but "Red Line" as is. */
export function lineLabel(line: string | undefined): string {
  const l = (line ?? '').trim();
  if (!l) return 'Subway';
  return /^[A-Z0-9]{1,3}$/i.test(l) ? `${l} train` : l;
}

const STEP_VERB: Record<StepKind, string> = {
  walk: 'Walk',
  subway: 'Ride',
  transit: 'Transit',
  bike: 'Bike',
  drive: 'Drive',
};

/**
 * A step without its line name, for showing next to a line badge:
 * "72 St → 14 St · 9 min" for a ride, "Walk 6 min" otherwise.
 */
export function stepDetail(s: Step): string {
  const time = stepMinutes(s.seconds);
  if (s.kind !== 'subway') return `${STEP_VERB[s.kind] ?? 'Travel'} ${time}`;
  const stations = s.from && s.to ? `${s.from} → ${s.to}` : (s.to ?? s.from ?? '');
  const n = (s.via?.length ?? 0) + 1;
  const stops = s.via?.length ? ` · ${n} stops` : '';
  return stations ? `${stations}${stops} · ${time}` : `${time}${stops}`;
}

/** A step as one line of text: "Walk 6 min" or "1 train · 72 St → 14 St · 9 min". */
export function stepText(s: Step): string {
  return s.kind === 'subway' ? `${lineLabel(s.line)} · ${stepDetail(s)}` : stepDetail(s);
}

/** A feed's line color as "#rrggbb", or null if missing or malformed. */
export function lineColor(hex: string | undefined): string | null {
  const h = (hex ?? '').trim().replace(/^#/, '');
  if (/^[0-9a-f]{6}$/i.test(h)) return `#${h.toLowerCase()}`;
  if (/^[0-9a-f]{3}$/i.test(h)) {
    return `#${[...h.toLowerCase()].map((c) => c + c).join('')}`;
  }
  return null;
}

/**
 * Black or white, whichever has more WCAG contrast on the given "#rrggbb"
 * background. The better of the two is always at least 4.58:1.
 */
export function textOn(bg: string): '#000000' | '#ffffff' {
  const lin = (i: number) => {
    const c = parseInt(bg.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  const y = 0.2126 * lin(1) + 0.7152 * lin(3) + 0.0722 * lin(5);
  // Contrast with black is (y + 0.05) / 0.05, with white 1.05 / (y + 0.05).
  return (y + 0.05) / 0.05 >= 1.05 / (y + 0.05) ? '#000000' : '#ffffff';
}

/** Reads track points (or route points) from GPX text for the map preview. */
export function parseGpxPoints(text: string): LatLon[] {
  const doc = new DOMParser().parseFromString(text, 'application/xml');
  if (doc.getElementsByTagName('parsererror').length > 0) {
    return [];
  }
  let els = Array.from(doc.getElementsByTagNameNS('*', 'trkpt'));
  if (els.length === 0) {
    els = Array.from(doc.getElementsByTagNameNS('*', 'rtept'));
  }
  return els
    .map((el) => ({ lat: Number(el.getAttribute('lat')), lon: Number(el.getAttribute('lon')) }))
    .filter((p) => Number.isFinite(p.lat) && Number.isFinite(p.lon));
}

/** Form values as the user enters them. */
export interface PlanForm {
  goalFinish: string; // h:mm:ss chip time
  splitPercent: number; // + positive split, - negative
  startDelayMinutes: number;
  uncertaintyPercent: number;
  distance: 'gpx' | 'marathon' | 'half' | 'custom';
  unit: DistanceUnit; // unit of customDistance and lateFrom
  customDistance: number | null;
  gunTime: string; // HH:MM
  spectatorStart: string; // HH:MM
  mode: Mode;
  raceDay: RaceDay;
  bufferMinutes: number;
  maxStops: number;
  lateFrom: number | null; // weight spots from this mile or km on
  lateWeight: number;
}

export interface MapMarks {
  start: LatLon | null;
  pinned: LatLon[];
  excluded: LatLon[];
  crossings: LatLon[];
}

/** Radius in meters around an excluded spot. */
export const EXCLUDE_RADIUS = 150;

/** Converts the form and map marks into API params, or returns an error. */
export function buildParams(f: PlanForm, marks: MapMarks): PlanParams | string {
  const finish = parseDuration(f.goalFinish);
  if (!Number.isFinite(finish) || finish <= 0) return 'Enter a goal finish time like 3:45:00.';
  const gun = parseClock(f.gunTime);
  if (!Number.isFinite(gun)) return 'Enter the gun time as HH:MM (24-hour).';
  const leave = parseClock(f.spectatorStart);
  if (!Number.isFinite(leave)) return 'Enter when you set off as HH:MM (24-hour).';
  if (!marks.start) return 'Click the map to set where you start from.';

  let certifiedMeters: number | undefined;
  if (f.distance === 'marathon') certifiedMeters = 42195;
  if (f.distance === 'half') certifiedMeters = 21097.5;
  const unitM = unitMeters(f.unit);
  if (f.distance === 'custom') {
    if (!f.customDistance || f.customDistance <= 0) {
      return `Enter the course distance in ${f.unit === 'km' ? 'kilometers' : 'miles'}.`;
    }
    certifiedMeters = f.customDistance * unitM;
  }

  // The API weights whole miles (mile n is the nth mile of the course), so a
  // km threshold starts at the mile that contains the start of that km.
  let mileWeights: Record<string, number> | undefined;
  if (f.lateFrom && f.lateWeight !== 1) {
    mileWeights = {};
    const first = Math.floor(((f.lateFrom - 1) * unitM) / METERS_PER_MILE + 1e-9) + 1;
    for (let m = first; m <= 30; m++) mileWeights[String(m)] = f.lateWeight;
  }

  return {
    runner: {
      finishSeconds: finish,
      split: f.splitPercent / 100,
      startDelaySeconds: f.startDelayMinutes * 60,
      uncertainty: f.uncertaintyPercent / 100,
    },
    spectator: {
      start: marks.start,
      startTimeSeconds: leave - gun,
      mode: f.mode,
      bufferSeconds: f.bufferMinutes * 60,
      raceDay: f.raceDay,
    },
    certifiedMeters,
    pinned: marks.pinned,
    excluded: marks.excluded.map((center) => ({ center, radius: EXCLUDE_RADIUS })),
    crossings: marks.crossings,
    mileWeights,
    maxStops: f.maxStops,
  };
}
