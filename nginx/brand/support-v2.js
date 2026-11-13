(function () {
  "use strict";
  const API = "/brand/api/support/v2/";
  let token = "",
    config = {},
    tickets = [],
    current = null,
    files = [],
    ended = false,
    stopStream = () => {},
    refreshTimer,
    sending = false,
    picker;
  const UI = window.SupportUI;
  const $ = (id) => document.getElementById(id);
  const labels = {
    waiting_operator: "等待客服",
    waiting_user: "等待用户",
    resolved: "已解决",
    closed: "已关闭",
  };
  async function auth() {
    const r = await fetch("/api/user/auth/refresh", {
      method: "POST",
      cache: "no-store",
      credentials: "same-origin",
    });
    const p = await r.json();
    token = (p && p.data && p.data.access_token) || "";
    if (!token) throw Error("请先登录 OmniAPI 控制台");
  }
  async function call(path, opt = {}) {
    opt.cache = "no-store";
    opt.headers = Object.assign({ Authorization: "Bearer " + token }, opt.headers || {});
    const r = await fetch(API + path, opt);
    const p = await r.json().catch(() => ({}));
    if (!r.ok || p.success === false) throw Error(p.message || "请求失败");
    return p;
  }
  const post = (path, data) =>
    call(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(data),
    });
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
  function statusBadge(row) {
    const s = document.createElement("span");
    s.className = "s-badge" + (row.status === "waiting_operator" ? " amber" : "");
    s.textContent = labels[row.status] || row.status;
    return s;
  }
  function renderList() {
    const box = $("tickets");
    box.replaceChildren();
    if (!tickets.length) {
      box.append(
        Object.assign(document.createElement("p"), {
          className: "s-muted",
          textContent: "还没有工单",
        }),
      );
      return;
    }
    tickets
      .filter((row) =>
        ended
          ? ["resolved", "closed"].includes(row.status)
          : !["resolved", "closed"].includes(row.status),
      )
      .filter(
        (row) =>
          !$("ticket-search").value ||
          (row.subject + " " + (row.last_text || "")).includes($("ticket-search").value),
      )
      .forEach((row) => {
        const b = document.createElement("button");
        b.type = "button";
        b.setAttribute("aria-pressed", String(row.id === current));
        b.className = row.id === current ? "active" : "";
        const title = document.createElement("span");
        title.className = "subject";
        title.textContent = row.subject || "客服咨询";
        const meta = document.createElement("span");
        meta.className = "meta";
        meta.textContent = (labels[row.status] || row.status) + " · " + time(row.last_message_at);
        const preview = document.createElement("span");
        preview.className = "preview";
        preview.textContent = row.last_text || "暂无消息";
        b.append(title, preview, meta);
        if (Number(row.unread_user))
          b.append(
            Object.assign(document.createElement("span"), {
              className: "s-badge",
              textContent: String(row.unread_user) + " 条新回复",
            }),
          );
        b.onclick = () => openTicket(row.id);
        box.append(b);
      });
  }
  async function load() {
    const p = await call("tickets");
    tickets = p.tickets || [];
    renderList();
    return p;
  }
  function attachment(row) {
    const a = document.createElement("a");
    a.href = "#";
    a.textContent = "查看图片 · " + (row.name || "附件");
    a.onclick = (e) => {
      e.preventDefault();
      download(row.id);
    };
    return a;
  }
  async function download(id) {
    const r = await fetch(API + "attachment?id=" + encodeURIComponent(id), {
      headers: { Authorization: "Bearer " + token },
      cache: "no-store",
    });
    if (!r.ok) return;
    const url = URL.createObjectURL(await r.blob());
    window.open(url, "_blank", "noopener");
    setTimeout(() => URL.revokeObjectURL(url), 30000);
  }
  function message(row) {
    const d = document.createElement("div");
    d.className = "msg " + (row.role || "operator");
    const text = document.createElement("span");
    text.textContent = row.text || "";
    d.append(text);
    if ((row.attachments || []).length) {
      const grid = document.createElement("div");
      grid.className = "attachment-grid";
      row.attachments.forEach((a) => grid.append(attachment(a)));
      d.append(grid);
    }
    const at = document.createElement("span");
    at.className = "at";
    at.textContent = (row.role === "user" ? "我" : "客服") + " · " + time(row.created_at);
    d.append(at);
    return d;
  }
  function applyTicket(p) {
    const t = p.ticket;
    $("ticket-subject").textContent = t.subject;
    $("ticket-meta").textContent = (labels[t.status] || t.status) + " · " + t.id;
    $("service-hours").textContent =
      "客服时间 " + (config.service_hours || "12:00–24:00（北京时间）");
    $("resolve-btn").textContent = t.status === "resolved" ? "重新打开" : "确认解决";
    $("resolve-btn").disabled = t.status === "closed";
    $("resolve-btn").onclick = () => changeStatus(t.status === "resolved" ? "reopen" : "resolve");
    const writable = ["waiting_operator", "waiting_user"].includes(t.status);
    $("send-btn").disabled = sending || !writable;
    $("message-text").disabled = !writable;
    UI.timeline($("timeline"), p.messages || [], message);
  }
  async function refreshTicket() {
    const id = current;
    if (!id) return;
    const p = await call("ticket?id=" + encodeURIComponent(id));
    if (current !== id || !$("timeline")) return;
    applyTicket(p);
    if (!document.hidden && !$("shell").classList.contains("show-list"))
      await post("read", { ticket_id: id });
  }
  function scheduleRefresh() {
    clearTimeout(refreshTimer);
    refreshTimer = setTimeout(() => {
      refreshTicket()
        .then(load)
        .catch(() => {});
    }, 180);
  }
  async function openTicket(id) {
    stopStream();
    current = id;
    const p = await call("ticket?id=" + encodeURIComponent(id));
    if (current !== id) return;
    files = [];
    const main = $("main");
    main.classList.remove("single-view");
    main.replaceChildren($("thread-template").content.cloneNode(true));
    $("back-btn").onclick = () => {
      $("shell").classList.add("show-list");
    };
    $("send-btn").onclick = send;
    picker = UI.picker(
      $("images"),
      $("file-preview"),
      (next) => {
        files = next;
      },
      $("send-error"),
    );
    $("message-text").oninput = (e) =>
      sessionStorage.setItem("omniapi-support-draft-" + id, e.target.value);
    $("message-text").value = sessionStorage.getItem("omniapi-support-draft-" + id) || "";
    UI.enter($("message-text"), send);
    applyTicket(p);
    $("timeline").scrollTop = $("timeline").scrollHeight;
    $("shell").classList.remove("show-list");
    await post("read", { ticket_id: id });
    if (current === id)
      stopStream = UI.stream(
        API +
          "stream?ticket_id=" +
          encodeURIComponent(id) +
          "&since=" +
          Math.max(0, ...(p.messages || []).map((r) => r.seq || 0)),
        { Authorization: "Bearer " + token },
        scheduleRefresh,
      );
    await load();
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
  async function send() {
    const input = $("message-text");
    if (sending || !input || input.disabled || !input.value.trim()) return;
    const id = current,
      text = input.value,
      chosen = files.slice(),
      activePicker = picker;
    sending = true;
    $("send-btn").disabled = true;
    $("send-error").textContent = "";
    try {
      let attachments = [];
      if (chosen.length) {
        const up = await post("upload", {
          ticket_id: id,
          files: await Promise.all(chosen.map(fileData)),
        });
        attachments = up.attachments || [];
      }
      await post("message", { ticket_id: id, text, attachments });
      if (current === id) {
        if (input.value === text) {
          input.value = "";
          sessionStorage.removeItem("omniapi-support-draft-" + id);
        }
        activePicker.clear();
        await refreshTicket();
      }
      await load();
    } catch (e) {
      if (current === id) $("send-error").textContent = e.message;
    } finally {
      sending = false;
      if (current === id) refreshTicket().catch(() => {});
    }
  }
  async function changeStatus(action) {
    try {
      await post("status", { ticket_id: current, action });
      await refreshTicket();
      await load();
    } catch (e) {
      $("send-error").textContent = e.message;
    }
  }
  function emptyState(text, kind) {
    const d = document.createElement("div");
    d.className = "s-empty" + (kind ? " " + kind : "");
    d.textContent = text;
    if (kind === "error") {
      const link = document.createElement("a");
      link.className = "s-btn secondary";
      link.href = "/login";
      link.textContent = "前往控制台登录";
      d.append(link);
    }
    $("main").classList.add("single-view");
    $("main").replaceChildren(d);
  }
  function create() {
    stopStream();
    current = null;
    $("main").classList.add("single-view");
    $("shell").classList.remove("show-list");
    $("main").replaceChildren($("create-template").content.cloneNode(true));
    $("greeting").textContent = config.greeting || "";
    (config.categories || []).forEach((c) => $("category").append(new Option(c.label, c.id)));
    $("cancel-btn").onclick = () => {
      if (tickets[0]) openTicket(tickets[0].id);
      else emptyState("选择一条咨询，或开始新的咨询。");
    };
    $("create-btn").onclick = async () => {
      const b = $("create-btn");
      b.disabled = true;
      $("create-error").textContent = "";
      try {
        const p = await post("tickets", {
          category: $("category").value,
          subject: $("description").value.trim().replace(/\s+/g, " ").slice(0, 32),
          description: $("description").value,
          source: "support-page",
        });
        await load();
        await openTicket(p.ticket.id);
      } catch (e) {
        $("create-error").textContent = e.message;
      } finally {
        b.disabled = false;
      }
    };
  }
  async function boot() {
    try {
      await auth();
      const c = await fetch("/brand/api/support/config", { cache: "no-store" });
      config = await c.json();
      $("ticket-search").oninput = renderList;
      $("active-tab").onclick = () => {
        ended = false;
        $("active-tab").classList.add("active");
        $("ended-tab").classList.remove("active");
        renderList();
      };
      $("ended-tab").onclick = () => {
        ended = true;
        $("ended-tab").classList.add("active");
        $("active-tab").classList.remove("active");
        renderList();
      };
      $("new-btn").onclick = create;
      $("history-btn").onclick = () => {
        $("shell").classList.add("show-list");
      };
      await load();
      const wanted = new URLSearchParams(location.search).get("ticket");
      if (wanted) await openTicket(wanted);
      else if (tickets.length) await openTicket(tickets[0].id);
      else create();
      setInterval(() => {
        if (!document.hidden) Promise.all([load(), refreshTicket()]).catch(() => {});
      }, 10000);
      document.addEventListener("visibilitychange", () => {
        if (!document.hidden) scheduleRefresh();
      });
      window.addEventListener("pagehide", () => stopStream());
    } catch (e) {
      emptyState(e.message, "error");
    }
  }
  boot();
})();
