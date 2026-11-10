// Package h264 has the few pieces of the H.264 byte stream that the codecs and the tests need: splitting an Annex B access unit
// into its NAL units, telling their types, and writing them with length prefixes (the "AVCC" form that VideoToolbox takes). It is
// plain Go with no codec in it.
package h264

import "encoding/binary"

// The NAL unit types that the program tells apart (ITU-T H.264, table 7-1).
const (
	// NALSlice is a slice of a picture that is not a keyframe.
	NALSlice = 1
	// NALIDR is a slice of a keyframe.
	NALIDR = 5
	// NALSEI is supplemental information.
	NALSEI = 6
	// NALSPS is a sequence parameter set.
	NALSPS = 7
	// NALPPS is a picture parameter set.
	NALPPS = 8
	// NALAUD is an access unit delimiter.
	NALAUD = 9
)

// Type is the type of a NAL unit (the low five bits of its first byte), or 0 for an empty one.
func Type(nal []byte) int {
	if len(nal) == 0 {
		return 0
	}
	return int(nal[0] & 0x1f)
}

// SplitAnnexB returns the NAL units of an Annex B byte stream, without their start codes (00 00 01 or 00 00 00 01). The units
// are slices of au. Bytes before the first start code are left out, and so are empty units.
func SplitAnnexB(au []byte) [][]byte {
	var nals [][]byte
	start := -1 // where the unit that is being read begins
	i := 0
	for i+2 < len(au) {
		if au[i] != 0 || au[i+1] != 0 || au[i+2] != 1 {
			i++
			continue
		}
		if start >= 0 {
			end := i
			// The zeros before a start code belong to the byte stream (a four-byte start code, trailing zero bytes), not
			// to the unit, which never ends with a zero byte.
			for end > start && au[end-1] == 0 {
				end--
			}
			if end > start {
				nals = append(nals, au[start:end])
			}
		}
		start = i + 3
		i += 3
	}
	if start >= 0 && start < len(au) {
		nals = append(nals, au[start:])
	}
	return nals
}

// AppendAVCC appends the NAL units to dst, each after its length as four bytes, big endian, and returns the result.
func AppendAVCC(dst []byte, nals ...[]byte) []byte {
	for _, nal := range nals {
		dst = binary.BigEndian.AppendUint32(dst, uint32(len(nal)))
		dst = append(dst, nal...)
	}
	return dst
}

// AppendAnnexB appends the NAL units to dst, each after the start code 00 00 00 01, and returns the result.
func AppendAnnexB(dst []byte, nals ...[]byte) []byte {
	for _, nal := range nals {
		dst = append(dst, 0, 0, 0, 1)
		dst = append(dst, nal...)
	}
	return dst
}

// IsKeyframe says that the Annex B access unit has a slice of a keyframe.
func IsKeyframe(au []byte) bool {
	for _, nal := range SplitAnnexB(au) {
		if Type(nal) == NALIDR {
			return true
		}
	}
	return false
}
