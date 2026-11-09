package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"time"

	"github.com/elianiva/meiro/m3"
)

func (a *app) openFloatingLyrics() {
	if !a.nowPlaying.floatingOpen {
		a.nowPlaying.floatingTab = tabLyrics
		a.nowPlaying.floatingFollow = lyricsFollow{followed: -1}
		a.nowPlaying.floatingScroll = ui.ScrollState{}
		a.nowPlaying.floatingScrubbing = false
	}
	a.nowPlaying.floatingOpen = true
	a.loadPanel()
	if a.desktop == nil {
		return
	}
	a.nowPlaying.floatingIgnore = false
	d := a.desktop
	if w := d.floatingLyrics.Load(); w != nil {
		w.Show()
		w.Focus()
		return
	}
	// No Parent: hiding the main player must not hide the lyrics too.
	w := mygo.NewWindow(mygo.WindowOptions{
		Title: "Meiro Floating Player", Width: 480, Height: 480,
		MinWidth: 320, MinHeight: 320, Panel: true, AlwaysOnTop: true,
		TitleBarStyle: mygo.TitleBarHidden,
		Transparent:   true, Vibrancy: mygo.VibrancyGlassClear,
		Content: ui.View(a.floatingLyricsView),
	})
	// A nonactivating panel otherwise becomes frosted over another app.
	w.SetActiveAppearance(true)
	d.floatingLyrics.Store(w)
	a.followFloatingPointer(w)
	a.setFloatingControls(a.nowPlaying.floatingControls)
	w.OnClosed(func() {
		d.floatingLyrics.CompareAndSwap(w, nil)
		a.nowPlaying.floatingOpen = false
		d.invalidate()
	})
}

func (a *app) closeFloatingLyrics() {
	a.nowPlaying.floatingOpen = false
	if a.desktop != nil {
		a.desktop.closeFloatingLyrics()
	}
}

func (d *desktop) closeFloatingLyrics() {
	if w := d.floatingLyrics.Load(); w != nil {
		w.Close()
	}
}

func (a *app) setFloatingControls(open bool) {
	n := &a.nowPlaying
	n.floatingControls, n.floatingScrubbing = open, false
	n.floatingFollow = lyricsFollow{followed: -1}
	if !open && n.speedMenu == "floating-speed" {
		n.speedMenu = ""
	}
	if a.desktop != nil {
		if w := a.desktop.floatingLyrics.Load(); w != nil {
			height := 320
			if open {
				height = 480
			}
			w.SetMinimumSize(320, height)
			if bounds := w.Bounds(); bounds.Height < height {
				w.SetSize(bounds.Width, height)
			}
		}
	}
}

func (a *app) floatingLyricsView(c *ui.Context) {
	a.followArtwork()
	m3.Provide(c, a.resolveTheme(c))
	sc := m3.Of(c).Scheme
	surface := panelSurface(sc)
	if c.Vibrancy() {
		// Leave the native material visible without fading the lyrics or controls.
		c.Root().Background(ui.Transparent)
		surface = ui.Transparent
	}
	n := &a.nowPlaying
	n.floatingHit = n.floatingHit[:0]
	bar := c.TitleBar()
	if bar.Height > 0 {
		// The native title bar is part of the clear-glass window, so it cannot
		// stay opaque on its own. This bar replaces it and remains solid.
		titleBar := ui.Row(c.Key("floating-titlebar")).Absolute().Top(0).Left(0).Right(0).Height(bar.Height).
			Padding(0, bar.Left+8, 0, 12).AlignItems(ui.Center).Background(sc.SurfaceContainer).DragWindow()
		titleBar.Children(func() {
			m3.EmphasizedText(c, m3.LabelLarge, "Floating lyrics").TextColor(sc.OnSurface)
		})
		a.markFloatingHit(titleBar)
	}
	root := ui.Column(c.Key("floating-lyrics")).Fill().Padding(bar.Height, 0, 0).Background(surface)
	root.Children(func() {
		ui.Row(c.Key("header")).FillWidth().Shrink(0).Padding(8, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			if n.floatingControls {
				a.art(c, a.current.Thumbnail, playerArt, 48, m3.Large)
			}
			ui.Column(c.Key("track")).Grow(1).MinWidth(0).Children(func() {
				if n.floatingControls && a.current.VideoID != "" {
					a.trackLine(c, m3.TitleSmall, m3.BodySmall, false)
				} else {
					m3.Text(c, m3.TitleSmall, "Floating lyrics").TextColor(sc.OnSurfaceVariant)
					m3.EmphasizedText(c, m3.TitleMedium, a.current.Title).MaxLines(1)
				}
			})
			icon, label := m3.IconExpandMore, "Show player controls"
			if n.floatingControls {
				icon, label = m3.IconExpandLess, "Hide player controls"
			}
			toggle := m3.IconButton(c, m3.IconButtonSpec{Icon: icon, Label: label, Key: "toggle-controls"})
			a.markFloatingHit(toggle)
			if toggle.Clicked() {
				a.setFloatingControls(!n.floatingControls)
			}
			closeButton := m3.IconButton(c, m3.IconButtonSpec{Icon: m3.IconClose, Label: "Close floating lyrics", Key: "close"})
			a.markFloatingHit(closeButton)
			if closeButton.Clicked() {
				a.closeFloatingLyrics()
			}
		})
		transport := ui.Column(c.Key("transport")).FillWidth().Shrink(0)
		transport.Children(func() { a.transport(c, 48, 4) })
		a.markFloatingHit(transport)
		if n.floatingControls {
			extra := ui.Column(c.Key("extra-controls")).FillWidth().Shrink(0).Padding(4, 16, 8).Gap(4)
			a.markFloatingHit(extra)
			extra.Children(func() {
				if !n.floatingScrubbing {
					n.floatingScrub = a.playbackPosition().Seconds()
				}
				a.scrubber(c, &n.floatingScrub, &n.floatingScrubbing)
				ui.Row(c.Key("volume")).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					a.muteButton(c)
					volume := a.volume
					if m3.Slider(c, &volume, 0, 100, m3.SliderSpec{Label: "Volume", Key: "volume-slider"}).Changed() {
						a.setVolume(volume)
						a.saveSettings()
					}
					a.speedButton(c, "floating-speed")
				})
			})
		}
		ui.Column(c.Key("panel")).Grow(1).MinHeight(0).Children(func() {
			a.nowPlayingTabs(c, true, &n.floatingTab)
			ui.Column(c.Key(n.floatingTab)).Grow(1).MinHeight(0).Children(func() {
				switch n.floatingTab {
				case tabQueue:
					a.queueView(c, true, &n.floatingQueueList, &n.floatingQueueFollow)
				case tabRelated:
					a.relatedView(c, &n.floatingRelatedList)
				case tabLyrics:
					switch {
					case a.current.VideoID == "":
						a.message(c, m3.IconLyrics, "Play a song to see its lyrics.", "", "", nil)
					case a.current.IsEpisode():
						a.detailsView(c)
					default:
						a.lyricsView(c, &n.floatingFollow, &n.floatingScroll, surface)
					}
				}
			})
		})
	})
	a.lyricsSourceDialog(c)
}

// followFloatingPointer lets clicks on empty glass reach the window behind
// the panel. Buttons, sliders, tabs and lyric lines keep the pointer, and the
// native title bar stays available for dragging.
func (a *app) followFloatingPointer(w *mygo.Window) {
	a.every(50*time.Millisecond, func() {
		if a.desktop == nil || a.desktop.floatingLyrics.Load() != w {
			return
		}
		point := mygo.Screen.CursorScreenPoint()
		content := w.ContentBounds()
		inside := point.X >= content.X && point.X < content.X+content.Width && point.Y >= content.Y && point.Y < content.Y+content.Height
		overControl := false
		x, y := float32(point.X-content.X), float32(point.Y-content.Y)
		for _, bounds := range a.nowPlaying.floatingHit {
			if expandHit(bounds, 8).Contains(x, y) {
				overControl = true
				break
			}
		}
		ignore := inside && !overControl
		if ignore == a.nowPlaying.floatingIgnore {
			return
		}
		a.nowPlaying.floatingIgnore = ignore
		w.SetIgnoreMouseEvents(ignore)
	})
}

func (a *app) markFloatingHit(target ui.Element) {
	if target.Valid() {
		a.nowPlaying.floatingHit = append(a.nowPlaying.floatingHit, target.LayoutBounds())
	}
}

func expandHit(bounds ui.Rect, pad float32) ui.Rect {
	return ui.Rect{X: bounds.X - pad, Y: bounds.Y - pad, W: bounds.W + 2*pad, H: bounds.H + 2*pad}
}
