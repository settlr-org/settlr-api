package export

import (
	"encoding/csv"
	"net/http"
)

// Exported CSV helpers shared with other packages that stream CSV (wallet reports),
// so formula-injection protection, deadlines and abort handling stay in one place.

// CSVSafe neutralises spreadsheet formula injection for a text cell.
func CSVSafe(s string) string { return csvSafe(s) }

// Money renders minor units as a decimal major-unit string (two places).
func Money(minorUnits int64) string { return money(minorUnits) }

// LongWrite lifts the server's write deadline for a streamed export (5 minutes).
func LongWrite(w http.ResponseWriter) { longWrite(w) }

// NewCSV sets the CSV headers (Content-Disposition attachment) and writes the header row.
func NewCSV(w http.ResponseWriter, filename string, header []string) *csv.Writer {
	return newCSV(w, filename, header)
}

// FinishCSV flushes and, when err is set (or the writer failed), aborts the connection
// with http.ErrAbortHandler instead of ending the body cleanly.
func FinishCSV(cw *csv.Writer, err error, what string) { finishCSV(cw, err, what) }
