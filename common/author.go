package common

import (
	"errors"
	"strings"
)

var errNoGitAuthorEmail = errors.New("git user.email is not set; run `git config --global user.email you@example.com`")
var errNoGitAuthorName = errors.New("git user.name is not set; run `git config --global user.name \"Your Name\"`")

func authorError(err error) error {
	var gerr *GitError
	if !errors.As(err, &gerr) {
		return nil
	}
	s := strings.ToLower(gerr.Stderr)
	switch {
	case strings.Contains(s, "no email was given"),
		strings.Contains(s, "unable to auto-detect email"),
		strings.Contains(s, "empty ident email"):
		return errNoGitAuthorEmail
	case strings.Contains(s, "no name was given"),
		strings.Contains(s, "empty ident name"),
		strings.Contains(s, "name consists only of disallowed characters"):
		return errNoGitAuthorName
	}
	return nil
}
