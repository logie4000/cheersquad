import { printBounds } from './course-map';
import { Step, Stop } from './plan';

describe('printBounds', () => {
  const stop = (lat: number, lon: number): Stop => ({
    node: {
      id: 1,
      pos: { lat, lon },
      dist: 0,
      tFast: 0,
      tExpected: 0,
      tSlow: 0,
      weight: 1,
      pinned: false,
    },
    depart: 0,
    travel: 0,
    arrive: 0,
    wait: 0,
  });

  it('covers the course, stops, routes and start point', () => {
    const leg: Step = {
      kind: 'subway',
      fromPos: { lat: 1, lon: 1 },
      toPos: { lat: 2, lon: 2 },
      path: [{ lat: 0.5, lon: 5 }],
      seconds: 60,
    };
    const b = printBounds(
      [
        { lat: 1, lon: 1 },
        { lat: 2, lon: 2 },
      ],
      [stop(3, 1)],
      [[leg]],
      { lat: 0, lon: 0 },
    )!;
    expect([b.getSouth(), b.getWest(), b.getNorth(), b.getEast()]).toEqual([0, 0, 3, 5]);
  });

  it('is null with nothing to show', () => {
    expect(printBounds([], [], [], null)).toBeNull();
  });
});
