package twofa

import (
	"crypto/rand"
	"fmt"
	"strings"
)

const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const CodeChars = 16

func NewRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, 0, n)
	for range n {
		c, err := RandomCode(CodeChars, 4)
		if err != nil {
			return nil, err
		}
		codes = append(codes, c)
	}
	return codes, nil
}

func RandomCode(symbols, group int) (string, error) {
	buf := make([]byte, symbols)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	var sb strings.Builder
	for i, b := range buf {
		if group > 0 && i > 0 && i%group == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(Alphabet[int(b)%len(Alphabet)])
	}
	return sb.String(), nil
}

func NormalizeRecovery(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= 'a' && r <= 'z':
			return r - 32
		default:
			return -1
		}
	}, code)
}
