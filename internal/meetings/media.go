package meetings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const MaxBytes int64 = 25000000
const MaxMinutes = 60

var ErrMedia = errors.New("unsupported or invalid media")

func Magic(b []byte, claimed string) error {
	valid := false
	switch claimed {
	case "audio/wav":
		valid = len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE"
	case "audio/mpeg":
		valid = len(b) >= 4 && (string(b[:3]) == "ID3" || (b[0] == 0xff && b[1]&0xe0 == 0xe0 && b[1]&0x06 != 0 && b[2]&0xf0 != 0xf0 && b[2]&0x0c != 0x0c))
	case "audio/mp4", "video/mp4":
		valid = len(b) >= 12 && string(b[4:8]) == "ftyp" && (string(b[8:12]) == "isom" || string(b[8:12]) == "mp42" || string(b[8:12]) == "iso2" || string(b[8:12]) == "avc1" || string(b[8:12]) == "M4A ")
	case "audio/webm", "video/webm":
		valid = len(b) >= 16 && bytes.Equal(b[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) && bytes.Contains(b, []byte("webm"))
	}
	if !valid {
		return ErrMedia
	}
	return nil
}

type Prober interface {
	Duration(context.Context, io.Reader) (int64, error)
}
type FFProbe struct{}

func (FFProbe) Duration(ctx context.Context, r io.Reader) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	dir, e := os.MkdirTemp("", "slipgraph-media-*")
	if e != nil {
		return 0, e
	}
	defer func() { _ = os.RemoveAll(dir) }()
	root, e := os.OpenRoot(dir)
	if e != nil {
		return 0, e
	}
	defer func() { _ = root.Close() }()
	file, e := root.OpenFile("recording", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return 0, e
	}
	n, e := io.Copy(file, io.LimitReader(r, MaxBytes+1))
	closeErr := file.Close()
	if e != nil || closeErr != nil || n > MaxBytes {
		return 0, ErrMedia
	}
	// Fixed executable and arguments; uploaded filenames and URLs are never executed.
	command := exec.CommandContext(ctx, "ffprobe", "-max_alloc", "67108864", "-v", "error", "-protocol_whitelist", "file", "-format_whitelist", "wav,mp3,mov,matroska,webm", "-show_entries", "format=duration:stream=codec_type", "-of", "json", "recording")
	command.Dir = dir
	var out limitedBuffer
	command.Stdout = &out
	command.Stderr = io.Discard
	if command.Run() != nil {
		return 0, ErrMedia
	}
	var data struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type string `json:"codec_type"`
		} `json:"streams"`
	}
	if json.Unmarshal(out.Bytes(), &data) != nil {
		return 0, ErrMedia
	}
	audioTracks := 0
	for _, s := range data.Streams {
		if s.Type == "audio" {
			audioTracks++
		}
	}
	seconds, e := strconv.ParseFloat(data.Format.Duration, 64)
	if e != nil || audioTracks != 1 || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > MaxMinutes*60 {
		return 0, ErrMedia
	}
	// Container duration metadata is attacker-controlled. Count decoded PCM samples
	// before any billable call, rather than charging from the declared duration.
	decoded := &sampleCounter{max: MaxMinutes * 60 * 32000}
	decode := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-max_alloc", "67108864", "-protocol_whitelist", "file,pipe", "-format_whitelist", "wav,mp3,mov,matroska,webm", "-i", "recording", "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
	decode.Dir = dir
	decode.Stdout = decoded
	decode.Stderr = io.Discard
	if decode.Run() != nil || decoded.n < 1 {
		return 0, ErrMedia
	}
	return (decoded.n + 31) / 32, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, ErrMedia
	}
	return b.Buffer.Write(p)
}
func Extension(mime string) string {
	switch mime {
	case "audio/wav":
		return ".wav"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4", "video/mp4":
		return ".mp4"
	case "audio/webm", "video/webm":
		return ".webm"
	}
	return ""
}
func validText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && !strings.ContainsRune(s, 0)
}

type sampleCounter struct{ n, max int64 }

func (c *sampleCounter) Write(p []byte) (int, error) {
	if c.n+int64(len(p)) > c.max {
		return 0, ErrMedia
	}
	c.n += int64(len(p))
	return len(p), nil
}
