// frpdesk 前端逻辑：纯原生 JS。bindings 调后端，Events 订阅推送。
import * as api from "./bindings/frpdesk/index.js";
import { t, tEn, applyPref, currentLang, normalizePref, normalizeLang, readStoredPref, resolveLang } from "./i18n.js";

const $ = (id) => document.getElementById(id);

const state = {
  snapshot: null, // { config, status }
  logs: [], // LogEntry[]（按 id 去重，保留末尾 500 条）
  logIds: new Set(), // 已有日志 id 集合：事件增量与全量拉取合并时去重
  proxyStatus: {}, // serviceId → { serviceId, state, friendly, rawErr }
  stats: { data: null, error: null, timer: null, inFlight: false, errLogged: false, chart: null }, // 统计面板：chart 存最近一次曲线槽位上下文供 hover 吸附
  serverStatus: {}, // serverId → { serverId, state, message, startedAt }
  serverProbe: {}, // serverId → true：连接测试进行中（重绘不丢 busy）
  serverEditingId: null, // 非 null 表示 Frps 配置模态处于编辑模式
  serverReturnFocus: null, // Frps 配置模态关闭后回焦目标
  editingOrigServerId: "", // 打开服务模态时的原归属，用于判断是否发生目标切换
  editingId: null, // 非 null 表示模态处于编辑模式
  autoScroll: true, // 日志自动滚动（用户上滚暂停）
  update: { info: null, seq: 0 }, // 更新状态（seq 用于丢弃迟到 fetch 的渲染，事件为权威）
  updateChecking: false, // 手动检查更新忙态（防重复点击）
  updateConfirming: false, // 确认弹窗期间防双击多弹窗
  updateApplying: false, // InstallUpdate RPC 挂起期间防重复触发（同步等待下载+重启，持续到真实完成）
  modalCtx: null, // 服务/配置模态打开上下文（语言切换时重算动态标题）
  dragSort: null, // 卡片拖拽排序进行中上下文（非 null 时事件推送不重建列表，拖拽结束后统一补绘）
  listRenderHoldUntil: 0, // 开关动画保护窗截止时间（拖拽另有 dragSort 状态，二者独立）
};

// 状态展示文案 key（稳定语义 key，渲染时按当前语言取词；未知状态原样展示）
const STATE_LABELS = {
  stopped: "state.stopped",
  starting: "state.starting",
  stopping: "state.stopping",
  running: "state.running",
  failed: "state.failed",
};

// stateLabel：状态 key → 当前语言文案（未知状态用原始键）
function stateLabel(st) {
  return STATE_LABELS[st] ? t(STATE_LABELS[st]) : st;
}

// lucide 内联图标（ISC；Feather 派生图标另受 MIT 许可，详见 THIRD_PARTY_NOTICES.md / licenses/）：16px / stroke 1.75 / currentColor / fill none
const ICONS = {
  pencil: '<svg class="icon" viewBox="0 0 24 24"><path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/><path d="m15 5 4 4"/></svg>',
  copy: '<svg class="icon" viewBox="0 0 24 24"><rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>',
  qrCode: '<svg class="icon" viewBox="0 0 24 24"><rect width="5" height="5" x="3" y="3" rx="1"/><rect width="5" height="5" x="16" y="3" rx="1"/><rect width="5" height="5" x="3" y="16" rx="1"/><path d="M21 16h-3a2 2 0 0 0-2 2v3"/><path d="M21 21v.01"/><path d="M12 7v3a2 2 0 0 1-2 2H7"/><path d="M3 12h.01"/><path d="M12 3h.01"/><path d="M12 16v.01"/><path d="M16 12h1"/><path d="M21 12v.01"/><path d="M12 21v-1"/></svg>',
  trash: '<svg class="icon" viewBox="0 0 24 24"><path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/><line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/></svg>',
  gripVertical: '<svg class="icon" viewBox="0 0 24 24"><circle cx="9" cy="12" r="1"/><circle cx="9" cy="5" r="1"/><circle cx="9" cy="19" r="1"/><circle cx="15" cy="12" r="1"/><circle cx="15" cy="5" r="1"/><circle cx="15" cy="19" r="1"/></svg>',
  checkCircle: '<svg class="icon" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/></svg>',
  alertCircle: '<svg class="icon" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>',
  info: '<svg class="icon" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>',
  sun: '<svg class="icon" viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/></svg>',
  moon: '<svg class="icon" viewBox="0 0 24 24"><path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/></svg>',
  monitor: '<svg class="icon" viewBox="0 0 24 24"><rect width="20" height="14" x="2" y="3" rx="2"/><line x1="8" x2="16" y1="21" y2="21"/><line x1="12" x2="12" y1="17" y2="21"/></svg>',
  gear: '<svg class="icon" viewBox="0 0 24 24"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>',
  refresh: '<svg class="icon" viewBox="0 0 24 24"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/></svg>',
  download: '<svg class="icon" viewBox="0 0 24 24"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" x2="12" y1="15" y2="3"/></svg>',
  loader: '<svg class="icon" viewBox="0 0 24 24"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>',
};

// ---------- 服务 emoji 图标 ----------

// 精选 emoji 池（偏服务场景：网络/存储/终端/工具/游戏/影音/安全/生活），弹窗选择器共用；
// 硬编码不分组，点选后原样存入 Service.Icon（后端限 8 rune）
const EMOJI_CHOICES = [
  "🌐", "📡", "🛰️", "🔗", "☁️", "📶", "🔌", "🕸️", "🏠", "🏢", "🏪", "🏭",
  "🗄️", "🗃️", "🗂️", "📁", "📂", "📦", "💾", "💽", "💿", "📀", "🧮", "🗓️",
  "💻", "🖥️", "⌨️", "🖱️", "🖲️", "📟", "⚙️", "🛠️", "🔧", "🔨", "🪛", "🔩",
  "🧰", "🧲", "🔬", "🔭", "🎮", "🕹️", "🎲", "♟️", "🎯", "🧩", "🎳", "🎪",
  "📷", "📸", "🎥", "📹", "🎬", "📺", "📻", "🎙️", "🎤", "🎧", "🎵", "🎶",
  "🎼", "🎸", "🎹", "🎺", "🎻", "🥁", "🔐", "🔑", "🔒", "🔓", "🛡️", "⚠️",
  "🔔", "📌", "📍", "🧭", "📝", "📄", "📊", "📈", "📉", "💰", "🏦", "💳",
  "🛒", "🛍️", "🎁", "🚀", "✈️", "🚂", "🚗", "🚲", "⛵", "🗺️", "🔥", "⭐",
  "✨", "⚡", "💡", "🌈", "❄️", "🌡️", "🤖", "👾", "👻", "💀", "👀", "🧠",
  "🦾", "🔮", "🧪", "🧬", "🔋", "💉", "🍔", "🍕", "☕", "🍺", "🌱", "🌸",
  "🐱", "🐶", "🐟", "🐦", "🦋", "🐢",
];

// icon 未设置时的类型兜底（卡片与弹窗触发按钮展示一致）
const ICON_FALLBACK = { http: "🌐", tcp: "🔌", udp: "📡" };

// svcIcon：服务展示图标 = 已设置 icon（trim）→ 类型兜底（未知类型兜底 http）
function svcIcon(svc) {
  const icon = String(svc?.icon ?? "").trim();
  return icon || ICON_FALLBACK[svc?.type] || ICON_FALLBACK.http;
}

// isPortType：端口转发类服务（TCP/UDP 共用远程端口与直连语义；HTTP 走域名入口）
function isPortType(t) {
  return t === "tcp" || t === "udp";
}

// svcTypeLabel：类型展示名（未知兜底 HTTP）
function svcTypeLabel(t) {
  return t === "tcp" ? "TCP" : t === "udp" ? "UDP" : "HTTP";
}

// ---------- Frps 配置（多服务器）辅助 ----------

// configServers：当前配置的全部 Frps（无快照时为空数组）
function configServers() {
  return state.snapshot?.config?.servers ?? [];
}

// serverById：按 id 查 Frps 配置，无则 null
function serverById(id) {
  return configServers().find((s) => s.id === id) ?? null;
}

// eligibleServers：按服务类型返回合格目标（HTTP 仅限 httpEnabled，TCP/UDP 全部）
function eligibleServers(type, servers) {
  return (servers || []).filter((s) => (type === "http" ? s.httpEnabled : true));
}

// serverById：按 id 查 Frps 配置
function serverStatus(id) {
  return state.serverStatus[id] ?? { serverId: id, state: "stopped" };
}

// svcServer：服务当前归属的 Frps 配置（无归属/悬空 id 返 null）
function svcServer(svc) {
  return serverById(svc?.serverId ?? "");
}

// svcPublicURL：按服务归属的目标生成公网地址（纯函数；无目标/缺字段返回空串，不产生错误地址）
// HTTP: {scheme}://{sub}.{baseDomain}{:publicPort}；TCP/UDP: {serverAddr}:{remotePort}
function svcPublicURL(svc, server) {
  if (!svc || !server) return "";
  if (isPortType(svc.type)) {
    const addr = String(server.serverAddr ?? "").trim();
    const host = addr.includes(":") && !addr.startsWith("[") ? `[${addr}]` : addr;
    return addr && Number(svc.remotePort) > 0 ? `${host}:${svc.remotePort}` : "";
  }
  if (!server.httpEnabled) return "";
  const base = String(server.baseDomain ?? "").trim();
  const sub = String(svc.subdomain ?? "").trim();
  if (!base || !sub) return ""; // 空 subdomain 由后端保存时自动生成，预览不拼非法 URL
  const scheme = server.scheme === "http" ? "http" : "https";
  const port = Number(server.publicPort) > 0 ? `:${server.publicPort}` : "";
  return `${scheme}://${sub}.${base}${port}`;
}

// serverConnAddr：Frps 连接地址 host:port
function serverConnAddr(srv) {
  const addr = String(srv?.serverAddr ?? "").trim();
  const host = addr.includes(":") && !addr.startsWith("[") ? `[${addr}]` : addr;
  return addr ? `${host}:${srv.serverPort ?? 0}` : "—";
}

// currentServerName：服务当前所属 Frps 配置名（无归属返空串；统计排行标注用）
function currentServerName(serviceID) {
  const svc = (state.snapshot?.config?.services ?? []).find((s) => s.id === serviceID);
  return svc ? String(svcServer(svc)?.name ?? "").trim() : "";
}

// pruneMovedProxyStatus：目标 Frps 变更（移走）的服务清旧 proxy 注册状态。
// 集中在 snapshot/config 应用共用路径调用：无论事件先到还是 RPC 先到，
// prev→new 的 diff 都会在变更落地那一刻清理一次，旧 server 的 registered 不会算给新 server；
// 同一 server 的普通快照 diff 无变化，有效状态不受影响。
function pruneMovedProxyStatus(prevServices, newServices) {
  const prev = new Map((prevServices ?? []).map((s) => [s.id, s.serverId]));
  for (const svc of newServices ?? []) {
    if (prev.has(svc.id) && prev.get(svc.id) !== svc.serverId) {
      delete state.proxyStatus[svc.id];
    }
  }
}

// ---------- 旧 localStorage key 兼容（只读回退，旧值不删） ----------
// 新 key 优先；缺失时只读回退旧 key（旧值不删、不写回）。注意：bundle ID 变更后
// WKWebView 的 localStorage 不保证与旧包共享（可能被视为不同域），因此这里只是
// 尽力而为的读取兜底，不做复杂迁移；读不到时表现为主题/上次选择回到默认，不影响功能。
const LEGACY_KEY_PREFIX = "frpcc.";
function readStored(key) {
  try {
    const v = localStorage.getItem(key);
    if (v !== null) return v;
    return localStorage.getItem(LEGACY_KEY_PREFIX + key.replace(/^frpdesk\./, ""));
  } catch {
    return null;
  }
}

// lastServerFor / setLastServer：新建服务时「记住上次选择」的每类型记忆（localStorage）
function lastServerFor(type) {
  return readStored(`frpdesk.lastServer.${type}`) ?? "";
}

function setLastServer(type, id) {
  try {
    localStorage.setItem(`frpdesk.lastServer.${type}`, id);
  } catch {}
}

// tunnelCounts：底部隧道维度计数（纯读取当前状态）。
// 分母 = 全部服务器上已启用的隧道；分子 = 已启用 且 所属 Frps 已连接 且 proxy 已注册。
// 以当前服务列表为准遍历：禁用/移走/删除的服务其旧 proxy 状态不参与计数（无旧状态误计）。
function tunnelCounts() {
  const services = state.snapshot?.config?.services ?? [];
  let enabled = 0;
  let registered = 0;
  for (const svc of services) {
    if (!svc.enabled) continue;
    enabled++;
    const srv = serverById(svc.serverId);
    if (!srv || serverStatus(srv.id).state !== "running") continue;
    if (state.proxyStatus[svc.id]?.state === "registered") registered++;
  }
  return { registered, enabled };
}

// randomEmoji：等概率取一个精选 emoji
function randomEmoji() {
  return EMOJI_CHOICES[Math.floor(Math.random() * EMOJI_CHOICES.length)];
}

// ---------- 通用工具 ----------

// briefText：按码点截断超长文本（… 结尾），防止多行/超长错误撑坏 UI；短串原样返回
function briefText(s, n) {
  const chars = Array.from(s || "");
  return chars.length > n ? chars.slice(0, n).join("") + "…" : s;
}

// ---------- 语言（首启无偏好由 system 自动解析；按钮为中/EN 两态显式切换）----------

// 语言偏好与主题同一模式：本地先应用（首帧正确），选择变化同步后端（托盘/后端消息语言）。
// 后端 config.json + 快照的 resolved language 为权威：system 模式不做前端独立判定，
// 统一采用后端探测结果，避免 WebView 与原生托盘语言不一致。
// 交互约定：首次/无偏好（system）自动跟随系统语言；语言按钮只在 zh-CN ⇄ en
// 两个显式语言间切换并持久化，不会回到 system（后端 system 能力保留，快照对齐不变）。

// langSync：语言写入串行/去重与确认状态。
//   seq：用户每次改动的序号；链上任务执行前/结果返回后都校验，只有最新一次生效
//        （快速连点只落最后一次，迟到响应丢弃，不覆盖新选择）；
//   chain：写入串行链（同一时刻至多一个 SetLanguage 在途）；
//   confirmed：最近一次后端确认一致的 { pref, resolved }（失败回滚基准）。
const langSync = {
  seq: 0,
  chain: Promise.resolve(),
  confirmed: null,
};

// renderLangButton：语言按钮显示当前生效语言的紧凑可见状态（中/EN），
// tip/aria 明确当前语言与点击后将切换到的语言
function renderLangButton() {
  const btn = $("btn-lang");
  if (!btn) return;
  btn.textContent = currentLang() === "zh-CN" ? "中" : "EN";
  btn.title = t("lang.title");
  btn.setAttribute("aria-label", t("lang.aria"));
}

// applyStaticI18n：静态 data 属性文案（text/title/aria-label/placeholder）
function applyStaticI18n() {
  for (const el of document.querySelectorAll("[data-i18n]")) el.textContent = t(el.dataset.i18n);
  for (const el of document.querySelectorAll("[data-i18n-title]")) el.title = t(el.dataset.i18nTitle);
  for (const el of document.querySelectorAll("[data-i18n-aria]")) el.setAttribute("aria-label", t(el.dataset.i18nAria));
  for (const el of document.querySelectorAll("[data-i18n-placeholder]")) el.placeholder = t(el.dataset.i18nPlaceholder);
}

// refreshOpenModalI18n：模态打开期间切语言时重算动态文案（不丢表单输入与目标选择）：
// 标题（协议以实际表单值为准，编辑 TCP→UDP 后标题随新协议）、目标 Frps 下拉
// （空状态/「（当前）」后缀）、迁移提示（地址预览/短暂中断）。
function refreshOpenModalI18n() {
  const ctx = state.modalCtx;
  if (!ctx) return;
  if (ctx.kind === "service" && !$("service-modal").hidden) {
    const type = $("service-form").elements.type.value || ctx.type;
    ctx.type = type;
    $("modal-title").textContent = ctx.editing
      ? t("modal.editService", svcTypeLabel(type))
      : type === "http"
        ? t("action.addHttp")
        : t("action.addPort");
    syncServerSelect(type, true); // preserve：原样保留已选目标（含未选择），只刷新文案
    updateMoveHint();
  } else if (ctx.kind === "server" && !$("server-modal").hidden) {
    $("server-modal-title").textContent = ctx.editing ? t("modal.editServer") : t("modal.addServer");
  }
}

// applyLanguage：语言变化后的整体重绘（纯渲染，不触发任何后端业务调用）。
// 覆盖静态文案、主题/语言按钮、状态栏、列表卡片、日志标注、统计、更新区与
// 打开中的模态动态文案，全部从内存/已有状态重渲染，不丢用户数据、不重启隧道。
function applyLanguage() {
  applyStaticI18n();
  positionTabSlider(true); // Tab 文案宽度随语言变化，指示条需立即按新尺寸测量
  applyTheme(document.documentElement.dataset.theme ?? "system", false);
  renderLangButton();
  if (state.snapshot) {
    renderSnapshot(state.snapshot);
  }
  renderLogs();
  if (state.stats.data) renderStats();
  else if (state.stats.error) renderStatsError();
  renderUpdate();
  renderCheckButton();
  refreshOpenModalI18n();
}

// initLanguage：首帧应用本地缓存偏好（只读；resolved 待快照/绑定返回后以后端为准）。
function initLanguage() {
  applyPref(readStoredPref(), { persist: false });
  applyStaticI18n();
  renderLangButton();
}

// reconcileLanguageFromSnapshot：启动快照对齐（后端权威）。
// - 拉取期间用户已改语言（seq 已推进）→ 不覆盖本地意图，返回 false；
// - 快照 config.language 缺失/非法 → 按 system 权威对齐（不信本地残留）；
// - 快照顶层 language（后端 resolved）合法则直接采用，否则回退本地系统解析（旧后端）。
// 成功对齐会持久化并记为「已确认一致」状态，返回 true。
function reconcileLanguageFromSnapshot(snap, seqAtRequest) {
  if (seqAtRequest !== langSync.seq) return false;
  const pref = normalizePref(snap?.config?.language);
  const resolved = normalizeLang(snap?.language);
  langSync.confirmed = { pref, resolved: resolved ?? resolveLang(pref) };
  applyPref(pref, { persist: true, resolved });
  applyLanguage();
  return true;
}

// setLanguagePref：切换偏好（后端确认后持久化）并即时重绘。返回串行链任务 Promise。
// 流程：本地即时反馈（system 先用本地解析）→ 串行发送 SetLanguage → 以后端返回的
// resolved 修正 UI 并持久化；失败则回滚到最近确认一致状态并可见提示。
// 链上任务执行前/结果返回后都校验 seq：快速连点只落最后一次，迟到结果一律丢弃。
function setLanguagePref(pref) {
  const p = normalizePref(pref);
  const seq = ++langSync.seq;
  applyPref(p, { persist: false }); // 本地即时反馈；持久化等后端确认，避免失败后长期不一致
  applyLanguage();
  const run = async () => {
    if (seq !== langSync.seq) return null; // 已有更新的点击：本次作废（去重）
    try {
      const res = await api.AppService.SetLanguage(p);
      const rPref = normalizePref(res?.preference ?? p);
      const rLang = normalizeLang(res?.resolved) ?? resolveLang(rPref);
      if (seq !== langSync.seq) return null; // 迟到结果：丢弃，不覆盖新选择
      langSync.confirmed = { pref: rPref, resolved: rLang };
      applyPref(rPref, { persist: true, resolved: rLang });
      applyLanguage();
      return { pref: rPref, resolved: rLang };
    } catch (err) {
      if (seq !== langSync.seq) return null; // 已被更新点击取代：不回滚、不提示
      // 持久化失败：回滚到最近确认一致状态（localStorage 只在成功后写入，未被污染）
      const fb = langSync.confirmed ?? { pref: readStoredPref(), resolved: null };
      applyPref(fb.pref, { persist: false, resolved: fb.resolved });
      applyLanguage();
      const msg = t("lang.syncFail", err?.message ?? err);
      toast("error", msg);
      try {
        await api.AppService.FrontendLog("error", tEn("lang.syncFail", enDetail(err)));
      } catch {}
      return null;
    }
  };
  langSync.chain = langSync.chain.then(run, run);
  return langSync.chain;
}

// ---------- Toast（顶部居中堆叠；info/success 3s、error 5s；同屏最多 3 条旧的先走）----------

const TOAST_DURATION = { info: 3000, success: 3000, error: 5000 };
const TOAST_ICONS = { info: ICONS.info, success: ICONS.checkCircle, error: ICONS.alertCircle };

function toast(level, text) {
  const box = $("toasts");
  // 同屏最多 3 条：超限最旧的立即走退场流程。leaving 中的元素已退场、只待 remove，
  // 不计入也不可再 dismiss——此前 while(box.children.length >= 3) 在最旧元素已
  // leaving 时（dismiss 是 no-op、children 不减）条件恒真，第 4 条 toast 触发
  // JS 死循环整页冻结（狂点启停时高密度 toast 必现）。
  const active = Array.from(box.children).filter((el) => !el.dataset.leaving);
  while (active.length >= 3) dismissToast(active.shift());
  // 超长错误截断渲染，防止撑坏 toast；全文已进后端环形日志，不怕丢信息
  const shown = briefText(text, 120);
  const el = document.createElement("div");
  el.className = `toast toast--${level}`;
  el.innerHTML = `${TOAST_ICONS[level] ?? TOAST_ICONS.info}<span class="toast-text">${esc(shown)}</span>`;
  box.appendChild(el);
  void el.offsetWidth; // 强制回流提交初始态（translateY(-12px)+opacity:0）
  el.classList.add("is-in"); // 进场 200ms var(--ease-out)
  setTimeout(() => dismissToast(el), TOAST_DURATION[level] ?? 3000);
}

function dismissToast(el) {
  if (!el || el.dataset.leaving) return;
  el.dataset.leaving = "1";
  el.classList.remove("is-in"); // 原路反向 160ms
  el.addEventListener("transitionend", () => el.remove(), { once: true });
  setTimeout(() => el.remove(), 200); // transitionend 兜底
}

// ---------- 主题（三态：system / light / dark，localStorage 记忆）----------

const THEME_KEY = "frpdesk.theme";
const THEME_META = {
  system: { labelKey: "theme.system", icon: ICONS.monitor },
  light: { labelKey: "theme.light", icon: ICONS.sun },
  dark: { labelKey: "theme.dark", icon: ICONS.moon },
};

// applyTheme：应用主题并刷新按钮（persist=false 用于语言切换时的纯重绘，不重复写存储）
function applyTheme(mode, persist = true) {
  const root = document.documentElement;
  if (mode === "system") {
    delete root.dataset.theme;
  } else {
    root.dataset.theme = mode;
  }
  const btn = $("btn-theme");
  btn.dataset.mode = mode;
  btn.title = t("theme.title", t(THEME_META[mode].labelKey));
  btn.setAttribute("aria-label", t("theme.aria"));
  btn.innerHTML = THEME_META[mode].icon;
  if (!persist) return;
  try {
    localStorage.setItem(THEME_KEY, mode);
  } catch {
    /* 隐私模式等场景允许失败 */
  }
}

function initTheme() {
  let mode = "system";
  try {
    mode = readStored(THEME_KEY) ?? "system";
  } catch {
    /* ignore */
  }
  if (!["system", "light", "dark"].includes(mode)) mode = "system";
  applyTheme(mode);
  // 跟随系统时，系统切换实时生效
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    const cur = document.documentElement.dataset.theme ?? "system";
    if (cur !== "system") return;
    applyTheme("system");
  });
  $("btn-theme").addEventListener("click", () => {
    const order = ["system", "light", "dark"];
    const cur = document.documentElement.dataset.theme ?? "system";
    applyTheme(order[(order.indexOf(cur) + 1) % order.length]);
  });
  // 语言按钮：中/EN 两态显式切换（按当前生效语言取反）；点击只持久化显式
  // 偏好（zh-CN/en），不回到 system；首启未选择时仍由后端 system 权威解析
  $("btn-lang").addEventListener("click", () => {
    setLanguagePref(currentLang() === "zh-CN" ? "en" : "zh-CN");
  });
}

// ---------- Tab 切换（底部短指示条；面板与工具行右侧操作随动显隐）----------

function switchTab(name) {
  for (const tab of document.querySelectorAll(".tab")) {
    const on = tab.dataset.tab === name;
    tab.classList.toggle("is-active", on);
    tab.setAttribute("aria-selected", String(on));
  }
  for (const panel of document.querySelectorAll(".panel")) {
    const on = panel.id === `tab-${name}`;
    panel.classList.toggle("is-active", on);
    panel.hidden = !on;
  }
  // 工具行右侧操作跟随面板：新增服务 / 日志工具
  for (const actions of document.querySelectorAll(".toolbar-actions")) {
    actions.hidden = actions.dataset.panel !== name;
  }
  positionTabSlider(true); // 必须在 classList.toggle 之后测量（is-active 的 font-weight 会改变文字宽）
  // 统计面板：激活即拉取 + 2s 轮询，切走即停（不在后台无谓请求）
  if (name === "stats") startStatsPolling();
  else stopStatsPolling();
  // 配置面板：激活时拉一次最新快照（服务器实例状态随事件推送，此处兜底刷新）
  if (name === "servers") {
    api.AppService.Snapshot()
      .then(applySnapshot)
      .catch(() => {});
  }
  if (name === "logs") {
    loadLogs().then(() => scrollLogToEnd(true)).catch(() => {});
    run("run.loadProxyStatus", () => api.AppService.ProxyStatuses())
      .then((list) => {
        state.proxyStatus = Object.fromEntries(
          JSON.parse(JSON.stringify(list ?? [])).map((x) => [x.serviceId, x]),
        );
        if (state.snapshot) {
          renderServices(state.snapshot);
          renderStatus(state.snapshot.status); // 底部隧道计数随注册状态同步刷新
        }
      })
      .catch(() => {});
  }
}

// 定位 Tab 指示滑块：宽度贴文字（tab 宽减去水平 padding 12px×2），
// animate=false 用于首帧定位（先无 transition 定位、强制回流、再恢复过渡）。
function positionTabSlider(animate) {
  const active = document.querySelector(".tab.is-active");
  const slider = $("tab-slider");
  if (!active || !slider) return;
  const width = active.offsetWidth - 24;
  const left = active.offsetLeft + 12;
  if (!animate) {
    slider.classList.add("no-anim");
    slider.style.width = `${width}px`;
    slider.style.left = `${left}px`;
    void slider.offsetWidth;
    slider.classList.remove("no-anim");
    return;
  }
  slider.style.width = `${width}px`;
  slider.style.left = `${left}px`;
}

// ---------- 渲染 ----------

// 已启动时长小字：每秒刷新，mm:ss（超 1 小时 h:mm:ss）
let startedTimer = null;

function fmtDuration(ms) {
  const total = Math.floor(ms / 1000);
  const h = Math.floor(total / 3600);
  const mm = String(Math.floor((total % 3600) / 60)).padStart(2, "0");
  const ss = String(total % 60).padStart(2, "0");
  return h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`;
}

function setStartedAt(startedAt) {
  const el = $("status-started");
  if (startedTimer) {
    clearInterval(startedTimer);
    startedTimer = null;
  }
  if (!startedAt) {
    el.textContent = "";
    return;
  }
  const start = new Date(startedAt).getTime();
  const tick = () => {
    el.textContent = t("status.startedFor", fmtDuration(Date.now() - start));
  };
  tick();
  startedTimer = setInterval(tick, 1000);
}

function renderStatus(status) {
  const label = stateLabel(status.state);
  $("status-dot").className = `dot dot--${status.state}`;
  $("status-label").textContent = label;
  // 小字与主标签同文案时（如 running「已连接」）不重复展示
  let message = status.message === label ? "" : status.message ?? "";
  // running 下逐服务注册失败：底部栏提示 N 个服务注册失败（连接态不变，不关其他代理）
  if (status.state === "running" && failedProxyCount() > 0) {
    message = t("status.proxyFailed", failedProxyCount());
  }
  $("status-message").textContent = message;
  $("status-message").title = message || status.message || ""; // failed 长消息悬停看全文

  // 底部计数：当前已映射（已注册）隧道数量；enabled 分母不再展示
  const tc = tunnelCounts();
  const countEl = $("service-count");
  countEl.textContent = String(tc.registered);
  countEl.title = t("status.countTip");
  const failed = Number(status.failedCount ?? 0);
  const failedEl = $("status-failed");
  failedEl.hidden = failed <= 0;
  failedEl.textContent = failed > 0 ? t("status.failedCount", failed) : "";

  setStartedAt(status.state === "running" ? status.startedAt : null);
}

function esc(text) {
  const div = document.createElement("div");
  div.textContent = String(text ?? "");
  return div.innerHTML;
}

function renderSnapshot(snapshot) {
  // 逐服务器实例状态（后端含所有配置的服务器）；操作返回值经 applyConfigOnly 同步
  state.serverStatus = Object.fromEntries(
    (snapshot.serverStatuses ?? []).map((x) => [x.serverId, JSON.parse(JSON.stringify(x))]),
  );
  renderStatus(snapshot.status);

  renderServices(snapshot);
  renderServers();
}

// svcBadgeHTML：逐服务运行状态徽章（已停用优先；状态以其归属 Frps 实例为准，proxy 采样仅 enabled 有意义）
function svcBadgeHTML(svc) {
  if (!svc.enabled) return `<span class="tag tag-disabled">${esc(t("badge.disabled"))}</span>`;
  const tunnelState = serverStatus(svc.serverId).state;
  if (tunnelState === "starting") return `<span class="tag tag-proxy">${esc(t("badge.waiting"))}</span>`;
  if (tunnelState !== "running") return `<span class="tag tag-proxy">${esc(t("badge.notRunning"))}</span>`;
  const ps = state.proxyStatus[svc.id];
  if (!ps || ps.state === "starting") return `<span class="tag tag-proxy">${esc(t("badge.registering"))}</span>`;
  if (ps.state === "unregistered") return `<span class="tag tag-proxy">${esc(t("badge.unregistered"))}</span>`;
  if (ps.state === "failed") {
    // friendly 优先；rawErr 截断展示防撐坏徽章，完整原文悬停 title 可看
    const reason = ps.friendly || briefText(ps.rawErr, 40) || t("badge.registerFailed");
    const title = esc(ps.rawErr || reason).replaceAll('"', "&quot;");
    return `<span class="tag tag-svc-failed" title="${title}">${esc(t("badge.registerFailedReason", reason))}</span>`;
  }
  if (ps.state === "registered") return `<span class="tag tag-ok">${esc(t("badge.registered"))}</span>`;
  return "";
}

// failedProxyCount：注册失败的服务数（底部栏「N 个服务注册失败」提示用）
function failedProxyCount() {
  return Object.values(state.proxyStatus).filter((x) => x.state === "failed").length;
}

function renderServices(snapshot) {
  const list = $("service-list");
  list.querySelectorAll(".service-item").forEach((el) => el.remove());
  $("service-empty").style.display = snapshot.config.services.length ? "none" : "";

  for (const svc of snapshot.config.services) {
    const type = svc.type === "tcp" || svc.type === "udp" ? svc.type : "http";
    const isPort = type !== "http";
    // 公网地址按服务归属的目标 Frps 生成；无归属/缺字段不产生地址（也不展示复制/二维码入口）。
    // TCP/UDP 地址恢复点击复制（Wails 原生剪贴板，host:port 无协议不宜用浏览器打开）；HTTP 才走链接。
    const server = svcServer(svc);
    const publicUrl = svcPublicURL(svc, server);
    const addressHTML = !publicUrl
      ? ""
      : isPort
        ? `<span class="addr-copy" data-copy="${escAttr(publicUrl)}" title="${escAttr(t("card.clickToCopy"))}">${esc(publicUrl)}</span>`
        : `<a href="${escAttr(publicUrl)}" data-url="${escAttr(publicUrl)}">${esc(publicUrl)}</a>`;
    // 卡片展示所属配置名称（仅文本，不放选择控件）；无归属时明示
    const srvTag = server
      ? `<span class="tag tag-srv" title="${escAttr(t("card.serverTip", server.name || serverConnAddr(server)))}">${esc(server.name || serverConnAddr(server))}</span>`
      : `<span class="tag tag-srv tag-srv--none" title="${escAttr(t("card.unassignedTip"))}">${esc(t("card.unassigned"))}</span>`;
    // 复制/二维码按钮仅在卡片有公网地址时渲染
    const copyBtn = publicUrl
      ? `<button class="btn btn-icon btn-ghost svc-op svc-op--r2" data-act="copy" data-copy="${escAttr(publicUrl)}" title="${escAttr(t("action.copyAddress"))}" aria-label="${escAttr(t("action.copyAddress"))}">${ICONS.copy}</button>`
      : "";
    const qrBtn = publicUrl
      ? `<button class="btn btn-icon btn-ghost svc-op svc-op--r3" data-act="qr" data-url="${escAttr(publicUrl)}" title="${escAttr(t("action.showQr"))}" aria-label="${escAttr(t("action.showQr"))}">${ICONS.qrCode}</button>`
      : "";
    // 排序把手（grip-vertical）：隐藏操作组最左（r4 波次位）；仅拖把手可排序，整卡不可拖。
    // no-drag：禁止窗口拖拽区干扰（与标题栏 drag 语义解耦）
    const dragBtn = `<button class="btn btn-icon btn-ghost svc-op svc-op--r4 svc-drag-handle no-drag" data-act="drag" title="${escAttr(t("action.reorder"))}" aria-label="${escAttr(t("action.reorder"))}">${ICONS.gripVertical}</button>`;
    const item = document.createElement("div");
    item.className = "service-item";
    item.dataset.id = svc.id;
    item.innerHTML = `
      <div class="service-icon">${esc(svcIcon(svc))}</div>
      <div class="service-main">
        <div class="service-name">
          <span class="service-name-text">${esc(svc.name)}</span>
          <span class="tag tag-${type}">${svcTypeLabel(type)}</span>
          ${srvTag}
          ${svcBadgeHTML(svc)}
        </div>
        <div class="service-meta">
          ${addressHTML}
          ${addressHTML ? '<span class="meta-arrow" aria-hidden="true">→</span>' : ""}
          <span title="${escAttr(t("card.localTargetTip"))}">${esc(svc.localHost)}:${svc.localPort}</span>
        </div>
      </div>
      <div class="service-ops">
        ${dragBtn}
        ${qrBtn}
        ${copyBtn}
        <button class="btn btn-icon btn-ghost svc-op svc-op--r1" data-act="edit" title="${escAttr(t("action.edit"))}" aria-label="${escAttr(t("action.edit"))}">${ICONS.pencil}</button>
        <button class="btn btn-icon btn-ghost svc-op svc-op-danger svc-op--r0" data-act="delete" title="${escAttr(t("action.delete"))}" aria-label="${escAttr(t("action.delete"))}">${ICONS.trash}</button>
        <input type="checkbox" class="switch" data-act="toggle" ${svc.enabled ? "checked" : ""} title="${escAttr(t("action.toggleEnabled"))}" />
      </div>
    `;
    list.appendChild(item);
  }
}

// renderServers：配置 Tab 的 Frps 卡片列表（两列等宽；状态/启停/编辑/删除/关联隧道数）
function renderServers() {
  const list = $("server-list");
  const servers = configServers();
  const services = state.snapshot?.config?.services ?? [];
  list.querySelectorAll(".server-item").forEach((el) => el.remove());
  $("server-empty").style.display = servers.length ? "none" : "";

  for (const srv of servers) {
    const st = serverStatus(srv.id);
    const stateText = st.state === "stopped" && !services.some((s) => s.serverId === srv.id && s.enabled)
      ? t("srv.noEnabledTunnels")
      : stateLabel(st.state);
    const badgeClass =
      st.state === "running" ? "tag-ok" : st.state === "failed" ? "tag-svc-failed" : st.state === "stopped" ? "tag-disabled" : "tag-proxy";
    // 失败原因只进状态 tag 的 title，不占卡片正文
    const badgeTitle = st.state === "failed" && st.message ? escAttr(st.message) : escAttr(stateText);
    const connAddr = serverConnAddr(srv);
    // 关联隧道：禁用计入关联，已启用单独计数（零为正常展示）
    const linked = services.filter((s) => s.serverId === srv.id).length;
    const linkedEnabled = services.filter((s) => s.serverId === srv.id && s.enabled).length;
    // 连接测试 busy：状态外置（state.serverProbe），轮询/快照重绘不丢
    const probing = Boolean(state.serverProbe[srv.id]);
    const item = document.createElement("div");
    item.className = "server-item";
    item.dataset.id = srv.id;
    item.innerHTML = `
      <div class="server-head">
        <span class="service-name-text" title="${escAttr(srv.name)}">${esc(srv.name)}</span>
        <span class="tag ${badgeClass}" title="${badgeTitle}">${esc(stateText)}</span>
      </div>
      <div class="server-addr">
        <span class="mono" title="${escAttr(t("srv.connAddrTip"))}">${esc(connAddr)}</span>
      </div>
      <div class="server-foot">
        <span>${esc(t("srv.linked", linked, linkedEnabled))}</span>
      </div>
      <div class="server-ops">
        <button class="btn btn-sm" data-act="srv-test" ${probing ? "disabled" : ""} title="${escAttr(t("srv.testTip"))}">${esc(probing ? t("srv.testing") : t("srv.test"))}</button>
        <button class="btn btn-sm" data-act="srv-edit" title="${escAttr(t("srv.editTip"))}" aria-label="${escAttr(t("srv.editTip"))}">${esc(t("action.edit"))}</button>
        <button class="btn btn-sm btn-ghost-danger" data-act="srv-delete" title="${escAttr(t("srv.deleteTip"))}" aria-label="${escAttr(t("srv.deleteTip"))}">${esc(t("action.delete"))}</button>
      </div>
    `;
    list.appendChild(item);
  }
}
function renderLogs() {
  const view = $("log-view");
  view.innerHTML = state.logs
    .map(
      (e) =>
        // 归属标注：LogEntry 带服务器信息则标名称；公共/旧字段固定标 System
        // （日志内容与归属标注均不随界面语言翻译）
        `<div class="log-line ${e.level}"><span class="log-time">${esc(e.timestamp)}</span><span class="log-srv">[${esc(e.serverName || "System")}]</span>${esc(e.message)}</div>`,
    )
    .join("");
}

// 自动滚动：用户上滚暂停，回到底部恢复；force 表示程序触发必须滚。
function scrollLogToEnd(force) {
  const view = $("log-view");
  if (!force && !state.autoScroll) return;
  view.scrollTop = view.scrollHeight;
}

// logKey：日志去重键（LogEntry.id；无 id 的异常条目退化为 内容组合键，避免误吞）
function logKey(e) {
  return e?.id != null ? `id:${e.id}` : `raw:${e?.timestamp}|${e?.level}|${e?.message}`;
}

function appendLogs(entries) {
  if (!entries || !entries.length) return;
  const fresh = [];
  for (const e of entries) {
    const key = logKey(e);
    if (state.logIds.has(key)) continue; // 按 id 去重：事件重放/推送重叠不重复渲染
    state.logIds.add(key);
    fresh.push(e);
  }
  if (!fresh.length) return;
  state.logs.push(...fresh);
  if (state.logs.length > 500) {
    const dropped = state.logs.slice(0, state.logs.length - 500);
    for (const e of dropped) state.logIds.delete(logKey(e));
    state.logs = state.logs.slice(-500);
  }
  renderLogs();
  scrollLogToEnd(false);
}

// mergeLogs：全量拉取结果与已有日志取并集，按数值 id 升序恢复时序后取尾 500。
// 修复：此前把已有事件日志无条件追加在拉取结果尾部，旧条目可能错序排在最新
// 日志之后并被误裁；升序排序消除追加顺序问题（无 id 异常条目排最前）。
function mergeLogs(fetched) {
  const base = JSON.parse(JSON.stringify(fetched ?? []));
  const seen = new Set(base.map(logKey));
  const union = [...base, ...state.logs.filter((e) => !seen.has(logKey(e)))];
  union.sort((a, b) => (typeof a?.id === "number" ? a.id : -1) - (typeof b?.id === "number" ? b.id : -1));
  state.logIds = new Set();
  state.logs = [];
  appendLogs(union.slice(-500));
}

function applySnapshot(snapshot) {
  // 事件/RPC 共用入口：先按 prev→new 的归属 diff 清理移走服务的旧 proxy 状态，
  // 再落地新快照（次序不能反，否则 diff 恒为空、清理失效）
  pruneMovedProxyStatus(state.snapshot?.config?.services, snapshot.config?.services);
  state.snapshot = JSON.parse(JSON.stringify(snapshot));
  renderSnapshot(state.snapshot);
}

// 操作类绑定调用的返回值只更新 config；status 一律以事件推送为权威——
// 绑定返回值可能携带竞速窗口内的过渡态（如「正在停止 frpc」），
// 后到会覆盖先到的终态事件（用户实测 UI 卡过渡态的根因）。
function applyConfigOnly(snapshot) {
  if (!state.snapshot) {
    applySnapshot(snapshot);
    return;
  }
  pruneMovedProxyStatus(state.snapshot.config.services, snapshot.config.services);
  state.snapshot.config = JSON.parse(JSON.stringify(snapshot.config));
  renderSnapshot(state.snapshot);
}

// ---------- 后端调用 ----------

// HAN_RE / enDetail：诊断日志通道的错误详情一律英文。后端业务错误按当前界面
// 语言渲染（中文时含 CJK），不能进入英文日志通道；后端已在 RPC 边界（
// AppService.fail）记录英文错误详情，此处用固定英文占位保留可读性。
// 英文技术错误（绑定/传输失败等）原样保留。
const HAN_RE = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u;
function enDetail(err) {
  const msg = String(err?.message ?? err ?? "");
  return HAN_RE.test(msg) ? "localized UI error (backend log has details)" : msg;
}

async function run(key, fn) {
  try {
    return await fn();
  } catch (err) {
    // toast 走当前语言（双语）；诊断日志通道固定英文（不随界面语言翻译）
    const msg = t("run.fail", t(key), err.message ?? err);
    toast("error", msg);
    try {
      await api.AppService.FrontendLog("error", tEn("run.fail", tEn(key), enDetail(err)));
    } catch {}
    throw err;
  }
}

async function refreshAll() {
  applySnapshot(await run("run.loadConfig", () => api.AppService.Snapshot()));
}

async function loadLogs() {
  const logs = await run("run.loadLogs", () => api.AppService.Logs());
  mergeLogs(logs); // 合并而非覆盖：拉取窗口内事件先到的更晚日志不丢
  renderLogs();
}

// ---------- 统计面板（HTTP 服务请求统计；契约字段与后端冻结，勿改） ----------
// 数据形状：{ since, total:{requests,trafficIn,trafficOut,errors}, services:[{serviceID,name,
// requests,trafficIn,trafficOut,errors,statusCodes,topPaths}], timeline:{bucketSeconds,buckets},
// recent:[{t,serviceID,method,path,status,durationMs,bytesIn,bytesOut}] }

const STATS_POLL_MS = 2000; // 轮询周期：2s（仅 stats tab 激活期间运行）
const TIMELINE_WINDOW_MS = 6 * 3600 * 1000; // 分时曲线时间窗：近 6 小时（since 不足 6h 时从 since 起）
const TIMELINE_MAX_POINTS = 600; // 曲线点数上限：超出按相邻聚合降采样，控制 path 体积

// monotonePath：monotone cubic 插值（Fritsch–Carlson）把数据点连成平滑曲线的纯函数。
// 切线取相邻差分均值并在符号相反处置 0，再用限幅系数（a²+b²≤9）抑制局部过冲，
// 保证曲线单调段不越过数据点（不产生数据之外的假峰/假谷）。输入至少 2 点，返回 path d。
function monotonePath(points) {
  const n = points.length;
  if (n < 2) return "";
  if (n === 2) return `L${points[1].x.toFixed(2)},${points[1].y.toFixed(2)}`;
  const dx = [];
  const slope = [];
  for (let i = 0; i < n - 1; i++) {
    dx[i] = points[i + 1].x - points[i].x;
    slope[i] = (points[i + 1].y - points[i].y) / dx[i];
  }
  // 初值切线：端点取单侧差分，中间同号取均值、变号置 0（极值点处切线水平）
  const tan = [slope[0]];
  for (let i = 1; i < n - 1; i++) {
    tan[i] = slope[i - 1] * slope[i] <= 0 ? 0 : (slope[i - 1] + slope[i]) / 2;
  }
  tan[n - 1] = slope[n - 2];
  // 限幅：三点区域超出抛物线可达范围时按比例收缩切线
  for (let i = 0; i < n - 1; i++) {
    if (slope[i] === 0) {
      tan[i] = 0;
      tan[i + 1] = 0;
      continue;
    }
    const a = tan[i] / slope[i];
    const b = tan[i + 1] / slope[i];
    const s = a * a + b * b;
    if (s > 9) {
      const t = 3 / Math.sqrt(s);
      tan[i] = t * a * slope[i];
      tan[i + 1] = t * b * slope[i];
    }
  }
  // 每段 cubic：控制点取段内 1/3、2/3 处（Hermite → Bézier 换算）
  let d = "";
  for (let i = 0; i < n - 1; i++) {
    const h = dx[i];
    const x0 = points[i].x;
    const y0 = points[i].y;
    const x1 = points[i + 1].x;
    const y1 = points[i + 1].y;
    const c1y = y0 + (tan[i] * h) / 3;
    const c2y = y1 - (tan[i + 1] * h) / 3;
    d += `C${(x0 + h / 3).toFixed(2)},${c1y.toFixed(2)} ${(x1 - h / 3).toFixed(2)},${c2y.toFixed(2)} ${x1.toFixed(2)},${y1.toFixed(2)}`;
  }
  return d;
}

// callStats：调用后端统计绑定。Stats 与其他方法一样生成在 AppService 命名空间下
// （api.AppService.Stats）；兑底识别顶层 api.Stats 形态，避免生成命名空间差异导致不可用。
function callStats() {
  const fn = api.AppService?.Stats ?? api.Stats;
  if (typeof fn !== "function") throw new Error(t("stats.bindingsMissing"));
  return fn.call(api.AppService ?? api);
}

// refreshStats：拉一次统计并渲染。轮询重入保护：上一轮未返回则跳过本轮，避免请求堆叠。
// 失败低调处理（不 toast）：无数据→整页错误态；已有数据→保留旧数据 + 顶部一行小字。
// 后端日志每次失败连击只记一次，恢复成功后重置，避免 2s 一条刷爆环形日志。
async function refreshStats() {
  if (state.stats.inFlight) return;
  state.stats.inFlight = true;
  try {
    const data = await callStats();
    state.stats.data = JSON.parse(JSON.stringify(data));
    state.stats.error = null;
    state.stats.errLogged = false;
    renderStats();
  } catch (err) {
    state.stats.error = String(err?.message ?? err);
    if (!state.stats.errLogged) {
      state.stats.errLogged = true;
      try {
        await api.AppService.FrontendLog("error", tEn("stats.fetchFailLog", enDetail(state.stats.error)));
      } catch {}
    }
    renderStatsError();
  } finally {
    state.stats.inFlight = false;
  }
}

function startStatsPolling() {
  stopStatsPolling();
  refreshStats(); // 激活立即拉取一次
  state.stats.timer = setInterval(refreshStats, STATS_POLL_MS);
}

function stopStatsPolling() {
  if (state.stats.timer) {
    clearInterval(state.stats.timer);
    state.stats.timer = null;
  }
}

// fmtBytes：字节自适应 B/KB/MB/GB（1024 进制，≤1 位小数）
function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n;
  let i = -1;
  do {
    v /= 1024;
    i++;
  } while (v >= 1024 && i < units.length - 1);
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

// fmtMs：耗时 ms，≥1000ms 显示 s（1 位小数）
function fmtMs(ms) {
  ms = Number(ms) || 0;
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.round(ms)} ms`;
}

// fmtRel：相对时间（统计起点用），刚发生返回「刚刚」（随语言）
function fmtRel(ms) {
  const abs = Math.max(0, ms);
  const MIN = 60e3;
  const HOUR = 3600e3;
  const DAY = 24 * HOUR;
  if (abs < MIN) return t("stats.relJustNow");
  if (abs < HOUR) return t("stats.relMinutes", Math.floor(abs / MIN));
  if (abs < DAY) return t("stats.relHours", Math.floor(abs / HOUR));
  return t("stats.relDays", Math.floor(abs / DAY));
}

// fmtClock / fmtHM：RFC3339 → 本地 HH:MM:SS / HH:MM（非法时间兑底占位）
function fmtClock(t) {
  const d = new Date(t);
  return isNaN(d) ? "--:--:--" : d.toTimeString().slice(0, 8);
}

function fmtHM(t) {
  const d = new Date(t);
  return isNaN(d) ? "--:--" : d.toTimeString().slice(0, 5);
}

// statusClassKey：状态码 → 分色键（语义：2xx 绿 / 3xx 蓝灰 / 4xx 橙 / 5xx 红 / 其他灰）
function statusClassKey(code) {
  const c = Number(code);
  if (c >= 200 && c < 300) return "2xx";
  if (c >= 300 && c < 400) return "3xx";
  if (c >= 400 && c < 500) return "4xx";
  if (c >= 500 && c < 600) return "5xx";
  return "other";
}

// escAttr：属性值转义（esc 不转引号，title/aria 等属性内插需兑底引号）
function escAttr(s) {
  return esc(s).replaceAll('"', "&quot;");
}

// svcDisplayName：统计条目的服务展示名 = 统计自带 name → 本地服务列表 name → serviceID。
// serviceID 与本地 id 可能存在前后缀差异（截断/哈希），精确匹配失败后按前缀互配兑底。
function svcDisplayName(item) {
  const name = String(item?.name ?? "").trim();
  if (name) return name;
  const sid = String(item?.serviceID ?? "");
  const list = state.snapshot?.config?.services ?? [];
  const local = list.find((s) => s.id === sid) ?? list.find((s) => sid && (s.id.startsWith(sid) || sid.startsWith(s.id)));
  return String(local?.name ?? "").trim() || sid || t("stats.unknownService");
}

// renderStats：统计面板唯一渲染入口（数据分发到各区块；无数据不渲染空图）
function renderStats() {
  const data = state.stats.data;
  if (!data) return;
  $("stats-error").hidden = true;
  const total = data.total ?? {};
  const empty = $("stats-empty");
  if (!total.requests) {
    $("stats-body").hidden = true;
    // 同步隐藏全部区块：避免上一轮数据残留的 hidden 标记不一致
    for (const sec of $("stats-body").querySelectorAll(".stat-section")) sec.hidden = true;
    empty.hidden = false;
    $("stats-empty-title").textContent = t("stats.emptyTitle");
    $("stats-empty-hint").textContent = t("stats.emptyHint");
    return;
  }
  empty.hidden = true;
  $("stats-body").hidden = false;
  renderStatCards(data);
  $("rank-grid").hidden = !(data.services ?? []).length; // 双排行容器：无服务整体隐藏
  renderServiceRank(data.services ?? []);
  renderTrafficRank(data.services ?? []);
  renderTopPaths(data.services ?? []);
  renderTimeline(data.timeline ?? {}, data.since);
  renderDevices(data.devices ?? []);
  renderRecent(data.recent ?? []);
}

// renderStatsError：拉取失败的低调错误态——无数据→整页提示；已有数据→顶部一行小字。
function renderStatsError() {
  const hasData = Boolean(state.stats.data && (state.stats.data.total?.requests ?? 0) > 0);
  if (hasData) {
    $("stats-error").hidden = false;
    $("stats-error").textContent = t("stats.refreshFail", briefText(state.stats.error, 80));
    return;
  }
  $("stats-error").hidden = true;
  $("stats-body").hidden = true;
  for (const sec of document.querySelectorAll("#stats-body .stat-section")) sec.hidden = true;
  $("stats-empty").hidden = false;
  $("stats-empty-title").textContent = t("stats.loadFailTitle");
  $("stats-empty-hint").textContent = `${state.stats.error}${t("stats.retryHint")}`;
}

// renderStatCards：顶部数字卡片行（总请求 / 总流量 / 用户数 / 统计起点）
function renderStatCards(data) {
  const total = data.total ?? {};
  // 用户数 = 按 UA 去重的客户端数（同一设备多次访问只计一），与 donut 中心同源
  const sinceMs = data.since ? new Date(data.since).getTime() : NaN;
  const sinceText = isNaN(sinceMs) ? "—" : fmtRel(Date.now() - sinceMs);
  const sinceFull = isNaN(sinceMs) ? "" : new Date(sinceMs).toLocaleString();
  $("stat-cards").innerHTML = `
    <div class="stat-card">
      <div class="stat-label">${esc(t("stats.totalRequests"))}</div>
      <div class="stat-value">${Number(total.requests ?? 0).toLocaleString()}</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">${esc(t("stats.totalTraffic"))}</div>
      <div class="stat-value">${fmtBytes((total.trafficIn ?? 0) + (total.trafficOut ?? 0))}</div>
      <div class="stat-sub"><span>↓ ${fmtBytes(total.trafficIn ?? 0)}</span><span>↑ ${fmtBytes(total.trafficOut ?? 0)}</span></div>
    </div>
    <div class="stat-card">
      <div class="stat-label">${esc(t("stats.clients"))}</div>
      <div class="stat-value">${Number(total.clients ?? 0).toLocaleString()}</div>
      <div class="stat-sub"><span>${esc(t("stats.uniqueByUA"))}</span></div>
    </div>
    <div class="stat-card">
      <div class="stat-label">${esc(t("stats.since"))}</div>
      <div class="stat-value">${esc(sinceText)}</div>
      <div class="stat-sub" title="${escAttr(sinceFull)}">${esc(sinceFull)}</div>
    </div>
  `;
}

// renderServiceRank：服务访问排行（水平条形，以最大值为基准；防御性按 requests 降序）
function renderServiceRank(services) {
  const sec = $("stat-services-sec");
  const list = [...services].sort((a, b) => (b.requests ?? 0) - (a.requests ?? 0));
  if (!list.length) {
    sec.hidden = true;
    return;
  }
  sec.hidden = false;
  const max = Math.max(...list.map((s) => s.requests ?? 0), 1);
  $("stat-services").innerHTML = list
    .map((s) => {
      const reqs = Number(s.requests ?? 0);
      const pct = reqs > 0 ? Math.max((reqs / max) * 100, 2) : 0; // >0 保底 2% 可见，0 则不留条
      const errs = Number(s.errors ?? 0);
      const name = svcDisplayName(s);
      const cur = currentServerName(s.serviceID);
      return `
      <div class="rank-row">
        <div class="rank-label">
          <span class="rank-name" title="${escAttr(name)}">${esc(name)}</span>
          ${cur ? `<span class="rank-cur" title="${escAttr(t("stats.currentServerTip", cur))}">${esc(t("stats.currentServer", cur))}</span>` : ""}
          ${errs > 0 ? `<span class="rank-err" title="${escAttr(t("stats.errTip", errs))}">${esc(t("stats.errCount", errs))}</span>` : ""}
          <span class="rank-count">${reqs.toLocaleString()}</span>
        </div>
        <div class="rank-bar"><i style="width:${pct}%"></i></div>
      </div>`;
    })
    .join("");
}

// renderTrafficRank：服务流量排行（trafficIn+trafficOut 降序，条形按最大流量为基准，字节格式化）
function renderTrafficRank(services) {
  const sec = $("stat-traffic-sec");
  const bytesOf = (s) => (s.trafficIn ?? 0) + (s.trafficOut ?? 0);
  const list = [...services].sort((a, b) => bytesOf(b) - bytesOf(a));
  if (!list.length) {
    sec.hidden = true;
    return;
  }
  sec.hidden = false;
  const max = Math.max(...list.map(bytesOf), 1);
  $("stat-traffic").innerHTML = list
    .map((s) => {
      const total = bytesOf(s);
      const pct = total > 0 ? Math.max((total / max) * 100, 2) : 0;
      const name = svcDisplayName(s);
      const cur = currentServerName(s.serviceID);
      return `
      <div class="rank-row">
        <div class="rank-label">
          <span class="rank-name" title="${escAttr(name)}">${esc(name)}</span>
          ${cur ? `<span class="rank-cur" title="${escAttr(t("stats.currentServerTip", cur))}">${esc(t("stats.currentServer", cur))}</span>` : ""}
          <span class="rank-count" title="${escAttr(`↓ ${fmtBytes(s.trafficIn ?? 0)} / ↑ ${fmtBytes(s.trafficOut ?? 0)}`)}">${fmtBytes(total)}</span>
        </div>
        <div class="rank-bar"><i style="width:${pct}%"></i></div>
      </div>`;
    })
    .join("");
}

// renderTopPaths：合并所有服务 topPaths 按 count 降序取前 10，条形列表
function renderTopPaths(services) {
  const sec = $("stat-paths-sec");
  const merged = [];
  for (const svc of services) {
    const name = svcDisplayName(svc);
    for (const p of svc.topPaths ?? []) {
      merged.push({ path: String(p.path ?? ""), count: Number(p.count ?? 0), svc: name });
    }
  }
  merged.sort((a, b) => b.count - a.count);
  const top = merged.slice(0, 10);
  if (!top.length) {
    sec.hidden = true;
    return;
  }
  sec.hidden = false;
  const max = Math.max(...top.map((p) => p.count), 1);
  $("stat-paths").innerHTML = top
    .map((p) => {
      const pct = p.count > 0 ? Math.max((p.count / max) * 100, 2) : 0;
      return `
      <div class="rank-row">
        <div class="rank-label">
          <span class="rank-path mono" title="${escAttr(p.path)}">${esc(p.path)}</span>
          <span class="rank-svc" title="${escAttr(p.svc)}">${esc(p.svc)}</span>
          <span class="rank-count">${p.count.toLocaleString()}</span>
        </div>
        <div class="rank-bar"><i style="width:${pct}%"></i></div>
      </div>`;
    })
    .join("");
}

// renderTimeline：近 6h 分时请求曲线（SVG 面积 + 折线，静态），全宽区块。
// 后端只回传非空桶（升序），按 epoch 对齐槽位表补零；窗口起点不早于 since（不足 6h 从 since 起），
// 时间跨度大于点数上限时相邻聚合降采样。
function renderTimeline(timeline, since) {
  const sec = $("stat-timeline-sec");
  const chart = $("stat-timeline-chart");
  const buckets = Array.isArray(timeline?.buckets) ? timeline.buckets : [];
  if (!buckets.length) {
    sec.hidden = true;
    return;
  }
  sec.hidden = false;
  const bucketSeconds = Math.max(Number(timeline.bucketSeconds) || 60, 1);
  const slotMs = bucketSeconds * 1000;
  const bySlot = new Map();
  for (const b of buckets) {
    const t = new Date(b.t).getTime();
    if (isNaN(t)) continue;
    const slot = Math.floor(t / slotMs);
    bySlot.set(slot, (bySlot.get(slot) ?? 0) + (Number(b.requests) || 0)); // 同槽重复桶累加兑底
  }
  const now = Date.now();
  // 窗口起点：近 6h，但不过早于统计起点 since（不足 6h 从 since 起）
  const sinceMs = since ? new Date(since).getTime() : NaN;
  const windowStart = Math.min(Math.max(now - TIMELINE_WINDOW_MS, isNaN(sinceMs) ? 0 : sinceMs), now);
  let slots = [];
  for (let s = Math.floor(windowStart / slotMs); s <= Math.floor(now / slotMs); s++) {
    slots.push({ t: s * slotMs, v: bySlot.get(s) ?? 0 });
  }
  if (!slots.length) {
    chart.innerHTML = "";
    return;
  }
  // 降采样：相邻 step 个桶求和合并（请求总数守恒），控制 SVG path 体积
  const step = Math.ceil(slots.length / TIMELINE_MAX_POINTS);
  if (step > 1) {
    const down = [];
    for (let i = 0; i < slots.length; i += step) {
      let v = 0;
      for (let j = i; j < Math.min(i + step, slots.length); j++) v += slots[j].v;
      down.push({ t: slots[i].t, v });
    }
    slots = down;
  }
  const peak = Math.max(...slots.map((p) => p.v));
  const bucketText =
    bucketSeconds % 3600 === 0
      ? t("stats.bucketHours", bucketSeconds / 3600)
      : bucketSeconds % 60 === 0
        ? t("stats.bucketMinutes", bucketSeconds / 60)
        : t("stats.bucketSeconds", bucketSeconds);
  // 窗口标签：since 在 6h 内时按 since 截短，首桶时间即窗口起点
  const windowLabel = !isNaN(sinceMs) && sinceMs > now - TIMELINE_WINDOW_MS ? t("stats.windowSince", fmtHM(slots[0].t)) : t("stats.windowRecent");
  $("stat-timeline-note").textContent = t("stats.timelineNote", windowLabel, peak.toLocaleString(), bucketText);

  const W = 600;
  const H = 140;
  const n = slots.length;
  const pts = slots.map((p, i) => ({ x: n <= 1 ? 0 : (i / (n - 1)) * W, y: H - (peak > 0 ? (p.v / peak) * (H - 6) : 0) - 2 }));
  let line;
  if (n === 1) {
    const yy = pts[0].y.toFixed(2);
    line = `M0,${yy}L${W},${yy}`; // 单桶横线占满
  } else {
    line = `M${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}` + monotonePath(pts); // monotone 平滑：不过数据点的真峰
  }
  const area = `${line}L${W},${H}L0,${H}Z`;
  // X 轴刻度：少量均分位置（首尾对齐图表两端）
  const tickCount = n >= 5 ? 5 : n;
  const ticks = [];
  for (let k = 0; k < tickCount; k++) {
    const i = tickCount > 1 ? Math.round((k / (tickCount - 1)) * (n - 1)) : 0;
    ticks.push(`<span>${esc(fmtHM(slots[i].t))}</span>`);
  }
  chart.innerHTML = `
    <svg class="timeline-svg" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" role="img" aria-label="${escAttr(t("stats.chartAria", windowLabel, peak))}">
      <defs>
        <linearGradient id="stat-area-grad" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" class="grad-stop-top"></stop>
          <stop offset="1" class="grad-stop-bottom"></stop>
        </linearGradient>
      </defs>
      <path class="timeline-area" d="${area}" fill="url(#stat-area-grad)"></path>
      <path class="timeline-line" d="${line}"></path>
      <line class="hover-line" y1="0" y2="${H}" x1="-99" x2="-99"></line>
      <circle class="hover-dot" r="3.5" cx="-99" cy="-99"></circle>
    </svg>
    <div class="timeline-tip"></div>
    <div class="timeline-ticks">${ticks.join("")}</div>
  `;
  // 曲线槽位上下文供 hover 吸附（窗口 resize 后由下次渲染重建，无需监听 resize）
  state.stats.chart = { slots, W, H };
}

// renderDevices：访问设备分布独立区块（donut 居中 + 图例右侧横排可换行）。
// devices 按 count 降序由后端保证；单类 100% 时 dasharray 满圈退化为整圆；空 devices 整区块不渲染。
const DEVICE_LABELS = {
  desktop: "device.desktop",
  mobile: "device.mobile",
  tablet: "device.tablet",
  bot: "device.bot",
  other: "device.other",
};

// deviceLabel：设备类型 → 当前语言文案（未知类型原样展示）
function deviceLabel(type) {
  return DEVICE_LABELS[type] ? t(DEVICE_LABELS[type]) : type;
}

function renderDevices(devices) {
  const sec = $("stat-devices-sec");
  const pieBox = $("stat-devices");
  const list = devices.filter((d) => (Number(d?.count) || 0) > 0);
  if (!list.length) {
    sec.hidden = true;
    pieBox.innerHTML = "";
    return;
  }
  sec.hidden = false;
  const total = list.reduce((sum, d) => sum + (Number(d.count) || 0), 0);
  const R = 40;
  const C = 2 * Math.PI * R;
  let acc = 0;
  const segs = list
    .map((d) => {
      const frac = (Number(d.count) || 0) / total;
      const label = deviceLabel(d.type);
      const count = Number(d.count) || 0;
      const seg = `<circle class="pie-seg pie-seg--${escAttr(d.type)}" data-type="${escAttr(d.type)}" data-count="${count}" data-pct="${Math.round(frac * 100)}" data-label="${escAttr(label)}" r="${R}" cx="50" cy="50" fill="none" stroke-width="18" stroke-dasharray="${(frac * C).toFixed(2)} ${(C - frac * C).toFixed(2)}" stroke-dashoffset="${(-acc * C).toFixed(2)}" transform="rotate(-90 50 50)"><title>${escAttr(t("device.segTip", label, count.toLocaleString(), Math.round(frac * 100)))}</title></circle>`;
      acc += frac;
      return seg;
    })
    .join("");
  pieBox.innerHTML = `
    <svg class="device-pie-svg" viewBox="0 0 100 100" role="img" aria-label="${escAttr(t("device.pieAria", total))}" data-total="${total}">
      ${segs}
      <g class="pie-center">
        <!-- 中心交叉淡：默认面（clients 总数）与 hover 面（当前类），has-hover class 切换双向淡入淡出 -->
        <g class="pie-center-face pie-center-default">
          <text class="pie-total" x="50" y="53" text-anchor="middle">${total.toLocaleString()}</text>
          <text class="pie-total-label" x="50" y="66" text-anchor="middle">${esc(t("device.clients"))}</text>
        </g>
        <g class="pie-center-face pie-center-hover" aria-hidden="true">
          <text class="pie-total" x="50" y="53" text-anchor="middle"></text>
          <text class="pie-total-label" x="50" y="66" text-anchor="middle"></text>
        </g>
      </g>
    </svg>
    <div class="device-legend">
      ${list
        .map((d) => {
          const label = deviceLabel(d.type);
          const count = Number(d.count) || 0;
          const pct = Math.round((count / total) * 100);
          return `<span class="legend-item"><i class="pie-dot pie-dot--${escAttr(d.type)}"></i>${esc(label)}<b>${count.toLocaleString()}</b><span class="legend-pct">${pct}%</span></span>`;
        })
        .join("")}
    </div>
  `;
}

// applyPieHover：饼图扇区 hover（type=null 恢复默认）；图例为纯静态展示不参与交互。
// 强调方式克制：悬停扇区 stroke 加粗、其余扇区降透明；中心数字切为该类（次数 + 标签 + 百分比）。
// 无动画位移，仅透明度/描边宽过渡。
function applyPieHover(type) {
  const svg = document.querySelector(".device-pie-svg");
  if (!svg) return;
  const total = Number(svg.dataset.total) || 0;
  const segs = svg.querySelectorAll(".pie-seg");
  if (!type) {
    svg.classList.remove("has-hover");
    segs.forEach((s) => s.classList.remove("is-hover"));
    svg.querySelector(".pie-total").textContent = total.toLocaleString();
    svg.querySelector(".pie-total-label").textContent = t("device.clients");
    return;
  }
  const seg = svg.querySelector(`.pie-seg[data-type="${type}"]`);
  if (!seg) return;
  svg.classList.add("has-hover");
  segs.forEach((s) => s.classList.toggle("is-hover", s === seg));
  svg.querySelector(".pie-center-hover .pie-total").textContent = seg.dataset.count;
  svg.querySelector(".pie-center-hover .pie-total-label").textContent = `${seg.dataset.label} ${seg.dataset.pct}%`;
}

// onChartPointerMove：曲线 hover 吸附最近槽位——垂直指示线 + 数据点圆圈 + tooltip（跟随指针、边缘翻转）。
// 显隐由容器 .is-hovering class 驱动（CSS 过渡）；坐标/文本实时更新不参与过渡。窗口 resize 后由轮询重建自然刷新。
function onChartPointerMove(e) {
  const ctx = state.stats.chart;
  const chart = $("stat-timeline-chart");
  const svg = chart.querySelector("svg");
  const line = chart.querySelector(".hover-line");
  const dot = chart.querySelector(".hover-dot");
  const tip = chart.querySelector(".timeline-tip");
  if (!ctx || !svg || !line || !dot || !tip) return;
  const rect = svg.getBoundingClientRect();
  if (rect.width === 0 || e.clientX < rect.left || e.clientX > rect.right) return;
  chart.classList.add("is-hovering");
  const { slots, W, H } = ctx;
  const n = slots.length;
  const vx = ((e.clientX - rect.left) / rect.width) * W; // 像素 → viewBox x
  const i = Math.min(Math.max(Math.round((vx / W) * (n - 1)), 0), n - 1); // 吸附最近桶
  const slot = slots[i];
  const x = n <= 1 ? 0 : (i / (n - 1)) * W;
  const peak = Math.max(...slots.map((p) => p.v), 1);
  const y = H - (slot.v / peak) * (H - 6) - 2;
  line.setAttribute("x1", x.toFixed(2));
  line.setAttribute("x2", x.toFixed(2));
  dot.setAttribute("cx", x.toFixed(2));
  dot.setAttribute("cy", y.toFixed(2));
  tip.textContent = t("stats.hoverTip", fmtHM(slot.t), slot.v.toLocaleString());
  // tooltip 定位：默认点右上侧，靠右缘翻到左侧、靠顶翻到下方（先展示后测量尺寸；
  // left/top 为跟随指针的位置更新，无过渡，不属动画属性）
  const px = e.clientX - chart.getBoundingClientRect().left;
  const py = e.clientY - chart.getBoundingClientRect().top;
  const tipW = tip.offsetWidth;
  const tipH = tip.offsetHeight;
  const flipX = px + 12 + tipW > chart.clientWidth;
  tip.style.left = `${Math.max(0, flipX ? px - tipW - 12 : px + 12)}px`;
  tip.style.top = `${py - tipH - 10 < 0 ? py + 14 : py - tipH - 10}px`;
}

function onChartPointerLeave() {
  $("stat-timeline-chart")?.classList.remove("is-hovering");
}

// renderRecent：最近请求紧凑表格（新在上）。轮询每 2s 重建 DOM，保留用户滚动位置。
function renderRecent(recent) {
  const sec = $("stat-recent-sec");
  const wrap = $("stat-recent");
  const rows = [...recent]
    .filter((r) => r && r.t)
    .sort((a, b) => new Date(b.t) - new Date(a.t))
    .slice(0, 200); // 兑底截断：极端批量下限制 DOM 规模（后端通常已限量）
  if (!rows.length) {
    sec.hidden = true;
    return;
  }
  sec.hidden = false;
  const keepScroll = wrap.scrollTop;
  wrap.innerHTML = `
    <table class="recent-table">
      <thead><tr><th>${esc(t("stats.colTime"))}</th><th>${esc(t("stats.colService"))}</th><th>${esc(t("stats.colServer"))}</th><th>${esc(t("stats.colMethod"))}</th><th>${esc(t("stats.colPath"))}</th><th class="th-num">${esc(t("stats.colStatus"))}</th><th class="th-num">${esc(t("stats.colDuration"))}</th><th class="th-num">${esc(t("stats.colTraffic"))}</th></tr></thead>
      <tbody>
        ${rows
          .map((r) => {
            const svc = svcDisplayName({ serviceID: r.serviceID });
            // 服务器名取事件发生时记录的目标（RecentRequest.serverName），非当前配置映射
            const srvName = String(r.serverName ?? "").trim() || "—";
            const path = String(r.path ?? "");
            const bytesIn = fmtBytes(r.bytesIn ?? 0);
            const bytesOut = fmtBytes(r.bytesOut ?? 0);
            return `<tr>
              <td class="mono td-time">${esc(fmtClock(r.t))}</td>
              <td class="td-svc" title="${escAttr(svc)}">${esc(svc)}</td>
              <td class="td-srv" title="${escAttr(srvName)}">${esc(srvName)}</td>
              <td class="mono td-method">${esc(r.method ?? "")}</td>
              <td class="td-path" title="${escAttr(path)}">${esc(path)}</td>
              <td class="mono td-num st-${statusClassKey(r.status)}">${esc(String(r.status ?? ""))}</td>
              <td class="mono td-num">${esc(fmtMs(r.durationMs ?? 0))}</td>
              <td class="mono td-num td-bytes" title="${escAttr(t("stats.bytesTip", bytesIn, bytesOut))}">${esc(bytesIn)} / ${esc(bytesOut)}</td>
            </tr>`;
          })
          .join("")}
      </tbody>
    </table>`;
  wrap.scrollTop = keepScroll; // 轮询重建后不丢失用户滚动位置
}

// ---------- 删除/操作确认（应用内确认弹窗）----------

// mountConfirm(panelHTML, resolve)：confirm 弹窗公共外壳——注入面板 HTML、进出场过渡、
// 按钮与 overlay 点击、Escape 取消。供 askConfirm（一行文本）与 askUpdateConfirm（更新
// 日志）复用；Promise 只 resolve 一次；done 时同步解绑 Escape 监听，避免残留监听在
// 后续 Escape 误触发。
function mountConfirm(panelHTML, resolve) {
  const overlay = document.createElement("div");
  overlay.className = "confirm";
  overlay.innerHTML = `<div class="confirm-overlay"></div>${panelHTML}`;
  document.body.appendChild(overlay);
  void overlay.offsetWidth; // 提交初始态后再过渡
  overlay.classList.add("is-open");
  let settled = false;
  const onKey = (e) => {
    if (e.key === "Escape" && overlay.isConnected) done(false);
  };
  const done = (val) => {
    if (settled) return;
    settled = true;
    document.removeEventListener("keydown", onKey);
    overlay.classList.remove("is-open");
    setTimeout(() => overlay.remove(), 300);
    resolve(val);
  };
  overlay.addEventListener("click", (e) => {
    const b = e.target.closest("button[data-r]");
    if (b) done(b.dataset.r === "1");
    else if (e.target.classList.contains("confirm-overlay")) done(false);
  });
  document.addEventListener("keydown", onKey);
}

// askConfirm(message, opts)：通用一行文本确认（删除/退出等场景），行为不变。
// opts.confirmText 自定义确认按钮文案（默认「删除」，不影响删除场景）。
function askConfirm(message, opts = {}) {
  return new Promise((resolve) => {
    mountConfirm(
      `
      <div class="confirm-panel">
        <p class="confirm-text">${esc(message)}</p>
        <div class="form-actions">
          <button class="btn" data-r="0">${esc(t("generic.cancel"))}</button>
          <button class="btn ${opts.confirmClass ?? "btn-primary"}" data-r="1">${esc(opts.confirmText ?? t("generic.delete"))}</button>
        </div>
      </div>
    `,
      resolve,
    );
  });
}

// ---------- 模态（新增/编辑服务）----------

let modalReturnFocus = null;

// ---------- 服务图标选择（弹窗标题行右侧 emoji picker）----------

// modalIcon：当前模态内的选中图标；空串 = 已清除（展示按类型兜底，提交 icon:""）
let modalIcon = "";
let iconPopoverOpen = false;
let iconGridBuilt = false;

// syncIconBtn：触发按钮展示当前生效图标（选中值或类型兜底）；popover 开着则同步网格高亮
function syncIconBtn() {
  const type = $("service-form").elements.type.value;
  $("btn-icon-pick").textContent = modalIcon || ICON_FALLBACK[type] || ICON_FALLBACK.http;
  if (iconPopoverOpen) syncIconGrid();
}

// buildIconGridOnce：精选网格内容静态，首次打开时构建一次，后续仅同步高亮
function buildIconGridOnce() {
  if (iconGridBuilt) return;
  iconGridBuilt = true;
  $("icon-grid").innerHTML = EMOJI_CHOICES.map(
    (e) => `<button type="button" class="icon-cell" data-icon="${e}" role="option" title="${e}">${e}</button>`,
  ).join("");
}

function syncIconGrid() {
  for (const cell of $("icon-grid").children) {
    const on = cell.dataset.icon === modalIcon;
    cell.classList.toggle("is-active", on);
    cell.setAttribute("aria-selected", String(on));
  }
}

function openIconPopover() {
  if (iconPopoverOpen) return;
  buildIconGridOnce();
  syncIconGrid();
  const pop = $("icon-popover");
  pop.hidden = false;
  void pop.offsetHeight; // 提交初始态后再过渡（对齐 modal 打开习惯）
  pop.classList.add("is-open");
  iconPopoverOpen = true;
  $("btn-icon-pick").setAttribute("aria-expanded", "true");
}

// closeIconPopover 关闭 popover，返回是否原本处于打开态（供 Esc 分流：先关 popover 再关模态）。
// immediate 用于模态关闭/重开时同步隐藏，不走退场过渡。
function closeIconPopover(immediate) {
  if (!iconPopoverOpen) return false;
  iconPopoverOpen = false;
  const pop = $("icon-popover");
  pop.classList.remove("is-open");
  if (immediate) {
    pop.hidden = true;
  } else {
    setTimeout(() => {
      pop.hidden = true;
    }, 160);
  }
  $("btn-icon-pick").setAttribute("aria-expanded", "false");
  return true;
}

// 类型决定协议选择器/字段组显隐 + 目标 Frps 合格列表：HTTP 由入口固定；
// 端口服务在弹窗内 TCP/UDP 可切换（默认 TCP），字段组共用远程端口。
// opts.preserveServer：弹窗内协议切换时保留本次已选目标（仅重建列表，不重置手动选择）；
// 首次打开弹窗不传，沿用既有初始化/历史选择逻辑。
function setServiceType(t, opts = {}) {
  const form = $("service-form");
  form.elements.type.value = t;
  // ctx.type 跟随实际表单：切语言重算标题/预览时以当前协议为准（编辑 TCP→UDP 后标题回 UDP）
  if (state.modalCtx?.kind === "service") state.modalCtx.type = t;
  document.querySelector('[data-fields="http"]').hidden = t !== "http";
  document.querySelector('[data-fields="port"]').hidden = !isPortType(t);
  document.querySelector('[data-fields="proto"]').hidden = t === "http"; // 协议选择仅端口服务可见
  const protoSel = $("svc-proto-select");
  if (protoSel) protoSel.value = isPortType(t) ? t : "tcp"; // http 时隐藏且不参与提交，值无意义
  syncIconBtn(); // 兜底图标随类型走（未选图标时 http 🌐 / tcp 🔌 / udp 📡）
  syncServerSelect(t, Boolean(opts.preserveServer)); // 合格目标列表随类型变化（HTTP 仅 httpEnabled；TCP/UDP 全部）
}

// syncServerSelect：填充目标 Frps 下拉。编辑保留原归属（即使已不在合格列表，如 HTTP 被关）；
// 新建：唯一合格自动选，多项记住上次选择（localStorage），无合格则禁用并提示去配置页。
function syncServerSelect(type, preserve = false) {
  const form = $("service-form");
  const sel = form.elements.serverId;
  const servers = configServers();
  const eligible = eligibleServers(type, servers);
  const hint = $("svc-move-hint");
  if (hint) hint.hidden = true;
  const selected = sel.value; // 重建 options 前捕获已选（协议切换保留本次手动选择用）
  const currentId = state.editingOrigServerId ?? "";
  const currentSrv = serverById(currentId);
  let options = eligible.slice();
  if (currentSrv && !options.some((s) => s.id === currentId)) {
    options = [currentSrv, ...options]; // 编辑保留归属可选项（标「当前」）
  }
  if (!options.length) {
    sel.innerHTML = `<option value="">${esc(t(type === "http" ? "pick.noHttpServer" : "pick.noServer"))}</option>`;
    sel.value = "";
    sel.disabled = true;
    return;
  }
  sel.disabled = false;
  sel.innerHTML = options
    .map((s) => `<option value="${escAttr(s.id)}">${esc(s.name || serverConnAddr(s))}${state.editingId && s.id === currentId ? esc(t("pick.currentSuffix")) : ""}</option>`)
    .join("");
  let picked = "";
  if (preserve) {
    // 语言切换/协议切换重刷文案：原样保留当前选择；未选择（多项待选）保持为空，
    // 不套用历史记忆，避免切换语言时静默改目标。仅当已选值不在合格列表时走默认逻辑。
    if (selected && options.some((s) => s.id === selected)) {
      picked = selected;
    } else if (currentId && options.some((s) => s.id === currentId)) {
      picked = currentId; // 编辑：保留归属（含原选择为空场景）
    } else if (!selected && options.length === 1) {
      picked = options[0].id; // 唯一合格目标自动选
    }
  } else if (currentId && options.some((s) => s.id === currentId)) {
    picked = currentId; // 编辑：保留归属
  } else if (options.length === 1) {
    picked = options[0].id; // 唯一合格目标自动选
  } else {
    const last = lastServerFor(type);
    if (last && options.some((s) => s.id === last)) picked = last; // 多项记住上次
  }
  sel.value = picked;
  updateMoveHint();
}

// updateMoveHint：目标选择时的地址预览与迁移提示（仅提示，保存前不触后端）。
// 编辑且目标 ≠ 原归属时追加「短暂中断」提醒。
function updateMoveHint() {
  const form = $("service-form");
  const sel = form.elements.serverId;
  const hint = $("svc-move-hint");
  if (!sel.value) {
    hint.hidden = true;
    return;
  }
  const type = form.elements.type.value;
  const isPort = isPortType(type);
  const srv = serverById(sel.value);
  const draft = {
    type,
    subdomain: form.elements.subdomain.value.trim(),
    remotePort: Number(form.elements.remotePort.value) || 0,
  };
  const url = svcPublicURL(draft, srv);
  const parts = [];
  if (url) parts.push(t("hint.addr", url));
  else if (isPort) parts.push(t("hint.fillRemotePort"));
  else if (draft.subdomain) parts.push(t("hint.httpDepends"));
  else {
    // HTTP 空前缀：后端保存时自动生成子域名，预览不拼非法 URL
    const scheme = srv && srv.scheme === "http" ? "http" : "https";
    const base = (srv && String(srv.baseDomain ?? "").trim()) || t("hint.baseDomainFallback");
    const port = srv && Number(srv.publicPort) > 0 ? `:${srv.publicPort}` : "";
    parts.push(t("hint.autoSubdomain", scheme, t("hint.subdomainPlaceholder"), base, port));
  }
  if (state.editingId && sel.value !== (state.editingOrigServerId ?? "")) {
    parts.push(t("hint.moveInterrupt"));
  }
  hint.textContent = parts.join(t("hint.join"));
  hint.hidden = false;
}

function openServiceModal(title, svc, typeOverride) {
  state.editingId = svc ? svc.id : null;
  state.editingOrigServerId = svc?.serverId ?? "";
  // 顺序关键：先回填字段值再切类型——setServiceType 会驱动目标列表与地址预览，
  // 预览必须读到本次打开的 subdomain/remotePort，而不是上一次打开的表单残留
  const type = typeOverride ?? ((svc ? svc.type : "http") || "http");
  // 语言切换时重算动态标题用（title 参数保留旧调用方兼容，不再作为唯一来源）
  state.modalCtx = { kind: "service", type, editing: Boolean(svc) };
  $("modal-title").textContent = svc
    ? t("modal.editService", svcTypeLabel(type))
    : type === "http"
      ? t("action.addHttp")
      : t("action.addPort");
  const form = $("service-form");
  form.elements.name.value = svc ? svc.name : "";
  form.elements.localHost.value = svc ? svc.localHost : "127.0.0.1";
  form.elements.localPort.value = svc ? svc.localPort : "";
  form.elements.remotePort.value = svc && svc.remotePort > 0 ? svc.remotePort : "";
  form.elements.subdomain.value = svc ? svc.subdomain : "";
  // 图标：编辑回显现有 icon；新建预填随机 emoji（可改）
  modalIcon = svc ? String(svc.icon ?? "").trim() : randomEmoji();
  closeIconPopover(true);
  setServiceType(type);

  const modal = $("service-modal");
  modalReturnFocus = document.activeElement;
  modal.hidden = false;
  void modal.offsetHeight; // 提交初始态，过渡可靠
  modal.classList.add("is-open");
  form.elements.name.focus();
}

function closeServiceModal() {
  const modal = $("service-modal");
  if (modal.hidden) return;
  closeIconPopover(true); // 模态关闭时 popover 同步收起，避免残留到下次打开
  modal.classList.remove("is-open");
  const panel = modal.querySelector(".modal-panel");
  let hidden = false;
  const hide = () => {
    if (hidden) return;
    hidden = true;
    modal.hidden = true;
    panel.removeEventListener("transitionend", hide);
  };
  panel.addEventListener("transitionend", hide);
  setTimeout(hide, 400); // 兜底
  if (modalReturnFocus && modalReturnFocus.focus) modalReturnFocus.focus();
  modalReturnFocus = null;
  state.editingId = null;
  state.modalCtx = null;
}

// ---------- Frps 配置模态（新增/编辑服务器；HTTP 复选切换字段组，隐藏时保留已填值） ----------

// toggleServerHttpFields：HTTP 能力开关联动字段组显隐 + 根域名必填切换；
// 关闭时仅隐藏，字段值保留（提交原值，后端保留语义）。
function toggleServerHttpFields(enabled) {
  const form = $("server-form");
  const box = form.elements.httpEnabled;
  const fields = $("srv-http-fields");
  fields.hidden = !enabled;
  box.setAttribute("aria-expanded", String(Boolean(enabled)));
  // 关闭：折叠保留值，字段置 disabled 避免隐藏的必填项阻止提交；开启：恢复可编辑
  form.elements.baseDomain.required = Boolean(enabled);
  form.elements.baseDomain.disabled = !enabled;
  form.elements.scheme.disabled = !enabled;
  form.elements.publicPort.disabled = !enabled;
}

function openServerModal(srv) {
  state.serverEditingId = srv ? srv.id : null;
  state.modalCtx = { kind: "server", editing: Boolean(srv) };
  $("server-modal-title").textContent = srv ? t("modal.editServer") : t("modal.addServer");
  const form = $("server-form");
  form.elements.name.value = srv ? srv.name : "";
  form.elements.serverAddr.value = srv ? srv.serverAddr : "";
  form.elements.serverPort.value = srv ? srv.serverPort : 7000;
  form.elements.authToken.value = srv ? srv.authToken : "";
  form.elements.httpEnabled.checked = srv ? Boolean(srv.httpEnabled) : false;
  form.elements.baseDomain.value = srv ? srv.baseDomain : "";
  form.elements.scheme.value = srv && srv.scheme === "http" ? "http" : "https";
  form.elements.publicPort.value = srv && Number(srv.publicPort) > 0 ? srv.publicPort : "";
  toggleServerHttpFields(form.elements.httpEnabled.checked);

  state.serverReturnFocus = document.activeElement;
  const modal = $("server-modal");
  modal.hidden = false;
  void modal.offsetHeight; // 提交初始态，过渡可靠
  modal.classList.add("is-open");
  form.elements.name.focus();
}

function closeServerModal() {
  const modal = $("server-modal");
  if (modal.hidden) return;
  modal.classList.remove("is-open");
  const panel = modal.querySelector(".modal-panel");
  let hidden = false;
  const hide = () => {
    if (hidden) return;
    hidden = true;
    modal.hidden = true;
    panel.removeEventListener("transitionend", hide);
  };
  panel.addEventListener("transitionend", hide);
  setTimeout(hide, 400); // 兜底
  if (state.serverReturnFocus?.focus) state.serverReturnFocus.focus();
  state.serverReturnFocus = null;
  state.serverEditingId = null;
  state.modalCtx = null;
}

// ---------- 访问二维码弹窗（卡片二维码按钮打开）----------

// qrCache：url → data URL。二维码内容只取决于地址，命中缓存免再走后端生成
const qrCache = new Map();

async function openQrModal(url) {
  if (!url) return;
  const modal = $("qr-modal");
  const box = $("qr-box");
  $("qr-url").textContent = url;
  modal.hidden = false;
  void modal.offsetHeight; // 提交初始态后再过渡（对齐其他模态）
  modal.classList.add("is-open");

  const cached = qrCache.get(url);
  if (cached) {
    box.innerHTML = `<img src="${cached}" alt="${escAttr(t("qr.imgAlt"))}" />`;
    return;
  }
  box.innerHTML = `<span class="qr-loading">${esc(t("qr.loading"))}</span>`;
  try {
    const dataUrl = await run("run.genQr", () => api.AppService.QRCode(url));
    qrCache.set(url, dataUrl);
    // 迟到回填防串台：弹窗仍开着且展示的就是这个地址才写入
    if (!modal.hidden && $("qr-url").textContent === url) {
      box.innerHTML = `<img src="${dataUrl}" alt="${escAttr(t("qr.imgAlt"))}" />`;
    }
  } catch {
    if (!modal.hidden && $("qr-url").textContent === url) {
      box.innerHTML = `<span class="qr-loading">${esc(t("qr.fail"))}</span>`;
    }
  }
}

function closeQrModal() {
  const modal = $("qr-modal");
  if (modal.hidden) return;
  modal.classList.remove("is-open");
  const panel = modal.querySelector(".modal-panel");
  let hidden = false;
  const hide = () => {
    if (hidden) return;
    hidden = true;
    modal.hidden = true;
    panel.removeEventListener("transitionend", hide);
  };
  panel.addEventListener("transitionend", hide);
  setTimeout(hide, 400); // 兜底
}

// ---------- 应用更新 ----------
// 契约：UpdateInfo()/CheckForUpdates() 返回 { currentVersion, latestVersion, state, message, progress }；
// app:update 事件数据同构；InstallUpdate() 触发安装并重启。state ∈ idle/checking/up-to-date/
// available/downloading/verifying/installing/ready/error/disabled，progress 0..100。

const UPDATE_BUSY_STATES = new Set(["downloading", "verifying", "installing"]);
const UPDATE_CHIP_TEXT = {
  available: "update.chip",
  ready: "update.chipReady",
  error: "update.chipRetry", // latestVersion 保留时可点重试
  downloading: "update.chipDownloading",
  verifying: "update.chipVerifying",
  installing: "update.chipInstalling",
};

// 三分支确认文案：标题（状态语义 + 新版本号）、降级一行文案、按钮文案，
// 语义沿用既有确认框文案不改词；notes 非空走更新日志弹窗，否则用 message。
const UPDATE_CONFIRM = {
  available: {
    title: (v) => t("update.found", v),
    message: (v, cur) => t("update.confirmAvailableMsg", v, cur),
    confirmText: () => t("update.confirmNow"),
  },
  ready: {
    title: (v) => t("update.confirmReadyTitle", v),
    message: (v) => t("update.confirmReadyMsg", v),
    confirmText: () => t("update.chipReady"),
  },
  error: {
    title: (v) => t("update.confirmErrorTitle", v),
    message: (v) => t("update.confirmErrorMsg", v),
    confirmText: () => t("update.chipRetry"),
  },
};

// applyUpdateInfo：唯一渲染入口（fetch 与事件都走这里），seq 单调递增。
// 事件只渲染不弹 toast：结果反馈由手动 RPC 返回与 InstallUpdate catch 各负责一次，
// 后台检查/下载失败不打扰用户（仅渲染状态）。
function applyUpdateInfo(info) {
  if (!info || typeof info !== "object" || !info.state) return;
  state.update.seq += 1;
  state.update.info = JSON.parse(JSON.stringify(info));
  renderUpdate();
}

// 启动时读一次 UpdateInfo；失败仅记后端日志（更新功能不可用不阻塞主界面）
async function fetchInitUpdateInfo() {
  const seqBefore = state.update.seq;
  const info = await tryFetch("UpdateInfo", () => api.AppService.UpdateInfo(), 2);
  if (state.update.seq !== seqBefore) {
    try {
      await api.AppService.FrontendLog("info", "UpdateInfo fetch resolved after an app:update event arrived; discarding the stale response");
    } catch {}
    return;
  }
  applyUpdateInfo(info);
}

// 手动检查更新：忙态防重复（含 InstallUpdate 挂起中，防检查覆盖安装）；
// 结果 toast 永远由 RPC 返回负责一次（不看 seq，避免「checking 事件先到、
// available 事件晚于 RPC 返回」时无反馈）；seq 仅保护渲染：事件已先到则不覆盖渲染，
// 但 toast 照常给出。app:update 事件只渲染不 toast，后台检查/失败不打扰。
async function checkForUpdates() {
  if (state.updateChecking || state.updateApplying || state.update.info?.state === "checking" || UPDATE_BUSY_STATES.has(state.update.info?.state)) return;
  state.updateChecking = true;
  renderCheckButton();
  const seqBefore = state.update.seq;
  try {
    const info = await api.AppService.CheckForUpdates();
    if (state.update.seq === seqBefore) applyUpdateInfo(info); // 事件已先到则不覆盖渲染
    if (info?.state === "available") toast("success", t("update.found", info.latestVersion));
    else if (info?.state === "ready") toast("info", t("update.readyToast"));
    else if (info?.state === "up-to-date") toast("info", t("update.upToDateToast", info.currentVersion));
    else if (info?.state === "error") toast("error", t("update.checkFail", info.message || t("update.unknownReason")));
    // disabled/idle 等异常态不 toast，仅渲染
  } catch (err) {
    toast("error", t("update.checkFail", err.message ?? err));
  } finally {
    state.updateChecking = false;
    renderCheckButton();
  }
}

function renderCheckButton() {
  const btn = $("btn-check-update");
  if (!btn) return;
  const busyUpdate = state.updateApplying || state.update.info?.state === "checking" || UPDATE_BUSY_STATES.has(state.update.info?.state);
  // 手动检查中或后台下载/安装中均禁点：防检查覆盖安装进行中的状态
  btn.disabled = state.updateChecking || busyUpdate;
  btn.classList.toggle("is-busy", state.updateChecking);
  btn.title = state.updateChecking ? t("update.checking") : busyUpdate ? t("update.busy") : t("update.check");
}

// renderUpdate：顶栏展示「frpdesk 当前版本」；更新 chip 可见性以 latestVersion +
// 可安装状态为准（available/ready/error 可点，busy 可见禁点，其余隐藏）。
// chip 状态不变时只改文字，避免重建 DOM 打断 spinner 旋转动画。
function renderUpdate() {
  const info = state.update.info;
  const chip = $("btn-update-apply");
  if (info?.currentVersion) $("app-version").textContent = `frpdesk ${info.currentVersion}`;
  const st = info?.state;
  const busy = UPDATE_BUSY_STATES.has(st);
  const visible = Boolean(info?.latestVersion) && (busy || st === "available" || st === "ready" || st === "error");
  if (!visible) {
    chip.hidden = true;
    chip.dataset.state = "";
    renderCheckButton();
    return;
  }
  chip.hidden = false;
  chip.disabled = busy || state.updateConfirming || state.updateApplying;
  chip.classList.toggle("is-busy", busy);
  if (chip.dataset.state !== st) {
    chip.dataset.state = st;
    const icon = busy ? ICONS.loader : st === "ready" ? ICONS.refresh : ICONS.download;
    chip.innerHTML = `${icon}<span class="update-chip-text"></span>`;
  }
  let text = UPDATE_CHIP_TEXT[st] ? t(UPDATE_CHIP_TEXT[st]) : t("update.chip");
  if (st === "downloading" && Number.isFinite(info.progress)) text = t("update.chipDownloadingPct", Math.round(info.progress));
  chip.querySelector(".update-chip-text").textContent = text;
  chip.title = busy ? t("update.tipBusy") : st === "error" ? t("update.tipRetryTo", info.latestVersion) : t("update.tipTo", info.latestVersion);
  chip.setAttribute("aria-label", chip.title);
  renderCheckButton();
}

// ---------- 迷你 Markdown 渲染（更新日志专用）----------

// renderMarkdown(md)：约定语法子集 → 安全 HTML。子集：`## ` 标题 → h4；`- `/`* `
// 连续行 → ul/li；空行分段；其余行并入 p（段内换行以 <br> 合并为一段）。
// 每一行先经 esc() 转义再识别结构拼 HTML，`<script>` 等只会作为文本展示；
// 其余语法（代码块、加粗等）不解析，原样作为段落文本。
function renderMarkdown(md) {
  const out = [];
  let list = null; // 聚集中的列表项，遇空行/非列表行/结尾收拢为 <ul>
  let para = []; // 聚集中的段落行，遇空行/结构行/结尾收拢为 <p>
  const flushList = () => {
    if (!list) return;
    out.push(`<ul>${list.map((li) => `<li>${li}</li>`).join("")}</ul>`);
    list = null;
  };
  const flushPara = () => {
    if (!para.length) return;
    out.push(`<p>${para.join("<br>")}</p>`);
    para = [];
  };
  for (const raw of String(md ?? "").split(/\r?\n/)) {
    const line = esc(raw).replace(/\s+$/, ""); // 先转义再识别结构（#、-、* 不受转义影响）
    if (!line.trim()) {
      flushList();
      flushPara();
    } else if (/^##\s/.test(line)) {
      flushList();
      flushPara();
      out.push(`<h4>${line.replace(/^##\s+/, "")}</h4>`);
    } else if (/^[-*]\s/.test(line)) {
      flushPara();
      if (!list) list = [];
      list.push(line.replace(/^[-*]\s+/, ""));
    } else {
      flushList();
      para.push(line);
    }
  }
  flushList();
  flushPara();
  return out.join("");
}

// askUpdateConfirm：更新日志确认弹窗（notes 非空时走这里，不影响通用 askConfirm）。
// 标题区 = 状态语义文案 + 新版本号，副行 = 当前版本 + 重启断开提示；内容区 =
// 渲染后的更新日志（超高滚动不撑爆弹窗）；按钮区 = 取消 + 分支确认按钮。
// 交互与 askConfirm 一致：取消/overlay 点击/Escape 都 resolve(false)，确认 resolve(true)，
// 焦点不抢占（跟随现有确认框模式）。
function askUpdateConfirm({ title, meta, notes, confirmText }) {
  return new Promise((resolve) => {
    mountConfirm(
      `
      <div class="confirm-panel confirm-panel--notes">
        <div class="update-confirm-head">
          <h3 class="update-confirm-title">${esc(title)}</h3>
          <p class="update-confirm-meta">${esc(meta)}</p>
        </div>
        <div class="update-confirm-notes" role="region" aria-label="${escAttr(t("update.notesAria"))}">${renderMarkdown(notes)}</div>
        <div class="form-actions">
          <button class="btn" data-r="0">${esc(t("generic.cancel"))}</button>
          <button class="btn btn-primary" data-r="1">${esc(confirmText)}</button>
        </div>
      </div>
    `,
      resolve,
    );
  });
}

// 更新 chip 点击：available/ready/error 先二次确认——notes 非空走更新日志弹窗，
// 为空/缺失（旧包/更新源未提供日志）降级为原一行文案确认；取消不安装；
// updateConfirming/updateApplying 本地标志防双击多弹窗与重复 RPC。
// InstallUpdate 为同步等待下载+重启完成的 RPC（JS Promise 挂起至真实完成，
// 事件继续推送进度不锁前端）：updateApplying 持续到 RPC 返回，catch 可靠提示
// 下载/重启失败（事件只渲染，不会重复 toast）。
async function applyUpdateClick() {
  if (state.updateConfirming || state.updateApplying) return;
  const info = state.update.info;
  if (!info?.latestVersion || !["available", "ready", "error"].includes(info.state)) return;
  const target = info.latestVersion;
  const text = UPDATE_CONFIRM[info.state];
  const notes = typeof info.notes === "string" ? info.notes.trim() : "";
  state.updateConfirming = true;
  renderUpdate();
  let ok = false;
  try {
    ok = notes
      ? await askUpdateConfirm({
          title: text.title(target),
          meta: `${info.currentVersion ? t("update.metaCurrent", info.currentVersion) : ""}${t("update.metaRestart")}`,
          notes,
          confirmText: text.confirmText(),
        })
      : await askConfirm(text.message(target, info.currentVersion), { confirmText: text.confirmText() });
  } finally {
    state.updateConfirming = false;
    renderUpdate();
  }
  if (!ok) return; // 取消：不安装
  // 确认后再次验证：版本仍是确认时的那个、状态不忙，才发起安装
  const cur = state.update.info;
  if (!cur || cur.latestVersion !== target || !["available", "ready", "error"].includes(cur.state)) return;
  state.updateApplying = true;
  renderUpdate();
  try {
    await api.AppService.InstallUpdate(); // 挂起至下载+重启完成；进度经 app:update 事件渲染
  } catch (err) {
    toast("error", t("update.applyFail", err.message ?? err)); // 下载/重启的实际错误，一次可靠提示
  } finally {
    state.updateApplying = false;
    renderUpdate();
  }
}

// ---------- 事件绑定 ----------

function bindEvents() {
  // WKWebView(macOS)：输入框聚焦时，首次点击按钮的 click 会被原生「取消聚焦」吞掉
  // （表现为取消/保存等要点两次）。捕获阶段提前 blur 活动输入框，让 click 正常派发。
  // 目标本身是表单控件或其 label 时跳过（焦点自然转移，避免同控件闪烁）。
  document.addEventListener(
    "pointerdown",
    (e) => {
      const active = document.activeElement;
      if (!active || !/^(INPUT|TEXTAREA)$/.test(active.tagName)) return;
      if (e.target.closest("input, textarea, select, label")) return;
      active.blur();
    },
    true,
  );

  // WKWebView 兜底：原生吞点击时可能连 pointerdown 都不派发（上述修复无效的形态）。
  // 改为悬停即失焦：指针一旦悬停到可点元素（按钮/链接/开关/分段/遮罩）上就提前
  // blur 输入框——按下时文档已无焦点，click 不再被吞。仅悬停非表单区才触发，
  // 输入过程中鼠标在表单内移动不会误失焦。
  document.addEventListener("pointerover", (e) => {
    const active = document.activeElement;
    if (!active || !/^(INPUT|TEXTAREA)$/.test(active.tagName)) return;
    if (!e.target.closest("button, a, .switch, .modal-overlay, .confirm-overlay")) return;
    active.blur();
  });

  document.querySelector(".tabbar").addEventListener("click", (e) => {
    const tab = e.target.closest(".tab");
    if (tab) switchTab(tab.dataset.tab);
  });

  // 退出程序：隧道运行/连接中（frpc 存活）才二次确认，未启动直接退出
  $("btn-quit").addEventListener("click", () => {
    // 依据 activeCount（starting/running/stopping 实例数）判断：聚合 state 在部分失败等
    // partial 状态下会漏确认
    const activeCount = Number(state.snapshot?.status?.activeCount ?? 0);
    const quit = () => api.AppService.QuitApp().catch(() => {});
    if (activeCount <= 0) {
      quit();
      return;
    }
    askConfirm(t("confirm.quitMsg"), {
      confirmText: t("action.quitConfirm"),
      confirmClass: "btn-danger",
    }).then((ok) => {
      if (ok) quit();
    });
  });

  // 访问二维码弹窗：遮罩点击关闭
  document
    .querySelectorAll("[data-close-qr]")
    .forEach((el) => el.addEventListener("click", closeQrModal));
  // Escape：popover 优先关闭，再各模态（互斥，不会同时打开）
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    if (closeIconPopover()) return;
    if (!$("service-modal").hidden) closeServiceModal();
    else if (!$("server-modal").hidden) closeServerModal();
    else if (!$("qr-modal").hidden) closeQrModal();
  });

  // Frps 配置模态：新增/编辑/HTTP 能力切换/提交（成功关模态，失败保持打开，错误 toast 由 run() 给出）
  $("btn-add-server").addEventListener("click", () => openServerModal(null));
  document.querySelectorAll("[data-close-server]").forEach((el) => el.addEventListener("click", closeServerModal));
  $("server-form").elements.httpEnabled.addEventListener("change", (e) => {
    toggleServerHttpFields(e.target.checked); // 仅 change 驱动；label 原生切换不手动 click
  });
  $("server-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const form = $("server-form");
    const httpEnabled = form.elements.httpEnabled.checked;
    const input = {
      name: form.elements.name.value.trim(),
      serverAddr: form.elements.serverAddr.value.trim(),
      serverPort: Number(form.elements.serverPort.value) || 0,
      authToken: form.elements.authToken.value,
      httpEnabled,
      // 关闭 HTTP 时隐藏但保留已填字段值（提交原值，由后端保留语义）
      baseDomain: form.elements.baseDomain.value.trim(),
      scheme: form.elements.scheme.value === "http" ? "http" : "https",
      publicPort: Number(form.elements.publicPort.value) || 0,
    };
    try {
      const wasEditing = Boolean(state.serverEditingId);
      const snapshot = wasEditing
        ? await run("run.saveConfig", () => api.AppService.UpdateServer(state.serverEditingId, input))
        : await run("run.createConfig", () => api.AppService.CreateServer(input));
      closeServerModal();
      applyConfigOnly(snapshot);
      toast("success", wasEditing ? t("toast.configUpdated") : t("toast.configAdded"));
    } catch {
      // 失败原因已由 run() toast（如名称重复）：模态保持打开，输入不丢
    }
  });

  // 应用更新：手动检查（忙态防重复）+ 版本号右侧更新 chip（二次确认后安装）
  $("btn-check-update").addEventListener("click", checkForUpdates);
  $("btn-update-apply").addEventListener("click", applyUpdateClick);

  // 新增/编辑模态：HTTP 由入口固定；端口服务统一入口，弹窗内协议默认 TCP 可切换
  $("btn-add-http").addEventListener("click", () => openServiceModal(t("action.addHttp"), null, "http"));
  $("btn-add-port").addEventListener("click", () => openServiceModal(t("action.addPort"), null, "tcp"));
  // 弹窗内协议切换（TCP/UDP）：驱动字段组/图标兜底/地址预览刷新，并保留本次已选目标
  $("svc-proto-select").addEventListener("change", (e) => setServiceType(e.target.value, { preserveServer: true }));
  document.querySelectorAll("[data-close-modal]").forEach((el) => el.addEventListener("click", closeServiceModal));
  // 图标选择器：触发按钮开关 popover；网格点选即选中并关闭；随机/清除保持打开便于连续调整
  $("btn-icon-pick").addEventListener("click", () => (iconPopoverOpen ? closeIconPopover() : openIconPopover()));
  $("icon-grid").addEventListener("click", (e) => {
    const cell = e.target.closest(".icon-cell");
    if (!cell) return;
    modalIcon = cell.dataset.icon;
    syncIconBtn();
    closeIconPopover();
  });
  $("btn-icon-random").addEventListener("click", () => {
    modalIcon = randomEmoji();
    syncIconBtn();
  });
  // 点击 popover 外部关闭（.icon-pick-wrap 内的点击由触发按钮自身开关逻辑处理）
  document.addEventListener("pointerdown", (e) => {
    if (iconPopoverOpen && !e.target.closest(".icon-pick-wrap")) closeIconPopover();
  });
  $("service-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const form = $("service-form");
    const type = form.elements.type.value;
    const isPort = isPortType(type);
    // 先捕获编辑标记：closeServiceModal 会清 editingId，toast 依赖提交前的状态
    const wasEditing = Boolean(state.editingId);
    // 模态无 enabled 控件：创建默认启用；编辑保留该服务当前启用状态（用卡片开关切换）
    const editingSvc = wasEditing
      ? (state.snapshot?.config.services ?? []).find((s) => s.id === state.editingId)
      : null;
    // 目标 Frps 必选：无合格目标时引导去配置页，不产生无归属保存
    const serverId = form.elements.serverId.value;
    if (!serverId) {
      const hasTargets = eligibleServers(type, configServers()).length > 0;
      toast("error", hasTargets ? t("pick.noTargetToast") : isPort ? t("pick.noServerToast") : t("pick.noHttpServerToast"));
      return;
    }
    const input = {
      name: form.elements.name.value.trim(),
      localHost: form.elements.localHost.value.trim() || "127.0.0.1",
      localPort: Number(form.elements.localPort.value) || 0,
      subdomain: isPort ? "" : form.elements.subdomain.value.trim(),
      enabled: editingSvc ? editingSvc.enabled : true,
      type,
      remotePort: isPort ? Number(form.elements.remotePort.value) || 0 : 0,
      icon: modalIcon, // 空串 = 未设置，后端展示层按类型兜底
      serverId, // 目标 Frps；runtime 迁移失败由后端报错，原因提示后保留新归属不回滚
    };
    try {
      const snapshot = wasEditing
        ? await run("run.saveService", () => api.AppService.UpdateService(state.editingId, input))
        : await run("run.createService", () => api.AppService.CreateService(input));
      setLastServer(type, serverId); // 记住本次选择供下次新建（按类型分键：http/tcp/udp）
      closeServiceModal();
      applyConfigOnly(snapshot);
      toast("success", wasEditing ? t("toast.serviceUpdated") : t("toast.serviceAdded"));
    } catch {
      // 失败原因已由 run() toast 呈现（如 runtime 迁移失败）：模态保持打开，
      // 用户输入与新选择的归属均保留，不自动回滚
    }
  });

  // 服务器归属：切换目标或地址字段变化时刷新提示（新地址 / 短暂中断）
  $("svc-server-select").addEventListener("change", updateMoveHint);
  $("service-form").elements.subdomain.addEventListener("input", updateMoveHint);
  $("service-form").elements.remotePort.addEventListener("input", updateMoveHint);

  // 服务列表操作（事件委托）
  $("service-list").addEventListener("click", onServiceListClick);
  $("service-list").addEventListener("change", onServiceListToggle);
  $("service-list").addEventListener("pointerdown", onServiceListPointerDown); // 拖拽排序（仅 handle）

  // Frps 配置列表操作（事件委托：启停/编辑/删除）
  $("server-list").addEventListener("click", onServerListClick);

  // 统计图表 hover：容器级事件委托（轮询 innerHTML 重建子元素后监听仍有效）
  $("stat-timeline-chart").addEventListener("pointermove", onChartPointerMove);
  $("stat-timeline-chart").addEventListener("pointerleave", onChartPointerLeave);
  $("stat-devices").addEventListener("pointerover", (e) => {
    const seg = e.target.closest(".pie-seg");
    if (seg) applyPieHover(seg.dataset.type);
  });
  $("stat-devices").addEventListener("pointerout", (e) => {
    // 仅当真正离开容器（relatedTarget 不在容器内）才恢复默认；容器内移动由 over 接续处理
    const left = e.target.closest(".pie-seg");
    if (left && (!e.relatedTarget || !e.currentTarget.contains(e.relatedTarget))) applyPieHover(null);
  });

  // 日志：手动上滚暂停，回到底部恢复
  const logView = $("log-view");
  logView.addEventListener("scroll", () => {
    const nearBottom = logView.scrollHeight - logView.scrollTop - logView.clientHeight < 24;
    state.autoScroll = nearBottom;
    $("log-autoscroll").checked = nearBottom;
  });
  $("log-autoscroll").addEventListener("change", (e) => {
    state.autoScroll = e.target.checked;
    if (state.autoScroll) scrollLogToEnd(true);
  });
  $("btn-clear-logs").addEventListener("click", () => {
    state.logs = [];
    state.logIds.clear(); // 同步清 id 集：切回日志 tab 重新拉取可完整加载
    renderLogs();
    toast("info", t("toast.logsCleared"));
  });

  // TCP/UDP 地址点击复制（Wails 原生剪贴板；host:port 无协议不走浏览器）/ HTTP 链接系统浏览器打开
  document.addEventListener("click", async (e) => {
    const addr = e.target.closest(".addr-copy[data-copy]");
    if (addr) {
      // WKWebView 里 navigator.clipboard 受 secure context 限制，走 wails 原生剪贴板
      try {
        await window.wails.Clipboard.SetText(addr.dataset.copy);
        toast("success", t("toast.copied"));
      } catch {
        toast("error", t("toast.copyFail"));
      }
      return;
    }
    const link = e.target.closest("a[data-url]");
    if (!link) return;
    e.preventDefault();
    window.wails.Browser.OpenURL(link.dataset.url);
  });

  // 后端推送：状态 / 增量日志 / 全量快照 / 更新状态（status 渲染唯一权威）
  window.wails.Events.On("app:status", (event) => {
    if (!state.snapshot) return;
    state.snapshot.status = JSON.parse(JSON.stringify(event.data));
    renderStatus(state.snapshot.status);
  });
  // 增量日志：appendLogs 内按 id 去重，启动拉取与事件的竞速窗口不会重复渲染
  window.wails.Events.On("app:logs", (event) => {
    appendLogs(event?.data ?? []);
  });
  // 更新状态推送（事件为权威）：迟到 fetch 由 seq 判定丢弃，不会覆盖事件新状态
  window.wails.Events.On("app:update", (event) => {
    applyUpdateInfo(event?.data, true);
  });
  window.wails.Events.On("app:proxies", (event) => {
    state.proxyStatus = Object.fromEntries(
      JSON.parse(JSON.stringify(event.data ?? [])).map((x) => [x.serviceId, x]),
    );
    if (state.snapshot) {
      // 拖拽排序进行中：只刷新底部状态，不重建列表（拖拽结束统一落定）
      if (state.dragSort) {
        renderStatus(state.snapshot.status);
        return;
      }
      // 开关动画保护窗（与 app:snapshot 同策略）：保护窗内轻量刷新并在窗口结束补绘
      if (Date.now() < (state.listRenderHoldUntil ?? 0)) {
        renderStatus(state.snapshot.status);
        scheduleListFlush();
        return;
      }
      renderServices(state.snapshot);
      renderStatus(state.snapshot.status);
    }
  });
  window.wails.Events.On("app:snapshot", (event) => {
    const snap = JSON.parse(JSON.stringify(event.data));
    // 拖拽排序进行中：只落地最新数据（含逐服务器状态），不重建列表（重绘会拔掉被拖卡）。
    // 拖拽结束/回弹后由 applyConfigOnly 或补绘用当前快照重建，不丢任何一个事件。
    if (state.dragSort) {
      pruneMovedProxyStatus(state.snapshot?.config?.services, snap.config?.services);
      state.snapshot = snap;
      state.serverStatus = Object.fromEntries((snap.serverStatuses ?? []).map((x) => [x.serverId, x]));
      renderStatus(state.snapshot.status);
      return;
    }
    // 开关动画保护窗（600ms）：SetServiceEnabled 触发的隧道重启会推送多次
    // app:snapshot（含 services 全量），整卡重绘会截断 switch 动画。
    // 窗内只做轻量更新（数据与逐服务器状态始终为最新），并在窗口结束时安排一次
    // 从当前快照的最终绘制——最后一个事件不会被丢绘；不做旧 response 回退。
    if (Date.now() < (state.listRenderHoldUntil ?? 0)) {
      // 保护窗内同样走共用清理（prev=当前 config），再轻量落地最新快照
      pruneMovedProxyStatus(state.snapshot?.config?.services, snap.config?.services);
      state.snapshot = snap;
      state.serverStatus = Object.fromEntries((snap.serverStatuses ?? []).map((x) => [x.serverId, x]));
      renderStatus(state.snapshot.status);
      scheduleListFlush();
      return;
    }
    applySnapshot(snap);
  });
}

// scheduleListFlush：动画保护窗结束后的最终列表补绘（幂等，单例 timer）。
// 服务/服务器卡片与状态都以调用时刻的最新快照绘制，不回退到任何旧响应。
let listFlushTimer = null;
function scheduleListFlush() {
  if (listFlushTimer) return;
  const wait = Math.max(0, (state.listRenderHoldUntil ?? 0) - Date.now()) + 30;
  listFlushTimer = setTimeout(() => {
    listFlushTimer = null;
    if (!state.snapshot) return;
    renderServices(state.snapshot);
    renderServers();
    renderStatus(state.snapshot.status);
  }, wait);
}

function onServiceListClick(e) {
  const btn = e.target.closest("button[data-act]");
  if (!btn) return;
  const id = btn.closest(".service-item").dataset.id;
  const svc = state.snapshot.config.services.find((s) => s.id === id);
  if (!svc) return;

  if (btn.dataset.act === "edit") {
    openServiceModal(t("modal.editService", svcTypeLabel(svc.type)), svc);
  } else if (btn.dataset.act === "copy") {
    // 走 wails 原生剪贴板（与 addr-copy 同因：WKWebView 下 navigator.clipboard 受限）
    window.wails.Clipboard.SetText(btn.dataset.copy)
      .then(() => toast("success", t("toast.copied")))
      .catch(() => toast("error", t("toast.copyFail")));
  } else if (btn.dataset.act === "qr") {
    openQrModal(btn.dataset.url);
  } else if (btn.dataset.act === "delete") {
    askConfirm(t("confirm.deleteService", svc.name)).then((ok) => {
      if (!ok) return;
      run("run.deleteService", () => api.AppService.DeleteService(id))
        .then(applyConfigOnly)
        .then(() => toast("success", t("toast.serviceDeleted")))
        .catch(() => {});
    });
  }
}

function onServiceListToggle(e) {
  const toggle = e.target.closest("input[data-act='toggle']");
  if (!toggle) return;
  const id = toggle.closest(".service-item").dataset.id;
  state.listRenderHoldUntil = Date.now() + 600; // 动画保护窗：期间 app:snapshot 不重建卡列表
  run("run.toggleService", () => api.AppService.SetServiceEnabled(id, toggle.checked))
    .then((snapshot) => {
      // 轻量更新：只改该服务的 enabled + 刷新计数等轻量部位；
      // 不整卡重绘（renderServices 会重建 switch，160ms 位移动画被截断）
      state.snapshot.config = JSON.parse(JSON.stringify(snapshot.config));
      // 轻量更新：只刷新底部隧道计数（当前已映射数量），不整卡重绘
      const tc = tunnelCounts();
      $("service-count").textContent = String(tc.registered);
      const row = toggle.closest(".service-item");
      if (row) {
        const stale = row.querySelector(".tag-disabled");
        if (stale) stale.remove();
      }
    })
    .catch(() => {
      // 失败回弹：后端未变更，恢复用户点击前的视觉状态（error toast 由 run() 给出）
      toggle.checked = !toggle.checked;
    });
}

// ---------- 隧道卡片拖拽排序（仅把手可拖；持久化只是配置数组顺序变化，不触隧道生命周期）----------

// 拖拽模型：被拖卡保持原 DOM 位（不移出文档流，避免列表重排跳变），用 transform
// 跟手并提层（阴影+微缩放）；其余卡用 FLIP 等价的 transform + 过渡丝滑让位。
// 松手换位时先物理重排 DOM（视觉无跳变），再调 ReorderServices 持久化；失败回弹原序。

// onServiceListPointerDown：仅排序把手触发拖拽（整卡不可拖；单卡时无排序意义）
function onServiceListPointerDown(e) {
  if (e.button !== undefined && e.button !== 0) return; // 仅主键
  const handle = e.target.closest(".svc-drag-handle");
  if (!handle) return;
  const list = $("service-list");
  const item = handle.closest(".service-item");
  const items = [...list.querySelectorAll(".service-item")];
  if (!item || items.length < 2) return;
  e.preventDefault(); // 阻止原生图片/文本拖拽与选择

  const index = items.indexOf(item);
  const homes = items.map((el) => el.offsetTop); // 同 offsetParent，相对位置可直接比较
  state.dragSort = {
    handle,
    item,
    items,
    index,
    homes,
    height: item.offsetHeight,
    pointerId: e.pointerId,
    startClientY: e.clientY,
    startScrollTop: list.scrollTop, // 列表初始滚动量：跟手位移的滚动补偿基准
    dy: 0,
    newIndex: index,
  };
  item.classList.add("is-dragging");
  list.classList.add("is-sorting");
  try {
    handle.setPointerCapture?.(e.pointerId); // 指针移出窗口仍能收到 move/up
  } catch {}
  document.addEventListener("pointermove", onServiceDragMove);
  document.addEventListener("pointerup", onServiceDragUp);
  document.addEventListener("pointercancel", onServiceDragCancel);
}

// onServiceDragMove：跟手位移 + 目标位判定（越过邻卡中线即换位；被夹在列表首尾间）。
// clientY 是视口坐标，列表滚动后卡片在视口中的原位置随之移动（offsetTop 布局坐标
// 不变），因此跟手位移必须计入 scrollTop 变化量，否则卡片会脱离指针。
function onServiceDragMove(e) {
  const d = state.dragSort;
  if (!d || e.pointerId !== d.pointerId) return;
  const minDy = d.homes[0] - d.homes[d.index];
  const maxDy = d.homes[d.items.length - 1] - d.homes[d.index];
  const pointerDy = e.clientY - d.startClientY;
  const scrollDy = ($("service-list").scrollTop ?? 0) - d.startScrollTop;
  d.dy = Math.max(minDy, Math.min(maxDy, pointerDy + scrollDy));
  d.item.style.transform = `translateY(${d.dy}px)`;
  const center = d.homes[d.index] + d.height / 2 + d.dy;
  let target = d.index;
  if (d.dy > 0) {
    for (let i = d.index + 1; i < d.items.length; i++) {
      if (center < d.homes[i] + d.height / 2) break;
      target = i;
    }
  } else if (d.dy < 0) {
    for (let i = d.index - 1; i >= 0; i--) {
      if (center > d.homes[i] + d.height / 2) break;
      target = i;
    }
  }
  if (target !== d.newIndex) {
    d.newIndex = target;
    applyServiceShifts(d);
  }
}

// applyServiceShifts：让位动画。兄弟卡按「目标静态位 - 自身原位」位移一格，
// 用真实 home 差（而非固定槽高）兼容不等高卡片；位移由 CSS 过渡提供平滑。
function applyServiceShifts(d) {
  for (let i = 0; i < d.items.length; i++) {
    if (i === d.index) continue;
    let shift = 0;
    if (i > d.index && i <= d.newIndex) shift = d.homes[i - 1] - d.homes[i];
    else if (i < d.index && i >= d.newIndex) shift = d.homes[i + 1] - d.homes[i];
    d.items[i].style.transform = shift ? `translateY(${shift}px)` : "";
  }
}

function onServiceDragUp(e) {
  const d = state.dragSort;
  if (!d || e.pointerId !== d.pointerId) return;
  endServiceDrag(d);
}

// onServiceDragCancel：系统中断（触摸被系统手势接管等）→ 直接归位，不提交
function onServiceDragCancel(e) {
  const d = state.dragSort;
  if (!d || e.pointerId !== d.pointerId) return;
  detachServiceDrag(d);
}

// detachServiceDrag：拆除拖拽临时状态（监听/提层/让位位移），DOM 顺序不动
function detachServiceDrag(d) {
  state.dragSort = null;
  document.removeEventListener("pointermove", onServiceDragMove);
  document.removeEventListener("pointerup", onServiceDragUp);
  document.removeEventListener("pointercancel", onServiceDragCancel);
  d.item.classList.remove("is-dragging");
  $("service-list").classList.remove("is-sorting");
  for (const el of d.items) el.style.transform = "";
}

// endServiceDrag：松手落定。未换位直接归位；换位则先把 DOM 物理重排为新序（无跳变），
// 再调后端持久化：成功用权威快照重绘；失败回弹到原序并提示（run() 已 toast）。
async function endServiceDrag(d) {
  const moved = d.newIndex !== d.index;
  detachServiceDrag(d);
  if (!moved) return;

  const list = $("service-list");
  const order = d.items.filter((_, i) => i !== d.index);
  order.splice(d.newIndex, 0, d.item);
  for (const el of order) list.appendChild(el); // 同步重排：已被过渡清零，无可见跳变
  const ids = order.map((el) => el.dataset.id);
  try {
    const snapshot = await run("run.reorderServices", () => api.AppService.ReorderServices(ids));
    applyConfigOnly(snapshot);
    toast("success", t("toast.orderSaved"));
  } catch {
    // 保存失败：后端顺序未变，FLIP 回弹到原序；最终由当前快照重建保证状态一致
    revertServiceOrder(d.items);
  }
}

// revertServiceOrder：失败回弹。DOM 此刻已是新序：先无过渡把每张卡摆到「新序位置」
// （即当前视觉位置不变），再把 DOM 物理回原序并过渡清零——从新序丝滑滑回原序。
function revertServiceOrder(oldItems) {
  const list = $("service-list");
  const homes = oldItems.map((el) => el.offsetTop);
  const newPos = new Map();
  [...list.querySelectorAll(".service-item")].forEach((el, i) => newPos.set(el, i));
  for (const [i, el] of oldItems.entries()) {
    const j = newPos.get(el) ?? i;
    el.style.transition = "none";
    el.style.transform = j === i ? "" : `translateY(${homes[j] - homes[i]}px)`;
  }
  for (const el of oldItems) list.appendChild(el); // 物理回原序
  void list.offsetWidth; // 提交初始态后再过渡清零
  const ease = "cubic-bezier(0.77, 0, 0.175, 1)";
  const dur = prefersReducedMotion() ? "0ms" : "220ms";
  for (const el of oldItems) {
    el.style.transition = `transform ${dur} ${ease}`;
    el.style.transform = "";
  }
  setTimeout(() => {
    for (const el of oldItems) el.style.transition = "";
    if (state.dragSort) return; // 用户已开始下一次拖拽：不重建打断
    if (state.snapshot) renderServices(state.snapshot);
  }, 240);
}

// prefersReducedMotion：减少动效偏好（回弹时长降为 0，位置仍正确）
function prefersReducedMotion() {
  try {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}

// onServerListClick：Frps 卡片编辑/删除（删除由后端校验引用，含 disabled 服务，消息原样呈现）
function onServerListClick(e) {
  const btn = e.target.closest("button[data-act]");
  if (!btn) return;
  const id = btn.closest(".server-item").dataset.id;
  const srv = serverById(id);
  if (!srv) return;

  if (btn.dataset.act === "srv-test") {
    // 连接测试：仅探测，不改服务器状态/不算实例 failed；busy 中防重复
    if (state.serverProbe[id]) return;
    state.serverProbe[id] = true;
    renderServers();
    run("run.testConn", () => api.AppService.TestServerConnection(id))
      .then((res) => {
        if (res?.success) {
          // 明确是控制连接/鉴权成功；core 附带的 message 仅短附，避免长 toast
          const base = t("toast.probeOk", Number(res.latencyMs ?? 0));
          const extra = res.message ? ` · ${briefText(String(res.message), 40)}` : "";
          toast("success", `${base}${extra}`);
        } else toast("error", t("toast.probeFail", res?.message || t("toast.unknownReason")));
      })
      .catch(() => {}) // RPC 异常原因已由 run() toast
      .finally(() => {
        delete state.serverProbe[id];
        // 配置仍在时才重绘恢复按钮（期间被删除则无需重绘）
        if (serverById(id)) renderServers();
      });
    return;
  }
  if (btn.dataset.act === "srv-edit") {
    openServerModal(srv);
  } else if (btn.dataset.act === "srv-delete") {
    askConfirm(t("confirm.deleteServer", srv.name)).then((ok) => {
      if (!ok) return;
      run("run.deleteConfig", () => api.AppService.DeleteServer(id))
        .then(applyConfigOnly)
        .then(() => toast("success", t("toast.configDeleted")))
        .catch(() => {}); // 失败原因已 toast（后端消息原样呈现）
    });
  }
}

// ---------- 启动 ----------

async function boot() {
  initLanguage(); // 语言必须在主题/事件绑定前应用：静态文案/按钮 tip 随偏好
  initTheme();
  bindEvents();
  try {
    await api.AppService.FrontendLog("info", `boot start t=${Date.now()}`);
  } catch {}
  // 初始拉取带重试：页面加载早期的绑定调用可能失败（响应丢失），指数退避重试兜底
  // （tryFetch 已提升为模块级，供更新信息拉取复用）。
  positionTabSlider(false); // 首帧无动画定位
  const langSeqAtBoot = langSync.seq; // 快照拉取期间的用户语言改动使对齐作废
  const [snapRes, logRes, proxyRes] = await Promise.allSettled([
    tryFetch("Snapshot", () => api.AppService.Snapshot(), 4),
    tryFetch("Logs", () => api.AppService.Logs(), 4),
    tryFetch("ProxyStatuses", () => api.AppService.ProxyStatuses(), 4),
  ]);
  if (snapRes.status === "fulfilled" && snapRes.value) {
    // 语言对齐：后端为权威（配置偏好 + 解析后的 resolved）；拉取期间用户已切语言则不覆盖
    reconcileLanguageFromSnapshot(snapRes.value, langSeqAtBoot);
    applySnapshot(snapRes.value);
  }
  if (logRes.status === "fulfilled" && logRes.value) {
    mergeLogs(logRes.value); // 合并非覆盖：拉取期间 app:logs 事件已到的更晚日志不丢
    renderLogs();
  }
  if (proxyRes.status === "fulfilled" && proxyRes.value) {
    state.proxyStatus = Object.fromEntries(
      JSON.parse(JSON.stringify(proxyRes.value ?? [])).map((x) => [x.serviceId, x]),
    );
    if (state.snapshot) {
      renderServices(state.snapshot);
      renderStatus(state.snapshot.status);
    }
  }
  scrollLogToEnd(true);
  // 更新信息：事件先到则丢弃迟到响应；失败不阻塞首屏（后端日志可查）
  fetchInitUpdateInfo().catch(() => {});
  try {
    await api.AppService.FrontendLog("info", `boot done logs=${state.logs.length} snapshot=${state.snapshot ? "ok" : "null"}`);
  } catch {}
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

// tryFetch（模块级，供更新信息拉取复用）：初始拉取带重试——页面加载早期的绑定
// 调用可能失败（响应丢失），失败会导致日志/配置空白，指数退避重试兜底；
// 重试耗尽抛最后一次错误，由调用方兜底（boot 的 allSettled / 更新信息的 catch）。
const tryFetch = async (label, fn, attempts) => {
  let lastErr = null;
  for (let i = 0; i < attempts; i++) {
    try {
      return await fn();
    } catch (err) {
      lastErr = err;
      try {
        await api.AppService.FrontendLog("error", `${label} #${i + 1} failed: ${enDetail(err)}`);
      } catch {}
      await sleep(400 * (i + 1));
    }
  }
  throw lastErr;
};

boot();

// 测试钩子：供 scripts/verify-multiserver-ui.mjs（node 零依赖 DOM 桩）执行行为场景；
// 导出仅为引用，不影响 wails 打包与运行时行为。
export const __test = {
  state,
  applyConfigOnly,
  svcPublicURL,
  eligibleServers,
  openServiceModal,
  closeServiceModal,
  openServerModal,
  closeServerModal,
  applySnapshot,
  renderServers,
  renderStatus,
  updateMoveHint,
  // 语言：切换/应用/取词与启动对齐（供验证脚本直接驱动，等价点击语言按钮的完整路径）
  setLanguagePref,
  applyLanguage,
  applyStaticI18n,
  reconcileLanguageFromSnapshot,
  langSync,
  t,
  // 诊断通道英文详情过滤（本地化错误不回流日志通道）
  enDetail,
};
