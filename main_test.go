package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

var (
	binName       = "ssort_test_bin"
	testFile      = "test_data.txt"
	testFileLines = 0
)

func TestMain(m *testing.M) {
	// 1. Build the binary
	if err := exec.Command("go", "build", "-o", binName, ".").Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build binary: %v\n", err)
		os.Exit(1)
	}

	// 2. Create dummy data
	data := `DEBUG: connection established
INFO: starting service
ERROR: critical failure in info db
DEBUG: payload received
INFO: errorneous data found
WARN: INFO_PAD not found
WARN: memory high`
	if err := os.WriteFile(testFile, []byte(data), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create test file: %v\n", err)
		os.Exit(1)
	}
	for range strings.Lines(data) {
		testFileLines++
	}

	// 3. Run tests
	exitVal := m.Run()

	// 4. Cleanup
	os.Remove(binName)
	os.Remove(testFile)

	os.Exit(exitVal)
}

func runPipeline(t *testing.T, cmdStr string) string {
	// We use sh -c to handle the pipes easily
	cmd := exec.Command("sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\nOutput: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func CheckNumberOfLines(t *testing.T, got string, expected int) {

	lines := 0
	for range strings.Lines(got) {
		lines++
	}
	if lines != expected {
		t.Errorf("\nExpected:\n%d\nGot:\n%d", expected, lines)
	}
}
func CheckString(t *testing.T, got string, expected string) {
	expected = strings.TrimSpace(expected)
	if got != expected {
		t.Errorf("\nExpected:\n%s\nGot:\n%s", expected, got)
	}
}
func CheckContains(t *testing.T, got string, needle string) {
	needle = strings.TrimSpace(needle)
	if !strings.Contains(got, needle) {
		t.Errorf("\nExpected to find:\n%s\nGot:\n%s", needle, got)
	}
}
func CheckPrefix(t *testing.T, got string, expectedPrefix string) {

	if !strings.HasPrefix(got, strings.TrimSpace(expectedPrefix)) {
		t.Errorf("\nExpected string with prefix:\n%s\nGot:\n%s", expectedPrefix, got)
	}
}
func TestPriorityFiltering(t *testing.T) {
	cmd := fmt.Sprintf("grep '.' %s | ./%s -f 'ERROR'", testFile, binName)
	expected := `ERROR: critical failure in info db`

	got := runPipeline(t, cmd)
	CheckNumberOfLines(t, got, testFileLines)
	CheckPrefix(t, got, expected)
}

func TestMultiPriority(t *testing.T) {
	cmd := fmt.Sprintf("grep '.' %s | ./%s -f 'ERROR,WARN'", testFile, binName)
	expected := `ERROR: critical failure in info db
WARN: INFO_PAD not found
WARN: memory high
`

	got := runPipeline(t, cmd)
	CheckPrefix(t, got, expected)
}

func TestWordBoundary1(t *testing.T) {
	cmd := fmt.Sprintf("grep '.' %s | ./%s -f 'error' -w", testFile, binName)
	notexpected := `INFO: errorneous data found`

	got := runPipeline(t, cmd)
	if strings.HasPrefix(got, notexpected) {
		t.Errorf("\nNot Expected at start:\n%s\nGot:\n%s", notexpected, got)
	}
}
func TestWordBoundary2(t *testing.T) {
	cmd := fmt.Sprintf("grep '.' %s | ./%s -f 'INFO' -w -o", testFile, binName)
	expected := `
INFO: starting service
INFO: errorneous data found
  `

	got := runPipeline(t, cmd)
	CheckString(t, got, expected)
}
func TestMatchLengthOrdering(t *testing.T) {
	cmd := fmt.Sprintf("grep '.' %s | ./%s -f 'INFO,INFO_PAD,DEBUG' -o", testFile, binName)
	expected := `
INFO: starting service
INFO: errorneous data found
WARN: INFO_PAD not found
DEBUG: connection established
DEBUG: payload received
	`
	got := runPipeline(t, cmd)
	CheckString(t, got, expected)

}
func TestOnlyMatching1(t *testing.T) {
	cmd := fmt.Sprintf("grep '.' %s | ./%s -f 'ERROR' -w -o", testFile, binName)

	got := runPipeline(t, cmd)
	CheckNumberOfLines(t, got, 1)
}
func TestIgnoreCaseWordBoundary(t *testing.T) {
	cmd := fmt.Sprintf("grep '.' %s | ./%s -f 'info' -i -w -o", testFile, binName)

	got := runPipeline(t, cmd)
	CheckContains(t, got, "failure in info db")
	CheckNumberOfLines(t, got, 3)
}
func TestIgnoreCase(t *testing.T) {
	cmd := fmt.Sprintf("grep '.' %s | ./%s -f 'info' -i -o", testFile, binName)

	got := runPipeline(t, cmd)
	CheckContains(t, got, "failure in info db")
	CheckNumberOfLines(t, got, 4)
}
func TestOnlyMatching2(t *testing.T) {
	cmd := fmt.Sprintf("grep '.' %s | ./%s -f 'DEBUG' -w -o", testFile, binName)

	got := runPipeline(t, cmd)
	CheckNumberOfLines(t, got, 2)
}
func TestLimitFlag(t *testing.T) {
	cmd := fmt.Sprintf("grep 'DEBUG' %s | ./%s -f 'connection' --limit 1", testFile, binName)
	expected := `DEBUG: connection established`

	got := runPipeline(t, cmd)
	CheckNumberOfLines(t, got, 1)
	CheckString(t, got, expected)
}

func TestPassThrough(t *testing.T) {
	cmd := fmt.Sprintf("grep -E 'INFO|WARN' %s | ./%s -f 'MISSING'", testFile, binName)
	expected := `
INFO: errorneous data found
INFO: starting service
WARN: INFO_PAD not found
WARN: memory high
  `

	got := runPipeline(t, cmd)
	CheckString(t, got, expected)
}

func TestLimitOnHighPriority(t *testing.T) {
	cmd := fmt.Sprintf("grep -E '.' %s | ./%s --limit 1 -f 'WARN'", testFile, binName)
	got := runPipeline(t, cmd)
	expected := `WARN: INFO_PAD not found`
	CheckNumberOfLines(t, got, 1)
	CheckString(t, got, expected)
}
