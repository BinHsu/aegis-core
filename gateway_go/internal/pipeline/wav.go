package pipeline

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// WAVInfo is the subset of a WAV header that the gateway and its
// tests care about. All fields are decoded from the little-endian
// RIFF/WAVE header.
type WAVInfo struct {
	SampleRateHz  int
	Channels      int
	BitsPerSample int
	// Data is the raw PCM samples, little-endian at BitsPerSample. For
	// the canonical MVP format (16 kHz mono 16-bit) this slice can be
	// passed directly to Pipeline.WritePCM.
	Data []byte
}

// ReadWAV parses a RIFF/WAVE file at path and returns its header fields
// plus the PCM payload. Supports only uncompressed PCM ("audio format" =
// 1 in the fmt chunk); any compressed variant (IMA ADPCM, µ-law, etc.)
// returns an error so the caller can't silently mis-interpret the bytes
// as linear PCM.
//
// The parser is deliberately minimal — it skips unknown sub-chunks so
// WAV files written by arbitrary tools (with "LIST INFO", "bext", etc.
// metadata chunks) parse correctly, but it does NOT validate chunk
// checksums or honor channel-mask extensions. Intended use is
// controlled test fixtures and pre-validated local recordings.
func ReadWAV(path string) (*WAVInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("wav: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// RIFF header: 4 "RIFF" + 4 size + 4 "WAVE".
	var riff [12]byte
	if _, err := io.ReadFull(f, riff[:]); err != nil {
		return nil, fmt.Errorf("wav: read RIFF header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return nil, fmt.Errorf("wav: not a RIFF/WAVE file")
	}

	info := &WAVInfo{}
	sawFmt := false

	// Walk sub-chunks. Each is: 4 byte id + 4 byte little-endian size +
	// size bytes of payload, with chunks padded to even length (though
	// the reader tolerates un-padded inputs because we seek by size).
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("wav: read sub-chunk header: %w", err)
		}
		id := string(hdr[0:4])
		size := binary.LittleEndian.Uint32(hdr[4:8])

		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("wav: fmt chunk too small: %d", size)
			}
			fmtBuf := make([]byte, size)
			if _, err := io.ReadFull(f, fmtBuf); err != nil {
				return nil, fmt.Errorf("wav: read fmt chunk: %w", err)
			}
			audioFormat := binary.LittleEndian.Uint16(fmtBuf[0:2])
			if audioFormat != 1 {
				return nil, fmt.Errorf("wav: unsupported audio format %d (expected 1 = PCM)", audioFormat)
			}
			info.Channels = int(binary.LittleEndian.Uint16(fmtBuf[2:4]))
			info.SampleRateHz = int(binary.LittleEndian.Uint32(fmtBuf[4:8]))
			info.BitsPerSample = int(binary.LittleEndian.Uint16(fmtBuf[14:16]))
			sawFmt = true
		case "data":
			if !sawFmt {
				return nil, fmt.Errorf("wav: data chunk before fmt")
			}
			data := make([]byte, size)
			if _, err := io.ReadFull(f, data); err != nil {
				return nil, fmt.Errorf("wav: read data chunk: %w", err)
			}
			info.Data = data
			return info, nil
		default:
			// Unknown chunk — skip its payload. The +1 padding to even
			// alignment is only necessary between chunks; since we exit
			// immediately upon hitting "data", we seek exactly size
			// bytes and rely on the next header read to verify.
			if _, err := f.Seek(int64(size), io.SeekCurrent); err != nil {
				return nil, fmt.Errorf("wav: skip chunk %q: %w", id, err)
			}
		}
	}
	return nil, fmt.Errorf("wav: no data chunk found")
}
