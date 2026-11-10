package mc

import (
	"time"

	"nitrous/internal/geom"
	"nitrous/internal/hotspot"
)

// Camera is the local player's view as of the last client tick.
type Camera struct {
	Pos, Prev          geom.Vec3 // feet, this tick and the one before
	Yaw, Pitch         float64
	PrevYaw, PrevPitch float64
	EyeHeight          float64
	Ticks              int  // client tick counter; changes once per tick
	HasTicks           bool // Ticks is meaningful

	// Frame is the camera exactly as the game last rendered it, when the
	// client's renderer state could be read; it is preferred over the fields
	// above, which only change once per tick.
	Frame *Frame
}

// Frame is the camera of the last rendered frame.
type Frame struct {
	Pos        geom.Vec3 // camera position, already interpolated by the game
	AtEye      bool      // Pos is the eye position, not the feet
	Yaw, Pitch float64
	Partial    float64 // the frame's partial tick, for interpolating other entities
	FOV        float64 // effective vertical FOV in degrees; 0 if unknown
	EyeHeight  float64 // live eye height (crouching etc.); 0 if unknown

	// Matrix is the frame's GL projection*modelview and Origin the world point
	// it treats as origin. Nil if the matrices could not be read.
	Matrix *geom.Mat4
	Origin geom.Vec3
}

const (
	tickLength     = 50 * time.Millisecond // Minecraft runs 20 client ticks per second
	eyeRatio       = 0.9                   // eye height / hitbox height: 1.62 for a standing player
	firstPersonMax = 1.5                   // game camera further than this from our eye: not first person
)

// tickClock estimates the render partial tick from the client tick counter,
// for clients whose own value can't be read: entity positions only change
// once per tick, so drawing them as-is at 60 Hz would visibly stutter.
type tickClock struct {
	ticks int
	start time.Time
	init  bool
}

// partial returns how far (0..1) we are into the current tick.
func (c *tickClock) partial(cam *Camera, now time.Time) float64 {
	if !cam.HasTicks {
		return 1
	}
	if !c.init || cam.Ticks != c.ticks {
		c.ticks, c.start, c.init = cam.Ticks, now, true
	}
	return max(0, min(1, float64(now.Sub(c.start))/float64(tickLength)))
}

// resolveCameras finds the renderer state once, on the first sample in a world.
func (c *Client) resolveCameras(mc hotspot.Oop) {
	c.legacy = c.resolveLegacy(mc)
	if c.legacy.rm == nil {
		c.modern = c.resolveModern(mc)
	}
}

// camera reads the local player's view. Nil if rotation fields weren't found.
func (c *Client) camera(mc hotspot.Oop, self *Player) *Camera {
	l, vm := c.ent, c.vm
	if l == nil || l.yaw == nil {
		return nil
	}
	cam := &Camera{Pos: self.Pos, Prev: self.Prev, EyeHeight: defaultHeight * eyeRatio}
	cam.Yaw, cam.Pitch = vm.Float(self.Ref, *l.yaw), vm.Float(self.Ref, *l.pitch)
	cam.PrevYaw, cam.PrevPitch = cam.Yaw, cam.Pitch
	if l.prevYaw != nil && l.prevPitch != nil {
		cam.PrevYaw, cam.PrevPitch = vm.Float(self.Ref, *l.prevYaw), vm.Float(self.Ref, *l.prevPitch)
	}
	if !geom.Finite(cam.Yaw, cam.Pitch, cam.PrevYaw, cam.PrevPitch) {
		return nil
	}
	if l.height != nil {
		if h := vm.Float(self.Ref, *l.height); validSize(h) {
			cam.EyeHeight = h * eyeRatio
		}
	}
	if l.ticks != nil {
		cam.Ticks, cam.HasTicks = vm.Int(self.Ref, *l.ticks), true
	}
	if c.modern != nil {
		cam.Frame = c.modernFrame(mc)
	} else {
		cam.Frame = c.legacyFrame(mc, self.Pos)
	}
	return cam
}

// view turns a camera into the view to project with, and the partial tick to
// interpolate entities by so that they line up with it.
func (c *Client) view(cam *Camera, now time.Time) (*geom.View, float64) {
	f := cam.Frame
	if f == nil {
		// Reconstruct from the player entity and an estimated tick phase.
		pt := c.clock.partial(cam, now)
		return &geom.View{
			Eye:   cam.Prev.Lerp(cam.Pos, pt).Up(cam.EyeHeight),
			Yaw:   geom.LerpAngle(cam.PrevYaw, cam.Yaw, pt),
			Pitch: geom.Lerp(cam.PrevPitch, cam.Pitch, pt),
			FOV:   c.FOV,
		}, pt
	}
	// Angles, FOV and matrices come from the frame the game last drew. The
	// camera *position*, though, is interpolated from our own player with the
	// same partial tick as every other player. The game updates that partial
	// tick at the start of a frame but the camera only during rendering, so
	// reading both can mix two frames — moving targets then wobble against the
	// camera. Using one partial tick for everything keeps the camera and the
	// entities consistent with each other.
	pt := f.Partial
	feet := cam.Prev.Lerp(cam.Pos, pt)
	eyeHeight := cam.EyeHeight
	if f.EyeHeight > 0 {
		eyeHeight = f.EyeHeight // live: lower while crouching
	}
	eye := feet.Up(eyeHeight)
	gameEye := f.Pos
	if !f.AtEye {
		gameEye = gameEye.Up(cam.EyeHeight)
	}
	// F5, spectating or a teleport: the game's camera isn't at our player.
	firstPerson := eye.Dist(gameEye) < firstPersonMax
	if !firstPerson {
		eye = gameEye
	}
	v := &geom.View{Eye: eye, Yaw: f.Yaw, Pitch: f.Pitch, FOV: c.FOV}
	if f.FOV > 0 {
		v.FOV = f.FOV // the game's live FOV, including sprint/speed changes
	}
	if f.Matrix != nil {
		// The game's own projection: includes dynamic FOV, bobbing, F5. It maps
		// world-minus-origin; the origin is the feet on 1.8 (the modelview adds
		// eye height) and the eye on modern.
		v.Matrix, v.Origin = f.Matrix, f.Origin
		switch {
		case firstPerson && f.AtEye:
			v.Origin = eye
		case firstPerson:
			v.Origin = feet
		}
	}
	return v, pt
}

// mat4 reads 16 floats into a matrix; ok is false if any isn't finite.
func mat4(read func(i int) float64) (m geom.Mat4, ok bool) {
	for i := range m {
		if m[i] = read(i); !geom.Finite(m[i]) {
			return m, false
		}
	}
	return m, true
}
