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

// ---------- 8) 运行日志与诊断通道固定英文 ----------
// 前端保证：FrontendLog(...) 实参不得含 CJK 字面量（诊断通道落后端终端日志，
// 不随界面语言翻译；可见 toast 仍走 t() 双语）。逐个 FrontendLog( 调用做
// 括号配平后扫描实参文本。
function frontendlLogArgs(src) {
  const args = [];
  let idx = src.indexOf("FrontendLog(");
  while (idx !== -1) {
    let depth = 0;
    let end = -1;
    for (let i = idx + "FrontendLog".length; i < src.length; i++) {
      const c = src[i];
      if (c === "(") depth++;
      else if (c === ")") {
        depth--;
        if (depth === 0) {
          end = i;
          break;
        }
      }
    }
    if (end === -1) break; // 括号不配平：交给语法工具报错，这里不误报
    args.push(src.slice(idx + "FrontendLog(".length, end));
    idx = src.indexOf("FrontendLog(", end);
  }
  return args;
}
const hanRe = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u;
const flArgs = frontendlLogArgs(mainSrc);
const flBad = flArgs.filter((a) => hanRe.test(a));
check(
  `FrontendLog 诊断通道无中文文案（${flArgs.length} 处调用）`,
  flArgs.length > 0 && flBad.length === 0,
  flBad.join(" | "),
);
// 日志面板公共归属固定 System（不随界面语言翻译，不进词典）
check(
  "日志归属固定 System（渲染不用词典 key）",
  mainSrc.includes('e.serverName || "System"') && !mainSrc.includes('t("log.system")'),
);
// 语言入口契约：齿轮设置弹层内三选一（system/zh-CN/en，允许回到自动），无两态取反切换
const langPrefs = [...html.matchAll(/data-lang-pref="([^"]+)"/g)].map((m) => m[1]);
check(
  "语言入口在设置弹层内三选一（system/zh-CN/en，可回到自动）",
  html.includes('id="settings-menu"') &&
    html.includes('aria-haspopup="menu"') &&
    html.includes('role="menuitemradio"') &&
    langPrefs.join(",") === "system,zh-CN,en" &&
    mainSrc.includes("setLanguagePref(langOpt.dataset.langPref)") &&
    !mainSrc.includes('currentLang() === "zh-CN" ? "en" : "zh-CN"') &&
    !mainSrc.includes("LANG_ORDER"),
  `prefs=${langPrefs.join(",")}`,
);
// 设置按钮保持静态图标：切语言只刷新 title/aria，不重写按钮内容（SVG 不变体）
check(
  "设置按钮为静态图标（切语言不重写按钮文本/图标）",
  html.includes('id="btn-settings"') &&
    html.includes('aria-haspopup="menu"') &&
    !mainSrc.includes('$("btn-settings").textContent') &&
    !mainSrc.includes('$("btn-settings").innerHTML'),
);

// 语言菜单为三行纯语言名：无 code 括号、无「跟随系统」后缀、无 system 副文本节点/key
check(
  "语言菜单为三行纯语言名（自动/简体中文/英语，无 code 与后缀）",
  DICT["lang.optionSystem"]?.zh === "自动" &&
    DICT["lang.optionSystem"]?.en === "Auto" &&
    DICT["lang.optionZh"]?.zh === "简体中文" &&
    DICT["lang.optionZh"]?.en === "Simplified Chinese" &&
    DICT["lang.optionEn"]?.zh === "英语" &&
    DICT["lang.optionEn"]?.en === "English" &&
    !("lang.systemResolved" in DICT) &&
    !html.includes("lang-system-note"),
);

// ---------- 9) README：保留顶部语言导航，且不出现 verify-i18n 命令 ----------
const readmeEn = readFileSync(join(ROOT, "README.md"), "utf8");
const readmeZh = readFileSync(join(ROOT, "README_zh-CN.md"), "utf8");
check(
  "README 不含语言/翻译内容（界面语言、双语 UI 行、UI language、Translation contributions）",
  !readmeEn.includes("UI language") &&
    !readmeEn.includes("Translation contributions") &&
    !readmeEn.includes("Simplified Chinese / English UI") &&
    !readmeZh.includes("界面语言") &&
    !readmeZh.includes("贡献翻译") &&
    !readmeZh.includes("简体中文 / 英文界面"),
);
check(
  "README 语言导航存在且无 verify-i18n 命令",
  readmeEn.startsWith("English | [简体中文](README_zh-CN.md)") &&
    readmeZh.startsWith("[English](README.md) | 简体中文") &&
    !readmeEn.includes("verify-i18n") &&
    !readmeZh.includes("verify-i18n"),
);

// ---------- 10) 表单小问号帮助：短标题 + 帮助 key + 文案深度 ----------
// 长解释从小问号入口（data-help）延迟展示：主 label 必须是短标题（不含括号/长解释），
// 可选性用可见短标记（badge）表达；每个帮助 key 都必须存在且与短标题不同；
// HTTP/根域名/访问协议/本地 IP 帮助必须覆盖服务端配置、DNS、反向代理/网关等关键语义；
// 旧的 HTTP 开关常驻 hint 行与 key 必须移除。
const helpKeys = [...html.matchAll(/data-help="([^"]+)"/g)].map((m) => m[1]);
const helpAriaKeys = [...html.matchAll(/data-i18n-aria="([^"]*HelpAria)"/g)].map((m) => m[1]);
const SHORT_TITLES = [
  ["form.localIP", "本地 IP", "Local IP"],
  ["form.subdomain", "子域名前缀", "Subdomain prefix"],
  ["form.remotePort", "远程端口", "Remote port"],
  ["srvForm.authToken", "authToken", "authToken"],
  ["srvForm.baseDomain", "根域名", "Base domain"],
  ["srvForm.scheme", "访问协议", "Access scheme"],
  ["srvForm.publicPort", "访问端口", "Access port"],
  ["srvForm.httpToggle", "支持 HTTP 域名", "HTTP domains"],
];
const HELP_TARGETS = [
  ["form.localIP", "form.localIPHelp"],
  ["form.subdomain", "form.subdomainHelp"],
  ["form.remotePort", "form.remotePortHelp"],
  ["srvForm.authToken", "srvForm.authTokenHelp"],
  ["srvForm.baseDomain", "srvForm.baseDomainHelp"],
  ["srvForm.scheme", "srvForm.schemeHelp"],
  ["srvForm.publicPort", "srvForm.publicPortHelp"],
  ["srvForm.httpToggle", "srvForm.httpToggleHelp"],
];
check(
  `小问号入口齐备（${helpKeys.length} 个 data-help / ${helpAriaKeys.length} 个 aria 均进词典）`,
  helpKeys.length === 8 &&
    helpAriaKeys.length === 8 &&
    helpKeys.every((k) => k in DICT) &&
    helpAriaKeys.every((k) => k in DICT) &&
    HELP_TARGETS.every(([, h]) => helpKeys.includes(h)),
  `help=${helpKeys.join(",")} aria=${helpAriaKeys.join(",")}`,
);
check(
  "主 label 为短标题（与词典一致，不含括号/长解释）",
  SHORT_TITLES.every(
    ([k, zh, en]) => DICT[k]?.zh === zh && DICT[k]?.en === en && !/[（(]/.test(DICT[k].zh) && !/[（(]/.test(DICT[k].en),
  ),
  SHORT_TITLES.map(([k]) => `${k}=${DICT[k]?.zh}`).join("；"),
);
check(
  "帮助文案非空、双语齐备且与短标题不同",
  HELP_TARGETS.every(([titleKey, helpKey]) => {
    const h = DICT[helpKey];
    return Boolean(h?.zh?.trim()) && Boolean(h?.en?.trim()) && h.zh !== DICT[titleKey].zh && h.en !== DICT[titleKey].en;
  }),
  HELP_TARGETS.map(([, h]) => h).join(","),
);
check(
  "authToken：可见位置有独立轻量「可选」标记（不是长括号文案）",
  DICT["srvForm.authTokenBadge"]?.zh === "可选" &&
    DICT["srvForm.authTokenBadge"]?.en === "Optional" &&
    !/[（(]/.test(DICT["srvForm.authTokenBadge"].zh) &&
    html.includes('class="field-badge"') &&
    html.includes('data-i18n="srvForm.authTokenBadge"') &&
    html.indexOf('data-i18n="srvForm.authTokenBadge"') < html.indexOf('data-help="srvForm.authTokenHelp"'),
  `badge=${DICT["srvForm.authTokenBadge"]?.zh}/${DICT["srvForm.authTokenBadge"]?.en}`,
);
const zhHelp = (k) => DICT[k].zh;
const enHelp = (k) => DICT[k].en;
check(
  "authToken 帮助：说明控制连接鉴权与可留空条件",
  zhHelp("srvForm.authTokenHelp").includes("控制连接") &&
    zhHelp("srvForm.authTokenHelp").includes("鉴权") &&
    zhHelp("srvForm.authTokenHelp").includes("可留空") &&
    enHelp("srvForm.authTokenHelp").includes("control connection") &&
    enHelp("srvForm.authTokenHelp").includes("leave empty"),
  zhHelp("srvForm.authTokenHelp"),
);
check(
  "HTTP domains 帮助：frps.toml 可执行配置（vhostHTTPPort/subDomainHost/放行/重启）+ 不自动改服务端",
  ["frps.toml", "vhostHTTPPort", "subDomainHost", "*.example.com", "放行", "重启", "不会自动修改服务端", "根域名", "访问协议", "端口"].every(
    (s) => zhHelp("srvForm.httpToggleHelp").includes(s),
  ) &&
    ["frps.toml", "vhostHTTPPort", "subDomainHost", "*.example.com", "restart", "base domain", "access scheme", "port"].every((s) =>
      enHelp("srvForm.httpToggleHelp").includes(s),
    ),
  zhHelp("srvForm.httpToggleHelp"),
);
check(
  "Base domain 帮助：*.example.com DNS + 不带 * + 与 subDomainHost 一致 + blog.example.com 入口",
  ["*.example.com", "不带", "subDomainHost", "vhostHTTPPort", "blog.example.com"].every((s) => zhHelp("srvForm.baseDomainHelp").includes(s)) &&
    ["*.example.com", "without", "subDomainHost", "vhostHTTPPort", "blog.example.com"].every((s) =>
      enHelp("srvForm.baseDomainHelp").includes(s),
    ),
  zhHelp("srvForm.baseDomainHelp"),
);
check(
  "Access scheme 帮助：只描述对外 URL、不为 Frps 开 TLS；直连 http + 反代网关按公开入口",
  ["不会为 Frps 开启 TLS", "vhostHTTPSPort", "vhostHTTPPort", "http", "反向代理", "网关", "公开入口"].every((s) =>
    zhHelp("srvForm.schemeHelp").includes(s),
  ) &&
    ["reverse proxy", "gateway", "TLS", "vhostHTTPPort", "public entry"].every((s) => enHelp("srvForm.schemeHelp").includes(s)),
  zhHelp("srvForm.schemeHelp"),
);
check(
  "双语均覆盖关键配置语义（*.example.com / vhostHTTPPort / subDomainHost）",
  ["*.example.com", "vhostHTTPPort", "subDomainHost"].every((tk) =>
    [zhHelp("srvForm.httpToggleHelp"), zhHelp("srvForm.baseDomainHelp"), zhHelp("srvForm.schemeHelp")].join(" ").includes(tk),
  ) &&
    ["*.example.com", "vhostHTTPPort", "subDomainHost"].every((tk) =>
      [enHelp("srvForm.httpToggleHelp"), enHelp("srvForm.baseDomainHelp"), enHelp("srvForm.schemeHelp")].join(" ").includes(tk),
    ),
  [zhHelp("srvForm.httpToggleHelp"), zhHelp("srvForm.baseDomainHelp"), zhHelp("srvForm.schemeHelp")].join(" | "),
);
check(
  "Local IP 帮助：本机 IP 与同内网其他设备均可填 + frpc 转发语义",
  ["本机 IP", "内网", "frpc"].every((s) => zhHelp("form.localIPHelp").includes(s)) &&
    enHelp("form.localIPHelp").includes("LAN") &&
    enHelp("form.localIPHelp").includes("frpc"),
  zhHelp("form.localIPHelp"),
);
check(
  "HTTP 开关常驻 hint 行与旧 key 已移除（说明改由小问号承载）",
  !html.includes("server-http-toggle-hint") &&
    !("srvForm.httpToggleHint" in DICT) &&
    html.includes('data-help="srvForm.httpToggleHelp"'),
);

// ---------- 汇总 ----------
console.log(`\n===== 词典校验汇总：${pass}/${pass + fail} 通过（词典 ${keys.length} 条） =====`);
process.exit(fail ? 1 : 0);