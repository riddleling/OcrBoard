//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	// Hotkey: Win+Alt+Shift+T
	MOD_ALT   uint32 = 0x0001
	MOD_SHIFT uint32 = 0x0004
	MOD_WIN   uint32 = 0x0008
	VK_T      uint32 = 0x54
	HOTKEY_ID int32  = 0xBEEF

	// UI config
	selectionBorderWidth = 5
	selectionBorderColor = rgb(0, 255, 255) // cyan
	dimAlpha             = byte(46)         // ~0.18*255
)

// =========================
// Win32 / DLL procs
// =========================

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	msimg32  = windows.NewLazySystemDLL("msimg32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	ntdll    = windows.NewLazySystemDLL("ntdll.dll")

	procRegisterHotKey      = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey    = user32.NewProc("UnregisterHotKey")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procPeekMessageW        = user32.NewProc("PeekMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procSetProcessDPIAware  = user32.NewProc("SetProcessDPIAware")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procShowWindow          = user32.NewProc("ShowWindow")
	procUpdateWindow        = user32.NewProc("UpdateWindow")
	procInvalidateRect      = user32.NewProc("InvalidateRect")
	procBeginPaint          = user32.NewProc("BeginPaint")
	procEndPaint            = user32.NewProc("EndPaint")
	procSetCapture          = user32.NewProc("SetCapture")
	procReleaseCapture      = user32.NewProc("ReleaseCapture")
	procSetFocus            = user32.NewProc("SetFocus")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetWindowLongPtrW   = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW   = user32.NewProc("SetWindowLongPtrW")
	procFillRect            = user32.NewProc("FillRect")
	procOpenClipboard       = user32.NewProc("OpenClipboard")
	procCloseClipboard      = user32.NewProc("CloseClipboard")
	procEmptyClipboard      = user32.NewProc("EmptyClipboard")
	procSetClipboardData    = user32.NewProc("SetClipboardData")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	procCreatePen              = gdi32.NewProc("CreatePen")
	procMoveToEx               = gdi32.NewProc("MoveToEx")
	procLineTo                 = gdi32.NewProc("LineTo")
	procStretchDIBits          = gdi32.NewProc("StretchDIBits")

	procAlphaBlend = msimg32.NewProc("AlphaBlend")

	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procGlobalAlloc        = kernel32.NewProc("GlobalAlloc")
	procGlobalLock         = kernel32.NewProc("GlobalLock")
	procGlobalUnlock       = kernel32.NewProc("GlobalUnlock")
	procGetCurrentThreadId = kernel32.NewProc("GetCurrentThreadId")

	procRtlMoveMemory = ntdll.NewProc("RtlMoveMemory")
)

// =========================
// Win32 const / types
// =========================

const (
	WM_HOTKEY      = 0x0312
	WM_DESTROY     = 0x0002
	WM_PAINT       = 0x000F
	WM_ERASEBKGND  = 0x0014
	WM_LBUTTONDOWN = 0x0201
	WM_MOUSEMOVE   = 0x0200
	WM_LBUTTONUP   = 0x0202
	WM_KEYDOWN     = 0x0100

	VK_ESCAPE = 0x1B

	WS_POPUP         = 0x80000000
	WS_VISIBLE       = 0x10000000
	WS_EX_TOPMOST    = 0x00000008
	WS_EX_TOOLWINDOW = 0x00000080

	SW_SHOW = 5

	SM_XVIRTUALSCREEN  = 76
	SM_YVIRTUALSCREEN  = 77
	SM_CXVIRTUALSCREEN = 78
	SM_CYVIRTUALSCREEN = 79

	SRCCOPY = 0x00CC0020

	PS_SOLID = 0

	// SetWindowPos
	HWND_TOPMOST   = ^uintptr(0) // (HWND)-1
	SWP_NOSIZE     = 0x0001
	SWP_NOMOVE     = 0x0002
	SWP_SHOWWINDOW = 0x0040

	// PeekMessage
	PM_REMOVE = 0x0001

	// Clipboard
	CF_UNICODETEXT = 13
	GMEM_MOVEABLE  = 0x0002

	// WM_APP
	WM_APP = 0x8000

	// Custom message: UI done
	WM_UI_DONE = WM_APP + 1
)

type POINT struct {
	X int32
	Y int32
}

type RECT struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type PAINTSTRUCT struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     RECT
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type WNDCLASSEXW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type BITMAPINFOHEADER struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type RGBQUAD struct {
	Blue     byte
	Green    byte
	Red      byte
	Reserved byte
}

type BITMAPINFO struct {
	BmiHeader BITMAPINFOHEADER
	BmiColors [1]RGBQUAD
}

type MSG struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
}

// =========================
// Helpers
// =========================

func mustUTF16Ptr(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func setDPIAware() {
	_, _, _ = procSetProcessDPIAware.Call()
}

func getModuleHandle() uintptr {
	r, _, _ := procGetModuleHandleW.Call(0)
	return r
}

func getCurrentThreadId() uint32 {
	r, _, _ := procGetCurrentThreadId.Call()
	return uint32(r)
}

func getSystemMetrics(n int32) int32 {
	r, _, _ := procGetSystemMetrics.Call(uintptr(n))
	return int32(r)
}

func messageBoxTop(title, msg string) {
	const MB_OK = 0x00000000
	const MB_TOPMOST = 0x00040000
	const MB_SETFOREGROUND = 0x00010000

	pMsg := unsafe.Pointer(mustUTF16Ptr(msg))
	pTitle := unsafe.Pointer(mustUTF16Ptr(title))

	procMessageBoxW.Call(
		0,
		uintptr(pMsg),
		uintptr(pTitle),
		uintptr(MB_OK|MB_TOPMOST|MB_SETFOREGROUND),
	)

	runtime.KeepAlive(pMsg)
	runtime.KeepAlive(pTitle)
}

func rgb(r, g, b byte) uint32 {
	return uint32(r) | (uint32(g) << 8) | (uint32(b) << 16)
}

func rectNorm(x1, y1, x2, y2 int32) (l, t, r, b int32) {
	l, r = x1, x2
	if l > r {
		l, r = r, l
	}
	t, b = y1, y2
	if t > b {
		t, b = b, t
	}
	return
}

func rectWH(l, t, r, b int32) (w, h int32) {
	w = r - l
	h = b - t
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return
}

// =========================
// Clipboard
// =========================

func setClipboardText(s string) error {
	utf16, err := windows.UTF16FromString(s)
	if err != nil {
		return err
	}
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	nbytes := uintptr(len(utf16) * 2)
	hMem, _, _ := procGlobalAlloc.Call(GMEM_MOVEABLE, nbytes)
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}

	ptr, _, _ := procGlobalLock.Call(hMem)
	if ptr == 0 {
		return fmt.Errorf("GlobalLock failed")
	}
	defer procGlobalUnlock.Call(hMem)

	// 不用 unsafe.Slice，直接用 WinAPI copy
	srcPtr := unsafe.Pointer(&utf16[0])
	procRtlMoveMemory.Call(
		ptr,             // dest
		uintptr(srcPtr), // src
		nbytes,          // bytes
	)

	// 保守：確保 utf16 在 copy 完前存活
	runtime.KeepAlive(utf16)

	// 成功 SetClipboardData 後，hMem 所有權交給系統，不要再 free
	if r, _, _ := procSetClipboardData.Call(CF_UNICODETEXT, hMem); r == 0 {
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

// =========================
// HTTP
// =========================

func postPNGAndGetOCR(url string, pngBytes []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	fw, err := w.CreateFormFile("file", "capture.png")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, bytes.NewReader(pngBytes)); err != nil {
		return "", err
	}
	_ = w.Close()

	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Printf("[OCR] API returned: error (%.3fs)\n", elapsed.Seconds())
		return "", err
	}
	defer resp.Body.Close()

	fmt.Printf("[OCR] API returned: %d (%.3fs)\n", resp.StatusCode, elapsed.Seconds())

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 800))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if v, ok := out["ocr_result"]; ok {
		if s, ok := v.(string); ok {
			return s, nil
		}
	}
	return "", fmt.Errorf("no ocr_result in response")
}

// =========================
// Screenshot
// =========================

func captureVirtualScreenRGBA() (*image.RGBA, int32, int32, int32, int32, error) {
	vx := getSystemMetrics(SM_XVIRTUALSCREEN)
	vy := getSystemMetrics(SM_YVIRTUALSCREEN)
	vw := getSystemMetrics(SM_CXVIRTUALSCREEN)
	vh := getSystemMetrics(SM_CYVIRTUALSCREEN)

	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return nil, 0, 0, 0, 0, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, hdcScreen)

	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		return nil, 0, 0, 0, 0, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(hdcMem)

	hbm, _, _ := procCreateCompatibleBitmap.Call(hdcScreen, uintptr(vw), uintptr(vh))
	if hbm == 0 {
		return nil, 0, 0, 0, 0, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(hbm)

	old, _, _ := procSelectObject.Call(hdcMem, hbm)
	defer procSelectObject.Call(hdcMem, old)

	ok, _, _ := procBitBlt.Call(
		hdcMem,
		0, 0, uintptr(vw), uintptr(vh),
		hdcScreen,
		uintptr(vx), uintptr(vy),
		SRCCOPY,
	)
	if ok == 0 {
		return nil, 0, 0, 0, 0, fmt.Errorf("BitBlt failed")
	}

	var bi BITMAPINFO
	bi.BmiHeader.BiSize = uint32(unsafe.Sizeof(bi.BmiHeader))
	bi.BmiHeader.BiWidth = vw
	bi.BmiHeader.BiHeight = -vh
	bi.BmiHeader.BiPlanes = 1
	bi.BmiHeader.BiBitCount = 32
	bi.BmiHeader.BiCompression = 0

	buf := make([]byte, int(vw*vh*4))

	pBuf := unsafe.Pointer(&buf[0])
	pBI := unsafe.Pointer(&bi)

	r, _, _ := procGetDIBits.Call(
		hdcMem,
		hbm,
		0,
		uintptr(vh),
		uintptr(pBuf),
		uintptr(pBI),
		0,
	)

	runtime.KeepAlive(buf)
	runtime.KeepAlive(&bi)

	if r == 0 {
		return nil, 0, 0, 0, 0, fmt.Errorf("GetDIBits failed")
	}

	img := image.NewRGBA(image.Rect(0, 0, int(vw), int(vh)))
	for i := 0; i < len(buf); i += 4 {
		b := buf[i+0]
		g := buf[i+1]
		rr := buf[i+2]
		a := buf[i+3]
		img.Pix[i+0] = rr
		img.Pix[i+1] = g
		img.Pix[i+2] = b
		img.Pix[i+3] = a
	}

	return img, vx, vy, vw, vh, nil
}

func cropRGBA(img *image.RGBA, vx, vy int32, l, t, r, b int32) *image.RGBA {
	lx := int(l - vx)
	ty := int(t - vy)
	rx := int(r - vx)
	by := int(b - vy)

	if lx < 0 {
		lx = 0
	}
	if ty < 0 {
		ty = 0
	}
	if rx > img.Bounds().Dx() {
		rx = img.Bounds().Dx()
	}
	if by > img.Bounds().Dy() {
		by = img.Bounds().Dy()
	}
	if rx <= lx || by <= ty {
		return nil
	}

	dst := image.NewRGBA(image.Rect(0, 0, rx-lx, by-ty))
	for y := ty; y < by; y++ {
		srcOff := img.PixOffset(lx, y)
		dstOff := dst.PixOffset(0, y-ty)
		copy(dst.Pix[dstOff:dstOff+(rx-lx)*4], img.Pix[srcOff:srcOff+(rx-lx)*4])
	}
	return dst
}

// =========================
// Selector window (double buffering + cached alpha src)
// =========================

type selectionState struct {
	vx, vy, vw, vh int32
	img            *image.RGBA

	dragging bool
	x1, y1   int32
	x2, y2   int32

	done     bool
	canceled bool
	hwnd     uintptr

	bgra []byte

	// ===== Double buffer =====
	backDC  uintptr
	backBmp uintptr
	backOld uintptr

	// Cached 1x1 black source DC for AlphaBlend
	blackDC  uintptr
	blackBmp uintptr
	blackOld uintptr
}

// Instead of storing *selectionState in GWLP_USERDATA, store an ID and look up in a Go map.
const GWLP_USERDATA_UPTR = ^uintptr(20)

var (
	stateMu   sync.Mutex
	stateMap  = make(map[uintptr]*selectionState)
	nextState atomic.Uintptr
)

func allocStateID() uintptr {
	id := nextState.Add(1)
	if id == 0 {
		id = nextState.Add(1)
	}
	return id
}

func attachState(hwnd uintptr, st *selectionState) {
	id := allocStateID()
	st.hwnd = hwnd

	stateMu.Lock()
	stateMap[id] = st
	stateMu.Unlock()

	procSetWindowLongPtrW.Call(hwnd, GWLP_USERDATA_UPTR, id)
}

func getStateFromHwnd(hwnd uintptr) *selectionState {
	id, _, _ := procGetWindowLongPtrW.Call(hwnd, GWLP_USERDATA_UPTR)
	if id == 0 {
		return nil
	}
	stateMu.Lock()
	st := stateMap[id]
	stateMu.Unlock()
	return st
}

func detachState(hwnd uintptr) {
	id, _, _ := procGetWindowLongPtrW.Call(hwnd, GWLP_USERDATA_UPTR)
	if id == 0 {
		return
	}
	stateMu.Lock()
	delete(stateMap, id)
	stateMu.Unlock()

	procSetWindowLongPtrW.Call(hwnd, GWLP_USERDATA_UPTR, 0)
}

func packBlend(alpha byte) uintptr {
	v := uint32(0) | (uint32(0) << 8) | (uint32(alpha) << 16) | (uint32(0) << 24)
	return uintptr(v)
}

func (s *selectionState) ensureBGRA() {
	if s.bgra != nil {
		return
	}
	w := int(s.vw)
	h := int(s.vh)
	bg := make([]byte, w*h*4)
	for i := 0; i < len(bg); i += 4 {
		r := s.img.Pix[i+0]
		g := s.img.Pix[i+1]
		b := s.img.Pix[i+2]
		a := s.img.Pix[i+3]
		bg[i+0] = b
		bg[i+1] = g
		bg[i+2] = r
		bg[i+3] = a
	}
	s.bgra = bg
}

func (s *selectionState) ensureBuffers(paintHdc uintptr) {
	if s.backDC != 0 && s.blackDC != 0 {
		return
	}

	// Back buffer
	if s.backDC == 0 {
		dc, _, _ := procCreateCompatibleDC.Call(paintHdc)
		if dc != 0 {
			bmp, _, _ := procCreateCompatibleBitmap.Call(paintHdc, uintptr(s.vw), uintptr(s.vh))
			if bmp != 0 {
				old, _, _ := procSelectObject.Call(dc, bmp)
				s.backDC = dc
				s.backBmp = bmp
				s.backOld = old
			} else {
				procDeleteDC.Call(dc)
			}
		}
	}

	// 1x1 black alpha source (cached)
	if s.blackDC == 0 {
		dc, _, _ := procCreateCompatibleDC.Call(paintHdc)
		if dc != 0 {
			bmp, _, _ := procCreateCompatibleBitmap.Call(paintHdc, 1, 1)
			if bmp != 0 {
				old, _, _ := procSelectObject.Call(dc, bmp)
				s.blackDC = dc
				s.blackBmp = bmp
				s.blackOld = old

				// Fill 1x1 black
				brush, _, _ := procCreateSolidBrush.Call(uintptr(rgb(0, 0, 0)))
				if brush != 0 {
					tmp := RECT{Left: 0, Top: 0, Right: 1, Bottom: 1}
					pTmp := unsafe.Pointer(&tmp)
					procFillRect.Call(s.blackDC, uintptr(pTmp), brush)
					runtime.KeepAlive(&tmp)
					procDeleteObject.Call(brush)
				}
			} else {
				procDeleteDC.Call(dc)
			}
		}
	}
}

func (s *selectionState) freeBuffers() {
	if s.backDC != 0 {
		procSelectObject.Call(s.backDC, s.backOld)
		procDeleteObject.Call(s.backBmp)
		procDeleteDC.Call(s.backDC)
		s.backDC, s.backBmp, s.backOld = 0, 0, 0
	}
	if s.blackDC != 0 {
		procSelectObject.Call(s.blackDC, s.blackOld)
		procDeleteObject.Call(s.blackBmp)
		procDeleteDC.Call(s.blackDC)
		s.blackDC, s.blackBmp, s.blackOld = 0, 0, 0
	}
}

func alphaFillRectFromBlack1x1(dstHdc uintptr, blackSrcDc uintptr, rc RECT, alpha byte) {
	w := rc.Right - rc.Left
	h := rc.Bottom - rc.Top
	if w <= 0 || h <= 0 || blackSrcDc == 0 {
		return
	}
	procAlphaBlend.Call(
		dstHdc,
		uintptr(rc.Left), uintptr(rc.Top), uintptr(w), uintptr(h),
		blackSrcDc,
		0, 0, 1, 1,
		packBlend(alpha),
	)
}

func drawBorder(hdc uintptr, l, t, r, b int32) {
	pen, _, _ := procCreatePen.Call(PS_SOLID, uintptr(selectionBorderWidth), uintptr(selectionBorderColor))
	if pen == 0 {
		return
	}
	defer procDeleteObject.Call(pen)

	old, _, _ := procSelectObject.Call(hdc, pen)
	defer procSelectObject.Call(hdc, old)

	procMoveToEx.Call(hdc, uintptr(l), uintptr(t), 0)
	procLineTo.Call(hdc, uintptr(r), uintptr(t))
	procLineTo.Call(hdc, uintptr(r), uintptr(b))
	procLineTo.Call(hdc, uintptr(l), uintptr(b))
	procLineTo.Call(hdc, uintptr(l), uintptr(t))
}

func (s *selectionState) paint(hwnd uintptr) {
	var ps PAINTSTRUCT
	pPS := unsafe.Pointer(&ps)

	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(pPS))
	if hdc == 0 {
		return
	}
	defer func() {
		procEndPaint.Call(hwnd, uintptr(pPS))
		runtime.KeepAlive(&ps)
	}()

	s.ensureBGRA()
	s.ensureBuffers(hdc)

	dst := s.backDC
	if dst == 0 {
		dst = hdc
	}

	w := int(s.vw)
	h := int(s.vh)

	var bi BITMAPINFO
	bi.BmiHeader.BiSize = uint32(unsafe.Sizeof(bi.BmiHeader))
	bi.BmiHeader.BiWidth = int32(w)
	bi.BmiHeader.BiHeight = -int32(h)
	bi.BmiHeader.BiPlanes = 1
	bi.BmiHeader.BiBitCount = 32
	bi.BmiHeader.BiCompression = 0

	pBits := unsafe.Pointer(&s.bgra[0])
	pBI := unsafe.Pointer(&bi)

	procStretchDIBits.Call(
		dst,
		0, 0, uintptr(w), uintptr(h),
		0, 0, uintptr(w), uintptr(h),
		uintptr(pBits),
		uintptr(pBI),
		0,
		SRCCOPY,
	)
	runtime.KeepAlive(s.bgra)
	runtime.KeepAlive(&bi)

	l, t, r, b := rectNorm(s.x1-s.vx, s.y1-s.vy, s.x2-s.vx, s.y2-s.vy)
	if l < 0 {
		l = 0
	}
	if t < 0 {
		t = 0
	}
	if r > s.vw {
		r = s.vw
	}
	if b > s.vh {
		b = s.vh
	}

	if !s.dragging && (s.x1 == s.x2 && s.y1 == s.y2) {
		alphaFillRectFromBlack1x1(dst, s.blackDC, RECT{Left: 0, Top: 0, Right: s.vw, Bottom: s.vh}, dimAlpha)
	} else {
		alphaFillRectFromBlack1x1(dst, s.blackDC, RECT{Left: 0, Top: 0, Right: s.vw, Bottom: t}, dimAlpha)
		alphaFillRectFromBlack1x1(dst, s.blackDC, RECT{Left: 0, Top: b, Right: s.vw, Bottom: s.vh}, dimAlpha)
		alphaFillRectFromBlack1x1(dst, s.blackDC, RECT{Left: 0, Top: t, Right: l, Bottom: b}, dimAlpha)
		alphaFillRectFromBlack1x1(dst, s.blackDC, RECT{Left: r, Top: t, Right: s.vw, Bottom: b}, dimAlpha)

		if r-l >= 1 && b-t >= 1 {
			drawBorder(dst, l, t, r, b)
		}
	}

	if dst != hdc && s.backDC != 0 {
		procBitBlt.Call(
			hdc,
			0, 0, uintptr(s.vw), uintptr(s.vh),
			s.backDC,
			0, 0,
			SRCCOPY,
		)
	}
}

func selectionWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_ERASEBKGND:
		return 1
	case WM_PAINT:
		st := getStateFromHwnd(hwnd)
		if st != nil {
			st.paint(hwnd)
			return 0
		}
	case WM_KEYDOWN:
		if wParam == VK_ESCAPE {
			st := getStateFromHwnd(hwnd)
			if st != nil {
				st.canceled = true
				st.done = true
				procDestroyWindow.Call(hwnd)
				return 0
			}
		}
	case WM_LBUTTONDOWN:
		st := getStateFromHwnd(hwnd)
		if st != nil {
			procSetCapture.Call(hwnd)
			var pt POINT
			pPt := unsafe.Pointer(&pt)
			procGetCursorPos.Call(uintptr(pPt))
			runtime.KeepAlive(&pt)

			st.dragging = true
			st.x1, st.y1 = pt.X, pt.Y
			st.x2, st.y2 = pt.X, pt.Y
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
	case WM_MOUSEMOVE:
		st := getStateFromHwnd(hwnd)
		if st != nil && st.dragging {
			var pt POINT
			pPt := unsafe.Pointer(&pt)
			procGetCursorPos.Call(uintptr(pPt))
			runtime.KeepAlive(&pt)

			if pt.X != st.x2 || pt.Y != st.y2 {
				st.x2, st.y2 = pt.X, pt.Y
				procInvalidateRect.Call(hwnd, 0, 0)
			}
			return 0
		}
	case WM_LBUTTONUP:
		st := getStateFromHwnd(hwnd)
		if st != nil && st.dragging {
			procReleaseCapture.Call()
			st.dragging = false

			var pt POINT
			pPt := unsafe.Pointer(&pt)
			procGetCursorPos.Call(uintptr(pPt))
			runtime.KeepAlive(&pt)

			st.x2, st.y2 = pt.X, pt.Y

			l, t, r, b := rectNorm(st.x1, st.y1, st.x2, st.y2)
			w, h := rectWH(l, t, r, b)
			if w < 3 || h < 3 {
				st.canceled = true
			}
			st.done = true
			procDestroyWindow.Call(hwnd)
			return 0
		}
	case WM_DESTROY:
		st := getStateFromHwnd(hwnd)
		if st != nil {
			st.freeBuffers()
		}
		detachState(hwnd)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func runSelectionWindow(img *image.RGBA, vx, vy, vw, vh int32) (l, t, r, b int32, canceled bool, err error) {
	hInstance := getModuleHandle()
	className := mustUTF16Ptr("OcrBoard_SelectionWindow")

	wndproc := syscall.NewCallback(selectionWndProc)
	var wc WNDCLASSEXW
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.LpfnWndProc = wndproc
	wc.HInstance = hInstance
	wc.LpszClassName = className
	cursor, _, _ := procLoadCursorW.Call(0, 32515) // IDC_CROSS
	wc.HCursor = cursor

	pWC := unsafe.Pointer(&wc)
	procRegisterClassExW.Call(uintptr(pWC)) // ignore already registered
	runtime.KeepAlive(&wc)

	exStyle := WS_EX_TOPMOST | WS_EX_TOOLWINDOW
	style := WS_POPUP | WS_VISIBLE

	hwnd, _, _ := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(mustUTF16Ptr("OcrBoardSelector"))),
		uintptr(style),
		uintptr(vx), uintptr(vy),
		uintptr(vw), uintptr(vh),
		0, 0,
		hInstance,
		0,
	)
	if hwnd == 0 {
		return 0, 0, 0, 0, true, fmt.Errorf("CreateWindowExW failed")
	}

	st := &selectionState{vx: vx, vy: vy, vw: vw, vh: vh, img: img}
	attachState(hwnd, st)

	procSetWindowPos.Call(hwnd, HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE|SWP_SHOWWINDOW)
	procShowWindow.Call(hwnd, SW_SHOW)
	procUpdateWindow.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)
	procSetFocus.Call(hwnd)

	var msg MSG
	for !st.done {
		pMsg := unsafe.Pointer(&msg)
		rv, _, _ := procPeekMessageW.Call(uintptr(pMsg), 0, 0, 0, PM_REMOVE)
		if rv != 0 {
			procTranslateMessage.Call(uintptr(pMsg))
			procDispatchMessageW.Call(uintptr(pMsg))
		} else {
			time.Sleep(1 * time.Millisecond)
		}
		runtime.KeepAlive(&msg)
	}

	if st.canceled {
		return 0, 0, 0, 0, true, nil
	}
	l, t, r, b = rectNorm(st.x1, st.y1, st.x2, st.y2)
	return l, t, r, b, false, nil
}

// =========================
// Hotkey register (MUST be called on main OS thread)
// =========================

func registerHotkey() error {
	r, _, _ := procRegisterHotKey.Call(0, uintptr(HOTKEY_ID), uintptr(MOD_WIN|MOD_ALT|MOD_SHIFT), uintptr(VK_T))
	if r == 0 {
		return fmt.Errorf("RegisterHotKey failed (maybe occupied)")
	}
	return nil
}

func unregisterHotkey() {
	_, _, _ = procUnregisterHotKey.Call(0, uintptr(HOTKEY_ID))
}

// =========================
// UI thread worker
// =========================

type uiRequest struct {
	apiURL       string
	mainThreadID uint32
}

func uiThreadLoop(reqCh <-chan uiRequest) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	for req := range reqCh {
		func() {
			defer func() {
				// notify main thread: UI done -> re-register hotkey
				procPostThreadMessageW.Call(uintptr(req.mainThreadID), WM_UI_DONE, 0, 0)
			}()

			img, vx, vy, vw, vh, err := captureVirtualScreenRGBA()
			if err != nil {
				messageBoxTop("OCR Error", err.Error())
				return
			}

			l, t, r, b, canceled, err := runSelectionWindow(img, vx, vy, vw, vh)
			if err != nil {
				messageBoxTop("OCR Error", err.Error())
				return
			}
			if canceled {
				return
			}

			crop := cropRGBA(img, vx, vy, l, t, r, b)
			if crop == nil {
				return
			}

			var buf bytes.Buffer
			if err := png.Encode(&buf, crop); err != nil {
				messageBoxTop("OCR Error", err.Error())
				return
			}

			ocrText, err := postPNGAndGetOCR(req.apiURL, buf.Bytes())
			if err != nil {
				messageBoxTop("OCR Error", err.Error())
				return
			}

			_ = setClipboardText(ocrText)

			msg := ocrText
			if msg == "" {
				msg = "(empty)"
			}
			runes := []rune(msg)
			if len(runes) > 2000 {
				msg = string(runes[:2000]) + "\n\n...(Content truncated. Full text has been copied to the clipboard)"
			}
			messageBoxTop("OCR Result (Copied to clipboard)", msg)
		}()
	}
}

// =========================
// Main (hotkey loop on main OS thread)
// =========================

func main() {
	// Hotkey loop must be on a fixed OS thread
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	setDPIAware()

	ip := flag.String("ip", "127.0.0.1", "Server IP")
	port := flag.Int("port", 8000, "Server Port")
	path := flag.String("path", "/upload", "API path")
	url := flag.String("url", "", "Full API URL (overrides -ip/-port/-path)")
	flag.Parse()

	apiURL := *url
	if apiURL == "" {
		apiURL = fmt.Sprintf("http://%s:%d%s", *ip, *port, *path)
	}

	fmt.Printf("[OCR] Hotkey ready: Win+Alt+Shift+T\n")
	fmt.Printf("[OCR] API: %s\n", apiURL)
	fmt.Printf("[OCR] ESC cancels selection (Win32).\n")

	mainThreadID := getCurrentThreadId()

	reqCh := make(chan uiRequest, 1)
	go uiThreadLoop(reqCh)

	if err := registerHotkey(); err != nil {
		messageBoxTop("OCR Error", err.Error())
		return
	}
	defer unregisterHotkey()

	capturing := false

	var msg MSG
	for {
		pMsg := unsafe.Pointer(&msg)
		rv, _, _ := procGetMessageW.Call(uintptr(pMsg), 0, 0, 0)
		runtime.KeepAlive(&msg)

		if int32(rv) == 0 || int32(rv) == -1 {
			break
		}

		switch msg.Message {
		case WM_HOTKEY:
			if int32(msg.WParam) == HOTKEY_ID && !capturing {
				capturing = true

				// 1) selector 開啟前先 UnregisterHotKey
				unregisterHotkey()

				// 2) selector 跑在 UI thread
				reqCh <- uiRequest{apiURL: apiURL, mainThreadID: mainThreadID}
			}

		case WM_UI_DONE:
			// selector 結束後再 RegisterHotKey
			_ = registerHotkey()
			capturing = false
		}

		procTranslateMessage.Call(uintptr(pMsg))
		procDispatchMessageW.Call(uintptr(pMsg))
	}
}
