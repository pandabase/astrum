package money

import "errors"

var (
	ErrInvalidCurrency = errors.New("money: invalid currency code")
	ErrOverflow        = errors.New("money: amount overflow")
)

const maxCurrencyLen = 16

type Currency string

func (c Currency) Validate() error {
	if len(c) < 3 || len(c) > maxCurrencyLen || c[0] < 'A' || c[0] > 'Z' {
		return ErrInvalidCurrency
	}
	for i := range len(c) {
		if (c[i] < 'A' || c[i] > 'Z') && (c[i] < '0' || c[i] > '9') && c[i] != '_' {
			return ErrInvalidCurrency
		}
	}
	return nil
}
