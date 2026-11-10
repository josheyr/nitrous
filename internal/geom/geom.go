// Package geom is the 3D math shared by the game reader and the overlay:
// vectors, boxes, matrices and world-to-screen projection.
package geom

import "math"

// Vec3 is a point or direction in Minecraft world coordinates.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

func (a Vec3) Add(b Vec3) Vec3    { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Sub(b Vec3) Vec3    { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Dot(b Vec3) float64 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }

func (a Vec3) Cross(b Vec3) Vec3 {
	return Vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}

// Up returns a raised by dy blocks.
func (a Vec3) Up(dy float64) Vec3 { return Vec3{a.X, a.Y + dy, a.Z} }

// Dist is the straight-line distance between a and b.
func (a Vec3) Dist(b Vec3) float64 { d := a.Sub(b); return math.Sqrt(d.Dot(d)) }

// Manhattan is the sum of the per-axis distances between a and b.
func (a Vec3) Manhattan(b Vec3) float64 {
	return math.Abs(a.X-b.X) + math.Abs(a.Y-b.Y) + math.Abs(a.Z-b.Z)
}

// Lerp returns the point t of the way from a to b.
func (a Vec3) Lerp(b Vec3, t float64) Vec3 {
	return Vec3{Lerp(a.X, b.X, t), Lerp(a.Y, b.Y, t), Lerp(a.Z, b.Z, t)}
}

// Round returns a rounded to one decimal place.
func (a Vec3) Round() Vec3 { return Vec3{Round(a.X), Round(a.Y), Round(a.Z)} }

// Finite reports whether no component is NaN or infinite.
func (a Vec3) Finite() bool { return Finite(a.X, a.Y, a.Z) }

// Box is an axis-aligned box in world coordinates.
type Box struct{ Min, Max Vec3 }

// Upright is the box of an entity standing at feet: width × width at the
// base, height tall.
func Upright(feet Vec3, width, height float64) Box {
	half := width / 2
	return Box{
		Min: Vec3{feet.X - half, feet.Y, feet.Z - half},
		Max: Vec3{feet.X + half, feet.Y + height, feet.Z + half},
	}
}

// Center is the middle of the box.
func (b Box) Center() Vec3 { return b.Min.Lerp(b.Max, 0.5) }

// Mat4 is a column-major 4x4 matrix, as OpenGL stores them.
type Mat4 [16]float64

// Mul returns a*b.
func (a Mat4) Mul(b Mat4) Mat4 {
	var r Mat4
	for c := 0; c < 4; c++ {
		for row := 0; row < 4; row++ {
			var v float64
			for k := 0; k < 4; k++ {
				v += a[k*4+row] * b[c*4+k]
			}
			r[c*4+row] = v
		}
	}
	return r
}

// Lerp returns the value t of the way from a to b.
func Lerp(a, b, t float64) float64 { return a + (b-a)*t }

// LerpAngle interpolates degrees along the shortest way round.
func LerpAngle(a, b, t float64) float64 {
	d := math.Mod(b-a, 360)
	if d > 180 {
		d -= 360
	} else if d < -180 {
		d += 360
	}
	return a + d*t
}

// Round rounds v to one decimal place.
func Round(v float64) float64 { return math.Round(v*10) / 10 }

// Finite reports whether none of vs is NaN or infinite.
func Finite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}
