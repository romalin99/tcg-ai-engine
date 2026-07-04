package helper

import (
	"database/sql"
	"math"
	"testing"

	"github.com/godror/godror"
)

// ── FloatToDecimal128 ─────────────────────────────────────────────────────────

func TestFloatToDecimal128_BasicValue(t *testing.T) {
	d, err := FloatToDecimal128(3.14)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Round-trip through Decimal128ToFloat and check 4-dp precision.
	got, err := Decimal128ToFloat(d)
	if err != nil {
		t.Fatalf("Decimal128ToFloat: %v", err)
	}
	if math.Abs(got-3.14) > 1e-4 {
		t.Errorf("round-trip: got %.6f, want ~3.1400", got)
	}
}

func TestFloatToDecimal128_Zero(t *testing.T) {
	d, err := FloatToDecimal128(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := Decimal128ToFloat(d)
	if err != nil {
		t.Fatalf("Decimal128ToFloat: %v", err)
	}
	if got != 0 {
		t.Errorf("got %v, want 0", got)
	}
}

func TestFloatToDecimal128_Negative(t *testing.T) {
	d, err := FloatToDecimal128(-9.9999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := Decimal128ToFloat(d)
	if err != nil {
		t.Fatalf("Decimal128ToFloat: %v", err)
	}
	if math.Abs(got-(-9.9999)) > 1e-4 {
		t.Errorf("got %.6f, want ~-9.9999", got)
	}
}

func TestFloatToDecimal128_FourDecimalPrecision(t *testing.T) {
	// 1.23456789 should be truncated to 4dp → 1.2346
	d, err := FloatToDecimal128(1.23456789)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := d.String()
	// The string representation should have exactly 4 decimal places
	if s != "1.2346" {
		t.Errorf("4dp string: got %q, want 1.2346", s)
	}
}

// ── StringToDecimal128 ────────────────────────────────────────────────────────

func TestStringToDecimal128_ValidNumber(t *testing.T) {
	d, err := StringToDecimal128("123.456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.String() != "123.456" {
		t.Errorf("got %q, want 123.456", d.String())
	}
}

func TestStringToDecimal128_Zero(t *testing.T) {
	d, err := StringToDecimal128("0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := Decimal128ToFloat(d)
	if got != 0 {
		t.Errorf("got %v, want 0", got)
	}
}

func TestStringToDecimal128_InvalidString_ReturnsError(t *testing.T) {
	_, err := StringToDecimal128("not-a-number")
	if err == nil {
		t.Error("expected error for invalid decimal string")
	}
}

func TestStringToDecimal128_NegativeNumber(t *testing.T) {
	d, err := StringToDecimal128("-55.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.String() != "-55.5" {
		t.Errorf("got %q, want -55.5", d.String())
	}
}

// ── GodrorNumberToDecimal128 ──────────────────────────────────────────────────

func TestGodrorNumberToDecimal128_ValidNumber(t *testing.T) {
	d, err := GodrorNumberToDecimal128(godror.Number("100.25"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.String() != "100.25" {
		t.Errorf("got %q, want 100.25", d.String())
	}
}

func TestGodrorNumberToDecimal128_Integer(t *testing.T) {
	d, err := GodrorNumberToDecimal128(godror.Number("42"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := Decimal128ToFloat(d)
	if got != 42 {
		t.Errorf("got %v, want 42", got)
	}
}

// ── NullGodrorNumberToDecimal128 ──────────────────────────────────────────────

func TestNullGodrorNumberToDecimal128_Valid(t *testing.T) {
	n := sql.Null[godror.Number]{V: godror.Number("7.5"), Valid: true}
	d, err := NullGodrorNumberToDecimal128(n)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.String() != "7.5" {
		t.Errorf("got %q, want 7.5", d.String())
	}
}

func TestNullGodrorNumberToDecimal128_Invalid_ReturnsZero(t *testing.T) {
	n := sql.Null[godror.Number]{Valid: false}
	d, err := NullGodrorNumberToDecimal128(n)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := Decimal128ToFloat(d)
	if got != 0 {
		t.Errorf("got %v, want 0 (null → zero)", got)
	}
}

// ── Decimal128ToFloat ─────────────────────────────────────────────────────────

func TestDecimal128ToFloat_RoundTrip(t *testing.T) {
	cases := []float64{0, 1, -1, 100.5, 0.0001, 99999.9999}
	for _, want := range cases {
		d, err := FloatToDecimal128(want)
		if err != nil {
			t.Fatalf("FloatToDecimal128(%v): %v", want, err)
		}
		got, err := Decimal128ToFloat(d)
		if err != nil {
			t.Fatalf("Decimal128ToFloat: %v", err)
		}
		if math.Abs(got-want) > 1e-4 {
			t.Errorf("round-trip %v: got %v (diff > 1e-4)", want, got)
		}
	}
}

func TestDecimal128ToFloat_PositiveExponent(t *testing.T) {
	// "1E+3" has a positive exponent: bigInt=1, exp=3 → 1000.0
	d, err := StringToDecimal128("1E+3")
	if err != nil {
		t.Fatalf("StringToDecimal128: %v", err)
	}
	got, err := Decimal128ToFloat(d)
	if err != nil {
		t.Fatalf("Decimal128ToFloat: %v", err)
	}
	if math.Abs(got-1000.0) > 1e-4 {
		t.Errorf("got %v, want 1000", got)
	}
}

// ── GodrorNumberToDecimal ─────────────────────────────────────────────────────

func TestGodrorNumberToDecimal_ValidNumber(t *testing.T) {
	d, err := GodrorNumberToDecimal(godror.Number("12.34"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f, _ := d.Float64()
	if math.Abs(f-12.34) > 1e-9 {
		t.Errorf("got %v, want 12.34", f)
	}
}

func TestGodrorNumberToDecimal_EmptyString_ReturnsZero(t *testing.T) {
	d, err := GodrorNumberToDecimal(godror.Number(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.IsZero() {
		t.Errorf("got %v, want zero", d)
	}
}

func TestGodrorNumberToDecimal_NegativeNumber(t *testing.T) {
	d, err := GodrorNumberToDecimal(godror.Number("-3.5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f, _ := d.Float64()
	if math.Abs(f-(-3.5)) > 1e-9 {
		t.Errorf("got %v, want -3.5", f)
	}
}

func TestGodrorNumberToDecimal_InvalidString_ReturnsError(t *testing.T) {
	_, err := GodrorNumberToDecimal(godror.Number("notanumber"))
	if err == nil {
		t.Error("expected error for invalid godror.Number")
	}
}
