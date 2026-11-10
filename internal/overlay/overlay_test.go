package overlay

import (
	"os"
	"testing"
	"time"

	"nitrous/internal/geom"
)

func testScene() Scene {
	m := geom.Mat4{0: 1, 5: 1, 10: 1, 11: -1, 15: 1}
	s := Scene{View: &geom.View{Eye: geom.Vec3{X: 1, Y: 65.62, Z: 2}, Yaw: 30, Pitch: -5, FOV: 70, Matrix: &m}}
	s.Box(geom.Upright(geom.Vec3{X: 1, Y: 64, Z: 8}, 0.6, 1.8), RGB(85, 255, 85))
	return s
}

func TestSceneEqualAndClone(t *testing.T) {
	s := testScene()
	c := s.clone()
	if !c.equal(s) {
		t.Fatal("clone differs")
	}
	c.View.Yaw++
	c.Boxes[0].Color++
	if s.equal(c) || s.View.Yaw != 30 || s.Boxes[0].Color != RGB(85, 255, 85) {
		t.Error("clone shares state with the original")
	}
	if !(Scene{}).equal(Scene{Boxes: []Box{}}) {
		t.Error("nil and empty slices should be the same scene")
	}
}

func TestProjectAndBounds(t *testing.T) {
	const w, h = 1920, 1080
	s := testScene()
	s.View = &geom.View{Eye: geom.Vec3{X: 1, Y: 65.62, Z: 2}, FOV: 70} // looking at the box, 6 blocks ahead
	strokes := project(s, w, h)
	if len(strokes) != 1 || len(strokes[0].segs) != 12 {
		t.Fatalf("strokes: %+v", strokes)
	}
	b := strokeBounds(strokes, w, h)
	if b.empty() || b.left < 0 || b.top < 0 || b.right > w || b.bottom > h {
		t.Errorf("bounds %+v outside the window", b)
	}
	if cx := (b.left + b.right) / 2; cx < w/2-20 || cx > w/2+20 {
		t.Errorf("box straight ahead should be centred, bounds %+v", b)
	}
	if got := project(Scene{Boxes: s.Boxes}, w, h); got != nil {
		t.Error("world geometry drawn without a view")
	}
	if !strokeBounds(nil, w, h).empty() {
		t.Error("no strokes should give empty bounds")
	}
	if u := (rect{}).union(b); u != b {
		t.Errorf("union with empty: %+v", u)
	}
}

// The window is created, ticks and accepts scenes without a game to follow
// (it stays hidden: this process has no window to track).
func TestStartSmoke(t *testing.T) {
	o, err := Start(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	o.Set(testScene())
	time.Sleep(150 * time.Millisecond) // a few timer ticks
	o.Set(Scene{})
	if _, err := Start(uint32(os.Getpid())); err == nil {
		t.Error("second overlay in one process should be refused")
	}
}
