// Served by the gateway and injected into the desktop's page. Reloads the tab when the desktop
// leaves Running (or is replaced by another pod), so the gateway can show the right page.
// It also pings the gateway every minute: the desktop's own traffic is one websocket, which the
// forward-auth in front of the gateway never sees, so its session would otherwise go inactive.
(function () {
  var script = document.currentScript;
  if (!script || !script.src || !window.EventSource) return;
  // The status stream is next to this script; watch=1 keeps it open while the desktop runs.
  var url = new URL("events?watch=1", script.src).href;
  var seen = null; // pod of the Running state this tab has observed
  var delay = 1000;
  var es;

  function connect() {
    es = new EventSource(url);
    es.addEventListener("status", function (e) {
      var s;
      try { s = JSON.parse(e.data); } catch (_) { return; }
      delay = 1000;
      if (s.phase === "Running") {
        if (seen === null) seen = s.podUID || "";
        else if (seen !== (s.podUID || "")) reload();
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

  function reload() {
    if (es) es.close();
    stopPing();
    location.reload();
  }

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
