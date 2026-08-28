// Package runid generates unique, sortable, human-readable run identifiers for
// orq-lite executions: “r20060701T193000Z-4f2a“ (compact UTC timestamp + 2
// random hex bytes). The timestamp prefixes sort lexically and let an operator
// read the start time at a glance; the random suffix disambiguates runs that
// start within the same second.
package runid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// New returns a fresh run ID.
func New() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("r%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b))
}
