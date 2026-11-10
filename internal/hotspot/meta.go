package hotspot

import "encoding/binary"

// Field is a resolved Java field.
type Field struct {
	Name   string
	Sig    string // JVM descriptor, e.g. "Ljava/lang/String;"
	Offset uint64
	Static bool
}

// IsObject reports whether the field holds a reference to a class instance
// (not a primitive or an array).
func (f Field) IsObject() bool { return len(f.Sig) > 0 && f.Sig[0] == 'L' }

// ClassName is the internal name of the field's declared class, e.g.
// "java/lang/String"; "" if the field is not an object reference.
func (f Field) ClassName() string {
	if len(f.Sig) < 3 || f.Sig[0] != 'L' {
		return ""
	}
	return f.Sig[1 : len(f.Sig)-1]
}

const (
	accStatic  = 0x0008
	maxInherit = 64 // superclass chain guard
)

// ---------------------------------------------------------------- classes

// Super returns k's superclass, 0 for java/lang/Object.
func (vm *VM) Super(k Klass) Klass { return Klass(vm.P.U64(uint64(k) + vm.kSuper)) }

// Mirror returns the java.lang.Class object for k (where static fields live).
func (vm *VM) Mirror(k Klass) Oop {
	v := vm.P.U64(uint64(k) + vm.kMirror)
	if !vm.mirrorIsHandle {
		return Oop(v)
	}
	if v == 0 {
		return 0
	}
	return vm.loadWide(vm.P.U64(v)) // OopStorage slots are colored under ZGC too
}

func (vm *VM) isInstanceKlass(k Klass) bool {
	return int32(vm.P.U32(uint64(k)+vm.kLayout)) >= 0
}

func (vm *VM) symbol(sym uint64) string {
	if sym == 0 {
		return ""
	}
	n := int(vm.P.U16(sym + vm.symLen))
	if n == 0 {
		return ""
	}
	b, err := vm.P.Bytes(sym+vm.symBody, n)
	if err != nil {
		return ""
	}
	return string(b)
}

// Name returns the internal class name, e.g. "net/minecraft/client/Minecraft".
func (vm *VM) Name(k Klass) string {
	if k == 0 {
		return ""
	}
	if n, ok := vm.names[k]; ok {
		return n
	}
	n := vm.symbol(vm.P.U64(uint64(k) + vm.kName))
	vm.names[k] = n
	return n
}

// Index walks every class loader's class list once, indexes the classes by
// name and returns how many there are.
func (vm *VM) Index() int {
	if vm.byName != nil {
		return vm.classes
	}
	const guard = 1 << 20
	vm.byName = map[string][]Klass{}
	cld := vm.P.U64(vm.cldgHead)
	for i := 0; cld != 0 && i < guard; i++ {
		k := vm.P.U64(cld + vm.cldKlasses)
		for j := 0; k != 0 && j < guard; j++ {
			if n := vm.Name(Klass(k)); n != "" {
				vm.byName[n] = append(vm.byName[n], Klass(k))
				vm.classes++
			}
			k = vm.P.U64(k + vm.kNextLink)
		}
		cld = vm.P.U64(cld + vm.cldNext)
	}
	return vm.classes
}

// FindClass returns every loaded class with this internal name (one per loader).
func (vm *VM) FindClass(name string) []Klass {
	vm.Index()
	return vm.byName[name]
}

// AllClasses calls fn for every indexed class until it returns false.
func (vm *VM) AllClasses(fn func(name string, k Klass) bool) {
	vm.Index()
	for n, ks := range vm.byName {
		for _, k := range ks {
			if !fn(n, k) {
				return
			}
		}
	}
}

// ---------------------------------------------------------------- fields

// DeclaredFields returns the fields declared directly by k. The result is
// cached and must not be modified.
func (vm *VM) DeclaredFields(k Klass) []Field {
	if fs, ok := vm.declared[k]; ok {
		return fs
	}
	var fs []Field
	if k != 0 && vm.isInstanceKlass(k) {
		if vm.useFieldStream {
			fs = vm.fieldsStream(k)
		} else {
			fs = vm.fieldsArray(k)
		}
	}
	vm.declared[k] = fs
	return fs
}

// AllFields returns the fields of k followed by those of its superclasses.
// The result is cached and must not be modified.
func (vm *VM) AllFields(k Klass) []Field {
	if fs, ok := vm.inherited[k]; ok {
		return fs
	}
	var fs []Field
	for s, d := k, 0; s != 0 && d < maxInherit; s, d = vm.Super(s), d+1 {
		fs = append(fs, vm.DeclaredFields(s)...)
	}
	vm.inherited[k] = fs
	return fs
}

// FindField searches k and its superclasses for a field called name; an empty
// sig matches any type.
func (vm *VM) FindField(k Klass, name, sig string) (Field, bool) {
	for _, f := range vm.AllFields(k) {
		if f.Name == name && (sig == "" || f.Sig == sig) {
			return f, true
		}
	}
	return Field{}, false
}

func (vm *VM) cpSymbol(cp, idx uint64) string {
	return vm.symbol(vm.P.U64(cp+vm.cpBase+idx*8) &^ 1)
}

// JDK 8..20: Array<u2> of 6-slot FieldInfo tuples.
func (vm *VM) fieldsArray(k Klass) []Field {
	cp := vm.P.U64(uint64(k) + vm.ikConstants)
	arr := vm.P.U64(uint64(k) + vm.ikFields)
	n := uint64(vm.P.U16(uint64(k) + vm.ikJavaFieldsCount))
	if cp == 0 || arr == 0 || n == 0 || n > 4096 {
		return nil
	}
	raw, err := vm.P.Bytes(arr+4, int(n*vm.fiSlots*2))
	if err != nil {
		return nil
	}
	u2 := func(i uint64) uint64 { return uint64(binary.LittleEndian.Uint16(raw[i*2:])) }
	out := make([]Field, 0, n)
	for i := uint64(0); i < n; i++ {
		b := i * vm.fiSlots
		low, high := u2(b+vm.fiLow), u2(b+vm.fiHigh)
		if low&3 != 1 { // FIELDINFO_TAG_OFFSET: offset not assigned yet
			continue
		}
		out = append(out, Field{
			Name:   vm.cpSymbol(cp, u2(b+vm.fiName)),
			Sig:    vm.cpSymbol(cp, u2(b+vm.fiSig)),
			Offset: (high<<16 | low) >> vm.fiTagSize,
			Static: u2(b+vm.fiAccess)&accStatic != 0,
		})
	}
	return out
}

// JDK 21+: UNSIGNED5-compressed FieldInfo stream.
func (vm *VM) fieldsStream(k Klass) []Field {
	cp := vm.P.U64(uint64(k) + vm.ikConstants)
	s := vm.P.U64(uint64(k) + vm.ikFieldStream)
	if cp == 0 || s == 0 {
		return nil
	}
	n := int(int32(vm.P.U32(s)))
	if n <= 0 || n > 1<<20 {
		return nil
	}
	data, err := vm.P.Bytes(s+4, n)
	if err != nil {
		return nil
	}
	pos := 0
	next := func() uint64 { // one UNSIGNED5 value
		const lgH, X, L, maxB = 6, 1, 191, 5
		if pos >= len(data) {
			return 0
		}
		b := uint64(data[pos])
		pos++
		sum := b - X
		if sum < L {
			return sum
		}
		shift := uint(lgH)
		for i := 1; ; i++ {
			if pos >= len(data) {
				return sum
			}
			b = uint64(data[pos])
			pos++
			sum += (b - X) << shift
			if b < X+L || i == maxB-1 {
				break
			}
			shift += lgH
		}
		return sum
	}
	javaFields := next()
	next() // injected fields count
	// k can be garbage (a stale reference under a concurrent GC): every field
	// takes at least five bytes of stream, so more than that is not a count.
	if javaFields > uint64(len(data))/5 {
		return nil
	}
	out := make([]Field, 0, javaFields)
	for i := uint64(0); i < javaFields && pos < len(data); i++ {
		nameIdx, sigIdx, offset, access, flags := next(), next(), next(), next(), next()
		for _, optional := range []uint64{1 << 0, 1 << 2, 1 << 4} { // initializer, generic signature, contended group
			if flags&optional != 0 {
				next()
			}
		}
		out = append(out, Field{
			Name:   vm.cpSymbol(cp, nameIdx),
			Sig:    vm.cpSymbol(cp, sigIdx),
			Offset: offset,
			Static: access&accStatic != 0,
		})
	}
	return out
}
