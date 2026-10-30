package content

import "regexp"

var entryIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func ValidateID(id string) error {
	if !entryIDPattern.MatchString(id) {
		return ErrBadQuery
	}
	return nil
}
