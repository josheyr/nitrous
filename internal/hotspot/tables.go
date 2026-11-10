package hotspot

import (
	"encoding/binary"
	"fmt"

	"nitrous/internal/proc"
)

// StructField is one entry from gHotSpotVMStructs.
type StructField struct {
	TypeString string // declared C++ type, e.g. "Symbol*", "OopHandle"
	Static     bool
	Offset     uint64 // for non-static fields
	Address    uint64 // for static fields
}

// VMType is one entry from gHotSpotVMTypes.
type VMType struct {
	Super string
	Size  uint64
}

// vmTables are the tables HotSpot exports for its Serviceability Agent.
type vmTables struct {
	structs map[string]map[string]StructField
	types   map[string]VMType
	ints    map[string]int64
	longs   map[string]uint64
}

// loadTables reads the tables out of the jvm.dll loaded in p.
//
// Each table "X" is an exported pointer gHotSpotVMXs to an array of entries,
// described by exported gHotSpotVMXEntry<Field>Offset values and a
// gHotSpotVMXEntryArrayStride. An entry whose first field (a name pointer) is
// null ends the array.
func loadTables(p *proc.Process) (*vmTables, error) {
	base, err := p.Module("jvm.dll")
	if err != nil {
		return nil, err
	}
	exports, err := p.Exports(base, "gHotSpotVM")
	if err != nil {
		return nil, fmt.Errorf("jvm.dll exports: %w", err)
	}
	le := binary.LittleEndian
	// every export is the address of a uint64
	val := func(name string) (uint64, error) {
		a, ok := exports["gHotSpotVM"+name]
		if !ok {
			return 0, fmt.Errorf("jvm.dll does not export gHotSpotVM%s (not a HotSpot JVM?)", name)
		}
		return p.U64(a), nil
	}
	// walk calls fn with every entry of a table and the offsets of fields in it.
	walk := func(table string, fields []string, fn func(e []byte, o []uint64)) error {
		arr, err := val(table + "s")
		if err != nil {
			return err
		}
		stride, err := val(table + "EntryArrayStride")
		if err != nil {
			return err
		}
		o := make([]uint64, len(fields))
		for i, f := range fields {
			if o[i], err = val(table + "Entry" + f + "Offset"); err != nil {
				return err
			}
		}
		for i := uint64(0); i < 1<<16; i++ {
			e, err := p.Bytes(arr+i*stride, int(stride))
			if err != nil {
				return fmt.Errorf("reading gHotSpotVM%ss entry %d: %w", table, i, err)
			}
			if le.Uint64(e[o[0]:]) == 0 {
				break
			}
			fn(e, o)
		}
		return nil
	}
	str := func(e []byte, off uint64) string { return p.CString(le.Uint64(e[off:])) }

	t := &vmTables{
		structs: map[string]map[string]StructField{},
		types:   map[string]VMType{},
		ints:    map[string]int64{},
		longs:   map[string]uint64{},
	}
	err = walk("Struct", []string{"TypeName", "FieldName", "TypeString", "IsStatic", "Offset", "Address"},
		func(e []byte, o []uint64) {
			typ := str(e, o[0])
			if t.structs[typ] == nil {
				t.structs[typ] = map[string]StructField{}
			}
			t.structs[typ][str(e, o[1])] = StructField{
				TypeString: str(e, o[2]),
				Static:     le.Uint32(e[o[3]:]) != 0,
				Offset:     le.Uint64(e[o[4]:]),
				Address:    le.Uint64(e[o[5]:]),
			}
		})
	if err != nil {
		return nil, err
	}
	err = walk("Type", []string{"TypeName", "SuperclassName", "Size"}, func(e []byte, o []uint64) {
		t.types[str(e, o[0])] = VMType{Super: str(e, o[1]), Size: le.Uint64(e[o[2]:])}
	})
	if err != nil {
		return nil, err
	}
	err = walk("IntConstant", []string{"Name", "Value"}, func(e []byte, o []uint64) {
		t.ints[str(e, o[0])] = int64(int32(le.Uint32(e[o[1]:])))
	})
	if err != nil {
		return nil, err
	}
	err = walk("LongConstant", []string{"Name", "Value"}, func(e []byte, o []uint64) {
		t.longs[str(e, o[0])] = le.Uint64(e[o[1]:])
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (t *vmTables) field(typ, name string) (StructField, bool) {
	f, ok := t.structs[typ][name]
	return f, ok
}

func (t *vmTables) offset(typ, name string) (uint64, error) {
	f, ok := t.structs[typ][name]
	if !ok {
		return 0, fmt.Errorf("vmStructs has no %s::%s", typ, name)
	}
	return f.Offset, nil
}

// static returns the address of typ::name if it is a static field.
func (t *vmTables) static(typ, name string) (uint64, bool) {
	f, ok := t.structs[typ][name]
	return f.Address, ok && f.Static
}
