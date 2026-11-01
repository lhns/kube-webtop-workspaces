# Spike: streaming one app at `/apps/<name>`

Question: can a desktop stream **one window** of the running Plasma session at `/apps/<name>`
instead of the whole desktop? Investigated from source only; nothing was run against a cluster.

Versions read: KWin **v6.8.1** (the version in webtop-nix's pinned nixpkgs), pixelflux **2.2.1**
(`67b6d83`, pinned by webtop-nix) and main `72e07bc`, Selkies main `58b6c35` (webtop-nix ships
2.1.0; the cited client, layout and fallback-handle code is the same there), webtop-nix `1aa1d33`.

Options:

- **A** — an extra Selkies instance per app in the same pod, with its own compositor; the app runs
  beside the KDE session, not in it.
- **B** — stream one window of the KDE session through KWin's window screencast
  (`zkde_screencast_unstable_v1.stream_window` → PipeWire), with window-relative input injection.
- **C** (found during the spike) — give the app **its own KWin screen** in the running session
  (a KWin virtual output), move the window there fullscreen, and stream that screen as a Selkies
  second display. Same session as B, but built from machinery that already exists end to end.

## Verdict

- **B as specified: no-go.** Capture works, but input cannot be made correct (an occluded window
  streams but does not receive its clicks), dialogs are not in the stream, and it needs a new
  capture source in pixelflux plus a new display kind in Selkies. Roughly 3–5 weeks plus carrying
  a fork, with the input problem still open.
- **C: go**, for one app at a time, ~1–1.5 weeks. Several apps at once need Selkies work on the
  web client and the extended-layout code (estimate soft, ~1 week). Needs one more KWin patch (or a
  small pixelflux change), see risk 1, and is gated on risk 3.
- **A** stays the fallback when an app must be independent of the session (own audio,
  concurrent use by another person, survives the session), ~1 week, at the cost of a second
  copy of the app and a compositor and encoder per app.

## 1. Window screencast in nested KWin 6.8

**Available, to any unsandboxed client, and works with software rendering.**

- `zkde_screencast_unstable_v1` and `org_kde_kwin_fake_input` are in KWin's restricted set, but
  the filter applies only to sandboxed clients
  ([wayland_server.cpp:131-162](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/wayland_server.cpp#L131-162)),
  and "sandboxed" means a flatpak or snap user unit
  ([clientconnection.cpp:31-57](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/wayland/clientconnection.cpp#L31-57)).
  No `X-KDE-Wayland-Interfaces` desktop entry is needed for a process started in the session.
  `KWIN_WAYLAND_NO_PERMISSION_CHECKS` no longer exists in 6.8.1 (no hits in `src/`); the variable
  in webtop-nix
  ([modules/webtop.nix:273](https://github.com/lhns/webtop-nix/blob/1aa1d335f26bd5a1c055e171e4fdbd427d42ddc2/modules/webtop.nix#L273))
  and the comment in pixelflux's
  [ficlient.rs:28](https://github.com/selkies-project/pixelflux/blob/72e07bc49fbad286092fead784e11b5be6444b4e/pixelflux/src/wayland/ficlient.rs#L28)
  are stale.
- `stream_window(uuid)` creates a `WindowScreenCastSource`; it requires OpenGL compositing
  ([screencastmanager.cpp:44-71](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/plugins/screencast/screencastmanager.cpp#L44-71)),
  which the patched KWin provides through llvmpipe. The uuid is KWin's `Window.internalId`
  (D-Bus `org.kde.KWin.queryWindowInfo`/`getWindowInfo`,
  [dbusinterface.h:78-87](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/dbusinterface.h#L78-87)).
- Buffers: the vendored software-presentation patch makes `ScreenCastStream::testCreateDmaBuf`
  return nothing on the memfd render device, so the PipeWire stream negotiates **memfd** buffers
  ([kwin-6.8-software-shm-presentation.patch, screencaststream.cpp hunk](https://github.com/lhns/webtop-nix/blob/1aa1d335f26bd5a1c055e171e4fdbd427d42ddc2/pkgs/kwin/kwin-6.8-software-shm-presentation.patch)).
  Each window frame is an extra llvmpipe render plus a `glReadPixels`-class copy on the CPU.
- What the source contains: the window plus transient **popups** only, added on `windowAdded`
  when `isPopupWindow()`
  ([windowscreencastsource.cpp:28-71](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/plugins/screencast/windowscreencastsource.cpp#L28-71));
  it renders offscreen (`refOffscreenRendering`), so it streams even when covered; the frame is the
  union of the members' frame geometries
  ([:206-213](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/plugins/screencast/windowscreencastsource.cpp#L206-213)),
  so the stream size changes whenever a menu opens past the window edge.
- KWin 6.8.1 has no `ext-image-copy-capture` (no hits in `src/`), so the standard per-toplevel
  capture protocol is not an alternative; pixelflux's comment that KWin 6.2+ serves it
  ([host.rs:7](https://github.com/selkies-project/pixelflux/blob/72e07bc49fbad286092fead784e11b5be6444b4e/pixelflux/src/wayland/host.rs#L7))
  does not hold for this KWin.

## 2. PipeWire / portal capture in Selkies and pixelflux

**The parts exist in pixelflux, but for a different mode; nobody does window capture.**

pixelflux 2.2.1 already contains (all present at the pinned commit):

- a dlopen'd PipeWire binding
  ([pipewire.rs](https://github.com/selkies-project/pixelflux/blob/67b6d833fc8eba87f9b15fb4cb3382d923a2fe56/pixelflux/src/pipewire.rs))
  and a PipeWire video consumer that takes dmabuf or memfd buffers in place
  ([wayland/pwcapture.rs](https://github.com/selkies-project/pixelflux/blob/67b6d833fc8eba87f9b15fb4cb3382d923a2fe56/pixelflux/src/wayland/pwcapture.rs));
- an xdg-desktop-portal client that asks for **monitors only**
  ([portal.rs:49](https://github.com/selkies-project/pixelflux/blob/72e07bc49fbad286092fead784e11b5be6444b4e/pixelflux/src/wayland/portal.rs#L49),
  [:317](https://github.com/selkies-project/pixelflux/blob/72e07bc49fbad286092fead784e11b5be6444b4e/pixelflux/src/wayland/portal.rs#L317));
- libei input over `ConnectToEIS`
  ([wayland/eiclient.rs](https://github.com/selkies-project/pixelflux/blob/67b6d833fc8eba87f9b15fb4cb3382d923a2fe56/pixelflux/src/wayland/eiclient.rs));
- a direct `zkde_screencast_unstable_v1` client, used to grow **virtual outputs** of a nested
  KWin for Selkies' second display
  ([wayland/kdeclient.rs](https://github.com/selkies-project/pixelflux/blob/67b6d833fc8eba87f9b15fb4cb3382d923a2fe56/pixelflux/src/wayland/kdeclient.rs)).

The PipeWire, portal and libei parts serve *host-capture* mode (pixelflux capturing an external
compositor, [selkies-project/pixelflux#46](https://github.com/selkies-project/pixelflux/issues/46)).
webtop-nix runs *nested* mode, where pixelflux is the host compositor. A window source would be a
hybrid: pixelflux consuming a PipeWire stream produced by its own nested client, exposed to Selkies
as an extra display. No upstream issue, PR or fork proposes window capture (searched both
repositories). Cost: Rust in pixelflux (a capture source fed by `stream_window` + `pwcapture`
against the session's PipeWire daemon instead of a portal fd, with frame-size changes), Python
and JS in Selkies (a display that is not a compositor output: sizing, input routing, cursor).

## 3. Input

**B: not solvable cleanly. C: native.**

- Injection into KWin goes through global layout coordinates: EIS absolute devices get one region
  per **output**
  ([eisbackend.cpp:173-189](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/plugins/eis/eisbackend.cpp#L173-189),
  D-Bus `org.kde.KWin.EIS.RemoteDesktop.connectToEIS`,
  [:102-112](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/plugins/eis/eisbackend.cpp#L102-112));
  `org_kde_kwin_fake_input` is the same. There is no window-relative mode. B would translate
  browser coordinates by the window's frame position and inject; KWin then hit-tests normally, so
  a click lands on whatever is on top at that point. Because the stream renders the window
  offscreen, a covered window looks fine and receives none of the clicks. Keeping the window on top
  and focused means fighting the desktop user over stacking and keyboard focus on one seat.
- Moving, resizing, focusing a given window is possible from outside: KWin scripts loaded over
  D-Bus (`org.kde.kwin.Scripting.loadScript`,
  [scripting.h:323](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/scripting/scripting.h#L323))
  can set `Window.frameGeometry`, `fullScreen`, `workspace.activeWindow` and
  `workspace.sendClientToScreen`
  ([window.h:412,612](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/window.h#L412),
  [workspace_wrapper.h:618](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/scripting/workspace_wrapper.h#L618)),
  matching windows by `desktopFileName`/`resourceClass`. KWin window rules can do the same
  declaratively.
- In **C** the app's screen is a real output: pixelflux opens it as a second host window
  ([wayland_backend.cpp:488-510, 622-627](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/backends/wayland/wayland_backend.cpp#L488-510)),
  Selkies routes the second page's pointer and keyboard to it in union-layout coordinates, and the
  output follows the browser's size. Popups, menus and dialogs are hit-tested by KWin as on any
  monitor. Pointer and keyboard focus are still one seat's: using the desktop tab and the app tab
  at once behaves like a two-monitor desktop, not like two independent sessions.

## 4. Popups, dialogs, multi-window apps

- **B**: menus, tooltips and other popups are captured (and grow the frame); dialogs, file
  pickers, password prompts and an app's second window are separate toplevels and never appear.
  An app that opens a modal dialog becomes unusable at `/apps/<name>`.
- **C**: KWin places transients and dialogs on the parent's output, so they show on the app's
  screen. Further top-level windows of the app land on the active output by KWin's placement
  policy; a window rule (or the script) pins all windows with the app's `desktopFileName` to that
  screen.
- **A**: everything the app opens is in its own compositor; correct by construction.

## 5. Audio per app

Selkies streams one PulseAudio/PipeWire source (`SELKIES_AUDIO_DEVICE_NAME`,
[modules/webtop.nix:233](https://github.com/lhns/webtop-nix/blob/1aa1d335f26bd5a1c055e171e4fdbd427d42ddc2/modules/webtop.nix#L233)),
and the web client ignores audio on a secondary display page
([selkies-web-core README:72](https://github.com/selkies-project/selkies/blob/58b6c350cd29ee4f4a2423ea1cbf9f927979064f/addons/selkies-web-core/README.md?plain=1#L72)).

- **B/C**: whole-session audio, played by the primary page only. Per-app audio would need a second
  sink (WirePlumber rule on the app's `application.process.binary`, or `pactl move-sink-input`) and
  Selkies changes to capture and send it on a secondary page.
- **A**: per-app audio is easy: one null sink per instance, the app started with `PULSE_SINK`, the
  instance's `SELKIES_AUDIO_DEVICE_NAME` on that sink's monitor.

## 6. Effort and risk

| | B: window screencast | C: KWin screen per app | A: Selkies per app |
| --- | --- | --- | --- |
| new code | pixelflux capture source (Rust), Selkies display kind (Py/JS), input translation, window tracking | KWin patch or pixelflux fix, KWin script, gateway route, agent launch hook | webtop-nix unit template, gateway route per port, launcher |
| effort | 3–5 weeks + fork upkeep | 1–1.5 weeks (one app at a time); N apps ~1 week more, soft | ~1 week |
| input | broken under occlusion | native | native |
| dialogs | missing | shown | shown |
| audio | session | session (primary page) | per app |
| same app instance as desktop | yes | yes | no (second process; profile locks, e.g. Firefox) |
| extra CPU/RAM | offscreen render per frame | one more KWin output + encoder | compositor + Selkies + encoder per app |
| main risk | unsolved input model | patch carry, one secondary in the web client | resource cost, app duplication |

**Recommendation: C.** It meets the requirement (a window of the running session, not a second
copy of the app), reuses Selkies' second display, pixelflux's KWin screen growing and KWin's own
virtual outputs, and keeps input, popups, dialogs and resizing correct for free. Choose A instead
when apps must have their own audio or be usable by someone else while the desktop is in use.

### Open risks (C)

1. **Second display is refused on our KWin today.** pixelflux confirms a grown screen by finding a
   `kde_output_device_v2` global in `wl_registry`
   ([kdeclient.rs:322](https://github.com/selkies-project/pixelflux/blob/67b6d833fc8eba87f9b15fb4cb3382d923a2fe56/pixelflux/src/wayland/kdeclient.rs#L322)),
   but KWin 6.7+ announces devices only through `kde_output_device_registry_v2`
   ([outputdevice_v2.cpp:24-62,320](https://invent.kde.org/plasma/kwin/-/blob/v6.8.1/src/wayland/outputdevice_v2.cpp#L24-62)).
   LinuxServer carries
   [kwin-6.7-output-device-globals.patch](https://github.com/linuxserver/selkies-layers/blob/main/patches/arch/arch/kwin/kwin-6.7-output-device-globals.patch)
   (6.7/6.8) for this; webtop-nix carries only the presentation patches. Fix: vendor that patch
   (like ADR 0004), or better, teach pixelflux to bind the registry object (upstream PR). LinuxServer's
   other patch, `kwin-nested-virtual-output.patch`, is already upstream in 6.8.1
   (`createOutput` does the bookkeeping).
2. **One secondary display assumed.** The page knows only `#display2`
   ([selkies-ws-core.js:870](https://github.com/selkies-project/selkies/blob/58b6c350cd29ee4f4a2423ea1cbf9f927979064f/addons/selkies-web-core/selkies-ws-core.js#L870));
   the server already maps any `displayN`
   ([display_utils.py:1358-1372](https://github.com/selkies-project/selkies/blob/58b6c350cd29ee4f4a2423ea1cbf9f927979064f/src/selkies/display_utils.py#L1358-L1372)).
   but the extended-layout helper is written for "a primary display plus one secondary"
   ([display_utils.py:1436-1460](https://github.com/selkies-project/selkies/blob/58b6c350cd29ee4f4a2423ea1cbf9f927979064f/src/selkies/display_utils.py#L1436-L1460)).
   Several apps at once need client and server layout changes (upstream PRs).
3. **Placement and lifecycle.** Apps that restore their geometry, single-instance apps that open
   their window elsewhere, Plasma laying a desktop containment on the new screen (hidden under the
   fullscreen app, still rendered once), and the app screen vanishing when its page closes (KWin
   moves the window back to the primary). Also unverified: whether a secondary page works with no
   primary page open (Selkies keeps a fallback handle for that case,
   [websockets_mode.py:1869](https://github.com/selkies-project/selkies/blob/58b6c350cd29ee4f4a2423ea1cbf9f927979064f/src/selkies/websockets_mode.py#L1869));
   if not, `/apps/<name>` needs the primary running headless.

## Sketched design (C)

Flow: user opens `/apps/<name>` → gateway serves the desktop's Selkies page with the second-display
fragment → Selkies/pixelflux grow KWin screen `displayN` sized to the tab → the agent starts or finds
the app and a KWin script sends its windows to that screen, fullscreen → closing the tab removes the
screen and the window returns to the primary.

| component | repo | change |
| --- | --- | --- |
| KWin output-device globals | webtop-nix (`pkgs/kwin/`) or pixelflux upstream | vendor LinuxServer's patch next to the presentation patches, or a pixelflux PR binding `kde_output_device_registry_v2` |
| second display check | webtop-nix VM test (`tests/vm.nix`) | open `#display2-right`, assert a second output appears in `kscreen-doctor -o` |
| app placement | webtop-nix module or kww image | a KWin script (packaged, loaded at session start) that, on request over D-Bus, finds windows by `desktopFileName`, `sendClientToScreen` to the named output, sets `fullScreen`, activates; and pins later windows of that app |
| launch | kww agent | endpoint `POST /apps/<name>`: start `<name>.desktop` via `systemd-run --user`/`gtk-launch` if not running, then call the script with the output name |
| route | kww gateway (`internal/gateway`) | `/apps/<name>` (and `/u/<user>/apps/<name>`) serves the base page with `#display2-<pos>` via the existing inject path and triggers the agent launch; contract.md gets the route |
| several apps | Selkies upstream | web client accepts `#displayN`, N ≥ 2; extended layout for more than one secondary |
| audio (later) | Selkies upstream + webtop-nix | per-app sink and audio on secondary pages, only if wanted |

Rough steps: (1) patch + VM test proving display2 on nested KWin 6.8.1, **with no primary page
connected**; (2) KWin script + manual placement test; (3) agent endpoint and gateway route, one app;
(4) Selkies `#displayN` and layout upstream for concurrent apps. Step 1 is the go/no-go gate: if the
second display does not come up with the patch, or needs the primary page open, fall back to A.
