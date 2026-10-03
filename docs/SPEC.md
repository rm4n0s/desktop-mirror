# desktop-mirror: Specification

Status: implemented (milestones M0-M5 below, minus the items under "Not done yet"). Sections 2 to 6 describe the design; section 11 records where the implementation differs from the first draft.

## 1. Goal

A Go desktop app (Qt via [miqt](https://github.com/mappu/miqt)) and a small website that mirror cameras between a PC and a phone over WebRTC:

- **Phone → PC**: the phone's camera feed is shown in the desktop app.
- **PC → phone**: the PC's camera feed is shown in the phone's browser, on the website.

Pairing flow:

1. The user picks a PC camera in the desktop app.
2. The app shows a QR code containing the website URL (with a one-time pairing token).
3. The user scans the QR code with the phone and opens the link in the phone's browser.
4. The phone grants camera permission, and both video feeds start.

### Non-goals (v1)

- Audio (video only; no microphone capture or playback).
- Internet / cross-NAT use. v1 is same-LAN only: no TURN, STUN is optional and off by default.
- More than one phone at a time.
- Recording, screenshots, or virtual-camera output.
- A native phone app. The phone side is a plain web page with no build step.

## 2. Key design decisions

### 2.1 The desktop app hosts the website and the signaling server

The app runs an embedded HTTPS server on the LAN. It serves the static page (`go:embed`) and a WebSocket endpoint used only for exchanging SDP and ICE candidates. No external service is needed.

**HTTPS is mandatory, not optional.** Mobile browsers only expose `getUserMedia` in a secure context, and a plain `http://192.168.x.x` URL is not one. The app therefore generates a self-signed ECDSA P-256 certificate (SANs: the selected LAN IP and `localhost`), persists it in the user config dir, and regenerates it when the selected IP is not covered. The phone shows a certificate warning once, which the user must accept. This is the main UX cost of the design (see section 10 for the alternative).

### 2.2 WebRTC stack: pion in the desktop app, native browser WebRTC on the phone

- Desktop: [`pion/webrtc`](https://github.com/pion/webrtc) (pure Go networking, no cgo).
- Phone: the browser's `RTCPeerConnection`.

### 2.3 Codecs are the hard part

Pion does not encode or decode video. The desktop app must both **decode** the phone's stream and **encode** the PC camera stream, so it needs a codec library behind cgo.

Decision (needs your approval, see section 10): use **libavcodec (FFmpeg) through cgo**, hidden behind two small interfaces so the choice is swappable and tests don't need it:

```go
type Encoder interface {
    Encode(ctx context.Context, f I420Frame, forceKeyframe bool) ([]byte, error)
    SetBitrate(bps int) error
    Close() error
}
type Decoder interface {
    Decode(ctx context.Context, au []byte) (I420Frame, error)
    Close() error
}
```

- **Default codec: VP8.** Royalty-free, BSD-licensed (libvpx), supported by Chrome, Firefox and Safari. H.264 is a possible later addition for hardware acceleration, but avoid GPL-only builds (x264) so the project's license stays unconstrained.
- Rejected: embedding QtWebEngine and letting Chromium do WebRTC. It makes codecs free but is a heavy dependency, hides the video behind a web view instead of Qt widgets, and complicates camera selection.

### 2.4 Camera capture and display use Qt Multimedia

- Camera enumeration: `QMediaDevices` (video inputs).
- Capture: `QCamera` + `QMediaCaptureSession` + `QVideoSink`. Each `videoFrameChanged` frame is converted to I420 and handed to the encoder.
- Display (both the local preview and the phone feed): a widget showing `QImage` frames.

The miqt README lists QtMultimedia and QtMultimediaWidgets as bound. Whether the pixel-level API (`QVideoFrame` map/`toImage`) is usable from Go has **not** been verified; milestone M1 is a spike for exactly this (see section 9).

### 2.5 The desktop app is the SDP offerer

When the phone connects to the signaling WebSocket, the desktop creates the offer with one `sendrecv` video transceiver. The phone, after the user has granted camera permission, attaches its camera track to that transceiver and answers. One negotiation, no renegotiation: camera switching on either side swaps the track source (`replaceTrack` on the phone, source swap plus forced keyframe on the desktop).

## 3. Architecture

```
 ┌───────────────────────── desktop-mirror process ─────────────────────────┐
 │                                                                          │
 │  internal/ui (miqt)          internal/app (session state machine)        │
 │  ├ MainWindow  ◄──state/frames──┤                                        │
 │  ├ VideoView                    ├──► internal/signaling (HTTPS + WS)  ◄──┼── phone browser (web/)
 │  └ QRView                       │        └ token manager, TLS cert       │      │
 │                                 └──► internal/rtc (pion PeerConnection) ◄┼──────┘ WebRTC (SRTP/UDP, LAN)
 │  internal/camera (miqt)  ──frames──►     │          │                    │
 │                                    internal/media (Encoder/Decoder,      │
 │                                    I420/RGBA conversion; ffmpeg impl)    │
 └──────────────────────────────────────────────────────────────────────────┘
```

### Package layout

Follows the repo conventions (`cmd/<app>`, `internal/`, tests beside code):

| Path | Responsibility | Imports miqt? |
|---|---|---|
| `cmd/desktop-mirror/` | Thin `main.go`: build dependencies, start the app | no |
| `internal/app/` | Session state machine; wires camera, rtc, signaling, and UI | no |
| `internal/ui/` | Main window, video view, QR view (miqt widgets) | **yes** |
| `internal/camera/` | `QMediaDevices` listing, `QCamera` capture to frames | **yes** |
| `internal/media/` | `I420Frame`, `Encoder`/`Decoder` interfaces, colorspace helpers | no |
| `internal/media/ffmpeg/` | libavcodec implementation of the interfaces (cgo) | no |
| `internal/rtc/` | Pion peer wrapper: offer/answer, tracks, RTP (de)packetization, PLI/NACK | no |
| `internal/signaling/` | HTTPS server, WebSocket handler, message types, one-time token manager | no |
| `internal/tlscert/` | Self-signed cert generation and persistence | no |
| `internal/netutil/` | LAN interface discovery and default-interface choice | no |
| `internal/web/` | `go:embed` of `static/` (`index.html`, `app.js`, `style.css`; vanilla JS) | no |

Only `ui` and `camera` import miqt, so everything else builds and tests quickly without Qt installed (miqt's first compile takes roughly 10 minutes).

### Threading rules

miqt binds the Qt application to a fixed OS thread, so Qt objects must only be touched there. From any other goroutine use `qt6/mainthread` (`Start`/`Wait`).

- **Capture path** (Qt thread to Go): the `videoFrameChanged` handler copies/converts the frame to I420 immediately and does a non-blocking send into a size-1 channel (drop the stale frame; never block the Qt thread). A worker goroutine encodes and writes RTP.
- **Display path** (Go to Qt thread): the decoder goroutine stores the latest RGBA frame in an atomic slot and schedules at most one pending `mainthread.Start` to paint it. The `QImage` is created on the Qt thread from a copy, since the Go buffer is reused.
- Pion callbacks run on pion goroutines and never touch Qt directly.

## 4. Session lifecycle

```
Idle ─start─► WaitingForPhone ─ws hello─► Negotiating ─ICE connected─► Connected
  ▲                 ▲                          │                           │
  │                 └──── timeout / failure ───┴──── disconnect / bye ─────┘
  └──────────── user stops / app exit ──────────────────────────────────────
```

- Start: generate a token, start (or reuse) the HTTPS server, render the QR code, state `WaitingForPhone`.
- A phone connecting to `/s/<token>/ws` with a valid token moves the session to `Negotiating`; the desktop creates a new `PeerConnection` and sends the offer.
- Disconnect (ICE failed/closed, WS closed, `bye`): tear down the peer, rotate the token, return to `WaitingForPhone` and refresh the QR code. A phone page reload during `Connected` therefore needs a re-scan. (Possible later improvement: a 60 s grace window on the old token.)
- State changes are published to the UI as events; the UI never reads session internals directly.

## 5. Signaling protocol

Transport: WebSocket at `wss://<ip>:<port>/s/<token>/ws`. The page itself is `GET /s/<token>/`. JSON text frames:

| `type` | Direction | Fields | Meaning |
|---|---|---|---|
| `hello` | phone → desktop | `ua` (string) | Phone page is ready (camera permission already granted) |
| `offer` | desktop → phone | `sdp` | SDP offer |
| `answer` | phone → desktop | `sdp` | SDP answer |
| `candidate` | both | `candidate`, `sdpMid`, `sdpMLineIndex` | Trickled ICE candidate (empty `candidate` = end of candidates) |
| `bye` | both | | Clean teardown |
| `error` | desktop → phone | `code`, `message` | e.g. `session_busy`, `bad_token` |

Pairing security:

- Token: 128 bits from `crypto/rand`, base64url, carried in the URL path.
- Valid for 10 minutes while waiting; consumed by the first accepted WebSocket. A second connection while a session is active is rejected (`session_busy`). The token rotates after every session.
- The server binds only to the selected LAN interface and rejects WebSocket upgrades with a mismatched `Origin`.
- Residual risk: anyone on the LAN who sees the QR URL can pair first. Acceptable for v1; the UI shows the connected phone's user agent so an unexpected pairing is visible.

## 6. WebRTC details

- **ICE**: host candidates only by default; optional STUN server in settings. Pion runs with `MulticastDNSMode: QueryOnly` so it can resolve the `.local` mDNS candidates that browsers emit before camera permission is granted. Since the page requests the camera before sending `hello`, browsers normally expose real IPs anyway.
- **UDP ports**: restrict to a configurable range via `SettingEngine.SetEphemeralUDPPortRange` so a firewall rule can be documented and applied. The HTTPS port is fixed (default `8443`, configurable, auto-fallback to a free port if taken).
- **RTP**: VP8 packetization with pion's `codecs.VP8Payloader`; receive side uses `samplebuilder` + `codecs.VP8Packet` to reassemble frames.
- **Feedback**: register pion's default interceptors (NACK, RTCP reports). On PLI/FIR from the phone, force a keyframe. On loss, send PLI to the phone. v1 uses a fixed target bitrate (default 1.5 Mbit/s at 640×480 to 1280×720, 30 fps); congestion-controlled bitrate (TWCC) is a later improvement.
- **Orientation**: the phone rotating changes the stream resolution mid-call; the decoder and `VideoView` must handle resolution changes without restarting the session.
- **Phone page**: `<video autoplay playsinline muted>` for the remote feed (required for autoplay on iOS), a start button as the user gesture that triggers `getUserMedia`, and a front/back camera toggle using `replaceTrack`.

## 7. Desktop UI

Single main window:

- **Left panel**: camera selector (`QComboBox` from `QMediaDevices.VideoInputs`, refreshed on `videoInputsChanged`), network interface selector (shown only when more than one LAN interface exists), local preview, QR code image, the URL as selectable text with a Copy button, "New QR code" button, status label (`Waiting for phone` / `Connecting` / `Connected: <user agent>` / `Disconnected`), Disconnect button.
- **Center**: the phone feed (`VideoView`), with a placeholder until the first frame.
- Changing the camera while connected swaps the capture source without ending the session. Changing the network interface restarts the server, rotates the token, and regenerates the QR code.
- The QR code is rendered with a QR library and painted into a `QImage` at a fixed module size with a quiet zone, so it scans reliably at any window size.

## 8. Error handling and testing

Per `CLAUDE.md`: all errors via `github.com/rm4n0s/errors` with a unique PascalCase Tag per failure, never re-wrapped; every new failure gets a test that asserts the route with `HasRoute`. Initial tags (not exhaustive):

| Tag | Where | Cause |
|---|---|---|
| `NoCameraFound` | camera | `QMediaDevices` returns no video inputs |
| `CameraOpenFailed` | camera | device busy or permission denied |
| `NoLanInterface` | netutil | no usable non-loopback IPv4 interface |
| `CertGenerationFailed` | tlscert | key or certificate creation error |
| `ServerListenFailed` | signaling | port in use / bind failure |
| `TokenInvalid` / `TokenExpired` / `SessionBusy` | signaling | pairing token rejected |
| `SignalingMessageInvalid` | signaling | malformed JSON or unknown `type` |
| `PeerConnectionFailed` | rtc | ICE failure or negotiation error |
| `EncodeFailed` / `DecodeFailed` | media | codec error |

Tests (table-driven, next to the code):

- **Unit**: token manager (expiry, single use, rotation), message parsing, cert SANs, interface selection, session state machine against a fake peer and fake signaling.
- **Integration (no Qt, no ffmpeg)**: a test "phone" built with pion connects to the real signaling server over loopback, answers the offer, and exchanges frames through stub passthrough `Encoder`/`Decoder` implementations. This covers the signaling protocol, negotiation, RTP flow, and teardown/re-pairing.
- **UI smoke**: construct the main window with `QT_QPA_PLATFORM=offscreen` and drive state events.
- **Manual checklist** (cannot be automated): Chrome on Android and Safari on iOS; cert warning flow; front/back camera switch; phone rotation; unplugging the PC camera; Wi-Fi drop and re-pair; Linux first, then Windows/macOS.

CI (GitHub Actions) installs Qt 6 dev packages (with Multimedia) and FFmpeg dev packages, and caches `GOCACHE` because the first miqt build is slow. The Qt-free packages are tested in a fast job that needs neither.

## 9. Milestones

| # | Deliverable | Notes |
|---|---|---|
| M0 | Go module, package skeleton, CI, lint config | Module path assumed `github.com/rm4n0s/desktop-mirror` |
| M1 | Qt window with camera selector and live local preview | **Spike**: confirm `QVideoSink`/`QVideoFrame` pixel access and `QImage` display from miqt |
| M2 | HTTPS + WebSocket server, cert, token, embedded page, QR code in the UI | No media yet; the phone can open the page and reach `hello` |
| M3 | Pion negotiation and phone → desktop video | Needs the ffmpeg decoder |
| M4 | Desktop → phone video | Needs the ffmpeg encoder, PLI handling |
| M5 | Reconnect/re-pair, hot-plug, camera switching, error surfacing, packaging notes | |

## 10. Open questions and risks

Decisions needed from you:

1. **New dependencies** (`CLAUDE.md` says to ask first). Proposed:
   - `github.com/mappu/miqt` (required by you)
   - `github.com/pion/webrtc/v4` (also pulls in `pion/interceptor`, `pion/rtp`)
   - `github.com/coder/websocket` (context-aware, actively maintained; `gorilla/websocket` is archived)
   - `github.com/skip2/go-qrcode`
   - An FFmpeg binding for libavcodec (for example `github.com/asticode/go-astiav`; to be evaluated and confirmed in M3, with `libvpx` as the only required codec). Requires FFmpeg dev libraries on the build machine and runtime.
   - Everything else stays on the standard library (`net/http`, `crypto/tls`, `crypto/x509`, `embed`).
2. **LAN-only with a self-signed cert vs a publicly hosted site.** A hosted HTTPS page cannot talk to a LAN signaling server (mixed content or untrusted cert), so a hosted site means a public signaling relay and likely TURN, which is out of scope for v1. The alternative that keeps trusted HTTPS on the LAN is a wildcard-certificate DNS service. Recommendation: self-signed for v1.
3. **Target platform order.** Assumed Linux first (your current OS), then Windows/macOS.
4. **Codec.** VP8 via libavcodec assumed; say so if you would rather have H.264 for hardware acceleration and accept its licensing constraints.

Risks:

- miqt's Qt Multimedia coverage for raw frame access is unverified (mitigated by the M1 spike; the fallback is capturing through V4L2/another Go library on Linux).
- The self-signed certificate warning is a real friction point on iOS and some Android browsers.
- Software VP8 encoding at 720p30 uses noticeable CPU; the default resolution/bitrate may need tuning.
- OS firewalls may block the HTTPS port or the UDP range on first run; the app should detect "phone never connected" and hint at this.

## 11. Implementation notes

Differences from the first draft:

- **No FFmpeg binding dependency.** `internal/media/ffmpeg` calls libavcodec directly through a small cgo layer (`pkg-config: libavcodec libavutil`), so `go-astiav` was not needed. The libvpx encoder and the built-in VP8 decoder are used. libavcodec's libvpx wrapper cannot change the rate on the fly, so `SetBitrate` restarts the encoder with a keyframe on the next frame.
- **`internal/qtutil`** exists because of miqt's memory model: values returned by value from Qt (`ToImage`, `Copy`, `ConvertToFormat`, device lists) carry a Go finalizer that deletes the C++ object. Calling `Delete` on them double-frees, and waiting for the finalizer lets frame buffers pile up because the Go GC cannot see C++ memory. `qtutil.Release` clears the finalizer and then deletes. Device lists returned by `QMediaDevices_VideoInputs` must be read inside `qtutil.WithGCPaused`.
- **Camera mode selection.** The camera is asked for the mode closest to 1280x720 at about 30 fps (`camera.BestFormat`); the test webcam otherwise defaults to 2592x1944. Frames wider than 1280 pixels are also shrunk before encoding (`media.ShrinkRGBA`).
- **Production wiring** lives in `internal/app/wiring.go` (`NewServerFactory`, `NewPeerFactory`) so the end-to-end test exercises the same code as the app. `ui.Run` builds the session; `cmd/desktop-mirror` only parses flags.
- **Certificates** cover every LAN address, so switching interface does not force the phone to accept a new one.
- **Loss detection** on the receive side uses RTP sequence gaps (rate limited to one keyframe request per second) plus pion's periodic PLI interceptor, rather than the sample builder's release handler, which fires for normal packets too.

Test coverage: unit tests for tokens, messages, certificates, interface ranking and colour conversion; a real-codec round trip (VP8 at VGA, 720p and portrait); a pion-to-pion loopback test; a session test with fakes; an end-to-end test (real HTTPS/WebSocket server, real pion peers, stub codecs) that drives the same signaling protocol as the web page; an offscreen Qt test of the window; and a camera test that skips without hardware.

Not done yet:

- Nothing has been run against a real phone browser (Chrome on Android, Safari on iOS). `web/static/app.js` is only syntax-checked and mirrored by the Go test phone.
- Windows and macOS builds, packaging, and a signed/trusted certificate flow.
- Congestion-controlled bitrate (fixed 1.5 Mbit/s for now) and hardware codecs.
