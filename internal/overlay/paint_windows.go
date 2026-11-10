package overlay

import "nitrous/internal/geom"

// Magenta is never used for drawing, so it serves as the transparent color key.
var colorKey = RGB(255, 0, 255)

// stroke is a set of screen-space segments drawn with one pen.
type stroke struct {
	color Color
	segs  []geom.Seg
}

func (o *Overlay) paint(hwnd uintptr) {
	var ps paintStruct
	hdc := call(pBeginPaint, hwnd, ptr(&ps))
	defer call(pEndPaint, hwnd, ptr(&ps))

	w, h := o.w, o.h
	// The back buffer is kept between frames; only rebuilt when the size changes.
	if o.memDC == 0 || o.bufW != w || o.bufH != h {
		if o.memDC != 0 {
			call(pSelectObject, o.memDC, o.oldBmp)
			call(pDeleteObject, o.memBmp)
			call(pDeleteDC, o.memDC)
		}
		o.memDC = call(pCreateCompatibleDC, hdc)
		o.memBmp = call(pCreateCompatibleBitmap, hdc, uintptr(w), uintptr(h))
		o.oldBmp = call(pSelectObject, o.memDC, o.memBmp)
		o.bufW, o.bufH = w, h
		o.fullPaint.Store(true)
	}
	mem := o.memDC

	o.mu.Lock()
	scene := o.scene // Set replaces, never mutates, so a shallow copy is safe
	o.mu.Unlock()

	// Clearing and copying just the area the boxes covered last frame and cover
	// now is far cheaper than the whole screen, which keeps paints well inside
	// one display refresh.
	strokes := project(scene, float64(w), float64(h))
	bounds := strokeBounds(strokes, w, h)
	region := o.worldBounds.union(bounds)
	if o.fullPaint.Swap(false) {
		region = rect{0, 0, w, h}
	}
	o.worldBounds = bounds
	if region.empty() {
		return // nothing on screen changed
	}
	call(pFillRect, mem, ptr(&region), o.keyBrush)
	drawStrokes(mem, strokes, h)
	call(pBitBlt, hdc, uintptr(region.left), uintptr(region.top),
		uintptr(region.right-region.left), uintptr(region.bottom-region.top),
		mem, uintptr(region.left), uintptr(region.top), srcCopy)
}

// project turns the scene's boxes into screen-space strokes.
func project(s Scene, w, h float64) []stroke {
	if s.View == nil {
		return nil
	}
	p, ok := s.View.Projector(w, h)
	if !ok {
		return nil
	}
	out := make([]stroke, 0, len(s.Boxes))
	for _, b := range s.Boxes {
		if segs := p.Box(b.Box); len(segs) > 0 {
			out = append(out, stroke{b.Color, segs})
		}
	}
	return out
}

func penWidth(h int32) int32 {
	if h >= 1440 {
		return 3
	}
	return 2
}

// strokeBounds is the on-screen rectangle covering all strokes, padded for
// the pen width and clamped to the window.
func strokeBounds(strokes []stroke, w, h int32) rect {
	r := rect{w, h, 0, 0}
	for _, s := range strokes {
		for _, sg := range s.segs {
			for _, p := range [2][2]float64{{sg.X1, sg.Y1}, {sg.X2, sg.Y2}} {
				x, y := int32(max(-1, min(float64(w)+1, p[0]))), int32(max(-1, min(float64(h)+1, p[1])))
				r.left, r.top = min(r.left, x), min(r.top, y)
				r.right, r.bottom = max(r.right, x), max(r.bottom, y)
			}
		}
	}
	if r.right < r.left {
		return rect{}
	}
	pad := penWidth(h) + 2
	return rect{max(0, r.left-pad), max(0, r.top-pad), min(w, r.right+pad), min(h, r.bottom+pad)}
}

// drawStrokes draws every stroke, reusing one pen per color.
func drawStrokes(dc uintptr, strokes []stroke, h int32) {
	pens := map[Color]uintptr{}
	var old uintptr
	for _, s := range strokes {
		pen, ok := pens[s.color]
		if !ok {
			pen = call(pCreatePen, psSolid, uintptr(penWidth(h)), uintptr(s.color))
			pens[s.color] = pen
		}
		prev := call(pSelectObject, dc, pen)
		if old == 0 {
			old = prev
		}
		for _, sg := range s.segs {
			call(pMoveToEx, dc, px(sg.X1), px(sg.Y1), 0)
			call(pLineTo, dc, px(sg.X2), px(sg.Y2))
		}
	}
	if old != 0 {
		call(pSelectObject, dc, old) // deselect before deleting
	}
	for _, p := range pens {
		call(pDeleteObject, p)
	}
}

// px converts a screen coordinate to a GDI int, clamped so that edges running
// close to the near plane can't overflow.
func px(v float64) uintptr {
	if v != v { // NaN
		return 0
	}
	return uintptr(int32(max(-1e6, min(1e6, v))))
}
