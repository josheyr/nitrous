// Package proc provides read-only access to another process's memory on Windows.
package proc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Process is a read-only handle to a target process.
type Process struct {
	PID    uint32
	handle windows.Handle
}

// Info describes a running process.
type Info struct {
	PID  uint32
	Name string
}

// Open opens pid with VM_READ access only. Nothing in this package ever writes.
func Open(pid uint32) (*Process, error) {
	h, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	return &Process{PID: pid, handle: h}, nil
}

func (p *Process) Close() error { return windows.CloseHandle(p.handle) }

// Alive reports whether the process is still running.
func (p *Process) Alive() bool {
	var code uint32
	if err := windows.GetExitCodeProcess(p.handle, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

var ErrRead = errors.New("read failed")

// Read fills buf from addr. Partial reads are treated as failures.
func (p *Process) Read(addr uint64, buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	if addr < 0x10000 {
		return ErrRead
	}
	var n uintptr
	err := windows.ReadProcessMemory(p.handle, uintptr(addr), &buf[0], uintptr(len(buf)), &n)
	if err != nil || int(n) != len(buf) {
		return ErrRead
	}
	return nil
}

func (p *Process) Bytes(addr uint64, n int) ([]byte, error) {
	b := make([]byte, n)
	return b, p.Read(addr, b)
}

func (p *Process) U8(addr uint64) uint8 {
	var b [1]byte
	if p.Read(addr, b[:]) != nil {
		return 0
	}
	return b[0]
}

func (p *Process) U16(addr uint64) uint16 {
	var b [2]byte
	if p.Read(addr, b[:]) != nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b[:])
}

func (p *Process) U32(addr uint64) uint32 {
	var b [4]byte
	if p.Read(addr, b[:]) != nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b[:])
}

func (p *Process) U64(addr uint64) uint64 {
	var b [8]byte
	if p.Read(addr, b[:]) != nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b[:])
}

// CString reads a NUL-terminated string (max 512 bytes), in small chunks so
// it does not fail when the string sits near the end of a mapped page.
func (p *Process) CString(addr uint64) string {
	if addr == 0 {
		return ""
	}
	var sb strings.Builder
	var chunk [32]byte
	for sb.Len() < 512 {
		if p.Read(addr+uint64(sb.Len()), chunk[:]) != nil {
			// fall back to byte-at-a-time near page boundaries
			c := p.U8(addr + uint64(sb.Len()))
			if c == 0 {
				break
			}
			sb.WriteByte(c)
			continue
		}
		for _, c := range chunk {
			if c == 0 {
				return sb.String()
			}
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// Module returns the base address of a loaded module (case-insensitive name).
func (p *Process) Module(name string) (uint64, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, p.PID)
	if err != nil {
		return 0, fmt.Errorf("module snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	for err = windows.Module32First(snap, &me); err == nil; err = windows.Module32Next(snap, &me) {
		if strings.EqualFold(windows.UTF16ToString(me.Module[:]), name) {
			return uint64(me.ModBaseAddr), nil
		}
	}
	return 0, fmt.Errorf("module %s not loaded in pid %d", name, p.PID)
}

// Exports parses the PE export table of a module mapped in the target and
// returns every named export whose name has the given prefix.
func (p *Process) Exports(base uint64, prefix string) (map[string]uint64, error) {
	if p.U16(base) != 0x5A4D {
		return nil, errors.New("bad DOS header")
	}
	nt := base + uint64(p.U32(base+0x3C))
	if p.U32(nt) != 0x4550 {
		return nil, errors.New("bad NT header")
	}
	opt := nt + 24
	if p.U16(opt) != 0x20B {
		return nil, errors.New("not a PE32+ (64-bit) module")
	}
	expRVA := uint64(p.U32(opt + 112))
	if expRVA == 0 {
		return nil, errors.New("no export directory")
	}
	dir, err := p.Bytes(base+expRVA, 40)
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	nNames := le.Uint32(dir[24:])
	funcs := base + uint64(le.Uint32(dir[28:]))
	names := base + uint64(le.Uint32(dir[32:]))
	ords := base + uint64(le.Uint32(dir[36:]))

	nameRVAs, err := p.Bytes(names, int(nNames)*4)
	if err != nil {
		return nil, err
	}
	ordTab, err := p.Bytes(ords, int(nNames)*2)
	if err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for i := uint32(0); i < nNames; i++ {
		n := p.CString(base + uint64(le.Uint32(nameRVAs[i*4:])))
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		ord := uint64(le.Uint16(ordTab[i*2:]))
		out[n] = base + uint64(p.U32(funcs+ord*4))
	}
	return out, nil
}

// List returns all running processes.
func List() ([]Info, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	var out []Info
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		out = append(out, Info{PID: pe.ProcessID, Name: windows.UTF16ToString(pe.ExeFile[:])})
	}
	return out, nil
}
