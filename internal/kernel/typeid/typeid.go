package typeid

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const (
	alphabet  = "0123456789abcdefghjkmnpqrstvwxyz"
	suffixLen = 26
)

var ErrInvalid = errors.New("typeid: invalid id")

var decodeTable = func() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = 0xff
	}
	for i := range len(alphabet) {
		t[alphabet[i]] = byte(i)
	}
	return t
}()

func Encode(prefix string, id uuid.UUID) string {
	var out [suffixLen]byte
	for i := range suffixLen {
		var v byte
		for b := range 5 {
			bit := 5*i + b - 2
			v <<= 1
			if bit >= 0 && id[bit/8]&(0x80>>(bit%8)) != 0 {
				v |= 1
			}
		}
		out[i] = alphabet[v]
	}
	return prefix + "_" + string(out[:])
}

func Parse(prefix, s string) (uuid.UUID, error) {
	suffix, ok := strings.CutPrefix(s, prefix+"_")
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: expected a %s_ id", ErrInvalid, prefix)
	}
	if len(suffix) != suffixLen || suffix[0] > '7' {
		return uuid.Nil, fmt.Errorf("%w: malformed %s_ id", ErrInvalid, prefix)
	}
	var id uuid.UUID
	for i := range suffixLen {
		v := decodeTable[suffix[i]]
		if v == 0xff {
			return uuid.Nil, fmt.Errorf("%w: malformed %s_ id", ErrInvalid, prefix)
		}
		for b := range 5 {
			bit := 5*i + b - 2
			if bit >= 0 && v&(0x10>>b) != 0 {
				id[bit/8] |= 0x80 >> (bit % 8)
			}
		}
	}
	return id, nil
}

type Prefix interface {
	Prefix() string
}

type ID[P Prefix] uuid.UUID

func (id ID[P]) UUID() uuid.UUID {
	return uuid.UUID(id)
}

func (id ID[P]) String() string {
	var p P
	return Encode(p.Prefix(), uuid.UUID(id))
}

func (id ID[P]) MarshalText() ([]byte, error) {
	return []byte(id.String()), nil
}

func (id *ID[P]) UnmarshalText(b []byte) error {
	var p P
	u, err := Parse(p.Prefix(), string(b))
	if err != nil {
		return err
	}
	*id = ID[P](u)
	return nil
}
