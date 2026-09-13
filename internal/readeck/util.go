// SPDX-License-Identifier: GPL-3.0-or-later

package readeck

import (
	"encoding/json"
	"io"
)

// decodeJSON decodes one JSON value from r into out, returning an
// error wrapping the underlying decode failure with the caller's
// method context.
func decodeJSON(r io.Reader, out any) error {
	if err := json.NewDecoder(r).Decode(out); err != nil {
		return err
	}
	return nil
}

// readAll reads the entire response body into a string. Used for
// non-JSON endpoints (e.g. the markdown article export).
func readAll(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
