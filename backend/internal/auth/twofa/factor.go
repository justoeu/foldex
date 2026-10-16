package twofa

type Factor string

const FactorTOTP Factor = "totp"

// RemovalCheck is the named input to MayRemove: five booleans in a row
// invited a transposed admin/factor flag, which compiles fine and answers
// the wrong policy question.
type RemovalCheck struct {
	RequireForAdmins bool
	TOTPOnly         bool
	IsAdmin          bool
	EmailEnabled     bool
	TOTPEnabled      bool
	Removing         Factor
}

func MayRemove(c RemovalCheck) bool {
	if !c.RequireForAdmins || !c.IsAdmin {
		return true
	}
	if c.TOTPOnly {
		return c.Removing != FactorTOTP
	}
	if c.Removing == FactorTOTP {
		return c.EmailEnabled
	}
	return c.TOTPEnabled
}
