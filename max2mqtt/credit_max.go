package main

import (
	"log/slog"
	"strings"
	"sync/atomic"
)

// The CUL refills one credit (10 ms of airtime) per second up to a maximum that depends on the firmware:
// culfw stops at 900, a-culfw saves up 3600 (36 s, the full 1 % of an hour).
// The maximum is taken from the version line ("V ...") and raised when a credit report shows more.
const (
	culfwCreditMax  = 900
	aculfwCreditMax = 3600
)

var culCreditMax atomic.Int32

func init() {
	culCreditMax.Store(culfwCreditMax)
}

// creditMax returns the most credits the connected CUL saves up
func creditMax() int {
	return int(culCreditMax.Load())
}

func setCreditMax(n int, reason string) {
	if old := culCreditMax.Swap(int32(n)); int(old) != n {
		slog.Info("CUL credit maximum", "credits", n, "before", old, "reason", reason)
	}
}

// detectCreditMax sets the credit maximum from the CUL's version line, e.g. "V 1.26.08 a-culfw Build: ..."
func detectCreditMax(version string) {
	if strings.Contains(strings.ToLower(version), "a-culfw") {
		setCreditMax(aculfwCreditMax, "a-culfw")
		return
	}
	setCreditMax(culfwCreditMax, "culfw")
}

// learnCreditMax raises the credit maximum when a credit report is above it (firmware not recognised)
func learnCreditMax(credits int) {
	cur := creditMax()
	if credits <= cur {
		return
	}
	if credits <= aculfwCreditMax {
		setCreditMax(aculfwCreditMax, "credit report above maximum")
		return
	}
	setCreditMax(credits, "credit report above maximum")
}

// isVersionLine reports whether a CUL line is the answer to "V"
func isVersionLine(text string) bool {
	return strings.HasPrefix(text, "V ") && strings.Contains(strings.ToLower(text), "culfw")
}
