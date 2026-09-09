// Package runid generates unique, sortable, human-readable run identifiers for
// orq-lite executions: `r20060701T193000Z-4f2a` (compact UTC timestamp + 16
// random hex bytes). The timestamp prefixes sort lexically and let an operator
// read the start time at a glance; the random suffix disambiguates runs that
// start within the same second.
package runid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

// New returns a fresh run ID.
func New() string {
	id, err := generate(time.Now(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return id
}

// generate never returns an ID when entropy is unavailable. New deliberately
// fails closed (like crypto/rand) rather than silently issuing predictable IDs.
func generate(now time.Time, entropy io.Reader) (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(entropy, b); err != nil {
		return "", fmt.Errorf("runid: entropy: %w", err)
	}
	return fmt.Sprintf("r%s-%s", now.UTC().Format("20060102T150405Z"), hex.EncodeToString(b)), nil
}
