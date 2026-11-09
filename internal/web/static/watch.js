// Web player of the account pages: plays TV channels and radio stations
// (HLS through hls.js, plain audio streams in an <audio> element) and On
// Demand movies and episodes. The portal decides where each stream comes
// from; when a stream from the source fails (a source that does not allow
// other websites), the player tries it once more through the portal.
(function () {
  'use strict';
  var cfgEl = document.getElementById('watch-config');
  if (!cfgEl) return;
  var cfg = JSON.parse(cfgEl.textContent);
  var msg = cfg.msg || {};
  var $ = function (s, el) { return (el || document).querySelector(s); };
  var $$ = function (s, el) { return Array.prototype.slice.call((el || document).querySelectorAll(s)); };
  var fmt = function (s, n) { return String(s).replace('%d', n); };

  var player = $('#player'), video = $('#video'), audio = $('#audio');
  var radioStage = $('#radio-stage'), radioLogo = $('#radio-logo');
  var titleEl = $('#player-title'), nowEl = $('#player-now'), msgEl = $('#player-msg');
  var search = $('#watch-search'), catSelect = $('#watch-cat');
  var hls = null, keepTimer = null, nowTimer = null, playing = null;

  // ---- playback ----

  function play(kind, id, radio, el) {
    stop();
    playing = { kind: kind, id: id };
    player.hidden = false;
    player.classList.toggle('is-radio', !!radio);
    video.hidden = !!radio;
    radioStage.hidden = !radio;
    titleEl.textContent = el ? el.dataset.name || '' : '';
    nowEl.textContent = msg.loading;
    message('');
    $$('.tile.playing, .episodes .playing').forEach(function (t) { t.classList.remove('playing'); });
    if (el) el.classList.add('playing');
    if (radio && el) {
      var img = $('img', el);
      radioLogo.src = img ? img.src : '';
    }
    if (window.matchMedia('(max-width: 900px)').matches || player.getBoundingClientRect().top < 0) {
      player.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
    request(kind, id, false).then(function (info) {
      if (!playing || playing.id !== id) return;
      titleEl.textContent = info.title;
      showNow(info);
      if (info.type === 'unsupported') { message(msg.unsupported); return; }
      if (radio && info.logo) radioLogo.src = info.logo;
      if (!radio && info.logo && kind !== 'live') video.poster = info.logo;
      attach(info, radio ? audio : video, info.url, false);
      keepTimer = setInterval(function () {
        request(kind, id, true).catch(function (err) {
          if (err.message === 'limit') { stop(); player.hidden = false; message(msg.limit); }
        });
      }, (cfg.keepalive || 300) * 1000);
      if (kind === 'live') {
        nowTimer = setInterval(function () {
          fetch('/account/watch/now/' + id, { credentials: 'same-origin' })
            .then(function (r) { return r.ok ? r.json() : null; })
            .then(function (n) { if (n && playing && playing.id === id) showNow(n); })
            .catch(function () {});
        }, 60000);
      }
    }).catch(function (err) {
      nowEl.textContent = '';
      message(msg[err.message] || msg.error);
    });
  }

  function request(kind, id, keepalive) {
    var body = new URLSearchParams({ csrf: cfg.csrf, kind: kind, id: id });
    if (keepalive) body.set('keepalive', '1');
    return fetch('/account/watch/play', { method: 'POST', body: body, credentials: 'same-origin' })
      .then(function (res) {
        return res.json().catch(function () { return {}; }).then(function (data) {
          if (!res.ok) throw new Error(data.error || 'error');
          return data;
        });
      });
  }

  function attach(info, media, url, isFallback) {
    var failed = function () {
      if (!playing) return;
      if (!isFallback && info.fallback) { attach(info, media, info.fallback, true); return; }
      message(msg.failed);
    };
    if (hls) { hls.destroy(); hls = null; }
    media.onerror = null;
    var native = media.canPlayType('application/vnd.apple.mpegurl');
    if (info.type === 'hls' && window.Hls && window.Hls.isSupported()) {
      var recovered = false;
      hls = new window.Hls({ enableWorker: true, backBufferLength: 60 });
      hls.on(window.Hls.Events.ERROR, function (e, d) {
        if (!d.fatal) return;
        if (d.type === window.Hls.ErrorTypes.MEDIA_ERROR && !recovered) { recovered = true; hls.recoverMediaError(); return; }
        hls.destroy(); hls = null;
        failed();
      });
      hls.on(window.Hls.Events.MANIFEST_PARSED, function () { media.play().catch(function () {}); });
      hls.loadSource(url);
      hls.attachMedia(media);
      return;
    }
    if (info.type === 'hls' && !native) { message(msg.unsupported); return; }
    media.onerror = function () { media.onerror = null; failed(); };
    media.src = url;
    media.play().catch(function () {});
  }

  function stop() {
    clearInterval(keepTimer); clearInterval(nowTimer);
    keepTimer = nowTimer = null;
    playing = null;
    if (hls) { hls.destroy(); hls = null; }
    [video, audio].forEach(function (m) {
      m.onerror = null;
      m.pause();
      m.removeAttribute('src');
      m.removeAttribute('poster');
      m.load();
    });
  }

  function showNow(info) {
    var parts = [];
    if (info.now) parts.push(msg.now + ': ' + info.now);
    if (info.next) parts.push(msg.next + ': ' + info.next);
    nowEl.textContent = parts.join(' · ');
  }

  function message(text) {
    msgEl.textContent = text;
    msgEl.hidden = !text;
    player.classList.toggle('has-msg', !!text);
  }

  $('#player-close').addEventListener('click', function () {
    stop();
    player.hidden = true;
    $$('.playing').forEach(function (t) { t.classList.remove('playing'); });
  });

  // ---- tabs and filters ----

  var tabs = $$('.watch-tabs [data-tab]');
  var current = null;
  var libraries = {};

  function showTab(name) {
    var btn = tabs.filter(function (t) { return t.dataset.tab === name; })[0] || tabs[0];
    if (!btn) return;
    current = btn.dataset.tab;
    tabs.forEach(function (t) { t.setAttribute('aria-selected', t === btn ? 'true' : 'false'); });
    $$('.watch-panel').forEach(function (p) { p.hidden = p.dataset.panel !== current; });
    try { history.replaceState(null, '', '#' + current); } catch (e) {}
    search.value = '';
    if (current === 'tv' || current === 'radio') {
      fillCategories((cfg.cats[current] || []).map(function (c) { return { id: c, name: c }; }));
      filterTiles();
    } else {
      fillCategories([]);
      loadLibrary(current);
    }
  }

  function fillCategories(list) {
    catSelect.innerHTML = '';
    var all = document.createElement('option');
    all.value = ''; all.textContent = msg.all;
    catSelect.appendChild(all);
    list.forEach(function (c) {
      var o = document.createElement('option');
      o.value = String(c.id); o.textContent = c.name;
      catSelect.appendChild(o);
    });
    catSelect.hidden = list.length < 2;
  }

  function panel() { return $('.watch-panel[data-panel="' + current + '"]'); }

  function matches(name) {
    var q = search.value.trim().toLowerCase();
    return !q || name.toLowerCase().indexOf(q) >= 0;
  }

  function filterTiles() {
    var p = panel(), cat = catSelect.value, shown = 0;
    $$('.tile', p).forEach(function (t) {
      var ok = matches(t.dataset.name) && (!cat || t.dataset.cat === cat);
      t.hidden = !ok;
      if (ok) shown++;
    });
    $('.empty', p).hidden = shown > 0;
  }

  function refresh() {
    if (current === 'tv' || current === 'radio') filterTiles();
    else renderLibrary(current, true);
  }
  search.addEventListener('input', refresh);
  catSelect.addEventListener('change', refresh);
  tabs.forEach(function (t) { t.addEventListener('click', function () { showTab(t.dataset.tab); }); });

  $$('.tile').forEach(function (t) {
    t.addEventListener('click', function () { play('live', t.dataset.id, t.dataset.radio === '1', t); });
    var img = $('img', t);
    if (img) img.addEventListener('error', function () { img.remove(); });
  });

  // ---- On Demand ----

  var pageSize = 120;

  function loadLibrary(type) {
    var p = panel();
    if (libraries[type]) {
      fillCategories(libraries[type].categories);
      renderLibrary(type, true);
      return;
    }
    $('.poster-grid', p).innerHTML = '<p class="muted">' + escapeHTML(msg.loading) + '</p>';
    fetch('/account/watch/library?type=' + type, { credentials: 'same-origin' })
      .then(function (r) { if (!r.ok) throw new Error(); return r.json(); })
      .then(function (lib) {
        libraries[type] = { categories: lib.categories || [], items: lib.items || [], shown: pageSize };
        if (current === type) { fillCategories(libraries[type].categories); renderLibrary(type, true); }
      })
      .catch(function () { $('.poster-grid', p).innerHTML = '<p class="muted">' + escapeHTML(msg.error) + '</p>'; });
  }

  function renderLibrary(type, reset) {
    var lib = libraries[type];
    if (!lib) return;
    var p = $('.watch-panel[data-panel="' + type + '"]');
    var detail = $('.series-detail', p);
    if (detail) detail.hidden = true;
    if (reset) lib.shown = pageSize;
    var cat = catSelect.value ? Number(catSelect.value) : 0;
    var list = lib.items.filter(function (it) {
      return matches(it.title) && (!cat || (it.cats || []).indexOf(cat) >= 0);
    });
    var grid = $('.poster-grid', p);
    grid.hidden = false;
    grid.innerHTML = '';
    list.slice(0, lib.shown).forEach(function (it) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'poster';
      b.dataset.name = it.title;
      var art = document.createElement('span');
      art.className = 'poster-art';
      if (it.poster) {
        var img = document.createElement('img');
        img.loading = 'lazy'; img.alt = ''; img.src = it.poster;
        img.addEventListener('error', function () { img.remove(); });
        art.appendChild(img);
      }
      var initial = document.createElement('b');
      initial.textContent = it.title;
      art.appendChild(initial);
      var name = document.createElement('span');
      name.className = 'poster-title';
      name.textContent = it.title;
      var meta = document.createElement('span');
      meta.className = 'poster-meta';
      meta.textContent = [it.year || '', it.minutes ? fmt(msg.minutes, it.minutes) : ''].filter(Boolean).join(' · ');
      b.appendChild(art); b.appendChild(name); b.appendChild(meta);
      b.addEventListener('click', function () {
        if (type === 'movies') play('movie', it.id, false, b);
        else openSeries(it.id);
      });
      grid.appendChild(b);
    });
    $('.empty', p).hidden = list.length > 0;
    if (!lib.items.length) $('.empty', p).textContent = msg.nolibrary;
    var more = $('.more', p);
    more.hidden = list.length <= lib.shown;
  }

  $$('.watch-panel .more button').forEach(function (b) {
    b.addEventListener('click', function () {
      var lib = libraries[current];
      if (!lib) return;
      lib.shown += pageSize;
      renderLibrary(current, false);
    });
  });

  function openSeries(id) {
    var p = $('.watch-panel[data-panel="series"]');
    var detail = $('.series-detail', p);
    fetch('/account/watch/series/' + id, { credentials: 'same-origin' })
      .then(function (r) { if (!r.ok) throw new Error(); return r.json(); })
      .then(function (s) {
        $('.poster-grid', p).hidden = true;
        $('.more', p).hidden = true;
        $('.empty', p).hidden = true;
        detail.hidden = false;
        $('.series-title', detail).textContent = s.show.title;
        $('.series-plot', detail).textContent = s.plot || '';
        var poster = $('.series-poster', detail);
        poster.hidden = !s.show.poster;
        if (s.show.poster) poster.src = s.show.poster;
        var sel = $('.season-select', detail);
        sel.innerHTML = '';
        (s.seasons || []).forEach(function (season, i) {
          var o = document.createElement('option');
          o.value = String(i);
          o.textContent = season.number === 0 ? msg.specials : fmt(msg.season, season.number);
          sel.appendChild(o);
        });
        var show = function () { renderEpisodes(s, s.seasons[Number(sel.value)] || { episodes: [] }); };
        sel.onchange = show;
        sel.parentNode.hidden = (s.seasons || []).length < 2;
        show();
        detail.scrollIntoView({ behavior: 'smooth', block: 'start' });
      })
      .catch(function () { message(msg.error); });
  }

  function renderEpisodes(s, season) {
    var list = $('.series-detail .episodes');
    list.innerHTML = '';
    season.episodes.forEach(function (ep) {
      var li = document.createElement('li');
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'episode';
      var label = (ep.number ? fmt(msg.episode, ep.number) + ' · ' : '') + ep.title;
      b.dataset.name = s.show.title + ' · ' + label;
      var t = document.createElement('strong');
      t.textContent = label;
      var meta = document.createElement('span');
      meta.className = 'muted';
      meta.textContent = ep.minutes ? fmt(msg.minutes, ep.minutes) : '';
      b.appendChild(t); b.appendChild(meta);
      if (ep.plot) {
        var plot = document.createElement('small');
        plot.textContent = ep.plot;
        b.appendChild(plot);
      }
      b.addEventListener('click', function () { play('episode', ep.id, false, b); });
      li.appendChild(b);
      list.appendChild(li);
    });
  }

  $$('.series-detail .back').forEach(function (b) {
    b.addEventListener('click', function () { renderLibrary('series', false); });
  });

  function escapeHTML(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  showTab((location.hash || '').slice(1));
})();
