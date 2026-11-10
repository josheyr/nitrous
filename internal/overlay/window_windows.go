package overlay

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Overlay is a running overlay window. Set is safe to call from any goroutine.
type Overlay struct {
	pid uint32

	mu        sync.Mutex
	scene     Scene
	dirty     atomic.Bool // window moved or resized
	fullPaint atomic.Bool // next paint must redraw everything

	hwnd atomic.Uintptr // replaced if the game destroys its window; see adopt

	// owned by the GUI thread
	hinst      uintptr
	target     uintptr // the game window
	lastSearch time.Time
	shown      bool
	x, y, w, h int32
	keyBrush   uintptr

	memDC, memBmp, oldBmp uintptr // persistent back buffer
	bufW, bufH            int32
	worldBounds           rect // area covered by last frame's boxes (empty if none)
}

const windowClass = "NitrousOverlay"

// Only one overlay per process; the window procedure finds it here.
var (
	current     *Overlay
	wndProcCB   = windows.NewCallback(wndProc)
	enumCB      = windows.NewCallback(enumProc)
	enumResults []uintptr
	enumPID     uint32
)

// Start creates the overlay window on a dedicated OS thread and returns once
// it exists. It follows the top-level window of process pid.
func Start(pid uint32) (*Overlay, error) {
	if current != nil {
		return nil, errors.New("overlay already running")
	}
	o := &Overlay{pid: pid}
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // window + message loop must share a thread
		err := o.create()
		errc <- err
		if err == nil {
			o.loop()
		}
	}()
	if err := <-errc; err != nil {
		return nil, err
	}
	return o, nil
}

// Set replaces what the overlay shows. An empty Scene hides everything.
func (o *Overlay) Set(s Scene) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.scene.equal(s) {
		return
	}
	o.scene = s.clone()
	// Repaint now rather than on the next timer tick. InvalidateRect may be
	// called from any thread; it just queues a WM_PAINT.
	call(pInvalidateRect, o.hwnd.Load(), 0, 0)
}

// WaitFrame blocks until the desktop compositor has presented its next frame,
// i.e. it paces the caller to the display's refresh rate. Sampling the game
// right after it returns, then painting, gets the result on screen at the next
// refresh with the same latency every frame — evenly spaced updates are what
// make motion look smooth. Returns false if DWM isn't available.
func WaitFrame() bool {
	if pDwmFlush.Find() != nil {
		return false
	}
	return call(pDwmFlush) == 0 // S_OK
}

func (o *Overlay) create() error {
	// Minecraft (GLFW) is per-monitor DPI aware; match it so that window
	// coordinates aren't scaled differently between the two processes.
	if pSetProcessDpiAwarenessContext.Find() == nil {
		call(pSetProcessDpiAwarenessContext, dpiPerMonitorV2)
	} else if pSetProcessDPIAware.Find() == nil {
		call(pSetProcessDPIAware)
	}

	current = o
	o.hinst = call(pGetModuleHandleW, 0)
	o.keyBrush = call(pCreateSolidBrush, uintptr(colorKey))
	className := utf16(windowClass)
	defer runtime.KeepAlive(className)
	wc := wndClassEx{
		lpfnWndProc:   wndProcCB,
		hInstance:     o.hinst,
		hbrBackground: o.keyBrush,
		lpszClassName: className,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if call(pRegisterClassExW, ptr(&wc)) == 0 {
		return fmt.Errorf("RegisterClassEx failed: %v", windows.GetLastError())
	}
	return o.createWindow()
}

// createWindow makes the (initially hidden, ownerless) overlay window.
func (o *Overlay) createWindow() error {
	className, title := utf16(windowClass), utf16("Nitrous overlay")
	defer runtime.KeepAlive(className)
	defer runtime.KeepAlive(title)
	hwnd := call(pCreateWindowExW,
		wsExLayered|wsExTransparent|wsExTopmost|wsExToolWindow|wsExNoActivate,
		ptr(className), ptr(title),
		wsPopup, 0, 0, 1, 1, 0, 0, o.hinst, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx failed: %v", windows.GetLastError())
	}
	call(pSetLayeredWindowAttributes, hwnd, uintptr(colorKey), 0, lwaColorKey)
	call(pSetTimer, hwnd, 1, tickInterval, 0)
	o.hwnd.Store(hwnd)
	o.target, o.shown = 0, false
	return nil
}

// adopt makes the overlay an owned window of the game window t. Windows keeps
// an owned window above its owner at all times, including when the owner is a
// topmost fullscreen window, so the overlay stays visible over fullscreen
// Minecraft without any special privileges.
//
// The price: an owned window is destroyed together with its owner. Clients
// that recreate their window (LWJGL 2 does when toggling fullscreen) take the
// overlay with them; wndProc then makes a new one.
func (o *Overlay) adopt(t uintptr) {
	call(pSetWindowLongPtrW, o.hwnd.Load(), gwlpHwndParent, t)
}

func (o *Overlay) loop() {
	var m msg
	for {
		if r := int32(call(pGetMessageW, ptr(&m), 0, 0, 0)); r == 0 || r == -1 {
			return
		}
		call(pTranslateMessage, ptr(&m))
		call(pDispatchMessageW, ptr(&m))
	}
}

func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmTimer:
		current.tick()
		return 0
	case wmPaint:
		current.paint(hwnd)
		return 0
	case wmEraseBkgnd:
		return 1
	case wmNcHitTest:
		return htTransparent
	case wmDestroy:
		if o := current; hwnd == o.hwnd.Load() { // our owner went away and took us along
			o.hwnd.Store(0)
			o.createWindow()
		}
		return 0
	}
	return call(pDefWindowProcW, hwnd, message, wParam, lParam)
}

// ---------------------------------------------------------------- tracking the game window

func enumProc(hwnd, _ uintptr) uintptr {
	var pid uint32
	call(pGetWindowThreadProcessId, hwnd, ptr(&pid))
	if pid == enumPID && call(pIsWindowVisible, hwnd) != 0 && call(pGetWindow, hwnd, gwOwner) == 0 {
		enumResults = append(enumResults, hwnd)
	}
	return 1
}

// findTarget returns Minecraft's main window: the GLFW/LWJGL one if there is
// one, otherwise the largest.
func (o *Overlay) findTarget() uintptr {
	if o.target != 0 && call(pIsWindow, o.target) != 0 {
		return o.target
	}
	o.target = 0
	if time.Since(o.lastSearch) < targetRetryPeriod {
		return 0
	}
	o.lastSearch = time.Now()
	enumResults, enumPID = enumResults[:0], o.pid
	call(pEnumWindows, enumCB, 0)
	var bestArea int64
	for _, h := range enumResults {
		var buf [64]uint16
		n := call(pGetClassNameW, h, ptr(&buf[0]), uintptr(len(buf)))
		if class := windows.UTF16ToString(buf[:n]); strings.HasPrefix(class, "GLFW") || strings.HasPrefix(class, "LWJGL") {
			o.target = h
			break
		}
		var r rect
		call(pGetClientRect, h, ptr(&r))
		if a := int64(r.right) * int64(r.bottom); a > bestArea {
			o.target, bestArea = h, a
		}
	}
	if o.target != 0 {
		o.adopt(o.target)
	}
	return o.target
}

func (o *Overlay) hide() {
	if o.shown {
		call(pShowWindow, o.hwnd.Load(), swHide)
		o.shown = false
	}
}

// tick keeps the overlay exactly over the game's client area, and on top.
func (o *Overlay) tick() {
	t := o.findTarget()
	if t == 0 || call(pIsIconic, t) != 0 || call(pGetForegroundWindow) != t {
		o.hide() // only draw while the game has focus
		return
	}
	var cr rect
	call(pGetClientRect, t, ptr(&cr))
	var pt point
	call(pClientToScreen, t, ptr(&pt))
	x, y, w, h := pt.x, pt.y, cr.right, cr.bottom
	if w <= 0 || h <= 0 {
		o.hide()
		return
	}
	if !o.shown || x != o.x || y != o.y || w != o.w || h != o.h {
		o.x, o.y, o.w, o.h = x, y, w, h
		call(pSetWindowPos, o.hwnd.Load(), hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpNoActivate|swpShowWindow)
		o.shown = true
		o.dirty.Store(true)
	} else if o.below(t) {
		// Belt and braces: ownership (adopt) already keeps us above the game,
		// but if it ever ends up above us anyway, put the overlay back on top.
		call(pSetWindowPos, o.hwnd.Load(), hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	}
	if o.dirty.Swap(false) {
		o.fullPaint.Store(true)
		call(pInvalidateRect, o.hwnd.Load(), 0, 0)
	}
}

// below reports whether window t is above the overlay in the z-order.
func (o *Overlay) below(t uintptr) bool {
	for h := call(pGetWindow, o.hwnd.Load(), gwHwndPrev); h != 0; h = call(pGetWindow, h, gwHwndPrev) {
		if h == t {
			return true
		}
	}
	return false
}
