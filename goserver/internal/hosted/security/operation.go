package security

import "unicode/utf8"

type OperationPurpose string

const (
	OperationBiliServiceReplace OperationPurpose = "bili_service_replace"
	OperationAdminEmailChange   OperationPurpose = "admin_email_change"
	OperationRecoveryRegenerate OperationPurpose = "recovery_regenerate"
	maximumOperationTargetSize                   = 256
)

func ParseOperationPurpose(value string) (OperationPurpose, bool) {
	purpose := OperationPurpose(value)
	switch purpose {
	case OperationBiliServiceReplace, OperationAdminEmailChange, OperationRecoveryRegenerate:
		return purpose, true
	default:
		return "", false
	}
}

func ValidOperationTarget(value string) bool {
	return value != "" && len(value) <= maximumOperationTargetSize && utf8.ValidString(value)
}
