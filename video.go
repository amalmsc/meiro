package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/internal/video"
	"github.com/elianiva/meiro/m3"
	"github.com/elianiva/meiro/youtube"
)

type videoDialogState struct {
	open     bool
	url, err string
}

// youtubeVideoURL accepts individual videos only. Canonicalizing also keeps
// playlists, arbitrary mpv protocols and extractor options out of playback.
func youtubeVideoURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	id := ""
	if err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" {
		switch strings.ToLower(u.Hostname()) {
		case "youtu.be":
			id = strings.TrimPrefix(u.Path, "/")
		case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com":
			if u.Path == "/watch" {
				id = u.Query().Get("v")
			} else if parts := strings.Split(u.Path, "/"); len(parts) == 3 && (parts[1] == "shorts" || parts[1] == "live" || parts[1] == "embed") {
				id = parts[2]
			}
		}
	}
	if len(id) == 11 && strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") == "" {
		return "https://www.youtube.com/watch?v=" + id, nil
	}
	return "", fmt.Errorf("paste an HTTPS YouTube video link, such as https://youtu.be/aqz-KE-bpKQ")
}

func (a *app) openVideoDialog(c *ui.Context) {
	d := a.videoDialog
	if d == nil {
		return
	}
	if !d.open {
		a.videoDialog = nil
		return
	}
	sc := m3.Of(c).Scheme
	m3.Dialog(c, &d.open, 520, func() {
		m3.EmphasizedText(c, m3.HeadlineSmall, "Open YouTube URL")
		m3.Text(c, m3.BodyMedium, "Watch a video in a separate window. Music pauses; closing the video leaves music paused.").TextColor(sc.OnSurfaceVariant)
		m3.Text(c, m3.BodySmall, "Experimental · macOS only · requires Homebrew mpv and yt-dlp (brew install mpv yt-dlp).").TextColor(sc.OnSurfaceVariant)
		ui.Column(c.Key("video-url")).Padding(12, 16).Radius(m3.Large).Background(sc.SurfaceContainerHighest).Children(func() {
			ui.TextInputBase(c.Key("video-url-field"), &d.url).
				Placeholder("https://www.youtube.com/watch?v=…").Label("YouTube URL").TextColor(sc.OnSurface).FontSize(16).AutoFocus().OnSubmit(a.watchVideo)
		})
		if d.err != "" {
			m3.Text(c, m3.BodyMedium, d.err).TextColor(sc.Error).MaxLines(4)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if m3.Button(c, m3.ButtonSpec{Label: "Cancel", Kind: m3.TextOnly, Key: "cancel-video"}).Clicked() {
				a.videoDialog = nil
			}
			if m3.Button(c, m3.ButtonSpec{Label: "Watch video", Disabled: strings.TrimSpace(d.url) == "", Key: "watch-video"}).Clicked() {
				a.watchVideo()
			}
		})
	})
}

func (a *app) watchVideo() {
	d := a.videoDialog
	if d == nil {
		return
	}
	input, err := youtubeVideoURL(d.url)
	if err == nil {
		err = a.playVideo(input)
	}
	if err != nil {
		d.err = err.Error()
		return
	}
	a.videoDialog = nil
}

func (a *app) playVideo(input string) error {
	a.videoStart.stop()
	playing := video.Playback{URL: input}
	if input == a.videoPlayback.URL {
		playing = a.videoPlayback
		playing.Paused, playing.Started, playing.Ended, playing.SeekVersion = false, false, false, 0
		if playing.Duration > 0 && playing.Position >= playing.Duration-restoreEndGuard {
			playing.Position = 0
		}
	} else if a.signedIn() {
		client := a.client()
		startTask(a, &a.videoStart, episodeProgressTimeout, func(ctx context.Context) (time.Duration, error) {
			return client.VideoProgress(ctx, strings.TrimPrefix(input, "https://www.youtube.com/watch?v="))
		}, func(position time.Duration, err error) {
			if a.client() != client {
				return
			}
			if err != nil {
				log.Printf("video: could not read account progress: %v", err)
			} else {
				playing.Position = position
			}
			if err := a.openVideoPlayback(playing); err != nil {
				a.notice = err.Error()
			}
		})
		return nil
	}
	return a.openVideoPlayback(playing)
}

// restoreVideo reopens only a window left open at exit, paused at its local
// position. It does not report playback until the user presses Play.
func (a *app) restoreVideo() {
	if !a.videoReopen || a.videoPlayback.URL == "" || video.Open == nil && a.openVideo == nil {
		return
	}
	if err := a.openVideoPlayback(a.videoPlayback); err != nil {
		a.notice = err.Error()
	}
}

func (a *app) openVideoPlayback(playing video.Playback) error {
	if a.videoWindow != nil {
		if !a.videoWindow.Load(playing) {
			return fmt.Errorf("the video window is closing; try again in a moment")
		}
		a.finishVideoProgress(false)
		a.videoPlayback, a.videoReopen = playing, true
		if !playing.Paused {
			a.useVideo()
		}
		a.videoWindow.Focus()
		return nil
	}
	open := a.openVideo
	if open == nil {
		open = video.Open
	}
	if open == nil {
		return fmt.Errorf("video playback is available on macOS only")
	}
	var win video.Window
	var err error
	win, err = open(playing, a.useVideo, func(progress video.Playback) {
		if a.videoWindow == win && !a.videoShutdown && progress.URL == a.videoPlayback.URL {
			a.videoAdvanced(progress)
		}
	}, func() {
		if a.videoWindow == win && !a.videoShutdown {
			a.videoReopen = false
			a.finishVideoProgress(false)
			a.savePlayback(0)
		}
		// Keep ownership until native resources are released, including when
		// the app exits before this result can be delivered to its main window.
		a.run(func() {
			win.Wait()
			a.update(func() {
				win.Release()
				if a.videoWindow == win {
					a.videoWindow = nil
				}
			})
		})
	})
	if err != nil {
		return err
	}
	a.videoWindow = win
	a.videoPlayback, a.videoReopen = playing, true
	if !playing.Paused {
		a.useVideo()
	}
	return nil
}

func (a *app) videoAdvanced(playing video.Playback) {
	first := playing.Started && !a.videoPlayback.Started
	stopped := playing.Paused && !a.videoPlayback.Paused || playing.Ended && !a.videoPlayback.Ended
	a.videoPlayback = playing
	id := strings.TrimPrefix(playing.URL, "https://www.youtube.com/watch?v=")
	title := playing.Title
	if title == "" {
		title = id
	}
	item := youtube.MusicItem{VideoID: id, Title: title, Subtitle: playing.Channel}
	if first {
		a.recordHistory(item)
	}
	if playing.Started && !playing.Paused && !playing.Ended && a.watchingVideo && a.signedIn() && a.videoProgress == nil {
		a.videoProgress = a.beginPlaybackProgress(item, playing.Position, playing.Duration, true)
		a.videoProgress.seekVersion = playing.SeekVersion
	}
	a.syncPlaybackProgress(a.videoProgress, playing.Position, playing.SeekVersion, !playing.Paused && !playing.Ended)
	if playing.Paused || playing.Ended {
		a.finishVideoProgress(false)
	}
	if first || stopped {
		a.savePlayback(0)
	}
}

func (a *app) finishVideoProgress(wait bool) {
	state := a.videoProgress
	a.videoProgress = nil
	a.finishPlaybackProgress(state, a.videoPlayback.Position, wait)
}

// prepareVideoShutdown runs before App.Quit closes its windows. A window
// closed as part of quitting must reopen next time, unlike an explicit Close.
func (a *app) prepareVideoShutdown() {
	a.videoStart.stop()
	a.videoShutdown = true
	a.finishVideoProgress(true)
	a.savePlayback(0)
}

func (a *app) useVideo() {
	a.watchingVideo = true
	a.suspendedWhilePlaying = false
	a.pause()
}

func (a *app) useMusic() {
	a.videoStart.stop()
	a.watchingVideo = false
	if a.videoWindow != nil {
		a.videoWindow.Pause()
	}
}
