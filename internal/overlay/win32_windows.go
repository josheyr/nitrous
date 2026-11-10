package overlay

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	pRegisterClassExW              = user32.NewProc("RegisterClassExW")
	pCreateWindowExW               = user32.NewProc("CreateWindowExW")
	pDefWindowProcW                = user32.NewProc("DefWindowProcW")
	pGetMessageW                   = user32.NewProc("GetMessageW")
	pTranslateMessage              = user32.NewProc("TranslateMessage")
	pDispatchMessageW              = user32.NewProc("DispatchMessageW")
	pSetLayeredWindowAttributes    = user32.NewProc("SetLayeredWindowAttributes")
	pShowWindow                    = user32.NewProc("ShowWindow")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pSetWindowLongPtrW             = user32.NewProc("SetWindowLongPtrW")
	pSetTimer                      = user32.NewProc("SetTimer")
	pBeginPaint                    = user32.NewProc("BeginPaint")
	pEndPaint                      = user32.NewProc("EndPaint")
	pInvalidateRect                = user32.NewProc("InvalidateRect")
	pGetClientRect                 = user32.NewProc("GetClientRect")
	pClientToScreen                = user32.NewProc("ClientToScreen")
	pIsIconic                      = user32.NewProc("IsIconic")
	pIsWindow                      = user32.NewProc("IsWindow")
	pIsWindowVisible               = user32.NewProc("IsWindowVisible")
	pGetWindow                     = user32.NewProc("GetWindow")
	pGetForegroundWindow           = user32.NewProc("GetForegroundWindow")
	pGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")
	pGetClassNameW                 = user32.NewProc("GetClassNameW")
	pEnumWindows                   = user32.NewProc("EnumWindows")
	pFillRect                      = user32.NewProc("FillRect")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")

	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	pCreatePen              = gdi32.NewProc("CreatePen")
	pMoveToEx               = gdi32.NewProc("MoveToEx")
	pLineTo                 = gdi32.NewProc("LineTo")

	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	pDwmFlush = dwmapi.NewProc("DwmFlush")
)

const (
	wsPopup         = 0x80000000
	wsExLayered     = 0x00080000
	wsExTransparent = 0x00000020
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExNoActivate  = 0x08000000

	wmDestroy    = 0x0002
	wmPaint      = 0x000F
	wmEraseBkgnd = 0x0014
	wmNcHitTest  = 0x0084
	wmTimer      = 0x0113

	swHide        = 0
	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoActivate = 0x0010
	swpShowWindow = 0x0040
	gwHwndPrev    = 3
	gwOwner       = 4

	lwaColorKey = 0x1
	srcCopy     = 0x00CC0020
	psSolid     = 0

	htTransparent   = ^uintptr(0) // -1
	hwndTopmost     = ^uintptr(0) // -1
	gwlpHwndParent  = ^uintptr(7) // -8: the owner of a top-level window
	dpiPerMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)

	tickInterval      = 33 // ms between window-tracking ticks
	targetRetryPeriod = 250 * time.Millisecond
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type point struct{ x, y int32 }

type rect struct{ left, top, right, bottom int32 }

func (r rect) empty() bool { return r.right <= r.left || r.bottom <= r.top }

// union is the smallest rect covering both a and b.
func (r rect) union(o rect) rect {
	switch {
	case r.empty():
		return o
	case o.empty():
		return r
	}
	return rect{min(r.left, o.left), min(r.top, o.top), max(r.right, o.right), max(r.bottom, o.bottom)}
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
	private uint32
}

type paintStruct struct {
	hdc         uintptr
	fErase      int32
	rcPaint     rect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

func call(p *windows.LazyProc, a ...uintptr) uintptr {
	r, _, _ := p.Call(a...)
	return r
}

func ptr[T any](v *T) uintptr { return uintptr(unsafe.Pointer(v)) }

// utf16 converts s for a Win32 call. s must not contain NUL.
func utf16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}
