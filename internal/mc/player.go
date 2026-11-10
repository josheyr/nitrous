package mc

import (
	"fmt"
	"math"
	"slices"

	"nitrous/internal/geom"
	"nitrous/internal/hotspot"
)

// Player is one player entity, as of the sample it came from.
type Player struct {
	Ref hotspot.Oop // the entity object

	Name string
	UUID string // "" if unreadable

	// HasPos reports whether the position fields below could be read.
	HasPos bool
	// Pos is the feet position as of the current client tick, Prev the tick
	// before (equal to Pos after a teleport). Use At to interpolate.
	Pos, Prev geom.Vec3
	// Width and Height are the hitbox dimensions, e.g. 0.6 × 1.8 standing.
	Width, Height float64

	// Distance from the local player in blocks; +Inf if either position is
	// unknown. Always 0 for the local player.
	Distance float64
}

// At is the feet position partial (0..1) of the way through the current tick.
func (p *Player) At(partial float64) geom.Vec3 { return p.Prev.Lerp(p.Pos, partial) }

// Bounds is the hitbox, interpolated like At.
func (p *Player) Bounds(partial float64) geom.Box {
	return geom.Upright(p.At(partial), p.Width, p.Height)
}

const (
	defaultWidth  = 0.6
	defaultHeight = 1.8
	maxCoord      = 3.1e7 // beyond the world border: not a position
	maxSize       = 16    // sanity bound on entity dimensions
	teleportJump  = 8     // tick-to-tick movement beyond this is a teleport
)

// player reads who o is. Name is "" if the profile is unreadable.
func (c *Client) player(o hotspot.Oop) *Player {
	vm := c.vm
	p := &Player{Ref: o, Distance: math.Inf(1), Width: defaultWidth, Height: defaultHeight}
	pf := c.profileField(vm.KlassOf(o))
	if pf == nil {
		return p
	}
	profile := vm.ObjField(o, *pf)
	pk := vm.KlassOf(profile)
	nf, ok := vm.FindField(pk, "name", "Ljava/lang/String;")
	if !ok {
		return p
	}
	if p.Name, ok = vm.String(vm.ObjField(profile, nf)); !ok {
		return p
	}
	if idf, ok := vm.FindField(pk, "id", "Ljava/util/UUID;"); ok {
		if u := vm.ObjField(profile, idf); u != 0 {
			uk := vm.KlassOf(u)
			hi, ok1 := vm.FindField(uk, "mostSigBits", "J")
			lo, ok2 := vm.FindField(uk, "leastSigBits", "J")
			if ok1 && ok2 {
				m, l := uint64(vm.Long(u, hi)), uint64(vm.Long(u, lo))
				p.UUID = fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", m>>32, (m>>16)&0xffff, m&0xffff, l>>48, l&0xffffffffffff)
			}
		}
	}
	return p
}

// locate fills in where p is and how big.
func (c *Client) locate(p *Player) {
	if c.pos == nil {
		return
	}
	pos, ok := c.position(c.pos, p.Ref)
	if !ok {
		return
	}
	p.HasPos, p.Pos, p.Prev = true, pos, pos
	l := c.ent
	if l == nil {
		return
	}
	switch {
	case l.width != nil && l.height != nil:
		p.setSize(c.vm.Float(p.Ref, *l.width), c.vm.Float(p.Ref, *l.height))
	case l.dims != nil:
		if d := c.vm.ObjField(p.Ref, *l.dims); d != 0 {
			p.setSize(c.vm.Float(d, l.dimW), c.vm.Float(d, l.dimH)) // e.g. 1.5 tall while crouching
		}
	}
	if l.prev != nil {
		// a teleport/respawn gives a huge jump: don't smear the player across it
		if prev := c.vec3(p.Ref, *l.prev); prev.Finite() && prev.Manhattan(pos) < teleportJump {
			p.Prev = prev
		}
	}
}

func (p *Player) setSize(w, h float64) {
	if validSize(w) && validSize(h) {
		p.Width, p.Height = w, h
	}
}

func validSize(v float64) bool { return geom.Finite(v) && v > 0 && v < maxSize }

// ---------------------------------------------------------------- positions

// posLayout is either a Vec3-typed field holding x/y/z doubles (1.16+), or
// three double fields directly on Entity (<= 1.15).
type posLayout struct {
	vec *hotspot.Field // nil: xyz are on the entity itself
	xyz [3]hotspot.Field
}

var (
	vecFieldNames = []string{"position", "field_22467"} // Mojang, Fabric intermediary
	vecXYZNames   = [][3]string{{"x", "y", "z"}, {"field_1352", "field_1351", "field_1350"}}
	entityXYZ     = [][3]string{
		{"posX", "posY", "posZ"},                            // MCP
		{"field_70165_t", "field_70163_u", "field_70161_v"}, // SRG (Forge 1.8.9 - 1.12)
	}
)

// resolvePos works out where entities keep their position, checking each
// candidate against the local player. Nil if none yields a sane position.
func (c *Client) resolvePos(self hotspot.Oop) *posLayout {
	vm := c.vm
	k := vm.KlassOf(self)
	fields := vm.AllFields(k)
	works := func(l *posLayout) bool {
		_, ok := c.position(l, self)
		return ok
	}

	// 1.16+: Vec3 position field
	for _, f := range fields {
		if f.Static || !f.IsObject() || !slices.Contains(vecFieldNames, f.Name) {
			continue
		}
		vk := vm.KlassOf(vm.ObjField(self, f))
		for _, names := range vecXYZNames {
			if xyz, ok := find3(vm.AllFields(vk), "D", names); ok {
				if l := (&posLayout{vec: &f, xyz: xyz}); works(l) {
					return l
				}
			}
		}
		if ds := doubles(vm.DeclaredFields(vk)); len(ds) == 3 {
			if l := (&posLayout{vec: &f, xyz: [3]hotspot.Field(ds)}); works(l) {
				return l
			}
		}
	}

	// <= 1.15: doubles on Entity
	for _, names := range entityXYZ {
		if xyz, ok := find3(fields, "D", names); ok {
			if l := (&posLayout{xyz: xyz}); works(l) {
				return l
			}
		}
	}

	// obfuscated vanilla <= 1.15: Entity declares prevPosX/Y/Z then posX/Y/Z as
	// its first six instance doubles.
	root := k
	for s := vm.Super(root); s != 0 && vm.Name(s) != "java/lang/Object"; s = vm.Super(s) {
		root = s
	}
	if ds := doubles(vm.DeclaredFields(root)); len(ds) >= 6 {
		if l := (&posLayout{xyz: [3]hotspot.Field(ds[3:6])}); works(l) {
			return l
		}
	}
	return nil
}

// position reads an entity's position; ok is false if it isn't a sane one.
func (c *Client) position(l *posLayout, o hotspot.Oop) (geom.Vec3, bool) {
	if l.vec != nil {
		if o = c.vm.ObjField(o, *l.vec); o == 0 {
			return geom.Vec3{}, false
		}
	}
	p := c.vec3(o, l.xyz)
	for _, v := range []float64{p.X, p.Y, p.Z} {
		if !geom.Finite(v) || math.Abs(v) > maxCoord {
			return geom.Vec3{}, false
		}
	}
	return p, true
}

// ---------------------------------------------------------------- entity fields

// Entity field names per mapping set: MCP, SRG (Forge), Mojang, Fabric intermediary.
var (
	yawNames       = []string{"rotationYaw", "field_70177_z", "yRot", "field_6031"}
	pitchNames     = []string{"rotationPitch", "field_70125_A", "xRot", "field_5965"}
	prevYawNames   = []string{"prevRotationYaw", "field_70126_B", "yRotO", "field_5982"}
	prevPitchNames = []string{"prevRotationPitch", "field_70127_C", "xRotO", "field_6004"}
	widthNames     = []string{"width", "field_70130_N"}
	heightNames    = []string{"height", "field_70131_O"}
	ticksNames     = []string{"ticksExisted", "field_70173_aa", "tickCount", "field_6012"}
	// lastTickPos first: it is what the renderer interpolates from.
	prevPosNames = [][3]string{
		{"lastTickPosX", "lastTickPosY", "lastTickPosZ"},
		{"field_70142_S", "field_70137_T", "field_70136_U"},
		{"xOld", "yOld", "zOld"},
		{"field_6038", "field_5971", "field_5989"},
		{"prevPosX", "prevPosY", "prevPosZ"},
		{"field_70169_q", "field_70167_r", "field_70166_s"},
		{"xo", "yo", "zo"},
		{"field_6014", "field_6036", "field_5969"},
	}
)

// entLayout is where entities keep rotation, size and tick state. Without
// yaw and pitch nothing else is resolved either.
type entLayout struct {
	yaw, pitch         *hotspot.Field
	prevYaw, prevPitch *hotspot.Field
	width, height      *hotspot.Field // <= 1.13
	ticks              *hotspot.Field
	prev               *[3]hotspot.Field
	// 1.14+: Entity.dimensions (EntityDimensions) holds width/height.
	dims       *hotspot.Field
	dimW, dimH hotspot.Field
}

func (c *Client) resolveEnt(self hotspot.Oop) *entLayout {
	vm := c.vm
	fields := vm.AllFields(vm.KlassOf(self))
	l := &entLayout{yaw: findPtr(fields, "F", yawNames...), pitch: findPtr(fields, "F", pitchNames...)}
	if l.yaw == nil || l.pitch == nil {
		return &entLayout{}
	}
	l.prevYaw, l.prevPitch = findPtr(fields, "F", prevYawNames...), findPtr(fields, "F", prevPitchNames...)
	l.width, l.height = findPtr(fields, "F", widthNames...), findPtr(fields, "F", heightNames...)
	l.ticks = findPtr(fields, "I", ticksNames...)
	if prev, ok := findAny3(fields, "D", prevPosNames); ok {
		l.prev = &prev
	}
	if l.width == nil || l.height == nil {
		if fd := findRef(fields, "dimensions"); fd != nil {
			if d := vm.ObjField(self, *fd); d != 0 {
				df := vm.AllFields(vm.KlassOf(d))
				w, ok1 := find(df, "F", "width")
				h, ok2 := find(df, "F", "height")
				if ok1 && ok2 {
					l.dims, l.dimW, l.dimH = fd, w, h
				}
			}
		}
	}
	return l
}
