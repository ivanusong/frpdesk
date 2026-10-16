// 多 Frps UI 行为验证（零依赖 Node，无浏览器）。
// 原理：自建轻量 DOM 桩解析 frontend/dist/index.html 为可操作树；
// 将 main.js 的 bindings import 替换为受控 stub 后以临时 ESM 模块加载真实实现，
// 在桩 DOM 上执行场景并断言（模态初始化/保存提示/全部启停/动画保护窗补绘等）。
// 运行：node scripts/verify-multiserver-ui.mjs
import { readFileSync, writeFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { pathToFileURL } from "node:url";

const ROOT = dirname(new URL(import.meta.url).pathname) + "/..";
const html = readFileSync(join(ROOT, "frontend/dist/index.html"), "utf8");
let mainSrc = readFileSync(join(ROOT, "frontend/dist/main.js"), "utf8");
const i18nSrc = readFileSync(join(ROOT, "frontend/dist/i18n.js"), "utf8");
const css = readFileSync(join(ROOT, "frontend/dist/style.css"), "utf8");

let pass = 0;
let fail = 0;
function check(name, ok, detail = "") {
  ok ? pass++ : fail++;
  console.log(`[check] ${ok ? "PASS" : "FAIL"} ${name}${!ok ? " — " + detail : ""}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------- 轻量 DOM 桩 ----------
let rejections = 0;
process.on("unhandledRejection", (e) => { rejections++; console.error("[unhandledRejection]", e && (e.stack || e.message || e)); });

const VOID = new Set(["input", "br", "img", "meta", "link", "hr", "source"]);

class El {
  constructor(tag, parent) {
    this.tagName = tag.toUpperCase();
    this.parent = parent ?? null;
    this.children = [];
    this.attrs = {};
    this.handlers = {};
    this.dataset = new Proxy({}, {
      get: (_t, k) => this.attrs["data-" + String(k).replaceAll("_", "-")],
      set: (_t, k, v) => ((this.attrs["data-" + String(k).replaceAll("_", "-")] = String(v)), true),
      deleteProperty: (_t, k) => (delete this.attrs["data-" + String(k).replaceAll("_", "-")], true),
    });
    this.style = {};
    this._class = new Set();
    this._text = "";
    this.hidden = false;
    this._value = "";
    this.checked = false;
    this.disabled = false;
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.offsetWidth = 0;
  }
  get classList() {
    const self = this;
    return {
      add: (...cs) => cs.forEach((c) => self._class.add(c)),
      remove: (...cs) => cs.forEach((c) => self._class.delete(c)),
      toggle: (c, force) => {
        const on = force === undefined ? !self._class.has(c) : Boolean(force);
        on ? self._class.add(c) : self._class.delete(c);
        return on;
      },
      contains: (c) => self._class.has(c),
    };
  }
  get className() {
    return [...this._class].join(" ");
  }
  set className(v) {
    this._class = new Set(String(v).split(/\s+/).filter(Boolean));
  }
  get id() {
    return this.attrs.id ?? "";
  }
  set id(v) {
    this.attrs.id = v;
  }
  get textContent() {
    return this._text + this.children.map((c) => c.textContent).join("");
  }
  set textContent(v) {
    this.children = [];
    this._text = String(v);
  }
  get innerHTML() {
    if (this._html !== undefined) return this._html;
    // 序列化：esc() 依赖 textContent→innerHTML 的转义往返，必须输出转义文本
    const escText = (t) => String(t).replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
    const parts = this.children.map((c) => c.serialize());
    if (this._text) parts.push(escText(this._text));
    return parts.join("");
  }
  set innerHTML(v) {
    this._html = String(v);
    this.children = [];
    this._text = "";
    parseInto(String(v), this);
  }
  serialize() {
    const tag = this.tagName.toLowerCase();
    const attrs = Object.entries(this.attrs)
      .filter(([k]) => k !== "class")
      .map(([k, v]) => (v === "" ? ` ${k}` : ` ${k}="${String(v).replaceAll('"', "&quot;")}"`))
      .join("");
    const cls = this._class.size ? ` class="${[...this._class].join(" ")}"` : "";
    if (VOID.has(tag)) return `<${tag}${cls}${attrs}>`;
    return `<${tag}${cls}${attrs}>${this.innerHTML}</${tag}>`;
  }
  get firstChild() {
    return this.children[0] ?? null;
  }
  setAttribute(k, v) {
    this.attrs[k] = String(v);
    if (k === "class") this.className = String(v);
  }
  getAttribute(k) {
    return this.attrs[k] ?? null;
  }
  removeAttribute(k) {
    delete this.attrs[k];
  }
  appendChild(el) {
    el.parent = this;
    this.children.push(el);
    return el;
  }
  remove() {
    if (this.parent) {
      const i = this.parent.children.indexOf(this);
      if (i >= 0) this.parent.children.splice(i, 1);
      this.parent = null;
    }
  }
  contains(el) {
    for (let p = el; p; p = p.parent) if (p === this) return true;
    return false;
  }
  addEventListener(type, fn) {
    (this.handlers[type] = this.handlers[type] ?? []).push(fn);
  }
  removeEventListener(type, fn) {
    this.handlers[type] = (this.handlers[type] ?? []).filter((f) => f !== fn);
  }
  dispatchEvent(ev) {
    ev.preventDefault = ev.preventDefault ?? (() => (ev.defaultPrevented = true));
    ev.stopPropagation = ev.stopPropagation ?? (() => {});
    ev.target = ev.target ?? this;
    let node = ev.target;
    let stopped = false;
    while (node && !stopped) {
      ev.currentTarget = node;
      for (const fn of node.handlers[ev.type] ?? []) fn(ev);
      node = ev.bubbles === false ? null : node.parent;
      if (stopped) break;
    }
    // 冒泡末端：document 级监听（与真实 DOM 一致，事件最终到达 document）
    if (ev.bubbles !== false && !stopped) {
      ev.currentTarget = globalThis.document;
      for (const fn of globalThis.document.handlers[ev.type] ?? []) fn(ev);
    }
    return !ev.defaultPrevented;
  }
  closest(selector) {
    for (let p = this; p; p = p.parent) if (p.matches?.(selector)) return p;
    return null;
  }
  matches(selector) {
    return selector.split(",").some((s) => matchCompound(this, s.trim()));
  }
  querySelector(sel) {
    return this.querySelectorAll(sel)[0] ?? null;
  }
  querySelectorAll(sel) {
    const out = [];
    collectDescendants(this, sel, out);
    return out;
  }
  get elements() {
    const map = {};
    const walk = (el) => {
      for (const c of el.children) {
        if (c.attrs.name && !(c.attrs.name in map)) map[c.attrs.name] = c;
        walk(c);
      }
    };
    walk(this);
    return map;
  }
  // 真实 DOM 语义：value 始终为字符串（openServiceModal 回填数字也能与字符串断言比较）
  get value() {
    return this._value ?? "";
  }
  set value(v) {
    this._value = String(v ?? "");
  }
  focus() {
    globalThis.__lastFocus = this;
  }
  click() {
    if (this.disabled) return; // 真实浏览器语义：disabled 元素不派发 click
    this.dispatchEvent({ type: "click", bubbles: true });
  }
  getBoundingClientRect() {
    return { left: 0, right: 0, top: 0, bottom: 0, width: 0, height: 0 };
  }
}

// 后代组合选择器收集：selector 含空格时逐级匹配祖先链（"a b" = b 且存在祖先 a）
function collectDescendants(root, selector, out) {
  const parts = selector.split(/\s+/).filter(Boolean);
  const consider = (el) => {
    if (!el.matches?.(parts[parts.length - 1])) return;
    if (parts.length === 1) {
      out.push(el);
      return;
    }
    for (let p = el.parent; p; p = p.parent) {
      if (ancestorChain(p, parts, parts.length - 2)) {
        out.push(el);
        return;
      }
    }
  };
  const walk = (el) => {
    for (const c of el.children) {
      consider(c);
      walk(c);
    }
  };
  walk(root);
}
function ancestorChain(el, parts, i) {
  if (!el.matches?.(parts[i])) return false;
  if (i === 0) return true;
  for (let p = el.parent; p; p = p.parent) {
    if (p.matches?.(parts[i - 1]) && (i - 1 === 0 || ancestorChain(p, parts, i - 1))) return true;
  }
  return false;
}

// 复合选择器匹配：tag / .class / #id / [name="x"] / [attr]
function matchCompound(el, sel) {
  if (!el || !el.attrs) return false;
  const m = sel.match(/^([a-zA-Z][a-zA-Z0-9-]*)?((?:[.#][\w-]+|\[[^\]]+\])*)$/);
  if (!m) return false;
  const [, tag, rest] = m;
  if (tag && el.tagName !== tag.toUpperCase()) return false;
  if (!rest) return true;
  const parts = rest.match(/[.#][\w-]+|\[[^\]]+\]/g) ?? [];
  for (const p of parts) {
    if (p.startsWith(".")) {
      if (!el._class.has(p.slice(1))) return false;
    } else if (p.startsWith("#")) {
      if (el.attrs.id !== p.slice(1)) return false;
    } else {
      const am = p.slice(1, -1).match(/^([\w-]+)(?:="([^"]*)")?$/);
      if (!am) return false;
      const v = el.attrs[am[1]];
      if (am[2] === undefined ? v === undefined : v !== am[2]) return false;
    }
  }
  return true;
}

// 简易 HTML 解析（属性/文本/嵌套；script/style 内容只存不解析）
function parseInto(html, parent) {
  let i = 0;
  const stack = [parent];
  const re = /<\/?([a-zA-Z][a-zA-Z0-9-]*)((?:\s+[\w-]+(?:="[^"]*")?)*)\s*(\/?)>|<!--|<\/?[a-zA-Z][^>]*>/g;
  while (i < html.length) {
    const lt = html.indexOf("<", i);
    if (lt < 0) {
      stack[stack.length - 1]._text += html.slice(i);
      break;
    }
    if (lt > i) stack[stack.length - 1]._text += html.slice(i, lt);
    if (html.startsWith("<!--", lt)) {
      const end = html.indexOf("-->", lt);
      i = end < 0 ? html.length : end + 3;
      continue;
    }
    re.lastIndex = lt;
    const m = re.exec(html);
    if (!m || m.index !== lt) {
      stack[stack.length - 1]._text += "<";
      i = lt + 1;
      continue;
    }
    i = re.lastIndex;
    const tag = m[1].toLowerCase();
    if (m[0].startsWith("</")) {
      if (stack.length > 1) stack.pop();
      continue;
    }
    const el = new El(tag, stack[stack.length - 1]);
    const attrRe = /([\w-]+)(?:="([^"]*)")?/g;
    let am;
    while ((am = attrRe.exec(m[2] ?? ""))) el.attrs[am[1]] = am[2] ?? "";
    if (el.attrs.class) el.className = el.attrs.class;
    if ("disabled" in el.attrs) el.disabled = true; // boolean 属性同步到 property
    if ("checked" in el.attrs) el.checked = true;
    stack[stack.length - 1].children.push(el);
    if (!VOID.has(tag) && !m[3]) {
      if (tag === "script" || tag === "style") {
        const close = html.indexOf(`</${tag}>`, i);
        i = close < 0 ? html.length : close + tag.length + 3;
      } else {
        stack.push(el);
      }
    }
  }
  return parent.children;
}

// ---------- document / window 桩 ----------
const docEl = new El("html");
const body = new El("body", docEl);
docEl.children.push(body);
const document = {
  documentElement: docEl,
  body,
  _byId: new Map(),
  activeElement: null,
  addEventListener(type, fn) {
    (this.handlers[type] = this.handlers[type] ?? []).push(fn);
  },
  removeEventListener(type, fn) {
    this.handlers[type] = (this.handlers[type] ?? []).filter((f) => f !== fn);
  },
  handlers: {},
};
function indexTree(el) {
  if (el.attrs.id) document._byId.set(el.attrs.id, el);
  el.children.forEach(indexTree);
}
document.getElementById = (id) => document._byId.get(id) ?? null;
document.createElement = (tag) => new El(tag);
document.querySelector = (sel) => document.querySelectorAll(sel)[0] ?? null;
document.querySelectorAll = (sel) => {
  const out = [];
  docEl.children.forEach((c) => collectDescendants(c, sel, out));
  return out;
};

const storage = new Map();
// 语言：预置简体中文，使既有中文断言与宿主 Node 系统语言（通常 en）解耦；
// 语言切换/回退场景在下方显式驱动。
storage.set("frpdesk.language", "zh-CN");
const wailsListeners = {};
const wailsCalls = [];
const stubWindow = {
  localStorage: {
    getItem: (k) => (storage.has(k) ? storage.get(k) : null),
    setItem: (k, v) => storage.set(k, String(v)),
    removeItem: (k) => storage.delete(k),
  },
  matchMedia: () => ({ addEventListener() {}, matches: false }),
  wails: {
    Events: {
      On: (name, cb) => {
        (wailsListeners[name] = wailsListeners[name] ?? []).push(cb);
      },
    },
    Clipboard: {
      calls: [],
      SetText: (t) => {
        stubWindow.wails.Clipboard.calls.push(t);
        return Promise.resolve();
      },
    },
    Browser: { OpenURL: (u) => stubWindow.wails.Browser.calls.push(u) },
  },
};
stubWindow.wails.Browser.calls = [];
globalThis.window = stubWindow;
globalThis.document = document;
globalThis.localStorage = stubWindow.localStorage;
// navigator 在 node 22+ 为只读 getter，无需覆写
globalThis.__lastFocus = null;

// ---------- 后端 stub（快照可注入；每次调用记录） ----------
function makeSnapshot(over = {}) {
  const servers = over.servers ?? [
    { id: "srvA", name: "主服务器", serverAddr: "a.example.com", serverPort: 7000, authToken: "", httpEnabled: true, baseDomain: "a.com", scheme: "https", publicPort: 8443 },
    { id: "srvB", name: "备服务器", serverAddr: "b.example.com", serverPort: 7000, authToken: "", httpEnabled: false, baseDomain: "", scheme: "https", publicPort: 0 },
  ];
  const services = over.services ?? [
    { id: "svc1", name: "博客", type: "http", subdomain: "blog", localHost: "127.0.0.1", localPort: 8080, remotePort: 0, enabled: true, serverId: "srvA", icon: "🌐" },
    { id: "svc2", name: "SSH", type: "tcp", subdomain: "", localHost: "127.0.0.1", localPort: 22, remotePort: 36000, enabled: true, serverId: "srvB", icon: "🔌" },
  ];
  return {
    config: { servers, services, language: over.language ?? "zh-CN", global: { serverAddr: "", serverPort: 0, authToken: "", httpEnabled: false, baseDomain: "", scheme: "", publicPort: 0 } },
    status: { state: over.state ?? "running", message: "", startedAt: new Date().toISOString(), runningCount: over.runningCount ?? 1, totalCount: over.totalCount ?? 2, activeCount: over.activeCount ?? 1, failedCount: over.failedCount ?? 0 },
    serverStatuses: over.serverStatuses ?? [
      { serverId: "srvA", state: "running", message: "", startedAt: new Date().toISOString() },
      { serverId: "srvB", state: "stopped", message: "", startedAt: "" },
    ],
    // 后端解析后的生效语言（system 偏好的权威结果）：显式传入 over.resolvedLanguage
    language: over.resolvedLanguage ?? "zh-CN",
  };
}
let currentSnapshot = makeSnapshot();
const appStub = {
  calls: [],
  results: {},
  record(name, args) {
    this.calls.push({ name, args });
    return this.results[name];
  },
  Snapshot() {
    this.calls.push({ name: "Snapshot", args: [] });
    return Promise.resolve(currentSnapshot);
  },
  Logs() {
    return Promise.resolve(this.record("Logs", []));
  },
  ProxyStatuses() {
    return Promise.resolve(this.record("ProxyStatuses", []));
  },
  FrontendLog(level, message) {
    // 记录诊断通道载荷：验证「本地化（中文）错误详情不进入英文日志通道」
    this.record("FrontendLog", [level, message]);
    return Promise.resolve();
  },
  UpdateInfo() {
    return Promise.resolve({ currentVersion: "0.0.0", state: "idle" });
  },
  CheckForUpdates() {
    return Promise.resolve({ currentVersion: "0.0.0", state: "idle" });
  },
  SetServiceEnabled: (...a) => Promise.resolve(appStub.record("SetServiceEnabled", a) ?? currentSnapshot),
  CreateService: (...a) => Promise.resolve(appStub.record("CreateService", a) ?? currentSnapshot),
  UpdateService: (...a) => Promise.resolve(appStub.record("UpdateService", a) ?? currentSnapshot),
  DeleteService: (...a) => Promise.resolve(appStub.record("DeleteService", a) ?? currentSnapshot),

  CreateServer: (...a) => Promise.resolve(appStub.record("CreateServer", a) ?? currentSnapshot),
  UpdateServer: (...a) => Promise.resolve(appStub.record("UpdateServer", a) ?? currentSnapshot),
  DeleteServer: (...a) => Promise.resolve(appStub.record("DeleteServer", a) ?? currentSnapshot),

  QuitApp: (...a) => Promise.resolve(appStub.record("QuitApp", a)),
  TestServerConnection: (...a) => {
    appStub.calls.push({ name: "TestServerConnection", args: a });
    const over = appStub.results.TestServerConnection;
    if (over && over.__pending) return over.__pending.then(() => over);
    if (over instanceof Error) return Promise.reject(over);
    return Promise.resolve(over ?? { success: true, message: "", latencyMs: 42 });
  },
  QRCode: () => Promise.resolve("data:image/png;base64,x"),
  // 语言绑定：返回 { preference, resolved } 最小契约；记录调用供断言。
  // 可用 appStub.langSystemResolved 指定 system 的后端解析结果（默认 en，
  // 与本机 Node 本地解析可能不同，用于验证前端采用后端 resolved 而不自行判定）。
  // appStub.results.SetLanguage 可设 {__pending:Promise} 延迟或 "reject" 失败。
  langSystemResolved: "en",
  languageState(pref) {
    const p = ["system", "zh-CN", "en"].includes(pref) ? pref : "system";
    return { preference: p, resolved: p === "system" ? appStub.langSystemResolved : p };
  },
  SetLanguage: (...a) => {
    appStub.calls.push({ name: "SetLanguage", args: a });
    const over = appStub.results.SetLanguage;
    if (over === "reject") return Promise.reject(new Error("语言保存失败"));
    if (over && over.__pending) return over.__pending.then(() => appStub.languageState(a[0]));
    return Promise.resolve(appStub.languageState(a[0]));
  },
};
globalThis.__wailsApi = { AppService: appStub };

// ---------- 加载 main.js（bindings import → 受控 stub；写临时模块后 import） ----------
parseInto(html, docEl);
docEl.children.forEach(indexTree);
mainSrc = mainSrc.replace(
  'import * as api from "./bindings/frpdesk/index.js";',
  "const api = globalThis.__wailsApi;",
);
const tmpDir = mkdtempSync(join(tmpdir(), "frpdesk-ui-verify-"));
const tmpModule = join(tmpDir, "main.test.mjs");
writeFileSync(tmpModule, mainSrc);
// main.js 相对导入 ./i18n.js：同一模块目录写出真实词典，保证模块图与产物一致
writeFileSync(join(tmpDir, "i18n.js"), i18nSrc);
const mod = await import(pathToFileURL(tmpModule).href);
const T = mod.__test;
if (!T) {
  console.error("[fatal] main.js 未导出 __test 测试钩子");
  process.exit(1);
}

// 等 boot 完成并渲染
for (let i = 0; i < 40 && !T.state.snapshot; i++) await sleep(25);
await sleep(30);

// ---------- 场景 S1/S2：纯地址与合格列表（真实实现） ----------
const srvA = { id: "a", name: "主", serverAddr: "a.com", serverPort: 7000, httpEnabled: true, baseDomain: "a.com", scheme: "https", publicPort: 8443 };
const srvB = { id: "b", name: "备", serverAddr: "b.com", serverPort: 7000, httpEnabled: false, baseDomain: "", scheme: "https", publicPort: 0 };
check("地址：HTTP 走归属配置", T.svcPublicURL({ type: "http", subdomain: "blog" }, srvA) === "https://blog.a.com:8443");
check("地址：TCP host:port", T.svcPublicURL({ type: "tcp", remotePort: 36000 }, srvB) === "b.com:36000");
check("地址：UDP host:port（与 TCP 同形态）", T.svcPublicURL({ type: "udp", remotePort: 36053 }, srvB) === "b.com:36053");
check("地址：TCP IPv6 使用方括号", T.svcPublicURL({ type: "tcp", remotePort: 36000 }, { ...srvB, serverAddr: "::1" }) === "[::1]:36000");
check("地址：UDP IPv6 使用方括号", T.svcPublicURL({ type: "udp", remotePort: 36053 }, { ...srvB, serverAddr: "::1" }) === "[::1]:36053");
check("地址：TCP 已有 IPv6 方括号不重复添加", T.svcPublicURL({ type: "tcp", remotePort: 36000 }, { ...srvB, serverAddr: "[::1]" }) === "[::1]:36000");
check("地址：无归属/未开放/缺域名均为空", T.svcPublicURL({ type: "http", subdomain: "x" }, null) === "" && T.svcPublicURL({ type: "http", subdomain: "x" }, srvB) === "" && T.svcPublicURL({ type: "http", subdomain: "x" }, { ...srvA, baseDomain: "" }) === "");
check("selector：HTTP 过滤 httpEnabled、TCP/UDP 全部", T.eligibleServers("http", [srvA, srvB]).map((s) => s.id).join() === "a" && T.eligibleServers("tcp", [srvA, srvB]).length === 2 && T.eligibleServers("udp", [srvA, srvB]).length === 2);

// ---------- 场景 S2：boot 后初始渲染 ----------
const snapshot = T.state.snapshot;
check("boot 后快照加载", Boolean(snapshot) && snapshot.config.servers.length === 2);
check("服务器卡片渲染两列数据（2 张）", document.querySelectorAll("#server-list .server-item").length === 2);
check("底部计数 = 当前已映射隧道数（boot 无注册事件为 0）", document.getElementById("service-count").textContent === "0", document.getElementById("service-count").textContent);
check("计数 title 简短口径", (document.getElementById("service-count").title || "").includes("当前已映射隧道数量"), document.getElementById("service-count").title);
check("计数 title 简短口径（重复断言对齐）", (document.getElementById("service-count").title || "").includes("当前已映射隧道数量"), document.getElementById("service-count").title);
// 配置卡片简化：仅标题/状态tag/连接地址 + 底行常驻操作（无图标/HTTP入口/关联隧道/错误正文）
const srvCard = document.querySelector("#server-list .server-item");
check(
  "配置卡仅标题+状态tag+连接地址",
  srvCard?.querySelector(".service-name-text")?.textContent === "主服务器" &&
    (srvCard?.querySelector(".tag")?.textContent.length ?? 0) > 0 &&
    srvCard?.querySelector(".server-addr .mono")?.textContent === "a.example.com:7000",
  srvCard ? srvCard.textContent : "null",
);
check(
  "配置卡不含图标/HTTP入口/未开放提示/错误正文（关联隧道信息为合法内容）",
  srvCard !== null &&
    !srvCard.textContent.includes("HTTP 未开放") &&
    !srvCard.querySelector(".service-icon") &&
    !srvCard.querySelector(".server-http-entry, .srv-http-off, .srv-err") &&
    Boolean(srvCard.querySelector(".server-foot")),
  srvCard ? srvCard.textContent : "null",
);
check(
  "底行操作常驻：测试/编辑/删除在左（无运行开关、无 svc-op hover 隐藏类）",
  srvCard?.querySelector(".server-ops .btn")?.textContent === "测试" &&
    srvCard?.querySelector(".server-ops .btn[data-act=\"srv-edit\"]")?.textContent === "编辑" &&
    srvCard?.querySelector(".server-ops .btn-ghost-danger")?.textContent === "删除" &&
    !srvCard?.querySelector(".server-ops .switch") &&
    !srvCard?.querySelector(".server-ops .svc-op"),
);
check("配置卡保留 2 倍高度 token 与两列布局", css.includes("min-height: var(--server-card-min-h)") && css.includes("grid-template-columns: repeat(2, minmax(0, 1fr))"));

// ---------- 场景 S4：隧道模态字段初始化（新建/编辑） ----------
// 新建 HTTP：两台合格（A http、B 否 → 仅 A）→ 唯一自动选
T.openServiceModal("新增 HTTP 服务", null, "http");
const sel = document.getElementById("svc-server-select");
check("新建 HTTP：唯一合格自动选 A", sel.value === "srvA" && !sel.disabled);
check(
  "新建 HTTP 空前缀：提示自动生成子域名（不拼非法 URL）",
  !document.getElementById("svc-move-hint").hidden &&
    document.getElementById("svc-move-hint").textContent.includes("保存时自动生成子域名") &&
    !document.getElementById("svc-move-hint").textContent.includes("https://."),
  document.getElementById("svc-move-hint").textContent,
);
// 填入前缀后提示变为真实访问地址
document.getElementById("service-form").elements.subdomain.value = "blog";
document.getElementById("service-form").elements.subdomain.dispatchEvent({ type: "input", bubbles: true });
check(
  "新建 HTTP 填前缀：提示真实访问地址",
  document.getElementById("svc-move-hint").textContent.includes("访问地址：https://blog.a.com:8443"),
  document.getElementById("svc-move-hint").textContent,
);
T.closeServiceModal();

// 新建端口服务（TCP / UDP 统一入口）：协议选择器默认 TCP、可切换；多项无上次选择留空待选
T.openServiceModal("新增 TCP / UDP 服务", null, "tcp");
const protoSel = document.getElementById("svc-proto-select");
const protoRow = document.querySelector('[data-fields="proto"]');
check("新建端口服务：协议选择器可见且默认 TCP", protoRow.hidden === false && protoSel.value === "tcp" && document.getElementById("service-form").elements.type.value === "tcp", `proto=${protoSel.value}`);
check("新建 TCP：多项无上次选择留空", sel.value === "" && !sel.disabled, sel.value);
document.getElementById("service-form").dispatchEvent({ type: "submit", bubbles: true });
await sleep(20);
check("已有配置但未选择：提示选择目标", lastToast().includes("请选择目标 Frps"));
// 上次选择记忆：直接选 B 提交一次（走 stub），再打开应记住 B
sel.value = "srvB";
document.getElementById("service-form").dispatchEvent({ type: "submit", bubbles: true });
await sleep(20);
T.openServiceModal("新增 TCP / UDP 服务", null, "tcp");
check("新建 TCP：记住上次选择 B", sel.value === "srvB", sel.value);
// 弹窗内切 UDP：类型随动、字段组保持、目标列表刷新（UDP 记忆独立）
protoSel.value = "udp";
protoSel.dispatchEvent({ type: "change", bubbles: true });
check("切 UDP：类型随动且字段组保持", document.getElementById("service-form").elements.type.value === "udp" && document.querySelector('[data-fields="port"]').hidden === false && document.querySelector('[data-fields="http"]').hidden === true);
T.closeServiceModal();
T.openServiceModal("新增 TCP / UDP 服务", null, "udp");
check("新建 UDP：协议回显 UDP", protoSel.value === "udp", protoSel.value);
// 切回 TCP：类型随动
protoSel.value = "tcp";
protoSel.dispatchEvent({ type: "change", bubbles: true });
check("切回 TCP：类型随动", document.getElementById("service-form").elements.type.value === "tcp" && protoSel.value === "tcp");
T.closeServiceModal();

// ---------- 协议切换保留本次已选目标（不重置为历史值/原归属/清空） ----------
// 新建：手选 A（覆盖 tcp 记忆 srvB）后 TCP→UDP，保持 A；提交 payload 为实际选中值
T.openServiceModal("新增 TCP / UDP 服务", null, "tcp");
sel.value = "srvA"; // 模拟手动选择 A
protoSel.value = "udp";
protoSel.dispatchEvent({ type: "change", bubbles: true });
check("新建手选A后 TCP→UDP：保持A不重置", sel.value === "srvA" && document.getElementById("service-form").elements.type.value === "udp", sel.value);
document.getElementById("service-form").elements.remotePort.value = "36053";
document.getElementById("service-form").dispatchEvent({ type: "submit", bubbles: true });
await sleep(20);
const createdUdp = appStub.calls.filter((c) => c.name === "CreateService").at(-1);
check(
  "新建 UDP 提交 payload：type=udp/serverId=srvA/remotePort=36053",
  createdUdp?.args?.[0]?.type === "udp" && createdUdp?.args?.[0]?.serverId === "srvA" && createdUdp?.args?.[0]?.remotePort === 36053,
  JSON.stringify(createdUdp?.args?.[0]),
);
T.closeServiceModal();

// 编辑 TCP@srvA：手选 B 后切 UDP 仍 B；保存 payload type=udp/serverId=srvB/remotePort 回显保留
const editProtoSnap = makeSnapshot({
  services: [{ id: "svcT", name: "端口A", type: "tcp", subdomain: "", localHost: "127.0.0.1", localPort: 22, remotePort: 36022, enabled: true, serverId: "srvA", icon: "" }],
});
await T.applySnapshot(editProtoSnap);
T.openServiceModal("编辑 TCP 服务", editProtoSnap.config.services[0]);
check("编辑 TCP：原归属 A 自动选中", sel.value === "srvA", sel.value);
sel.value = "srvB"; // 手动改选 B
protoSel.value = "udp";
protoSel.dispatchEvent({ type: "change", bubbles: true });
check("编辑手选B后切UDP：仍B且type=udp", sel.value === "srvB" && document.getElementById("service-form").elements.type.value === "udp", sel.value);
document.getElementById("service-form").dispatchEvent({ type: "submit", bubbles: true });
await sleep(20);
const updProto = appStub.calls.filter((c) => c.name === "UpdateService").at(-1);
check(
  "编辑切协议保存：type=udp/serverId=srvB/remotePort=36022",
  updProto?.args?.[1]?.type === "udp" && updProto?.args?.[1]?.serverId === "srvB" && updProto?.args?.[1]?.remotePort === 36022,
  JSON.stringify(updProto?.args?.[1]),
);
T.closeServiceModal();

// 编辑 HTTP 服务：协议选择器隐藏（仅端口服务可见）；保留归属并标注（当前）
const blogSvc = T.state.snapshot.config.services.find((s) => s.id === "svc1");
T.openServiceModal("编辑 HTTP 服务", blogSvc, "http");
check("编辑 HTTP：协议选择器隐藏", document.querySelector('[data-fields="proto"]').hidden === true, String(document.querySelector('[data-fields="proto"]').hidden));
check("编辑：保留归属 A 并标（当前）", sel.value === "srvA" && sel.innerHTML.includes("（当前）"), sel.value);
// 切换到 B（若 B 不合格则列表仅 A——此处 A 唯一合格且为归属；改测提示：无变化时不提示中断）
check("编辑未切换：无「短暂中断」提示", !document.getElementById("svc-move-hint").textContent.includes("短暂中断"));
T.closeServiceModal();

// HTTP 空前缀：提示自动生成，不产生非法 URL
T.openServiceModal("新增 HTTP 服务", null, "http");
document.getElementById("service-form").elements.subdomain.value = "";
document.getElementById("service-form").elements.subdomain.dispatchEvent({ type: "input", bubbles: true });
const hintText = document.getElementById("svc-move-hint").textContent;
check("HTTP 空前缀：提示自动生成子域名且无非法 URL", hintText.includes("保存时自动生成子域名") && !hintText.includes("https://."), hintText);
T.closeServiceModal();

// ---------- 场景 S5：编辑保存（归属迁移提示 + serverId 传递 + toast + 失败保留输入） ----------
// 给备服务器开 HTTP（直接改 stub 快照源，重开模态走最新 state）
currentSnapshot = makeSnapshot({
  servers: [
    { id: "srvB", name: "备服务器", serverAddr: "b.example.com", serverPort: 7000, authToken: "", httpEnabled: true, baseDomain: "b.example.com", scheme: "https", publicPort: 8443 },
    { id: "srvA", name: "主服务器", serverAddr: "a.example.com", serverPort: 7000, authToken: "", httpEnabled: true, baseDomain: "a.com", scheme: "https", publicPort: 8443 },
  ],
  services: [{ ...blogSvc, serverId: "srvA" }],
});
await T.applySnapshot(currentSnapshot);
T.openServiceModal("编辑 HTTP 服务", blogSvc, "http");
check("编辑：两台均合格时保留归属 A", sel.value === "srvA", sel.value);
// 切换目标到 B → 提示新地址与短暂中断
sel.value = "srvB";
sel.dispatchEvent({ type: "change", bubbles: true });
const moveText = document.getElementById("svc-move-hint").textContent;
check("切换目标：提示新地址与短暂中断", moveText.includes("访问地址：https://blog.b.example.com:8443") && moveText.includes("短暂中断"), moveText);
// 提交：UpdateService 收到新归属 serverId=srvB；toast 为「更新」；模态关闭
document.getElementById("service-form").dispatchEvent({ type: "submit", bubbles: true });
await sleep(480); // > closeServiceModal 的 400ms 隐藏兜底
const upd = appStub.calls.filter((c) => c.name === "UpdateService").at(-1);
check("编辑保存：UpdateService 携带新归属 serverId", upd?.args?.[1]?.serverId === "srvB", JSON.stringify(upd?.args?.[1]?.serverId));
check("编辑保存：toast 为「服务已更新」", lastToast().includes("服务已更新"), lastToast());
check("编辑保存成功：模态已关闭", document.getElementById("service-modal").hidden === true);

// 失败场景：UpdateService reject → run() toast 错误原因、模态保持打开、新归属保留、无未处理 rejection
const beforeRej = rejections;
appStub.results.UpdateService = { error: true };
appStub.UpdateService = (...a) => {
  appStub.calls.push({ name: "UpdateService", args: a });
  return Promise.reject(new Error("目标 frps 连接失败，迁移未完成"));
};
T.openServiceModal("编辑 HTTP 服务", { ...blogSvc, serverId: "srvA" }, "http");
sel.value = "srvB";
sel.dispatchEvent({ type: "change", bubbles: true });
document.getElementById("service-form").dispatchEvent({ type: "submit", bubbles: true });
await sleep(30);
check("保存失败：错误原因 toast 呈现", lastToast().includes("目标 frps 连接失败"), lastToast());
check("保存失败：模态保持打开且归属选择保留", document.getElementById("service-modal").hidden === false && sel.value === "srvB");
check("保存失败：无未处理 Promise rejection", rejections === beforeRej, String(rejections - beforeRej));
T.closeServiceModal();
delete appStub.results.UpdateService;
appStub.UpdateService = (...a) => Promise.resolve(appStub.record("UpdateService", a) ?? currentSnapshot);

// ---------- HTTP 能力开关回归（change 驱动；无 ReferenceError；显隐/required/disabled/值保留） ----------
const srvForm = document.getElementById("server-form");
const httpBox = srvForm.elements.httpEnabled;
const httpFields = document.getElementById("srv-http-fields");

// R1 新建默认关：字段组折叠 + required/disabled 关闭 + aria-expanded=false
T.openServerModal(null);
check(
  "R1 新建默认关：折叠+baseDomain required/disabled 关闭+aria-expanded=false",
  httpFields.hidden === true &&
    httpBox.getAttribute("aria-expanded") === "false" &&
    srvForm.elements.baseDomain.required === false &&
    srvForm.elements.baseDomain.disabled === true &&
    srvForm.elements.scheme.disabled === true &&
    srvForm.elements.publicPort.disabled === true,
);

// R2 change 开启：展开 + required/disabled 恢复 + aria-expanded=true（无 ReferenceError 即执行到此）
httpBox.checked = true;
httpBox.dispatchEvent({ type: "change", bubbles: true });
check(
  "R2 开启：展开+baseDomain required/字段可用+aria-expanded=true",
  httpFields.hidden === false &&
    httpBox.getAttribute("aria-expanded") === "true" &&
    srvForm.elements.baseDomain.required === true &&
    srvForm.elements.baseDomain.disabled === false &&
    srvForm.elements.scheme.disabled === false &&
    srvForm.elements.publicPort.disabled === false,
);

// R3 填值后关闭：字段组折叠、值保留、required/disabled 再关
srvForm.elements.baseDomain.value = "legacy.com";
srvForm.elements.scheme.value = "https";
srvForm.elements.publicPort.value = "8443";
httpBox.checked = false;
httpBox.dispatchEvent({ type: "change", bubbles: true });
check(
  "R3 关闭：折叠保留值+required/disabled 关闭",
  httpFields.hidden === true &&
    srvForm.elements.baseDomain.required === false &&
    srvForm.elements.baseDomain.disabled === true &&
    srvForm.elements.baseDomain.value === "legacy.com" &&
    srvForm.elements.scheme.disabled === true &&
    srvForm.elements.publicPort.disabled === true,
);

// R4 重新开启：输入值仍在
httpBox.checked = true;
httpBox.dispatchEvent({ type: "change", bubbles: true });
check("R4 重新开启：保留输入值", httpFields.hidden === false && srvForm.elements.baseDomain.value === "legacy.com");

// R5 关闭态提交：payload 保留 httpEnabled=false 与域名/协议/端口 metadata
srvForm.elements.name.value = "遗留配置";
srvForm.elements.serverAddr.value = "legacy.example.com";
srvForm.elements.serverPort.value = 7000;
httpBox.checked = false;
httpBox.dispatchEvent({ type: "change", bubbles: true });
srvForm.dispatchEvent({ type: "submit", bubbles: true });
await sleep(480); // > closeServerModal 的 400ms 隐藏兜底
const created = appStub.calls.filter((c) => c.name === "CreateServer").at(-1);
check(
  "R5 关闭态提交：payload httpEnabled=false 且 metadata 保留",
  created?.args?.[0]?.httpEnabled === false &&
    created?.args?.[0]?.baseDomain === "legacy.com" &&
    created?.args?.[0]?.scheme === "https" &&
    created?.args?.[0]?.publicPort === 8443,
  JSON.stringify(created?.args?.[0]),
);
check("R5 提交成功：toast 配置已添加", lastToast().includes("配置已添加"), lastToast());

// R6 编辑初始化：开关配置状态与卡片运行开关含义区分（开启/关闭两种回显）
currentSnapshot = makeSnapshot();
await T.applySnapshot(currentSnapshot);
T.openServerModal({ id: "s1", name: "开HTTP", serverAddr: "x.com", serverPort: 7000, authToken: "", httpEnabled: true, baseDomain: "e.com", scheme: "https", publicPort: 8443 });
check(
  "R6 编辑(开启)：展开+required+aria-expanded=true+值回显",
  httpFields.hidden === false && httpBox.checked === true && httpBox.getAttribute("aria-expanded") === "true" && srvForm.elements.baseDomain.required === true && srvForm.elements.baseDomain.value === "e.com",
);
T.closeServerModal();
await sleep(450);
T.openServerModal({ id: "s2", name: "关HTTP", serverAddr: "y.com", serverPort: 7000, authToken: "", httpEnabled: false, baseDomain: "old.com", scheme: "https", publicPort: 8443 });
check(
  "R6 编辑(关闭)：折叠+required/disabled 关闭+值保留",
  httpFields.hidden === true && httpBox.checked === false && srvForm.elements.baseDomain.required === false && srvForm.elements.baseDomain.disabled === true && srvForm.elements.baseDomain.value === "old.com",
);
T.closeServerModal();
await sleep(450);

// R7 编辑开启态提交接口失败：toast 原因、模态保持、值保留
const beforeRejServer = rejections;
appStub.UpdateServer = (...a) => {
  appStub.calls.push({ name: "UpdateServer", args: a });
  return Promise.reject(new Error("配置名称重复"));
};
T.openServerModal({ id: "s1", name: "开HTTP", serverAddr: "x.com", serverPort: 7000, authToken: "", httpEnabled: true, baseDomain: "e.com", scheme: "https", publicPort: 8443 });
srvForm.elements.name.value = "重复名";
srvForm.dispatchEvent({ type: "submit", bubbles: true });
await sleep(30);
check(
  "R7 接口失败：错误 toast+模态保持+输入值保留",
  lastToast().includes("配置名称重复") &&
    document.getElementById("server-modal").hidden === false &&
    srvForm.elements.baseDomain.value === "e.com" &&
    rejections === beforeRejServer,
);
T.closeServerModal();
await sleep(450);
delete appStub.results.UpdateServer;
appStub.UpdateServer = (...a) => Promise.resolve(appStub.record("UpdateServer", a) ?? currentSnapshot);

// ---------- 场景 S6：自动启停模式下无手动启停 UI/API + 退出确认按 activeCount ----------
check(
  "无手动启停 UI：配置卡/底部均无启停开关与按钮",
  document.getElementById("btn-toggle") === null &&
    document.querySelector("#server-list .switch") === null,
);
check("四个启停 API 在前端已无引用", !["StartServer", "StopServer", "StartTunnel", "StopTunnel"].some((f) => mainSrc.includes(`api.AppService.${f}`)));

// 退出确认：activeCount>0 → 弹确认，不直接退出
currentSnapshot = makeSnapshot({ state: "running", runningCount: 1, activeCount: 1 });
await T.applySnapshot(currentSnapshot);
const quitCallsBefore = appStub.calls.filter((c) => c.name === "QuitApp").length;
document.getElementById("btn-quit").click();
await sleep(30);
console.error("[diag] after click, body children:", document.body.children.map((c) => c.className || c.tagName).join(","));
const confirmPanel = document.querySelector(".confirm");
check("activeCount>0 退出弹确认（不直接退出）", Boolean(confirmPanel) && appStub.calls.filter((c) => c.name === "QuitApp").length === quitCallsBefore);
// 取消 → 确认框关闭、仍不退出
confirmPanel.querySelector("button[data-r=\"0\"]")?.click();
await sleep(400);
check("取消后不退出", appStub.calls.filter((c) => c.name === "QuitApp").length === quitCallsBefore && document.querySelector(".confirm") === null);
// activeCount=0 → 直接退出（QuitApp 调用）
currentSnapshot = makeSnapshot({ state: "stopped", runningCount: 0, activeCount: 0 });
await T.applySnapshot(currentSnapshot);
document.getElementById("btn-quit").click();
await sleep(30);
check("activeCount=0 退出直接执行", appStub.calls.filter((c) => c.name === "QuitApp").length === quitCallsBefore + 1);

// ---------- 场景 S7：动画保护窗内轻量更新 + 窗末补绘（不回退旧响应） ----------
T.state.listRenderHoldUntil = Date.now() + 400;
const snap2 = makeSnapshot({
  servers: [
    { id: "srvA", name: "主服务器", serverAddr: "a.example.com", serverPort: 7000, authToken: "", httpEnabled: true, baseDomain: "a.com", scheme: "https", publicPort: 8443 },
    { id: "srvB", name: "备服务器", serverAddr: "b.example.com", serverPort: 7000, authToken: "", httpEnabled: false, baseDomain: "", scheme: "https", publicPort: 0 },
    { id: "srvC", name: "新节点", serverAddr: "c.example.com", serverPort: 7000, authToken: "", httpEnabled: true, baseDomain: "c.io", scheme: "https", publicPort: 0 },
  ],
  serverStatuses: [
    { serverId: "srvA", state: "running", message: "", startedAt: "" },
    { serverId: "srvB", state: "starting", message: "", startedAt: "" },
    { serverId: "srvC", state: "stopped", message: "", startedAt: "" },
  ],
});
// 推第一帧（新增 srvC）→ 保护窗内卡片数不变，但状态已更新
wailsListeners["app:snapshot"][0]({ data: snap2 });
check("保护窗内：卡片数不变（动画保护）", document.querySelectorAll("#server-list .server-item").length === 2);
check("保护窗内：serverStatus 已更新为最新", T.state.serverStatus.srvC?.serverId === "srvC" && T.state.serverStatus.srvB?.state === "starting");
// 窗内再推更新的终态帧（srvC 更名）——补绘必须呈现最新而非旧帧
const snap3 = JSON.parse(JSON.stringify(snap2));
snap3.config.servers[2].name = "新节点-终态";
wailsListeners["app:snapshot"][0]({ data: snap3 });
await sleep(600); // 越过保护窗与补绘 timer
const names = [...document.querySelectorAll("#server-list .service-name-text")].map((e) => e.textContent);
check("保护窗结束：补绘为最新帧（不回退旧响应）", names.includes("新节点-终态") && document.querySelectorAll("#server-list .server-item").length === 3, JSON.stringify(names));

// ---------- TCP/UDP/HTTP 卡片形态（渲染产物） ----------
// 专用快照：三类型各一，验证标签/图标/地址形态与点击复制
const cardSnap = makeSnapshot({
  services: [
    { id: "svc1", name: "博客", type: "http", subdomain: "blog", localHost: "127.0.0.1", localPort: 8080, remotePort: 0, enabled: true, serverId: "srvA", icon: "🌐" },
    { id: "svc2", name: "SSH", type: "tcp", subdomain: "", localHost: "127.0.0.1", localPort: 22, remotePort: 36000, enabled: true, serverId: "srvB", icon: "🔌" },
    { id: "svc3", name: "DNS", type: "udp", subdomain: "", localHost: "127.0.0.1", localPort: 53, remotePort: 36053, enabled: true, serverId: "srvB", icon: "" }, // 未设图标：类型兑底
  ],
});
await T.applySnapshot(cardSnap);
const svcCards = [...document.querySelectorAll("#service-list .service-item")];
const cardOf = (id) => svcCards.find((c) => c.dataset.id === id);
check("HTTP 地址为链接（a[data-url]）", cardOf("svc1")?.querySelector("a[data-url]")?.getAttribute("data-url") === "https://blog.a.com:8443");
check("TCP 卡：tag-tcp + TCP 文案 + 地址恢复点击复制形态（span.addr-copy[data-copy]）", cardOf("svc2")?.querySelector(".tag-tcp")?.textContent === "TCP" && cardOf("svc2")?.querySelector(".addr-copy[data-copy]")?.getAttribute("data-copy") === "b.example.com:36000");
check("UDP 卡：tag-udp + UDP 文案 + host:port 复制形态", cardOf("svc3")?.querySelector(".tag-udp")?.textContent === "UDP" && cardOf("svc3")?.querySelector(".addr-copy[data-copy]")?.getAttribute("data-copy") === "b.example.com:36053");
check("UDP 未设图标：类型兑底 📡（TCP 兑底 🔌 不变）", cardOf("svc3")?.querySelector(".service-icon")?.textContent === "📡" && cardOf("svc2")?.querySelector(".service-icon")?.textContent === "🔌");
// 地址点击 → Wails 剪贴板（TCP/UDP 同路径）
const udpSpan = cardOf("svc3")?.querySelector(".addr-copy[data-copy]");
udpSpan.dispatchEvent({ type: "click", bubbles: true });
await sleep(10);
check("UDP 地址点击复制走 Wails 剪贴板", stubWindow.wails.Clipboard.calls.at(-1) === "b.example.com:36053", JSON.stringify(stubWindow.wails.Clipboard.calls));

// 编辑 UDP 服务：协议选择器回显 UDP 且可切换
const udpSvc = cardSnap.config.services.find((s) => s.id === "svc3");
T.openServiceModal("编辑 UDP 服务", udpSvc);
check("编辑 UDP：协议回显 UDP 且字段组可见", protoSel.value === "udp" && document.getElementById("service-form").elements.type.value === "udp" && document.querySelector('[data-fields="port"]').hidden === false, protoSel.value);
protoSel.value = "tcp";
protoSel.dispatchEvent({ type: "change", bubbles: true });
check("编辑 UDP 切 TCP：类型随动（提交按新协议）", document.getElementById("service-form").elements.type.value === "tcp");
T.closeServiceModal();

// ---------- 场景 S8：底部隧道计数（registered/enabled）+ proxy 事件刷新 + 移走清旧态 ----------
currentSnapshot = makeSnapshot();
await T.applySnapshot(currentSnapshot);
// proxy 事件：svc1(srvA running) registered → 1/2；svc2(srvB stopped) 不计
wailsListeners["app:proxies"][0]({ data: [{ serviceId: "svc1", state: "registered" }] });
check("proxy 事件刷新：registered 计入、未连接服务器不计（1）", document.getElementById("service-count").textContent === "1", document.getElementById("service-count").textContent);
// 禁用 svc2：分母减为 1
const disSnap = JSON.parse(JSON.stringify(currentSnapshot));
disSnap.config.services[1].enabled = false;
currentSnapshot = disSnap;
await T.applySnapshot(disSnap);
check("禁用隧道不影响当前映射数（1）", document.getElementById("service-count").textContent === "1", document.getElementById("service-count").textContent);
// 移走 svc2 → srvA：applyConfigOnly 清旧 proxy registered（无旧状态误计）
T.state.proxyStatus["svc2"] = { serviceId: "svc2", state: "registered" };
const movedSnap = JSON.parse(JSON.stringify(currentSnapshot));
movedSnap.config.services[1].serverId = "srvA";
movedSnap.config.services[1].enabled = true;
currentSnapshot = movedSnap;
await T.applyConfigOnly(movedSnap);
check("移走清旧 proxy 态：不误计（仍 1）", document.getElementById("service-count").textContent === "1", document.getElementById("service-count").textContent);
// 关联数量：禁用计入关联、启用单独计（零正常）
const foots = [...document.querySelectorAll("#server-list .server-foot span")].map((e) => e.textContent);
check(
  "关联数量：禁用计入关联、已启用单独计",
  foots[0] === "关联 2 个隧道 · 已启用 2 个" && foots[1] === "关联 0 个隧道 · 已启用 0 个",
  JSON.stringify(foots),
);

// ---------- 场景 S8b：移动时序——事件先到清旧态，RPC 后到不误清同 server 有效状态 ----------
// 初始：svc1@srvA、svc2@srvB，两台均 running（移动注册后才可计数）
currentSnapshot = makeSnapshot({ serverStatuses: [
  { serverId: "srvA", state: "running", message: "", startedAt: "" },
  { serverId: "srvB", state: "running", message: "", startedAt: "" },
] });
await T.applySnapshot(currentSnapshot);
wailsListeners["app:proxies"][0]({ data: [{ serviceId: "svc1", state: "registered" }] });
check("S8b 初始：svc1 注册计入（1）", document.getElementById("service-count").textContent === "1");
// 移动 svc1 → srvB：**snapshot 事件先到**（config 先落 B）→ 共用清理应清掉 A 时代的 registered
const movedFirst = JSON.parse(JSON.stringify(currentSnapshot));
movedFirst.config.services[0].serverId = "srvB";
wailsListeners["app:snapshot"][0]({ data: movedFirst });
check(
  "snapshot 先到：A 时代 registered 已清（不把 A 的注册算给 B）",
  T.state.proxyStatus["svc1"] === undefined && document.getElementById("service-count").textContent === "0",
  document.getElementById("service-count").textContent,
);
// RPC 后到：applyConfigOnly(同 configB)——diff 为空不清，但也没有可误清的状态
await T.applyConfigOnly(movedFirst);
check("RPC 后到：不复活 A 时代注册（仍 0）", document.getElementById("service-count").textContent === "0");
// 同 server 普通 snapshot：另一服务（新增）在 A 的注册状态不受普通快照影响
wailsListeners["app:proxies"][0]({ data: [{ serviceId: "svc2", state: "registered" }] });
const sameServerSnap = JSON.parse(JSON.stringify(T.state.snapshot));
sameServerSnap.config.services[1].name = "SSH-改名"; // 同 server 普通快照（无归属变更）
wailsListeners["app:snapshot"][0]({ data: sameServerSnap });
check(
  "同 server 快照不清有效注册状态（svc2@srvB 计入：1/2）",
  T.state.proxyStatus["svc2"]?.state === "registered" && document.getElementById("service-count").textContent === "1",
  document.getElementById("service-count").textContent,
);
// proxy 事件后到：B 上注册成功的事件直接覆盖为 registered（不覆盖新的已注册状态的对称面：事件本身就是新状态源）
// app:proxies 为全量替换语义：推送两台当前注册状态
wailsListeners["app:proxies"][0]({ data: [{ serviceId: "svc1", state: "registered" }, { serviceId: "svc2", state: "registered" }] });
check("proxy 事件：B 上注册成功计入（2）", document.getElementById("service-count").textContent === "2", document.getElementById("service-count").textContent);

// ---------- 场景 S9：连接测试按钮（busy/成功/失败/异常/防双击/重绘不丢） ----------
currentSnapshot = makeSnapshot();
await T.applySnapshot(currentSnapshot);
const testBtnOf = () => document.querySelector('#server-list .server-item .server-ops .btn[data-act="srv-test"]');
const testCalls = () => appStub.calls.filter((c) => c.name === "TestServerConnection").length;
let releaseProbe;
const probeGate = new Promise((r) => (releaseProbe = r));
appStub.results.TestServerConnection = Object.assign(
  probeGate.then(() => ({ success: true, message: "", latencyMs: 42 })),
  { __pending: probeGate },
);
testBtnOf().click();
await sleep(20);
let busyBtn = testBtnOf();
check("测试 busy：按钮「测试中…」并 disabled", busyBtn.textContent === "测试中…" && busyBtn.disabled === true, `${busyBtn.textContent}/${busyBtn.disabled}`);
// busy 中快照重绘：busy 不丢（状态外置）
await T.applySnapshot(JSON.parse(JSON.stringify(T.state.snapshot)));
busyBtn = testBtnOf();
check("busy 中重绘不丢 busy", busyBtn.textContent === "测试中…" && busyBtn.disabled === true);
busyBtn.click(); // disabled：桩不派发 → 防双击
await sleep(10);
check("busy 中防重复（TestServerConnection 仅 1 次调用）", testCalls() === 1, String(testCalls()));
releaseProbe();
await sleep(30);
check("测试成功：toast 为 FRPS 登录成功 · 时延", lastToast().startsWith("FRPS 登录成功 · 42 ms"), lastToast());
check("测试完成后按钮恢复可点", testBtnOf().textContent === "测试" && !testBtnOf().disabled);
const testTitle = (testBtnOf().getAttribute("title") || "");
check("测试按钮 title 明确控制连接/鉴权且不验证 HTTP 入口", testTitle.includes("控制连接与鉴权") && testTitle.includes("不验证 HTTP 入口"), testTitle);
// 失败（业务 fail：后端可达但连接不通）
// core 附带 message：短附进 toast（超长截断避免长 toast）
appStub.results.TestServerConnection = { success: true, message: "登录正常，鉴权通过", latencyMs: 42 };
testBtnOf().click();
await sleep(30);
check("测试成功：message 短附进 toast", lastToast().startsWith("FRPS 登录成功 · 42 ms · 登录正常，鉴权通过"), lastToast());
appStub.results.TestServerConnection = { success: false, message: "握手超时", latencyMs: 1200 };
testBtnOf().click();
await sleep(30);
check("测试失败：toast 呈现后端 message", lastToast().includes("连接失败") && lastToast().includes("握手超时"), lastToast());
check("失败后按钮恢复", testBtnOf().textContent === "测试" && !testBtnOf().disabled);
// RPC 异常
appStub.results.TestServerConnection = new Error("RPC 断开");
testBtnOf().click();
await sleep(30);
check("RPC 异常：toast 失败原因且按钮恢复", lastToast().includes("测试连接失败") && lastToast().includes("RPC 断开") && testBtnOf().textContent === "测试" && !testBtnOf().disabled);
// 诊断通道（FrontendLog）固定英文：本地化（中文）错误详情不回流日志通道，
// toast 仍按当前语言展示原始错误详情
delete appStub.results.TestServerConnection;
const flExpect = appStub.calls.filter((c) => c.name === "FrontendLog").at(-1)?.args?.[1] ?? "";
check(
  "诊断日志固定英文：中文错误详情被过滤（toast 仍双语）",
  flExpect.includes("Test connection failed") && !/[\p{Script=Han}]/u.test(flExpect) && lastToast().includes("RPC 断开"),
  flExpect,
);
// enDetail 直接单测：英文技术错误原样保留，本地化错误用英文占位
check(
  "enDetail：英文错误原样保留/本地化错误不进入英文通道",
  T.enDetail(new Error("transport closed")) === "transport closed" &&
    T.enDetail(new Error("服务器名称不能为空")).startsWith("localized UI error") &&
    T.enDetail(undefined) === "",
  `${T.enDetail(new Error("transport closed"))} | ${T.enDetail(new Error("服务器名称不能为空"))}`,
);

// ---------- 场景 S10：双语切换、回退、写入生命周期与动态渲染 ----------
// 预置 zh-CN（同 harness 初始化 + 快照 language 字段）；覆盖：
// 静态/动态全量重绘、后端 resolved 对齐、串行/去重/迟到丢弃、失败回滚、
// 启动快照对齐（过期/缺失字段）、模态动态文案（协议/空状态/（当前）后缀）。
const langSnap = makeSnapshot({
  services: [{ id: "svc1", name: "博客", type: "http", subdomain: "blog", localHost: "127.0.0.1", localPort: 8080, remotePort: 0, enabled: true, serverId: "srvA", icon: "🌐" }],
  serverStatuses: [
    { serverId: "srvA", state: "running", message: "", startedAt: "" },
    { serverId: "srvB", state: "stopped", message: "", startedAt: "" },
  ],
});
await T.applySnapshot(langSnap);
// 注册事件：让卡片出现「已注册」徽章（徽章文案双语断言需要可预期的 proxy 状态）
wailsListeners["app:proxies"][0]({ data: [{ serviceId: "svc1", state: "registered" }] });
const bizCount = () => appStub.calls.filter((c) => ["SetServiceEnabled", "CreateService", "UpdateService", "DeleteService", "CreateServer", "UpdateServer", "DeleteServer"].includes(c.name)).length;
check("S10 初始：偏好 zh-CN 且 html lang=zh-CN", storage.get("frpdesk.language") === "zh-CN" && document.documentElement.lang === "zh-CN", String(document.documentElement.lang));
const tabTunnels = document.querySelector('.tab[data-tab="tunnels"]');
const langBtn = () => document.getElementById("btn-lang");
check("S10 初始：静态 Tab 为中文且语言按钮为紧凑「中」态", tabTunnels.textContent === "隧道" && langBtn().textContent === "中" && (langBtn().title || "").includes("语言：中文") && (langBtn().title || "").includes("切换到 English"), langBtn().title);

// 记录切换前业务调用数：切语言绝不能触发隧道/服务 RPC
const bizBefore = bizCount();

await T.setLanguagePref("en");
check("S10 切英文：html lang 与 localStorage 持久化", document.documentElement.lang === "en" && storage.get("frpdesk.language") === "en");
check("S10 切英文：语言按钮为「EN」态且 tip/aria 指向切回中文", langBtn().textContent === "EN" && (langBtn().title || "").includes("Language: English") && (langBtn().getAttribute("aria-label") || "").includes("switch to 中文"), `${langBtn().textContent} / ${langBtn().title} / ${langBtn().getAttribute("aria-label")}`);
check("S10 切英文：静态文案（Tab/按钮/空态属性）", tabTunnels.textContent === "Tunnels" && document.getElementById("btn-add-http").textContent.includes("Add HTTP") && document.getElementById("service-empty").textContent.includes("No tunnels yet"));
check("S10 切英文：动态状态栏", document.getElementById("status-label").textContent === "Connected" && document.getElementById("service-count").title === "Currently mapped tunnels", `${document.getElementById("status-label").textContent} / ${document.getElementById("service-count").title}`);
check("S10 切英文：动态卡片操作/徽章", document.querySelector("#service-list .service-item .svc-op--r1")?.getAttribute("title") === "Edit" && document.querySelector("#service-list .service-item .tag-ok")?.textContent === "Registered", `${document.querySelector("#service-list .service-item .svc-op--r1")?.getAttribute("title")} / ${document.querySelector("#service-list .service-item .tag")?.textContent}`);
check("S10 切英文：服务器卡操作文案", document.querySelector('#server-list .server-item .btn[data-act="srv-test"]')?.textContent === "Test" && document.querySelector("#server-list .server-item")?.textContent.includes("tunnel(s) linked"), `${document.querySelector('#server-list .server-item .btn[data-act="srv-test"]')?.textContent} / ${document.querySelector("#server-list .server-item")?.textContent}`);
check("S10 切英文：后端同步 SetLanguage(en)", appStub.calls.filter((c) => c.name === "SetLanguage").at(-1)?.args?.[0] === "en");
check("S10 切英文：不触发任何隧道/配置业务调用", bizCount() === bizBefore);

// 语言按钮：中/EN 两态显式切换（zh-CN ⇄ en），不再有三态循环、不会回到 system
langBtn().click();
await sleep(20);
check(
  "S10 按钮两态：en → zh-CN（持久化显式偏好）",
  storage.get("frpdesk.language") === "zh-CN" && document.documentElement.lang === "zh-CN" && tabTunnels.textContent === "隧道" && langBtn().textContent === "中",
  `store=${storage.get("frpdesk.language")} lang=${document.documentElement.lang} btn=${langBtn().textContent}`,
);
langBtn().click();
await sleep(20);
check(
  "S10 按钮两态：zh-CN → en（不回到 system）",
  storage.get("frpdesk.language") === "en" && document.documentElement.lang === "en" && tabTunnels.textContent === "Tunnels" && langBtn().textContent === "EN",
  `store=${storage.get("frpdesk.language")} lang=${document.documentElement.lang} btn=${langBtn().textContent}`,
);

// ---------- S10a：system 采用后端 resolved（不自行判定） ----------
// system 偏好保留：首启/无偏好仍由后端解析系统语言；按钮在 system 下显示
// 当前生效语言，点击切到另一显式语言并持久化，不回到 system
appStub.langSystemResolved = "en"; // 后端判定 en（本机 Node 前端解析可能不同）
await T.setLanguagePref("system");
check(
  "S10a system 采用后端 resolved=en（前后端一致）",
  storage.get("frpdesk.language") === "system" && document.documentElement.lang === "en" && tabTunnels.textContent === "Tunnels" && langBtn().textContent === "EN",
  `store=${storage.get("frpdesk.language")} lang=${document.documentElement.lang} btn=${langBtn().textContent}`,
);
langBtn().click();
await sleep(20);
check(
  "S10a system 下点击按钮：切到显式 zh-CN（不回 system）",
  storage.get("frpdesk.language") === "zh-CN" && document.documentElement.lang === "zh-CN" && langBtn().textContent === "中",
  `store=${storage.get("frpdesk.language")} lang=${document.documentElement.lang} btn=${langBtn().textContent}`,
);
appStub.langSystemResolved = "zh-CN";
await T.setLanguagePref("system");
check("S10a system 后端 resolved=zh-CN 时随其后端", storage.get("frpdesk.language") === "system" && document.documentElement.lang === "zh-CN" && tabTunnels.textContent === "隧道");
appStub.langSystemResolved = "en";

// ---------- S10b：写入串行/去重与迟到结果丢弃 ----------
// 同 tick 连点：中间值去重跳过，只执行最后一次
const beforeDedupe = appStub.calls.filter((c) => c.name === "SetLanguage").length;
await Promise.all([T.setLanguagePref("en"), T.setLanguagePref("zh-CN"), T.setLanguagePref("en")]);
check(
  "S10b 同 tick 连点只发最后一次写入（去重）",
  appStub.calls.filter((c) => c.name === "SetLanguage").length === beforeDedupe + 1 && appStub.calls.filter((c) => c.name === "SetLanguage").at(-1)?.args?.[0] === "en",
  `calls=${appStub.calls.filter((c) => c.name === "SetLanguage").length - beforeDedupe}`,
);
// 在途响应迟到：先点 en（RPC 挂起）再点 zh；en 返回时必须丢弃，不覆盖中文
let releaseLang;
appStub.results.SetLanguage = { __pending: new Promise((r) => (releaseLang = r)) };
const pEn = T.setLanguagePref("en");
await sleep(10); // 让 en 的 RPC 确实发出
const pZh = T.setLanguagePref("zh-CN");
releaseLang();
await Promise.all([pEn, pZh]);
check("S10b 迟到 en 结果不覆盖后续中文选择", document.documentElement.lang === "zh-CN" && storage.get("frpdesk.language") === "zh-CN" && tabTunnels.textContent === "隧道", `lang=${document.documentElement.lang} store=${storage.get("frpdesk.language")}`);
check("S10b 后端最终收到最新一次写入（zh-CN）", appStub.calls.filter((c) => c.name === "SetLanguage").at(-1)?.args?.[0] === "zh-CN");
delete appStub.results.SetLanguage;

// ---------- S10c：持久化失败 → 可见错误 + 回滚到最近确认一致 ----------
await T.setLanguagePref("zh-CN"); // 确认 zh-CN 为基准
const bizBeforeFail = bizCount();
appStub.results.SetLanguage = "reject";
await T.setLanguagePref("en");
check("S10c 失败可见：toast 提示语言未保存", lastToast().includes("语言偏好未保存"), lastToast());
check(
  "S10c 失败回滚：UI 与 localStorage 保持最近确认的 zh-CN",
  document.documentElement.lang === "zh-CN" && storage.get("frpdesk.language") === "zh-CN" && tabTunnels.textContent === "隧道",
  `lang=${document.documentElement.lang} store=${storage.get("frpdesk.language")}`,
);
check("S10c 失败不触发业务 RPC", bizCount() === bizBeforeFail);
delete appStub.results.SetLanguage;

// ---------- S10d：启动快照对齐（后端权威 / 过期丢弃 / 缺失字段按 system） ----------
const appliedEn = T.reconcileLanguageFromSnapshot(makeSnapshot({ language: "en", resolvedLanguage: "en" }), T.langSync.seq);
check("S10d 快照对齐：采用后端偏好与 resolved", appliedEn === true && document.documentElement.lang === "en" && storage.get("frpdesk.language") === "en");
// 拉取期间用户已改语言：旧快照不得覆盖
const staleSeq = T.langSync.seq;
const staleWrite = T.setLanguagePref("zh-CN");
const appliedStale = T.reconcileLanguageFromSnapshot(makeSnapshot({ language: "en", resolvedLanguage: "en" }), staleSeq);
await staleWrite;
check("S10d 过期快照不覆盖刚切的中文", appliedStale === false && document.documentElement.lang === "zh-CN" && storage.get("frpdesk.language") === "zh-CN");
// 快照缺 language 字段：按 system 权威对齐（不保留本地显式选择）
await T.setLanguagePref("en");
const appliedMissing = T.reconcileLanguageFromSnapshot({ config: { servers: [], services: [] } }, T.langSync.seq);
check(
  "S10d 缺失 language 字段按 system 权威对齐",
  appliedMissing === true && storage.get("frpdesk.language") === "system" && ["zh-CN", "en"].includes(document.documentElement.lang),
  `store=${storage.get("frpdesk.language")} lang=${document.documentElement.lang}`,
);
await T.setLanguagePref("zh-CN");

// ---------- S10e：模态动态文案（插值/协议切换/空状态/（当前）后缀） ----------
T.openServiceModal("ignored", null, "http");
const langForm = document.getElementById("service-form");
const langSel = document.getElementById("svc-server-select");
langForm.elements.name.value = "keep-me";
langForm.elements.subdomain.value = "";
langForm.elements.subdomain.dispatchEvent({ type: "input", bubbles: true });
await T.setLanguagePref("en");
const enHint = document.getElementById("svc-move-hint").textContent;
check("S10e 英文插值：自动生成子域名提示", enHint.includes("subdomain") && enHint.includes("generated on save") && !enHint.includes("{0}"), enHint);
check("S10e 英文插值：子域名占位符英文", enHint.includes("<subdomain>"), enHint);
T.setLanguagePref("zh-CN");
await sleep(20);
check("S10e 模态切语言：标题随语言且输入不丢", document.getElementById("modal-title").textContent === "新增 HTTP 服务" && langForm.elements.name.value === "keep-me" && document.getElementById("service-modal").hidden === false);
T.closeServiceModal();
await sleep(450);

// 编辑 TCP→UDP 后切语言：标题跟随实际表单协议，hint/输入/目标选择全部保留
const tcpSnap = makeSnapshot({
  services: [{ id: "svcT", name: "端口", type: "tcp", subdomain: "", localHost: "127.0.0.1", localPort: 22, remotePort: 36022, enabled: true, serverId: "srvB", icon: "" }],
});
await T.applySnapshot(tcpSnap);
T.openServiceModal("ignored", tcpSnap.config.services[0]);
const langProtoSel = document.getElementById("svc-proto-select");
langProtoSel.value = "udp";
langProtoSel.dispatchEvent({ type: "change", bubbles: true });
await T.setLanguagePref("en");
check(
  "S10e 编辑 TCP→UDP 后切语言：标题随实际协议（Edit UDP）",
  document.getElementById("modal-title").textContent === "Edit UDP tunnel" && langForm.elements.type.value === "udp" && langForm.elements.remotePort.value === "36022" && langSel.value === "srvB",
  `${document.getElementById("modal-title").textContent} / type=${langForm.elements.type.value} / port=${langForm.elements.remotePort.value} / srv=${langSel.value}`,
);
check("S10e 协议切换后 hint 为英文且保留地址预览", document.getElementById("svc-move-hint").textContent.includes("Access address"), document.getElementById("svc-move-hint").textContent);
await T.setLanguagePref("zh-CN");
check("S10e 切回中文：编辑 UDP 标题且选择不丢", document.getElementById("modal-title").textContent === "编辑 UDP 服务" && langSel.value === "srvB" && langForm.elements.remotePort.value === "36022");
T.closeServiceModal();
await sleep(450);

// 无合格目标：select 空状态文案随语言刷新
const emptySnap = makeSnapshot({ servers: [], services: [] });
await T.applySnapshot(emptySnap);
T.openServiceModal("ignored", null, "http");
check("S10e 空状态初始为中文", langSel.innerHTML.includes("暂无支持 HTTP") && langSel.disabled === true, langSel.innerHTML);
await T.setLanguagePref("en");
check("S10e 空状态随语言刷新为英文", langSel.innerHTML.includes("No HTTP-capable Frps configuration") && langSel.disabled === true, langSel.innerHTML);
T.closeServiceModal();
await sleep(450);

// 编辑不可用合格目标（HTTP 服务指向未开启 HTTP 的服务器）：「（当前）」后缀随语言
await T.setLanguagePref("zh-CN");
const curSnap = makeSnapshot({
  services: [{ id: "svcCur", name: "博客", type: "http", subdomain: "blog", localHost: "127.0.0.1", localPort: 8080, remotePort: 0, enabled: true, serverId: "srvB", icon: "🌐" }],
});
await T.applySnapshot(curSnap);
T.openServiceModal("ignored", curSnap.config.services[0]);
check("S10e （当前）后缀初始为中文且归属保留", langSel.innerHTML.includes("（当前）") && langSel.value === "srvB", `${langSel.innerHTML} / ${langSel.value}`);
await T.setLanguagePref("en");
check("S10e （当前）后缀随语言刷为英文且选择不丢", langSel.innerHTML.includes("(current)") && !langSel.innerHTML.includes("（当前）") && langSel.value === "srvB", `${langSel.innerHTML} / ${langSel.value}`);
T.closeServiceModal();
await sleep(450);
await T.setLanguagePref("zh-CN");
check("S10 收尾：恢复中文", document.documentElement.lang === "zh-CN" && document.getElementById("status-label").textContent === "已连接");
check("S10 全程不触发任何隧道/配置业务调用", bizCount() === bizBefore, `biz=${bizCount()} before=${bizBefore}`);

// ---------- 场景 S11：日志面板固定英文与公共归属 System ----------
// 日志内容不随界面语言翻译：中英文界面下同一 LogEntry 渲染一致；
// 无归属条目固定标 System（非「系统」双语）。
const logEntry = { id: 9001, timestamp: "2025-01-01 00:00:00", level: "info", message: "[frpdesk] Stopping frpc", serverId: "", serverName: "" };
wailsListeners["app:logs"][0]({ data: [logEntry] });
const logView = () => document.getElementById("log-view");
const logRendered = () => logView().innerHTML || "";
check(
  "S11 日志条目渲染且公共归属为 System",
  logRendered().includes("[System]") && logRendered().includes("[frpdesk] Stopping frpc") && !logRendered().includes("系统"),
  logRendered().slice(-200),
);
await T.setLanguagePref("en");
const logEn = logRendered();
await T.setLanguagePref("zh-CN");
check(
  "S11 切换语言后日志内容不变（不随界面语言翻译）",
  logRendered() === logEn && logRendered().includes("[System]") && logRendered().includes("[frpdesk] Stopping frpc"),
  logRendered().slice(-200),
);

// ---------- 服务器卡片专用 CSS ----------
check("服务器卡无 hover 隐藏/translate 残留", !css.includes(".server-item .svc-op"));
check("服务器卡底行落底（margin-top:auto）", /\.server-ops \{[^}]*margin-top: auto;/.test(css));

// ---------- 汇总 ----------
console.log(`\n===== 行为验证汇总：${pass}/${pass + fail} 通过 =====`);
process.exit(fail ? 1 : 0);

// ---- helpers ----
function srvHttp(base) {
  return { ...base, httpEnabled: true, baseDomain: "b.example.com", scheme: "https", publicPort: 8443 };
}
function lastToast() {
  const box = document.getElementById("toasts");
  const items = [...box.children];
  return items.length ? items.at(-1).textContent : "";
}
