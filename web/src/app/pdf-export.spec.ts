import { jsPDF } from 'jspdf';
import { fitView, layoutSteps, pdfText, project, tilesFor, toPixel } from './pdf-export';
import { Step } from './plan';

describe('pdf map projection', () => {
  it('projects like Web Mercator at zoom 0', () => {
    expect(project({ lat: 0, lon: 0 })).toEqual({ x: 128, y: 128 });
    expect(project({ lat: 0, lon: 180 }).x).toBe(256);
    expect(project({ lat: 85.0511, lon: -180 }).y).toBeCloseTo(0, 2);
  });

  it('fits the points inside the padded frame, centered', () => {
    const pts = [
      { lat: 40.7, lon: -74.0 },
      { lat: 40.8, lon: -73.9 },
    ];
    const v = fitView(pts, 720, 513, 24);
    const a = toPixel(v, pts[0]);
    const b = toPixel(v, pts[1]);
    // The tighter axis touches the padding; both stay inside.
    for (const p of [a, b]) {
      expect(p.x).toBeGreaterThanOrEqual(23.99);
      expect(p.x).toBeLessThanOrEqual(696.01);
      expect(p.y).toBeGreaterThanOrEqual(23.99);
      expect(p.y).toBeLessThanOrEqual(489.01);
    }
    expect(Math.min(a.y, b.y)).toBeCloseTo(24, 1);
    expect((a.x + b.x) / 2).toBeCloseTo(360, 6);
  });

  it('uses a fixed zoom for a single point', () => {
    expect(fitView([{ lat: 1, lon: 1 }], 100, 100).zoom).toBe(15);
  });

  it('covers the whole frame with tiles at the next whole zoom', () => {
    const v = { zoom: 2.5, cx: 128, cy: 128, width: 300, height: 200 };
    const { z, size, tiles } = tilesFor(v);
    expect(z).toBe(3);
    expect(size).toBeCloseTo(256 / Math.SQRT2, 6);
    const lefts = tiles.map((t) => t.left);
    const tops = tiles.map((t) => t.top);
    expect(Math.min(...lefts)).toBeLessThanOrEqual(0);
    expect(Math.max(...lefts) + size).toBeGreaterThanOrEqual(300);
    expect(Math.min(...tops)).toBeLessThanOrEqual(0);
    expect(Math.max(...tops) + size).toBeGreaterThanOrEqual(200);
    // The world's center at zoom 3 is the corner of tiles 3 and 4.
    const t = tiles.find((t) => t.x === 4 && t.y === 4)!;
    expect(t.left).toBeCloseTo(150, 6);
    expect(t.top).toBeCloseTo(100, 6);
  });

  it('wraps tiles across the antimeridian and skips rows off the world', () => {
    const { tiles } = tilesFor({ zoom: 0, cx: 0, cy: 0, width: 256, height: 256 });
    expect(tiles.every((t) => t.x === 0 && t.y === 0)).toBe(true);
  });
});

describe('pdfText', () => {
  it('keeps Latin-1 and swaps symbols the PDF fonts lack', () => {
    expect(pdfText('72 St → 14 St · 9 min')).toBe('72 St to 14 St · 9 min');
    expect(pdfText('8:58 AM–9:11 AM')).toBe('8:58 AM-9:11 AM');
    expect(pdfText('Südkreuz')).toBe('Südkreuz');
    expect(pdfText('Walk  ›  U2 新宿')).toBe('Walk  >  U2 ');
  });
});

describe('layoutSteps', () => {
  const a = { lat: 40.78, lon: -73.98 };
  const steps: Step[] = [
    { kind: 'walk', fromPos: a, toPos: a, seconds: 240 },
    {
      kind: 'subway',
      line: '1',
      color: 'EE352E',
      from: '72 St',
      to: '14 St',
      fromPos: a,
      toPos: a,
      seconds: 540,
    },
  ];

  it('gives subway steps a badge in the line color', () => {
    const [line, ...rest] = layoutSteps(new jsPDF({ unit: 'pt' }), steps, 500);
    expect(rest).toEqual([]);
    expect(line.parts.map((p) => [p.kind, p.text])).toEqual([
      ['text', 'Walk 4 min'],
      ['sep', '>'],
      ['badge', '1'],
      ['text', '72 St to 14 St · 9 min'],
    ]);
    expect(line.parts[2].bg).toBe('#ee352e');
    expect(line.parts[2].fg).toBe('#000000'); // as on screen: black has more contrast on this red
    expect(line.x).toEqual([...line.x].sort((p, q) => p - q));
  });

  it('wraps a step that does not fit onto its own line, without a leading separator', () => {
    const lines = layoutSteps(new jsPDF({ unit: 'pt' }), steps, 90);
    expect(lines.length).toBe(2);
    expect(lines[1].parts[0].kind).toBe('badge');
    for (const l of lines) {
      const last = l.parts.length - 1;
      expect(l.x[last] + l.parts[last].width).toBeLessThanOrEqual(90.01);
    }
  });
});
