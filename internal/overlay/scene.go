// Package overlay draws over the Minecraft window using a transparent,
// click-through Win32 window that tracks the game's client area. It never
// touches the game process: it only reads window geometry and makes its own
// window one that the game window owns.
//
// What to draw is described by a Scene: boxes in world coordinates that the
// overlay projects through the Scene's View.
//
// Works with windowed, borderless and fullscreen Minecraft, with no special
// privileges. OpenGL has no D3D-style exclusive mode, so the compositor
// always owns the screen and the only thing that can hide the overlay is
// z-order: fullscreen game windows are topmost themselves. Windows keeps an
// owned window above its owner whatever the owner does, so the overlay has
// the game window adopt it (see Overlay.adopt).
package overlay

import (
	"slices"

	"nitrous/internal/geom"
)

// Color is a Win32 COLORREF (0x00BBGGRR).
type Color uint32

// RGB builds a Color.
func RGB(r, g, b uint8) Color { return Color(r) | Color(g)<<8 | Color(b)<<16 }

// Box is a wireframe box in world coordinates.
type Box struct {
	geom.Box
	Color Color
}

// Scene is everything the overlay draws. The zero Scene draws nothing, and
// so does one without a View.
type Scene struct {
	View  *geom.View
	Boxes []Box
}

// Box adds a wireframe box in world coordinates.
func (s *Scene) Box(b geom.Box, c Color) { s.Boxes = append(s.Boxes, Box{b, c}) }

func (s Scene) equal(o Scene) bool {
	return slices.Equal(s.Boxes, o.Boxes) && s.View.Equal(o.View)
}

func (s Scene) clone() Scene {
	c := Scene{Boxes: slices.Clone(s.Boxes)}
	if s.View != nil {
		v := *s.View
		c.View = &v
	}
	return c
}
