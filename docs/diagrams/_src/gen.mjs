// Hand-drawn diagram generator for the docs: one spec -> <name>.svg (embedded in markdown)
// + <name>.excalidraw (editable at excalidraw.com). Black rough strokes, pastel hachure
// fills, coloured dashed zones, Kalam font (embedded in each SVG, so GitHub renders it).
//
//   cd docs/diagrams/_src && npm install && node gen.mjs specs.mjs ..
//   node gen.mjs specs.mjs .. --obsidian    # .excalidraw.md for an Obsidian vault instead
import fs from 'node:fs';
import path from 'node:path';
import rough from 'roughjs';

const FILL = { blue: '#a5d8ff', green: '#b2f2bb', yellow: '#ffec99', red: '#ffc9c9', violet: '#d0bfff',
  gray: '#e9ecef', orange: '#ffd8a8', none: 'transparent' };
const INK = { black: '#1e1e1e', gray: '#868e96', red: '#e03131', green: '#2f9e44', blue: '#1971c2', orange: '#f08c00', violet: '#7048e8', darkred: '#c92a2a' };
// zone colour -> [stroke, light fill]
const ZONE = { blue: ['#1971c2', '#e7f5ff'], orange: ['#f08c00', '#fff9db'], green: ['#2f9e44', '#ebfbee'],
  violet: ['#7048e8', '#f3f0ff'], red: ['#e03131', '#ffe3e3'], gray: ['#868e96', '#f8f9fa'] };
const FONT = `Kalam, 'Chalkboard SE', 'Comic Sans MS', cursive`;
const FONT_B64 = fs.readFileSync(new URL('./kalam.woff2', import.meta.url)).toString('base64');

let seedN = 1;
const seed = () => (seedN = (seedN * 48271) % 2147483647);
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

// ── sequence-diagram spec -> plain nodes/edges ──────────────────────────────
function expandSeq(spec) {
  const { actors, steps, x0 = 40, y0 = 80, gap = 260, aw = 200, ah = 64, row = 56 } = spec.seq;
  const cx = {};
  actors.forEach((a, i) => {
    const x = x0 + i * gap;
    cx[a.id] = x + aw / 2;
    spec.nodes.push({ id: a.id, x, y: y0, w: aw, h: ah, label: a.label, fill: a.fill || 'gray' });
  });
  let y = y0 + ah + 50;
  for (const s of steps) {
    if (s.note) {
      spec.texts.push({ x: x0, y, text: s.note, color: s.color || 'gray', size: 17 });
      y += row * 0.8;
      continue;
    }
    const a = cx[s.from], b = cx[s.to];
    if (s.from === s.to) {
      spec.edges.push({ points: [[a, y - 8], [a + 46, y - 8], [a + 46, y + 14], [a + 4, y + 14]], label: s.label,
        labelAt: [a + 56, y + 3], anchor: 'start', dashed: s.dashed, color: s.color });
      y += row;
    } else {
      spec.edges.push({ points: [[a, y], [b, y]], label: s.label, dashed: s.dashed, color: s.color });
      y += row;
    }
  }
  const yEnd = y + 10;
  spec.h ||= Math.round(yEnd + 40);
  for (const a of actors) spec.lifelines.push([[cx[a.id], y0 + ah], [cx[a.id], yEnd]]);
}

// ── geometry ─────────────────────────────────────────────────────────────────
function anchor(n, ref, side, off = 0.5) {
  if (side === 'l') return [n.x, n.y + n.h * off];
  if (side === 'r') return [n.x + n.w, n.y + n.h * off];
  if (side === 't') return [n.x + n.w * off, n.y];
  if (side === 'b') return [n.x + n.w * off, n.y + n.h];
  const cx = n.x + n.w / 2, cy = n.y + n.h / 2, dx = ref[0] - cx, dy = ref[1] - cy;
  if (n.shape === 'ellipse') {
    const t = 1 / Math.sqrt((dx * dx) / ((n.w / 2) ** 2) + (dy * dy) / ((n.h / 2) ** 2));
    return [cx + dx * t, cy + dy * t];
  }
  const s = Math.min(Math.abs((n.w / 2) / (dx || 1e-9)), Math.abs((n.h / 2) / (dy || 1e-9)));
  return [cx + dx * s, cy + dy * s];
}
const center = (n) => [n.x + n.w / 2, n.y + n.h / 2];
function edgePoints(e, nodes) {
  if (e.points) return e.points;
  const [fid, fs_, fo] = e.from.split(':'), [tid, ts, to] = e.to.split(':');
  const A = nodes[fid], B = nodes[tid];
  const via = e.via || [];
  const p0 = anchor(A, via[0] || center(B), fs_, fo ? +fo : 0.5);
  const p1 = anchor(B, via.length ? via[via.length - 1] : center(A), ts, to ? +to : 0.5);
  return [p0, ...via, p1];
}

// ── SVG ──────────────────────────────────────────────────────────────────────
function svgOf(spec, nodes) {
  const g = rough.generator();
  const out = [];
  const draw = (d) => {
    for (const p of g.toPaths(d)) {
      const dash = d.options.strokeLineDash ? ` stroke-dasharray="${d.options.strokeLineDash.join(' ')}"` : '';
      const pd = p.d.replace(/(\d+\.\d)\d+/g, '$1'); // 1 decimal is plenty at this scale; ~halves file size
      out.push(`<path d="${pd}" stroke="${p.stroke}" stroke-width="${p.strokeWidth}" fill="${p.fill || 'none'}"${dash}/>`);
    }
  };
  const opt = (fill, extra = {}) => ({ seed: seed(), roughness: 1, bowing: 1, stroke: INK.black, strokeWidth: 2,
    fill: fill && fill !== 'none' ? FILL[fill] : undefined, fillStyle: 'hachure', hachureGap: 7, fillWeight: 1.2, ...extra });
  const text = (x, y, s, { size = 20, color = 'black', anchor: a = 'middle', center: c = false } = {}) => {
    const lines = String(s).split('\n'), lh = size * 1.25;
    const y0 = c ? y - ((lines.length - 1) * lh) / 2 : y;
    const t = lines.map((l, i) => `<tspan x="${x}" y="${(y0 + i * lh).toFixed(1)}">${esc(l)}</tspan>`).join('');
    out.push(`<text font-family="${FONT}" font-size="${size}" fill="${INK[color] || color}" text-anchor="${a}" dominant-baseline="central" paint-order="stroke" stroke="#ffffff" stroke-width="5" stroke-linejoin="round">${t}</text>`);
  };

  for (const z of spec.zones) {
    const [zs, zf] = ZONE[z.color || 'gray'];
    draw(g.rectangle(z.x, z.y, z.w, z.h, opt(null, { stroke: zs, strokeLineDash: [12, 8], strokeWidth: 2, fill: zf, fillStyle: 'solid', roughness: 0.8 })));
    text(z.x + 20, z.y + 30, z.label, { size: 22, color: zs, anchor: 'start' });
  }
  for (const l of spec.lifelines) draw(g.linearPath(l, opt(null, { stroke: INK.gray, strokeWidth: 1.5, strokeLineDash: [6, 7] })));
  for (const n of spec.nodes) {
    const o = opt(n.fill, n.dashed ? { strokeLineDash: [10, 8] } : {});
    if (n.shape === 'ellipse') draw(g.ellipse(n.x + n.w / 2, n.y + n.h / 2, n.w, n.h, o));
    else if (n.shape === 'diamond') draw(g.polygon([[n.x + n.w / 2, n.y], [n.x + n.w, n.y + n.h / 2], [n.x + n.w / 2, n.y + n.h], [n.x, n.y + n.h / 2]], o));
    else if (n.shape === 'cyl') {
      const e = 30, { x, y, w, h } = n;
      draw(g.path(`M${x} ${y + e / 2} L${x} ${y + h - e / 2} A${w / 2} ${e / 2} 0 0 0 ${x + w} ${y + h - e / 2} L${x + w} ${y + e / 2}`, o));
      draw(g.ellipse(x + w / 2, y + e / 2, w, e, opt(n.fill, { fillStyle: 'solid' })));
    } else draw(g.rectangle(n.x, n.y, n.w, n.h, o));
    const [cx, cy] = center(n);
    if (n.align === 'left') text(n.x + 16, n.y + 26, n.label, { size: n.size || 18, anchor: 'start' });
    else text(cx, n.shape === 'cyl' ? cy + 10 : cy, n.label, { size: n.size || 19, center: true });
  }
  for (const st of spec.stickers) {
    const h = st.h || 32;
    draw(g.rectangle(st.x, st.y, st.w, h, opt(null, { stroke: INK.red, strokeWidth: 1.5, fill: '#ffc9c9', hachureGap: 6 })));
    text(st.x + 12, st.y + h / 2, st.text, { size: 16, color: INK.darkred, anchor: 'start', center: true });
  }
  for (const e of spec.edges) {
    const pts = edgePoints(e, nodes), col = INK[e.color || 'black'];
    draw(g.linearPath(pts, opt(null, { stroke: col, strokeLineDash: e.dashed ? [10, 8] : undefined })));
    const [a, b] = pts.slice(-2), ang = Math.atan2(b[1] - a[1], b[0] - a[0]);
    for (const s of [-1, 1]) {
      const t = ang + Math.PI + s * 0.45;
      draw(g.line(b[0], b[1], b[0] + 15 * Math.cos(t), b[1] + 15 * Math.sin(t), opt(null, { stroke: col })));
    }
    if (e.both) {
      const [c, d] = pts.slice(0, 2), ang2 = Math.atan2(c[1] - d[1], c[0] - d[0]);
      for (const s of [-1, 1]) {
        const t = ang2 + Math.PI + s * 0.45;
        draw(g.line(c[0], c[1], c[0] + 15 * Math.cos(t), c[1] + 15 * Math.sin(t), opt(null, { stroke: col })));
      }
    }
    if (e.label) {
      const m = Math.floor((pts.length - 1) / 2), p = pts[m], q = pts[m + 1];
      const at = e.labelAt || [(p[0] + q[0]) / 2, (p[1] + q[1]) / 2 - (Math.abs(q[1] - p[1]) < 4 ? 14 : 0)];
      text(at[0], at[1], e.label, { size: e.size || 16, color: e.color && e.color !== 'black' ? e.color : 'gray', anchor: e.anchor || 'middle', center: true });
    }
  }
  for (const t of spec.texts) text(t.x, t.y, t.text, { size: t.size || 18, color: t.color || 'black', anchor: t.anchor || 'start' });
  if (spec.title) text(30, 34, spec.title, { size: 26, anchor: 'start' });
  if (spec.subtitle) text(30, 66, spec.subtitle, { size: 16, color: 'red', anchor: 'start' });

  return `<svg xmlns="http://www.w3.org/2000/svg" width="${spec.w}" height="${spec.h}" viewBox="0 0 ${spec.w} ${spec.h}">
<style>@font-face{font-family:Kalam;src:url(data:font/woff2;base64,${FONT_B64}) format('woff2');}</style>
<rect width="100%" height="100%" fill="#ffffff"/>
${out.join('\n')}
</svg>
`;
}

// ── Excalidraw source ────────────────────────────────────────────────────────
function excalidrawOf(spec, nodes) {
  const els = [], texts = [];
  let k = 0;
  const id = (p) => `${p}${(k++).toString(36)}${spec.name.length.toString(36)}`;
  const base = (type, x, y, w, h, o = {}) => ({ id: o.id || id('el'), type, x, y, width: w, height: h, angle: 0,
    strokeColor: INK.black, backgroundColor: 'transparent', fillStyle: 'hachure', strokeWidth: 2, strokeStyle: 'solid',
    roughness: 1, opacity: 100, groupIds: [], frameId: null, roundness: null, seed: seed(), version: 1,
    versionNonce: seed(), isDeleted: false, boundElements: [], updated: 1790640000000, link: null, locked: false, ...o });
  const txt = (x, y, s, o = {}) => {
    const size = o.fontSize || 20, lines = String(s).split('\n');
    const w = Math.max(...lines.map((l) => l.length)) * size * 0.55, h = lines.length * size * 1.25;
    const ax = o.textAlign === 'center' ? x - w / 2 : x, ay = o.containerId ? y - h / 2 : y - size * 0.6;
    const e = base('text', ax, ay, w, h, { text: s, rawText: s, originalText: s, fontSize: size, fontFamily: 1,
      textAlign: 'left', verticalAlign: 'middle', containerId: null, autoResize: true, lineHeight: 1.25, ...o });
    els.push(e); texts.push(e);
    return e;
  };
  for (const z of spec.zones) {
    const [zs, zf] = ZONE[z.color || 'gray'];
    els.push(base('rectangle', z.x, z.y, z.w, z.h, { strokeColor: zs, backgroundColor: zf, fillStyle: 'solid', strokeStyle: 'dashed' }));
    txt(z.x + 20, z.y + 30, z.label, { strokeColor: zs, fontSize: 22 });
  }
  for (const l of spec.lifelines) els.push(base('line', l[0][0], l[0][1], 0, l[1][1] - l[0][1],
    { strokeColor: INK.gray, strokeStyle: 'dashed', strokeWidth: 1, points: [[0, 0], [0, l[1][1] - l[0][1]]], roundness: { type: 2 } }));
  for (const n of spec.nodes) {
    const bg = FILL[n.fill || 'none'];
    const type = n.shape === 'ellipse' ? 'ellipse' : n.shape === 'diamond' ? 'diamond' : 'rectangle';
    const r = base(type, n.x, n.y, n.w, n.h, { id: n.id, backgroundColor: bg, roundness: type === 'rectangle' ? { type: 3 } : null,
      strokeStyle: n.dashed ? 'dashed' : 'solid' });
    els.push(r); n.__el = r;
    if (n.shape === 'cyl') els.push(base('ellipse', n.x, n.y, n.w, 30, { backgroundColor: bg, fillStyle: 'solid' }));
    const [cx, cy] = center(n);
    const t = txt(cx, cy, n.label, { containerId: n.id, textAlign: n.align === 'left' ? 'left' : 'center',
      verticalAlign: n.align === 'left' ? 'top' : 'middle', fontSize: n.size || (n.align === 'left' ? 18 : 19) });
    r.boundElements.push({ type: 'text', id: t.id });
  }
  for (const e of spec.edges) {
    const pts = edgePoints(e, nodes), [x0, y0] = pts[0];
    const a = base('arrow', x0, y0, pts.at(-1)[0] - x0, pts.at(-1)[1] - y0, {
      strokeColor: INK[e.color || 'black'], strokeStyle: e.dashed ? 'dashed' : 'solid', roundness: { type: 2 },
      points: pts.map(([x, y]) => [x - x0, y - y0]), lastCommittedPoint: null,
      startArrowhead: e.both ? 'arrow' : null, endArrowhead: 'arrow',
      startBinding: e.from ? { elementId: e.from.split(':')[0], focus: 0, gap: 4 } : null,
      endBinding: e.to ? { elementId: e.to.split(':')[0], focus: 0, gap: 4 } : null });
    for (const b of [a.startBinding, a.endBinding]) if (b) nodes[b.elementId].__el?.boundElements.push({ type: 'arrow', id: a.id });
    els.push(a);
    if (e.label) {
      const m = Math.floor((pts.length - 1) / 2), p = pts[m], q = pts[m + 1];
      const at = e.labelAt || [(p[0] + q[0]) / 2, (p[1] + q[1]) / 2 - 14];
      txt(at[0], at[1], e.label, { fontSize: 16, strokeColor: e.color && e.color !== 'black' ? INK[e.color] : INK.gray, textAlign: e.anchor === 'start' ? 'left' : 'center' });
    }
  }
  for (const st of spec.stickers) {
    const h = st.h || 32, r = base('rectangle', st.x, st.y, st.w, h, { strokeColor: INK.red, backgroundColor: '#ffc9c9', strokeWidth: 1 });
    els.push(r);
    const t = txt(st.x + 12, st.y + h / 2, st.text, { containerId: r.id, fontSize: 16, strokeColor: INK.darkred, textAlign: 'left' });
    r.boundElements.push({ type: 'text', id: t.id });
  }
  for (const t of spec.texts) txt(t.x, t.y, t.text, { fontSize: t.size || 18, strokeColor: INK[t.color || 'black'] });
  if (spec.title) txt(30, 34, spec.title, { fontSize: 26 });
  if (spec.subtitle) txt(30, 66, spec.subtitle, { fontSize: 16, strokeColor: INK.red });

  const scene = { type: 'excalidraw', version: 2, source: 'https://excalidraw.com',
    elements: els, appState: { gridSize: null, viewBackgroundColor: '#ffffff' }, files: {} };
  if (!OBSIDIAN) return JSON.stringify(scene, null, 2);
  return `---

excalidraw-plugin: parsed
tags: [excalidraw]

---
==⚠  Switch to EXCALIDRAW VIEW in the MORE OPTIONS menu of this document. ⚠==


# Excalidraw Data

## Text Elements
${texts.map((t) => `${t.text} ^${t.id}`).join('\n\n')}

%%
## Drawing
\`\`\`json
${JSON.stringify(scene, null, 2)}
\`\`\`
%%`;
}

// ── main ─────────────────────────────────────────────────────────────────────
const [specFile, outDir] = process.argv.slice(2);
const OBSIDIAN = process.argv.includes('--obsidian');
const specs = (await import(path.resolve(specFile))).default;
for (const spec of specs) {
  seedN = [...spec.name].reduce((a, c) => a + c.charCodeAt(0), 7);
  spec.nodes ||= []; spec.edges ||= []; spec.texts ||= []; spec.zones ||= []; spec.stickers ||= []; spec.lifelines = [];
  if (spec.seq) expandSeq(spec);
  const nodes = Object.fromEntries(spec.nodes.map((n) => [n.id, n]));
  fs.writeFileSync(path.join(outDir, `${spec.name}.svg`), svgOf(spec, nodes));
  fs.writeFileSync(path.join(outDir, `${spec.name}.excalidraw${OBSIDIAN ? '.md' : ''}`), excalidrawOf(spec, nodes));
  console.log('wrote', spec.name);
}
