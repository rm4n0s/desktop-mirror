package ui

import (
	"context"

	m "github.com/rm4n0s/glutter/material"
	gui "github.com/rm4n0s/glutter/ui"

	"github.com/rm4n0s/errors"
)

const appID = "dev.rm4n0s.desktop-mirror"

// Run shows the window and blocks until it is closed.
func Run(cfg Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := newController(ctx, cfg)
	defer c.close()

	err := m.RunApp(newApplication(c), m.RunAppOpts{
		AppID: appID,
		Window: m.WindowConfig{
			Title:       "Desktop Mirror",
			Size:        m.Size{Width: 1180, Height: 720},
			MinSize:     m.Size{Width: 760, Height: 560},
			ContentType: gui.ContentTypeVideo,
		},
	})
	if err != nil {
		return errors.NewErr("RunAppFailed", err)
	}
	return nil
}
