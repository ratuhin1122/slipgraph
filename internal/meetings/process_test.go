package meetings

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Child processes verify the executable boundary without requiring FFmpeg on the
// developer host. The Compose smoke test additionally exercises real FFmpeg.
func TestMain(m *testing.M) {
	if os.Getenv("SLIPGRAPH_MEDIA_TEST_HELPER") == "1" {
		name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
		if name == "ffprobe" || name == "ffmpeg" {
			cwd, err := os.Getwd()
			if err != nil || !strings.HasPrefix(filepath.Base(cwd), "slipgraph-media-") {
				os.Exit(2)
			}
			data, err := os.ReadFile("recording")
			if err != nil || string(data) != "RIFF0000WAVEfmt " {
				os.Exit(3)
			}
			if err := os.WriteFile(os.Getenv("SLIPGRAPH_MEDIA_TEST_MARKER"), []byte(cwd), 0600); err != nil {
				os.Exit(4)
			}
			if name == "ffprobe" {
				if os.Args[len(os.Args)-1] != "recording" {
					os.Exit(5)
				}
				_, _ = io.WriteString(os.Stdout, `{"format":{"duration":"1"},"streams":[{"codec_type":"audio"}]}`)
			} else {
				found := false
				for i, arg := range os.Args {
					if arg == "-i" && i+1 < len(os.Args) && os.Args[i+1] == "recording" {
						found = true
					}
				}
				if !found || os.Args[len(os.Args)-1] != "pipe:1" {
					os.Exit(6)
				}
				_, _ = os.Stdout.Write(make([]byte, 64000))
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestDurationUsesDecodedSamplesAndPrivateWorkingDirectory(t *testing.T) {
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, name := range []string{"ffprobe", "ffmpeg"} {
		input, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		output, err := os.OpenFile(filepath.Join(directory, name+suffix), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
		if err != nil {
			_ = input.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		_ = input.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatal("could not create media test helper")
		}
	}
	marker := filepath.Join(directory, "working-directory")
	t.Setenv("SLIPGRAPH_MEDIA_TEST_HELPER", "1")
	t.Setenv("SLIPGRAPH_MEDIA_TEST_MARKER", marker)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	duration, err := (FFProbe{}).Duration(context.Background(), bytes.NewReader([]byte("RIFF0000WAVEfmt ")))
	if err != nil || duration != 2000 {
		t.Fatalf("trusted metadata instead of decoded samples: %d %v", duration, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(data)); !os.IsNotExist(err) {
		t.Fatal("temporary recording directory survived")
	}
}
