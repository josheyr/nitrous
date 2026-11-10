package hotspot

import (
	"encoding/binary"
	"math"
	"unicode/utf16"
)

// ---------------------------------------------------------------- references

func (vm *VM) decodeOop(n uint32) Oop {
	if n == 0 {
		return 0
	}
	return Oop(vm.oopBase + uint64(n)<<vm.oopShift)
}

// uncolor strips generational-ZGC metadata from a stored reference.
// zpointer layout: [address | RRRR MMMM mm FF rr 0000]; exactly one of the
// four "remapped" bits (12..15) is set and the address starts just above it,
// so the load shift is (index of that bit)+1.
func uncolor(v uint64) uint64 {
	r := (v >> 12) & 0xF
	if r == 0 {
		return 0 // null (possibly with color bits)
	}
	tz := uint(0)
	for r&1 == 0 {
		r >>= 1
		tz++
	}
	return v >> (12 + tz + 1)
}

func (vm *VM) loadWide(v uint64) Oop {
	if vm.zgen {
		return Oop(uncolor(v))
	}
	return Oop(v)
}

// ReadOopAt reads a reference stored at addr (a field or array slot in the heap).
func (vm *VM) ReadOopAt(addr uint64) Oop {
	if vm.compOops {
		return vm.decodeOop(vm.P.U32(addr))
	}
	return vm.loadWide(vm.P.U64(addr))
}

// KlassOf returns the class of a heap object.
func (vm *VM) KlassOf(o Oop) Klass {
	if o == 0 {
		return 0
	}
	var narrow uint64
	switch {
	case vm.compact:
		narrow = vm.P.U64(uint64(o)) >> vm.markKlassShift
	case vm.compKlass:
		narrow = uint64(vm.P.U32(uint64(o) + vm.oopKlassOff))
	default:
		return Klass(vm.P.U64(uint64(o) + vm.oopKlassOff))
	}
	if narrow == 0 {
		return 0
	}
	return Klass(vm.klassBase + narrow<<vm.klassShift)
}

// ---------------------------------------------------------------- field values

// ObjField reads a reference field of o.
func (vm *VM) ObjField(o Oop, f Field) Oop { return vm.ReadOopAt(uint64(o) + f.Offset) }

// StaticObj reads a static reference field of k.
func (vm *VM) StaticObj(k Klass, f Field) Oop {
	m := vm.Mirror(k)
	if m == 0 {
		return 0
	}
	return vm.ReadOopAt(uint64(m) + f.Offset)
}

// The readers below return the value of a primitive field of o. They do not
// check f's type; a failed read yields zero.

func (vm *VM) Byte(o Oop, f Field) uint8 { return vm.P.U8(uint64(o) + f.Offset) }
func (vm *VM) Int(o Oop, f Field) int    { return int(int32(vm.P.U32(uint64(o) + f.Offset))) }
func (vm *VM) Long(o Oop, f Field) int64 { return int64(vm.P.U64(uint64(o) + f.Offset)) }

func (vm *VM) Float(o Oop, f Field) float64 {
	return float64(math.Float32frombits(vm.P.U32(uint64(o) + f.Offset)))
}

func (vm *VM) Double(o Oop, f Field) float64 {
	return math.Float64frombits(vm.P.U64(uint64(o) + f.Offset))
}

// ---------------------------------------------------------------- arrays

func (vm *VM) arrBase(elemSize uint64) uint64 {
	if elemSize >= 8 {
		return vm.arrBaseWide
	}
	return vm.arrBaseSmall
}

// ArrayLen returns the length of a Java array.
func (vm *VM) ArrayLen(o Oop) int { return int(int32(vm.P.U32(uint64(o) + vm.arrLenOff))) }

// ObjArray returns the first n elements of an Object[] (all of them if n < 0),
// but never more than max.
func (vm *VM) ObjArray(o Oop, n, max int) []Oop {
	if o == 0 {
		return nil
	}
	if l := vm.ArrayLen(o); n < 0 || n > l {
		n = l
	}
	if n > max {
		n = max
	}
	if n <= 0 {
		return nil
	}
	size := uint64(8)
	if vm.compOops {
		size = 4
	}
	raw, err := vm.P.Bytes(uint64(o)+vm.arrBase(size), n*int(size))
	if err != nil {
		return nil
	}
	out := make([]Oop, n)
	for i := range out {
		if vm.compOops {
			out[i] = vm.decodeOop(binary.LittleEndian.Uint32(raw[i*4:]))
		} else {
			out[i] = vm.loadWide(binary.LittleEndian.Uint64(raw[i*8:]))
		}
	}
	return out
}

// ---------------------------------------------------------------- java.lang / java.util

// String decodes a java.lang.String (JDK 8 char[] or JDK 9+ compact byte[]).
func (vm *VM) String(o Oop) (string, bool) {
	k := vm.KlassOf(o)
	if k == 0 || vm.Name(k) != "java/lang/String" {
		return "", false
	}
	vf, ok := vm.FindField(k, "value", "")
	if !ok {
		return "", false
	}
	arr := vm.ObjField(o, vf)
	if arr == 0 {
		return "", false
	}
	n := vm.ArrayLen(arr)
	if n < 0 || n > 4096 {
		return "", false
	}
	utf16String := func(raw []byte) string {
		u := make([]uint16, len(raw)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(raw[i*2:])
		}
		return string(utf16.Decode(u))
	}
	switch vf.Sig {
	case "[C":
		raw, err := vm.P.Bytes(uint64(arr)+vm.arrBase(2), n*2)
		if err != nil {
			return "", false
		}
		return utf16String(raw), true
	case "[B":
		raw, err := vm.P.Bytes(uint64(arr)+vm.arrBase(1), n)
		if err != nil {
			return "", false
		}
		if cf, ok := vm.FindField(k, "coder", "B"); ok && vm.Byte(o, cf) != 0 {
			return utf16String(raw), true
		}
		r := make([]rune, n) // LATIN1
		for i, c := range raw {
			r[i] = rune(c)
		}
		return string(r), true
	}
	return "", false
}

// ListElems reads elements of common java.util.List implementations.
func (vm *VM) ListElems(o Oop, max int) ([]Oop, bool) {
	for depth := 0; o != 0 && depth < 4; depth++ {
		k := vm.KlassOf(o)
		switch vm.Name(k) {
		case "java/util/ArrayList", "java/util/Vector", "java/util/Stack":
			ed, ok1 := vm.FindField(k, "elementData", "[Ljava/lang/Object;")
			sz, ok2 := vm.FindField(k, "size", "I")
			if !ok2 {
				sz, ok2 = vm.FindField(k, "elementCount", "I")
			}
			if !ok1 || !ok2 {
				return nil, false
			}
			return vm.ObjArray(vm.ObjField(o, ed), vm.Int(o, sz), max), true
		case "java/util/concurrent/CopyOnWriteArrayList":
			a, ok := vm.FindField(k, "array", "[Ljava/lang/Object;")
			if !ok {
				return nil, false
			}
			return vm.ObjArray(vm.ObjField(o, a), -1, max), true
		case "java/util/Collections$UnmodifiableRandomAccessList", "java/util/Collections$UnmodifiableList",
			"java/util/Collections$SynchronizedRandomAccessList", "java/util/Collections$SynchronizedList":
			f, ok := vm.FindField(k, "list", "Ljava/util/List;")
			if !ok {
				return nil, false
			}
			o = vm.ObjField(o, f) // unwrap and retry
		default:
			return nil, false
		}
	}
	return nil, false
}
