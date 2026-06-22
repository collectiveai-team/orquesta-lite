/*
 * orquestalite — office gameboard (live screen mode)
 *
 * A self-contained React component (no build step, no dc-runtime): each role is
 * a desk in a virtual office you walk between (WASD / d-pad); approaching a desk
 * and pressing E opens a panel with the role's live state, result JSON, and a
 * summary. All data is pulled from the same read-only APIs the table dashboard
 * uses (/api/tasks, /api/factory, /api/cost, SSE /api/events, /api/result).
 *
 * React + ReactDOM are loaded as vendored UMD globals (see gameboard.html), so
 * this works fully offline like the rest of the tool.
 */
(function () {
  "use strict";
  var React = window.React, ReactDOM = window.ReactDOM;
  var h = React.createElement;

  // css("a-b:c;d:e") -> {aB:"c", d:"e"} so template-style CSS strings can be
  // passed straight to React (custom --props are preserved verbatim).
  function css(str) {
    var o = {};
    String(str).split(";").forEach(function (decl) {
      var i = decl.indexOf(":");
      if (i < 0) return;
      var k = decl.slice(0, i).trim(), v = decl.slice(i + 1).trim();
      if (!k) return;
      if (k.indexOf("--") !== 0) k = k.replace(/-([a-z])/g, function (_, c) { return c.toUpperCase(); });
      o[k] = v;
    });
    return o;
  }
  // el() mirrors React.createElement but lets style be a CSS string.
  function el(tag, props) {
    var kids = Array.prototype.slice.call(arguments, 2);
    if (props && typeof props.style === "string") {
      props = Object.assign({}, props, { style: css(props.style) });
    }
    return React.createElement.apply(React, [tag, props].concat(kids));
  }

  var hhmmss = function (ts) {
    return String(ts || "").replace(/^.*T/, "").replace(/(\.\d+)?(Z|[+-].*)$/, "");
  };
  var shortAgent = function (a) { return String(a || "").replace(/_/g, " "); };

  var Office = function (props) { React.Component.call(this, props); this._init(); };
  Office.prototype = Object.create(React.Component.prototype);
  Office.prototype.constructor = Office;

  Office.prototype._init = function () {
    this.W = 940; this.H = 620;
    this.SPR = [
      "....kkkk....", "...khhhhk...", "..khhhhhhk..", "..khsssshk..",
      "..ksessesk..", "..kssssssk..", "..kssSSssk..", "....kssk....",
      ".kttttttttk.", ".kttttttttk.", ".ktTTTTTTtk.", ".ksttttttsk.",
      ".kppppppppk.", "..kpp..ppk..", "..kkk..kkk.."
    ];
    this.LAYOUT = {
      planner:      { x: 66,  y: 64,  hub: false },
      parser:       { x: 410, y: 48,  hub: false },
      coder:        { x: 754, y: 64,  hub: false },
      tester:       { x: 790, y: 248, hub: false },
      critic:       { x: 754, y: 438, hub: false },
      reviewer:     { x: 410, y: 470, hub: false },
      verifier:     { x: 66,  y: 438, hub: false },
      orchestrator: { x: 410, y: 248, hub: true }
    };
    this.PIPE = ["planner", "parser", "coder", "tester", "critic", "reviewer", "verifier"];
    // Static per-role identity (palette + one-line description). Live state is
    // derived from events/tasks at render time, never hardcoded here.
    this.ROLE_META = {
      planner:      { label: "PLANNER",      color: "#b07cff", skin: "#f1c9a5", hair: "#3a2d4a", desc: "Lee el plan y extrae features verticales." },
      parser:       { label: "PARSER",       color: "#5b9cff", skin: "#e8b48c", hair: "#241a2a", desc: "Convierte la feature en tareas atómicas." },
      coder:        { label: "CODER",        color: "#46d39a", skin: "#f1c9a5", hair: "#6b4a2a", desc: "Implementa la tarea actual del ciclo." },
      tester:       { label: "TESTER",       color: "#ffc24b", skin: "#d99b6c", hair: "#1e1726", desc: "Corre la suite y reporta pass/fail." },
      critic:       { label: "CRITIC",       color: "#ff6b8a", skin: "#f1c9a5", hair: "#8a3b3b", desc: "Revisión en dos ejes: Standards y Spec." },
      reviewer:     { label: "REVIEWER",     color: "#38d6d6", skin: "#e8b48c", hair: "#241a2a", desc: "Cierra el ciclo y genera las próximas tareas." },
      verifier:     { label: "VERIFIER",     color: "#ff924b", skin: "#c98a5e", hair: "#1e1726", desc: "Verificación black-box: corre la app de verdad." },
      orchestrator: { label: "ORCHESTRATOR", color: "#ffd84b", skin: "#f1c9a5", hair: "#caa23a", desc: "Coordina roles, loops, commits y rate-limits." }
    };
    this.keys = { up: false, down: false, left: false, right: false };
    this.player = { x: 264, y: 310 };
    this.facing = 1;
    this.stageRef = React.createRef();
    this.stageWrapRef = React.createRef();
    this.playerRef = React.createRef();
    this.facingRef = React.createRef();
    this.elapsedRef = React.createRef();
    this._spriteCache = {};
    this._mountTs = Date.now();
    this._firstTs = 0;
    var ml = window.innerWidth < 760;
    this.state = {
      scale: this.calcScale(window.innerWidth, window.innerHeight, ml),
      tx: 0, ty: 0,
      near: null, openRole: "", tab: "estado",
      isMobile: ml,
      tasks: [], features: [], cost: null, events: [], results: {}, connected: false
    };
  };

  // ---------- layout / scaling ----------
  Office.prototype.calcScale = function (w, h, ml) {
    var aw = w - 24, ah = h - (ml ? 54 : 62) - 18;
    return Math.min(aw / this.W, ah / this.H, ml ? 1.6 : 1.55);
  };
  Office.prototype.layoutStage = function () {
    var ml = window.innerWidth < 760;
    var wrap = this.stageWrapRef.current;
    var ww = wrap ? wrap.clientWidth : window.innerWidth;
    var wh = wrap ? wrap.clientHeight : window.innerHeight;
    var scale = Math.min(ww / this.W, wh / this.H, ml ? 1.6 : 1.55);
    var tx = Math.max(0, (ww - this.W * scale) / 2);
    var ty = Math.max(0, (wh - this.H * scale) / 2);
    this.setState({ scale: scale, tx: tx, ty: ty, isMobile: ml });
  };

  Office.prototype.componentDidMount = function () {
    this.layoutStage();
    this._onResize = function () { this.layoutStage(); }.bind(this);
    window.addEventListener("resize", this._onResize);
    this._onKeyDown = function (e) {
      var k = e.key.toLowerCase();
      if (k === "arrowup" || k === "w") { this.keys.up = true; e.preventDefault(); }
      else if (k === "arrowdown" || k === "s") { this.keys.down = true; e.preventDefault(); }
      else if (k === "arrowleft" || k === "a") { this.keys.left = true; e.preventDefault(); }
      else if (k === "arrowright" || k === "d") { this.keys.right = true; e.preventDefault(); }
      else if (k === "e" || k === "enter") { if (this.state.near && !this.state.openRole) this.open(this.state.near); }
      else if (k === "escape") { if (this.state.openRole) this.setState({ openRole: "" }); }
    }.bind(this);
    this._onKeyUp = function (e) {
      var k = e.key.toLowerCase();
      if (k === "arrowup" || k === "w") this.keys.up = false;
      else if (k === "arrowdown" || k === "s") this.keys.down = false;
      else if (k === "arrowleft" || k === "a") this.keys.left = false;
      else if (k === "arrowright" || k === "d") this.keys.right = false;
    }.bind(this);
    window.addEventListener("keydown", this._onKeyDown);
    window.addEventListener("keyup", this._onKeyUp);
    var self = this;
    this.centers = Object.keys(this.LAYOUT).map(function (id) {
      var L = self.LAYOUT[id];
      return { id: id, cx: L.x + (L.hub ? 62 : 60), cy: L.y + (L.hub ? 66 : 72) };
    });
    this.deskRects = Object.keys(this.LAYOUT).map(function (id) {
      var L = self.LAYOUT[id], cx = L.x + (L.hub ? 62 : 60);
      return { x: cx - (L.hub ? 52 : 48), y: L.y + 6, w: (L.hub ? 104 : 96), h: (L.hub ? 56 : 46) };
    });
    this.tick();
    this._timer = setInterval(function () {
      if (self.elapsedRef.current) self.elapsedRef.current.textContent = self.fmt(self.elapsedSecs());
    }, 1000);
    if (this.elapsedRef.current) this.elapsedRef.current.textContent = this.fmt(0);
    this.startData();
  };
  Office.prototype.componentWillUnmount = function () {
    window.removeEventListener("resize", this._onResize);
    window.removeEventListener("keydown", this._onKeyDown);
    window.removeEventListener("keyup", this._onKeyUp);
    cancelAnimationFrame(this._raf);
    clearInterval(this._timer);
    [this._iv1, this._iv2, this._iv3].forEach(function (iv) { if (iv) clearInterval(iv); });
    if (this._es) this._es.close();
  };

  // ---------- live data ----------
  Office.prototype.startData = function () {
    var self = this;
    var pull = function (url, key, xform) {
      return fetch(url).then(function (r) { return r.json(); }).then(function (d) {
        var patch = {}; patch[key] = xform ? xform(d) : d; self.setState(patch);
      }).catch(function () {});
    };
    this._p1 = function () { return pull("/api/tasks", "tasks", function (d) { return (d && d.tasks) || []; }); };
    this._p2 = function () { return pull("/api/factory", "features", function (d) { return (d && d.features) || []; }); };
    this._p3 = function () { return pull("/api/cost", "cost"); };
    this._p1(); this._p2(); this._p3();
    this._iv1 = setInterval(this._p1, 2000);
    this._iv2 = setInterval(this._p2, 3000);
    this._iv3 = setInterval(this._p3, 60000);
    this._es = new EventSource("/api/events");
    this._es.onopen = function () { self.setState({ connected: true }); };
    this._es.onerror = function () { self.setState({ connected: false }); };
    this._es.onmessage = function (m) {
      var ev; try { ev = JSON.parse(m.data); } catch (e) { return; }
      self.pushEvent(ev);
    };
  };
  Office.prototype.pushEvent = function (ev) {
    this.setState(function (st) {
      var evs = st.events.concat(ev);
      if (evs.length > 600) evs.splice(0, evs.length - 600);
      return { events: evs };
    });
    if (!this._firstTs && ev && ev.ts) {
      var t = Date.parse(ev.ts);
      if (!isNaN(t)) this._firstTs = t;
    }
  };
  Office.prototype.elapsedSecs = function () {
    var base = this._firstTs || this._mountTs;
    return Math.max(0, Math.floor((Date.now() - base) / 1000));
  };

  // Role of the most recent agent_run drives the "working" highlight.
  Office.prototype.activeRole = function () {
    var evs = this.state.events;
    for (var i = evs.length - 1; i >= 0; i--) {
      var e = evs[i];
      if (e && e.event === "agent_run" && this.PIPE.indexOf(e.role) >= 0) return e.role;
    }
    return null;
  };
  Office.prototype.currentCycle = function () {
    var evs = this.state.events;
    for (var i = evs.length - 1; i >= 0; i--) {
      if (evs[i] && evs[i].event === "cycle_start" && evs[i].cycle != null) return evs[i].cycle;
    }
    return 1;
  };
  Office.prototype.currentTask = function () {
    var ts = this.state.tasks;
    var ip = ts.find(function (t) { return t.status === "in_progress"; });
    if (ip) return { id: ip.id, title: ip.title };
    var pend = ts.find(function (t) { return t.status === "pending"; });
    if (pend) return { id: pend.id, title: pend.title };
    if (ts.length) { var last = ts[ts.length - 1]; return { id: last.id, title: last.title }; }
    return { id: "—", title: "Sin tareas todavía" };
  };
  Office.prototype.roleActivity = function (id) {
    var out = [];
    var evs = this.state.events;
    for (var i = evs.length - 1; i >= 0 && out.length < 6; i--) {
      var e = evs[i];
      if (e && e.event === "agent_run" && e.role === id) {
        out.push({
          t: hhmmss(e.ts),
          m: shortAgent(e.agent) + " → " + (e.status || "") +
             (e.duration_s ? " (" + Math.round(e.duration_s) + "s)" : "") +
             (e.task_id ? " · " + e.task_id : "")
        });
      }
    }
    return out;
  };
  Office.prototype.roleStatChips = function (id) {
    var evs = this.state.events, last = null, runs = 0;
    for (var i = evs.length - 1; i >= 0; i--) {
      var e = evs[i];
      if (e && e.event === "agent_run" && e.role === id) { runs++; if (!last) last = e; }
    }
    return [
      { k: "estado", v: last ? (last.status || "—") : "—" },
      { k: "agente", v: last ? shortAgent(last.agent) : "—" },
      { k: "última", v: last && last.duration_s ? Math.round(last.duration_s) + "s" : "—" },
      { k: "corridas", v: String(runs) }
    ];
  };
  Office.prototype.roleSummary = function (id) {
    var r = this.state.results[id];
    if (r && typeof r === "object") {
      var s = r.notes_for_memory || r.summary || r.summary_of_cycle;
      if (s) return s;
    }
    return "Sin resumen disponible todavía. Abrí la pestaña JSON para ver el último resultado crudo del rol.";
  };
  Office.prototype.featureInfo = function () {
    var fs = this.state.features;
    var ip = fs.find(function (f) { return f.status === "in_progress"; });
    var f = ip || (fs.length ? fs[fs.length - 1] : null);
    return f ? { name: f.title, branch: f.branch || "—" } : { name: "—", branch: "—" };
  };

  // ---------- collision / movement ----------
  Office.prototype.fmt = function (s) {
    var hh = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60;
    var p = function (n) { return String(n).padStart(2, "0"); };
    return p(hh) + ":" + p(m) + ":" + p(x);
  };
  Office.prototype.hits = function (x, y) {
    var r = 11;
    if (x < 24 + r || x > this.W - 24 - r || y < 24 + r || y > this.H - 24 - r) return true;
    for (var i = 0; i < this.deskRects.length; i++) {
      var d = this.deskRects[i];
      var nx = Math.max(d.x, Math.min(x, d.x + d.w));
      var ny = Math.max(d.y, Math.min(y, d.y + d.h));
      if ((x - nx) * (x - nx) + (y - ny) * (y - ny) < r * r) return true;
    }
    return false;
  };
  Office.prototype.tick = function () {
    var dx = 0, dy = 0;
    if (this.keys.left) dx -= 1; if (this.keys.right) dx += 1;
    if (this.keys.up) dy -= 1; if (this.keys.down) dy += 1;
    if (dx || dy) {
      var m = Math.hypot(dx, dy); dx /= m; dy /= m; var sp = 2.8;
      var nx = this.player.x + dx * sp; if (!this.hits(nx, this.player.y)) this.player.x = nx;
      var ny = this.player.y + dy * sp; if (!this.hits(this.player.x, ny)) this.player.y = ny;
      if (dx !== 0) { this.facing = dx < 0 ? -1 : 1; if (this.facingRef.current) this.facingRef.current.style.transform = "scaleX(" + this.facing + ")"; }
    }
    if (this.playerRef.current) this.playerRef.current.style.transform = "translate(" + (this.player.x - 24) + "px," + (this.player.y - 58) + "px)";
    if (this.centers) {
      var near = null, best = 94;
      for (var i = 0; i < this.centers.length; i++) {
        var c = this.centers[i], d = Math.hypot(this.player.x - c.cx, this.player.y - c.cy);
        if (d < best) { best = d; near = c.id; }
      }
      if (near !== this.state.near && !this.state.openRole) this.setState({ near: near });
    }
    this._raf = requestAnimationFrame(this.tick.bind(this));
  };

  // ---------- sprites / json ----------
  Office.prototype.darken = function (hex, amt) {
    var n = parseInt(hex.slice(1), 16), r = (n >> 16) & 255, g = (n >> 8) & 255, b = n & 255;
    r = Math.round(r * (1 - amt)); g = Math.round(g * (1 - amt)); b = Math.round(b * (1 - amt));
    return "#" + ((1 << 24) + (r << 16) + (g << 8) + b).toString(16).slice(1);
  };
  Office.prototype.spriteEl = function (id, scale) {
    var key = id + "@" + scale;
    if (this._spriteCache[key]) return this._spriteCache[key];
    var R = id === "player" ? { hair: "#2a2238", skin: "#f1c9a5", color: "#ffd84b" } : this.ROLE_META[id];
    var pal = { ".": null, "k": "#221830", "h": R.hair, "s": R.skin, "S": this.darken(R.skin, 0.14), "e": "#1c1330", "t": R.color, "T": this.darken(R.color, 0.22), "p": "#2b2742" };
    var sh = [];
    for (var r = 0; r < this.SPR.length; r++) {
      var row = this.SPR[r];
      for (var c = 0; c < row.length; c++) { var col = pal[row[c]]; if (!col) continue; sh.push((c * scale) + "px " + (r * scale) + "px 0 0 " + col); }
    }
    var elm = h("div", { style: { position: "relative", width: (12 * scale) + "px", height: (this.SPR.length * scale) + "px" } },
      h("div", { style: { position: "absolute", left: 0, top: 0, width: scale + "px", height: scale + "px", boxShadow: sh.join(","), background: "transparent" } }));
    this._spriteCache[key] = elm; return elm;
  };
  Office.prototype.jsonEl = function (obj) {
    var s = JSON.stringify(obj, null, 2);
    s = s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    s = s.replace(/("(?:\\.|[^"\\])*"(\s*:)?|\b(?:true|false|null)\b|-?\d+(?:\.\d+)?)/g, function (m) {
      var c = "#ffd84b";
      if (/^"/.test(m)) { c = /:\s*$/.test(m) ? "#7aa7ff" : "#5ad6a0"; }
      else if (/true|false/.test(m)) c = "#ff924b";
      else if (/null/.test(m)) c = "#ff6b8a";
      return '<span style="color:' + c + '">' + m + "</span>";
    });
    return h("pre", { style: { margin: 0, fontFamily: "'IBM Plex Mono',monospace", fontSize: "11.5px", lineHeight: 1.6, color: "#cfc7f0", whiteSpace: "pre", tabSize: 2 }, dangerouslySetInnerHTML: { __html: s } });
  };

  // ---------- status ----------
  Office.prototype.statusFor = function (id) {
    if (id === "orchestrator") return "coord";
    var active = this.activeRole();
    var ai = this.PIPE.indexOf(active), i = this.PIPE.indexOf(id);
    if (ai < 0) return "queued";
    if (i < ai) return "done";
    if (i === ai) return "working";
    if (i === ai + 1) return "waiting";
    return "queued";
  };
  Office.prototype.statusMeta = function (s) {
    return ({
      done: { c: "#5b9cff", t: "completado" }, working: { c: "#46d39a", t: "trabajando" },
      waiting: { c: "#ffc24b", t: "en espera" }, queued: { c: "#6c63a0", t: "en cola" },
      coord: { c: "#ffd84b", t: "coordinando" }
    })[s];
  };

  // ---------- panel actions ----------
  Office.prototype.open = function (id) {
    var self = this;
    this.setState({ openRole: id, tab: "estado" });
    fetch("/api/result/" + id).then(function (r) { return r.json(); }).then(function (j) {
      self.setState(function (st) { var m = Object.assign({}, st.results); m[id] = j; return { results: m }; });
    }).catch(function () {});
  };
  Office.prototype.setTab = function (t) { this.setState({ tab: t }); };

  // ---------- screen-mode toggle ----------
  Office.prototype.modeToggle = function () {
    var pill = function (on) {
      return {
        textDecoration: "none", cursor: "pointer", fontFamily: "'Silkscreen'", fontSize: "8.5px",
        letterSpacing: ".3px", padding: "5px 9px", borderRadius: "7px", whiteSpace: "nowrap",
        color: on ? "#15102b" : "#c4bbe8", background: on ? "#ffd84b" : "#1c1638",
        border: "1px solid " + (on ? "#ffd84b" : "#2c2250")
      };
    };
    var remember = function (mode) { try { localStorage.setItem("orqScreenMode", mode); } catch (e) {} };
    return el("div", { style: "display:flex;gap:4px;min-width:max-content;" },
      h("a", { href: "/", className: "modepill", style: pill(false), onClick: function () { remember("dashboard"); } }, "▣ Dashboard"),
      h("span", { className: "modepill", style: pill(true), onClick: function () { remember("office"); } }, "🎮 Office"));
  };

  // ---------- compute render values ----------
  Office.prototype.computeVals = function () {
    var self = this, S = this.state;
    var showPath = true, showCrt = true;

    var stations = Object.keys(this.LAYOUT).map(function (id) {
      var L = self.LAYOUT[id], R = self.ROLE_META[id], st = self.statusFor(id), sm = self.statusMeta(st);
      var cw = L.hub ? 124 : 120, ch = L.hub ? 150 : 128;
      return {
        id: id, label: R.label,
        cellStyle: { position: "absolute", left: L.x + "px", top: L.y + "px", width: cw + "px", height: ch + "px", zIndex: 5 },
        rugStyle: { position: "absolute", left: "50%", top: (L.hub ? 70 : 78) + "px", transform: "translateX(-50%)", width: (L.hub ? 112 : 96) + "px", height: (L.hub ? 64 : 54) + "px", borderRadius: "50%", background: R.color + "1f", boxShadow: "0 0 22px " + R.color + "22", filter: "blur(1px)" },
        deskStyle: { position: "absolute", left: "50%", top: "6px", transform: "translateX(-50%)", width: (L.hub ? 100 : 90) + "px", height: (L.hub ? 46 : 40) + "px", borderRadius: "8px", background: "linear-gradient(180deg,#4a4368,#37314f)", border: "2px solid #221830", boxShadow: "0 4px 0 #1d1730, 0 6px 10px #0006", cursor: "pointer", display: "flex", alignItems: "center", justifyContent: "center" },
        screenStyle: { width: (L.hub ? 54 : 46) + "px", height: (L.hub ? 26 : 22) + "px", borderRadius: "4px", background: "#120c24", border: "2px solid #0c081c", boxShadow: "inset 0 0 10px " + R.color + (st === "working" ? "aa" : "55") + ", 0 0 8px " + R.color + (st === "working" ? "66" : "22"), backgroundImage: "repeating-linear-gradient(0deg," + R.color + "33 0 2px,transparent 2px 5px)" },
        plateStyle: { position: "absolute", left: "50%", top: (L.hub ? 54 : 52) + "px", transform: "translateX(-50%)", fontFamily: "'Silkscreen'", fontSize: (L.hub ? 9 : 8.5) + "px", letterSpacing: ".4px", color: "#fff", background: "#15102b", border: "1px solid " + R.color + "66", borderRadius: "5px", padding: "2px 7px", whiteSpace: "nowrap", boxShadow: "0 2px 5px #0007", zIndex: 8 },
        charStyle: { position: "absolute", left: "50%", top: (L.hub ? 86 : 92) + "px", transform: "translateX(-50%)", cursor: "pointer", animation: "bob " + (1.4 + (id.length % 5) * 0.12) + "s ease-in-out infinite", filter: "drop-shadow(0 3px 3px #0007)", zIndex: 7 },
        dotStyle: { position: "absolute", left: "calc(50% + 14px)", top: (L.hub ? 80 : 86) + "px", width: "9px", height: "9px", borderRadius: "50%", background: sm.c, boxShadow: "0 0 8px " + sm.c, border: "1.5px solid #15102b", zIndex: 9, animation: st === "working" ? "pls 1.1s ease-in-out infinite" : (st === "coord" ? "pls 1.6s ease-in-out infinite" : "none") },
        promptStyle: { position: "absolute", left: "50%", top: "-2px", transform: "translateX(-50%)", fontFamily: "'Silkscreen'", fontSize: "9px", color: "#15102b", background: "#ffd84b", borderRadius: "6px", padding: "3px 8px", whiteSpace: "nowrap", boxShadow: "0 3px 8px #000a, 0 0 14px #ffd84b66", zIndex: 12, animation: "float 1.6s ease-in-out infinite" },
        promptText: L.hub ? "E · " + R.label : "E · hablar",
        spriteEl: self.spriteEl(id, L.hub ? 5 : 4),
        near: (S.near === id && !S.openRole),
        onOpen: function () { self.open(id); }
      };
    });

    var pipeline = this.PIPE.map(function (id) {
      var R = self.ROLE_META[id], st = self.statusFor(id), sm = self.statusMeta(st), on = (id === self.activeRole());
      return {
        id: id, short: R.label, label: R.label + " · " + sm.t,
        chipStyle: { display: "flex", alignItems: "center", gap: 5, cursor: "pointer", padding: "4px 8px", borderRadius: "7px", whiteSpace: "nowrap", color: on ? "#15102b" : "#c4bbe8", background: on ? R.color : "#1c1638", border: "1px solid " + (on ? R.color : "#2c2250"), boxShadow: on ? "0 0 14px " + R.color + "77" : "none", transition: "all .15s" },
        dotStyle: { width: 7, height: 7, borderRadius: "50%", background: on ? "#15102b" : sm.c, boxShadow: on ? "none" : "0 0 6px " + sm.c, display: "inline-block", animation: st === "working" ? "pls 1.1s ease-in-out infinite" : "none" },
        onClick: function () { self.open(id); }
      };
    });

    var pathEl = null;
    if (showPath) {
      var C = {}; Object.keys(this.LAYOUT).forEach(function (id) { var L = self.LAYOUT[id]; C[id] = { x: L.x + (L.hub ? 62 : 60), y: L.y + (L.hub ? 70 : 78) }; });
      var pts = this.PIPE.map(function (id) { return C[id]; });
      var poly = pts.map(function (p) { return p.x + "," + p.y; }).join(" ");
      var active = this.activeRole() || "coder", ac = this.ROLE_META[active].color, acP = C[active];
      var spokes = this.PIPE.map(function (id, i) { return h("line", { key: "sp" + i, x1: C.orchestrator.x, y1: C.orchestrator.y, x2: C[id].x, y2: C[id].y, stroke: "#ffffff10", strokeWidth: 2, strokeDasharray: "2 6" }); });
      pathEl = h("svg", { width: this.W, height: this.H, style: { position: "absolute", left: 0, top: 0, overflow: "visible" } },
        spokes,
        h("polyline", { points: poly, fill: "none", stroke: "#ffffff12", strokeWidth: 7, strokeLinejoin: "round", strokeLinecap: "round" }),
        h("polyline", { points: poly, fill: "none", stroke: "#ffffff2e", strokeWidth: 3, strokeLinejoin: "round", strokeLinecap: "round", strokeDasharray: "4 12", style: { animation: "flow 1.1s linear infinite" } }),
        h("circle", { cx: acP.x, cy: acP.y, r: 16, fill: "none", stroke: ac, strokeWidth: 2, style: { transformOrigin: acP.x + "px " + acP.y + "px", animation: "tok 1.4s ease-in-out infinite" } }),
        h("circle", { cx: acP.x, cy: acP.y, r: 7, fill: ac, style: { filter: "drop-shadow(0 0 8px " + ac + ")" } }));
    }

    var doneN = this.state.tasks.filter(function (t) { return t.status === "done"; }).length;
    var costStr = (this.state.cost && this.state.cost.available) ? "$" + Number(this.state.cost.total_usd).toFixed(2) : "$0.00";
    var stats = { done: doneN, total: this.state.tasks.length, cycle: this.currentCycle(), cost: costStr };

    var oid = S.openRole, open = null, panelStyle = null;
    if (oid) {
      var Ro = this.ROLE_META[oid], sto = this.statusFor(oid), smo = this.statusMeta(sto);
      var rc = Ro.color, rcSoft = rc + "1c";
      var tabStyle = function (name) {
        return { cursor: "pointer", padding: "8px 12px", fontFamily: "'Silkscreen'", fontSize: "8.5px", letterSpacing: ".3px", borderRadius: "7px 7px 0 0", color: S.tab === name ? "#fff" : "#8a7fb8", background: S.tab === name ? "#19132f" : "transparent", borderBottom: S.tab === name ? "2px solid " + rc : "2px solid transparent" };
      };
      var ct = this.currentTask();
      var res = this.state.results[oid];
      var jsonNode;
      if (res === undefined) jsonNode = h("div", { style: { color: "#6c63a0", fontSize: "11.5px" } }, "Cargando…");
      else if (res === null) jsonNode = h("div", { style: { color: "#6c63a0", fontSize: "11.5px" } }, "Sin resultado todavía.");
      else jsonNode = this.jsonEl(res);
      open = {
        label: Ro.label, desc: Ro.desc, spriteEl: this.spriteEl(oid, 4), statusLabel: smo.t,
        badgeStyle: { fontFamily: "'Silkscreen'", fontSize: "8px", color: smo.c, background: smo.c + "1f", border: "1px solid " + smo.c + "55", borderRadius: "6px", padding: "4px 8px", whiteSpace: "nowrap" },
        taskId: ct.id, taskTitle: ct.title, activity: this.roleActivity(oid), statChips: this.roleStatChips(oid), summary: this.roleSummary(oid),
        isEstado: S.tab === "estado", isResumen: S.tab === "resumen", isJson: S.tab === "json",
        tabEstadoStyle: tabStyle("estado"), tabResumenStyle: tabStyle("resumen"), tabJsonStyle: tabStyle("json"),
        tEstado: function () { self.setTab("estado"); }, tResumen: function () { self.setTab("resumen"); }, tJson: function () { self.setTab("json"); },
        jsonEl: jsonNode
      };
      panelStyle = S.isMobile
        ? { position: "absolute", left: 0, right: 0, bottom: 0, height: "72%", display: "flex", flexDirection: "column", background: "#15102b", borderTop: "2px solid " + rc, borderRadius: "16px 16px 0 0", boxShadow: "0 -10px 40px #000a", zIndex: 50, "--rc": rc, "--rc-soft": rcSoft, overflow: "hidden" }
        : { position: "absolute", right: 0, top: 0, bottom: 0, width: "390px", display: "flex", flexDirection: "column", background: "#15102b", borderLeft: "2px solid " + rc, boxShadow: "-12px 0 40px #000a", zIndex: 50, "--rc": rc, "--rc-soft": rcSoft, overflow: "hidden" };
    }

    var dpad = {
      upD: function () { self.keys.up = true; }, upU: function () { self.keys.up = false; },
      downD: function () { self.keys.down = true; }, downU: function () { self.keys.down = false; },
      leftD: function () { self.keys.left = true; }, leftU: function () { self.keys.left = false; },
      rightD: function () { self.keys.right = true; }, rightU: function () { self.keys.right = false; }
    };
    var dpadBtn = { display: "flex", alignItems: "center", justifyContent: "center", background: "rgba(36,28,64,0.82)", border: "1.5px solid #3a2f5c", borderRadius: "10px", color: "#cbbff0", fontSize: "15px", userSelect: "none", touchAction: "none", backdropFilter: "blur(4px)" };

    return {
      feature: this.featureInfo(),
      pipeline: pipeline, stations: stations, stats: stats,
      stageStyle: { position: "absolute", left: 0, top: 0, width: this.W + "px", height: this.H + "px", transformOrigin: "0 0", transform: "translate(" + S.tx + "px," + S.ty + "px) scale(" + S.scale + ")", background: "repeating-conic-gradient(#332e4c 0 25%, #3a3556 0 50%) 0 0/72px 72px", border: "14px solid #1b1430", borderRadius: "10px", boxShadow: "inset 0 0 0 2px #2a2348, inset 0 0 90px #0007, 0 30px 80px #000a" },
      showPath: showPath, pathEl: pathEl, playerSpriteEl: this.spriteEl("player", 4),
      openRole: oid, open: open, panelStyle: panelStyle, closePanel: function () { self.setState({ openRole: "" }); },
      showCrt: showCrt, isMobile: S.isMobile,
      hintStyle: { position: "absolute", left: "14px", bottom: "12px", display: S.isMobile ? "none" : "block", fontSize: "10px", color: "#9a8fc8", background: "rgba(18,12,38,0.7)", border: "1px solid #2c2250", borderRadius: "8px", padding: "6px 11px", backdropFilter: "blur(6px)", zIndex: 20 },
      dpadWrapStyle: { position: "absolute", right: "14px", bottom: "14px", display: S.isMobile ? "block" : "none", zIndex: 25, opacity: .96 },
      dpad: dpad, dpadBtn: dpadBtn
    };
  };

  // ---------- render ----------
  Office.prototype.render = function () {
    var self = this, v = this.computeVals();

    var hud = el("div", { style: "flex:0 0 auto;display:flex;align-items:center;gap:16px;padding:9px 16px;border-bottom:2px solid #2c2250;background:rgba(18,12,38,0.82);backdrop-filter:blur(8px);z-index:30;flex-wrap:wrap;" },
      el("div", { style: "display:flex;align-items:center;gap:10px;min-width:max-content;" },
        el("div", { style: "width:26px;height:26px;border-radius:6px;background:linear-gradient(135deg,#ffd84b,#ff924b);box-shadow:0 0 14px #ffba4b66;display:flex;align-items:center;justify-content:center;font-family:'Silkscreen';font-size:14px;color:#1a1430;" }, "O"),
        el("div", { style: "line-height:1.05;" },
          el("div", { style: "font-family:'Silkscreen';font-size:12px;letter-spacing:.5px;color:#fff;" }, "ORQUESTALITE"),
          el("div", { style: "font-size:9.5px;color:#8a7fb8;letter-spacing:.3px;margin-top:2px;" }, "factory · oficina virtual"))),
      this.modeToggle(),
      el("div", { style: "display:flex;align-items:center;gap:8px;min-width:max-content;padding-left:14px;border-left:1px solid #2c2250;" },
        el("span", { style: "font-size:11px;color:#b9b0e0;" }, v.feature.name),
        el("span", { style: "font-size:10px;color:#46d39a;background:#46d39a1f;border:1px solid #46d39a44;border-radius:5px;padding:2px 7px;display:inline-flex;align-items:center;gap:4px;" }, "⎇ " + v.feature.branch)),
      el("div", { style: "display:flex;align-items:center;gap:2px;flex:1 1 320px;min-width:0;overflow-x:auto;padding:2px;" },
        v.pipeline.map(function (p, i) {
          return el("div", { key: i, onClick: p.onClick, title: p.label, className: "hb25", style: p.chipStyle },
            h("span", { style: p.dotStyle }),
            el("span", { style: "font-family:'Silkscreen';font-size:8.5px;letter-spacing:.2px;" }, p.short));
        })),
      el("div", { style: "display:flex;align-items:center;gap:14px;min-width:max-content;margin-left:auto;" },
        el("div", { style: "text-align:right;line-height:1.1;" },
          el("div", { style: "font-family:'Silkscreen';font-size:11px;color:#fff;" }, v.stats.done, el("span", { style: "color:#6c63a0;" }, "/" + v.stats.total)),
          el("div", { style: "font-size:8.5px;color:#8a7fb8;" }, "tareas")),
        el("div", { style: "text-align:right;line-height:1.1;" },
          el("div", { style: "font-family:'Silkscreen';font-size:11px;color:#fff;" }, "C" + v.stats.cycle),
          el("div", { style: "font-size:8.5px;color:#8a7fb8;" }, "ciclo")),
        el("div", { style: "text-align:right;line-height:1.1;" },
          el("div", { style: "font-family:'Silkscreen';font-size:11px;color:#ffd84b;" }, v.stats.cost),
          el("div", { style: "font-size:8.5px;color:#8a7fb8;" }, "gasto")),
        el("div", { style: "text-align:right;line-height:1.1;" },
          el("div", { ref: this.elapsedRef, style: "font-family:'Silkscreen';font-size:11px;color:" + (this.state.connected ? "#46d39a" : "#6c63a0") + ";" }, "00:00:00"),
          el("div", { style: "font-size:8.5px;color:#8a7fb8;" }, "en vivo"))));

    var stage = el("div", { ref: this.stageWrapRef, style: "position:relative;flex:1 1 auto;overflow:hidden;" },
      el("div", { ref: this.stageRef, style: v.stageStyle },
        v.showPath ? el("div", { style: "position:absolute;inset:0;pointer-events:none;z-index:2;" }, v.pathEl) : null,
        v.stations.map(function (st, i) {
          return el("div", { key: i, style: st.cellStyle },
            h("div", { style: st.rugStyle }),
            el("div", { onClick: st.onOpen, className: "hb12", style: st.deskStyle }, h("div", { style: st.screenStyle })),
            el("div", { style: st.plateStyle }, st.label),
            el("div", { onClick: st.onOpen, style: st.charStyle }, st.spriteEl),
            h("div", { style: st.dotStyle }),
            st.near ? el("div", { style: st.promptStyle }, st.promptText) : null);
        }),
        el("div", { ref: this.playerRef, style: "position:absolute;left:0;top:0;transform:translate(204px,250px);z-index:18;will-change:transform;" },
          el("div", { ref: this.facingRef, style: "animation:bob 1.5s ease-in-out infinite;transform-origin:50% 100%;" }, v.playerSpriteEl),
          el("div", { style: "position:absolute;top:-15px;left:50%;font-family:'Silkscreen';font-size:8px;color:#ffd84b;text-shadow:0 1px 0 #000,0 0 6px #ffba4b88;white-space:nowrap;animation:float 2s ease-in-out infinite;" }, "TÚ"))),
      el("div", { style: v.hintStyle },
        el("span", { style: "color:#ffd84b;font-family:'Silkscreen';font-size:8px;" }, "WASD"), " moverse  ·  ",
        el("span", { style: "color:#ffd84b;font-family:'Silkscreen';font-size:8px;" }, "E"), " hablar"),
      el("div", { style: v.dpadWrapStyle },
        el("div", { style: "display:grid;grid-template-columns:repeat(3,46px);grid-template-rows:repeat(3,46px);gap:4px;" },
          h("div"), el("div", { onPointerDown: v.dpad.upD, onPointerUp: v.dpad.upU, onPointerLeave: v.dpad.upU, style: v.dpadBtn }, "▲"), h("div"),
          el("div", { onPointerDown: v.dpad.leftD, onPointerUp: v.dpad.leftU, onPointerLeave: v.dpad.leftU, style: v.dpadBtn }, "◀"),
          el("div", { onPointerDown: v.dpad.downD, onPointerUp: v.dpad.downU, onPointerLeave: v.dpad.downU, style: v.dpadBtn }, "▼"),
          el("div", { onPointerDown: v.dpad.rightD, onPointerUp: v.dpad.rightU, onPointerLeave: v.dpad.rightU, style: v.dpadBtn }, "▶"))));

    var panel = null;
    if (v.openRole) {
      var o = v.open;
      panel = [
        el("div", { key: "ov", onClick: v.closePanel, style: "position:absolute;inset:0;background:rgba(6,4,16,0.55);z-index:40;animation:fade .18s ease;" }),
        el("div", { key: "pn", style: v.panelStyle },
          el("div", { style: "flex:0 0 auto;display:flex;align-items:center;gap:12px;padding:14px 16px;border-bottom:2px solid #2c2250;background:linear-gradient(180deg, var(--rc-soft), transparent);" },
            el("div", { style: "width:40px;height:54px;display:flex;align-items:flex-end;justify-content:center;filter:drop-shadow(0 2px 4px #0008);" }, o.spriteEl),
            el("div", { style: "flex:1 1 auto;min-width:0;" },
              el("div", { style: "font-family:'Silkscreen';font-size:14px;color:#fff;letter-spacing:.4px;" }, o.label),
              el("div", { style: "font-size:10px;color:#9a8fc8;margin-top:3px;" }, o.desc)),
            el("div", { style: o.badgeStyle }, o.statusLabel),
            el("div", { onClick: v.closePanel, className: "btnx", style: "cursor:pointer;width:28px;height:28px;border-radius:7px;display:flex;align-items:center;justify-content:center;background:#241c40;color:#b9b0e0;font-size:15px;" }, "✕")),
          el("div", { style: "flex:0 0 auto;display:flex;gap:4px;padding:8px 12px 0;border-bottom:1px solid #251d44;background:#15102b;" },
            el("div", { onClick: o.tEstado, style: o.tabEstadoStyle }, "ESTADO"),
            el("div", { onClick: o.tResumen, style: o.tabResumenStyle }, "RESUMEN"),
            el("div", { onClick: o.tJson, style: o.tabJsonStyle }, "JSON")),
          o.isEstado ? el("div", { style: "flex:1 1 auto;overflow-y:auto;padding:16px;" },
            el("div", { style: "border:1px solid #2e2550;border-radius:10px;padding:14px;background:#19132f;box-shadow:inset 0 0 0 1px var(--rc-soft);" },
              el("div", { style: "font-size:9px;color:#8a7fb8;letter-spacing:1px;" }, "TAREA ACTUAL"),
              el("div", { style: "display:flex;align-items:center;gap:8px;margin-top:7px;" },
                el("span", { style: "font-family:'Silkscreen';font-size:10px;color:var(--rc);background:var(--rc-soft);border-radius:5px;padding:2px 6px;" }, o.taskId),
                el("span", { style: "font-size:12px;color:#ece7ff;" }, o.taskTitle))),
            el("div", { style: "font-size:9px;color:#8a7fb8;letter-spacing:1px;margin:18px 0 9px;" }, "ACTIVIDAD · run.log"),
            o.activity.length ? o.activity.map(function (a, i) {
              return el("div", { key: i, style: "display:flex;gap:10px;padding:7px 0;border-bottom:1px dashed #251d44;" },
                el("span", { style: "font-size:10px;color:#6c63a0;flex:0 0 auto;font-variant-numeric:tabular-nums;" }, a.t),
                el("span", { style: "font-size:11px;color:#c7bff0;line-height:1.4;" }, a.m));
            }) : el("div", { style: "font-size:11px;color:#6c63a0;" }, "Sin actividad todavía."),
            el("div", { style: "display:grid;grid-template-columns:1fr 1fr;gap:8px;margin-top:16px;" },
              o.statChips.map(function (sc, i) {
                return el("div", { key: i, style: "background:#19132f;border:1px solid #2a2248;border-radius:8px;padding:9px 11px;" },
                  el("div", { style: "font-family:'Silkscreen';font-size:12px;color:#fff;" }, sc.v),
                  el("div", { style: "font-size:9px;color:#8a7fb8;margin-top:3px;" }, sc.k));
              }))) : null,
          o.isResumen ? el("div", { style: "flex:1 1 auto;overflow-y:auto;padding:16px;" },
            el("div", { style: "font-size:9px;color:#8a7fb8;letter-spacing:1px;margin-bottom:9px;" }, "RESUMEN DE SESIÓN"),
            el("div", { style: "font-size:12.5px;line-height:1.65;color:#d9d2f5;border-left:3px solid var(--rc);padding-left:13px;" }, o.summary)) : null,
          o.isJson ? el("div", { style: "flex:1 1 auto;overflow:auto;padding:14px;background:#0c0820;" },
            el("div", { style: "font-size:9px;color:#6c63a0;letter-spacing:1px;margin-bottom:10px;" }, ".orquestalite/results/" + v.openRole + ".json"),
            o.jsonEl) : null)
      ];
    }

    var crt = null;
    if (v.showCrt) {
      crt = [
        el("div", { key: "c1", style: "position:fixed;inset:0;pointer-events:none;z-index:60;background-image:repeating-linear-gradient(0deg, rgba(0,0,0,0.16) 0 1px, transparent 1px 3px);mix-blend-mode:multiply;" }),
        el("div", { key: "c2", style: "position:fixed;inset:0;pointer-events:none;z-index:60;background:radial-gradient(120% 120% at 50% 50%, transparent 62%, rgba(0,0,0,0.42) 100%);" })
      ];
    }

    return el("div", { style: "position:fixed;inset:0;display:flex;flex-direction:column;background:radial-gradient(130% 120% at 50% -15%, #241a47 0%, #110c24 60%, #0b0818 100%);color:#ece7ff;font-family:'IBM Plex Mono',monospace;" },
      hud, stage, panel, crt);
  };

  var root = document.getElementById("root");
  if (ReactDOM.createRoot) ReactDOM.createRoot(root).render(h(Office));
  else ReactDOM.render(h(Office), root);
})();
