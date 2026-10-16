#!/usr/bin/env node
/**
 * mirror-manifest.mjs —— 镜像版本清单生成与本地产物校验（零依赖 Node）。
 *
 * 输入是已发布的 GitHub stable.json（Wails updater 清单）。本工具：
 *   1. 逐制品核对本地文件的存在性、size、sha512（清单内为 base64 摘要）；
 *   2. 仅把 artifact.url 改写为 <base-url>/releases/<version>/<文件名>，
 *      其余字段（version/digest/size/notes 等）一律原样保留——
 *      镜像与主源"同版本同摘要"是客户端回退时不装错版本的硬前提；
 *   3. 确定性输出（2 空格缩进 + 结尾换行），同输入必同输出，保证幂等重跑。
 *
 * 用法：
 *   node mirror-manifest.mjs --manifest <stable.json> --artifacts-dir <dir> \
 *        [--base-url <镜像根URL>] [--output <输出文件>] \
 *        [--expect-version X.Y.Z] [--check-only] [--list-files]
 *
 * 日志一律走 stderr（[时间戳] [级别]）；--list-files 的文件名清单走 stdout，
 * 方便 shell 编排捕获。退出码：0 成功，1 校验/参数失败。
 */
import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';

function log(level, msg) {
  process.stderr.write(`[${new Date().toISOString()}] [${level}] ${msg}\n`);
}

function die(msg) {
  log('ERROR', msg);
  process.exit(1);
}

// --- 参数解析 ---
const args = process.argv.slice(2);

function takeValue(flag) {
  const i = args.indexOf(flag);
  if (i === -1) return undefined;
  const v = args[i + 1];
  if (!v || v.startsWith('--')) die(`参数 ${flag} 缺少取值`);
  args.splice(i, 2);
  return v;
}

const manifestPath = takeValue('--manifest');
const artifactsDir = takeValue('--artifacts-dir');
const baseUrl = takeValue('--base-url');
const outputPath = takeValue('--output');
const expectVersion = takeValue('--expect-version');
const checkOnly = args.includes('--check-only');
const listFiles = args.includes('--list-files');
// 布尔开关已消费，从待检列表移除，避免误报未知参数
for (const flag of ['--check-only', '--list-files']) {
  const i = args.indexOf(flag);
  if (i !== -1) args.splice(i, 1);
}

if (args.length > 0) die(`未知参数：${args.join(' ')}`);
if (!manifestPath || !artifactsDir) {
  die('用法：--manifest <stable.json> --artifacts-dir <dir> [--base-url URL] [--output FILE] [--expect-version V] [--check-only] [--list-files]');
}
if (checkOnly && (outputPath || baseUrl)) die('--check-only 不能与 --output / --base-url 同用');
if (!checkOnly && !outputPath && baseUrl) die('给了 --base-url 但既未给 --output 也非 --check-only');
if (outputPath && !baseUrl) die('--output 需要配套 --base-url（用于改写制品下载地址）');

// --- 校验镜像根地址（与客户端 normalizeMirrorBaseURL 规则保持一致）---
function validateBaseUrl(raw) {
  if (/[\s"'\\]/.test(raw)) die(`镜像根地址不能包含空白/引号/反斜杠：${raw}`);
  if (raw.endsWith('/')) die(`镜像根地址不能以 / 结尾：${raw}`);
  let u;
  try {
    u = new URL(raw);
  } catch {
    die(`镜像根地址无法解析：${raw}`);
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') die(`镜像根地址必须是 http(s)：${raw}`);
  if (!u.host) die(`镜像根地址缺少主机名：${raw}`);
  if (u.search || u.hash) die(`镜像根地址不能带查询参数或片段：${raw}`);
}

// --- sha512 流式摘要 ---
function sha512File(file) {
  return new Promise((resolve, reject) => {
    const h = createHash('sha512');
    const rs = createReadStream(file);
    rs.on('data', (d) => h.update(d));
    rs.on('end', () => resolve(h.digest('hex')));
    rs.on('error', reject);
  });
}

// --- 从制品 URL 提取文件名（带安全校验，拒绝任何路径穿越可能）---
function artifactFilename(u) {
  if (typeof u !== 'string' || u === '') die(`制品缺少下载地址：${JSON.stringify(u)}`);
  let parsed;
  try {
    parsed = new URL(u);
  } catch {
    die(`制品 URL 无法解析：${u}`);
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') die(`制品 URL 必须是 http(s)：${u}`);
  const last = parsed.pathname.split('/').filter(Boolean).pop();
  if (!last) die(`制品 URL 缺少文件名：${u}`);
  let name;
  try {
    name = decodeURIComponent(last);
  } catch {
    die(`制品 URL 文件名无法解码：${u}`);
  }
  if (
    name === '.' || name === '..' ||
    name.includes('/') || name.includes('\\') ||
    /\s/.test(name) || /[\0-\x1f]/.test(name)
  ) {
    die(`制品文件名不安全，拒绝用于镜像路径：${JSON.stringify(name)}`);
  }
  return name;
}

// --- 读取并校验清单 ---
if (!statSync(manifestPath).isFile()) die(`清单不存在或不是文件：${manifestPath}`);
let manifest;
try {
  manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
} catch (e) {
  die(`清单不是合法 JSON：${manifestPath}（${e.message}）`);
}
if (!manifest || typeof manifest !== 'object') die(`清单结构异常：${manifestPath}`);
if (manifest.schemaVersion !== 1) die(`不支持的 schemaVersion：${JSON.stringify(manifest.schemaVersion)}（预期 1）`);
if (typeof manifest.version !== 'string' || manifest.version === '') die('清单缺少 version 字段');
if (/[\\/]/.test(manifest.version) || manifest.version.includes('..') || /\s/.test(manifest.version)) {
  die(`清单版本号不安全，拒绝用于镜像路径：${JSON.stringify(manifest.version)}`);
}
if (expectVersion !== undefined && manifest.version !== expectVersion) {
  die(`清单版本 ${manifest.version} 与期望版本 ${expectVersion} 不一致（本地清单与目标发布不匹配？）`);
}
if (!Array.isArray(manifest.artifacts) || manifest.artifacts.length === 0) die('清单 artifacts 为空或不是数组');
if (baseUrl !== undefined) validateBaseUrl(baseUrl);

// --- 逐制品校验并改写 URL ---
const verified = [];
for (const [i, art] of manifest.artifacts.entries()) {
  if (!art || typeof art !== 'object') die(`artifacts[${i}] 不是对象`);
  const name = artifactFilename(art.url);
  const local = path.join(artifactsDir, name);
  let st;
  try {
    st = statSync(local);
  } catch {
    die(`制品本地文件缺失：${local}（清单 artifacts[${i}]）`);
  }
  if (!st.isFile()) die(`制品本地路径不是文件：${local}`);
  if (typeof art.size !== 'number' || art.size !== st.size) {
    die(`制品大小与清单不一致：${name}（本地 ${st.size}B ≠ 清单 ${JSON.stringify(art.size)}B）`);
  }
  if (art.digestAlgo !== 'sha512') {
    die(`制品摘要算法暂不支持：${name}（digestAlgo=${JSON.stringify(art.digestAlgo)}，目前仅支持 sha512）`);
  }
  if (typeof art.digest !== 'string' || art.digest === '') die(`制品缺少摘要：${name}`);
  const hex = await sha512File(local);
  const b64 = Buffer.from(hex, 'hex').toString('base64');
  if (b64 !== art.digest) {
    die(`制品 sha512 与清单不一致：${name}（本地 ${b64} ≠ 清单 ${art.digest}）`);
  }
  log('INFO', `制品校验通过：${name} platform=${art.platform ?? '?'} arch=${art.arch ?? '?'} size=${st.size} sha512=${hex.slice(0, 16)}…`);
  if (baseUrl !== undefined) {
    art.url = `${baseUrl}/releases/${encodeURIComponent(manifest.version)}/${encodeURIComponent(name)}`;
  }
  verified.push(name);
}

// --- 输出 ---
if (outputPath) {
  // 改写后自检：除 url 外一切字段必须与原清单一致
  const out = JSON.parse(readFileSync(manifestPath, 'utf8'));
  for (let i = 0; i < out.artifacts.length; i++) {
    const before = { ...out.artifacts[i] };
    const after = manifest.artifacts[i];
    for (const k of Object.keys(before)) {
      if (k === 'url') continue;
      if (JSON.stringify(before[k]) !== JSON.stringify(after?.[k])) {
        die(`改写自检失败：artifacts[${i}].${k} 被意外改动`);
      }
    }
    if (!after.url.startsWith(`${baseUrl}/releases/${encodeURIComponent(manifest.version)}/`)) {
      die(`改写自检失败：artifacts[${i}].url 未指向镜像`);
    }
  }
  for (const k of ['schemaVersion', 'version', 'channel', 'name', 'notes', 'publishedAt']) {
    if (JSON.stringify(out[k]) !== JSON.stringify(manifest[k])) {
      die(`改写自检失败：顶层字段 ${k} 被意外改动`);
    }
  }
  mkdirSync(path.dirname(outputPath), { recursive: true });
  writeFileSync(outputPath, `${JSON.stringify(manifest, null, 2)}\n`);
  log('INFO', `镜像版本清单已生成：${outputPath}（${manifest.artifacts.length} 个制品 URL 指向 ${baseUrl}，摘要/大小未改动）`);
}

if (listFiles) {
  for (const name of verified) process.stdout.write(`${name}\n`);
}
log('INFO', `共 ${verified.length} 个制品校验通过（size/sha512 与清单一致）`);
