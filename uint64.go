package ncbigenome

import (
	"fmt"
	"strconv"
)

// Uint64 is an unsigned 64-bit integer that NCBI's JSON writes as a string
// ("118960141"), following the protobuf JSON mapping for 64-bit integers. It
// marshals back to that string form; when decoding it accepts either a string
// or a plain JSON number.
type Uint64 uint64

// MarshalJSON implements json.Marshaler.
func (u Uint64) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatUint(uint64(u), 10) + `"`), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (u *Uint64) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid uint64 value %s: %w", b, err)
	}
	*u = Uint64(v)
	return nil
}
