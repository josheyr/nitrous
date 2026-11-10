// Package hotspot reads a running HotSpot JVM from the outside, read-only.
//
// Nothing is hardcoded per JDK build: all internal layouts are resolved at
// runtime from the tables HotSpot exports for its own Serviceability Agent
// (gHotSpotVMStructs / Types / IntConstants / LongConstants), which makes it
// work across JDK 8 .. 25.
//
// The package is layered like the JVM itself:
//
//	tables.go  the exported layout tables
//	vm.go      attaching and resolving the layout of this particular JVM
//	meta.go    metadata: classes, names, fields
//	heap.go    heap objects: references, field values, arrays, strings, collections
package hotspot

import (
	"errors"

	"nitrous/internal/proc"
)

// Klass is a pointer to a HotSpot Klass (metadata, not heap).
type Klass uint64

// Oop is an uncompressed heap object address; 0 is null.
type Oop uint64

// VM is a read-only view of a HotSpot JVM.
type VM struct {
	P *proc.Process
	t *vmTables

	jdkMajor             int
	compOops, compKlass  bool
	compact              bool // -XX:+UseCompactObjectHeaders: narrow klass lives in the mark word
	markKlassShift       uint
	zgen                 bool // generational ZGC: heap refs are colored zpointers
	oopBase, klassBase   uint64
	oopShift, klassShift uint
	oopKlassOff          uint64
	arrLenOff            uint64
	arrBaseSmall         uint64 // element size <= 4
	arrBaseWide          uint64 // element size 8

	// metadata layouts
	kName, kSuper, kMirror, kLayout, kNextLink uint64
	mirrorIsHandle                             bool
	ikConstants, ikFields, ikJavaFieldsCount   uint64
	ikFieldStream                              uint64
	useFieldStream                             bool
	cpBase                                     uint64
	symLen, symBody                            uint64
	fiSlots, fiAccess, fiName, fiSig           uint64
	fiLow, fiHigh, fiTagSize                   uint64
	cldgHead, cldNext, cldKlasses              uint64

	// caches
	names     map[Klass]string
	declared  map[Klass][]Field
	inherited map[Klass][]Field
	byName    map[string][]Klass // nil until Index has run
	classes   int
}

// Attach resolves the JVM layout of an already-opened process.
func Attach(p *proc.Process) (*VM, error) {
	t, err := loadTables(p)
	if err != nil {
		return nil, err
	}
	vm := &VM{
		P: p, t: t,
		names:     map[Klass]string{},
		declared:  map[Klass][]Field{},
		inherited: map[Klass][]Field{},
	}
	if err := vm.resolveLayout(); err != nil {
		return nil, err
	}
	return vm, nil
}

func (vm *VM) resolveLayout() error {
	t, p := vm.t, vm.P
	var errs []error
	off := func(typ, name string) uint64 {
		o, err := t.offset(typ, name)
		if err != nil {
			errs = append(errs, err)
		}
		return o
	}

	vm.kName = off("Klass", "_name")
	vm.kSuper = off("Klass", "_super")
	vm.kLayout = off("Klass", "_layout_helper")
	vm.kNextLink = off("Klass", "_next_link")
	if f, ok := t.field("Klass", "_java_mirror"); ok {
		vm.kMirror = f.Offset
		vm.mirrorIsHandle = f.TypeString == "OopHandle" // JDK 11+
	} else {
		errs = append(errs, errors.New("vmStructs has no Klass::_java_mirror"))
	}
	vm.ikConstants = off("InstanceKlass", "_constants")
	if f, ok := t.field("InstanceKlass", "_fieldinfo_stream"); ok { // JDK 21+
		vm.useFieldStream = true
		vm.ikFieldStream = f.Offset
	} else {
		vm.ikFields = off("InstanceKlass", "_fields")
		vm.ikJavaFieldsCount = off("InstanceKlass", "_java_fields_count")
		constant := func(name string, def uint64) uint64 {
			if v, ok := t.ints[name]; ok {
				return uint64(v)
			}
			return def
		}
		vm.fiSlots = constant("FieldInfo::field_slots", 6)
		vm.fiAccess = constant("FieldInfo::access_flags_offset", 0)
		vm.fiName = constant("FieldInfo::name_index_offset", 1)
		vm.fiSig = constant("FieldInfo::signature_index_offset", 2)
		vm.fiLow = constant("FieldInfo::low_packed_offset", 4)
		vm.fiHigh = constant("FieldInfo::high_packed_offset", 5)
		vm.fiTagSize = constant("FIELDINFO_TAG_SIZE", 2)
	}
	if ty, ok := t.types["ConstantPool"]; ok {
		vm.cpBase = ty.Size
	} else {
		errs = append(errs, errors.New("vmTypes has no ConstantPool"))
	}
	vm.symLen = off("Symbol", "_length")
	vm.symBody = off("Symbol", "_body")

	if f, ok := t.field("ClassLoaderDataGraph", "_head"); ok {
		vm.cldgHead = f.Address
	} else {
		errs = append(errs, errors.New("vmStructs has no ClassLoaderDataGraph::_head"))
	}
	vm.cldNext = off("ClassLoaderData", "_next")
	vm.cldKlasses = off("ClassLoaderData", "_klasses")
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	vm.jdkMajor = 8
	if f, ok := t.field("Abstract_VM_Version", "_vm_major_version"); ok {
		if v := int(p.U32(f.Address)); v >= 9 && v < 1000 {
			vm.jdkMajor = v
		}
	}

	// heap reference encoding
	zgc := vm.flagBool("UseZGC", false)
	_, hasZPtr := t.field("ZGlobalsForVMStructs", "_ZPointerLoadShift")
	vm.zgen = zgc && hasZPtr && vm.flagBool("ZGenerational", true)
	vm.compOops = !zgc && vm.flagBool("UseCompressedOops", true)
	vm.compKlass = vm.flagBool("UseCompressedClassPointers", true)
	vm.compact = vm.flagBool("UseCompactObjectHeaders", false)
	if vm.compact {
		vm.compKlass = true
		vm.markKlassShift = 42
		if v, ok := t.longs["markWord::klass_shift"]; ok {
			vm.markKlassShift = uint(v)
		}
	}
	// The narrow-pointer encoding statics moved between classes across JDKs.
	narrow := func(class, kind, member string) (uint64, bool) {
		for _, c := range [][2]string{
			{class, member},
			{class, "_narrow_" + kind + "." + member},
			{"Universe", "_narrow_" + kind + "." + member},
		} {
			if a, ok := t.static(c[0], c[1]); ok {
				return a, true
			}
		}
		return 0, false
	}
	if a, ok := narrow("CompressedOops", "oop", "_base"); ok {
		vm.oopBase = p.U64(a)
	}
	if a, ok := narrow("CompressedOops", "oop", "_shift"); ok {
		vm.oopShift = uint(p.U32(a))
	}
	if a, ok := narrow("CompressedKlassPointers", "klass", "_base"); ok {
		vm.klassBase = p.U64(a)
	}
	if a, ok := narrow("CompressedKlassPointers", "klass", "_shift"); ok {
		vm.klassShift = uint(p.U32(a))
	}

	vm.oopKlassOff = 8
	if f, ok := t.field("oopDesc", "_metadata._compressed_klass"); ok && vm.compKlass {
		vm.oopKlassOff = f.Offset
	} else if f, ok := t.field("oopDesc", "_metadata._klass"); ok {
		vm.oopKlassOff = f.Offset
	}
	switch {
	case vm.compact:
		vm.arrLenOff = 8
	case vm.compKlass:
		vm.arrLenOff = 12
	default:
		vm.arrLenOff = 16
	}
	vm.arrBaseWide = (vm.arrLenOff + 4 + 7) &^ 7 // 8-aligned
	vm.arrBaseSmall = vm.arrBaseWide
	if vm.jdkMajor >= 22 { // JDK-8139457: small-element arrays no longer 8-aligned
		vm.arrBaseSmall = vm.arrLenOff + 4
	}
	return nil
}

// flagBool reads a bool -XX flag from the JVM flag table.
func (vm *VM) flagBool(name string, def bool) bool {
	for _, typ := range []string{"JVMFlag", "Flag"} {
		flags, ok1 := vm.t.field(typ, "flags")
		num, ok2 := vm.t.field(typ, "numFlags")
		nameF, ok3 := vm.t.field(typ, "_name")
		addrF, ok4 := vm.t.field(typ, "_addr")
		ty, ok5 := vm.t.types[typ]
		if !(ok1 && ok2 && ok3 && ok4 && ok5) {
			continue
		}
		arr := vm.P.U64(flags.Address)
		n := min(vm.P.U64(num.Address), 10000)
		for i := uint64(0); i < n; i++ {
			e := arr + i*ty.Size
			if vm.P.CString(vm.P.U64(e+nameF.Offset)) == name {
				return vm.P.U8(vm.P.U64(e+addrF.Offset)) != 0
			}
		}
	}
	return def
}
