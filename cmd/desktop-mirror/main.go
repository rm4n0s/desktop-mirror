// Command desktop-mirror shows a phone's camera on the PC and the PC's
// camera on the phone, paired by scanning a QR code.
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/rm4n0s/desktop-mirror/internal/media/ffmpeg"
	"github.com/rm4n0s/desktop-mirror/internal/ui"
)

func main() {
	defaultDir, err := os.UserConfigDir()
	if err != nil {
		defaultDir = os.TempDir()
	}
	configDir := flag.String("config-dir", filepath.Join(defaultDir, "desktop-mirror"), "directory for the TLS certificate")
	port := flag.Int("port", 8443, "preferred HTTPS port (a free port is used if taken)")
	udpMin := flag.Uint("udp-min", 0, "lowest UDP port for WebRTC media (0 = any)")
	udpMax := flag.Uint("udp-max", 0, "highest UDP port for WebRTC media (0 = any)")
	stun := flag.String("stun", "", "optional STUN server URL, e.g. stun:stun.l.google.com:19302")
	flag.Parse()

	cfg := ui.Config{
		ConfigDir:      *configDir,
		Port:           *port,
		UDPPortMin:     uint16(*udpMin),
		UDPPortMax:     uint16(*udpMax),
		EncoderFactory: ffmpeg.NewEncoder,
		DecoderFactory: ffmpeg.NewDecoder,
	}
	if *stun != "" {
		cfg.STUNServers = []string{*stun}
	}
	if err := ui.Run(cfg); err != nil {
		log.Fatal(err)
	}
}
