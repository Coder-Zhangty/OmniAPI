import React, {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import { normalize, repeatedPrefix } from "./protocol";

const base = location.pathname.startsWith("/archive-v3") ? "/archive-v3" : "";
const endpoint = (path: string) => base + "/api" + path;
async function api(path: string, data?: any) {
  const res = await fetch(endpoint(path), {
    credentials: "same-origin",
    headers: {
      "X-Archive-Request": "1",
      ...(data === undefined ? {} : { "Content-Type": "application/json" }),
    },
    ...(data === undefined
      ? {}
      : { method: "POST", body: JSON.stringify(data) }),
  });
  const value = await res.json();
  if (!res.ok) {
    const error: any = new Error(value.error || "请求失败");
    error.status = res.status;
    throw error;
  }
  return value;
}
const bytes = (n?: number) =>
  n == null
    ? "—"
    : n < 1024
      ? `${n} B`
      : n < 1024 ** 2
        ? `${(n / 1024).toFixed(1)} KB`
        : n < 1024 ** 3
          ? `${(n / 1024 ** 2).toFixed(1)} MB`
          : `${(n / 1024 ** 3).toFixed(1)} GB`;
const inputDate = (seconds?: string) => {
  if (!seconds) return "";
  const d = new Date(Number(seconds) * 1000);
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
};
const dayKey = (n: number) =>
  new Intl.DateTimeFormat("sv-SE", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(n * 1000);
const dayLabel = (n: number) => {
  const day = dayKey(n),
    now = Date.now() / 1000;
  return day === dayKey(now)
    ? "今天"
    : day === dayKey(now - 86400)
      ? "昨天"
      : day;
};
const clock = (n: number) =>
  new Date(n * 1000).toLocaleTimeString("zh-CN", {
    timeZone: "Asia/Shanghai",
    hour12: false,
    hour: "2-digit",
    minute: "2-digit",
  });
const stamp = (n?: number) =>
  n
    ? new Date(n * 1000).toLocaleString("zh-CN", {
        timeZone: "Asia/Shanghai",
        hour12: false,
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "—";
const statusName: any = {
  pending: "进行中",
  completed: "记录完整",
  failed: "请求失败",
  cancelled: "已取消",
  interrupted: "已中断",
  queued: "等待上传",
  uploading: "上传中",
  retry: "等待重试",
  blocked: "需要处理",
  verified: "已验证",
  running: "生成中",
  ready: "可下载",
  cancelled_export: "已取消",
};
const iconPaths: any = {
  search: "m21 21-5-5M19 11a8 8 0 1 1-16 0 8 8 0 0 1 16 0",
  chat: "M21 15a3 3 0 0 1-3 3H8l-5 3V6a3 3 0 0 1 3-3h12a3 3 0 0 1 3 3z",
  folder: "M3 7V5a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v11H3z",
  download: "M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5",
  cloud: "M6 18a5 5 0 1 1 1-10 6 6 0 0 1 11 1 4.5 4.5 0 0 1 0 9z",
  settings:
    "M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8M9 3l-1 3-3 1-2 3 2 2-1 3 2 3 3-1 3 2 3-2 3 1 2-3-1-3 2-2-2-3-3-1-1-3z",
  user: "M16 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0M3 21v-2a7 7 0 0 1 7-7h4a7 7 0 0 1 7 7v2z",
  plus: "M12 4v16M4 12h16",
  filter: "M3 4h18l-7 8v7l-4 2v-9z",
  chevron: "m9 5 7 7-7 7",
  close: "m6 6 12 12M6 18 18 6",
  check: "m5 12 4 4L19 6",
  attach: "m8 12 6-6a3 3 0 0 1 4 4l-8 8a5 5 0 0 1-7-7l9-9",
  more: "M5 12h.01M12 12h.01M19 12h.01",
  copy: "M9 8h12v13H9zM15 8V3H3v13h6",
  up: "M12 21V3m-7 7 7-7 7 7",
  file: "M5 2h9l5 5v15H5zM14 2v6h5",
  panel: "M3 4h18v16H3zM15 4v16",
  logout: "M9 4H3v16h6M9 12h12m-5-5 5 5-5 5",
  refresh: "M20 7v5h-5M4 17v-5h5M6 6a8 8 0 0 1 14 6M18 18A8 8 0 0 1 4 12",
};
function Icon({ name, size = 18 }: any) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={iconPaths[name] || iconPaths.file} />
    </svg>
  );
}
function BrandIcon() {
  return (
    <svg
      className="brand-mark"
      viewBox="0 0 28 28"
      fill="currentColor"
      aria-hidden="true"
    >
      <path d="m13.8 2.3 10.8 6.2v9.1l-6.5 3.8v-9.1L9.9 7.6z" />
      <path d="m9.9 8.8 5.8 3.3v8.3l-2.1 1.2v4.1L3.1 19.6v-6.9z" />
    </svg>
  );
}
function DriveIcon() {
  return (
    <svg width="23" height="22" viewBox="0 0 24 22" aria-hidden="true">
      <path fill="#0f9d58" d="M8 1h7l-8 14H0z" />
      <path fill="#fbbc04" d="m15 1 9 15h-7L8 1z" />
      <path fill="#4285f4" d="m0 15 4 6h17l3-6z" />
    </svg>
  );
}
const Markdown = React.memo(function Markdown({ text }: { text: string }) {
  const markup = useMemo(() => {
    const fragment = DOMPurify.sanitize(
      marked.parse(text || "", { async: false }) as string,
      {
        RETURN_DOM_FRAGMENT: true,
        ALLOWED_TAGS: [
          "p",
          "br",
          "strong",
          "em",
          "del",
          "ul",
          "ol",
          "li",
          "blockquote",
          "pre",
          "code",
          "h1",
          "h2",
          "h3",
          "h4",
          "table",
          "thead",
          "tbody",
          "tr",
          "th",
          "td",
          "hr",
        ],
        ALLOWED_ATTR: ["class"],
      },
    );
    fragment.querySelectorAll("[class]").forEach((el) => {
      if (
        el.tagName !== "CODE" ||
        !/^language-[a-z0-9_+#-]{1,28}$/i.test(el.className)
      )
        el.removeAttribute("class");
    });
    fragment.querySelectorAll("pre").forEach((pre) => {
      const code = pre.querySelector("code");
      if (!code) return;
      const bar = document.createElement("div");
      bar.className = "code-toolbar";
      const language = document.createElement("span");
      language.textContent =
        code.className.replace("language-", "").toUpperCase() || "纯文本";
      const button = document.createElement("button");
      button.type = "button";
      button.className = "code-copy";
      button.textContent = "复制";
      button.setAttribute("aria-label", "复制代码");
      const gutter = document.createElement("span");
      gutter.className = "code-gutter";
      gutter.setAttribute("aria-hidden", "true");
      gutter.textContent = (code.textContent || "")
        .replace(/\n$/, "")
        .split("\n")
        .map((_, i) => String(i + 1))
        .join("\n");
      bar.append(language, button);
      pre.prepend(bar, gutter);
    });
    const host = document.createElement("div");
    host.append(fragment);
    return { __html: host.innerHTML };
  }, [text]);
  const copy = (event: React.MouseEvent<HTMLDivElement>) => {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>(
      "button.code-copy",
    );
    if (!button) return;
    const source =
      button.closest("pre")?.querySelector("code")?.textContent || "";
    void navigator.clipboard.writeText(source).then(
      () => {
        button.textContent = "已复制";
      },
      () => {
        button.textContent = "复制失败";
      },
    );
  };
  return (
    <div className="markdown" onClick={copy} dangerouslySetInnerHTML={markup} />
  );
});
function Empty({ title, detail, children }: any) {
  return (
    <div className="empty">
      <Icon name="chat" size={32} />
      <h2>{title}</h2>
      <p>{detail}</p>
      {children}
    </div>
  );
}
function Badge({ value }: any) {
  return <span className={"badge " + value}>{statusName[value] || value}</span>;
}

export function App() {
  const [user, setUser] = useState<any>(undefined),
    [demo, setDemo] = useState(false),
    [token, setToken] = useState(""),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false);
  const [page, setPage] = useState("conversations"),
    [directory, setDirectory] = useState<any>({ users: [], groups: [] }),
    [stats, setStats] = useState<any>({}),
    [drive, setDrive] = useState<any>({}),
    [settings, setSettings] = useState<any>({});
  const [records, setRecords] = useState<any[]>([]),
    [cursor, setCursor] = useState<string | null>(null),
    [selected, setSelected] = useState<any>(null),
    [details, setDetails] = useState<any[]>([]),
    [earlier, setEarlier] = useState<string | null>(null),
    [loading, setLoading] = useState(false);
  const [scope, setScope] = useState<any>({}),
    [search, setSearch] = useState(""),
    [query, setQuery] = useState(""),
    [localSearch, setLocalSearch] = useState(""),
    [listQuery, setListQuery] = useState(""),
    [tab, setTab] = useState("all"),
    [raw, setRaw] = useState(false),
    [inspector, setInspector] = useState(
      () => matchMedia("(min-width: 78.1875em)").matches,
    ),
    [mobileDirectory, setMobileDirectory] = useState(false),
    [expanded, setExpanded] = useState<Set<string>>(new Set()),
    [checked, setChecked] = useState<Set<string>>(new Set());
  const [dialog, setDialog] = useState(""),
    [form, setForm] = useState<any>({}),
    [queue, setQueue] = useState<any[]>([]),
    [exports, setExports] = useState<any[]>([]),
    [audits, setAudits] = useState<any[]>([]),
    [grants, setGrants] = useState<number[]>([]),
    [follow, setFollow] = useState(true),
    [mobile, setMobile] = useState("list");
  const seededExpansion = useRef(false);
  const searchRef = useRef<HTMLInputElement>(null),
    readerRef = useRef<HTMLDivElement>(null),
    dialogRef = useRef<HTMLDialogElement>(null),
    detailGeneration = useRef(0),
    listGeneration = useRef(0);
  const act = useCallback(async (fn: () => Promise<any>) => {
    setBusy(true);
    try {
      return await fn();
    } catch (e: any) {
      setNotice(e.message);
      if (e.status === 401) setUser(null);
    } finally {
      setBusy(false);
    }
  }, []);
  const refresh = useCallback(async () => {
    const [d, s, g, t] = await Promise.all([
      api("/directory"),
      api("/stats"),
      api("/drive/status"),
      api("/settings"),
    ]);
    setDirectory(d);
    if (!seededExpansion.current && d.users[0]) {
      seededExpansion.current = true;
      const first = d.users[0].id,
        group = d.groups.find((g: any) => g.user_id === first);
      setExpanded(
        new Set(["u" + first, ...(group ? ["p" + first + group.project] : [])]),
      );
    }
    setStats(s);
    setDrive(g);
    setSettings(t);
  }, []);
  useEffect(() => {
    api("/public-config")
      .then((x) => setDemo(x.demo))
      .catch(() => {});
    api("/session")
      .then((x) => {
        setUser(x.user);
        setDemo(x.demo);
      })
      .catch(() => setUser(null));
  }, []);
  useEffect(() => {
    if (user) void act(refresh);
  }, [user?.id]);
  useEffect(() => {
    const t = setTimeout(() => {
      setQuery(search.trim());
      setListQuery(localSearch.trim());
    }, 220);
    return () => clearTimeout(t);
  }, [search, localSearch]);
  useEffect(() => {
    const media = matchMedia("(min-width: 78.1875em)");
    const resize = () => {
      setInspector(media.matches);
      setMobileDirectory(false);
    };
    media.addEventListener("change", resize);
    return () => media.removeEventListener("change", resize);
  }, []);
  const filters = useMemo(
    () => ({
      ...scope,
      ...(query ? { q: query } : {}),
      ...(listQuery ? { local_q: listQuery } : {}),
      ...(tab === "attachment" ? { attachment: "1" } : {}),
      ...(tab === "abnormal" ? { abnormal: "1" } : {}),
    }),
    [scope, query, listQuery, tab],
  );
  const loadList = useCallback(
    async (more = false) => {
      const generation = ++listGeneration.current;
      const result = await api(
        "/records?" +
          new URLSearchParams({
            ...filters,
            limit: "60",
            ...(more && cursor ? { cursor } : {}),
          }),
      );
      if (generation !== listGeneration.current) return;
      setRecords((old) =>
        more ? [...old, ...result.records] : result.records,
      );
      setCursor(result.next_cursor);
    },
    [filters, cursor],
  );
  useEffect(() => {
    if (user) {
      setChecked(new Set());
      void act(() => loadList());
    }
  }, [user?.id, filters]);
  useEffect(() => {
    if (!user) return;
    const id = setInterval(() => {
      if (document.hidden) return;
      void refresh().catch(() => {});
      if (page === "storage")
        void api("/queue")
          .then(setQueue)
          .catch(() => {});
      if (page === "exports")
        void api("/exports")
          .then(setExports)
          .catch(() => {});
    }, 5000);
    return () => clearInterval(id);
  }, [user?.id, page, refresh]);
  useEffect(() => {
    if (user && page === "storage")
      void act(async () => setQueue(await api("/queue")));
    if (user && page === "exports")
      void act(async () => setExports(await api("/exports")));
    if (user && page === "settings")
      void act(async () => {
        if (user.role >= 100) {
          setGrants(await api("/grants"));
          setAudits(await api("/audits"));
        }
      });
  }, [page, user?.id]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        searchRef.current?.focus();
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);
  useEffect(() => {
    if (!notice) return;
    const timer = setTimeout(() => setNotice(""), 8000);
    return () => clearTimeout(timer);
  }, [notice]);
  useEffect(() => {
    if (dialog) {
      dialogRef.current?.showModal();
    } else if (dialogRef.current?.open) dialogRef.current.close();
  }, [dialog]);
  useEffect(() => {
    if (follow && readerRef.current)
      readerRef.current.scrollTo({
        top: readerRef.current.scrollHeight,
        behavior: "instant",
      });
  }, [details, follow]);
  useEffect(() => {
    if (!selected || !user || !details.length) return;
    let stale = false,
      running = false;
    const id = setInterval(() => {
      if (document.hidden || running) return;
      running = true;
      void Promise.all(
        details.map(async (d) => {
          if (d.record.status === "pending")
            return api("/records/" + d.record.id);
          if (d.sync === "verified") return d;
          return {
            ...d,
            ...(await api("/records/" + d.record.id + "/status")),
          };
        }),
      )
        .then((next) => {
          if (
            !stale &&
            next.some(
              (d, i) =>
                d.record.updated !== details[i].record.updated ||
                d.sync !== details[i].sync ||
                d.attachments.length !== details[i].attachments.length ||
                d.record.status !== details[i].record.status,
            )
          )
            setDetails(next);
        })
        .catch(() => {})
        .finally(() => {
          running = false;
        });
    }, 3500);
    return () => {
      stale = true;
      clearInterval(id);
    };
  }, [selected?.id, details, user?.id]);
  const login = async (value: string) =>
    act(async () => {
      const result = await api("/session", { token: value });
      setToken("");
      setUser(result.user);
    });
  const consoleLogin = () =>
    void act(async () => {
      const res = await fetch("/api/user/auth/refresh", {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
      });
      const result = await res.json().catch(() => ({}));
      if (!res.ok || result.success !== true || !result.data?.access_token) {
        setNotice("请先登录 OmniAPI 控制台，然后回到此页验证。");
        return;
      }
      await login(result.data.access_token);
    });
  async function select(row: any, older = false) {
    const gen = ++detailGeneration.current;
    setLoading(true);
    if (!older) {
      setMobileDirectory(false);
      setSelected(row);
      setDetails([]);
      setRaw(false);
      setFollow(true);
      setMobile("reader");
    }
    try {
      let batch;
      if (row.conversation) {
        batch = await api(
          "/records?" +
            new URLSearchParams({
              user: String(row.user_id),
              project: row.project,
              conversation: row.conversation,
              limit: "20",
              ...(older && earlier ? { cursor: earlier } : {}),
            }),
        );
      } else batch = { records: [row], next_cursor: null };
      const result = await Promise.all(
        batch.records.map((r: any) => api("/records/" + r.id)),
      );
      if (gen !== detailGeneration.current) return;
      setDetails((old) =>
        older ? [...result.reverse(), ...old] : result.reverse(),
      );
      setEarlier(batch.next_cursor);
    } catch (e: any) {
      setNotice(e.message);
    } finally {
      if (gen === detailGeneration.current) setLoading(false);
    }
  }
  const selectGroup = (
    user: number,
    project: string,
    conversation: string,
    row?: any,
  ) => {
    const target = { user: String(user), project, conversation };
    setScope(target);
    setPage("conversations");
    setTab("all");
    setSearch("");
    setLocalSearch("");
    setMobileDirectory(false);
    if (row) void select(row);
    else
      void act(async () => {
        const result = await api(
          "/records?" + new URLSearchParams({ ...target, limit: "1" }),
        );
        if (result.records[0]) void select(result.records[0]);
        else {
          detailGeneration.current++;
          setSelected(null);
          setDetails([]);
          setEarlier(null);
          setMobile("list");
        }
      });
  };
  const toggle = (key: string) =>
    setExpanded((old) => {
      const next = new Set(old);
      next.has(key) ? next.delete(key) : next.add(key);
      return next;
    });
  const openDialog = (name: string, initial: any = {}) => {
    setForm(initial);
    setDialog(name);
  };
  const visibleRows = useMemo(() => {
    const seen = new Set<string>();
    return records.filter((r) => {
      const key = r.conversation
        ? [r.user_id, r.project, r.conversation].join("\x00")
        : r.id;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
  }, [records]);
  const transcript = useMemo(() => {
    let prior: any[] = [];
    return details.map((d) => {
      let format = "json";
      try {
        format = JSON.parse(d.payloads.request).input_format || "json";
      } catch {}
      const view = normalize(d.payloads.input, d.payloads.output, format);
      const prefix = repeatedPrefix(prior, view.messages);
      prior = view.messages;
      return {
        detail: d,
        view,
        messages: prefix ? view.messages.slice(prefix) : view.messages,
        hidden: prefix,
      };
    });
  }, [details]);
  const current = details.at(-1),
    attachments = details.flatMap((d) => d.attachments),
    currentUser = directory.users.find((u: any) => u.id === selected?.user_id),
    currentGroup = selected?.conversation
      ? directory.groups.find(
          (g: any) =>
            g.user_id === selected.user_id &&
            g.project === selected.project &&
            g.conversation === selected.conversation,
        )
      : null,
    quota = drive.health?.storageQuota,
    capacity = Number(quota?.limit || 0),
    used = Number(quota?.usage || 0),
    percentage = capacity ? Math.min(100, (used / capacity) * 100) : 0;
  const selectedFilters = () =>
    selected?.conversation
      ? {
          user: String(selected.user_id),
          project: selected.project,
          conversation: selected.conversation,
        }
      : { ...filters };
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    await act(async () => {
      if (dialog === "group")
        await api("/group", {
          request_ids: checked.size ? [...checked] : [selected.id],
          project: form.project || "",
          conversation: form.conversation || "",
        });
      if (dialog === "rename")
        await api("/user-name", { id: form.id, name: form.name });
      if (dialog === "export") {
        const job = await api("/exports", {
          format: form.format || "html",
          filters: form.scope === "current" ? selectedFilters() : filters,
          ...(form.scope === "current" && !selected?.conversation
            ? { request_ids: [selected.id] }
            : {}),
        });
        setExports((old) => [job, ...old]);
        setPage("exports");
        setNotice("导出任务已开始，可在导出中心下载");
      }
      if (dialog === "filter") {
        if (form.start && form.end && form.start >= form.end)
          throw Error("结束时间应晚于开始时间");
        const {
          user: _user,
          model: _model,
          start: _start,
          end: _end,
          ...rest
        } = scope;
        setScope({
          ...((form.user || "") === (scope.user || "") ? rest : {}),
          ...(form.user ? { user: form.user } : {}),
          ...(form.model ? { model: form.model.trim() } : {}),
          ...(form.start
            ? { start: String(new Date(form.start).getTime() / 1000) }
            : {}),
          ...(form.end
            ? { end: String(new Date(form.end).getTime() / 1000) }
            : {}),
        });
        setPage("conversations");
        setMobile("list");
      }
      if (dialog === "collection")
        await api("/collections", {
          user_id: Number(form.user),
          project: form.project || "",
          conversation: form.conversation || "",
          title: form.title || form.conversation || form.project,
        });
      if (dialog === "grant") {
        await api("/grants", { user_id: Number(form.user), allowed: true });
        setGrants(await api("/grants"));
      }
      setDialog("");
      await refresh();
      await loadList();
    });
  }
  const connect = () =>
    act(async () => {
      const popup = window.open(
        "about:blank",
        "archive-google",
        "popup,width=580,height=740",
      );
      try {
        const result = await api("/drive/connect", {});
        if (popup) popup.location.href = result.url;
        else location.href = result.url;
      } catch (e) {
        popup?.close();
        throw e;
      }
    });
  if (user === undefined)
    return (
      <div className="login">
        <div className="brand">
          <BrandIcon /> OmniAPI
        </div>
        <p>正在确认管理员身份…</p>
      </div>
    );
  if (!user)
    return (
      <main className="login">
        <div className="brand">
          <BrandIcon /> OmniAPI
        </div>
        <h1>对话封存</h1>
        <p>使用控制台管理员身份进入工作台。</p>
        <button className="primary" onClick={consoleLogin}>
          使用控制台身份登录
        </button>
        <details>
          <summary>使用短期控制台令牌</summary>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void login(token);
            }}
          >
            <label>
              管理员登录令牌
              <input
                type="password"
                autoComplete="off"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                required
              />
            </label>
            <button disabled={busy}>验证身份</button>
          </form>
        </details>
        {demo && (
          <button onClick={() => void login("archive-v3-local-demo")}>
            进入本地合成验收
          </button>
        )}
        {notice && (
          <p role="alert">
            {notice}{" "}
            {!demo && (
              <a href="/sign-in" target="_blank" rel="noreferrer">
                打开控制台登录 ↗
              </a>
            )}
          </p>
        )}
        <small>不接受模型 API Key；登录身份不会写入对话档案。</small>
      </main>
    );
  return (
    <div
      className={
        "app mobile-" +
        mobile +
        (inspector ? "" : " inspector-closed") +
        (mobileDirectory ? " directory-open" : "")
      }
    >
      <header className="topbar">
        <button
          className="brand"
          onClick={() => {
            setPage("conversations");
            setScope({});
            setMobile("list");
          }}
          aria-label="OmniAPI 全部对话"
        >
          <BrandIcon />
          OmniAPI
        </button>
        <span className="workspace-name">
          {settings.workspace_name || "对话封存"}
        </span>
        <label className="global-search">
          <Icon name="search" />
          <input
            ref={searchRef}
            placeholder="搜索用户、会话或内容"
            aria-label="全局搜索"
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              setScope({});
              setTab("all");
              setPage("conversations");
              setMobile("list");
              setMobileDirectory(false);
            }}
          />
          <kbd>⌘K</kbd>
        </label>
        <button className="drive-chip" onClick={() => setPage("storage")}>
          <DriveIcon />
          <span>Google Drive</span>
          <small>{drive.connected ? "已连接" : "未连接"}</small>
        </button>
        <button
          className={"sync-state " + (drive.error ? "warning" : "")}
          onClick={() => setPage("storage")}
        >
          <i />
          {drive.error
            ? "同步需处理"
            : !drive.connected
              ? "等待连接"
              : stats.queued_items
                ? "同步中"
                : "同步正常"}
        </button>
        <button
          className="avatar operator"
          title="管理员设置"
          onClick={() => setPage("settings")}
        >
          {String(user.username || "Z")
            .slice(0, 1)
            .toUpperCase()}
        </button>
      </header>
      <aside className="sidebar">
        <nav aria-label="封存工作台">
          {[
            ["conversations", "chat", "全部对话"],
            ["exports", "download", "导出中心"],
            ["storage", "cloud", "存储与同步"],
          ].map(([id, icon, label]) => (
            <button
              key={id}
              className={page === id ? "nav-active" : ""}
              onClick={() => {
                setPage(id);
                setMobile("list");
                setMobileDirectory(false);
                if (id === "conversations") {
                  setScope({});
                  setSearch("");
                  setLocalSearch("");
                }
              }}
            >
              <Icon name={icon} />
              {label}
            </button>
          ))}
          <button
            className="mobile-directory"
            aria-label="用户目录"
            aria-expanded={mobileDirectory}
            onClick={() => {
              setMobileDirectory(!mobileDirectory);
              setInspector(false);
            }}
          >
            <Icon name="folder" />
          </button>
          <button
            className="mobile-settings"
            aria-label="设置"
            onClick={() => {
              setPage("settings");
              setMobileDirectory(false);
            }}
          >
            <Icon name="settings" />
          </button>
        </nav>
        <div className="tree-heading">
          <span>用户目录</span>
          <button
            className="icon-button"
            title="新建整理目录"
            aria-label="新建整理目录"
            onClick={() =>
              openDialog("collection", {
                user: scope.user || directory.users[0]?.id || "",
              })
            }
          >
            <Icon name="plus" size={15} />
          </button>
          <button
            className="icon-button"
            title="筛选目录"
            aria-label="筛选目录"
            onClick={() =>
              openDialog("filter", {
                ...scope,
                start: inputDate(scope.start),
                end: inputDate(scope.end),
              })
            }
          >
            <Icon name="filter" size={15} />
          </button>
        </div>
        <div className="tree" tabIndex={0} aria-label="用户项目与会话目录">
          {directory.users.map((u: any) => {
            const groups = directory.groups.filter(
                (g: any) => g.user_id === u.id,
              ),
              projects = [
                ...new Set(groups.map((g: any) => g.project)),
              ] as string[];
            return (
              <div key={u.id}>
                <div className="user-row">
                  <button
                    className="tree-node user-node"
                    aria-expanded={expanded.has("u" + u.id)}
                    onClick={() => {
                      if (
                        !expanded.has("u" + u.id) ||
                        scope.user === String(u.id)
                      )
                        toggle("u" + u.id);
                      setScope({ user: String(u.id) });
                      setPage("conversations");
                      setMobile("list");
                    }}
                  >
                    <span
                      className={
                        "caret " + (expanded.has("u" + u.id) ? "open" : "")
                      }
                    >
                      <Icon name="chevron" size={13} />
                    </span>
                    <Icon name="user" />
                    <span>
                      用户 {u.id}
                      <small>{u.name === "用户 " + u.id ? "" : u.name}</small>
                    </span>
                  </button>
                  <button
                    className="tree-edit"
                    aria-label={"编辑用户 " + u.id + " 显示名"}
                    onClick={() =>
                      openDialog("rename", { id: u.id, name: u.name })
                    }
                  >
                    <Icon name="more" size={14} />
                  </button>
                </div>
                {expanded.has("u" + u.id) &&
                  projects.map((p) => (
                    <div key={p} className="project-node">
                      <button
                        className="tree-node"
                        aria-expanded={expanded.has("p" + u.id + p)}
                        onClick={() => {
                          if (
                            !expanded.has("p" + u.id + p) ||
                            (scope.user === String(u.id) && scope.project === p)
                          )
                            toggle("p" + u.id + p);
                          setScope({ user: String(u.id), project: p });
                          setPage("conversations");
                          setMobile("list");
                        }}
                      >
                        <span
                          className={
                            "caret " +
                            (expanded.has("p" + u.id + p) ? "open" : "")
                          }
                        >
                          <Icon name="chevron" size={13} />
                        </span>
                        <Icon name="folder" />
                        <span>{p || "默认项目"}</span>
                      </button>
                      {expanded.has("p" + u.id + p) &&
                        groups
                          .filter((g: any) => g.project === p)
                          .map((g: any) => {
                            const row = records.find(
                              (r) =>
                                r.user_id === u.id &&
                                r.project === p &&
                                r.conversation === g.conversation,
                            );
                            return (
                              <button
                                key={g.conversation}
                                className={
                                  "tree-node conversation-node " +
                                  (scope.user === String(u.id) &&
                                  scope.project === p &&
                                  scope.conversation === g.conversation
                                    ? "chosen"
                                    : "")
                                }
                                onClick={() =>
                                  selectGroup(u.id, p, g.conversation, row)
                                }
                              >
                                <Icon
                                  name={g.conversation ? "chat" : "folder"}
                                  size={16}
                                />
                                <span>
                                  {g.title ||
                                    row?.title ||
                                    g.conversation ||
                                    "未分类请求"}
                                </span>
                                <small>{g.count}</small>
                              </button>
                            );
                          })}
                    </div>
                  ))}
              </div>
            );
          })}
          {!directory.users.length && (
            <p className="muted tree-empty">尚无已采集用户</p>
          )}
        </div>
        <div className="drive-meter">
          <div>
            <DriveIcon />
            <b>Google Drive</b>
          </div>
          <p>
            {capacity
              ? `${bytes(used)} / ${bytes(capacity)}`
              : "连接后显示空间"}
            <span>{capacity ? percentage.toFixed(0) + "%" : ""}</span>
          </p>
          <progress
            max="100"
            value={percentage}
            aria-label="Google Drive 空间使用量"
          />
          <small>最近同步 {stamp(drive.last_sync)}</small>
        </div>
        <button className="settings-link" onClick={() => setPage("settings")}>
          <Icon name="settings" />
          设置
        </button>
      </aside>
      {page === "conversations" ? (
        <>
          <section className="conversation-list">
            <div className="list-heading">
              <div>
                <h2>
                  {scope.project ||
                    (scope.user ? "用户 " + scope.user : "全部对话")}
                </h2>
                <small>
                  {visibleRows.length} 个已加载会话{cursor ? " · 还有更多" : ""}
                </small>
              </div>
              <button
                className="icon-button"
                aria-label="批量整理请求"
                title="批量整理请求"
                disabled={!checked.size && !selected}
                onClick={() =>
                  openDialog("group", {
                    project: scope.project || "",
                    conversation: scope.conversation || "",
                  })
                }
              >
                <Icon name="more" />
              </button>
            </div>
            <label className="local-search">
              <Icon name="search" />
              <input
                aria-label="搜索当前目录"
                placeholder="搜索当前列表"
                value={localSearch}
                onChange={(e) => setLocalSearch(e.target.value)}
              />
            </label>
            <div className="segments">
              {[
                ["all", "全部"],
                ["attachment", "含附件"],
                ["abnormal", "异常"],
              ].map(([id, label]) => (
                <button
                  key={id}
                  className={tab === id ? "active" : ""}
                  onClick={() => setTab(id)}
                >
                  {label}
                </button>
              ))}
            </div>
            {Object.keys(scope).length > 0 && (
              <button className="clear-filter" onClick={() => setScope({})}>
                清除范围筛选 ×
              </button>
            )}
            <div className="list-scroll" tabIndex={0}>
              {visibleRows.map((r, i) => (
                <React.Fragment key={r.id}>
                  {(i === 0 ||
                    dayKey(r.updated) !==
                      dayKey(visibleRows[i - 1].updated)) && (
                    <h3 className="list-day">{dayLabel(r.updated)}</h3>
                  )}
                  <div
                    className={
                      "list-row " + (selected?.id === r.id ? "selected" : "")
                    }
                  >
                    <input
                      type="checkbox"
                      aria-label={"选择 " + r.title}
                      checked={checked.has(r.id)}
                      onChange={(e) =>
                        setChecked((old) => {
                          const s = new Set(old);
                          e.target.checked ? s.add(r.id) : s.delete(r.id);
                          return s;
                        })
                      }
                    />
                    <button className="row-main" onClick={() => void select(r)}>
                      <div>
                        <b>{r.title || "正在采集…"}</b>
                        <time
                          dateTime={new Date(r.updated * 1000).toISOString()}
                          title={stamp(r.updated)}
                        >
                          {clock(r.updated)}
                        </time>
                      </div>
                      <p>{r.preview || "等待输出内容"}</p>
                      <span className="row-meta">
                        <span>{r.model || "未知模型"}</span>
                        {r.attachment_count > 0 && (
                          <>
                            <Icon name="attach" size={13} />
                            {r.attachment_count}
                          </>
                        )}
                        {r.status !== "completed" && <Badge value={r.status} />}
                      </span>
                    </button>
                  </div>
                </React.Fragment>
              ))}
              {!records.length && (
                <Empty
                  title="没有匹配的记录"
                  detail="调整搜索条件，或等待网关采集新请求。"
                />
              )}
              {cursor && (
                <button
                  className="load-more"
                  disabled={busy}
                  onClick={() => void act(() => loadList(true))}
                >
                  加载更多
                </button>
              )}
            </div>
          </section>
          <main className="reader">
            <div className="reader-header">
              <button className="mobile-back" onClick={() => setMobile("list")}>
                ‹ 返回列表
              </button>
              <div className="reader-breadcrumb">
                {selected
                  ? `用户 ${selected.user_id} / ${selected.project || "默认项目"}`
                  : "选择一个会话开始阅读"}
              </div>
              <div className="reader-title">
                <h1>{selected?.title || "对话记录"}</h1>
                <div>
                  <button
                    disabled={!selected}
                    onClick={() =>
                      openDialog("export", { format: "html", scope: "current" })
                    }
                  >
                    <Icon name="download" />
                    导出
                  </button>
                  <button
                    className="icon-button"
                    aria-label="显示或隐藏会话信息"
                    aria-pressed={inspector}
                    onClick={() => setInspector((v) => !v)}
                  >
                    <Icon name="panel" />
                  </button>
                  <button
                    className="icon-button"
                    aria-label="整理当前请求"
                    disabled={!selected}
                    onClick={() =>
                      openDialog("group", {
                        project: selected.project,
                        conversation: selected.conversation,
                      })
                    }
                  >
                    <Icon name="more" />
                  </button>
                </div>
              </div>
              <p>
                {selected
                  ? `${stamp(selected.created)} · ${details.length} 个已加载请求 · ${transcript.reduce((n, t) => n + t.messages.length, 0)} 条消息`
                  : "完整保存实际经过网关的内容"}
              </p>
              <div className="segments reader-tabs">
                <button
                  className={!raw ? "active" : ""}
                  onClick={() => setRaw(false)}
                >
                  对话
                </button>
                <button
                  className={raw ? "active" : ""}
                  onClick={() => setRaw(true)}
                >
                  原始记录
                </button>
              </div>
            </div>
            <div
              ref={readerRef}
              className="reader-body"
              tabIndex={0}
              onScroll={(e) => {
                const el = e.currentTarget;
                setFollow(
                  el.scrollHeight - el.scrollTop - el.clientHeight < 60,
                );
              }}
            >
              {earlier && (
                <button
                  className="load-more"
                  onClick={() => void select(selected, true)}
                  disabled={loading}
                >
                  加载更早的请求
                </button>
              )}
              {loading && !details.length ? (
                <Empty title="正在读取记录" detail="正在校验并整理会话内容…" />
              ) : !selected ? (
                <Empty
                  title="选择一段对话"
                  detail="从左侧用户目录或会话列表中选择记录。"
                />
              ) : raw ? (
                details.map((d) => (
                  <section key={d.record.id} className="raw-record">
                    <h3>请求 {d.record.id}</h3>
                    {Object.entries(d.payloads).map(([kind, text]) => (
                      <details
                        key={kind}
                        open={kind === "input" || kind === "output"}
                      >
                        <summary>{kind}</summary>
                        <pre>{String(text)}</pre>
                      </details>
                    ))}
                  </section>
                ))
              ) : (
                transcript.map(
                  ({ detail: d, view, messages: items, hidden }) => (
                    <section className="request-block" key={d.record.id}>
                      {hidden > 0 && (
                        <details className="history-prefix">
                          <summary>已折叠 {hidden} 条重复历史消息</summary>
                          <p>重复历史仍保存在原始请求中。</p>
                        </details>
                      )}
                      {items.map((m: any, i: number) => (
                        <article
                          className={"message " + m.role}
                          key={d.record.id + "-" + i}
                        >
                          <div
                            className={
                              "avatar " +
                              (m.role === "assistant" ? "model" : "")
                            }
                          >
                            {m.role === "assistant"
                              ? <BrandIcon />
                              : m.role === "user"
                                ? (currentUser?.name || "用").slice(0, 1)
                                : "⋯"}
                          </div>
                          <div className="message-content">
                            <div className="message-heading">
                              <b>
                                {m.role === "assistant"
                                  ? d.record.model || "模型"
                                  : m.role === "user"
                                    ? currentUser?.name ||
                                      "用户 " + d.record.user_id
                                    : (
                                        {
                                          system: "系统提示",
                                          developer: "开发者提示",
                                          tool: "工具结果",
                                        } as any
                                      )[m.role] || m.role}
                              </b>
                              <span>·</span>
                              <time>{stamp(d.record.created)}</time>
                              <button
                                className="message-copy"
                                title="复制消息"
                                aria-label="复制消息"
                                onClick={() =>
                                  void act(async () => {
                                    await navigator.clipboard.writeText(
                                      m.text || m.tools || "",
                                    );
                                    setNotice("消息已复制");
                                  })
                                }
                              >
                                <Icon name="copy" size={14} />
                              </button>
                            </div>
                            {m.text && <Markdown text={m.text} />}{" "}
                            {m.thinking && (
                              <details>
                                <summary>已返回的思考内容</summary>
                                <Markdown text={m.thinking} />
                              </details>
                            )}
                            {m.tools && (
                              <details className="tool-call">
                                <summary>
                                  <Icon name="chevron" size={13} />
                                  工具调用 · 查看参数与结果
                                </summary>
                                <pre>{m.tools}</pre>
                              </details>
                            )}
                            {m.role === "user" && d.attachments.length > 0 && (
                              <div className="attachment-grid">
                                {d.attachments.map((a: any) => (
                                  <button
                                    className="attachment-tile"
                                    key={a.id}
                                    onClick={() => openDialog("attachment", a)}
                                  >
                                    {a.mime.startsWith("image/") &&
                                    a.object_id ? (
                                      <img
                                        src={endpoint(
                                          "/attachments/" + a.id + "?preview=1",
                                        )}
                                        alt="附件缩略图"
                                      />
                                    ) : (
                                      <span
                                        className={
                                          "file-icon " +
                                          (a.mime === "application/pdf"
                                            ? "pdf"
                                            : "")
                                        }
                                      >
                                        <Icon name="file" size={24} />
                                      </span>
                                    )}
                                    <span>
                                      <b>{a.name}</b>
                                      <small>
                                        {a.object_id
                                          ? bytes(a.size)
                                          : "仅外部引用"}
                                      </small>
                                    </span>
                                  </button>
                                ))}
                              </div>
                            )}
                          </div>
                        </article>
                      ))}
                      {view.unknown && (
                        <p className="inline-warning">
                          此协议无法完整解析，请查看原始记录。
                        </p>
                      )}
                      {view.error && (
                        <p role="alert" className="inline-warning">
                          {view.error}
                        </p>
                      )}
                      <div className="request-end">
                        <Badge value={d.record.status} />
                        <small>请求 {d.record.id.slice(0, 8)}</small>
                      </div>
                    </section>
                  ),
                )
              )}
            </div>
            <footer className="reader-footer">
              <span>
                <Icon name={current?.sync === "verified" ? "check" : "cloud"} />
                {current
                  ? current.sync === "verified"
                    ? "已归档 · 异地校验通过"
                    : current.sync === "blocked"
                      ? "本地已保存 · Drive 同步需处理"
                      : "本地已保存 · 等待 Drive 确认"
                  : "尚未选择记录"}
              </span>
              <button
                onClick={() => {
                  setFollow(true);
                  readerRef.current?.scrollTo({
                    top: readerRef.current.scrollHeight,
                    behavior: "instant",
                  });
                }}
              >
                <Icon name="up" size={15} />
                回到最新
              </button>
            </footer>
          </main>
          {inspector && (
            <aside className="inspector">
              <div className="inspector-title">
                <h2>会话信息</h2>
                <button
                  className="icon-button"
                  aria-label="关闭会话信息"
                  onClick={() => setInspector(false)}
                >
                  <Icon name="close" />
                </button>
              </div>
              {selected ? (
                <>
                  <dl>
                    {[
                      ["用户", "用户 " + selected.user_id],
                      ["项目", selected.project || "默认项目"],
                      ["会话", selected.conversation || "未分类"],
                      ["模型", selected.model || "未知"],
                      ["状态", statusName[current?.record.status] || "读取中"],
                      [
                        "开始时间",
                        stamp(currentGroup?.first || selected.created),
                      ],
                      ["请求数", currentGroup?.count || details.length],
                      ["附件", attachments.length + " 个"],
                    ].map(([key, value]) => (
                      <React.Fragment key={key}>
                        <dt>{key}</dt>
                        <dd>{value}</dd>
                      </React.Fragment>
                    ))}
                  </dl>
                  <section className="inspector-section">
                    <div className="section-title">
                      <DriveIcon />
                      <b>Google Drive</b>
                      <Badge
                        value={
                          current?.sync === "pending" || !current
                            ? "queued"
                            : current.sync
                        }
                      />
                    </div>
                    <small>归档位置</small>
                    <p className="archive-path">
                      用户-{selected.user_id} / {selected.project || "默认项目"}{" "}
                      / {selected.conversation || "未分类请求"}
                    </p>
                    {current?.drive_folder_id ? (
                      <a
                        className="text-link"
                        href={
                          "https://drive.google.com/drive/folders/" +
                          encodeURIComponent(current.drive_folder_id)
                        }
                        target="_blank"
                        rel="noreferrer"
                      >
                        在 Drive 中查看 ↗
                      </a>
                    ) : (
                      <button
                        className="text-link"
                        onClick={() => setPage("storage")}
                      >
                        {drive.connected
                          ? "查看同步进度 →"
                          : "连接 Google Drive →"}
                      </button>
                    )}
                    {current?.manifest?.path && (
                      <details className="physical-path">
                        <summary>查看实际存储路径</summary>
                        <small>{current.manifest.path}</small>
                      </details>
                    )}
                  </section>
                  <section className="inspector-section">
                    <h3>附件</h3>
                    <div className="attachment-list">
                      {attachments.map((a: any) => (
                        <button
                          key={a.id}
                          onClick={() => openDialog("attachment", a)}
                        >
                          <Icon name="file" />
                          <span>{a.name}</span>
                          <small>{bytes(a.size)}</small>
                        </button>
                      ))}
                      {!attachments.length && (
                        <small>此会话没有已取得附件</small>
                      )}
                    </div>
                  </section>
                  <details className="technical">
                    <summary>技术详情</summary>
                    {details.map((d) => (
                      <div key={d.record.id}>
                        <b>{d.record.id.slice(0, 8)}</b>
                        <pre>{d.payloads.final || "等待结束事件"}</pre>
                      </div>
                    ))}
                  </details>
                </>
              ) : (
                <p className="muted">选择会话后显示关联信息。</p>
              )}
            </aside>
          )}
        </>
      ) : (
        <main className="utility">
          <div className="utility-heading">
            <div>
              <p className="eyebrow">OmniAPI / 对话封存</p>
              <h1>
                {page === "exports"
                  ? "导出中心"
                  : page === "storage"
                    ? "存储与同步"
                    : "设置与权限"}
              </h1>
            </div>
            {page === "exports" && (
              <button
                className="primary"
                onClick={() =>
                  openDialog("export", { format: "html", scope: "filter" })
                }
              >
                <Icon name="download" />
                新建导出
              </button>
            )}
            {page === "storage" && (
              <button
                disabled={busy || user.role < 100}
                onClick={() =>
                  void act(async () => {
                    await api("/sync", {});
                    await refresh();
                    setQueue(await api("/queue"));
                    setNotice("已封口现有记录并重试可处理任务");
                  })
                }
              >
                <Icon name="refresh" />
                立即同步 / 重试
              </button>
            )}
          </div>
          {page === "storage" ? (
            <>
              <section className="storage-connection">
                <DriveIcon />
                <div>
                  <h2>个人 Google Drive</h2>
                  <p>
                    {drive.connected
                      ? drive.health?.user?.emailAddress ||
                        "已授权，等待容量检查"
                      : "尚未连接存储账号"}
                  </p>
                </div>
                <button
                  className="primary"
                  disabled={busy || user.role < 100}
                  onClick={() => void connect()}
                >
                  {drive.connected ? "重新授权" : "连接 Google Drive"}
                </button>
                <button
                  disabled={busy || !drive.connected}
                  onClick={() =>
                    void act(async () => {
                      setDrive(await api("/drive/refresh", {}));
                    })
                  }
                >
                  刷新容量
                </button>
              </section>
              {drive.error && (
                <p role="alert" className="inline-warning">
                  同步需要处理：{drive.error}。本地未确认记录会继续保留。
                </p>
              )}
              <div className="metric-row">
                {[
                  [
                    "空间使用",
                    capacity ? `${bytes(used)} / ${bytes(capacity)}` : "未取得",
                  ],
                  ["待上传对象", String(stats.queued_items || 0)],
                  ["待上传大小", bytes(stats.queued_bytes)],
                  [
                    "最老等待",
                    stats.queue_age_seconds
                      ? Math.floor(stats.queue_age_seconds / 60) + " 分钟"
                      : "—",
                  ],
                  ["本地占用", bytes(stats.local_bytes)],
                  ...(stats.max_local_bytes ? [["本地上限", (stats.max_local_bytes / 1e9).toLocaleString("zh-CN") + " GB"]] : []),
                ].map(([label, value]) => (
                  <div key={label}>
                    <small>{label}</small>
                    <strong>{value}</strong>
                  </div>
                ))}
              </div>
              <h2>同步队列</h2>
              <p className="muted">
                记录、附件及清单分别校验；以下显示最近 200 个对象。
              </p>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>用户与文件</th>
                      <th>类型</th>
                      <th>大小</th>
                      <th>状态</th>
                      <th>详情</th>
                    </tr>
                  </thead>
                  <tbody>
                    {queue.map((o) => (
                      <tr key={o.id}>
                        <td title={o.path}>{o.path}</td>
                        <td>{o.kind}</td>
                        <td>{bytes(o.size)}</td>
                        <td>
                          <Badge value={o.state} />
                        </td>
                        <td>{o.error || stamp(o.verified)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {!queue.length && (
                  <Empty
                    title="没有待展示对象"
                    detail="新请求封段后会自动进入上传队列。"
                  />
                )}
              </div>
            </>
          ) : page === "exports" ? (
            <>
              <p className="muted">
                导出 HTML、JSON 或
                Markdown，并包含实际取得的附件。全部为明文文件。
              </p>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>导出任务</th>
                      <th>创建时间</th>
                      <th>格式</th>
                      <th>请求数</th>
                      <th>状态</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {exports.map((j) => (
                      <tr key={j.id}>
                        <td>{j.id.slice(0, 10)}</td>
                        <td>{stamp(j.created)}</td>
                        <td>{j.format.toUpperCase()}</td>
                        <td>{j.count}</td>
                        <td>
                          <Badge value={j.state} />
                          {j.error}
                        </td>
                        <td>
                          {j.state === "ready" ? (
                            <a
                              className="button text-link"
                              href={endpoint("/exports/" + j.id + "/download")}
                            >
                              下载 ZIP · {bytes(j.bytes)}
                            </a>
                          ) : j.state === "running" ? (
                            <button
                              onClick={() =>
                                void act(async () => {
                                  await api("/exports/" + j.id, {
                                    cancel: true,
                                  });
                                  setExports(await api("/exports"));
                                })
                              }
                            >
                              取消
                            </button>
                          ) : null}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {!exports.length && (
                  <Empty
                    title="尚无导出任务"
                    detail="从会话阅读区导出当前会话，或按筛选范围创建任务。"
                  />
                )}
              </div>
            </>
          ) : (
            <>
              <section className="settings-section">
                <h2>工作台</h2>
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    void act(async () => {
                      setSettings(
                        await api("/settings", {
                          workspace_name: new FormData(e.currentTarget).get(
                            "workspace_name",
                          ),
                        }),
                      );
                      setNotice("工作台名称已保存");
                    });
                  }}
                >
                  <label>
                    工作台名称
                    <input
                      name="workspace_name"
                      defaultValue={settings.workspace_name}
                      maxLength={40}
                    />
                  </label>
                  <button disabled={user.role < 100}>保存</button>
                </form>
                <dl>
                  <dt>存储格式</dt>
                  <dd>明文 JSONL 与原始附件</dd>
                  <dt>保留策略</dt>
                  <dd>{settings.retention}</dd>
                  {settings.max_local_bytes > 0 && <>
                    <dt>本地上限</dt>
                    <dd>{(settings.max_local_bytes / 1e9).toLocaleString("zh-CN")} GB，包含索引、附件与导出</dd>
                  </>}
                  <dt>目录规则</dt>
                  <dd>{settings.root_directory}</dd>
                  <dt>Google 回调</dt>
                  <dd>{settings.oauth_callback || "尚未配置"}</dd>
                </dl>
              </section>
              {user.role >= 100 && (
                <>
                  <section className="settings-section">
                    <div className="section-title">
                      <h2>管理员授权</h2>
                      <button onClick={() => openDialog("grant")}>
                        添加授权
                      </button>
                    </div>
                    <p className="muted">
                      站长始终可访问。其他账号须同时满足网关管理员角色和封存专属授权。
                    </p>
                    {grants.map((uid) => (
                      <div className="grant-row" key={uid}>
                        <span>管理员 {uid}</span>
                        <button
                          onClick={() =>
                            void act(async () => {
                              setGrants(
                                await api("/grants", {
                                  user_id: uid,
                                  allowed: false,
                                }),
                              );
                            })
                          }
                        >
                          撤销授权
                        </button>
                      </div>
                    ))}
                  </section>
                  <section className="settings-section">
                    <h2>最近操作</h2>
                    <div className="table-wrap">
                      <table>
                        <thead>
                          <tr>
                            <th>时间</th>
                            <th>操作者</th>
                            <th>动作</th>
                            <th>目标</th>
                          </tr>
                        </thead>
                        <tbody>
                          {audits.map((a) => (
                            <tr key={a.id}>
                              <td>{stamp(a.created)}</td>
                              <td>{a.actor}</td>
                              <td>{a.action}</td>
                              <td>{a.target}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  </section>
                </>
              )}
              <button
                onClick={() =>
                  void act(async () => {
                    await api("/logout", {});
                    setUser(null);
                    setDetails([]);
                    setRecords([]);
                    setSelected(null);
                    setEarlier(null);
                    setPage("conversations");
                    setMobile("list");
                    setScope({});
                    setSearch("");
                    setLocalSearch("");
                    setDirectory({ users: [], groups: [] });
                    seededExpansion.current = false;
                  })
                }
              >
                <Icon name="logout" />
                退出封存工作台
              </button>
            </>
          )}
        </main>
      )}
      {notice && (
        <div className="toast" role="status">
          <span>{notice}</span>
          <button
            className="icon-button"
            aria-label="关闭提示"
            onClick={() => setNotice("")}
          >
            <Icon name="close" size={15} />
          </button>
        </div>
      )}
      {demo && (
        <span className="demo-label">本地合成验收 · 未连接真实客户与上游</span>
      )}
      <dialog
        ref={dialogRef}
        className="modal"
        onCancel={() => setDialog("")}
        onClick={(e) => {
          if (e.target === e.currentTarget) setDialog("");
        }}
      >
        <div className="modal-heading">
          <h2>
            {{
              export: "导出记录",
              group: "整理请求",
              filter: "筛选记录",
              rename: "用户显示名",
              attachment: "附件",
              collection: "新建整理目录",
              grant: "授予封存访问权限",
            }[dialog] || "操作"}
          </h2>
          <button
            className="icon-button"
            aria-label="关闭对话框"
            onClick={() => setDialog("")}
          >
            <Icon name="close" />
          </button>
        </div>
        {dialog === "attachment" ? (
          <div className="attachment-preview">
            <h3>{form.name}</h3>
            <p>
              {form.mime} · {bytes(form.size)}
            </p>
            {form.object_id &&
              /^image\/(png|jpeg|webp|gif)$/.test(form.mime) && (
                <img
                  src={endpoint("/attachments/" + form.id + "?preview=1")}
                  alt={form.name}
                />
              )}{" "}
            {form.object_id ? (
              <a
                className="button primary"
                href={endpoint("/attachments/" + form.id)}
              >
                下载原文件
              </a>
            ) : (
              <p>仅保存了外部引用，文件尚未取得。未自动访问此地址。</p>
            )}
          </div>
        ) : (
          <form onSubmit={submit}>
            {["filter", "collection"].includes(dialog) && (
              <label>
                用户
                <select
                  value={form.user || ""}
                  onChange={(e) => setForm({ ...form, user: e.target.value })}
                  required={dialog === "collection"}
                >
                  <option value="">全部用户</option>
                  {directory.users.map((u: any) => (
                    <option key={u.id} value={u.id}>
                      用户 {u.id} · {u.name}
                    </option>
                  ))}
                </select>
              </label>
            )}
            {["group", "collection"].includes(dialog) && (
              <>
                <label>
                  项目名称
                  <input
                    required={dialog === "collection"}
                    maxLength={128}
                    value={form.project || ""}
                    onChange={(e) =>
                      setForm({ ...form, project: e.target.value })
                    }
                    placeholder="留空归入默认项目"
                  />
                </label>
                <label>
                  会话标识
                  <input
                    maxLength={128}
                    value={form.conversation || ""}
                    onChange={(e) =>
                      setForm({ ...form, conversation: e.target.value })
                    }
                    placeholder="留空保留为未分类请求"
                  />
                </label>
                {dialog === "collection" && (
                  <label>
                    显示名
                    <input
                      maxLength={128}
                      value={form.title || ""}
                      onChange={(e) =>
                        setForm({ ...form, title: e.target.value })
                      }
                      placeholder="留空使用会话标识"
                    />
                  </label>
                )}
                <small>
                  只整理同一用户的请求。已写入 Drive
                  的原件留在原目录，整理关系会同步保存。
                </small>
              </>
            )}
            {dialog === "rename" && (
              <label>
                显示名
                <input
                  required
                  maxLength={128}
                  value={form.name || ""}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </label>
            )}
            {dialog === "filter" && (
              <>
                <label>
                  模型
                  <input
                    value={form.model || ""}
                    onChange={(e) =>
                      setForm({ ...form, model: e.target.value })
                    }
                  />
                </label>
                <label>
                  开始时间
                  <input
                    type="datetime-local"
                    value={form.start || ""}
                    onChange={(e) =>
                      setForm({ ...form, start: e.target.value })
                    }
                  />
                </label>
                <label>
                  结束时间（不包含）
                  <input
                    type="datetime-local"
                    value={form.end || ""}
                    onChange={(e) => setForm({ ...form, end: e.target.value })}
                  />
                </label>
              </>
            )}
            {dialog === "export" && (
              <>
                <label>
                  导出范围
                  <select
                    value={form.scope || "filter"}
                    onChange={(e) =>
                      setForm({ ...form, scope: e.target.value })
                    }
                  >
                    <option value="filter">当前搜索与筛选范围</option>
                    {selected && (
                      <option value="current">当前会话 / 请求</option>
                    )}
                  </select>
                </label>
                <label>
                  格式
                  <select
                    value={form.format || "html"}
                    onChange={(e) =>
                      setForm({ ...form, format: e.target.value })
                    }
                  >
                    <option value="html">
                      HTML 离线阅读 + 原始 JSON + 附件
                    </option>
                    <option value="json">原始 JSON + 附件</option>
                    <option value="markdown">
                      Markdown + 原始 JSON + 附件
                    </option>
                  </select>
                </label>
                <p className="muted">下载包含明文对话，请妥善保管。</p>
              </>
            )}
            {dialog === "grant" && (
              <label>
                管理员用户 ID
                <input
                  type="number"
                  min="1"
                  required
                  value={form.user || ""}
                  onChange={(e) => setForm({ ...form, user: e.target.value })}
                />
              </label>
            )}
            <div className="modal-actions">
              <button type="button" onClick={() => setDialog("")}>
                取消
              </button>
              <button className="primary" disabled={busy}>
                {busy ? "处理中…" : dialog === "export" ? "开始导出" : "保存"}
              </button>
            </div>
          </form>
        )}
      </dialog>
    </div>
  );
}
