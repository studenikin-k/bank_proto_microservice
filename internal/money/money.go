// Package money — денежные суммы в целых копейках.
//
// Внутри системы (БД, gRPC, расчёт комиссий) деньги хранятся только как int64 копеек:
// float64 не может точно представить 0.10 и при округлении теряет или создаёт копейки.
// В публичном JSON API сумма выглядит как обычное число в рублях: 1234.50.
package money

import (
	"errors"
	"strconv"
	"strings"
)

// Amount — сумма в копейках.
type Amount int64

// MaxAmount ограничивает вводимые суммы (10 млрд ₽), чтобы расчёты не переполняли int64.
const MaxAmount Amount = 10_000_000_000_00

var ErrInvalidAmount = errors.New("некорректная сумма: ожидается число с не более чем двумя знаками после запятой")

// Rubles создаёт сумму из целого числа рублей.
func Rubles(r int64) Amount { return Amount(r * 100) }

// Parse разбирает десятичную запись суммы в рублях ("12", "12.5", "12.50", "-3.07").
// Разбор идёт по строке, без float64, поэтому "0.29" — ровно 29 копеек.
func Parse(s string) (Amount, error) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if intPart == "" || (hasDot && fracPart == "") || !digitsOnly(intPart) || !digitsOnly(fracPart) {
		return 0, ErrInvalidAmount
	}
	if len(fracPart) > 2 {
		// Допускаем только незначащие нули: "12.340" == "12.34".
		if strings.Trim(fracPart[2:], "0") != "" {
			return 0, ErrInvalidAmount
		}
		fracPart = fracPart[:2]
	}
	if len(strings.TrimLeft(intPart, "0")) > 13 {
		return 0, ErrInvalidAmount
	}
	for len(fracPart) < 2 {
		fracPart += "0"
	}

	rub, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, ErrInvalidAmount
	}
	kop, _ := strconv.ParseInt(fracPart, 10, 64)
	a := Amount(rub*100 + kop)
	if neg {
		a = -a
	}
	return a, nil
}

func digitsOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// String возвращает сумму в рублях с двумя знаками: 123450 -> "1234.50".
func (a Amount) String() string {
	sign := ""
	v := int64(a)
	if v < 0 {
		sign = "-"
		v = -v
	}
	kop := v % 100
	s := sign + strconv.FormatInt(v/100, 10) + "."
	if kop < 10 {
		s += "0"
	}
	return s + strconv.FormatInt(kop, 10)
}

// Kopecks возвращает сумму в копейках.
func (a Amount) Kopecks() int64 { return int64(a) }

// MarshalJSON выводит сумму числом в рублях: 1234.50.
func (a Amount) MarshalJSON() ([]byte, error) {
	return []byte(a.String()), nil
}

// UnmarshalJSON принимает число (12.34) или строку ("12.34").
func (a *Amount) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		return ErrInvalidAmount
	}
	if unq, err := strconv.Unquote(s); err == nil {
		s = unq
	}
	v, err := Parse(s)
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// Fee считает комиссию percent% от суммы с округлением до копейки (половина — вверх).
func Fee(a Amount, percent int64) Amount {
	return Amount((int64(a)*percent + 50) / 100)
}
