// 前端双语词典校验（零依赖 Node，无浏览器）。
// 校验点：
//   1) DICT 完整性：每个 key 的 zh/en 均非空，且两侧参数占位符 {n} 一一对齐；
//   2) 静态 data-i18n* 属性引用的 key 必须存在于 DICT；
//   3) main.js 中 t("literal") 直接引用的 key 必须存在于 DICT；
//   4) 反向死键检查：DICT 中每个 key 至少在 index.html 或 main.js 出现过；
//   5) 语言解析与回退：首选语言顺序、显式偏好直选、未知偏好回退 system、
//      未知 key 原样返回；
//   6) 参数插值：单次扫描替换（参数内容含 {1}/花括号/$ 不被递归替换/二次处理）；
//   7) 校验函数自身对不匹配 fixture 必须报告失败（防“只记录不失败”的回归）。
// 运行：node scripts/verify-i18n.mjs
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";

const ROOT = dirname(new URL(import.meta.url).pathname) + "/..";
const html = readFileSync(join(ROOT, "frontend/dist/index.html"), "utf8");
const mainSrc = readFileSync(join(ROOT, "frontend/dist/main.js"), "utf8");
const i18nMod = await import(pathToFileURL(join(ROOT, "frontend/dist/i18n.js")).href);
const { DICT, resolveLang, systemLang, langFromTags, normalizePref, normalizeLang, applyPref, t } = i18nMod;

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

// validateDict：词典完整性校验（可独立单测/自检）。返回错误列表，空列表 = 全部通过。
export function validateDict(dict) {
  const errors = [];
  for (const key of Object.keys(dict)) {
    const e = dict[key];
    if (!e || !String(e.zh ?? "").trim() || !String(e.en ?? "").trim()) {
      errors.push(`${key}: 缺少 zh 或 en`);
      continue;
    }
    const zhP = placeholders(e.zh);
    const enP = placeholders(e.en);
    for (const p of zhP) if (!enP.has(p)) errors.push(`${key}: en 缺少占位符 ${p}`);
    for (const p of enP) if (!zhP.has(p)) errors.push(`${key}: zh 缺少占位符 ${p}`);
  }
  return errors;
}

// ---------- 1) DICT 完整性 ----------
const keys = Object.keys(DICT);
check("词典非空", keys.length > 0, `keys=${keys.length}`);
const dictErrors = validateDict(DICT);
check("词典完整性（zh/en 非空且占位符对齐）", dictErrors.length === 0, dictErrors.slice(0, 8).join("；"));

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

// ---------- 5) 校验函数自检（不匹配 fixture 必须失败） ----------
const badFixture = { "fixture.mismatch": { zh: "值 {0} 与 {1}", en: "value {0}" } };
const badErrors = validateDict(badFixture);
check("自检：占位符不匹配 fixture 必须报告失败", badErrors.length === 1 && badErrors[0].includes("en 缺少占位符 {1}"), JSON.stringify(badErrors));
check(
  "自检：缺 zh/en 的 fixture 必须报告失败",
  validateDict({ "fixture.empty": { zh: "", en: "x" } }).length === 1 && validateDict({ "fixture.onlyZh": { zh: "x" } }).length === 1,
);
check("自检：合法 fixture 通过", validateDict({ "fixture.ok": { zh: "你好 {0}", en: "hi {0}" } }).length === 0);

// ---------- 6) 解析与回退（首选顺序优先） ----------
check("显式偏好：zh-CN / en 直选", resolveLang("zh-CN") === "zh-CN" && resolveLang("en") === "en");
check("未知偏好回退 system 解析", ["fr-FR", "", "xx", undefined].every((p) => ["zh-CN", "en"].includes(resolveLang(p))));
check("system 解析结果合法", ["zh-CN", "en"].includes(systemLang()));
check("首选顺序：英文首选 + 次选中文 → 英文（不因列表含中文而误选）", langFromTags(["en-US", "zh-CN"]) === "en" && langFromTags(["fr-FR", "zh-CN"]) === "en");
check("首选顺序：中文首选 → 中文", langFromTags(["zh-Hans-CN", "en"]) === "zh-CN" && langFromTags(["zh"]) === "zh-CN");
check("首选顺序：空列表/空串回退英文", langFromTags([]) === "en" && langFromTags(["", "zh-CN"]) === "zh-CN" && langFromTags([null, undefined]) === "en");
check("偏好/语言归一", normalizePref("fr") === "system" && normalizePref("en") === "en" && normalizeLang("zh-CN") === "zh-CN" && normalizeLang("fr") === null);
check("未知 key 原样返回（可见可排查）", t("no.such.key") === "no.such.key");

// ---------- 7) 插值：单次扫描，不递归 ----------
applyPref("zh-CN", { persist: false });
check("参数插值基础", t("update.found", "1.2.3").includes("1.2.3"));
check(
  "参数内容 {1} 不被后续参数递归替换",
  t("srv.linked", "a{1}b", "C") === "关联 a{1}b 个隧道 · 已启用 C 个",
  t("srv.linked", "a{1}b", "C"),
);
check("参数内容 {1} 单独作为参数原样保留", t("srv.linked", "{1}", "X") === "关联 {1} 个隧道 · 已启用 X 个", t("srv.linked", "{1}", "X"));
check("参数内容花括号/美元符原样保留", t("update.found", "100$ {0}{2}") === "发现新版本 v100$ {0}{2}", t("update.found", "100$ {0}{2}"));
check("缺失参数占位符原样保留", t("srv.linked", "A") === "关联 A 个隧道 · 已启用 {1} 个", t("srv.linked", "A"));
applyPref("en", { persist: false });
check("英文插值", t("srv.linked", "A", "B") === "A tunnel(s) linked · B enabled", t("srv.linked", "A", "B"));

// ---------- 汇总 ----------
console.log(`\n===== 词典校验汇总：${pass}/${pass + fail} 通过（词典 ${keys.length} 条） =====`);
process.exit(fail ? 1 : 0);