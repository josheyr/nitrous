package geom

import "math"

// View is a camera to project with. If Matrix is set it is used as-is: the
// game's projection*modelview, applied to world - Origin. Otherwise the view
// is rebuilt from Eye/Yaw/Pitch/FOV. Angles are Minecraft's, in degrees: yaw 0
// looks toward +Z and grows clockwise (seen from above); positive pitch looks
// down. FOV is the vertical field of view.
type View struct {
	Eye   Vec3
	Yaw   float64
	Pitch float64
	FOV   float64

	Matrix *Mat4
	Origin Vec3
}

// Equal reports whether v and o describe the same camera.
func (v *View) Equal(o *View) bool {
	if v == nil || o == nil {
		return v == o
	}
	if v.Eye != o.Eye || v.Yaw != o.Yaw || v.Pitch != o.Pitch || v.FOV != o.FOV || v.Origin != o.Origin {
		return false
	}
	if v.Matrix == nil || o.Matrix == nil {
		return v.Matrix == o.Matrix
	}
	return *v.Matrix == *o.Matrix
}

// Seg is a screen-space line segment in client pixels.
type Seg struct{ X1, Y1, X2, Y2 float64 }

const nearPlane = 0.05

// Projector maps world coordinates onto a w×h viewport for one View.
type Projector struct {
	clip func(Vec3) [4]float64 // world -> clip space
	w, h float64
}

// Projector prepares v for a w×h viewport. ok is false if nothing can be
// projected (empty viewport or a degenerate FOV).
func (v View) Projector(w, h float64) (p Projector, ok bool) {
	if w <= 0 || h <= 0 {
		return p, false
	}
	p.w, p.h = w, h
	if m := v.Matrix; m != nil {
		o := v.Origin
		p.clip = func(pt Vec3) [4]float64 {
			d := pt.Sub(o)
			var r [4]float64
			for row := 0; row < 4; row++ {
				r[row] = m[row]*d.X + m[4+row]*d.Y + m[8+row]*d.Z + m[12+row]
			}
			return r
		}
		return p, true
	}
	sy, cy := math.Sincos(v.Yaw * math.Pi / 180)
	sp, cp := math.Sincos(v.Pitch * math.Pi / 180)
	fwd := Vec3{-sy * cp, -sp, cy * cp}
	right := Vec3{-cy, 0, -sy}
	up := right.Cross(fwd)
	tanHalf := math.Tan(v.FOV * math.Pi / 360)
	if tanHalf <= 0 {
		return p, false
	}
	aspect := w / h
	p.clip = func(pt Vec3) [4]float64 {
		d := pt.Sub(v.Eye)
		return [4]float64{d.Dot(right) / (tanHalf * aspect), d.Dot(up) / tanHalf, 0, d.Dot(fwd)}
	}
	return p, true
}

// Line returns the visible part of the world-space segment a-b. ok is false
// if it lies entirely behind the camera.
func (p Projector) Line(a, b Vec3) (Seg, bool) { return p.edge(p.clip(a), p.clip(b)) }

// Box returns the screen-space edges of b. Edges are clipped against the near
// plane, so boxes behind or around the camera still draw correctly.
func (p Projector) Box(b Box) []Seg {
	var corners [8][4]float64
	for i := range corners {
		pt := b.Min
		if i&1 != 0 {
			pt.X = b.Max.X
		}
		if i&2 != 0 {
			pt.Y = b.Max.Y
		}
		if i&4 != 0 {
			pt.Z = b.Max.Z
		}
		corners[i] = p.clip(pt)
	}
	segs := make([]Seg, 0, 12)
	for i := 0; i < 8; i++ {
		for bit := 1; bit < 8; bit <<= 1 {
			j := i ^ bit
			if j < i {
				continue // each edge once
			}
			if s, ok := p.edge(corners[i], corners[j]); ok {
				segs = append(segs, s)
			}
		}
	}
	return segs
}

// edge clips a clip-space segment against the near plane and maps it to
// pixels. w is the distance in front of the eye for a perspective projection.
func (p Projector) edge(a, b [4]float64) (Seg, bool) {
	if a[3] < nearPlane && b[3] < nearPlane {
		return Seg{}, false
	}
	if a[3] < nearPlane {
		a = lerp4(a, b, (nearPlane-a[3])/(b[3]-a[3]))
	} else if b[3] < nearPlane {
		b = lerp4(b, a, (nearPlane-b[3])/(a[3]-b[3]))
	}
	x1, y1 := p.pixel(a)
	x2, y2 := p.pixel(b)
	return Seg{x1, y1, x2, y2}, true
}

func (p Projector) pixel(c [4]float64) (x, y float64) {
	return (c[0]/c[3] + 1) / 2 * p.w, (1 - c[1]/c[3]) / 2 * p.h
}

func lerp4(a, b [4]float64, t float64) [4]float64 {
	return [4]float64{Lerp(a[0], b[0], t), Lerp(a[1], b[1], t), Lerp(a[2], b[2], t), Lerp(a[3], b[3], t)}
}
