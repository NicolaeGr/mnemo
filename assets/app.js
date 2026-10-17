// app.js holds the two client-side hooks that make the segment stack work.
// 1. attach the currently-mounted segment stack to every htmx request
// 2. adopt the new stack after each navigation (drives body[data-stack]).
// The client reports no modal state: modal context is baked into links by the
// server (X-Modal headers + hx-target).

// The server stamps the true mounted stack onto <body data-stack> on full
// loads, so initialize from the DOM (a direct reload of /dashboard/settings
// has root,dashboard,settings mounted).
let mounted = (document.body.dataset.stack || "root").split(",");

document.body.addEventListener("htmx:configRequest", (e) => {
  // X-Drop-Segments (baked into the expand button) marks a "leave the modal
  // and render the whole page" request. The modal's segments were reported
  // as mounted, but the page is about to be re-rendered from scratch, so
  // drop everything except root, so every segment's Load runs again and the
  // re-rendered layout gets fresh data.
  const drop = e.detail.headers["X-Drop-Segments"];
  if (drop) {
    mounted = ["root"];
  }

  e.detail.headers["X-Mounted-Segments"] = mounted.join(",");

  // The expand button carries the modal's open URL, but the user may have
  // navigated within the modal, so request the live path instead.
  if (e.detail.headers["X-Modal"] === "none") {
    e.detail.path = location.pathname + location.search;
  }
});

document.body.addEventListener("segments", (e) => {
  mounted = e.detail.value.split(",");
  document.body.dataset.stack = e.detail.value;
  document.body.dataset.mounted = mounted.join(",");
});

// A request in flight shows a progress cursor everywhere, so htmx interactions
// (search, sort, paging, mutations) never look inert.
document.body.addEventListener("htmx:beforeRequest", () =>
  document.body.classList.add("htmx-busy"),
);
for (const ev of [
  "htmx:afterRequest",
  "htmx:responseError",
  "htmx:sendError",
  "htmx:timeout",
]) {
  document.body.addEventListener(ev, () =>
    document.body.classList.remove("htmx-busy"),
  );
}

// shell drives the app frame: the mobile drawer (nav) and the slim/wide sidebar
// (collapsed, persisted in localStorage). The sidebar reacts to these via its
// `:class` binding; no DOM side effects are needed.
window.shell = () => ({
  nav: false,
  collapsed: false,
  init() {
    try {
      this.collapsed = localStorage.getItem("sidebarCollapsed") === "1";
    } catch (e) {
      /* storage unavailable; the choice just won't persist */
    }
  },
  toggleCollapse() {
    this.collapsed = !this.collapsed;
    try {
      localStorage.setItem("sidebarCollapsed", this.collapsed ? "1" : "0");
    } catch (e) {
      /* storage unavailable */
    }
  },
});

// palette backs the command palette (Ctrl/Cmd+K). Alpine owns open/close, the
// query, and keyboard selection; htmx owns the requests that swap
// #palette-results. Options are real elements, so Enter just clicks the active
// one. See pages/palette.templ.
window.palette = () => ({
  open: false,
  q: "",
  activeId: "",
  index: -1,
  _returnFocus: null,
  init() {
    this._onHotkey = (e) => {
      if (
        (e.metaKey || e.ctrlKey) &&
        !e.altKey &&
        (e.key === "k" || e.key === "K")
      ) {
        e.preventDefault();
        this.toggle();
      }
    };
    window.addEventListener("keydown", this._onHotkey);

    // htmx event names are camelCase but Alpine attribute handlers are lowercased
    // by the HTML parser, so preselect via a direct listener after every swap.
    this._onSwap = (e) => {
      if (e.target && e.target.id === "palette-results") this.selectFirst();
    };
    document.addEventListener("htmx:afterSwap", this._onSwap);
  },
  destroy() {
    window.removeEventListener("keydown", this._onHotkey);
    document.removeEventListener("htmx:afterSwap", this._onSwap);
  },
  // items are the rendered, visible options (a hidden palette reports none).
  get items() {
    return Array.from(
      this.$refs.results.querySelectorAll('[role="option"]'),
    ).filter((el) => el.getClientRects().length > 0);
  },
  toggle() {
    if (this.open) this.close();
    else this.show();
  },
  show() {
    this._returnFocus = document.activeElement;
    this.open = true;
    this.$nextTick(() => {
      this.$refs.input.focus();
      this.$refs.input.select();
      this.selectFirst();
    });
  },
  close() {
    this.open = false;
    if (this._returnFocus && this._returnFocus.focus) this._returnFocus.focus();
  },
  onKeydown(e) {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        this.move(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        this.move(-1);
        break;
      case "Enter":
        e.preventDefault();
        this.choose();
        break;
      case "Escape":
        e.preventDefault();
        this.close();
        break;
    }
  },
  selectFirst() {
    const items = this.items;
    this.select(items.length ? items[0] : null);
  },
  select(el) {
    this.items.forEach((it) => it.classList.remove("is-active"));
    if (!el) {
      this.index = -1;
      this.activeId = "";
      return;
    }
    const i = this.items.indexOf(el);
    if (i < 0) return;
    this.index = i;
    el.classList.add("is-active");
    this.activeId = el.id || "";
    el.scrollIntoView({ block: "nearest" });
  },
  move(delta) {
    const items = this.items;
    if (items.length === 0) return;
    if (this.index < 0) {
      this.select(items[delta > 0 ? 0 : items.length - 1]);
      return;
    }
    const next = (this.index + delta + items.length) % items.length;
    this.select(items[next]);
  },
  choose() {
    const el = this.items[this.index];
    if (el) el.click();
  },
});

// Show the platform's command key in the palette trigger hint (⌘ on Mac).
// The header is server-rendered once and persists across fragment navigations,
// so a single pass at load is enough.
document.querySelectorAll("[data-palette-mod]").forEach((el) => {
  if (/Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent)) {
    el.textContent = "⌘";
  }
});

// listView backs the sortable, searchable tables (contacts, devices). It owns
// the sort/dir/page state, debounces changes, and asks htmx to refetch just the
// rows. The count and pager render from this state, refreshed by the rows
// endpoint's HX-Trigger payload. On the contacts list it also drives structured
// search: suggestions come from the server, so the grammar lives in one place.
window.listView = (init) => ({
  q: init.q || "",
  sort: init.sort || "name",
  dir: init.dir || "asc",
  page: init.page || 1,
  per: init.per || 50,
  total: init.total || 0,
  endpoint: init.endpoint,
  target: init.target,
  suggest: init.suggest || "",
  warnings: init.warnings || [],
  queryDesc: init.desc || "",
  ids: init.ids || [],
  selected: [],
  loading: false,
  timer: null,
  suggestTimer: null,
  suggestAbort: null,
  suggestSeq: 0,
  comboOpen: false,
  comboIndex: -1,
  comboItems: [],
  init() {
    this.$watch("q", () => {
      this.page = 1;
      this.queue();
    });
  },
  get pages() {
    return Math.max(1, Math.ceil(this.total / this.per));
  },
  get hasPrev() {
    return this.page > 1;
  },
  get hasNext() {
    return this.page < this.pages;
  },
  get allSelected() {
    return this.ids.length > 0 && this.selected.length === this.ids.length;
  },
  get hasSelection() {
    return this.selected.length > 0;
  },
  toggle(id) {
    const i = this.selected.indexOf(id);
    if (i >= 0) this.selected.splice(i, 1);
    else this.selected.push(id);
  },
  toggleAll() {
    this.selected = this.allSelected ? [] : this.ids.slice();
  },
  clearSelection() {
    this.selected = [];
  },
  get countLabel() {
    return this.total === 1 ? "1 result" : this.total + " results";
  },
  get showDesc() {
    const d = (this.queryDesc || "").trim().toLowerCase();
    const q = (this.q || "").trim().toLowerCase();
    return d !== "" && d !== q;
  },
  sortBy(field) {
    if (this.sort === field) {
      this.dir = this.dir === "asc" ? "desc" : "asc";
    } else {
      this.sort = field;
      this.dir = "asc";
    }
    this.page = 1;
    this.queue();
  },
  go(page) {
    const p = Math.min(Math.max(1, page), this.pages);
    if (p === this.page) return;
    this.page = p;
    this.queue();
  },
  queue() {
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.reload(), 180);
  },
  reload() {
    const params = new URLSearchParams({
      q: this.q,
      sort: this.sort,
      dir: this.dir,
      page: String(this.page),
    });
    this.selected = [];
    this.loading = true;
    htmx.ajax("GET", this.endpoint + "?" + params.toString(), {
      target: this.target,
      swap: "innerHTML",
    });
  },
  onMeta(e) {
    const d = (e && e.detail) || {};
    if (typeof d.total === "number") this.total = d.total;
    if (typeof d.page === "number") this.page = d.page;
    if (Array.isArray(d.ids)) this.ids = d.ids;
    if (Array.isArray(d.warnings)) this.warnings = d.warnings;
    if (typeof d.desc === "string") this.queryDesc = d.desc;
    this.selected = [];
    this.loading = false;
  },
  ariaSort(field) {
    if (this.sort !== field) return "none";
    return this.dir === "asc" ? "ascending" : "descending";
  },

  // --- structured search ---------------------------------------------------
  // The server parses the query and returns suggestions with exact replacement
  // spans. The browser only inserts text; it never re-implements the grammar.
  insertToken(token) {
    const q = (this.q || "").trim();
    if (q === "") {
      this.q = token;
    } else if (!q.includes(token)) {
      this.q = q + " " + token;
    }
    this.$nextTick(() => {
      this.focusEnd();
      this.queueSuggest();
    });
  },
  clearQuery() {
    this.q = "";
    this.closeCombo();
    this.$nextTick(() => this.focusEnd());
  },
  focusEnd() {
    const el = this.$refs.search;
    if (!el) return;
    el.focus();
    el.setSelectionRange(this.q.length, this.q.length);
  },

  onSearchInput() {
    this.queueSuggest();
  },

  onCaretMove(e) {
    if (
      e &&
      ["ArrowDown", "ArrowUp", "Enter", "Tab", "Escape"].includes(e.key)
    ) {
      return;
    }
    this.queueSuggest();
  },

  queueSuggest() {
    if (!this.suggest) return;
    this.closeCombo();
    clearTimeout(this.suggestTimer);
    this.suggestTimer = setTimeout(() => this.refreshCombo(), 120);
  },

  async refreshCombo() {
    if (!this.suggest) {
      this.closeCombo();
      return;
    }
    const q = this.q || "";
    if (q.trim() === "") {
      this.closeCombo();
      return;
    }
    const el = this.$refs.search;
    const caret =
      el && el.selectionStart != null ? el.selectionStart : q.length;

    this.suggestSeq += 1;
    const seq = this.suggestSeq;
    if (this.suggestAbort) this.suggestAbort.abort();
    const ctrl = new AbortController();
    this.suggestAbort = ctrl;

    try {
      const params = new URLSearchParams({ q, caret: String(caret) });
      const res = await fetch(this.suggest + "?" + params.toString(), {
        signal: ctrl.signal,
        headers: { Accept: "application/json" },
      });
      if (!res.ok) {
        if (seq === this.suggestSeq) this.closeCombo();
        return;
      }
      const data = await res.json();
      if (seq !== this.suggestSeq) return;
      this.comboItems = data.items || [];
      this.comboIndex = -1;
      this.comboOpen = this.comboItems.length > 0;
    } catch (err) {
      if (err && err.name === "AbortError") return;
      if (seq === this.suggestSeq) this.closeCombo();
    }
  },

  onSearchKeydown(e) {
    if (!this.comboOpen) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      this.comboIndex =
        this.comboIndex < 0
          ? 0
          : (this.comboIndex + 1) % this.comboItems.length;
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      this.comboIndex =
        this.comboIndex < 0
          ? this.comboItems.length - 1
          : (this.comboIndex - 1 + this.comboItems.length) %
            this.comboItems.length;
    } else if (e.key === "Enter" || e.key === "Tab") {
      // Only insert on an explicit selection; a bare Enter must not.
      if (this.comboIndex >= 0 && this.comboIndex < this.comboItems.length) {
        e.preventDefault();
        this.pickSuggestion(this.comboItems[this.comboIndex]);
      }
    } else if (e.key === "Escape") {
      this.closeCombo();
    }
  },

  // Replace the server-provided span. item.start/end are JS string offsets.
  pickSuggestion(item) {
    const el = this.$refs.search;
    const value = this.q || "";
    this.q = value.slice(0, item.start) + item.insert + value.slice(item.end);
    this.closeCombo();
    this.comboItems = [];
    if (el) {
      const caret = item.start + item.insert.length;
      this.$nextTick(() => {
        el.focus();
        el.setSelectionRange(caret, caret);
      });
    }
  },

  closeCombo() {
    this.comboOpen = false;
    this.comboIndex = -1;
  },
});

// rowMenu positions a per-row actions dropdown with fixed coordinates, so the
// table's overflow-hidden wrapper cannot clip it.
window.rowMenu = () => ({
  open: false,
  top: 0,
  left: 0,
  toggle(e) {
    if (this.open) {
      this.open = false;
      return;
    }
    const r = e.currentTarget.getBoundingClientRect();
    this.top = r.bottom + 4;
    this.left = Math.max(8, Math.min(r.right - 176, window.innerWidth - 184));
    this.open = true;
  },
  get panelStyle() {
    return "top:" + this.top + "px;left:" + this.left + "px;";
  },
  close() {
    this.open = false;
  },
});

// listSort backs the lists grid: two buttons that reorder the cards server-side.
window.listSort = (init) => ({
  sort: init.sort || "name",
  endpoint: init.endpoint,
  target: init.target,
  activeClass:
    "inline-flex h-8 items-center rounded-md bg-accent px-3 text-xs font-medium text-accent-foreground",
  idleClass:
    "inline-flex h-8 items-center rounded-md border border-border px-3 text-xs text-muted-foreground transition-colors hover:bg-muted",
  setSort(next) {
    if (this.sort === next) return;
    this.sort = next;
    htmx.ajax("GET", this.endpoint + "?sort=" + encodeURIComponent(next), {
      target: this.target,
      swap: "outerHTML",
    });
  },
});

// Any handler can trigger `showToast` to surface feedback.
let toastTimer = null;
document.body.addEventListener("showToast", (e) => {
  const msg = e.detail?.value || "Done";
  let toast = document.getElementById("app-toast");
  if (!toast) {
    toast = document.createElement("div");
    toast.id = "app-toast";
    toast.className = "app-toast";
    document.body.appendChild(toast);
  }
  toast.textContent = msg;
  toast.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toast.classList.remove("show"), 2000);
});

// contactEditor backs the contact form: a structured name, composable addresses,
// grouped property rows, and the live modal title. See contacts.templ.
let __contactUid = 0;
const nextContactUid = () => (__contactUid += 1);

// shownParts reports which optional components already carry a value, so the
// editor reveals them on open.
function shownParts(obj, ids) {
  const out = {};
  for (const id of ids) {
    out[id] = !!(obj[id] || "").trim();
  }
  return out;
}

// popoverFor drives a native popover from its trigger. Hover opens it, leaving
// closes it after a short grace so the pointer can cross the gap, and the script
// places it under the trigger.
window.popoverFor = (id) => ({
  timer: null,
  show(el) {
    this.keep();
    const menu = document.getElementById(id);
    if (!menu) return;
    if (!menu.matches(":popover-open")) menu.showPopover();
    if (el) {
      const r = el.getBoundingClientRect();
      menu.style.position = "fixed";
      menu.style.margin = "0";
      menu.style.top = r.bottom + 4 + "px";
      const left = Math.min(
        r.right - menu.offsetWidth,
        window.innerWidth - menu.offsetWidth - 8,
      );
      menu.style.left = Math.max(8, left) + "px";
    }
  },
  keep() {
    clearTimeout(this.timer);
  },
  close() {
    clearTimeout(this.timer);
    const menu = document.getElementById(id);
    if (menu && menu.matches(":popover-open")) menu.hidePopover();
  },
  hide() {
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.close(), 140);
  },
});

window.contactEditor = (init) => {
  const row = () => ({ uid: nextContactUid(), key: "", type: "", value: "" });
  const addrRow = () => ({
    uid: nextContactUid(),
    type: "",
    pobox: "",
    ext: "",
    street: "",
    city: "",
    region: "",
    postal: "",
    country: "",
    extra: {
      pobox: false,
      ext: false,
      region: false,
      postal: false,
      country: false,
    },
  });
  const groups = [];
  for (const f of init.fields || []) {
    let g = groups.find((g) => g.kind === f.kind);
    if (!g) {
      g = { kind: f.kind, rows: [] };
      groups.push(g);
    }
    g.rows.push({
      uid: nextContactUid(),
      key: f.key,
      type: f.type,
      value: f.value,
    });
  }
  const nameIds = ["prefix", "additional", "suffix"];
  const addrIds = ["pobox", "ext", "region", "postal", "country"];
  // Phone is always present, and address joins the same list so both can be
  // pinned to the top no matter what order the card listed its fields in.
  if (!groups.some((g) => g.kind === "tel")) {
    groups.push({ kind: "tel", rows: [row()] });
  }
  if ((init.addresses || []).length > 0) {
    groups.push({
      kind: "adr",
      rows: init.addresses.map((a) => ({
        ...a,
        extra: shownParts(a, addrIds),
      })),
    });
  }
  const rank = (kind) => (kind === "tel" ? 0 : kind === "adr" ? 1 : 2);
  const sortGroups = (gs) => gs.sort((a, b) => rank(a.kind) - rank(b.kind));
  sortGroups(groups);
  return {
    titleBase: init.titleBase,
    kinds: init.kinds,
    nameParts: init.nameParts,
    addressParts: init.addressParts,
    name: {
      prefix: init.prefix,
      given: init.given,
      additional: init.additional,
      family: init.family,
      suffix: init.suffix,
      extra: shownParts(init, nameIds),
    },
    groups,

    init() {
      this.$watch(
        () => this.nameText(),
        () => this.syncTitle(),
      );
      this.syncTitle();
    },

    kind(id) {
      return this.kinds.find((k) => k.id === id);
    },
    kindLabel(id) {
      const k = this.kind(id);
      return k ? k.label : id;
    },
    hasTypes(id) {
      const k = this.kind(id);
      return !!(k && k.types && k.types.length);
    },
    typesFor(id) {
      const k = this.kind(id);
      return (k && k.types) || [];
    },
    // The modal title tracks the name parts live (the title element lives in the
    // modal chrome, outside this component, so it is written by id).
    nameText() {
      return [
        this.name.prefix,
        this.name.given,
        this.name.additional,
        this.name.family,
        this.name.suffix,
      ]
        .map((s) => (s || "").trim())
        .filter(Boolean)
        .join(" ");
    },
    syncTitle() {
      const el = document.getElementById("modal-title");
      if (!el) return;
      const name = this.nameText();
      el.textContent = name ? this.titleBase + " - " + name : this.titleBase;
    },

    // duplicable reports whether a kind can have more than one instance, which
    // decides between an "add another" and a remove control in the section bar.
    duplicable(id) {
      const k = this.kind(id);
      return !!(k && !k.single);
    },

    addField(id) {
      if (id === "adr") {
        this.addAddress();
        return;
      }
      if (this.groups.some((g) => g.kind === id)) return;
      this.groups.push({ kind: id, rows: [row()] });
    },
    addRow(kind) {
      const g = this.groups.find((g) => g.kind === kind);
      if (g) {
        g.rows.push(kind === "adr" ? addrRow() : row());
      }
    },
    removeRow(g, i) {
      g.rows.splice(i, 1);
      if (g.rows.length > 0) return;
      // Phone is the always-present field, so its last row resets instead of
      // taking the whole section with it.
      if (g.kind === "tel") {
        g.rows.push(row());
        return;
      }
      this.groups = this.groups.filter((x) => x !== g);
    },
    removeGroup(g) {
      this.groups = this.groups.filter((x) => x !== g);
    },

    adrGroup() {
      return this.groups.find((g) => g.kind === "adr");
    },
    addAddress() {
      let g = this.adrGroup();
      if (!g) {
        g = { kind: "adr", rows: [] };
        this.groups.push(g);
        sortGroups(this.groups);
      }
      g.rows.push(addrRow());
    },
    addressPartsAvailable(i) {
      const g = this.adrGroup();
      if (!g || !g.rows[i]) return [];
      return this.addressParts.filter((p) => !g.rows[i].extra[p.id]);
    },
    showAddressPart(i, id) {
      const g = this.adrGroup();
      if (g && g.rows[i]) g.rows[i].extra[id] = true;
    },

    namePartsAvailable() {
      return this.nameParts.filter((p) => !this.name.extra[p.id]);
    },
    showNamePart(id) {
      this.name.extra[id] = true;
    },
    removeNamePart(id) {
      this.name.extra[id] = false;
      this.name[id] = "";
    },

    removeAddressPart(i, id) {
      const g = this.adrGroup();
      if (g && g.rows[i]) {
        g.rows[i].extra[id] = false;
        g.rows[i][id] = "";
      }
    },
    availableKinds() {
      return this.kinds.filter(
        (k) => !this.groups.some((g) => g.kind === k.id),
      );
    },
  };
};

// multiSelect backs the book picker: chips for the chosen values plus a filtered
// list to add more. The chosen values serialize as repeated hidden inputs.
window.multiSelect = (init) => ({
  ...init,
  get chosen() {
    return this.selected
      .map((id) => this.options.find((o) => o.value === id))
      .filter(Boolean);
  },
  get filtered() {
    const q = (this.query || "").trim().toLowerCase();
    return this.options.filter(
      (o) =>
        !this.selected.includes(o.value) &&
        (!q || o.label.toLowerCase().includes(q)),
    );
  },
  choiceLabel(id) {
    const o = this.options.find((o) => o.value === id);
    return o ? o.label : String(id);
  },
  add(id) {
    if (!this.selected.includes(id)) {
      this.selected.push(id);
    }
    this.query = "";
  },
  remove(id) {
    this.selected = this.selected.filter((x) => x !== id);
  },
});

// slugField keeps the slug in step with the display name while the slug still
// looks derived from it (the name's slug, optionally with a -NNN suffix) and is
// checked against the server as either changes. When an auto slug is taken it
// gets three random digits appended until it is free, so what the form submits
// is already unique. Editing the slug by hand detaches it from the name.
window.slugField = (init) => {
  let seq = 0;
  return {
    endpoint: init.endpoint,
    name: init.name || "",
    slug: init.slug || "",
    prevName: init.name || "",
    status: "",
    slugify(s) {
      return (s || "")
        .normalize("NFKD")
        .replace(/[\u0300-\u036f]/g, "")
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-+|-+$/g, "")
        .slice(0, 60);
    },
    random3() {
      return String(100 + Math.floor(Math.random() * 900));
    },
    // isDerived reports whether the slug still belongs to the name it was made
    // from, so we know it is safe to rewrite when the name changes.
    isDerived() {
      if (!this.slug) return true;
      const root = this.slugify(this.prevName);
      if (!root) return false;
      const esc = root.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      return (
        this.slug === root || new RegExp("^" + esc + "-\\d{3}$").test(this.slug)
      );
    },
    onName() {
      if (!this.isDerived()) return;
      this.prevName = this.name;
      this.slug = this.slugify(this.name);
      return this.check();
    },
    async check() {
      const base = this.slug;
      if (!base) {
        this.status = "";
        return;
      }
      const auto = this.isDerived();
      const mine = ++seq;
      this.status = "checking";
      let candidate = base;
      for (let i = 0; i < 5; i++) {
        let taken;
        try {
          const r = await fetch(
            this.endpoint + "?slug=" + encodeURIComponent(candidate),
          );
          taken = !(await r.json()).available;
        } catch {
          if (mine === seq) this.status = "";
          return;
        }
        if (mine !== seq) return;
        if (!taken) {
          this.slug = candidate;
          this.status = "ok";
          return;
        }
        if (!auto) {
          this.status = "taken";
          return;
        }
        candidate = base.replace(/-\d{3}$/, "") + "-" + this.random3();
      }
      this.status = "taken";
    },
  };
};
