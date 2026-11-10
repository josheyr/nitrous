package mc

import (
	"fmt"
	"math"

	"nitrous/internal/geom"
	"nitrous/internal/hotspot"
)

// Modern clients (1.21.x / 26.x, Mojang names) keep the frame's view in
// GameRenderer.mainCamera: the interpolated eye position, the camera angles
// and the effective (sprint/speed-modified) FOV. Lunar also keeps the full
// matrices. Projecting with these follows FOV changes from settings,
// sprinting, speed etc. exactly.
type modernCamera struct {
	gr, cam, camPos hotspot.Field     // Minecraft.gameRenderer, GameRenderer.mainCamera, Camera.position
	pos             [3]hotspot.Field  // Vec3 x/y/z
	xRot, yRot, fov hotspot.Field     // Camera angles and effective FOV
	eyeH, eyeHOld   *hotspot.Field    // Camera.eyeHeight / eyeHeightOld (smoothed crouch)
	dt, residual    *hotspot.Field    // Minecraft.deltaTracker, DeltaTracker.Timer.deltaTickResidual
	proj            *hotspot.Field    // GameRenderer.projection (Lunar adds it; vanilla doesn't have it)
	bob             *hotspot.Field    // GameRenderer.bobbingEffectsModel
	viewRot         hotspot.Field     // Camera.cachedViewRotMatrix; only with proj
	m               [16]hotspot.Field // Matrix4f m00..m33, column-major order; only with proj
}

func (c *Client) resolveModern(mc hotspot.Oop) *modernCamera {
	vm := c.vm
	l := &modernCamera{}
	mcFields := vm.AllFields(c.klass)
	var ok bool
	if l.gr, ok = find(mcFields, "", "gameRenderer"); !ok {
		return nil
	}
	gr := vm.ObjField(mc, l.gr)
	if gr == 0 {
		return nil
	}
	grf := vm.AllFields(vm.KlassOf(gr))
	if l.cam, ok = find(grf, "", "mainCamera"); !ok {
		return nil
	}
	l.proj = findPtr(grf, "Lorg/joml/Matrix4f;", "projection")
	l.bob = findPtr(grf, "", "bobbingEffectsModel")
	cam := vm.ObjField(gr, l.cam)
	if cam == 0 {
		return nil
	}
	cf := vm.AllFields(vm.KlassOf(cam))
	if l.camPos, ok = find(cf, "", "position"); !ok {
		return nil
	}
	var ok1, ok2, ok3 bool
	l.xRot, ok1 = find(cf, "F", "xRot")
	l.yRot, ok2 = find(cf, "F", "yRot")
	l.fov, ok3 = find(cf, "F", "fov")
	if !ok1 || !ok2 || !ok3 {
		return nil
	}
	if a, b := findPtr(cf, "F", "eyeHeight"), findPtr(cf, "F", "eyeHeightOld"); a != nil && b != nil {
		l.eyeH, l.eyeHOld = a, b
	}
	pos := vm.ObjField(cam, l.camPos)
	if pos == 0 {
		return nil
	}
	if l.pos, ok = find3(vm.AllFields(vm.KlassOf(pos)), "D", [3]string{"x", "y", "z"}); !ok {
		return nil
	}
	// The matrix path (Lunar) also needs the camera's view-rotation matrix.
	if l.proj != nil && !c.resolveViewRot(l, cam, cf) {
		l.proj = nil
	}
	if f := findPtr(mcFields, "", "deltaTracker"); f != nil {
		if d := vm.ObjField(mc, *f); d != 0 {
			if r := findPtr(vm.AllFields(vm.KlassOf(d)), "F", "deltaTickResidual"); r != nil {
				l.dt, l.residual = f, r
			}
		}
	}
	return l
}

func (c *Client) resolveViewRot(l *modernCamera, cam hotspot.Oop, camFields []hotspot.Field) bool {
	var ok bool
	if l.viewRot, ok = find(camFields, "", "cachedViewRotMatrix"); !ok {
		return false
	}
	rot := c.vm.ObjField(cam, l.viewRot)
	if rot == 0 {
		return false
	}
	mf := c.vm.AllFields(c.vm.KlassOf(rot))
	for i := range l.m {
		if l.m[i], ok = find(mf, "F", fmt.Sprintf("m%d%d", i/4, i%4)); !ok {
			return false
		}
	}
	return true
}

// matrix4f reads an org.joml.Matrix4f.
func (c *Client) matrix4f(o hotspot.Oop) (geom.Mat4, bool) {
	if o == 0 {
		return geom.Mat4{}, false
	}
	return mat4(func(i int) float64 { return c.vm.Float(o, c.modern.m[i]) })
}

// pureProjection reports whether p has no terms besides a plain perspective's,
// i.e. no view bobbing has been multiplied into it.
func pureProjection(p geom.Mat4) bool {
	for _, i := range []int{1, 2, 3, 4, 6, 7, 8, 9, 12, 13, 15} {
		if math.Abs(p[i]) > 1e-6 {
			return false
		}
	}
	return true
}

// modernFrame reads the last frame's camera from a modern client.
func (c *Client) modernFrame(mc hotspot.Oop) *Frame {
	l, vm := c.modern, c.vm
	gr := vm.ObjField(mc, l.gr)
	if gr == 0 {
		return nil
	}
	cam := vm.ObjField(gr, l.cam)
	if cam == 0 {
		return nil
	}
	pos := vm.ObjField(cam, l.camPos)
	if pos == 0 {
		return nil
	}
	f := &Frame{
		Pos: c.vec3(pos, l.pos), AtEye: true, Partial: 1,
		Yaw: vm.Float(cam, l.yRot), Pitch: vm.Float(cam, l.xRot), FOV: vm.Float(cam, l.fov),
	}
	if !f.Pos.Finite() || !geom.Finite(f.Yaw, f.Pitch, f.FOV) {
		return nil
	}
	if f.FOV < 1 || f.FOV > 179 {
		f.FOV = 0 // unknown: the caller falls back to Client.FOV
	}
	// Lunar keeps the exact frame matrices (including bobbing); use them if present.
	if l.proj != nil {
		rot, ok1 := c.matrix4f(vm.ObjField(cam, l.viewRot))
		proj, ok2 := c.matrix4f(vm.ObjField(gr, *l.proj))
		if ok1 && ok2 && proj[11] != 0 {
			if l.bob != nil && pureProjection(proj) {
				if b, ok := c.matrix4f(vm.ObjField(gr, *l.bob)); ok {
					proj = proj.Mul(b)
				}
			}
			mat := proj.Mul(rot)
			f.Matrix, f.Origin = &mat, f.Pos
		}
	}
	if l.dt != nil {
		if d := vm.ObjField(mc, *l.dt); d != 0 {
			if p := vm.Float(d, *l.residual); geom.Finite(p) && p >= 0 && p <= 1 {
				f.Partial = p
			}
		}
	}
	if l.eyeH != nil {
		if h := geom.Lerp(vm.Float(cam, *l.eyeHOld), vm.Float(cam, *l.eyeH), f.Partial); validSize(h) {
			f.EyeHeight = h
		}
	}
	return f
}
