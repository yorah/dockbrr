// Package dockername detects container names that Docker auto-assigned from its
// frozen namesgenerator word lists ("adjective_surname"). Such a name means the
// user never explicitly named the container, so it is likely disposable.
package dockername

import "strings"

// IsDockerAssigned reports whether name matches Docker's auto-generated
// "adjective_surname" form, optionally followed by a single collision digit
// (e.g. "focused_turing3"). Detection is exact against the frozen word lists:
// a name matches only when both halves are Docker vocabulary.
func IsDockerAssigned(name string) bool {
	// Strip a single optional trailing digit: GetRandomName appends 0..9 on a
	// name collision. No adjective or surname ends in a digit, so this only
	// removes the collision suffix.
	if n := len(name); n > 0 {
		if c := name[n-1]; c >= '0' && c <= '9' {
			name = name[:n-1]
		}
	}
	// Adjectives and surnames contain no underscore, so the first underscore is
	// the separator.
	i := strings.IndexByte(name, '_')
	if i <= 0 || i >= len(name)-1 {
		return false
	}
	if _, ok := adjectives[name[:i]]; !ok {
		return false
	}
	_, ok := surnames[name[i+1:]]
	return ok
}
