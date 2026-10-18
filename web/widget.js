/* Ask OLake — embeddable chat widget. Vanilla JS. */
(function () {
  "use strict";
  if (window.__askOlakeLoaded) return;
  window.__askOlakeLoaded = true;

  var script = document.currentScript || document.querySelector('script[src$="widget.js"]');
  var apiUrl = (script && script.getAttribute("data-api-url")) || "";
  if (!apiUrl && script && script.src) {
    try { apiUrl = new URL(script.src, location.href).origin; } catch (e) { apiUrl = ""; }
  }
  apiUrl = apiUrl.replace(/\/+$/, "");
  var title = (script && script.getAttribute("data-title")) || "Ask OLake";

  var MAX_CHARS = 2000;
  var COUNTER_AT = 1800;
  var TIMEOUT_MS = 50000;
  var EXAMPLES = [
    "How do I set up CDC for Postgres?",
    "How do I deploy OLake on Kubernetes with Helm?",
    "How does OLake Fusion compact Iceberg tables?"
  ];

  var CSS = [
    ":host{all:initial}",
    ".w{--bg:#ffffff;--fg:#1d2433;--muted:#5b6476;--line:#e3e7ee;--soft:#f4f6fa;--accent:#2a5bd7;--accent-fg:#ffffff;--user:#2a5bd7;--user-fg:#ffffff;--bot:#f4f6fa;--code:#0f172a;--code-fg:#e2e8f0;--shadow:0 12px 40px rgba(16,24,40,.18);",
    "font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.5;color:var(--fg)}",
    ".w.dark{--bg:#151a23;--fg:#e6e9ef;--muted:#9aa3b2;--line:#2a3140;--soft:#1d2330;--accent:#6c95ff;--accent-fg:#0b1020;--user:#3b6cf0;--user-fg:#ffffff;--bot:#1d2330;--code:#0b0f17;--code-fg:#e2e8f0;--shadow:0 12px 40px rgba(0,0,0,.5)}",
    ".launcher{position:fixed;right:20px;bottom:20px;display:inline-flex;align-items:center;gap:8px;padding:10px 16px;border:0;border-radius:999px;background:var(--accent);color:var(--accent-fg);font:inherit;font-weight:600;cursor:pointer;box-shadow:var(--shadow);transition:transform .15s ease}",
    ".launcher[hidden]{display:none}",
    ".launcher:hover{transform:translateY(-1px)}",
    ".launcher:focus-visible,button:focus-visible,textarea:focus-visible,a:focus-visible{outline:2px solid var(--accent);outline-offset:2px}",
    ".launcher svg{width:18px;height:18px}",
    ".panel{position:fixed;right:20px;bottom:20px;width:400px;height:600px;max-height:calc(100vh - 40px);max-width:calc(100vw - 40px);display:flex;flex-direction:column;background:var(--bg);color:var(--fg);border:1px solid var(--line);border-radius:14px;box-shadow:var(--shadow);overflow:hidden;animation:pop .16s ease-out}",
    ".panel[hidden]{display:none}",
    "@keyframes pop{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}",
    "@media (max-width:479px){.panel{right:0;bottom:0;width:100vw;height:100%;max-width:none;max-height:none;border-radius:0;border:0}}",
    ".head{display:flex;align-items:center;justify-content:space-between;padding:12px 14px;border-bottom:1px solid var(--line)}",
    ".head h2{margin:0;font-size:15px;font-weight:650}",
    ".close{border:0;background:transparent;color:var(--muted);font-size:22px;line-height:1;cursor:pointer;padding:2px 6px;border-radius:6px}",
    ".close:hover{background:var(--soft);color:var(--fg)}",
    ".log{flex:1;overflow-y:auto;padding:14px;display:flex;flex-direction:column;gap:10px}",
    ".msg{max-width:92%;padding:9px 12px;border-radius:12px;overflow-wrap:anywhere;word-break:break-word}",
    ".msg.user{align-self:flex-end;background:var(--user);color:var(--user-fg);white-space:pre-wrap;border-bottom-right-radius:4px}",
    ".msg.bot{align-self:flex-start;background:var(--bot);border-bottom-left-radius:4px}",
    ".msg.err{align-self:flex-start;background:transparent;border:1px solid var(--line);color:var(--muted)}",
    ".msg p{margin:0 0 8px}.msg p:last-child{margin-bottom:0}",
    ".msg ul,.msg ol{margin:4px 0 8px;padding-left:20px}",
    ".msg a{color:var(--accent)}",
    ".msg code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12.5px;background:var(--soft);padding:1px 4px;border-radius:4px}",
    ".msg.bot code{background:rgba(127,127,127,.16)}",
    ".code{position:relative;margin:6px 0 8px}",
    ".code pre{margin:0;background:var(--code);color:var(--code-fg);padding:10px 12px;border-radius:8px;overflow-x:auto}",
    ".code pre code{background:transparent;padding:0;color:inherit;white-space:pre}",
    ".copy{position:absolute;top:6px;right:6px;border:1px solid rgba(255,255,255,.2);background:rgba(255,255,255,.08);color:#e2e8f0;font:inherit;font-size:11px;padding:2px 8px;border-radius:6px;cursor:pointer}",
    "sup.cite{font-size:10.5px;line-height:0}sup.cite a{text-decoration:none;font-weight:600;padding:0 1px}",
    ".sources{margin-top:8px;padding-top:8px;border-top:1px solid var(--line);font-size:12.5px}",
    ".sources h3{margin:0 0 4px;font-size:12px;font-weight:650;color:var(--muted);text-transform:uppercase;letter-spacing:.04em}",
    ".sources ol{margin:0;padding-left:18px}.sources li{margin:2px 0}",
    ".typing{display:inline-flex;gap:4px;padding:4px 0}.typing span{width:6px;height:6px;border-radius:50%;background:var(--muted);animation:blink 1.2s infinite ease-in-out}",
    ".typing span:nth-child(2){animation-delay:.15s}.typing span:nth-child(3){animation-delay:.3s}",
    "@keyframes blink{0%,80%,100%{opacity:.25;transform:translateY(0)}40%{opacity:1;transform:translateY(-2px)}}",
    ".empty{color:var(--muted);display:flex;flex-direction:column;gap:8px}",
    ".empty p{margin:0}",
    ".ex{text-align:left;border:1px solid var(--line);background:var(--bg);color:var(--fg);font:inherit;padding:8px 10px;border-radius:10px;cursor:pointer}",
    ".ex:hover{background:var(--soft)}",
    ".composer{display:flex;gap:8px;align-items:flex-end;padding:10px 12px;border-top:1px solid var(--line)}",
    ".composer textarea{flex:1;resize:none;border:1px solid var(--line);border-radius:10px;padding:8px 10px;font:inherit;line-height:20px;max-height:116px;background:var(--bg);color:var(--fg);box-sizing:border-box}",
    ".send{border:0;border-radius:10px;padding:9px 14px;background:var(--accent);color:var(--accent-fg);font:inherit;font-weight:600;cursor:pointer}",
    ".send:disabled{opacity:.5;cursor:not-allowed}",
    ".counter{font-size:11px;color:var(--muted);padding:0 14px 0;text-align:right}",
    ".counter[hidden]{display:none}",
    ".foot{font-size:11px;color:var(--muted);padding:2px 14px 10px}",
    "@media (prefers-reduced-motion:reduce){*{animation:none!important;transition:none!important}}"
  ].join("\n");

  var CHAT_ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>';

  var host, root, wrap, launcher, panel, log, input, sendBtn, counter, emptyEl;
  var pending = false;

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function isHttp(u) { return /^https?:\/\//i.test(u || ""); }

  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }

  /* XSS-safe Markdown subset. The whole text is escaped first; every tag
     below is produced by this function, never taken from the input. */
  function renderMarkdown(text, sources) {
    var t = esc(text || "");
    var blocks = [], inl = [], links = [];
    var ph = function (kind, i) { return "\u0000" + kind + i + "\u0000"; };

    t = t.replace(/```([\w+.#-]*)[^\n]*\n([\s\S]*?)```/g, function (_, lang, code) {
      blocks.push(code.replace(/\n$/, ""));
      return "\n" + ph("B", blocks.length - 1) + "\n";
    });
    t = t.replace(/`([^`\n]+)`/g, function (_, c) { inl.push(c); return ph("I", inl.length - 1); });
    t = t.replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>");
    t = t.replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g, function (_, label, url) {
      links.push('<a href="' + url + '" target="_blank" rel="noopener noreferrer">' + label + "</a>");
      return ph("L", links.length - 1);
    });
    t = t.replace(/https?:\/\/[^\s<>()\[\]\u0000]+/g, function (u) {
      var trail = "";
      var cut = u.search(/&(?:quot|#39|lt|gt);/);
      if (cut >= 0) { trail = u.slice(cut); u = u.slice(0, cut); }
      var m = u.match(/[.,;:!?]+$/);
      if (m) { trail = m[0] + trail; u = u.slice(0, -m[0].length); }
      links.push('<a href="' + u + '" target="_blank" rel="noopener noreferrer">' + u + "</a>");
      return ph("L", links.length - 1) + trail;
    });

    var html = [], para = [], list = null;
    function flushPara() { if (para.length) { html.push("<p>" + para.join("<br>") + "</p>"); para = []; } }
    // An ordered list split by a code block or a line keeps its numbering (start="N").
    function flushList() { if (list) { var start = list.start > 1 ? ' start="' + list.start + '"' : ""; html.push("<" + list.tag + start + ">" + list.items.join("") + "</" + list.tag + ">"); list = null; } }
    t.split("\n").forEach(function (line) {
      var m;
      if (/^\u0000B\d+\u0000$/.test(line.trim())) { flushPara(); flushList(); html.push(line.trim()); return; }
      if ((m = line.match(/^\s*[-*]\s+(.*)$/))) { flushPara(); if (!list || list.tag !== "ul") { flushList(); list = { tag: "ul", items: [] }; } list.items.push("<li>" + m[1] + "</li>"); return; }
      if ((m = line.match(/^\s*(\d+)\.\s+(.*)$/))) { flushPara(); if (!list || list.tag !== "ol") { flushList(); list = { tag: "ol", items: [], start: +m[1] }; } list.items.push("<li>" + m[2] + "</li>"); return; }
      if ((m = line.match(/^#{1,6}\s+(.*)$/))) { flushPara(); flushList(); html.push("<p><strong>" + m[1] + "</strong></p>"); return; }
      if (!line.trim()) { flushPara(); flushList(); return; }
      flushList();
      para.push(line);
    });
    flushPara(); flushList();
    var out = html.join("");

    out = out.replace(/\[(\d+)\]/g, function (m, n) {
      n = +n;
      if (n >= 1 && n <= sources.length && isHttp(sources[n - 1])) {
        return '<sup class="cite"><a href="' + esc(sources[n - 1]) + '" target="_blank" rel="noopener noreferrer">' + n + "</a></sup>";
      }
      return m;
    });
    out = out.replace(/\u0000L(\d+)\u0000/g, function (_, i) { return links[+i]; });
    out = out.replace(/\u0000I(\d+)\u0000/g, function (_, i) { return "<code>" + inl[+i] + "</code>"; });
    out = out.replace(/\u0000B(\d+)\u0000/g, function (_, i) {
      return '<div class="code"><button type="button" class="copy">Copy</button><pre><code>' + blocks[+i] + "</code></pre></div>";
    });
    return out;
  }

  function sourceLabel(u) {
    var p;
    try { p = new URL(u); } catch (e) { return u; }
    var host = p.hostname.replace(/^www\./, "");
    var anchor = p.hash ? " " + decodeURIComponent(p.hash) : "";
    if (host === "olake.io") {
      var path = p.pathname.replace(/^\/docs\/?/, "").replace(/\/+$/, "");
      return (path ? path.split("/").join(" › ") : "docs") + anchor;
    }
    if (host === "github.com") {
      var seg = p.pathname.split("/").filter(Boolean); // owner, repo, ...
      var rest = seg.slice(2);
      if (rest[0] === "blob" || rest[0] === "tree") rest = rest.slice(2);
      var tail = rest.slice(-2).join("/");
      return "GitHub · " + (seg[1] || "") + (tail ? "/" + tail : "") + (p.hash && /^#L\d+/.test(p.hash) ? p.hash : "");
    }
    return host + p.pathname.replace(/\/+$/, "");
  }

  function renderSources(container, sources) {
    if (!sources.length) return;
    var box = el("div", "sources");
    box.appendChild(el("h3", null, "Sources"));
    var ol = el("ol");
    sources.forEach(function (u) {
      var li = el("li");
      if (isHttp(u)) {
        var a = el("a", null, sourceLabel(u));
        a.href = u; a.target = "_blank"; a.rel = "noopener noreferrer";
        li.appendChild(a);
      } else {
        li.textContent = u;
      }
      ol.appendChild(li);
    });
    box.appendChild(ol);
    container.appendChild(box);
  }

  function wireCopyButtons(container) {
    var buttons = container.querySelectorAll("button.copy");
    Array.prototype.forEach.call(buttons, function (btn) {
      btn.addEventListener("click", function () {
        var code = btn.parentNode.querySelector("code");
        var text = code ? code.textContent : "";
        var done = function () { btn.textContent = "Copied"; setTimeout(function () { btn.textContent = "Copy"; }, 1500); };
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(done, function () { selectNode(code); });
        } else {
          selectNode(code);
        }
      });
    });
  }

  function selectNode(node) {
    if (!node) return;
    var range = document.createRange();
    range.selectNodeContents(node);
    var sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
  }

  function scrollDown() { log.scrollTop = log.scrollHeight; }

  function addUser(text) {
    if (emptyEl) { emptyEl.remove(); emptyEl = null; }
    log.appendChild(el("div", "msg user", text));
    scrollDown();
  }

  function addTyping() {
    var m = el("div", "msg bot");
    m.setAttribute("aria-label", "Assistant is typing");
    var dots = el("span", "typing");
    dots.appendChild(el("span")); dots.appendChild(el("span")); dots.appendChild(el("span"));
    m.appendChild(dots);
    log.appendChild(m);
    scrollDown();
    return m;
  }

  function showAnswer(m, data) {
    var sources = Array.isArray(data.sources) ? data.sources.filter(function (s) { return typeof s === "string"; }) : [];
    m.removeAttribute("aria-label");
    m.innerHTML = renderMarkdown(typeof data.answer === "string" ? data.answer : "", sources);
    wireCopyButtons(m);
    renderSources(m, sources);
    scrollDown();
  }

  function showError(m, text) {
    m.className = "msg err";
    m.removeAttribute("aria-label");
    m.textContent = text;
    scrollDown();
  }

  function setPending(p) {
    pending = p;
    sendBtn.disabled = p || !input.value.trim();
  }

  function ask(question) {
    question = (question || "").trim().slice(0, MAX_CHARS);
    if (!question || pending) return;
    addUser(question);
    input.focus(); // the clicked example button may have been removed
    input.value = "";
    autoGrow();
    updateCounter();
    var bubble = addTyping();
    setPending(true);
    var ctrl = typeof AbortController !== "undefined" ? new AbortController() : null;
    var timer = setTimeout(function () { if (ctrl) ctrl.abort(); }, TIMEOUT_MS);
    fetch(apiUrl + "/ask", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ question: question }),
      signal: ctrl ? ctrl.signal : undefined
    }).then(function (res) {
      return res.text().then(function (body) {
        var data = null;
        try { data = JSON.parse(body); } catch (e) { data = null; }
        if (res.ok && data) { showAnswer(bubble, data); return; }
        if (res.status === 429) { showError(bubble, "You're asking too fast. Please wait a minute and try again."); return; }
        if (data && typeof data.error === "string") { showError(bubble, data.error); return; }
        showError(bubble, "Something went wrong (HTTP " + res.status + ").");
      });
    }).catch(function () {
      showError(bubble, "Couldn't reach the assistant. Please try again.");
    }).then(function () {
      clearTimeout(timer);
      setPending(false);
    });
  }

  function autoGrow() {
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 116) + "px";
  }

  function updateCounter() {
    var n = input.value.length;
    counter.hidden = n <= COUNTER_AT;
    counter.textContent = n + " / " + MAX_CHARS;
    sendBtn.disabled = pending || !input.value.trim();
  }

  function buildPanel() {
    panel = el("div", "panel");
    panel.setAttribute("role", "dialog");
    panel.setAttribute("aria-label", title);
    var head = el("div", "head");
    head.appendChild(el("h2", null, title));
    var close = el("button", "close", "×");
    close.type = "button";
    close.setAttribute("aria-label", "Close");
    close.addEventListener("click", closePanel);
    head.appendChild(close);

    log = el("div", "log");
    log.setAttribute("role", "log");
    log.setAttribute("aria-live", "polite");
    emptyEl = el("div", "empty");
    emptyEl.appendChild(el("p", null, "Ask anything about OLake and OLake Fusion. Try:"));
    EXAMPLES.forEach(function (q) {
      var b = el("button", "ex", q);
      b.type = "button";
      b.addEventListener("click", function () { ask(q); });
      emptyEl.appendChild(b);
    });
    log.appendChild(emptyEl);

    var composer = el("div", "composer");
    input = el("textarea");
    input.rows = 1;
    input.maxLength = MAX_CHARS;
    input.placeholder = "Ask a question…";
    input.setAttribute("aria-label", "Your question");
    input.addEventListener("input", function () { autoGrow(); updateCounter(); });
    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); ask(input.value); }
    });
    sendBtn = el("button", "send", "Send");
    sendBtn.type = "button";
    sendBtn.disabled = true;
    sendBtn.addEventListener("click", function () { ask(input.value); });
    composer.appendChild(input);
    composer.appendChild(sendBtn);

    counter = el("div", "counter");
    counter.hidden = true;
    var foot = el("div", "foot", "Answers are generated from OLake docs and may be inaccurate.");

    panel.appendChild(head);
    panel.appendChild(log);
    panel.appendChild(composer);
    panel.appendChild(counter);
    panel.appendChild(foot);
    wrap.appendChild(panel);
  }

  function openPanel() {
    if (!panel) buildPanel();
    panel.hidden = false;
    launcher.hidden = true;
    launcher.setAttribute("aria-expanded", "true");
    setTimeout(function () { input.focus(); }, 0);
  }

  function closePanel() {
    if (!panel) return;
    panel.hidden = true;
    launcher.hidden = false;
    launcher.setAttribute("aria-expanded", "false");
    launcher.focus();
  }

  function applyTheme() {
    var attr = document.documentElement.getAttribute("data-theme");
    var dark = attr === "dark" || (attr == null && window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
    wrap.classList.toggle("dark", !!dark);
  }

  function mount() {
    host = document.createElement("div");
    host.id = "ask-olake-root";
    host.style.position = "fixed";
    host.style.zIndex = "2147483000";
    document.body.appendChild(host);
    root = host.attachShadow({ mode: "open" });
    var style = document.createElement("style");
    style.textContent = CSS;
    root.appendChild(style);
    wrap = el("div", "w");
    root.appendChild(wrap);

    launcher = el("button", "launcher");
    launcher.type = "button";
    launcher.setAttribute("aria-label", "Open Ask OLake");
    launcher.setAttribute("aria-expanded", "false");
    launcher.innerHTML = CHAT_ICON; // static markup, no external text
    launcher.appendChild(document.createTextNode("Ask AI"));
    launcher.addEventListener("click", openPanel);
    wrap.appendChild(launcher);

    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && panel && !panel.hidden) closePanel();
    });
    applyTheme();
    new MutationObserver(applyTheme).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    if (window.matchMedia) {
      var mq = window.matchMedia("(prefers-color-scheme: dark)");
      if (mq.addEventListener) mq.addEventListener("change", applyTheme);
      else if (mq.addListener) mq.addListener(applyTheme);
    }
  }

  if (document.body) mount();
  else document.addEventListener("DOMContentLoaded", mount);
})();
