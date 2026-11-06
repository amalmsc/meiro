// Package video plays video in a separate native window, independently of the
// music decoder. Its caller coordinates which player may play audio.
package video

import "time"

// Open is installed by the macOS backend. A nil value means this build has no
// video feature; no fallback player is compiled for other operating systems.
var Open func(input Playback, onPlay func(), onProgress func(Playback), onClosed func()) (Window, error)

// Playback is a main-thread snapshot of the current video. Progress is published
// after loading, including paused restores, and once more before closing.
type Playback struct {
	URL, Title, Channel    string
	Position, Duration     time.Duration
	Paused, Started, Ended bool
	SeekVersion            uint64
}

// Window belongs to the main thread, except Wait. After a close, wait off the
// main thread before Release frees the native view on the main thread. During
// application shutdown Close, Wait and Release can run sequentially.
type Window interface {
	// Load replaces the current video, returning false if the window is closing.
	Load(input Playback) bool
	Pause()
	Focus()
	Close()
	Wait()
	Release()
}
