(function () {
  "use strict";
  const ROOT = "/brand/admin/api/support/";
  const API = ROOT + "v2/";
  let csrf = "",
    tickets = [],
    current = "",
    currentData = null,
    settings = {},
    sending = false,
    refreshTimer,
    stopStream = () => {};
  const UI = window.SupportUI;
  const $ = (id) => document.getElementById(id);
  const labels = {
    waiting_operator: "等待客服",
    waiting_user: "等待用户",
    resolved: "已解决",
    closed: "已关闭",
    high: "高",
    normal: "普通",
    low: "低",
    recharge: "充值到账",
    api_access: "API 接入",
    model_error: "模型调用错误",
    billing: "计费与扣额",
    account_security: "账号与安全",
    other: "其他",
  };
  async function raw(path, opt = {}) {
    opt.cache = "no-store";
    opt.credentials = "same-origin";
    if (opt.method && opt.method !== "GET")
      opt.headers = Object.assign(
        { "Content-Type": "application/json", "X-CSRF-Token": csrf },
        opt.headers || {},
      );
    const r = await fetch(ROOT + path, opt);
    const p = await r.json().catch(() => ({}));
    if (r.status === 401) throw Error("管理员会话已过期，请重新登录控制台");
    if (!r.ok || p.success === false) throw Error(p.message || "请求失败");
    return p;
  }
  const call = (path, opt) => raw("v2/" + path, opt);
  const post = (path, data) => call(path, { method: "POST", body: JSON.stringify(data) });
  const time = (v) => {
    const d = new Date(v);
    return isNaN(d)
      ? ""
      : d.toLocaleString("zh-CN", {
          hour12: false,
          month: "2-digit",
          day: "2-digit",
          hour: "2-digit",
          minute: "2-digit",
        });
  };
  async function bootstrap() {
    let s = await raw("session");
    if (!s.authenticated) {
      const r = await fetch("/api/user/auth/refresh", {
        method: "POST",
        cache: "no-store",
        credentials: "same-origin",
      });
      const p = await r.json();
      const token = p && p.data && p.data.access_token;
      if (!token) throw Error("请先登录管理员控制台");
      await raw("session", { method: "POST", body: JSON.stringify({ access_token: token }) });
      s = await raw("session");
    }
    csrf = s.csrf_token || "";
    return s;
  }
  function query() {
    const p = new URLSearchParams();
    [
      ["q", "filter-q"],
      ["category", "filter-category"],
      ["status", "filter-status"],
      ["priority", "filter-priority"],
    ].forEach(([k, id]) => {
      if ($(id).value) p.set(k, $(id).value);
    });
    if ($("filter-unread").checked) p.set("unread", "1");
    return p.toString();
  }
  async function load() {
    const p = await call("tickets?" + query());
    tickets = p.tickets || [];
    const s = p.stats || {};
    $("m-waiting").textContent = s.waiting || 0;
    $("m-overdue").textContent = s.overdue || 0;
    $("m-unread").textContent = s.unread || 0;
    $("m-resolved").textContent = s.resolved_today || 0;
    $("queue-count").textContent = tickets.length + " 条";
    renderQueue();
    $("connection").textContent = "已连接";
    $("connection").dataset.state = "connected";
  }
  function renderQueue() {
    const box = $("desk-queue");
    box.replaceChildren();
    tickets.forEach((row) => {
      const b = document.createElement("button");
      b.type = "button";
      b.setAttribute("aria-pressed", String(row.id === current));
      b.className =
        "desk-ticket" + (row.id === current ? " active" : "") + (row.overdue ? " overdue" : "");
      const line = document.createElement("span");
      line.className = "line";
      const title = document.createElement("strong");
      title.textContent = row.subject || "客服咨询";
      line.append(title);
      if (row.unread_operator) {
        const badge = document.createElement("span");
        badge.className = "s-badge";
        badge.textContent = row.unread_operator;
        line.append(badge);
      }
      const p = document.createElement("p");
      p.textContent =
        (row.username || "用户 " + row.uid) +
        " · " +
        (labels[row.status] || row.status) +
        " · " +
        time(row.last_message_at);
      const preview = document.createElement("p");
      preview.textContent = row.last_text || "暂无消息";
      b.append(line, p, preview);
      b.onclick = () => open(row.id);
      box.append(b);
    });
    if (!tickets.length) {
      const p = document.createElement("p");
      p.className = "s-empty";
      p.textContent = "没有符合条件的工单";
      box.append(p);
    }
  }
  function bubble(row) {
    const d = document.createElement("div");
    d.className = "msg " + (row.role || "operator");
    const t = document.createElement("span");
    t.textContent = row.text || "";
    d.append(t);
    if ((row.attachments || []).length) {
      const g = document.createElement("div");
      g.className = "attachment-grid";
      row.attachments.forEach((x) => {
        const a = document.createElement("a");
        a.href = API + "attachment?id=" + encodeURIComponent(x.id);
        a.target = "_blank";
        a.rel = "noopener";
        a.textContent = "查看图片 · " + (x.name || "附件");
        g.append(a);
      });
      d.append(g);
    }
    const at = document.createElement("span");
    at.className = "at";
    at.textContent =
      (row.role === "user" ? "用户" : row.role === "note" ? "内部备注 · 仅客服可见" : "客服") +
      " · " +
      time(row.created_at);
    d.append(at);
    return d;
  }
  function thread(p) {
    const t = p.ticket;
    const main = $("desk-thread");
    main.classList.remove("single-view");
    main.replaceChildren();
    const bar = document.createElement("div");
    bar.className = "desk-actions";
    const back = document.createElement("button");
    back.className = "s-icon";
    back.textContent = "←";
    back.setAttribute("aria-label", "返回工单队列");
    back.onclick = () => {
      $("desk-grid").classList.remove("thread-open");
    };
    const heading = document.createElement("div");
    heading.className = "grow";
    const strong = document.createElement("strong");
    strong.textContent = t.subject || "客服咨询";
    const sub = document.createElement("div");
    sub.className = "s-muted";
    sub.textContent = t.id + " · " + (labels[t.category] || t.category);
    heading.append(strong, sub);
    const priority = document.createElement("select");
    [
      ["low", "低"],
      ["normal", "普通"],
      ["high", "高"],
    ].forEach((x) => priority.append(new Option(x[1], x[0])));
    priority.value = t.priority || "normal";
    priority.setAttribute("aria-label", "工单优先级");
    priority.id = "thread-priority";
    priority.onchange = () =>
      post("update", { ticket_id: t.id, priority: priority.value })
        .then(refreshCurrent)
        .catch(showError);
    const status = document.createElement("select");
    [
      ["waiting_operator", "等待客服"],
      ["waiting_user", "等待用户"],
      ["resolved", "已解决"],
      ["closed", "已关闭"],
    ].forEach((x) => status.append(new Option(x[1], x[0])));
    status.value = t.status;
    status.setAttribute("aria-label", "工单状态");
    status.id = "thread-status";
    status.onchange = () =>
      post("status", { ticket_id: t.id, status: status.value })
        .then(refreshCurrent)
        .catch(showError);
    const contextButton = document.createElement("button");
    contextButton.className = "s-btn secondary context-toggle";
    contextButton.textContent = "用户资料";
    contextButton.setAttribute("aria-controls", "context-pane");
    contextButton.setAttribute(
      "aria-expanded",
      String($("desk-grid").classList.contains("context-open")),
    );
    contextButton.onclick = () => {
      const expanded = $("desk-grid").classList.toggle("context-open");
      contextButton.setAttribute("aria-expanded", String(expanded));
      if (expanded) $("context-close").focus();
    };
    bar.append(back, heading, priority, status, contextButton);
    const log = document.createElement("div");
    log.className = "timeline";
    log.id = "admin-timeline";
    log.tabIndex = 0;
    log.setAttribute("role", "region");
    log.setAttribute("aria-label", "工单消息");
    UI.timeline(log, p.messages || [], bubble);
    const compose = document.createElement("div");
    compose.className = "composer";
    const text = document.createElement("textarea");
    text.placeholder = "输入回复内容…";
    text.maxLength = 2000;
    text.setAttribute("aria-label", "客服回复");
    text.id = "admin-message";
    text.value = sessionStorage.getItem("omniapi-admin-draft-" + t.id) || "";
    text.oninput = () => sessionStorage.setItem("omniapi-admin-draft-" + t.id, text.value);
    const quick = document.createElement("div");
    quick.className = "quick";
    (settings.quick_replies || []).forEach((q) => {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = q;
      b.onclick = () => {
        text.value = q;
        text.dispatchEvent(new Event("input"));
        text.focus();
      };
      quick.append(b);
    });
    const row = document.createElement("div");
    row.className = "composer-row";
    const upload = document.createElement("label");
    upload.className = "s-btn secondary";
    upload.textContent = "添加图片";
    const file = document.createElement("input");
    file.type = "file";
    file.accept = "image/png,image/jpeg,image/webp";
    file.multiple = true;
    file.hidden = true;
    upload.append(file);
    const note = document.createElement("button");
    note.className = "compose-tab";
    note.textContent = "内部备注";
    const replyTab = document.createElement("button");
    replyTab.className = "compose-tab active";
    replyTab.textContent = "回复用户";
    const tabs = document.createElement("div");
    tabs.className = "compose-tabs";
    tabs.append(replyTab, note);
    let mode = "reply";
    function setMode(next) {
      mode = next;
      note.classList.toggle("active", mode === "note");
      replyTab.classList.toggle("active", mode === "reply");
      text.placeholder = mode === "note" ? "添加内部备注，仅管理员可见…" : "输入回复内容…";
      send.textContent = mode === "note" ? "保存内部备注" : "发送回复";
      upload.hidden = mode === "note";
    }
    note.onclick = () => setMode("note");
    replyTab.onclick = () => setMode("reply");
    const ai = document.createElement("button");
    ai.className = "s-btn secondary";
    ai.textContent = "AI 建议";
    ai.hidden = !(settings.ai || {}).enabled;
    ai.onclick = () => draft(text);
    const grow = document.createElement("span");
    grow.className = "grow";
    const send = document.createElement("button");
    send.className = "s-btn";
    send.textContent = "发送回复";
    send.id = "admin-send";
    send.onclick = () => submit(text, send, mode);
    UI.enter(text, () => submit(text, send, mode));
    const hint = document.createElement("span");
    hint.className = "key-hint";
    hint.textContent = "Enter 发送 · Shift+Enter 换行";
    row.append(upload, ai, grow, hint, send);
    const err = document.createElement("div");
    err.className = "error";
    err.id = "thread-error";
    err.setAttribute("role", "alert");
    const previews = document.createElement("div");
    previews.className = "file-preview";
    main.chosenFiles = [];
    main.picker = UI.picker(
      file,
      previews,
      (next) => {
        main.chosenFiles = next;
      },
      err,
    );
    note.id = "admin-note";
    compose.append(tabs, quick, text, row, previews, err);
    main.append(bar, log, compose);
    requestAnimationFrame(() => {
      log.scrollTop = log.scrollHeight;
    });
    $("desk-grid").classList.add("thread-open");
  }
  async function open(id) {
    current = id;
    try {
      const p = await call("ticket?id=" + encodeURIComponent(id));
      if (current !== id) return;
      currentData = p;
      thread(currentData);
      renderContext(currentData.context || {}, currentData.ticket);
      await raw("read", { method: "POST", body: JSON.stringify({ conversation_id: id }) });
      await load();
    } catch (e) {
      $("connection").textContent = e.message;
      $("connection").dataset.state = "error";
    }
  }
  function fileData(file) {
    return new Promise((ok, no) => {
      if (file.size > 5 * 1024 * 1024) return no(Error("每张图片不能超过 5 MiB"));
      const r = new FileReader();
      r.onload = () => ok({ name: file.name, data: String(r.result).split(",")[1] || "" });
      r.onerror = () => no(Error("图片读取失败"));
      r.readAsDataURL(file);
    });
  }
  function showError(e) {
    const box = $("thread-error");
    if (box) box.textContent = e.message;
    else {
      $("connection").textContent = e.message;
      $("connection").dataset.state = "error";
    }
  }
  async function refreshCurrent() {
    const id = current;
    if (!id) return;
    const p = await call("ticket?id=" + encodeURIComponent(id));
    if (current !== id || !$("admin-timeline")) return;
    currentData = p;
    UI.timeline($("admin-timeline"), p.messages || [], bubble);
    $("thread-status").value = p.ticket.status;
    $("thread-priority").value = p.ticket.priority || "normal";
    renderContext(p.context || {}, p.ticket);
    if (!document.hidden)
      await raw("read", { method: "POST", body: JSON.stringify({ conversation_id: id }) });
  }
  function scheduleRefresh() {
    clearTimeout(refreshTimer);
    refreshTimer = setTimeout(() => {
      Promise.all([load(), refreshCurrent()]).catch((e) => {
        $("connection").textContent = e.message;
        $("connection").dataset.state = "error";
      });
    }, 200);
  }
  async function submit(input, button, kind) {
    if (sending || !input.value.trim()) return;
    const id = current,
      text = input.value,
      main = $("desk-thread"),
      chosen = main.chosenFiles.slice(),
      picker = main.picker;
    sending = true;
    button.disabled = true;
    $("thread-error").textContent = "";
    try {
      let attachments = [];
      if (kind === "reply" && chosen.length) {
        const up = await post("upload", {
          ticket_id: id,
          files: await Promise.all(chosen.map(fileData)),
        });
        attachments = up.attachments || [];
      }
      await post(kind, { ticket_id: id, text, ...(kind === "reply" ? { attachments } : {}) });
      if (current === id) {
        if (input.value === text) {
          input.value = "";
          sessionStorage.removeItem("omniapi-admin-draft-" + id);
        }
        if (kind === "reply") picker.clear();
        await refreshCurrent();
      }
      await load();
    } catch (e) {
      if (current === id) showError(e);
    } finally {
      sending = false;
      button.disabled = false;
    }
  }
  async function reply(input, button, file) {
    return submit(input, button, "reply");
  }
  async function sendNote(input) {
    return submit(input, $("admin-note"), "note");
  }
  async function draft(input) {
    const id = current;
    try {
      const p = await post("draft", { ticket_id: id });
      if (current === id) {
        input.value = p.draft || "";
        input.dispatchEvent(new Event("input"));
      }
    } catch (e) {
      showError(e);
    }
  }
  function renderContext(c, t) {
    const box = $("context");
    box.replaceChildren();
    const h = document.createElement("h3");
    h.textContent = "用户资料";
    const dl = document.createElement("dl");
    [
      ["账号", c.username || "—"],
      ["用户 ID", String(c.uid || "—")],
      ["余额", c.balance == null ? "需从控制台查看" : c.balance],
      ["分组", c.group || "—"],
      [
        "最近充值",
        c.recent_topup == null
          ? "需从核验页查看"
          : typeof c.recent_topup === "object"
            ? JSON.stringify(c.recent_topup)
            : c.recent_topup,
      ],
    ].forEach((x) => {
      const dt = document.createElement("dt");
      dt.textContent = x[0];
      const dd = document.createElement("dd");
      dd.textContent = x[1];
      dl.append(dt, dd);
    });
    box.append(h, dl);
    const link = document.createElement("a");
    link.href = "/brand/admin.html";
    link.className = "s-btn secondary";
    link.textContent = "前往充值核验";
    box.append(link);
    const h2 = document.createElement("h3");
    h2.textContent = "历史工单";
    box.append(h2);
    (c.tickets || []).forEach((row) => {
      const a = document.createElement("a");
      a.href = "#";
      a.className = "history-link";
      a.textContent = (row.subject || row.id) + " · " + (labels[row.status] || row.status);
      a.onclick = (e) => {
        e.preventDefault();
        open(row.id);
      };
      box.append(a);
    });
    const warn = document.createElement("p");
    warn.className = "s-muted";
    warn.textContent = "客服台不能修改余额；充值问题请进入充值核验流程。";
    box.append(warn);
  }
  async function loadSettings() {
    settings = await raw("settings");
    $("set-hours").value = settings.service_hours || "";
    $("set-greeting").value = settings.greeting || "";
    $("set-offline").value = settings.offline_hint || "";
    $("set-quick").value = (settings.quick_replies || []).join("\n");
    const channel = (settings.notify || {}).channel || "none";
    $("set-notify-status").textContent =
      channel === "wecom" && settings.notify_credentials_present
        ? "企业微信通知已启用"
        : "企业微信通知尚未启用";
  }
  let settingsFocus;
  function settingsOpen() {
    settingsFocus = document.activeElement;
    $("settings-pop").hidden = false;
    $("set-hours").focus();
  }
  function settingsClose() {
    $("settings-pop").hidden = true;
    settingsFocus?.focus();
  }
  function contextClose() {
    $("desk-grid").classList.remove("context-open");
    const button = document.querySelector(".context-toggle");
    button?.setAttribute("aria-expanded", "false");
    button?.focus();
  }
  async function boot() {
    try {
      await bootstrap();
      await loadSettings();
      await load();
      const wanted = location.hash.slice(1);
      if (wanted) open(wanted);
      $("settings-btn").onclick = settingsOpen;
      $("context-close").onclick = contextClose;
      document.addEventListener("keydown", (e) => {
        if (e.key === "Escape") {
          if (!$("settings-pop").hidden) settingsClose();
          else if ($("desk-grid").classList.contains("context-open")) contextClose();
        }
        if (e.key === "Tab" && !$("settings-pop").hidden) {
          const items = Array.from(
            $("settings-pop").querySelectorAll("input,textarea,button,a[href],select"),
          ).filter((x) => !x.disabled && !x.hidden);
          const first = items[0],
            last = items[items.length - 1];
          if (e.shiftKey && document.activeElement === first) {
            e.preventDefault();
            last.focus();
          } else if (!e.shiftKey && document.activeElement === last) {
            e.preventDefault();
            first.focus();
          }
        }
      });
      $("settings-pop").onclick = (e) => {
        if (e.target === $("settings-pop")) settingsClose();
      };
      $("settings-cancel").onclick = settingsClose;
      $("settings-form").onsubmit = async (e) => {
        e.preventDefault();
        try {
          await post("settings", {
            service_hours: $("set-hours").value,
            greeting: $("set-greeting").value,
            offline_hint: $("set-offline").value,
            quick_replies: $("set-quick").value.split("\n"),
          });
          await loadSettings();
          settingsClose();
          if (current) open(current);
        } catch (x) {
          $("settings-error").textContent = x.message;
        }
      };
      let timer;
      ["filter-q", "filter-category", "filter-status", "filter-priority", "filter-unread"].forEach(
        (id) =>
          $(id).addEventListener(id === "filter-q" ? "input" : "change", () => {
            clearTimeout(timer);
            timer = setTimeout(() => load().catch(() => {}), 200);
          }),
      );
      setInterval(() => {
        if (!document.hidden)
          Promise.all([load(), refreshCurrent()]).catch((e) => {
            $("connection").textContent = e.message;
            $("connection").dataset.state = "error";
          });
      }, 10000);
      stopStream = UI.stream(API + "stream", {}, scheduleRefresh);
      document.addEventListener("visibilitychange", () => {
        if (!document.hidden) scheduleRefresh();
      });
      window.addEventListener("pagehide", () => stopStream());
    } catch (e) {
      $("connection").textContent = e.message;
      $("connection").dataset.state = "error";
    }
  }
  boot();
})();
