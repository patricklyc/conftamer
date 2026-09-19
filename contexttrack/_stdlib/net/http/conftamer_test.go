package http_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const conftamerConfigurationChild = "CONFTAMER_CONFIGURATION_TEST_CHILD"

var conftamerConfigurationNames = map[string]bool{
	"CONFTAMER_EVENTS":          true,
	"CONFTAMER_EVENTS_DIR":      true,
	"CONFTAMER_CAPTURE_ID":      true,
	conftamerConfigurationChild: true,
}

func TestConftamerConfiguration(t *testing.T) {
	if os.Getenv(conftamerConfigurationChild) != "" {
		t.Fatal("parent configuration test ran as a child")
	}

	t.Run("disabled", func(t *testing.T) {
		stderr := runConftamerConfigurationChild(t, nil)
		if strings.Contains(stderr, "conftamer:") {
			t.Fatalf("disabled diagnostics = %q, want none", stderr)
		}
	})

	for _, test := range []struct {
		name        string
		environment map[string]string
		want        string
	}{
		{
			name: "missing capture ID",
			environment: map[string]string{
				"CONFTAMER_EVENTS_DIR": t.TempDir(),
			},
			want: "CONFTAMER_EVENTS_DIR and CONFTAMER_CAPTURE_ID must both be set",
		},
		{
			name: "missing events directory",
			environment: map[string]string{
				"CONFTAMER_CAPTURE_ID": "unit-capture",
			},
			want: "CONFTAMER_EVENTS_DIR and CONFTAMER_CAPTURE_ID must both be set",
		},
		{
			name: "relative events directory",
			environment: map[string]string{
				"CONFTAMER_EVENTS_DIR": "relative/capture",
				"CONFTAMER_CAPTURE_ID": "unit-capture",
			},
			want: "CONFTAMER_EVENTS_DIR must be absolute",
		},
		{
			name: "legacy setting",
			environment: map[string]string{
				"CONFTAMER_EVENTS": "/tmp/legacy-events.jsonl",
			},
			want: "CONFTAMER_EVENTS is unsupported",
		},
		{
			name: "legacy setting wins over v2",
			environment: map[string]string{
				"CONFTAMER_EVENTS":     "/tmp/legacy-events.jsonl",
				"CONFTAMER_EVENTS_DIR": t.TempDir(),
				"CONFTAMER_CAPTURE_ID": "unit-capture",
			},
			want: "CONFTAMER_EVENTS is unsupported",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stderr := runConftamerConfigurationChild(t, test.environment)
			if !strings.Contains(stderr, "conftamer: configuration error: "+test.want) {
				t.Fatalf("diagnostics = %q, want %q", stderr, test.want)
			}
			if directory := test.environment["CONFTAMER_EVENTS_DIR"]; filepath.IsAbs(directory) {
				assertConftamerDirectoryEmpty(t, directory)
			}
		})
	}

	t.Run("enabled", func(t *testing.T) {
		directory := t.TempDir()
		stderr := runConftamerConfigurationChild(t, map[string]string{
			"CONFTAMER_EVENTS_DIR": directory,
			"CONFTAMER_CAPTURE_ID": "unit-capture",
		})
		files, err := filepath.Glob(filepath.Join(directory, "*.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatalf("process files = %v, want exactly one", files)
		}
		processID := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(processID) {
			t.Fatalf("process ID = %q, want 32 lowercase hex characters", processID)
		}
		info, err := os.Stat(files[0])
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("process file mode = %04o, want 0600", got)
		}
		if !strings.Contains(stderr, "conftamer: enabled") ||
			!strings.Contains(stderr, "capture_id=\"unit-capture\"") ||
			!strings.Contains(stderr, "process_id=\""+processID+"\"") ||
			!strings.Contains(stderr, "path=\""+files[0]+"\"") {
			t.Fatalf("enabled diagnostics = %q", stderr)
		}
	})
}

func TestConftamerConfigurationChild(t *testing.T) {
	if os.Getenv(conftamerConfigurationChild) == "" {
		t.Skip("helper process")
	}
}

func runConftamerConfigurationChild(t *testing.T, overrides map[string]string) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestConftamerConfigurationChild$", "-test.count=1")
	command.Env = conftamerConfigurationEnvironment(overrides)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if output, err := command.Output(); err != nil {
		t.Fatalf("configuration child failed: %v\nstdout: %s\nstderr: %s", err, output, stderr.String())
	}
	return stderr.String()
}

func conftamerConfigurationEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides)+1)
	for _, setting := range os.Environ() {
		name, _, _ := strings.Cut(setting, "=")
		if !conftamerConfigurationNames[name] {
			environment = append(environment, setting)
		}
	}
	environment = append(environment, conftamerConfigurationChild+"=1")
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}

func assertConftamerDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory entries = %v, want empty", entries)
	}
}
