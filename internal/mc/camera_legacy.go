package mc

import (
	"encoding/binary"
	"math"

	"nitrous/internal/geom"
	"nitrous/internal/hotspot"
)

// Legacy clients (1.8 – 1.12, MCP/SRG names) keep the exact camera of the
// last rendered frame in their RenderManager (interpolated viewer position
// and angles), and the frame's partial tick in their Timer. Reading these
// instead of reconstructing them from the player entity removes the guesswork
// that makes boxes jitter.
//
// ActiveRenderInfo additionally keeps copies of the frame's GL modelview and
// projection matrices in direct FloatBuffers. With them the overlay projects
// exactly like the game does: dynamic FOV (sprint, speed, bow), view bobbing,
// hurt shake, F5 — all included.
var (
	timerNames   = []string{"timer", "field_71428_T"}
	partialNames = []string{"renderPartialTicks", "field_74281_c"}
	rmNames      = []string{"renderManager", "field_175616_W"}
	viewerNames  = [][3]string{
		{"viewerPosX", "viewerPosY", "viewerPosZ"},
		{"field_78730_l", "field_78731_m", "field_78728_n"},
	}
	viewYawNames   = []string{"playerViewY", "field_78735_i"}
	viewPitchNames = []string{"playerViewX", "field_78732_j"}

	ariClasses     = []string{"net/minecraft/client/renderer/ActiveRenderInfo"}
	modelviewNames = []string{"MODELVIEW", "field_178812_b"}
	projNames      = []string{"PROJECTION", "field_178813_c"}
	renderPosNames = [][3]string{
		{"renderPosX", "renderPosY", "renderPosZ"},
		{"field_78725_b", "field_78726_c", "field_78723_d"},
	}
)

type legacyCamera struct {
	timer   *hotspot.Field // Minecraft.timer; nil if not found
	partial hotspot.Field  // Timer.renderPartialTicks

	rm         *hotspot.Field // Minecraft.renderManager; nil if not found
	viewer     [3]hotspot.Field
	yaw, pitch hotspot.Field

	mats *legacyMatrices // nil if not found
}

type legacyMatrices struct {
	ari      hotspot.Klass // ActiveRenderInfo
	mv, proj hotspot.Field // its static FloatBuffers
	addr     hotspot.Field // java.nio.Buffer.address
	origin   [3]hotspot.Field
}

// resolveLegacy looks the renderer fields up on the live Minecraft instance.
func (c *Client) resolveLegacy(mc hotspot.Oop) *legacyCamera {
	vm := c.vm
	l := &legacyCamera{}
	mcFields := vm.AllFields(c.klass)
	if f := findRef(mcFields, timerNames...); f != nil {
		if o := vm.ObjField(mc, *f); o != 0 {
			if p, ok := find(vm.AllFields(vm.KlassOf(o)), "F", partialNames...); ok {
				l.timer, l.partial = f, p
			}
		}
	}
	f := findRef(mcFields, rmNames...)
	if f == nil {
		return l
	}
	rm := vm.ObjField(mc, *f)
	if rm == 0 {
		return l
	}
	rf := vm.AllFields(vm.KlassOf(rm))
	yaw, ok1 := find(rf, "F", viewYawNames...)
	pitch, ok2 := find(rf, "F", viewPitchNames...)
	viewer, ok3 := findAny3(rf, "D", viewerNames)
	if !ok1 || !ok2 || !ok3 {
		return l
	}
	l.rm, l.viewer, l.yaw, l.pitch = f, viewer, yaw, pitch

	for _, name := range ariClasses {
		for _, k := range vm.FindClass(name) {
			mv, ok1 := findStatic(vm.DeclaredFields(k), modelviewNames...)
			proj, ok2 := findStatic(vm.DeclaredFields(k), projNames...)
			if !ok1 || !ok2 {
				continue
			}
			buf := vm.StaticObj(k, mv)
			if buf == 0 {
				continue
			}
			addr, ok := vm.FindField(vm.KlassOf(buf), "address", "J")
			if !ok {
				continue
			}
			// RenderManager.renderPos is the world origin of the modelview;
			// viewerPos is set to the same value every frame.
			origin, ok := findAny3(rf, "D", renderPosNames)
			if !ok {
				origin = viewer
			}
			l.mats = &legacyMatrices{ari: k, mv: mv, proj: proj, addr: addr, origin: origin}
			return l
		}
	}
	return l
}

// legacyFrame reads the last rendered frame's camera. self is the local
// player's position, used to reject a camera that isn't ours (spectating, ...).
func (c *Client) legacyFrame(mc hotspot.Oop, self geom.Vec3) *Frame {
	l, vm := c.legacy, c.vm
	if l == nil || l.rm == nil || l.timer == nil {
		return nil
	}
	timer, rm := vm.ObjField(mc, *l.timer), vm.ObjField(mc, *l.rm)
	if timer == 0 || rm == 0 {
		return nil
	}
	f := &Frame{
		Pos:     c.vec3(rm, l.viewer),
		Yaw:     vm.Float(rm, l.yaw),
		Pitch:   vm.Float(rm, l.pitch),
		Partial: vm.Float(timer, l.partial),
	}
	if !f.Pos.Finite() || !geom.Finite(f.Yaw, f.Pitch, f.Partial) || f.Partial < 0 || f.Partial > 1 ||
		f.Pos.Manhattan(self) > 4 {
		return nil
	}
	if m := l.mats; m != nil {
		mv, ok1 := c.bufferMatrix(m.ari, m.mv, m.addr)
		proj, ok2 := c.bufferMatrix(m.ari, m.proj, m.addr)
		if origin := c.vec3(rm, m.origin); ok1 && ok2 && origin.Finite() {
			mat := proj.Mul(mv)
			f.Matrix, f.Origin = &mat, origin
		}
	}
	return f
}

// bufferMatrix reads a matrix out of the direct FloatBuffer in static field f
// of k. ok is false if it is unreadable or still all zero.
func (c *Client) bufferMatrix(k hotspot.Klass, f, addrField hotspot.Field) (m geom.Mat4, ok bool) {
	buf := c.vm.StaticObj(k, f)
	if buf == 0 {
		return m, false
	}
	addr := uint64(c.vm.Long(buf, addrField))
	if addr == 0 {
		return m, false
	}
	b, err := c.vm.P.Bytes(addr, 64)
	if err != nil {
		return m, false
	}
	m, ok = mat4(func(i int) float64 {
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:])))
	})
	return m, ok && m != geom.Mat4{}
}
