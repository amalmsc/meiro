//go:build darwin && !ios

package video

import (
	"fmt"
	"log"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// OpenGL is deprecated by Apple, but remains libmpv's supported embedded GPU
// render API on macOS. The rest of MyGo still uses its own Metal renderer.
// AppKit owns placement on the main thread; the render worker owns GL calls.
type videoSurface struct {
	view, parent, gl objc.ID
	context          uintptr
	library          uintptr
	makeCurrent      func(uintptr) int32
	lock             func(uintptr) int32
	unlock           func(uintptr) int32
	flush            func(uintptr) int32
	getString        func(uint32) string
	size             atomic.Uint64
	frame            nativeRect // main thread only
	lastSize         uint64     // render thread only
}

// NSRect's ABI is four CGFloat values on supported macOS architectures.
type nativeRect struct{ X, Y, W, H float64 }

func newVideoSurface(win *mygo.Window) (*videoSurface, error) {
	lib, err := purego.Dlopen("/System/Library/Frameworks/OpenGL.framework/OpenGL", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("load OpenGL: %w", err)
	}
	s := &videoSurface{library: lib}
	for name, fn := range map[string]any{
		"CGLSetCurrentContext": &s.makeCurrent,
		"CGLLockContext":       &s.lock,
		"CGLUnlockContext":     &s.unlock,
		"CGLFlushDrawable":     &s.flush,
		"glGetString":          &s.getString,
	} {
		purego.RegisterLibFunc(fn, lib, name)
	}
	// Accelerated, no software fallback, double-buffered, 3.2 core profile.
	attributes := []uint32{73, 72, 5, 8, 24, 11, 8, 99, 0x3200, 0}
	format := objc.ID(objc.GetClass("NSOpenGLPixelFormat")).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("initWithAttributes:"), &attributes[0])
	if format == 0 {
		return nil, fmt.Errorf("no accelerated OpenGL pixel format available")
	}
	defer format.Send(objc.RegisterName("release"))
	s.view = objc.ID(objc.GetClass("NSOpenGLView")).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("initWithFrame:pixelFormat:"), nativeRect{W: 1, H: 1}, format)
	if s.view == 0 {
		return nil, fmt.Errorf("cannot create native OpenGL view")
	}
	s.view.Send(objc.RegisterName("setWantsBestResolutionOpenGLSurface:"), true)
	s.gl = s.view.Send(objc.RegisterName("openGLContext"))
	if s.gl == 0 {
		s.view.Send(objc.RegisterName("release"))
		return nil, fmt.Errorf("cannot create OpenGL context")
	}
	s.context = objc.Send[uintptr](s.gl, objc.RegisterName("CGLContextObj"))
	s.parent = objc.ID(win.NativeHandle()).Send(objc.RegisterName("contentView"))
	s.parent.Send(objc.RegisterName("addSubview:"), s.view)
	return s, nil
}

// layout takes only a copied rectangle, never a retained UI Context/Element.
// NSView uses bottom-left coordinates unless the parent is flipped.
func (s *videoSurface) layout(bounds ui.Rect) {
	if bounds.W <= 0 || bounds.H <= 0 {
		return
	}
	frame := nativeRect{float64(bounds.X), float64(bounds.Y), float64(bounds.W), float64(bounds.H)}
	if !objc.Send[bool](s.parent, objc.RegisterName("isFlipped")) {
		parentBounds := objc.Send[nativeRect](s.parent, objc.RegisterName("bounds"))
		frame.Y = parentBounds.H - frame.Y - frame.H
	}
	s.lock(s.context)
	defer s.unlock(s.context)
	if frame != s.frame {
		s.view.Send(objc.RegisterName("setFrame:"), frame)
		s.frame = frame
	}
	// Update even at the same DIP size: moving between Retina/non-Retina
	// screens changes the drawable's backing dimensions without a resize.
	s.gl.Send(objc.RegisterName("update"))
	backing := objc.Send[nativeRect](s.view, objc.RegisterName("convertRectToBacking:"), nativeRect{W: frame.W, H: frame.H})
	s.size.Store(uint64(uint32(backing.W))<<32 | uint64(uint32(backing.H)))
}

func (s *videoSurface) initRenderer(m *mpv) error {
	// mpv_opengl_init_params: get_proc_address callback and its context.
	// Resolve directly through dlsym, whose C signature matches the callback.
	// This avoids a Go callback and its lifetime/foreign-thread constraints.
	dlsym, err := purego.Dlsym(purego.RTLD_DEFAULT, "dlsym")
	if err != nil {
		return err
	}
	init := [2]uintptr{dlsym, s.library}
	api := []byte("opengl\x00")
	params := []renderParam{
		{1, unsafe.Pointer(&api[0])},
		{2, unsafe.Pointer(&init[0])},
		{},
	}
	s.lock(s.context)
	defer s.unlock(s.context)
	if err := m.check(m.createRender(&m.view, m.handle, &params[0])); err != nil {
		return fmt.Errorf("initialize GPU video renderer: %w", err)
	}
	log.Printf("video GPU renderer: %s; OpenGL %s", s.getString(0x1F01), s.getString(0x1F02))
	return nil
}

func (s *videoSurface) render(m *mpv) error {
	// Serialize drawable updates with rendering, not with normal mpv APIs.
	s.lock(s.context)
	defer s.unlock(s.context)
	size := s.size.Load()
	if size == 0 {
		return nil
	}
	if m.updateRender(m.view)&1 == 0 && size == s.lastSize {
		return nil
	}
	s.lastSize = size
	fbo := [4]int32{0, int32(size >> 32), int32(uint32(size)), 0}
	flip := int32(1)
	params := []renderParam{
		{3, unsafe.Pointer(&fbo[0])}, // MPV_RENDER_PARAM_OPENGL_FBO
		{4, unsafe.Pointer(&flip)},   // MPV_RENDER_PARAM_FLIP_Y
		{},
	}
	if err := m.check(m.render(m.view, &params[0])); err != nil {
		return err
	}
	s.flush(s.context)
	return nil
}

// Called on the main thread after the worker has freed mpv's render context.
func (s *videoSurface) close() {
	s.view.Send(objc.RegisterName("removeFromSuperview"))
	s.view.Send(objc.RegisterName("release"))
}
