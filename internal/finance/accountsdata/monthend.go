package accountsdata

import "time"

func LastDayOf(d time.Time) time.Time {
	return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, -1)
}

func ClosesItsMonth(d time.Time) bool {
	return d.Day() == LastDayOf(d).Day()
}
