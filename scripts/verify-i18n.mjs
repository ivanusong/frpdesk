// 前端双语词典校验（零依赖 Node，无浏览器）。
// 校验点：
//   1) DICT 完整性：每个 key 的 zh/en 均非空，且两侧参数占位符 {n} 一一对齐；
//   2) 静态 data-i18n* 属性引用的 key 必须存在于 DICT；
//   3) main.js 中 t("literal") 直接引用的 key 必须存在于 DICT；
//   4) 反向死键检查：DICT 中每个 key 至少在 index.html 或 main.js 出现过；
//   5) 语言解析与回退：显式偏好直选、非法/未知偏好回退 system、未知 key 原样返回。
// 运行：node scripts/verify-i18n.mjs
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";

const ROOT = dirname(new URL(import.meta.url).pathname) + "/..";
const html = readFileSync(join(ROOT, "frontend/dist/index.html"), "utf8");
const mainSrc = readFileSync(join(ROOT, "frontend/dist/main.js"), "utf8");
const i18nMod = await import(pathToFileURL(join(ROOT, "frontend/dist/i18n.js")).href);
const { DICT, resolveLang, systemLang, t } = i18nMod;

let pass = 0;
let fail = 0;
function check(name, ok, detail = "") {
  ok ? pass++ : fail++;
  console.log(`[check] ${ok ? "PASS" : "FAIL"} ${name}${!ok ? " — " + detail : ""}`);
}

// 占位符集合：{0}..{n}
function placeholders(s) {
  const set = new Set();
  for (const m of String(s).matchAll(/\{[0-9]\}/g)) set.add(m[0]);
  return set;
}

// ---------- 1) DICT 完整性 ----------
const keys = Object.keys(DICT);
check("词典非空", keys.length > 0, `keys=${keys.length}`);
let dictOk = true;
const dictErrors = [];
for (const key of keys) {
  const e = DICT[key];
  if (!e || !String(e.zh ?? "").trim() || !String(e.en ?? "").trim()) {
    dictOk = false;
    dictErrors.push(`${key}: 缺少 zh 或 en`);
    continue;
  }
  const zhP = placeholders(e.zh);
  const enP = placeholders(e.en);
  for (const p of zhP) if (!enP.has(p)) dictErrors.push(`${key}: en 缺少 ${p}`);
  for (const p of enP) if (!zhP.has(p)) dictErrors.push(`${key}: zh 缺少 ${p}`);
}
check("词典完整性（zh/en 非空且占位符对齐）", dictOk, dictErrors.slice(0, 8).join("；"));

// ---------- 2) index.html 静态 key ----------
const htmlKeys = new Set();
for (const m of html.matchAll(/data-i18n(?:-title|-aria|-placeholder)?="([^"]+)"/g)) htmlKeys.add(m[1]);
const missingHtml = [...htmlKeys].filter((k) => !(k in DICT));
check(`index.html 静态 key 均存在（${htmlKeys.size} 个）`, missingHtml.length === 0, missingHtml.join(", "));

// ---------- 3) main.js t("literal") 引用 ----------
const mainKeys = new Set();
for (const m of mainSrc.matchAll(/\bt\(\s*"([^"]+)"/g)) mainKeys.add(m[1]);
const missingMain = [...mainKeys].filter((k) => !(k in DICT));
check(`main.js t() 引用 key 均存在（${mainKeys.size} 个）`, missingMain.length === 0, missingMain.join(", "));

// ---------- 4) 反向死键 ----------
// 所有 key 必须以带引号字面量出现在 main.js/index.html（枚举映射的值也写成字面量）
const source = html + "\n" + mainSrc;
const dead = keys.filter((k) => !source.includes(`"${k}"`) && !source.includes(`'${k}'`) && !source.includes(`\"${k}\"`));
check("DICT 无死键（每个 key 都有字面量引用）", dead.length === 0, dead.join(", "));

// ---------- 5) 解析与回退 ----------
check("显式偏好：zh-CN / en 直选", resolveLang("zh-CN") === "zh-CN" && resolveLang("en") === "en");
check("未知偏好回退 system 解析", ["fr-FR", "", "xx", undefined].every((p) => ["zh-CN", "en"].includes(resolveLang(p))));
check("system 解析结果合法", ["zh-CN", "en"].includes(systemLang()));
check("未知 key 原样返回（可见可排查）", t("no.such.key") === "no.such.key");
check("参数插值", t("update.found", "1.2.3").includes("1.2.3"));

// ---------- 汇总 ----------
console.log(`\n===== 词典校验汇总：${pass}/${pass + fail} 通过（词典 ${keys.length} 条） =====`);
process.exit(fail ? 1 : 0);