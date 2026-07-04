package conv

import (
	"database/sql"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
)

// Basic unwrap functions ---------------------------------------------

// String unwraps a sql.NullString and returns the string or empty when invalid.
func String(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// Int64 unwraps a sql.NullInt64 and returns the int64 or 0 when invalid.
func Int64(ni sql.NullInt64) int64 {
	if ni.Valid {
		return ni.Int64
	}
	return 0
}

// Int unwraps a sql.NullInt64 and returns the int or 0 when invalid.
func Int(ni sql.NullInt64) int {
	if ni.Valid {
		return int(ni.Int64)
	}
	return 0
}

// Time unwraps a sql.NullTime and returns the time or empty when invalid.
func Time(nt sql.NullTime) time.Time {
	if nt.Valid {
		return nt.Time
	}
	return time.Time{}
}

// Float64 unwraps a sql.NullFloat64 and returns the float64 or 0 when invalid.
func Float64(nf sql.NullFloat64) float64 {
	if nf.Valid {
		return nf.Float64
	}
	return 0
}

// Bool unwraps a sql.NullBool and returns the bool or false when invalid.
func Bool(nb sql.NullBool) bool {
	if nb.Valid {
		return nb.Bool
	}
	return false
}

// ProtoBuf helper -----------------------------------------------------

// PBString converts a sql.NullString into a protobuf value, using null when empty.
func PBString(ns sql.NullString) *structpb.Value {
	if ns.Valid && ns.String != "" {
		return structpb.NewStringValue(ns.String)
	}
	return structpb.NewNullValue()
}

// Pointer to null representation --------------------------------------

// StringPtr converts a string pointer to a sql.NullString.
func StringPtr(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: *s, Valid: true}
}

func NullString(ns sql.NullString, defaultVal string) string {
	if ns.Valid {
		return ns.String
	}
	return defaultVal
}

func NullStringToString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func NewNullString(s string) sql.NullString {
	return sql.NullString{
		String: s,
		Valid:  s != "",
	}
}

func NullInt64(ni sql.NullInt64, defaultVal int64) int64 {
	if ni.Valid {
		return ni.Int64
	}
	return defaultVal
}

func NullInt32(ni sql.NullInt32, defaultVal int32) int32 {
	if ni.Valid {
		return ni.Int32
	}
	return defaultVal
}

func NullInt16(ni sql.NullInt16, defaultVal int16) int16 {
	if ni.Valid {
		return ni.Int16
	}
	return defaultVal
}

func NewNullInt64(i int64) sql.NullInt64 {
	return sql.NullInt64{
		Int64: i,
		Valid: i != 0,
	}
}

func NewNullInt32(i int32) sql.NullInt32 {
	return sql.NullInt32{
		Int32: i,
		Valid: i != 0,
	}
}

func NewNullInt16(i int16) sql.NullInt16 {
	return sql.NullInt16{
		Int16: i,
		Valid: i != 0,
	}
}

func NullBool(nb sql.NullBool, defaultVal bool) bool {
	if nb.Valid {
		return nb.Bool
	}
	return defaultVal
}

func NewNullBool(b bool) sql.NullBool {
	return sql.NullBool{
		Bool:  b,
		Valid: true,
	}
}

func FormatNullTime(nt sql.NullTime, layout string) string {
	inlayout := "2006-01-02 15:04:05"
	if len(layout) > 0 {
		inlayout = layout
	}
	if nt.Valid {
		return nt.Time.Format(inlayout)
	}
	return ""
}

func FromNullTime(nt sql.NullTime) string {
	if nt.Valid {
		return nt.Time.Format("2006-01-02 15:04:05")
	}
	return ""
}

func NullTime(nt sql.NullTime, defaultTime time.Time) time.Time {
	if nt.Valid {
		return nt.Time
	}
	return defaultTime
}

func NewNullTime(t time.Time) sql.NullTime {
	return sql.NullTime{
		Time:  t,
		Valid: !t.IsZero(),
	}
}
