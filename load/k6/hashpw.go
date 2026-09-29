package main

import (
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Prints a bcrypt hash of the password file. Used by load/k6/run.sh so the
// load account can be stored the same way Foldex stores every password.
func main() {
	if len(os.Args) != 2 {
		os.Stderr.WriteString("usage: hashpw <password-file>\n")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		os.Stderr.WriteString("read password file\n")
		os.Exit(1)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimRight(string(raw), "\n")), bcrypt.DefaultCost)
	if err != nil {
		os.Stderr.WriteString("bcrypt\n")
		os.Exit(1)
	}
	os.Stdout.Write(hash)
}
