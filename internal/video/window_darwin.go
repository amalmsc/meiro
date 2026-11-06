//go:build darwin && !ios

package video

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/internal/proc"
)

// These types and format numbers follow mpv/client.h and mpv/render.h.
type renderParam struct {
	kind int32
	data unsafe.Pointer
}

type event struct {
	id, err int32
	reply   uint64 // reply_userdata
	data    unsafe.Pointer
}

type mpv struct {
	create        func() uintptr
	initialize    func(uintptr) int32
	setOption     func(uintptr, string, string) int32
	command       func(uintptr, uint64, **byte) int32
	getProperty   func(uintptr, string, int32, unsafe.Pointer) int32
	waitEvent     func(uintptr, float64) *event
	hookAdd       func(uintptr, uint64, string, int32) int32
	hookContinue  func(uintptr, uint64) int32
	errorString   func(int32) string
	free          func(unsafe.Pointer)
	destroy       func(uintptr)
	createRender  func(*uintptr, uintptr, *renderParam) int32
	updateRender  func(uintptr) uint64
	render        func(uintptr, *renderParam) int32
	destroyRender func(uintptr)
	handle, view  uintptr
}

func openMPV(path, ytdlp string) (*mpv, error) {
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("load libmpv (install with brew install mpv yt-dlp): %w", err)
	}
	m := &mpv{}
	for name, fn := range map[string]any{
		"mpv_create": &m.create, "mpv_initialize": &m.initialize,
		"mpv_set_option_string": &m.setOption, "mpv_command_async": &m.command,
		"mpv_get_property": &m.getProperty, "mpv_wait_event": &m.waitEvent,
		"mpv_hook_add": &m.hookAdd, "mpv_hook_continue": &m.hookContinue,
		"mpv_free":                  &m.free,
		"mpv_error_string":          &m.errorString,
		"mpv_terminate_destroy":     &m.destroy,
		"mpv_render_context_create": &m.createRender,
		"mpv_render_context_update": &m.updateRender,
		"mpv_render_context_render": &m.render,
		"mpv_render_context_free":   &m.destroyRender,
	} {
		symbol, err := purego.Dlsym(lib, name)
		if err != nil {
			return nil, err
		}
		purego.RegisterFunc(fn, symbol)
	}
	m.handle = m.create()
	if m.handle == 0 {
		return nil, fmt.Errorf("mpv_create failed")
	}
	for _, option := range [][2]string{
		{"config", "no"},
		{"vo", "libmpv"},
		{"hwdec", "videotoolbox,videotoolbox-copy"},
		{"terminal", "no"},
		{"idle", "yes"},
		{"keep-open", "yes"},
		{"sid", "no"},
		{"script-opts", "ytdl_hook-ytdl_path=" + ytdlp},
		// Read the caption catalogue, but do not create hundreds of mpv
		// subtitle demuxers. The CC menu loads only the chosen language.
		{"ytdl-raw-options", "no-playlist=,sub-langs=-all"},
		// Prefer H.264, which older Macs can decode in hardware too. Keep
		// other formats available when YouTube offers no H.264 version.
		{"ytdl-format", "bestvideo[height<=720][vcodec^=avc1]+bestaudio/best[height<=720][vcodec^=avc1]/bestvideo[height<=720]+bestaudio/best[height<=720]"},
	} {
		if err := m.check(m.setOption(m.handle, option[0], option[1])); err != nil {
			m.destroy(m.handle)
			return nil, fmt.Errorf("set %s: %w", option[0], err)
		}
	}
	if err := m.check(m.initialize(m.handle)); err != nil {
		m.destroy(m.handle)
		return nil, err
	}
	// Run after ytdl_hook has resolved the file, but before demuxing opens it.
	if err := m.check(m.hookAdd(m.handle, 0, "on_load", 100)); err != nil {
		m.destroy(m.handle)
		return nil, err
	}
	return m, nil
}

func (m *mpv) check(code int32) error {
	if code < 0 {
		return fmt.Errorf("mpv: %s", m.errorString(code))
	}
	return nil
}

func (m *mpv) textProperty(name string) string {
	var value *byte
	if m.getProperty(m.handle, name, 1, unsafe.Pointer(&value)) < 0 {
		return ""
	}
	defer m.free(unsafe.Pointer(value))
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(value), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(value, n))
}

// mpv_command_async copies its arguments and returns without waiting for the
// render thread, so an input handler cannot stall video rendering.
func (m *mpv) send(args ...string) error {
	storage := make([][]byte, len(args))
	argv := make([]*byte, len(args)+1)
	for i, arg := range args {
		storage[i] = append([]byte(arg), 0)
		argv[i] = &storage[i][0]
	}
	var reply uint64
	if args[0] == "sub-add" {
		reply = 1
	}
	err := m.check(m.command(m.handle, reply, &argv[0]))
	runtime.KeepAlive(storage)
	return err
}

type captionTrack struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Language string `json:"lang"`
	Selected bool   `json:"selected"`
}

type captionSource struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Ext  string `json:"ext"`
}

type captionChoice struct {
	label, language, url string
}

type resolvedVideo struct {
	Title            string  `json:"title"`
	Channel          string  `json:"channel"`
	Uploader         string  `json:"uploader"`
	AvailableAt      float64 `json:"available_at"`
	RequestedFormats []struct {
		AvailableAt float64 `json:"available_at"`
	} `json:"requested_formats"`
	Subtitles map[string][]captionSource `json:"subtitles"`
	Automatic map[string][]captionSource `json:"automatic_captions"`
}

func (r resolvedVideo) captions() []captionChoice {
	var choices []captionChoice
	for _, group := range []struct {
		tracks map[string][]captionSource
		auto   bool
	}{{r.Subtitles, false}, {r.Automatic, true}} {
		start := len(choices)
		for lang, sources := range group.tracks {
			if lang == "live_chat" {
				continue
			}
			if group.auto {
				// yt-dlp includes the original automatic language twice.
				if _, exists := group.tracks[lang+"-orig"]; exists {
					continue
				}
				if _, uploaded := r.Subtitles[strings.TrimSuffix(lang, "-orig")]; uploaded {
					continue
				}
			}
			for _, source := range sources {
				if source.Ext != "vtt" || !strings.HasPrefix(source.URL, "https://") {
					continue
				}
				name := source.Name
				if name == "" {
					name = lang
				}
				if group.auto {
					name += " (automatic)"
				}
				choices = append(choices, captionChoice{name + " · " + lang, lang, source.URL})
				break
			}
		}
		// Uploaded captions come first, with automatic choices after them.
		sort.Slice(choices[start:], func(i, j int) bool { return choices[start+i].label < choices[start+j].label })
	}
	return choices
}

func (t captionTrack) label() string {
	if t.Title != "" && t.Language != "" {
		return t.Title + " · " + t.Language
	}
	if t.Title != "" {
		return t.Title
	}
	if t.Language != "" {
		return t.Language
	}
	return fmt.Sprintf("Subtitle %d", t.ID)
}

type window struct {
	win            *mygo.Window
	player         *mpv
	surface        *videoSurface
	videoSlot      ui.Handle
	videoBounds    ui.Rect
	ctx            context.Context
	cancel         context.CancelFunc
	workers        sync.WaitGroup
	onPlay         func()
	onProgress     func(Playback)
	playback       Playback
	loaded         bool
	started        bool
	resumeChecked  bool
	input          string
	initialPause   bool
	position       float64
	duration       float64
	volume         float64
	paused         bool
	status         string
	decoder        string
	captionTracks  []captionTrack
	captionChoices []captionChoice
	captionsReady  bool
}

func (a *window) run(fn func()) {
	a.workers.Go(fn)
}

func (a *window) send(args ...string) {
	if a.player == nil || a.ctx.Err() != nil {
		return
	}
	if err := a.player.send(args...); err != nil {
		a.status = err.Error()
	}
}

func (a *window) view(c *ui.Context) {
	ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
		ui.Text(c, "Meiro · YouTube video").FontSize(20)
		ui.Box(c).Key("video").Bind(&a.videoSlot).Grow(1).MinHeight(0).FillWidth()
		a.videoBounds = a.videoSlot.Bounds(c)
		ui.Text(c, a.status)
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			label := "Pause"
			if a.paused {
				label = "Play"
			}
			ui.Button(c, label).Disabled(a.player == nil).OnClick(func() {
				if a.paused {
					a.initialPause = false
					a.onPlay()
					a.send("set", "pause", "no")
				} else {
					a.Pause()
				}
			})
			ui.Button(c, "Back 10s").Disabled(a.player == nil).OnClick(func() {
				a.playback.SeekVersion++
				a.send("seek", "-10", "relative+exact")
			})
			ui.Button(c, "Forward 10s").Disabled(a.player == nil).OnClick(func() {
				a.playback.SeekVersion++
				a.send("seek", "10", "relative+exact")
			})
			ui.Text(c, fmt.Sprintf("%.0fs / %.0fs", a.position, a.duration))
			ui.Spacer(c).Grow(1)
			ui.Button(c, "Fullscreen").OnClick(func() {
				a.win.ToggleFullScreen()
			})
			ui.Button(c, "Close").OnClick(func() { a.win.Close() })
		})
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			label := "CC: Off"
			if !a.captionsReady {
				label = "CC: Loading…"
			} else if len(a.captionChoices) == 0 && len(a.captionTracks) == 0 {
				label = "CC: Unavailable"
			}
			selected := int64(0)
			for _, track := range a.captionTracks {
				if track.Selected {
					selected = track.ID
					label = "CC: On"
				}
			}
			// Use the native menu so it can appear above the OpenGL view.
			ui.MenuButton(c, label, func(m *ui.Menu) {
				if m.Item("Off").Checked(selected == 0).Chosen() {
					a.send("set", "sid", "no")
				}
				if len(a.captionChoices) > 0 {
					for _, choice := range a.captionChoices {
						var id int64
						for _, track := range a.captionTracks {
							if track.Title == choice.label {
								id = track.ID
							}
						}
						if m.Item(choice.label).Checked(id != 0 && id == selected).Chosen() {
							if id != 0 {
								a.send("set", "sid", strconv.FormatInt(id, 10))
							} else {
								a.send("sub-add", choice.url, "select", choice.label, choice.language)
							}
						}
					}
				} else {
					for _, track := range a.captionTracks {
						if m.Item(track.label()).Checked(track.ID == selected).Chosen() {
							a.send("set", "sid", strconv.FormatInt(track.ID, 10))
						}
					}
				}
			}).Disabled(!a.captionsReady || len(a.captionChoices)+len(a.captionTracks) == 0)
			ui.Text(c, "Volume")
			ui.Slider(c, &a.volume, 0, 100).Width(180).Label("Volume").OnChange(func() {
				a.send("set", "volume", strconv.FormatFloat(a.volume, 'f', 0, 64))
			})
			ui.Text(c, "Experimental · OpenGL GPU · "+a.decoder)
		})
	})
}

func (a *window) start() {
	// All render API calls share one OpenGL context on one locked OS thread.
	// Loading starts only after the renderer is ready. Neither rendering nor
	// the regular mpv API worker waits for the UI thread.
	a.run(func() {
		ytdlp, err := proc.Tool("yt-dlp")
		if err != nil {
			a.win.Update(func() { a.status = "Install the video helpers with brew install mpv yt-dlp." })
			return
		}
		library := "/opt/homebrew/lib/libmpv.dylib"
		if runtime.GOARCH == "amd64" {
			library = "/usr/local/lib/libmpv.dylib"
		}
		m, err := openMPV(library, ytdlp)
		if err != nil {
			a.win.Update(func() { a.status = err.Error() })
			return
		}
		defer m.destroy(m.handle)
		if a.ctx.Err() != nil {
			return
		}
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		a.surface.makeCurrent(a.surface.context)
		defer a.surface.makeCurrent(0)
		if err := a.surface.initRenderer(m); err != nil {
			a.win.Update(func() { a.status = err.Error() })
			return
		}
		defer m.destroyRender(m.view)
		// Publishing and starting playback happen together on the main thread.
		// A music Play received during initialization can hold this video first.
		a.win.Update(func() {
			if a.ctx.Err() != nil {
				return
			}
			a.player = m
			a.send("set", "volume", strconv.FormatFloat(a.volume, 'f', 0, 64))
			a.send("set", "start", strconv.FormatFloat(a.playback.Position.Seconds(), 'f', 3, 64))
			if a.initialPause {
				a.send("set", "pause", "yes")
			}
			a.send("loadfile", a.input)
		})
		polled := make(chan struct{})
		a.run(func() {
			defer close(polled)
			a.poll(m)
		})
		// No regular mpv API call may outlive the client or render context.
		defer func() { a.cancel(); <-polled }()
		ticker := time.NewTicker(time.Second / 60)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
			}
			if err := a.surface.render(m); err != nil {
				log.Print(err)
			}
		}
	})
}

func (a *window) poll(m *mpv) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
		input := m.textProperty("path")
		var position, duration float64
		var paused int32
		m.getProperty(m.handle, "time-pos", 5, unsafe.Pointer(&position))
		m.getProperty(m.handle, "duration", 5, unsafe.Pointer(&duration))
		m.getProperty(m.handle, "pause", 3, unsafe.Pointer(&paused))
		var ended int32
		m.getProperty(m.handle, "eof-reached", 3, unsafe.Pointer(&ended))
		codec := m.textProperty("video-format")
		title := m.textProperty("media-title")
		var tracks []captionTrack
		if json.Unmarshal([]byte(m.textProperty("track-list")), &tracks) == nil {
			captions := tracks[:0]
			for _, track := range tracks {
				if track.Type == "sub" {
					captions = append(captions, track)
				}
			}
			tracks = captions
		}
		hwdec := m.textProperty("hwdec-current")
		decoder := "Waiting for video"
		if codec != "" {
			decoder = "CPU (" + codec + ")"
			if hwdec != "" && hwdec != "no" {
				decoder = hwdec + " (" + codec + ")"
			}
		}
		status := ""
		failed := false
		loaded := false
		for e := m.waitEvent(m.handle, 0); e.id != 0; e = m.waitEvent(m.handle, 0) {
			if e.id == 25 && e.data != nil { // MPV_EVENT_HOOK
				hook := (*struct {
					name uintptr
					id   uint64
				})(e.data)
				id := hook.id
				// YouTube can issue URLs that are not valid yet. yt-dlp waits
				// until available_at when downloading; mpv's hook does not.
				var resolved resolvedVideo
				raw := m.textProperty("user-data/mpv/ytdl/json-subprocess-result/stdout")
				// user-data subproperties are returned as JSON, including the
				// quoting around this string containing yt-dlp's JSON output.
				var stdout string
				if json.Unmarshal([]byte(raw), &stdout) == nil && json.Unmarshal([]byte(stdout), &resolved) == nil {
					choices := resolved.captions()
					hookInput := m.textProperty("path")
					a.win.Update(func() {
						if a.ctx.Err() == nil && a.input == hookInput {
							a.captionChoices = choices
							a.playback.Title = resolved.Title
							a.playback.Channel = resolved.Channel
							if a.playback.Channel == "" {
								a.playback.Channel = resolved.Uploader
							}
						}
					})
					at := resolved.AvailableAt
					for _, format := range resolved.RequestedFormats {
						at = max(at, format.AvailableAt)
					}
					if delay := time.Until(time.Unix(int64(at), 0)); delay > 0 {
						timer := time.NewTimer(delay)
						select {
						case <-timer.C:
						case <-a.ctx.Done():
						}
						timer.Stop()
					}
				}
				m.hookContinue(m.handle, id)
			}
			if e.err < 0 {
				status = m.errorString(e.err)
				if e.reply == 1 {
					status = "Cannot load captions. Try another language or turn CC off."
				}
				failed = true
			}
			if e.id == 5 && e.reply == 1 && e.err >= 0 { // MPV_EVENT_COMMAND_REPLY
				status = "Captions loaded"
			}
			if e.id == 7 && e.data != nil { // MPV_EVENT_END_FILE
				end := (*struct{ reason, err int32 })(e.data)
				if end.err < 0 {
					status = "Cannot play video: " + m.errorString(end.err)
					failed = true
				}
			}
			if e.id == 8 { // MPV_EVENT_FILE_LOADED
				loaded = true
				status = "Playing: " + input
				log.Print("video loaded: ", input)
			}
		}
		if status != "" {
			log.Print(status)
		}
		a.win.Update(func() {
			if a.ctx.Err() != nil {
				return
			}
			// Failed loads can leave mpv's path pointing to the old file.
			// Do not hide that error while waiting for the requested path.
			if failed {
				a.status = status
			}
			if input != a.input {
				return
			}
			a.position, a.duration, a.paused = position, duration, paused != 0 || a.initialPause
			a.loaded = a.loaded || loaded
			if a.loaded && !a.resumeChecked && duration > 0 {
				a.resumeChecked = true
				if a.playback.Position > 0 && a.playback.Position.Seconds() >= duration-2 {
					a.playback.Position = 0
					a.playback.SeekVersion++
					a.send("seek", "0", "absolute+exact")
					return
				}
			}
			if a.loaded && !a.started && !a.paused && !a.initialPause && position > 0 && codec != "" {
				a.started = true
				if a.playback.Title == "" {
					a.playback.Title = title
				}
			}
			if a.loaded {
				a.playback.Position = time.Duration(position * float64(time.Second))
				a.playback.Duration = time.Duration(duration * float64(time.Second))
				a.playback.Paused, a.playback.Started, a.playback.Ended = a.paused, a.started, ended != 0
				a.publishProgress()
			}
			a.captionTracks, a.captionsReady = tracks, codec != ""
			if decoder != a.decoder {
				a.decoder = decoder
				log.Print("video decoder: ", decoder)
			}
			a.surface.layout(a.videoBounds)
			if status != "" {
				a.status = status
			}
		})
	}
}

func init() { Open = openWindow }

// openWindow creates a native video window on the main thread. libmpv initialization
// and playback run in the background. All callbacks run on the main thread.
func openWindow(input Playback, onPlay func(), onProgress func(Playback), onClosed func()) (Window, error) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &window{ctx: ctx, cancel: cancel, onPlay: onPlay, onProgress: onProgress, playback: input, input: input.URL, initialPause: input.Paused, paused: input.Paused, position: input.Position.Seconds(), volume: 100, status: "Loading video…", decoder: "Waiting for video"}
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title: "Meiro · YouTube video", Width: 1000, Height: 700,
		MinWidth: 800, MinHeight: 500, Content: ui.View(a.view),
	})
	var err error
	a.surface, err = newVideoSurface(a.win)
	if err != nil {
		cancel()
		a.win.Close()
		return nil, err
	}
	a.win.OnClosed(func() { a.publishProgress(); cancel(); onClosed() })
	a.Focus()
	a.start()
	return a, nil
}

func (a *window) Load(input Playback) bool {
	if a.ctx.Err() != nil {
		return false
	}
	a.input, a.initialPause, a.paused = input.URL, input.Paused, input.Paused
	a.playback, a.loaded, a.started = input, false, false
	a.resumeChecked = false
	a.position, a.duration = input.Position.Seconds(), 0
	a.status, a.decoder = "Loading video…", "Waiting for video"
	a.captionTracks, a.captionsReady = nil, false
	a.captionChoices = nil
	a.send("set", "sid", "no")
	a.send("set", "start", strconv.FormatFloat(input.Position.Seconds(), 'f', 3, 64))
	a.send("loadfile", input.URL)
	paused := "no"
	if input.Paused {
		paused = "yes"
	}
	a.send("set", "pause", paused)
	return true
}

func (a *window) publishProgress() {
	if a.loaded && a.onProgress != nil {
		a.onProgress(a.playback)
	}
}

func (a *window) Pause() {
	a.initialPause = true
	a.paused, a.playback.Paused = true, true
	a.send("set", "pause", "yes")
	a.publishProgress()
}

func (a *window) Focus() { mygo.App.Focus(); a.win.Focus() }

func (a *window) Close() { a.cancel(); a.win.Close() }

func (a *window) Wait() { a.workers.Wait() }

func (a *window) Release() {
	a.workers.Wait()
	if a.surface != nil {
		a.surface.close()
		a.surface = nil
	}
}
