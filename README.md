# desktop-mirror

Show your phone's camera on your PC, and your PC's camera on your phone, over WebRTC.

1. Start the desktop app and pick a PC camera.
2. Scan the QR code with your phone and accept the one-time certificate warning.
3. Allow camera access on the phone. Both feeds start.

The app serves the phone page itself over HTTPS on your LAN, so both devices must be on the same network. Nothing leaves your network unless you configure a STUN server.

## Install the build dependencies

You need Go, a C++ toolchain, `pkg-config`, Qt 6 development files (including Multimedia), and FFmpeg development files (`libavcodec`, `libavutil`) from an FFmpeg built with libvpx, which provides the VP8 encoder.

**Fedora** (the package set the project is developed against):

```
sudo dnf install golang gcc-c++ pkgconf-pkg-config mesa-libGL-devel \
    qt6-qtbase-devel qt6-qtmultimedia-devel \
    libavcodec-free-devel libavutil-free-devel libvpx
```

**Ubuntu / Debian** (package names not tested on this project; the CI workflow installs the same set):

```
sudo apt update
sudo apt install golang-go build-essential pkg-config libgl1-mesa-dev \
    qt6-base-dev qt6-multimedia-dev \
    libavcodec-dev libavutil-dev libvpx-dev
```

**Arch Linux** (package names not tested on this project):

```
sudo pacman -S go base-devel pkgconf \
    qt6-base qt6-multimedia \
    ffmpeg libvpx
```

On Arch the `ffmpeg` package already includes the development headers and `libvpx` support. `go.mod` requires Go 1.26.8. If your distribution's `go` package is older, Go can download the right toolchain itself (`GOTOOLCHAIN=auto`, the default from Go 1.21), or you can install Go from [go.dev/dl](https://go.dev/dl/).

To check that the libraries are found: `pkg-config --modversion Qt6Multimedia libavcodec`. To check the VP8 encoder: `ffmpeg -hide_banner -encoders | grep libvpx`.

## Build and run

```
go build -o desktop-mirror ./cmd/desktop-mirror
./desktop-mirror
```

The first build compiles miqt's Qt bindings and takes around 10 minutes; later builds are fast.

Flags: `-port` (default 8443), `-udp-min`/`-udp-max` (restrict the WebRTC media ports so a firewall rule can be written), `-stun`, `-config-dir`. Allow the HTTPS port and the UDP range through your firewall if the phone cannot connect.

## Develop

```
go test -race ./...
```

Tests for everything except the Qt code need neither Qt nor a camera. The camera test skips when there is no camera. See [docs/SPEC.md](docs/SPEC.md) for the design.
