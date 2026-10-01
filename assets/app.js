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
