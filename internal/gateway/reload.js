// Served by the gateway and injected into the desktop's page. Reloads the tab when the desktop
// leaves Running, falls asleep (or is replaced by another pod), so the gateway can show the right page.
// An app page (#display<n>-right-<app>, see /apps/<app>) also tells the gateway which screen has the
// user's attention, so the desktop raises that app's windows; a plain page asks for the main screen.
// It also pings the gateway every minute: the desktop's own traffic is one websocket, which the
// forward-auth in front of the gateway never sees, so its session would otherwise go inactive.
(function () {
  var script = document.currentScript;
  if (!script || !script.src || !window.EventSource) return;
  // The status stream is next to this script; watch=1 keeps it open while the desktop runs.
  var url = new URL("events?watch=1", script.src).href;
  var m = /^#display(\d+)-[a-z]+(?:-([A-Za-z0-9._-]+))?$/.exec(location.hash);
  var screen = m ? m[1] : "main";
  var app = (m && m[2]) || "";
  if (app) document.title = app;
  var seen = null; // pod of the Running state this tab has observed
  var awake = false; // this tab has seen the desktop Running and not asleep
  var loggedOut = null; // lastLogout as first seen; only a later change counts
  var locked = null; // lastLock, likewise
  var delay = 1000;
  var es;

  function connect() {
    es = new EventSource(url);
    es.addEventListener("status", function (e) {
      var s;
      try { s = JSON.parse(e.data); } catch (_) { return; }
      delay = 1000;
      // The session logged out while this tab was open (the gateway sends it to the owner only,
      // and only with a logout URL): visit the gateway path that redirects to the provider.
      if (loggedOut === null) loggedOut = s.loggedOut || "";
      else if (s.loggedOut && s.loggedOut !== loggedOut) { logout(); return; }
      // Lock signs out the same way; the desktop itself keeps running.
      if (locked === null) locked = s.locked || "";
      else if (s.locked && s.locked !== locked) { logout(); return; }
      if (s.phase === "Running") {
        if (seen === null) seen = s.podUID || "";
        else if (seen !== (s.podUID || "")) reload();
        // Sleep freezes the stream: reload so the gateway shows its sleep page. A tab opened
        // while the desktop is still waking is not awake yet, so it does not loop.
        if (s.asleep) { if (awake) reload(); } else awake = true;
      } else if (seen !== null) {
        reload();
      }
    });
    es.onerror = function () {
      // The browser retries by itself unless the answer was an HTTP error.
      if (es.readyState !== EventSource.CLOSED) return;
      setTimeout(connect, delay);
      delay = Math.min(delay * 2, 30000);
    };
  }

  function logout() {
    if (es) es.close();
    stopPing();
    location.assign(new URL("logout", script.src).href);
  }

  function reload() {
    if (es) es.close();
    stopPing();
    // An app page goes back through /apps/<app>, which places the app again: a restored tab or a
    // restarted desktop would otherwise show a screen no app is on.
    if (app) location.replace(new URL("../apps/" + encodeURIComponent(app), script.src).href);
    else location.reload();
  }

  // Raising a screen is best effort; a 404 on an app page means the desktop forgot the app.
  var focusUrl = new URL("focus?screen=" + screen, script.src).href;
  var focusTimer = null;
  function raiseScreen() {
    clearTimeout(focusTimer);
    focusTimer = setTimeout(function () {
      fetch(focusUrl, { method: "POST", credentials: "same-origin", cache: "no-store", redirect: "manual" })
        .then(function (r) { if (r.status === 404 && app) reload(); }, function () {});
    }, 300);
  }
  addEventListener("focus", raiseScreen);
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "visible") raiseScreen();
  });
  raiseScreen();

  // Any answer is fine: a redirect, 401 or 403 means the session is already gone and a reload
  // (the user's call) shows the login. Errors are ignored; the next tick tries again.
  var pingUrl = new URL("ping", script.src).href;
  var pinger = null;
  function startPing() {
    if (pinger === null) pinger = setInterval(function () {
      fetch(pingUrl, { credentials: "same-origin", cache: "no-store", redirect: "manual" }).catch(function () {});
    }, 60000);
  }
  function stopPing() { clearInterval(pinger); pinger = null; }
  addEventListener("pagehide", stopPing);
  addEventListener("pageshow", function (e) { if (e.persisted) startPing(); }); // back from bfcache
  startPing();

  connect();
})();
