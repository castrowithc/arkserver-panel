// Reader for the property tree ARK writes into its save files. A .arkprofile and a .arktribe are
// both a flat list of named, typed properties, and that list is all this file understands: enough to
// answer who is in a save game, and deliberately not enough to write one back.
//
// The layout of one property, measured against the real files of this deployment:
//
//	name   int32 length + bytes, "None" ends the list
//	type   int32 length + bytes
//	size   int32, the length of the value
//	index  int32, which repeats a name instead of nesting an array
//	       StructProperty and ArrayProperty carry one more name here, then size bytes
//	       everything else: size bytes of value
//
// The index is what carries the per-stat numbers: the same name appears once per stat and only the
// index tells them apart.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// props is one property list, keyed by name and index. A name that appears once sits at index 0,
// which is what the file itself says, so nothing needs to special-case the common case.
type props struct {
	values map[propKey]any
	// order keeps the names in the order the file had them, for the few callers that want to walk
	// rather than look up.
	order []propKey
}

type propKey struct {
	Name  string
	Index int32
}

// errShortRead is returned for every truncated or malformed read. The caller never needs to tell
// them apart: the answer is the same, the file cannot be believed and is reported as unreadable.
var errShortRead = errors.New("Datei endet mitten in einer Eigenschaft")

var errNoProps = errors.New("keine Eigenschaftsliste in der Datei gefunden")

// readProps finds the property list in a save file and parses it. The header before the list is not
// parsed at all: it differs by file type, and the .arktribe variant of it cannot be checked against
// anything here, so reading it would be guesswork holding up everything behind it. The list itself
// is unmistakable and is found by looking for it.
func readProps(b []byte, nested map[string]bool) (props, error) {
	off, ok := findProps(b)
	if !ok {
		return props{}, errNoProps
	}
	return parseProps(b, off, nested)
}

// findProps returns the offset of the first property header in the file. A property header is a
// name, then a type ending in "Property", then two plausible integers, and nothing in the header
// before it looks like that: its name table is strings back to back, so the second string there is
// another name rather than a type.
func findProps(b []byte) (int, bool) {
	for off := 0; off+8 < len(b); off++ {
		r := &reader{b: b, off: off}
		name, err := r.str()
		if err != nil || !plausibleName(name) {
			continue
		}
		typ, err := r.str()
		if err != nil || !strings.HasSuffix(typ, "Property") || !plausibleName(typ) {
			continue
		}
		size, err := r.i32()
		if err != nil || size < 0 {
			continue
		}
		index, err := r.i32()
		if err != nil || index < 0 {
			continue
		}
		if r.off+int(size) > len(b) {
			continue
		}
		return off, true
	}
	return 0, false
}

// plausibleName keeps the search from locking onto a run of bytes that merely decodes. Property
// names are short, printable and never empty.
func plausibleName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if c < ' ' || c > '~' {
			return false
		}
	}
	return true
}

// parseProps reads a property list starting at the given offset and stopping at the end of the
// slice or at the "None" that terminates it. Structures are entered only when the caller asks for
// them by name, because entering everything would mean understanding every struct ARK has, and most
// of them are raw numbers with no property list inside.
func parseProps(b []byte, off int, nested map[string]bool) (props, error) {
	p := props{values: map[propKey]any{}}
	r := &reader{b: b, off: off}
	for r.off < len(b) {
		name, err := r.str()
		if err != nil {
			return p, err
		}
		if name == "None" || name == "" {
			return p, nil
		}
		typ, err := r.str()
		if err != nil {
			return p, err
		}
		size, err := r.i32()
		if err != nil {
			return p, err
		}
		index, err := r.i32()
		if err != nil {
			return p, err
		}
		if size < 0 || r.off+int(size) > len(b) {
			return p, errShortRead
		}
		key := propKey{Name: name, Index: index}
		value, err := r.value(typ, int(size), name, nested)
		if err != nil {
			return p, err
		}
		if _, seen := p.values[key]; !seen {
			p.order = append(p.order, key)
		}
		p.values[key] = value
		if r.off > len(b) {
			return p, errShortRead
		}
	}
	// Running out of bytes without a "None" is not an error worth refusing over: everything read so
	// far is intact, and the caller only ever asks for names it expects.
	return p, nil
}

// value reads one property's payload and leaves the reader on the next property. A type this does
// not know is skipped by its size, which is exactly why the size is in the file.
func (r *reader) value(typ string, size int, name string, nested map[string]bool) (any, error) {
	switch typ {
	case "StrProperty":
		end := r.off + size
		s, err := r.str()
		r.off = end
		return s, err
	case "IntProperty":
		return r.skipTo(size, func(b []byte) any { return int64(int32(binary.LittleEndian.Uint32(b))) })
	case "UInt32Property":
		return r.skipTo(size, func(b []byte) any { return int64(binary.LittleEndian.Uint32(b)) })
	case "UInt16Property":
		return r.skipTo(size, func(b []byte) any { return int64(binary.LittleEndian.Uint16(b)) })
	case "UInt64Property":
		return r.skipTo(size, func(b []byte) any { return int64(binary.LittleEndian.Uint64(b)) })
	case "FloatProperty":
		return r.skipTo(size, func(b []byte) any {
			return float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
		})
	case "DoubleProperty":
		return r.skipTo(size, func(b []byte) any { return math.Float64frombits(binary.LittleEndian.Uint64(b)) })
	case "BoolProperty":
		// The flag sits before the payload rather than inside it, so a bool is one byte plus a size
		// that is normally zero.
		if r.off >= len(r.b) {
			return nil, errShortRead
		}
		v := r.b[r.off] != 0
		r.off += 1 + size
		return v, nil
	case "ByteProperty":
		// A byte names its enum first. "None" means it is a plain number, anything else means the
		// value is itself a name.
		enum, err := r.str()
		if err != nil {
			return nil, err
		}
		if enum != "None" {
			end := r.off + size
			s, err := r.str()
			r.off = end
			return s, err
		}
		return r.skipTo(size, func(b []byte) any { return int64(b[0]) })
	case "StructProperty", "ArrayProperty":
		inner, err := r.str()
		if err != nil {
			return nil, err
		}
		end := r.off + size
		if end > len(r.b) {
			return nil, errShortRead
		}
		if typ == "StructProperty" && nested[inner] {
			sub, err := parseProps(r.b, r.off, nested)
			r.off = end
			return sub, err
		}
		if typ == "ArrayProperty" {
			if v, ok := r.array(inner, end); ok {
				r.off = end
				return v, nil
			}
		}
		r.off = end
		return fmt.Sprintf("<%s>", inner), nil
	default:
		r.off += size
		return nil, nil
	}
}

// array reads the two kinds of array worth having: a list of strings and a list of 32-bit numbers.
// Both begin with a count. Anything else stays a skipped block, because an array of structs would
// mean understanding the struct, and none of the answers here need one.
//
// A refusal here costs nothing: the caller falls back to skipping the array by its own size, which
// is what happened to every array before this existed.
func (r *reader) array(inner string, end int) (any, bool) {
	n, err := r.i32()
	if err != nil || n < 0 {
		return nil, false
	}
	// No entry of either kind is shorter than its four-byte prefix, so a count that cannot fit is a
	// count that was misread. Without this a corrupt length would size an allocation.
	if int(n)*4 > end-r.off {
		return nil, false
	}
	switch inner {
	case "StrProperty":
		out := make([]string, 0, n)
		for i := int32(0); i < n; i++ {
			s, err := r.str()
			if err != nil || r.off > end {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case "UInt32Property", "IntProperty":
		out := make([]int64, 0, n)
		for i := int32(0); i < n; i++ {
			v, err := r.i32()
			if err != nil || r.off > end {
				return nil, false
			}
			if inner == "IntProperty" {
				out = append(out, int64(v))
			} else {
				out = append(out, int64(uint32(v)))
			}
		}
		return out, true
	}
	return nil, false
}

type reader struct {
	b   []byte
	off int
}

func (r *reader) i32() (int32, error) {
	if r.off+4 > len(r.b) {
		return 0, errShortRead
	}
	v := int32(binary.LittleEndian.Uint32(r.b[r.off:]))
	r.off += 4
	return v, nil
}

// str reads a length-prefixed string. The length counts the trailing null byte, which is dropped.
func (r *reader) str() (string, error) {
	n, err := r.i32()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	// A negative length means UTF-16 in Unreal's encoding. Nothing this reads is ever written that
	// way, and guessing at it would be worse than saying so.
	if n < 0 || r.off+int(n) > len(r.b) {
		return "", errShortRead
	}
	s := string(r.b[r.off : r.off+int(n)])
	r.off += int(n)
	return strings.TrimRight(s, "\x00"), nil
}

// skipTo decodes a fixed-width value and advances by the size the file declared, not by the width of
// the type. The two agree in every file seen, and where they would not, the file's own size is the
// one that keeps the rest of the list readable.
func (r *reader) skipTo(size int, decode func([]byte) any) (any, error) {
	if r.off+size > len(r.b) {
		return nil, errShortRead
	}
	var v any
	if size > 0 {
		v = decode(r.b[r.off:])
	}
	r.off += size
	return v, nil
}

// The lookups below all answer "absent" the same way: with the zero value and false. An absent
// property is the game's default, never a zero the file wrote down, and only the caller knows which
// default that is.

func (p props) str(name string) (string, bool) {
	v, ok := p.values[propKey{Name: name}].(string)
	return v, ok
}

func (p props) num(name string) (int64, bool) {
	v, ok := p.values[propKey{Name: name}].(int64)
	return v, ok
}

func (p props) float(name string) (float64, bool) {
	v, ok := p.values[propKey{Name: name}].(float64)
	return v, ok
}

func (p props) sub(name string) (props, bool) {
	v, ok := p.values[propKey{Name: name}].(props)
	return v, ok
}

// strs and nums answer an array property. An absent one is an empty list, which reads the same as an
// array with nothing in it and needs no separate handling anywhere.

func (p props) strs(name string) []string {
	v, _ := p.values[propKey{Name: name}].([]string)
	return v
}

func (p props) nums(name string) []int64 {
	v, _ := p.values[propKey{Name: name}].([]int64)
	return v
}

// indexed collects every occurrence of one name, keyed by its index. This is how the per-stat
// numbers come out: one property name, one entry per stat that has points in it.
func (p props) indexed(name string) map[int32]int64 {
	out := map[int32]int64{}
	for k, v := range p.values {
		if k.Name != name {
			continue
		}
		if n, ok := v.(int64); ok {
			out[k.Index] = n
		}
	}
	return out
}
