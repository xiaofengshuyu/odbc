package odbc

import (
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"
)

const (
	timeFormat = "2006-01-02 15:04:05.999999"
)

/******************************************************************************
*                           Time related utils                                *
******************************************************************************/

// NullTime represents a time.Time that may be NULL.
// NullTime implements the Scanner interface so
// it can be used as a scan destination:
//
//  var nt NullTime
//  err := db.QueryRow("SELECT time FROM foo WHERE id=?", id).Scan(&nt)
//  ...
//  if nt.Valid {
//     // use nt.Time
//  } else {
//     // NULL value
//  }
//
// This NullTime implementation is not driver-specific
type NullTime struct {
	Time  time.Time
	Valid bool // Valid is true if Time is not NULL
}

// Scan implements the Scanner interface.
// The value type must be time.Time or string / []byte (formatted time-string),
// otherwise Scan fails.
func (nt *NullTime) Scan(value interface{}) (err error) {
	if value == nil {
		nt.Time, nt.Valid = time.Time{}, false
		return
	}

	switch v := value.(type) {
	case time.Time:
		nt.Time, nt.Valid = v, true
		return
	case []byte:
		nt.Time, err = parseDateTime(string(v), time.UTC)
		nt.Valid = (err == nil)
		return
	case string:
		nt.Time, err = parseDateTime(v, time.UTC)
		nt.Valid = (err == nil)
		return
	}

	nt.Valid = false
	return fmt.Errorf("Can't convert %T to time.Time", value)
}

// Value implements the driver Valuer interface.
func (nt NullTime) Value() (driver.Value, error) {
	if !nt.Valid {
		return nil, nil
	}
	return nt.Time, nil
}

func parseDateTime(str string, loc *time.Location) (t time.Time, err error) {
	base := "0000-00-00 00:00:00.0000000"
	switch len(str) {
	case 10, 19, 21, 22, 23, 24, 25, 26: // up to "YYYY-MM-DD HH:MM:SS.MMMMMM"
		if str == base[:len(str)] {
			return
		}
		t, err = time.Parse(timeFormat[:len(str)], str)
	default:
		err = fmt.Errorf("invalid time string: %s", str)
		return
	}

	// Adjust location
	if err == nil && loc != time.UTC {
		y, mo, d := t.Date()
		h, mi, s := t.Clock()
		t, err = time.Date(y, mo, d, h, mi, s, t.Nanosecond(), loc), nil
	}

	return
}

func parseBinaryDateTime(num uint64, data []byte, loc *time.Location) (driver.Value, error) {
	switch num {
	case 0:
		return time.Time{}, nil
	case 4:
		return time.Date(
			int(binary.LittleEndian.Uint16(data[:2])), // year
			time.Month(data[2]),                       // month
			int(data[3]),                              // day
			0, 0, 0, 0,
			loc,
		), nil
	case 7:
		return time.Date(
			int(binary.LittleEndian.Uint16(data[:2])), // year
			time.Month(data[2]),                       // month
			int(data[3]),                              // day
			int(data[4]),                              // hour
			int(data[5]),                              // minutes
			int(data[6]),                              // seconds
			0,
			loc,
		), nil
	case 11:
		return time.Date(
			int(binary.LittleEndian.Uint16(data[:2])), // year
			time.Month(data[2]),                       // month
			int(data[3]),                              // day
			int(data[4]),                              // hour
			int(data[5]),                              // minutes
			int(data[6]),                              // seconds
			int(binary.LittleEndian.Uint32(data[7:11]))*1000, // nanoseconds
			loc,
		), nil
	}
	return nil, fmt.Errorf("invalid DATETIME packet length %d", num)
}

// zeroDateTime is used in formatBinaryDateTime to avoid an allocation
// if the DATE or DATETIME has the zero value.
// It must never be changed.
// The current behavior depends on database/sql copying the result.
var zeroDateTime = []byte("0000-00-00 00:00:00.000000")

const digits01 = "0123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789"
const digits10 = "0000000000111111111122222222223333333333444444444455555555556666666666777777777788888888889999999999"

func appendMicrosecs(dst, src []byte, decimals int) []byte {
	if decimals <= 0 {
		return dst
	}
	if len(src) == 0 {
		return append(dst, ".000000"[:decimals+1]...)
	}

	microsecs := binary.LittleEndian.Uint32(src[:4])
	p1 := byte(microsecs / 10000)
	microsecs -= 10000 * uint32(p1)
	p2 := byte(microsecs / 100)
	microsecs -= 100 * uint32(p2)
	p3 := byte(microsecs)

	switch decimals {
	default:
		return append(dst, '.',
			digits10[p1], digits01[p1],
			digits10[p2], digits01[p2],
			digits10[p3], digits01[p3],
		)
	case 1:
		return append(dst, '.',
			digits10[p1],
		)
	case 2:
		return append(dst, '.',
			digits10[p1], digits01[p1],
		)
	case 3:
		return append(dst, '.',
			digits10[p1], digits01[p1],
			digits10[p2],
		)
	case 4:
		return append(dst, '.',
			digits10[p1], digits01[p1],
			digits10[p2], digits01[p2],
		)
	case 5:
		return append(dst, '.',
			digits10[p1], digits01[p1],
			digits10[p2], digits01[p2],
			digits10[p3],
		)
	}
}

func formatBinaryDateTime(src []byte, length uint8) (driver.Value, error) {
	// length expects the deterministic length of the zero value,
	// negative time and 100+ hours are automatically added if needed
	if len(src) == 0 {
		return zeroDateTime[:length], nil
	}
	var dst []byte      // return value
	var p1, p2, p3 byte // current digit pair

	switch length {
	case 10, 19, 21, 22, 23, 24, 25, 26:
	default:
		t := "DATE"
		if length > 10 {
			t += "TIME"
		}
		return nil, fmt.Errorf("illegal %s length %d", t, length)
	}
	switch len(src) {
	case 4, 7, 11:
	default:
		t := "DATE"
		if length > 10 {
			t += "TIME"
		}
		return nil, fmt.Errorf("illegal %s packet length %d", t, len(src))
	}
	dst = make([]byte, 0, length)
	// start with the date
	year := binary.LittleEndian.Uint16(src[:2])
	pt := year / 100
	p1 = byte(year - 100*uint16(pt))
	p2, p3 = src[2], src[3]
	dst = append(dst,
		digits10[pt], digits01[pt],
		digits10[p1], digits01[p1], '-',
		digits10[p2], digits01[p2], '-',
		digits10[p3], digits01[p3],
	)
	if length == 10 {
		return dst, nil
	}
	if len(src) == 4 {
		return append(dst, zeroDateTime[10:length]...), nil
	}
	dst = append(dst, ' ')
	p1 = src[4] // hour
	src = src[5:]

	// p1 is 2-digit hour, src is after hour
	p2, p3 = src[0], src[1]
	dst = append(dst,
		digits10[p1], digits01[p1], ':',
		digits10[p2], digits01[p2], ':',
		digits10[p3], digits01[p3],
	)
	return appendMicrosecs(dst, src[2:], int(length)-20), nil
}

func formatBinaryTime(src []byte, length uint8) (driver.Value, error) {
	// length expects the deterministic length of the zero value,
	// negative time and 100+ hours are automatically added if needed
	if len(src) == 0 {
		return zeroDateTime[11 : 11+length], nil
	}
	var dst []byte // return value

	switch length {
	case
		8,                      // time (can be up to 10 when negative and 100+ hours)
		10, 11, 12, 13, 14, 15: // time with fractional seconds
	default:
		return nil, fmt.Errorf("illegal TIME length %d", length)
	}
	switch len(src) {
	case 8, 12:
	default:
		return nil, fmt.Errorf("invalid TIME packet length %d", len(src))
	}
	// +2 to enable negative time and 100+ hours
	dst = make([]byte, 0, length+2)
	if src[0] == 1 {
		dst = append(dst, '-')
	}
	days := binary.LittleEndian.Uint32(src[1:5])
	hours := int64(days)*24 + int64(src[5])

	if hours >= 100 {
		dst = strconv.AppendInt(dst, hours, 10)
	} else {
		dst = append(dst, digits10[hours], digits01[hours])
	}

	min, sec := src[6], src[7]
	dst = append(dst, ':',
		digits10[min], digits01[min], ':',
		digits10[sec], digits01[sec],
	)
	return appendMicrosecs(dst, src[8:], int(length)-9), nil
}
