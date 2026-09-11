// Package codec implements the lossless block format shared by raw samples and
// rollup buckets: per-column delta + run-length encoding of int64 values,
// optionally wrapped in a Zstandard frame with a CRC32 of the decoded payload.
//
// Block layout (little endian):
//
//	0..3   magic (4 ASCII bytes, e.g. "QHR2" raw samples, "QHS3" rollups)
//	4      format version (1)
//	5      flag: 0 = payload stored as-is, 1 = payload zstd-compressed
//	6..9   row count
//	10..13 decoded payload length
//	14..17 CRC32 (IEEE) of decoded payload
//	18..   payload
//
// Payload: for every column, a sequence of (uvarint run, varint delta) pairs;
// the column value is reconstructed as prev += delta repeated run times.
package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
	"qbit-history/internal/model"
)

const MaxSamples = 60000
const MaxDecoded = 16 << 20

var mu sync.Mutex
var encoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
var decoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(32<<20))

var ErrLimit = errors.New("codec limit")
var ErrCorrupt = errors.New("corrupt block")

func Pack(magic string, raw []byte, n int) ([]byte, error) {
	if len(raw) > MaxDecoded || n > MaxSamples || n < 0 || len(magic) != 4 {
		return nil, ErrLimit
	}
	mu.Lock()
	compressed := encoder.EncodeAll(raw, nil)
	mu.Unlock()
	flag := byte(1)
	if len(compressed) >= len(raw) {
		compressed = raw
		flag = 0
	}
	out := make([]byte, 18, len(compressed)+18)
	copy(out, magic)
	out[4] = 1
	out[5] = flag
	binary.LittleEndian.PutUint32(out[6:10], uint32(n))
	binary.LittleEndian.PutUint32(out[10:14], uint32(len(raw)))
	binary.LittleEndian.PutUint32(out[14:18], crc32.ChecksumIEEE(raw))
	return append(out, compressed...), nil
}

func Unpack(magic string, b []byte) ([]byte, int, error) {
	if len(b) < 18 || len(b) > MaxDecoded+18 || string(b[:4]) != magic || b[4] != 1 || b[5] > 1 {
		return nil, 0, errors.New("invalid block header")
	}
	n := int(binary.LittleEndian.Uint32(b[6:10]))
	size := int(binary.LittleEndian.Uint32(b[10:14]))
	if n > MaxSamples || size > MaxDecoded {
		return nil, 0, ErrLimit
	}
	raw := b[18:]
	var err error
	if b[5] == 1 {
		mu.Lock()
		raw, err = decoder.DecodeAll(raw, make([]byte, 0, size))
		mu.Unlock()
	}
	if err != nil || len(raw) != size || crc32.ChecksumIEEE(raw) != binary.LittleEndian.Uint32(b[14:18]) {
		return nil, 0, ErrCorrupt
	}
	return raw, n, nil
}

// EncodeColumns writes cols columns of n rows using delta + RLE per column.
func EncodeColumns(n, cols int, get func(row, col int) int64) []byte {
	var b bytes.Buffer
	var scratch [binary.MaxVarintLen64]byte
	for col := 0; col < cols; col++ {
		var prev, delta int64
		run := uint64(0)
		flush := func() {
			if run > 0 {
				b.Write(binary.AppendUvarint(scratch[:0], run))
				b.Write(binary.AppendVarint(scratch[:0], delta))
			}
		}
		for row := 0; row < n; row++ {
			v := get(row, col)
			d := v - prev
			prev = v
			if run > 0 && d != delta {
				flush()
				run = 0
			}
			delta = d
			run++
		}
		flush()
	}
	return b.Bytes()
}

// DecodeColumns is the inverse of EncodeColumns; set is called for every cell in
// column-major order and may return an error to reject invalid values.
func DecodeColumns(raw []byte, n, cols int, set func(row, col int, v int64) error) error {
	r := bytes.NewReader(raw)
	for c := 0; c < cols; c++ {
		i := 0
		var v int64
		for i < n {
			run, e := binary.ReadUvarint(r)
			if e != nil || run == 0 || run > uint64(n-i) {
				return errors.New("invalid run")
			}
			d, e := binary.ReadVarint(r)
			if e != nil {
				return e
			}
			for j := uint64(0); j < run; j++ {
				v += d
				if e := set(i, c, v); e != nil {
					return e
				}
				i++
			}
		}
	}
	if _, e := r.ReadByte(); e != io.EOF {
		return errors.New("trailing codec bytes")
	}
	return nil
}

const sampleColumns = 10

func Encode(samples []model.Sample) ([]byte, error) {
	if len(samples) > MaxSamples {
		return nil, ErrLimit
	}
	raw := EncodeColumns(len(samples), sampleColumns, func(row, col int) int64 { return value(samples[row], col) })
	return Pack("QHR2", raw, len(samples))
}

func value(s model.Sample, c int) int64 {
	switch c {
	case 0:
		return s.At
	case 1:
		return int64(s.Epoch)
	case 2:
		return int64(s.Seq)
	case 3:
		return s.Up
	case 4:
		return s.Down
	case 5:
		return s.Uploaded
	case 6:
		return s.Downloaded
	case 7:
		return int64(s.Valid)
	case 8:
		return int64(s.Quality)
	default:
		return s.StepMS
	}
}

func Decode(b []byte) ([]model.Sample, error) {
	raw, n, err := Unpack("QHR2", b)
	if err != nil {
		return nil, err
	}
	out := make([]model.Sample, n)
	err = DecodeColumns(raw, n, sampleColumns, func(i, c int, v int64) error {
		s := &out[i]
		switch c {
		case 0:
			s.At = v
		case 1:
			s.Epoch = uint64(v)
		case 2:
			s.Seq = uint64(v)
		case 3:
			s.Up = v
		case 4:
			s.Down = v
		case 5:
			s.Uploaded = v
		case 6:
			s.Downloaded = v
		case 7:
			if v < 0 || v > 15 {
				return errors.New("invalid validity")
			}
			s.Valid = uint8(v)
		case 8:
			if v < 0 || v > 65535 {
				return errors.New("invalid quality")
			}
			s.Quality = uint16(v)
		case 9:
			if v <= 0 || v > 60000 {
				return errors.New("invalid step")
			}
			s.StepMS = v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
