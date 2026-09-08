package links

import (
	"errors"
	"net/url"
	"strings"
)

// The absolute-http(s) URL rule for link URLs, in one place: dto.Create,
// dto.Update and the importer's JSON validation all enforce it. The scheme
// policy (never ftp:, never scheme-relative) is a security-relevant business
// rule — three private copies could drift and let imported rows bypass what
// the CRUD API enforces.
func ValidateAbsoluteHTTPURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("url must be an absolute http(s) URL")
	}
	s := strings.ToLower(u.Scheme)
	if s != "http" && s != "https" {
		return errors.New("url scheme must be http or https")
	}
	return nil
}
