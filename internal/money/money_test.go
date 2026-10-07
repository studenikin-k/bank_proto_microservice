package money

import (
	"encoding/json"
	"testing"
)

func TestParse(t *testing.T) {
	valid := map[string]Amount{
		"0":             0,
		"12":            1200,
		"12.5":          1250,
		"12.50":         1250,
		"12.340":        1234,
		"0.29":          29,
		"0.01":          1,
		"-3.07":         -307,
		"20000":         2000000,
		" 7.1 ":         710,
		"9999999999999": 999999999999900,
	}
	for in, want := range valid {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}

	invalid := []string{"", ".", "1.", ".5", "1.234", "1e3", "abc", "1,5", "--1", "12.3.4", "+5", "10000000000000"}
	for _, in := range invalid {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) должен вернуть ошибку", in)
		}
	}
}

func TestString(t *testing.T) {
	cases := map[Amount]string{0: "0.00", 1: "0.01", 29: "0.29", 1250: "12.50", -307: "-3.07", 2000000: "20000.00"}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("Amount(%d).String() = %q; want %q", in, got, want)
		}
	}
}

func TestJSONRoundTrip(t *testing.T) {
	var v struct {
		A Amount `json:"a"`
		B Amount `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a": 12.34, "b": "0.10"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != 1234 || v.B != 10 {
		t.Fatalf("получено %d и %d", v.A, v.B)
	}
	out, _ := json.Marshal(v)
	if string(out) != `{"a":12.34,"b":0.10}` {
		t.Fatalf("неожиданный JSON: %s", out)
	}
	if err := json.Unmarshal([]byte(`{"a": 0.001}`), &v); err == nil {
		t.Fatal("сумма с долями копейки должна отклоняться")
	}
}

func TestFee(t *testing.T) {
	cases := []struct {
		amount  Amount
		percent int64
		want    Amount
	}{
		{10000, 1, 100}, // 100.00 ₽ * 1% = 1.00 ₽
		{1234, 1, 12},   // 12.34 ₽ * 1% = 0.1234 ₽ -> 0.12 ₽
		{1250, 1, 13},   // 0.125 ₽ -> 0.13 ₽ (половина вверх)
		{1234, 3, 37},   // 0.3702 ₽ -> 0.37 ₽
		{1, 1, 0},       // комиссия с одной копейки округляется до нуля
		{MaxAmount, 3, MaxAmount * 3 / 100},
	}
	for _, c := range cases {
		if got := Fee(c.amount, c.percent); got != c.want {
			t.Errorf("Fee(%d, %d) = %d; want %d", c.amount, c.percent, got, c.want)
		}
	}
}
