package drills

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden wire traces")

// volatileTags are dropped before comparison. Every one of them changes on
// every run for reasons that have nothing to do with session behavior:
//
//	9    BodyLength       — derived from the bytes, changes when a timestamp does
//	10   CheckSum         — likewise
//	52   SendingTime      — wall clock
//	60   TransactTime     — wall clock
//	122  OrigSendingTime  — wall clock, and present only on replayed messages
//
// Everything else is asserted, including sequence numbers and PossDupFlag,
// which is the whole point: a drill about sequence recovery whose golden trace
// ignored tag 34 or tag 43 would assert nothing worth asserting.
var volatileTags = map[string]bool{
	"9": true, "10": true, "52": true, "60": true, "122": true,
}

// normalizeWire reduces a captured log to the messages one session saw, with
// volatile fields removed.
//
// Filtering to a single session is not cosmetic. Three sessions write to the
// capture from their own goroutines, so the interleaving between them is not
// deterministic and a golden trace spanning all of them would flake. Within one
// session the order is the order the session processed things, which is exactly
// what a drill is asserting.
func normalizeWire(raw, sessionPrefix string) string {
	var out []string

	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, sessionPrefix) {
			continue
		}

		dir := ""
		switch {
		case strings.Contains(line, " --> "):
			dir = "-->"
		case strings.Contains(line, " <-- "):
			dir = "<--"
		default:
			// An event line ("Received logon request"). Useful when reading a
			// failure, but it is engine narration rather than wire traffic and
			// its wording is not ours to depend on.
			continue
		}

		msg := line[strings.Index(line, dir)+len(dir):]
		out = append(out, dir+" "+stripVolatile(strings.TrimSpace(msg)))
	}

	return strings.Join(out, "\n") + "\n"
}

func stripVolatile(msg string) string {
	fields := strings.Split(msg, "|")
	kept := make([]string, 0, len(fields))

	for _, f := range fields {
		if f == "" {
			continue
		}
		tag, _, found := strings.Cut(f, "=")
		if found && volatileTags[tag] {
			continue
		}
		kept = append(kept, f)
	}

	return strings.Join(kept, "|") + "|"
}

// assertGolden compares a normalized trace against testdata, or rewrites it
// when -update is passed.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name)

	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}

	if got != string(want) {
		t.Errorf("wire trace does not match %s\n\n--- got ---\n%s\n--- want ---\n%s",
			path, got, want)
	}
}
