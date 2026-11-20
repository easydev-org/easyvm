// Package console serves the ONE PAGE that shows every operation a run performed and what came of it.
//
// # WHAT IT IS FOR
//
// MEASURED, over several sessions of a failed walkthrough: the answers to "what did it actually do"
// and "why did it stop" were spread across three places - a text log on stdout, numbered frame files
// whose names were not tied to a step, and a report that had to be generated after the fact. None of
// them answered the question people actually ask while a stage is running or just after it fails:
//
//	WHAT HAPPENED, IN ORDER, AND WHAT DID THAT ONE THING SEE?
//
// So this page is a TIMELINE: every external call and every step, newest last, with the pictures the
// deciding steps used. It is served live, so a run in progress is watchable, and it is the same data
// `easyvm report` writes to a file.
//
// # WHY IT IS ONE FILE AND NO FRAMEWORK
//
// A page that needs a build step to serve is a page that is wrong the moment its assets and its
// server disagree. This is one HTML document with its script inline and no dependencies: the daemon
// writes it, the browser runs it, and there is nothing to keep in step. It talks to four endpoints,
// all of which already exist for other callers:
//
//	GET /api/journal/stream      the live feed (SSE)
//	GET /api/journal/step?seq=N  one step with its pictures and its calls
//	GET /api/artifact?h=<sha>    a library picture
//	GET /screen.png              the machine's screen, for the panel on the right
//
// # AND A CLICK ON THE SCREEN IS A CLICK IN THE GUEST
//
// That is the one thing this page does that changes state: a pointer press on the live screen posts
// to `/api/click`, which reaches the same driver method a walkthrough's `click:` reaches - and is
// therefore recorded, with origin `api`, beside everything else. A page that could drive the machine
// without appearing in the record of the machine would be the worst of both.
package console

// Page is the whole front end: one document, no assets, no build.
//
// IT IS A GO STRING RATHER THAN AN HTML FILE because the daemon embeds it: a file on disk is a file
// that can be missing from an image, and this page must be there whenever the daemon answers.
const Page = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>easyvm console</title>
<style>
:root{
 --fg:#e8eaed; --dim:#9aa0a6; --bg:#0f1114; --panel:#171a1f; --line:#282d34; --soft:#1d2127;
 --ok:#4ade80; --bad:#f87171; --warn:#fbbf24; --walk:#60a5fa; --api:#c084fc; --sys:#94a3b8;
}
*{box-sizing:border-box}
html,body{height:100%;margin:0}
body{background:var(--bg);color:var(--fg);font:13px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;
 display:grid;grid-template-columns:minmax(0,1fr) 420px;grid-template-rows:auto minmax(0,1fr);gap:0}
header{grid-column:1/-1;display:flex;align-items:center;gap:14px;padding:9px 16px;border-bottom:1px solid var(--line);background:var(--panel)}
header h1{font-size:13px;font-weight:600;margin:0;letter-spacing:.02em}
.pill{border:1px solid var(--line);border-radius:999px;padding:2px 10px;font-size:11px;color:var(--dim)}
.pill b{color:var(--fg);font-variant-numeric:tabular-nums}
.spacer{flex:1}
#status{font-variant-numeric:tabular-nums}
main{display:contents}
#timeline{overflow-y:auto;padding:10px 14px 40px}
#side{border-left:1px solid var(--line);background:var(--panel);overflow-y:auto;display:flex;flex-direction:column}
#screenwrap{padding:10px;border-bottom:1px solid var(--line)}
#screen{width:100%;border:1px solid var(--line);border-radius:6px;background:#000;cursor:crosshair;display:block}
#detail{padding:12px 14px;flex:1;overflow-y:auto}
.ev{display:grid;grid-template-columns:74px 16px 92px minmax(0,1fr) auto;gap:8px;align-items:baseline;
 padding:3px 6px;border-radius:4px;cursor:pointer;font-size:12px}
.ev:hover{background:var(--soft)}
.ev.sel{background:var(--soft);outline:1px solid var(--line)}
.ev .t{color:var(--dim);font-variant-numeric:tabular-nums;font-size:11px}
.ev .dot{width:8px;height:8px;border-radius:50%;justify-self:center}
.ev .k{color:var(--dim);font-size:11px;text-transform:uppercase;letter-spacing:.04em}
.ev .d{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ev .ms{color:var(--dim);font-variant-numeric:tabular-nums;font-size:11px}
.ev.bad .d{color:var(--bad)}
.o-walk .dot{background:var(--walk)} .o-api .dot{background:var(--api)} .o-system .dot{background:var(--sys)}
.step{border:1px solid var(--line);border-radius:6px;margin:14px 0 6px;background:var(--panel);overflow:hidden}
.step>summary{display:flex;align-items:center;gap:10px;padding:7px 11px;cursor:pointer;list-style:none;background:var(--soft)}
.step>summary::-webkit-details-marker{display:none}
.step .n{color:var(--dim);font-variant-numeric:tabular-nums;min-width:2.4em;font-size:11px}
.step .nm{font-weight:600;flex:1;overflow-wrap:anywhere}
.step .du{color:var(--dim);font-variant-numeric:tabular-nums;font-size:11px}
.tag{font-size:10px;font-weight:700;letter-spacing:.05em;text-transform:uppercase;border-radius:3px;padding:2px 6px}
.t-passed{color:var(--ok);background:rgba(74,222,128,.12)}
.t-failed{color:var(--bad);background:rgba(248,113,113,.14)}
.t-optional{color:var(--warn);background:rgba(251,191,36,.14)}
.t-skipped,.t-running{color:var(--dim);background:rgba(154,160,166,.12)}
.stepbody{padding:2px 11px 10px}
.msg{margin:9px 0;padding:8px 10px;border-left:3px solid var(--bad);background:rgba(248,113,113,.07);
 border-radius:0 5px 5px 0;white-space:pre-wrap;overflow-wrap:anywhere;font-family:ui-monospace,Menlo,monospace;font-size:11.5px}
.verdict{margin:9px 0;font-size:12px;color:var(--dim)}
.verdict b{color:var(--fg)}
.verdict code{background:var(--soft);border:1px solid var(--line);border-radius:3px;padding:0 4px;color:var(--fg)}
.figs{display:flex;flex-wrap:wrap;gap:9px;margin:10px 0}
figure{margin:0}
figure img{display:block;max-width:260px;height:auto;border:1px solid var(--line);border-radius:5px;background:#000}
figcaption{color:var(--dim);font-size:11px;margin-top:3px}
.words{display:flex;flex-wrap:wrap;gap:4px;margin:8px 0}
.w{font-family:ui-monospace,Menlo,monospace;font-size:11px;background:var(--soft);border:1px solid var(--line);border-radius:3px;padding:0 5px}
.w.miss{border-color:var(--bad);color:var(--bad);text-decoration:line-through}
table{border-collapse:collapse;width:100%;margin:8px 0;font-size:11.5px}
th,td{text-align:left;padding:3px 8px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--dim);font-weight:600;font-size:10px;text-transform:uppercase;letter-spacing:.05em}
td.mono{font-family:ui-monospace,Menlo,monospace;overflow-wrap:anywhere}
td.ok{color:var(--ok)} td.bad{color:var(--bad)}
h3{font-size:12px;margin:0 0 8px;color:var(--dim);text-transform:uppercase;letter-spacing:.05em}
.empty{color:var(--dim);padding:22px 6px;text-align:center}
#detail img{max-width:100%;border:1px solid var(--line);border-radius:5px;margin:6px 0}
#live{display:flex;gap:8px;align-items:center}
button{background:var(--soft);color:var(--fg);border:1px solid var(--line);border-radius:5px;
 padding:3px 9px;font-size:11px;cursor:pointer;font-family:inherit}
button:hover{border-color:var(--dim)}
</style></head>
<body>
<header>
  <h1>easyvm console</h1>
  <span class="pill">run <b id="run">-</b></span>
  <span class="pill">step <b id="step">-</b></span>
  <span class="pill">calls <b id="calls">0</b></span>
  <span class="spacer"></span>
  <span id="live" class="pill"><span id="dot" style="width:7px;height:7px;border-radius:50%;background:var(--dim);display:inline-block"></span><span id="status">connecting</span></span>
</header>
<main>
  <div id="timeline"><div class="empty">waiting for the first call…</div></div>
  <aside id="side">
    <div id="screenwrap">
      <img id="screen" alt="the machine's screen">
    </div>
    <div id="detail"><h3>detail</h3><div class="empty">click a call, or a spot on the screen</div></div>
  </aside>
</main>
<script>
(function(){
  "use strict";

  var runEl = document.getElementById('run');
  var stepEl = document.getElementById('step');
  var callsEl = document.getElementById('calls');
  var statusEl = document.getElementById('status');
  var dotEl = document.getElementById('dot');
  var tl = document.getElementById('timeline');
  var detail = document.getElementById('detail');
  var screen = document.getElementById('screen');

  var run = null;          // the run this page is following
  var lastCall = 0;        // the newest call sequence seen
  var callCount = 0;
  var steps = {};          // step_index -> {el, details, name, ...}
  var selected = null;     // the element describing the selected call

  function esc(s){
    return String(s == null ? '' : s).replace(/[&<>"']/g, function(c){
      return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c];
    });
  }
  function timeOf(t){ return t && t.length >= 23 ? t.substring(11, 23) : (t || ''); }
  function dur(ms){
    if(!ms) return '';
    if(ms < 1000) return ms + 'ms';
    if(ms < 60000) return (ms/1000).toFixed(1) + 's';
    return Math.floor(ms/60000) + 'm' + String(Math.floor(ms/1000)%60).padStart(2,'0') + 's';
  }

  // A STEP IS A HEADING IN THE TIMELINE, and its calls go inside it. The page groups by step_index
  // because that is what the journal records; a call that arrived before its step's record is
  // created under a placeholder and filled in when the record lands.
  function stepBox(index, name){
    var key = String(index);
    if(steps[key]) return steps[key];
    var wrap = document.createElement('details');
    wrap.className = 'step';
    wrap.open = true;
    wrap.innerHTML =
      '<summary><span class="n">' + esc(index) + '</span>' +
      '<span class="nm">' + esc(name || 'step ' + index) + '</span>' +
      '<span class="du"></span><span class="tag t-running">running</span></summary>' +
      '<div class="stepbody"></div>';
    if(tl.querySelector('.empty')) tl.innerHTML = '';
    tl.appendChild(wrap);
    var obj = {el: wrap, body: wrap.querySelector('.stepbody'), name: name || ''};
    steps[key] = obj;
    return obj;
  }

  function applyStep(rec){
    var box = stepBox(rec.step_index, rec.step);
    box.name = rec.step;
    box.el.querySelector('.nm').textContent = rec.step;
    var tag = box.el.querySelector('.tag');
    tag.className = 'tag t-' + (rec.status || 'running');
    tag.textContent = rec.status || 'running';
    box.el.querySelector('.du').textContent = dur(rec.duration_ms);
    // THE STEP'S OWN VERDICT, rendered once its record lands: a check's numbers, its pictures and
    // the words it read are the reason a person opens this page at all.
    var old = box.body.querySelector('.summary');
    if(old) old.remove();
    if(rec.mode || rec.got != null || rec.ok != null || rec.message){
      var s = document.createElement('div');
      s.className = 'summary';
      var h = '';
      if(rec.message) h += '<div class="msg">' + esc(rec.message) + '</div>';
      if(rec.mode || rec.got != null || rec.ok != null){
        h += '<div class="verdict">';
        if(rec.mode) h += 'mode <code>' + esc(rec.mode) + '</code> ';
        if(rec.got != null) h += 'read <code>' + Number(rec.got).toFixed(4) + '</code> ';
        if(rec.want) h += 'wanted <code>' + esc(rec.want) + '</code> ';
        if(rec.ok === true) h += '<b style="color:var(--ok)">matched</b>';
        else if(rec.ok === false) h += '<b style="color:var(--bad)">did not match</b>';
        h += '</div>';
      }
      if(rec.words && rec.words.length){
        var miss = {};
        (rec.missing||[]).forEach(function(m){ miss[String(m).toLowerCase()] = 1; });
        h += '<div class="words">' + rec.words.map(function(w){
          var c = miss[String(w).toLowerCase()] ? 'w miss' : 'w';
          return '<span class="' + c + '">' + esc(w) + '</span>';
        }).join('') + '</div>';
      }
      if(rec.images && rec.images.length){
        h += '<div class="figs">' + rec.images.map(function(im){
          var src = im.sha ? ('/api/artifact?h=' + im.sha) : (im.rel ? ('/api/artifact?p=' + im.rel) : '');
          if(!src) return '';
          var cap = esc(im.label || '');
          if(im.rect && im.rect.length === 4) cap += ' · rect ' + im.rect.join(',');
          return '<figure><img loading="lazy" src="' + src + '" alt="' + esc(im.label||'') + '">' +
                 '<figcaption>' + cap + '</figcaption></figure>';
        }).join('') + '</div>';
      }
      s.innerHTML = h;
      box.body.insertBefore(s, box.body.firstChild);
    }
    // AND ITS CALLS, read from the same endpoint the detail pane uses so the grouping is not a second
    // interpretation of the stream.
    fetch('/api/journal/step?seq=' + rec.seq)
      .then(function(r){ return r.json(); })
      .then(function(d){
        if(!d.ok || !d.events) return;
        d.events.forEach(function(e){ addCall(e, box); });
      })
      .catch(function(){});
  }

  function addCall(e, box){
    var origin = e.origin || 'walk';
    var ok = e.ok;
    var row = document.createElement('div');
    row.className = 'ev o-' + origin + (ok === false ? ' bad' : '');
    row.dataset.seq = e.seq;
    row.innerHTML =
      '<span class="t">' + esc(timeOf(e.time)) + '</span>' +
      '<span class="dot"></span>' +
      '<span class="k">' + esc(e.kind) + '</span>' +
      '<span class="d">' + esc(e.detail || e.error || '') + '</span>' +
      '<span class="ms">' + (e.duration_ms ? dur(e.duration_ms) : '') + '</span>';
    row.title = origin + (e.error ? ' — ' + e.error : '');
    row.addEventListener('click', function(){
      if(selected) selected.classList.remove('sel');
      selected = row;
      row.classList.add('sel');
      showCall(e);
    });
    // THE CALL BELONGS UNDER ITS STEP, which may not have arrived yet.
    var target = box || stepBox(e.step_index, e.step);
    target.body.appendChild(row);
    callCount++;
    callsEl.textContent = callCount;
  }

  function showCall(e){
    var h = '<h3>' + esc(e.kind) + ' · ' + esc(e.origin || 'walk') + '</h3>';
    h += '<div class="verdict">step <code>' + esc(e.step) + '</code> · ' + esc(timeOf(e.time)) +
         (e.duration_ms ? ' · <b>' + dur(e.duration_ms) + '</b>' : '') + '</div>';
    if(e.detail) h += '<div class="msg" style="border-color:var(--line);background:var(--soft)">' + esc(e.detail) + '</div>';
    if(e.error) h += '<div class="msg">' + esc(e.error) + '</div>';
    if(e.rel) h += '<img loading="lazy" src="/api/artifact?p=' + encodeURIComponent(e.rel) + '" alt="evidence">';
    if(e.kind === 'screendump' && !e.rel){
      h += '<div class="verdict">a capture; the screen panel shows the newest one</div>';
    }
    detail.innerHTML = h;
  }

  // ---- the live feed ----------------------------------------------------------------
  function connect(){
    var url = '/api/journal/stream';
    if(run) url += '?run=' + encodeURIComponent(run) + '&since=' + lastCall;
    var es = new EventSource(url);
    es.addEventListener('step', function(ev){
      try { applyStep(JSON.parse(ev.data)); } catch(_){}
    });
    es.addEventListener('call', function(ev){
      var e;
      try { e = JSON.parse(ev.data); } catch(_) { return; }
      if(!run){ run = e.run_id; runEl.textContent = run; }
      if(e.seq <= lastCall) return;
      lastCall = e.seq;
      addCall(e, null);
      if(e.step_index) stepEl.textContent = e.step_index;
      tl.scrollTop = tl.scrollHeight;
    });
    es.onopen = function(){
      statusEl.textContent = 'live';
      dotEl.style.background = 'var(--ok)';
    };
    es.onerror = function(){
      statusEl.textContent = 'reconnecting';
      dotEl.style.background = 'var(--warn)';
      // EventSource reconnects on its own using the retry: the stream sent; nothing to do here but
      // say so, because a silent dead feed is the failure this page exists to prevent.
    };
  }

  // ---- the screen, and a click on it ------------------------------------------------
  function refreshScreen(){
    // A CACHE-BUSTING PARAMETER, because the browser would otherwise show the first frame for ever.
    screen.src = '/screen.png?t=' + Date.now();
  }
  setInterval(refreshScreen, 1000);
  refreshScreen();

  screen.addEventListener('click', function(ev){
    var r = screen.getBoundingClientRect();
    // THE IMAGE IS SCALED TO THE PANEL, so the pixel the person clicked is in the SOURCE frame's
    // coordinates - the same space every click: in this repository is written in.
    var x = Math.round((ev.clientX - r.left) * (screen.naturalWidth || r.width) / r.width);
    var y = Math.round((ev.clientY - r.top) * (screen.naturalHeight || r.height) / r.height);
    fetch('/api/click', {
      method: 'POST',
      headers: {'content-type': 'application/json'},
      body: JSON.stringify({x: x, y: y})
    }).then(function(res){ return res.json(); }).then(function(d){
      if(!d.ok) showCall({kind:'click', origin:'api', step:'(manual)', time:new Date().toISOString(),
                          detail:x+','+y, ok:false, error:d.error});
    }).catch(function(){});
  });

  connect();
})();
</script>
</body></html>`