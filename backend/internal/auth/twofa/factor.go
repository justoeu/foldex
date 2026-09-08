package twofa

type Factor string

const (
	FactorTOTP  Factor = "totp"
	FactorEmail Factor = "email"
)

func MayRemove(requireForAdmins, totpOnly, isAdmin, emailEnabled, totpEnabled bool, removing Factor) bool {
	if !requireForAdmins || !isAdmin {
		return true
	}
	if totpOnly {
		return removing != FactorTOTP
	}
	if removing == FactorTOTP {
		return emailEnabled
	}
	return totpEnabled
}
