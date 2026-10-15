package probe

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

// PrintRecord writes a committed record to w: as JSON with asJSON, else as
// a summary line (id, verb, outcome, file) and one line per observation.
// Every environment prints its records this way.
func PrintRecord(w io.Writer, rec *evidence.Record, path string, asJSON bool) error {
	if asJSON {
		data, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", data)
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %s (%s)\n", rec.ID, rec.Verb, rec.Outcome, path)
	for _, o := range rec.Observations {
		v, err := json.Marshal(o.Value)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "  %s = %s\n", o.Key, v)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
