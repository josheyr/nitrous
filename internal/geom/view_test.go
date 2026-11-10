package geom

import (
	"math"
	"testing"
)

func boxSegs(t *testing.T, v View, b Box, w, h float64) []Seg {
	t.Helper()
	p, ok := v.Projector(w, h)
	if !ok {
		t.Fatalf("no projector for %+v", v)
	}
	return p.Box(b)
}

func TestBoxCenterAndSide(t *testing.T) {
	// Standing at the origin looking south (+Z), a 1-block box 5 blocks ahead
	// is centred horizontally; one 5 blocks to the player's *left* (+X when
	// facing south) must land on the left half of the screen.
	cam := View{Yaw: 0, Pitch: 0, FOV: 70}
	const w, h = 1600, 900
	mean := func(segs []Seg) (x, y float64) {
		for _, s := range segs {
			x += s.X1 + s.X2
			y += s.Y1 + s.Y2
		}
		n := float64(2 * len(segs))
		return x / n, y / n
	}
	segs := boxSegs(t, cam, Box{Min: Vec3{-.5, -.5, 5}, Max: Vec3{.5, .5, 6}}, w, h)
	if len(segs) != 12 {
		t.Fatalf("want 12 edges, got %d", len(segs))
	}
	if x, y := mean(segs); math.Abs(x-w/2) > 1 || math.Abs(y-h/2) > 1 {
		t.Errorf("box ahead not centred: %.1f,%.1f", x, y)
	}
	segs = boxSegs(t, cam, Box{Min: Vec3{4.5, -.5, 5}, Max: Vec3{5.5, .5, 6}}, w, h)
	if x, _ := mean(segs); x >= w/2 {
		t.Errorf("box on +X (left when facing south) should be left of centre, x=%.1f", x)
	}
	// A box directly behind the camera must produce no segments.
	if segs := boxSegs(t, cam, Box{Min: Vec3{-.5, -.5, -6}, Max: Vec3{.5, .5, -5}}, w, h); len(segs) != 0 {
		t.Errorf("box behind camera drew %d edges", len(segs))
	}
	// Looking down (+pitch) moves a box at eye level up the screen.
	down := View{Pitch: 45, FOV: 70}
	segs = boxSegs(t, down, Box{Min: Vec3{-.5, -.5, 5}, Max: Vec3{.5, .5, 6}}, w, h)
	if _, y := mean(segs); y >= h/2 {
		t.Errorf("looking down should push level box above centre, y=%.1f", y)
	}
}

func TestLine(t *testing.T) {
	p, _ := View{FOV: 70}.Projector(1600, 900)
	if s, ok := p.Line(Vec3{0, 0, 5}, Vec3{0, 0, 10}); !ok || math.Abs(s.X1-800) > 1e-9 || math.Abs(s.Y2-450) > 1e-9 {
		t.Errorf("line straight ahead should sit on the crosshair: %+v ok=%v", s, ok)
	}
	if _, ok := p.Line(Vec3{0, 0, -5}, Vec3{1, 0, -10}); ok {
		t.Error("line behind the camera was drawn")
	}
	// One end behind the camera: clipped, not dropped.
	if s, ok := p.Line(Vec3{1, 0, -5}, Vec3{1, 0, 5}); !ok || !Finite(s.X1, s.Y1, s.X2, s.Y2) {
		t.Errorf("straddling line: %+v ok=%v", s, ok)
	}
}

// glMatrix builds what the game's GL state would hold for this camera:
// gluPerspective * view, column-major, with the world origin at origin.
func glMatrix(c View, aspect float64, origin Vec3) Mat4 {
	sy, cy := math.Sincos(c.Yaw * math.Pi / 180)
	sp, cp := math.Sincos(c.Pitch * math.Pi / 180)
	f := Vec3{-sy * cp, -sp, cy * cp}
	r := Vec3{-cy, 0, -sy}
	u := r.Cross(f)
	e := c.Eye.Sub(origin)
	var mv Mat4 // rows: right, up, -forward
	for i, row := range []Vec3{r, u, {-f.X, -f.Y, -f.Z}} {
		mv[0*4+i], mv[1*4+i], mv[2*4+i] = row.X, row.Y, row.Z
		mv[3*4+i] = -row.Dot(e)
	}
	mv[15] = 1
	t := 1 / math.Tan(c.FOV*math.Pi/360)
	n, fa := 0.05, 512.0
	var p Mat4
	p[0], p[5] = t/aspect, t
	p[10], p[11] = (fa+n)/(n-fa), -1
	p[14] = 2 * fa * n / (n - fa)
	return p.Mul(mv)
}

func TestMatrixPathMatchesAngles(t *testing.T) {
	const w, h = 1920, 1080
	origin := Vec3{100, 64, -200}
	for _, c := range []View{
		{Eye: Vec3{100.3, 65.62, -199.1}, Yaw: 37, Pitch: 12, FOV: 70},
		{Eye: Vec3{101, 66, -201}, Yaw: -170, Pitch: -40, FOV: 90},
	} {
		m := glMatrix(c, float64(w)/h, origin)
		mc := View{Matrix: &m, Origin: origin}
		for _, b := range []Box{
			{Min: Vec3{110, 64, -190}, Max: Vec3{110.6, 65.8, -189.4}},
			{Min: Vec3{99, 64, -230}, Max: Vec3{99.6, 65.8, -229.4}},
			{Min: Vec3{100, 65, -199.5}, Max: Vec3{100.6, 66.8, -198.9}}, // straddles the camera
		} {
			a, bb := boxSegs(t, c, b, w, h), boxSegs(t, mc, b, w, h)
			if len(a) != len(bb) {
				t.Fatalf("edge count %d vs %d", len(a), len(bb))
			}
			for i := range a {
				d := math.Abs(a[i].X1-bb[i].X1) + math.Abs(a[i].Y1-bb[i].Y1) + math.Abs(a[i].X2-bb[i].X2) + math.Abs(a[i].Y2-bb[i].Y2)
				if d > 0.5 {
					t.Errorf("edge %d differs: %+v vs %+v", i, a[i], bb[i])
				}
			}
		}
	}
}

func TestViewEqual(t *testing.T) {
	m1, m2 := Mat4{1}, Mat4{1}
	a, b := &View{Yaw: 1, Matrix: &m1}, &View{Yaw: 1, Matrix: &m2}
	if !a.Equal(b) {
		t.Error("identical views compare unequal")
	}
	m2[3] = 2
	if a.Equal(b) || a.Equal(nil) || !(*View)(nil).Equal(nil) {
		t.Error("Equal mishandles differing matrices or nil")
	}
}
