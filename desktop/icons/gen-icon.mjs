#!/usr/bin/env node
// gen-icon.mjs —— gah 图标生成器(扁平化 / 极简 / 与 App 同色)。
//
// 为什么要有这个脚本:图标是 App 的门面 —— 窗口图标、托盘图标、安装器、快捷方式全用它 ——
// 但它一旦只以二进制入库,就没人改得动、也没人知道它从哪来。这里把**几何与配色作为唯一源**
// 写死在下面的常量里,由它同时产出两份东西:
//   gah-icon.svg       人读的设计稿(要与设计对齐就改这里的常量,再重跑)
//   gah-icon-1024.png  交给 `npx @tauri-apps/cli icon` 的源图(它生成 .ico/.icns/全套 png)
//
// 设计:gah 是跑在本机的 agent 运行时,门面用**终端提示符 `>_`** —— 一眼看出是给开发者用的
// 东西;扁平单色,无渐变、无阴影、无描边,16px 托盘尺寸下仍然认得出。
// 配色取自 web-src/src/style.css 的 --accent(#4176e6),界面与图标同色。
//
// 用法:
//   node desktop/icons/gen-icon.mjs
//   npx @tauri-apps/cli@2 icon desktop/icons/gah-icon-1024.png -o desktop/src-tauri/icons
//   rm -rf desktop/src-tauri/icons/{android,ios}   # 本项目 targets 只有 app/dmg/nsis,移动端图标用不上
//   (该命令会顺带重写 16/24/32/48/64/256 的 icon.ico 与 icon.icns,以及全套 png)
//
// 注:自画 + 自写 PNG,不引任何依赖 —— 装了 rsvg/cairosvg/ImageMagick 的环境反而是少数。

import { writeFileSync } from 'node:fs';
import { deflateSync } from 'node:zlib';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));

// 画布固定 1024(tauri icon 要求源图 ≥1024 且为正方形)。
const SIZE = 1024;
// 背景圆角方块(留白让它在 macOS 的 Big Sur 网格里不顶边,Windows 也接受这点边距)。
const BG = { inset: 51, radius: 205 };
// 笔画宽度 + 折线(左键提示符 > 与光标 _)。点距 = 圆头半径,故实际外沿再各外扩 STROKE/2。
const STROKE = 110;
const CHEVRON = [
  [210, 301],
  [463, 512],
  [210, 723],
];
const BAR = [
  [636, 723],
  [815, 723],
];

const ACCENT = '#4176e6';
const GLYPH = '#ffffff';

const hex = (s) => [
  parseInt(s.slice(1, 3), 16),
  parseInt(s.slice(3, 5), 16),
  parseInt(s.slice(5, 7), 16),
];
const clamp01 = (v) => (v < 0 ? 0 : v > 1 ? 1 : v);

// —— 有符号距离场:形状全部用解析 SDF 表达,边缘取 1px 线性斜坡得到抗锯齿 ——
// (比超采样便宜得多,也不会在细笔画上糊掉。)

// 圆角矩形的 SDF(标准式:q = |p| - (half - r);d = len(max(q,0)) + min(max(qx,qy),0) - r)。
function sdRoundRect(px, py) {
  const c = SIZE / 2;
  const half = c - BG.inset;
  const qx = Math.abs(px - c) - (half - BG.radius);
  const qy = Math.abs(py - c) - (half - BG.radius);
  return (
    Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) + Math.min(Math.max(qx, qy), 0) - BG.radius
  );
}

// 点到线段距离(圆头端点 = 距离再减去半宽)。
function sdSegment(px, py, [x0, y0], [x1, y1]) {
  const dx = x1 - x0;
  const dy = y1 - y0;
  const len2 = dx * dx + dy * dy;
  let t = len2 === 0 ? 0 : ((px - x0) * dx + (py - y0) * dy) / len2;
  t = t < 0 ? 0 : t > 1 ? 1 : t;
  return Math.hypot(px - (x0 + t * dx), py - (y0 + t * dy));
}

function sdStroke(px, py, pts) {
  let d = Infinity;
  for (let i = 0; i < pts.length - 1; i++) {
    const seg = sdSegment(px, py, pts[i], pts[i + 1]);
    if (seg < d) d = seg;
  }
  return d - STROKE / 2;
}

// —— 光栅化:2×2 超采样后取平均(1px 斜坡 + 4 采样,边缘已足够干净) ——
const SS = 2;
const [ar, ag, ab] = hex(ACCENT); // accent RGB
const [wr, wg, wb] = hex(GLYPH);  // white  RGB

function rasterize() {
  const out = Buffer.alloc(SIZE * SIZE * 4);
  const step = 1 / SS;
  for (let y = 0; y < SIZE; y++) {
    for (let x = 0; x < SIZE; x++) {
      let aBg = 0;
      let aGl = 0;
      for (let sy = 0; sy < SS; sy++) {
        for (let sx = 0; sx < SS; sx++) {
          const px = x + step * (sx + 0.5);
          const py = y + step * (sy + 0.5);
          aBg += clamp01(0.5 - sdRoundRect(px, py));
          const dg = Math.min(sdStroke(px, py, CHEVRON), sdStroke(px, py, BAR));
          aGl += clamp01(0.5 - dg);
        }
      }
      aBg /= SS * SS;
      aGl /= SS * SS;
      // 笔画整体在背景之内 → alpha 取背景覆盖率;颜色按笔画覆盖率在「强调色 ↔ 白」之间插值。
      const i = (y * SIZE + x) * 4;
      out[i] = Math.round(ar + (wr - ar) * aGl);
      out[i + 1] = Math.round(ag + (wg - ag) * aGl);
      out[i + 2] = Math.round(ab + (wb - ab) * aGl);
      out[i + 3] = Math.round(aBg * 255);
    }
  }
  return out;
}

// —— 极简 PNG 编码器(8bit RGBA,filter 0):单个 IDAT + zlib,不引依赖 ——
const CRC_TABLE = (() => {
  const t = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c;
  }
  return t;
})();

function crc32(buf) {
  let c = -1;
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return (c ^ -1) >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body), 0);
  return Buffer.concat([len, body, crc]);
}

function encodePng(rgba) {
  const stride = SIZE * 4 + 1; // 每行前置 1 字节 filter 类型
  const raw = Buffer.alloc(stride * SIZE);
  for (let y = 0; y < SIZE; y++) {
    rgba.copy(raw, y * stride + 1, y * SIZE * 4, (y + 1) * SIZE * 4);
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(SIZE, 0);
  ihdr.writeUInt32BE(SIZE, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // color type: RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

// —— 同一份几何导出 SVG(人读的设计稿) ——
function toSvg() {
  const half = SIZE / 2 - BG.inset;
  const poly = (pts) => pts.map(([x, y], i) => `${i ? 'L' : 'M'}${x} ${y}`).join(' ');
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${SIZE}" height="${SIZE}" viewBox="0 0 ${SIZE} ${SIZE}">
  <!-- gah 图标(扁平 / 极简):终端提示符 >_ ,配色同 web-src 的 --accent。
       本文件由 gen-icon.mjs 生成,改几何请改脚本里的常量后重跑,别只改这里。 -->
  <rect x="${SIZE / 2 - half}" y="${SIZE / 2 - half}" width="${half * 2}" height="${half * 2}" rx="${BG.radius}" fill="${ACCENT}"/>
  <g fill="none" stroke="${GLYPH}" stroke-width="${STROKE}" stroke-linecap="round" stroke-linejoin="round">
    <path d="${poly(CHEVRON)}"/>
    <path d="${poly(BAR)}"/>
  </g>
</svg>
`;
}

const png = join(HERE, 'gah-icon-1024.png');
const svg = join(HERE, 'gah-icon.svg');
writeFileSync(png, encodePng(rasterize()));
writeFileSync(svg, toSvg());
console.log(`写入 ${png}`);
console.log(`写入 ${svg}`);
console.log(`\n下一步(生成 .ico/.icns/全套 png):`);
console.log(
  `  npx @tauri-apps/cli@2 icon desktop/icons/gah-icon-1024.png -o desktop/src-tauri/icons`,
);
