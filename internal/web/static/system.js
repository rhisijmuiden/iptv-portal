// System resources page: fills in the measurements and draws the charts,
// then reloads them every few seconds. Text comes translated from the page.
(function () {
  'use strict';
  var report = JSON.parse(document.getElementById('system-report').textContent);
  var text = JSON.parse(document.getElementById('system-text').textContent);
  var lang = document.documentElement.lang || 'en';
  var SVG = 'http://www.w3.org/2000/svg';

  // ---- formatting ----
  function num(v, digits) {
    return v.toLocaleString(lang, { minimumFractionDigits: digits || 0, maximumFractionDigits: digits || 0 });
  }
  function fmt(s) { // fmt("%s of %d", a, b)
    var args = Array.prototype.slice.call(arguments, 1);
    return s.replace(/%[sd]/g, function () { return args.length ? args.shift() : ''; });
  }
  function pct(v) { return v < 0 ? '–' : num(v, v > 0 && v < 10 ? 1 : 0) + '%'; }
  function size(n) {
    if (n < 1e3) return num(n) + ' B';
    if (n < 1e6) return num(n / 1e3, 1) + ' kB';
    if (n < 1e9) return num(n / 1e6, 1) + ' MB';
    if (n < 1e12) return num(n / 1e9, 1) + ' GB';
    return num(n / 1e12, 2) + ' TB';
  }
  function bits(bytesPerSec) {
    var b = bytesPerSec * 8;
    if (b < 1e3) return num(b) + ' bit/s';
    if (b < 1e6) return num(b / 1e3, b < 1e4 ? 1 : 0) + ' kbit/s';
    if (b < 1e9) return num(b / 1e6, b < 1e7 ? 1 : 0) + ' Mbit/s';
    return num(b / 1e9, 1) + ' Gbit/s';
  }
  function dur(ns) {
    var m = Math.floor(ns / 6e10);
    if (m >= 1440) return fmt(text.days, Math.floor(m / 1440), Math.floor(m % 1440 / 60));
    if (m >= 60) return fmt(text.hours, Math.floor(m / 60), m % 60);
    return fmt(text.mins, m);
  }
  function clock(t) {
    return t.toLocaleTimeString(lang, { hour: '2-digit', minute: '2-digit' });
  }

  // ---- measurements ----
  function set(k, v) {
    document.querySelectorAll('[data-k="' + k + '"]').forEach(function (el) { el.textContent = v; });
  }
  function meter(k, p) {
    var el = document.querySelector('[data-meter="' + k + '"]');
    if (!el) return;
    el.style.width = Math.max(0, Math.min(100, p)) + '%';
    el.className = p >= 90 ? 'high' : p >= 75 ? 'raised' : '';
  }

  function fill(r) {
    var n = r.now, s = r.system, d = r.disk, p = r.process;
    var cores = num(s.cpus, s.cpus % 1 ? 1 : 0);
    set('cpu', pct(n.cpu));
    set('cpuSub', fmt(text.coresPortal, cores, pct(n.proc)));
    meter('cpu', n.cpu);
    var memP = n.memTotal ? n.mem * 100 / n.memTotal : 0;
    set('mem', pct(memP));
    set('memSub', fmt(text.of, size(n.memTotal)));
    meter('mem', memP);
    var diskP = d.used + d.free ? d.used * 100 / (d.used + d.free) : 0;
    set('disk', d.total ? pct(diskP) : '–');
    set('diskSub', fmt(text.free, size(d.free)));
    meter('disk', diskP);
    set('load', num(n.load, 2));
    set('loadSub', fmt(text.perCore, num(s.cpus ? n.load / s.cpus : 0, 2)));
    set('net', '↓ ' + bits(n.rx));
    set('netSub', fmt(text.netSub, bits(n.tx)));
    set('online', num(n.online));
    set('onlineSub', (n.watching ? fmt(text.watching, n.watching) : text.onlineNow) +
      (n.proxied ? ' · ' + fmt(text.vod, n.proxied) : ''));

    set('hostname', s.hostname);
    set('os', s.os);
    set('kernel', s.kernel);
    set('container', s.container ? fmt(text.container, s.container) : text.host);
    set('cpus', s.cpus === Math.round(s.cpus) ? cores : cores + ' (' + text.limit + ')');
    set('memFull', size(n.mem) + ' / ' + size(n.memTotal) + (s.memLimited ? ' (' + text.limit + ')' : ''));
    set('swap', s.swapTotal ? size(s.swapUsed) + ' / ' + size(s.swapTotal) : text.none);
    set('loads', num(n.load, 2) + ' · ' + num(s.load5, 2) + ' · ' + num(s.load15, 2));
    set('uptime', dur(s.uptime));

    set('version', r.version);
    set('pid', p.pid);
    set('procUptime', dur(p.uptime));
    set('procCPU', pct(n.proc));
    set('rss', size(p.rss));
    set('heap', size(p.heap) + ' / ' + size(p.goSys));
    set('goroutines', num(p.goroutines));
    set('threads', num(p.threads));
    set('openFiles', num(p.openFiles));
    set('gc', num(p.gcRuns));

    set('path', d.path);
    set('diskTotal', size(d.total));
    set('diskUsed', size(d.used) + ' (' + pct(diskP) + ')');
    set('diskFree', size(d.free));
    set('dataDir', size(d.dataDir));
    set('db', size(d.db));

    set('onlineNow', num(n.online));
    set('watching', num(n.watching));
    set('proxied', num(n.proxied));
    set('proxyRate', bits(n.proxyBps));
    set('proxyTotal', size(r.proxyTotal));
    var body = document.querySelector('[data-k="ifaces"]');
    body.textContent = '';
    (r.network.interfaces || []).forEach(function (i) {
      var tr = document.createElement('tr');
      [i.name, size(i.rx), size(i.tx)].forEach(function (v) {
        var td = document.createElement('td');
        td.textContent = v;
        tr.appendChild(td);
      });
      body.appendChild(tr);
    });
  }

  // ---- charts ----
  // Each chart has one y axis; series are drawn in a fixed order so a
  // colour always means the same thing.
  var charts = {
    cpu: { max: 100, fmt: pct, series: [
      { key: 'system', get: function (s) { return s.cpu; } },
      { key: 'portal', get: function (s) { return s.proc; } }] },
    mem: { max: 100, fmt: pct, series: [
      { key: 'used', get: function (s) { return s.memTotal ? s.mem * 100 / s.memTotal : -1; } }] },
    net: { fmt: bits, series: [
      { key: 'down', get: function (s) { return s.rx; } },
      { key: 'up', get: function (s) { return s.tx; } }] },
    viewers: { fmt: function (v) { return num(v); }, whole: true, series: [
      { key: 'online', get: function (s) { return s.online; } },
      { key: 'watching', get: function (s) { return s.watching; } },
      { key: 'proxied', get: function (s) { return s.proxied; } }] }
  };

  function el(name, attrs, parent) {
    var e = document.createElementNS(SVG, name);
    for (var k in attrs) e.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(e);
    return e;
  }

  // niceMax rounds the highest value up to 1, 2 or 5 times a power of ten.
  function niceMax(v, whole) {
    if (v <= 0) return whole ? 4 : 1;
    var p = Math.pow(10, Math.floor(Math.log10(v)));
    var m = [1, 2, 2.5, 5, 10].map(function (f) { return f * p; }).find(function (x) { return x >= v; });
    return whole ? Math.max(4, Math.ceil(m / 4) * 4) : m;
  }

  function draw(box, cfg, history, range, gap) {
    var W = Math.max(box.clientWidth, 260), H = 190;
    var L = 64, R = 10, T = 8, B = 24;
    var end = Date.now(), span = { day: 86400, hour: 3600 }[range] * 1000 || 900e3, start = end - span;
    var pts = history.map(function (s) { return { t: new Date(s.t).getTime(), s: s }; })
      .filter(function (p) { return p.t >= start; });
    var top = cfg.max;
    if (!top) {
      var hi = 0;
      pts.forEach(function (p) { cfg.series.forEach(function (se) { hi = Math.max(hi, se.get(p.s)); }); });
      top = niceMax(hi, cfg.whole);
    }
    var x = function (t) { return L + (t - start) / span * (W - L - R); };
    var y = function (v) { return T + (1 - v / top) * (H - T - B); };

    box.textContent = '';
    if (cfg.series.length > 1) {
      var legend = document.createElement('div');
      legend.className = 'legend';
      cfg.series.forEach(function (se, i) {
        var item = document.createElement('span');
        var key = document.createElement('i');
        key.className = 's' + (i + 1);
        item.appendChild(key);
        item.appendChild(document.createTextNode(text.series[se.key]));
        legend.appendChild(item);
      });
      box.appendChild(legend);
    }
    var svg = el('svg', { viewBox: '0 0 ' + W + ' ' + H, width: W, height: H });
    box.appendChild(svg);
    for (var g = 0; g <= 4; g++) {
      var v = top * g / 4, gy = y(v);
      el('line', { x1: L, x2: W - R, y1: gy, y2: gy, class: g ? 'grid' : 'base' }, svg);
      el('text', { x: L - 8, y: gy + 4, 'text-anchor': 'end', class: 'tick' }, svg).textContent = cfg.fmt(v);
    }
    // Time labels on round minutes or hours, as many as fit.
    var room = Math.max(1, Math.floor((W - L - R) / 80));
    var step = [1, 2, 5, 10, 15, 30, 60, 120, 180, 240, 360, 720].map(function (m) { return m * 60e3; })
      .find(function (st) { return span / st <= room; }) || span;
    var offset = new Date().getTimezoneOffset() * 60e3;
    for (var tt = Math.ceil((start - offset) / step) * step + offset; tt <= end; tt += step) {
      var tx = x(tt);
      if (tx < L + 24 || tx > W - R - 24) continue;
      el('text', { x: tx, y: H - 6, 'text-anchor': 'middle', class: 'tick' }, svg).textContent = clock(new Date(tt));
    }
    if (!pts.length) {
      el('text', { x: (L + W - R) / 2, y: H / 2, 'text-anchor': 'middle', class: 'tick' }, svg).textContent = text.waiting;
      return;
    }
    cfg.series.forEach(function (se, i) {
      // A gap in the samples (a restart) breaks the line.
      var d = '', prev = null;
      pts.forEach(function (p) {
        var v = se.get(p.s);
        if (v < 0) { prev = null; return; }
        d += (prev === null || p.t - prev > gap ? 'M' : 'L') + x(p.t).toFixed(1) + ' ' + y(Math.min(v, top)).toFixed(1);
        prev = p.t;
      });
      el('path', { d: d, class: 'line s' + (i + 1) }, svg);
    });

    // Hover: a crosshair on the nearest sample and every series' value.
    var cross = el('line', { y1: T, y2: H - B, class: 'cross', visibility: 'hidden' }, svg);
    var dots = cfg.series.map(function (se, i) { return el('circle', { r: 4, class: 'dot s' + (i + 1), visibility: 'hidden' }, svg); });
    var tip = document.createElement('div');
    tip.className = 'chart-tip';
    tip.hidden = true;
    box.appendChild(tip);
    var hit = el('rect', { x: L, y: T, width: W - L - R, height: H - T - B, fill: 'transparent', tabindex: 0 }, svg);
    function show(px) {
      var t = start + (px - L) / (W - L - R) * span, best = pts[0];
      pts.forEach(function (p) { if (Math.abs(p.t - t) < Math.abs(best.t - t)) best = p; });
      if (Math.abs(best.t - t) > Math.max(gap, span / 60)) { hide(); return; } // no measurement here
      var bx = x(best.t);
      cross.setAttribute('x1', bx); cross.setAttribute('x2', bx); cross.setAttribute('visibility', 'visible');
      tip.textContent = '';
      var head = document.createElement('div');
      head.className = 'tip-time';
      head.textContent = clock(new Date(best.t));
      tip.appendChild(head);
      cfg.series.forEach(function (se, i) {
        var v = se.get(best.s);
        if (v >= 0) {
          dots[i].setAttribute('cx', bx); dots[i].setAttribute('cy', y(Math.min(v, top)));
          dots[i].setAttribute('visibility', 'visible');
        }
        var row = document.createElement('div');
        var key = document.createElement('i'); key.className = 's' + (i + 1);
        var val = document.createElement('strong'); val.textContent = v < 0 ? '–' : cfg.fmt(v);
        row.appendChild(key); row.appendChild(val); row.appendChild(document.createTextNode(' ' + text.series[se.key]));
        tip.appendChild(row);
      });
      tip.hidden = false;
      var left = bx / W * box.clientWidth;
      tip.style.left = (left > box.clientWidth / 2 ? left - tip.offsetWidth - 12 : left + 12) + 'px';
    }
    function hide() {
      cross.setAttribute('visibility', 'hidden');
      dots.forEach(function (d) { d.setAttribute('visibility', 'hidden'); });
      tip.hidden = true;
    }
    hit.addEventListener('pointermove', function (e) {
      var r = svg.getBoundingClientRect();
      show((e.clientX - r.left) * W / r.width);
    });
    hit.addEventListener('pointerleave', hide);
    hit.addEventListener('focus', function () { show(x(pts[pts.length - 1].t)); });
    hit.addEventListener('blur', hide);
  }

  function render(r) {
    fill(r);
    var gap = r.range === 'day' ? 3 * 60e3 : 3 * r.interval * 1000;
    Object.keys(charts).forEach(function (k) {
      var box = document.querySelector('[data-chart="' + k + '"]');
      if (box && !box.matches(':hover')) draw(box, charts[k], r.history, r.range, gap);
    });
  }

  var live = document.querySelector('[data-live]');
  function refresh() {
    if (document.hidden) return;
    fetch(text.url, { credentials: 'same-origin', headers: { Accept: 'application/json' } })
      .then(function (res) {
        if (!res.ok || res.redirected) throw new Error(res.status);
        return res.json();
      })
      .then(function (r) {
        report = r;
        render(r);
        live.textContent = text.live;
        live.classList.remove('paused');
      })
      .catch(function () {
        live.textContent = text.offline;
        live.classList.add('paused');
      });
  }

  render(report);
  setInterval(refresh, (report.interval || 5) * 1000);
  document.addEventListener('visibilitychange', refresh);
  var resized;
  window.addEventListener('resize', function () {
    clearTimeout(resized);
    resized = setTimeout(function () { render(report); }, 150);
  });
})();
