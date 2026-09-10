package config

import (
	"os"
	"strconv"
	"strings"
)

// GoParallelismDefault caps `go build` / `go test` / `go vet` package-level
// parallelism (`-p`) for every Gas Town agent session.
//
// Go defaults `-p` to GOMAXPROCS, so a single unscoped `go test ./...` on a
// 48-core host fans out to dozens of concurrent compile/link actions. One such
// command drove load to 326 and stalled the town's Dolt server (gt-38l,
// operator directive hq-rp93). The dotnet toolchain has always self-capped at
// -maxcpucount:4 behind its wrapper; this gives Go the same discipline
// structurally, at spawn, instead of relying on per-invocation worker
// discipline — which cannot work, because the rule was never served in the
// role documents workers are actually given.
//
// A cap is preferred over routing Go verbs through the town's shared
// heavy-build lock: the cap bounds fan-out without queueing, so a polecat's
// gate can never be starved behind another rig's 30-minute build and die
// unrun with its session.
const GoParallelismDefault = 4

// GoParallelismEnvVar overrides GoParallelismDefault when set to a
// non-negative integer in gt's own environment. 0 disables the cap entirely
// (no -p is injected), which is an operator escape hatch, not a worker one.
const GoParallelismEnvVar = "GT_GO_PARALLELISM"

// ResolveGoParallelism returns the `-p` cap to inject into agent GOFLAGS.
// Invalid or absent overrides fall back to GoParallelismDefault.
func ResolveGoParallelism() int {
	v := os.Getenv(GoParallelismEnvVar)
	if v == "" {
		return GoParallelismDefault
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return GoParallelismDefault
	}
	return n
}

// GoflagsWithParallelismCap returns existing GOFLAGS with `-p=<parallelism>`
// appended, preserving any flags already present.
//
// An explicit -p already in GOFLAGS wins — an operator who set one meant it.
// A parallelism of 0 disables injection and returns existing unchanged.
func GoflagsWithParallelismCap(existing string, parallelism int) string {
	existing = strings.TrimSpace(existing)
	if parallelism <= 0 || hasGoParallelismFlag(existing) {
		return existing
	}
	flag := "-p=" + strconv.Itoa(parallelism)
	if existing == "" {
		return flag
	}
	return existing + " " + flag
}

// hasGoParallelismFlag reports whether a GOFLAGS value already sets -p.
// Matches "-p=N" and a bare "-p"; does not match longer flags that merely
// start with -p (e.g. go test's -parallel).
func hasGoParallelismFlag(goflags string) bool {
	for _, f := range strings.Fields(goflags) {
		if f == "-p" || strings.HasPrefix(f, "-p=") {
			return true
		}
	}
	return false
}
