package deposit

import "errors"

var (
	ErrDepositNotFound  = errors.New("invite deposit not found")
	ErrDepositConsumed  = errors.New("invite deposit already consumed")
	ErrDepositRevoked   = errors.New("invite deposit revoked")
	ErrDepositExpired   = errors.New("invite deposit expired")
)
