package mc

import (
	"nitrous/internal/geom"
	"nitrous/internal/hotspot"
)

// find returns the first of names that is an instance field with the given
// descriptor ("" = any). Earlier names win over field order.
func find(fields []hotspot.Field, sig string, names ...string) (hotspot.Field, bool) {
	for _, n := range names {
		for _, f := range fields {
			if !f.Static && f.Name == n && (sig == "" || f.Sig == sig) {
				return f, true
			}
		}
	}
	return hotspot.Field{}, false
}

// findPtr is find for optional fields: nil if there is none.
func findPtr(fields []hotspot.Field, sig string, names ...string) *hotspot.Field {
	if f, ok := find(fields, sig, names...); ok {
		return &f
	}
	return nil
}

// findRef is findPtr for a field that must be an object reference.
func findRef(fields []hotspot.Field, names ...string) *hotspot.Field {
	if f, ok := find(fields, "", names...); ok && f.IsObject() {
		return &f
	}
	return nil
}

// find3 finds three same-typed fields, e.g. an x/y/z triple.
func find3(fields []hotspot.Field, sig string, names [3]string) (out [3]hotspot.Field, ok bool) {
	for i, n := range names {
		if out[i], ok = find(fields, sig, n); !ok {
			return out, false
		}
	}
	return out, true
}

// findAny3 tries each name triple in turn.
func findAny3(fields []hotspot.Field, sig string, triples [][3]string) ([3]hotspot.Field, bool) {
	for _, names := range triples {
		if out, ok := find3(fields, sig, names); ok {
			return out, true
		}
	}
	return [3]hotspot.Field{}, false
}

// findStatic returns the first of names that is a static field.
func findStatic(fields []hotspot.Field, names ...string) (hotspot.Field, bool) {
	for _, n := range names {
		for _, f := range fields {
			if f.Static && f.Name == n {
				return f, true
			}
		}
	}
	return hotspot.Field{}, false
}

// doubles returns the instance fields of type double, in declaration order.
func doubles(fields []hotspot.Field) []hotspot.Field {
	var out []hotspot.Field
	for _, f := range fields {
		if !f.Static && f.Sig == "D" {
			out = append(out, f)
		}
	}
	return out
}

// vec3 reads three double fields of o.
func (c *Client) vec3(o hotspot.Oop, f [3]hotspot.Field) geom.Vec3 {
	return geom.Vec3{X: c.vm.Double(o, f[0]), Y: c.vm.Double(o, f[1]), Z: c.vm.Double(o, f[2])}
}
