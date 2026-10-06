package test

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCatalogLineDiff(t *testing.T) {
	for _, tt := range []struct{ name, old, new, want string }{
		{"identical", "a\nb", "a\nb", " a\n b"},
		{"replacement", "a\nold\nz", "a\nnew\nz", " a\n-old\n+new\n z"},
		{"insert", "a\nz", "a\nb\nz", " a\n+b\n z"},
		{"delete", "a\nb\nz", "a\nz", " a\n-b\n z"},
		{"all_changed", "old", "new", "-old\n+new"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := diff(tt.old, tt.new, false); got != tt.want {
				t.Fatalf("diff = %q, want %q", got, tt.want)
			}
			colored := diff(tt.old, tt.new, true)
			plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(colored, "")
			if plain != tt.want || !strings.Contains(colored, "\x1b[") {
				t.Fatalf("colored diff changed content: %q", colored)
			}
		})
	}
	// Repeated lines admit several equally long common sequences. Validate the
	// actual edit operations instead of imposing an unstated tie preference.
	oldText, newText := "a\nx\na\ny", "a\ny\na\nx"
	var before, after []string
	for _, line := range strings.Split(diff(oldText, newText, false), "\n") {
		if len(line) == 0 {
			t.Fatal("diff emitted an unmarked line")
		}
		switch line[0] {
		case ' ':
			before = append(before, line[1:])
			after = append(after, line[1:])
		case '-':
			before = append(before, line[1:])
		case '+':
			after = append(after, line[1:])
		default:
			t.Fatalf("unknown diff operation %q", line)
		}
	}
	if strings.Join(before, "\n") != oldText || strings.Join(after, "\n") != newText {
		t.Fatalf("edit operations did not reconstruct source and destination: %q / %q", before, after)
	}
	old := []string{"prefix", "one", "two", "suffix"}
	new := []string{"other", "one", "two", "changed"}
	if i, j, n := lcSubstr(old, new); i != 1 || j != 1 || n != 2 {
		t.Fatalf("longest shared sequence = %d/%d/%d", i, j, n)
	}
	if _, _, n := lcSubstr(nil, new); n != 0 {
		t.Fatal("empty sequence has shared lines")
	}
}

func TestCatalogAssertionsReportNativeFailures(t *testing.T) {
	const selector = "CATALOG_ASSERTION_CHILD"
	if mode := os.Getenv(selector); mode != "" {
		switch mode {
		case "equal":
			AssertEqual(t, "actual-value", "expected-value")
		case "diff":
			AssertEqualWithDiff(t, "common\nactual-value", "common\nexpected-value")
		default:
			t.Fatalf("unknown child mode %q", mode)
		}
		t.Fatal("failing assertion returned")
	}
	AssertEqual(t, "same", "same")
	AssertEqualWithDiff(t, "same", "same")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"equal", "diff"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestCatalogAssertionsReportNativeFailures$", "-test.count=1"}
			if testing.CoverMode() != "" {
				// Go's native teardown accepts same-executable subprocess pods
				// from this invocation's coverage directory. GOCOVERDIR alone
				// does not select the test runner's collection directory.
				dir := flag.Lookup("test.gocoverdir")
				if dir == nil || dir.Value.String() == "" {
					t.Fatal("coverage-enabled native runner has no test.gocoverdir")
				}
				args = append(args, "-test.gocoverdir="+dir.Value.String())
			}
			cmd := exec.CommandContext(ctx, executable, args...)
			cmd.Env = append(os.Environ(), selector+"="+mode)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("native failing assertion exit = %v, context %v, output %s", err, ctx.Err(), output)
			}
			if !strings.Contains(string(output), "actual-value") || !strings.Contains(string(output), "expected-value") || strings.Contains(string(output), "failing assertion returned") {
				t.Fatalf("failure diagnostic lost values/call termination: %s", output)
			}
			t.Logf("same executable %s; child %s exit %d; native failure diagnostic verified", executable, mode, exit.ExitCode())
		})
	}
}
