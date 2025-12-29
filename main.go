//go:generate cue export cue/LICENSE.cue --out text -e license -f -o LICENSE

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const VERSION = "v0.0.2"

// Config holds all application configuration
type Config struct {
	Filters      string
	OnlyMatching bool
	IgnoreCase   bool
	Keep         bool
	Limit        int
	Timeout      time.Duration
	Color        bool
	WordBoundary bool
	Exec         string
	VersionFlag  bool
}

// item represents a buffered line
type item struct {
	raw      string // Original line with colors
	clean    string // Line without colors for sorting/matching
	priority int    // 0 is highest, MaxInt is unmatched
}

func main() {
	// 1. CLI Parsing
	var cliCfg Config
	cliFs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	defineFlags(cliFs, &cliCfg)

	cliFs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] [filter_file]\n", os.Args[0])
		cliFs.PrintDefaults()
	}
	cliFs.Parse(os.Args[1:])

	// Track which flags were explicitly set on CLI so they override file args
	cliSet := make(map[string]bool)
	cliFs.Visit(func(f *flag.Flag) {
		cliSet[f.Name] = true
	})

	if cliCfg.VersionFlag {
		fmt.Printf("ssort, version: %s\n", VERSION)
		os.Exit(0)
	}

	// 2. Identify and Read Filter File
	var filterFileLines []string
	args := cliFs.Args()
	if len(args) > 0 {
		filename := args[0]
		content, err := os.ReadFile(filename)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading filter file: %v\n", err)
			os.Exit(1)
		}
		// Split lines manually to handle backslashes and comments
		filterFileLines = strings.Split(string(content), "\n")
	}

	// 3. Parse File Args and Filters
	finalCfg := cliCfg // Start with CLI config
	var filters []string

	if len(filterFileLines) > 0 {
		// Filter out comments and extract args/filters
		var processedLines []string

		// Remove comments first
		for _, line := range filterFileLines {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "#") {
				continue
			}
			processedLines = append(processedLines, line)
		}

		if len(processedLines) > 0 {
			first := processedLines[0]
			trimFirst := strings.TrimSpace(first)

			// Check if first line is an argument line
			// Condition: Starts with "-" OR starts with whitespace (blanks)
			isArgLine := strings.HasPrefix(trimFirst, "-") || (len(first) > 0 && (first[0] == ' ' || first[0] == '\t'))

			argLineEndIndex := -1

			if isArgLine {
				// Parse argument block (handle backslash extension)
				var argBuilder strings.Builder

				for i, line := range processedLines {
					trim := strings.TrimSpace(line)
					hasBackslash := strings.HasSuffix(trim, "\\")

					content := trim
					if hasBackslash {
						content = strings.TrimSuffix(content, "\\")
					}

					if argBuilder.Len() > 0 {
						argBuilder.WriteString(" ")
					}
					argBuilder.WriteString(content)

					if !hasBackslash {
						argLineEndIndex = i
						break
					}
				}

				// Parse the args found in file
				if argBuilder.Len() > 0 {
					var fileCfg Config
					fileFs := flag.NewFlagSet("file", flag.ContinueOnError)
					fileFs.SetOutput(io.Discard) // Silence errors or usage from file parsing
					defineFlags(fileFs, &fileCfg)

					// Tokenize respecting quotes
					fileArgs := tokenize(argBuilder.String())
					if err := fileFs.Parse(fileArgs); err != nil {
						fmt.Fprintf(os.Stderr, "Error parsing args in file: %v\n", err)
						os.Exit(1)
					}

					// Merge: Apply file config if NOT set in CLI
					applyFileConfig(&finalCfg, &fileCfg, cliSet)
				}
			}

			// The rest are filters
			startFilterIdx := argLineEndIndex + 1
			for i := startFilterIdx; i < len(processedLines); i++ {
				l := processedLines[i]
				if t := strings.TrimSpace(l); t != "" {
					filters = append(filters, t)
				}
			}
		}
	}

	// Add CLI filters (from -f flag)
	if finalCfg.Filters != "" {
		parts := strings.Split(finalCfg.Filters, ",")
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				filters = append(filters, trimmed)
			}
		}
	}

	// 4. Pre-compile Regex
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	var filterRegexps []*regexp.Regexp
	if finalCfg.WordBoundary {
		for _, f := range filters {
			if finalCfg.IgnoreCase {
				f = strings.ToLower(f)
			}

			pattern := `\b` + regexp.QuoteMeta(f) + `\b`
			re, err := regexp.Compile(pattern)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid filter pattern '%s': %v\n", f, err)
				os.Exit(1)
			}
			filterRegexps = append(filterRegexps, re)
		}
	}

	// 5. Input Source Setup
	linesCh := make(chan string, 100) // Small buffer to smooth input

	go func() {
		defer close(linesCh)

		var input io.Reader
		var cmd *exec.Cmd

		if finalCfg.Exec != "" {
			// Execute command
			tokens := tokenize(finalCfg.Exec)
			if len(tokens) > 0 {
				// Expand paths/env in tokens
				for i := range tokens {
					tokens[i] = expand(tokens[i])
				}

				cmd = exec.Command(tokens[0], tokens[1:]...)
				cmd.Stderr = os.Stderr
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error creating stdout pipe: %v\n", err)
					return
				}
				if err := cmd.Start(); err != nil {
					fmt.Fprintf(os.Stderr, "Error starting command '%s': %v\n", finalCfg.Exec, err)
					return
				}
				input = stdout
			} else {
				fmt.Fprintln(os.Stderr, "Empty executable command")
				return
			}
		} else {
			// Standard Input
			input = os.Stdin
		}

		scanner := bufio.NewScanner(input)
		// Increase buffer to 10MB to avoid "token too long" errors on minified files
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 10*1024*1024)

		for scanner.Scan() {
			linesCh <- scanner.Text()
		}

		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		}

		if cmd != nil {
			// Wait for command to finish (ignore exit code)
			_ = cmd.Wait()
		}
	}()

	// 6. Processing Loop Setup
	var resultsLimit (*int)
	if finalCfg.Limit > 0 {
		limit := finalCfg.Limit
		resultsLimit = &limit
	}

	printCh := make(chan string, 100) // Buffer print channel slightly
	printDone := make(chan struct{})

	go func() {
		defer close(printDone)
		for line := range printCh {
			fmt.Println(line)
			if resultsLimit != nil {
				*resultsLimit--
				if *resultsLimit <= 0 {
					break
				}
			}
		}
		// Drain if limit reached but generator still going
		for range printCh {
		}
	}()

	var buffer []item
	prioritizedCount := 0
	const unmatchedPriority = 999999

	ticker := time.NewTicker(finalCfg.Timeout)
	defer ticker.Stop()

	flush := func() {
		if len(buffer) == 0 {
			return
		}
		sort.SliceStable(buffer, func(i, j int) bool {
			if buffer[i].priority != buffer[j].priority {
				return buffer[i].priority < buffer[j].priority
			}
			return buffer[i].clean < buffer[j].clean
		})
		for _, it := range buffer {
			printCh <- it.raw
		}
		buffer = buffer[:0]
		prioritizedCount = 0
		ticker.Reset(finalCfg.Timeout)
	}

	// 7. Main Event Loop
	for {
		select {
		case line, ok := <-linesCh:
			if !ok {
				flush()
				close(printCh) // Signal printer to finish
				<-printDone    // Wait for printer to finish
				return
			}

			cleanLine := line
			if finalCfg.Color {
				cleanLine = ansiRegex.ReplaceAllString(line, "")
			}
			if finalCfg.IgnoreCase {
				cleanLine = strings.ToLower(cleanLine)
			}

			matchedIndex := -1
			matchLen := 0

			for i, f := range filters {
				matched := false
				if finalCfg.WordBoundary {
					matched = filterRegexps[i].MatchString(cleanLine)
				} else {
					if finalCfg.IgnoreCase {
						f = strings.ToLower(f)
					}
					matched = strings.Contains(cleanLine, f)
				}

				if matched {
					if len(f) > matchLen {
						matchedIndex = i
						matchLen = len(f)
					}
				}
			}

			// Case A: Highest Priority
			if matchedIndex == 0 {
				printCh <- line
				prioritizedCount++
				continue
			}

			// Case B: Unmatched
			if matchedIndex == -1 {
				if finalCfg.OnlyMatching {
					continue
				}
				if finalCfg.Keep {
					fmt.Println(line)
				} else {
					buffer = append(buffer, item{raw: line, clean: cleanLine, priority: unmatchedPriority})
				}
				continue
			}

			// Case C: Buffered
			buffer = append(buffer, item{raw: line, clean: cleanLine, priority: matchedIndex})
			prioritizedCount++

			if finalCfg.Limit > 0 && prioritizedCount >= finalCfg.Limit {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

// Helpers

func defineFlags(fs *flag.FlagSet, c *Config) {
	fs.StringVar(&c.Filters, "f", "", "Comma separated list of prioritized strings")
	fs.BoolVar(&c.OnlyMatching, "o", false, "Output only matching results")
	fs.BoolVar(&c.Keep, "k", false, "Output unsorted (unmatched) lines immediately")
	fs.BoolVar(&c.Keep, "keep-going", false, "Output unsorted (unmatched) lines immediately")
	fs.BoolVar(&c.IgnoreCase, "i", false, "Ignore case")
	fs.BoolVar(&c.IgnoreCase, "ignore-case", false, "Ignore case")
	fs.IntVar(&c.Limit, "limit", 0, "Flush buffer after N prioritized matches")
	fs.DurationVar(&c.Timeout, "timeout", 500*time.Millisecond, "Flush timeout")
	fs.BoolVar(&c.Color, "color", false, "Enable color-aware mode")
	fs.BoolVar(&c.WordBoundary, "w", false, "Match on word boundaries only")
	fs.BoolVar(&c.VersionFlag, "version", false, "Display version and quit")
	fs.StringVar(&c.Exec, "e", "", "Execute command and sort its output")
}

func applyFileConfig(dst *Config, src *Config, cliSet map[string]bool) {
	if !cliSet["f"] {
		dst.Filters = src.Filters
	}
	if !cliSet["o"] {
		dst.OnlyMatching = src.OnlyMatching
	}
	if !cliSet["k"] && !cliSet["keep-going"] {
		dst.Keep = src.Keep
	}
	if !cliSet["limit"] {
		dst.Limit = src.Limit
	}
	if !cliSet["i"] && !cliSet["ignore-case"] {
		dst.IgnoreCase = src.IgnoreCase
	}
	if !cliSet["timeout"] {
		dst.Timeout = src.Timeout
	}
	if !cliSet["color"] {
		dst.Color = src.Color
	}
	if !cliSet["w"] {
		dst.WordBoundary = src.WordBoundary
	}
	if !cliSet["e"] {
		dst.Exec = src.Exec
	}
}

func tokenize(input string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, r := range input {
		switch {
		case inQuote:
			if r == quoteChar {
				inQuote = false
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			inQuote = true
			quoteChar = r
		case r == ' ' || r == '\t':
			if !inQuote {
				if current.Len() > 0 {
					args = append(args, current.String())
					current.Reset()
				}
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// expand handles environment variable expansion ($VAR) and tilde expansion (~/ or ~)
func expand(path string) string {
	// 1. Expand standard env vars
	expanded := os.ExpandEnv(path)

	// 2. Expand tilde (~) if it's the start of the path
	if strings.HasPrefix(expanded, "~/") || expanded == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			if expanded == "~" {
				return home
			}
			return filepath.Join(home, expanded[2:])
		}
	}
	return expanded
}
