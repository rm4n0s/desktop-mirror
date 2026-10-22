// Package ui is the glutter front end: the home page and the controller that
// connects it to the camera and the pairing session.
package ui

import (
	"context"

	m "github.com/rm4n0s/glutter/material"
	"github.com/rm4n0s/glutter/services"
	gui "github.com/rm4n0s/glutter/ui"
)

const (
	panelWidth = 360.0
	gap        = 8.0
)

var (
	black       = m.NewColor(0xFF000000)
	placeholder = m.NewTextStyle(m.TextStyleOpts{Color: m.Some(m.NewColor(0xFFAAAFB9))})
	errorStyle  = m.NewTextStyle(m.TextStyleOpts{Color: m.Some(m.NewColor(0xFFC0392B))})
)

// updateFunc changes the view model on the UI thread. It can be called from any
// goroutine and reports false when there is no page to show the change.
type updateFunc func(func(*viewModel)) bool

// host is what the home page needs from the application behind it.
type host interface {
	// attach is called when the page appears. update is valid until detach;
	// attach must not call it before it returns.
	attach(update updateFunc)
	detach()

	selectCamera(i int)
	selectInterface(i int)
	newPairing()
}

// HomePage is the main window: pairing controls on the left, the phone's feed
// on the right.
type HomePage struct {
	m.StatefulWidget
	host host
}

func newHomePage(h host) *HomePage {
	return &HomePage{StatefulWidget: m.NewStatefulWidgetBase(), host: h}
}

func (p *HomePage) CreateState() m.State { return &homeState{} }

type homeState struct {
	m.StateOf[*HomePage]
	vm viewModel
}

func (s *homeState) InitState() {
	s.vm = newViewModel()
	s.Widget().host.attach(s.update)
}

func (s *homeState) Dispose() { s.Widget().host.detach() }

// update runs fn on the UI thread. SetState drops it silently once the page is
// gone, and the host stops calling after detach.
func (s *homeState) update(fn func(*viewModel)) bool {
	s.SetState(func() { fn(&s.vm) })
	return true
}

func (s *homeState) Build(bc m.BuildContext) m.Widget {
	vm := &s.vm
	h := s.Widget().host

	left := []m.Widget{
		m.NewText("PC camera"),
		s.picker(len(vm.cameras), vm.cameraIndex, "No camera", func(i int) string { return vm.cameras[i].Description },
			func(i int) {
				s.SetState(func() { vm.cameraIndex = i })
				h.selectCamera(i)
			}),
		m.NewExpanded(imageView(vm.preview, vm.previewPlaceholder, true)),
	}
	if len(vm.ifaces) > 1 {
		left = append(left,
			m.NewText("Network"),
			s.picker(len(vm.ifaces), vm.ifaceIndex, "", func(i int) string { return vm.ifaces[i].String() },
				func(i int) {
					s.SetState(func() { vm.ifaceIndex = i })
					h.selectInterface(i)
				}),
		)
	}
	left = append(left, m.NewExpanded(imageView(vm.qr, vm.qrPlaceholder, false), m.ExpandedOpts{Flex: m.Some(2)}))
	if vm.link != "" {
		left = append(left, m.NewSelectionArea(m.NewText(vm.link)))
	}
	left = append(left,
		m.NewWrap([]m.Widget{
			m.NewFilledButtonTonal(s.copyAction(bc, vm), m.NewText("Copy link")),
			m.NewFilledButtonTonal(onlyIf(vm.waiting(), h.newPairing), m.NewText("New QR code")),
			m.NewFilledButtonTonal(onlyIf(vm.canHangUp(), h.newPairing), m.NewText("Disconnect")),
		}, m.WrapOpts{Spacing: gap, RunSpacing: gap}),
		m.NewText(vm.status),
	)
	if vm.err != "" {
		left = append(left, m.NewText(vm.err, m.TextOpts{Style: errorStyle}))
	}

	return m.NewScaffold(m.ScaffoldOpts{
		Body: m.NewRow([]m.Widget{
			m.NewSizedBox(
				m.NewPadding(m.NewEdgeInsetsAll(16), m.NewColumn(left, m.ColumnOpts{
					CrossAxisAlignment: m.CrossAxisAlignmentStretch,
					Spacing:            gap,
				})),
				m.SizedBoxOpts{Width: m.Some(panelWidth)},
			),
			m.NewExpanded(m.NewPadding(m.NewEdgeInsetsAll(gap), imageView(vm.remote, remotePlaceholder, true))),
		}, m.RowOpts{CrossAxisAlignment: m.CrossAxisAlignmentStretch}),
	})
}

// picker is a drop-down over n entries named by label. It is disabled when
// there is nothing to pick, and then shows hint.
func (s *homeState) picker(n, selected int, hint string, label func(int) string, pick func(int)) m.Widget {
	if n == 0 {
		return m.NewDropdownButton[int](nil, nil, m.DropdownButtonOpts[int]{IsExpanded: true, Hint: m.NewText(hint)})
	}
	items := make([]*m.DropdownMenuItem[int], n)
	for i := range items {
		items[i] = m.NewDropdownMenuItem(
			m.NewText(label(i), m.TextOpts{Overflow: m.TextOverflowEllipsis, MaxLines: m.Some(1)}),
			m.DropdownMenuItemOpts[int]{Value: m.Some(i)})
	}
	return m.NewDropdownButton(items, func(v m.Opt[int]) {
		if i, ok := v.Get(); ok {
			pick(i)
		}
	}, m.DropdownButtonOpts[int]{IsExpanded: true, Value: m.Some(selected)})
}

// copyAction copies the pairing link; nil (a disabled button) while there is
// none to copy.
func (s *homeState) copyAction(bc m.BuildContext, vm *viewModel) func() {
	if !vm.waiting() || vm.url == "" {
		return nil
	}
	url := vm.url
	clipboard := m.WidgetsBindingOf(bc).Clipboard()
	return func() {
		clipboard.SetData(context.Background(), services.NewClipboardData(url), func(err error) {
			if err != nil {
				s.update(func(vm *viewModel) { vm.err = err.Error() })
			}
		})
	}
}

// onlyIf returns fn when ok, else nil, which disables a button.
func onlyIf(ok bool, fn func()) func() {
	if ok {
		return fn
	}
	return nil
}

// imageView paints img centered and scaled to fit on a dark background, or the
// placeholder text while there is no image. smooth blends the pixels of scaled
// video; otherwise they stay sharp, as for QR codes.
func imageView(img *gui.Image, text string, smooth bool) m.Widget {
	var child m.Widget
	if img != nil {
		quality := m.FilterQualityNone
		if smooth {
			quality = m.FilterQualityMedium
		}
		child = m.NewRawImage(img, m.RawImageOpts{Fit: m.BoxFitContain, FilterQuality: quality})
	} else {
		child = m.NewCenter(m.NewText(text, m.TextOpts{Style: placeholder, TextAlign: m.TextAlignCenter}))
	}
	return m.NewSizedBoxExpand(m.NewColoredBox(black, child))
}

// newApplication is the root widget.
func newApplication(h host) m.Widget {
	return m.NewMaterialApp(m.MaterialAppOpts{
		Title:                      "Desktop Mirror",
		Theme:                      m.NewThemeData(m.ThemeDataOpts{ColorSchemeSeed: m.Some(m.Colors.Blue.Primary)}),
		DebugShowCheckedModeBanner: m.Some(false),
		Home:                       newHomePage(h),
	})
}
