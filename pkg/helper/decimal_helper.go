package helper

import (
	"database/sql"
	"fmt"
	"math/big"

	"github.com/godror/godror"
	"github.com/shopspring/decimal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// FloatToDecimal128 converts a float64 to bson.Decimal128, preserving 4 decimal places
// to match Oracle NUMBER(38,4).
func FloatToDecimal128(value float64) (bson.Decimal128, error) {
	return bson.ParseDecimal128(fmt.Sprintf("%.4f", value))
}

// StringToDecimal128 converts a string to bson.Decimal128.
func StringToDecimal128(value string) (bson.Decimal128, error) {
	return bson.ParseDecimal128(value)
}

// GodrorNumberToDecimal128 converts a godror.Number to bson.Decimal128.
func GodrorNumberToDecimal128(number godror.Number) (bson.Decimal128, error) {
	return bson.ParseDecimal128(number.String())
}

// NullGodrorNumberToDecimal128 converts a nullable godror.Number to bson.Decimal128,
// returning "0" when the value is null.
func NullGodrorNumberToDecimal128(nullNumber sql.Null[godror.Number]) (bson.Decimal128, error) {
	if !nullNumber.Valid {
		return bson.ParseDecimal128("0")
	}
	return GodrorNumberToDecimal128(nullNumber.V)
}

// Decimal128ToFloat converts a bson.Decimal128 to float64 using big.Float for precision.
func Decimal128ToFloat(d bson.Decimal128) (float64, error) {
	bigInt, exp, err := d.BigInt()
	if err != nil {
		return 0, err
	}

	bf := new(big.Float).SetInt(bigInt)
	if exp != 0 {
		scale := new(big.Float).SetInt(big.NewInt(10))
		if exp > 0 {
			for i := int32(0); i < int32(exp); i++ {
				bf.Mul(bf, scale)
			}
		} else {
			for i := int32(0); i < -int32(exp); i++ {
				bf.Quo(bf, scale)
			}
		}
	}

	result, _ := bf.Float64()
	return result, nil
}

// GodrorNumberToDecimal converts a godror.Number to decimal.Decimal.
func GodrorNumberToDecimal(num godror.Number) (decimal.Decimal, error) {
	if num == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(string(num))
}
