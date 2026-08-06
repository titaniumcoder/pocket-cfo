package render

// countryNamesMap maps ISO 3166-1 alpha-2 codes to English country names, for
// the recipient address block — a postal address reads a country name, not
// a code. Covers the EU27, EFTA, and a handful of other common trading
// partners; anything missing falls back to the raw code (CountryName never
// errors or blanks out).
var countryNamesMap = map[string]string{
	"AT": "Austria",
	"BE": "Belgium",
	"BG": "Bulgaria",
	"HR": "Croatia",
	"CY": "Cyprus",
	"CZ": "Czechia",
	"DK": "Denmark",
	"EE": "Estonia",
	"FI": "Finland",
	"FR": "France",
	"DE": "Germany",
	"GR": "Greece",
	"HU": "Hungary",
	"IE": "Ireland",
	"IT": "Italy",
	"LV": "Latvia",
	"LT": "Lithuania",
	"LU": "Luxembourg",
	"MT": "Malta",
	"NL": "Netherlands",
	"PL": "Poland",
	"PT": "Portugal",
	"RO": "Romania",
	"SK": "Slovakia",
	"SI": "Slovenia",
	"ES": "Spain",
	"SE": "Sweden",

	"CH": "Switzerland",
	"NO": "Norway",
	"IS": "Iceland",
	"LI": "Liechtenstein",

	"GB": "United Kingdom",
	"US": "United States",
	"CA": "Canada",
	"AU": "Australia",
}

// CountryName renders code as an English country name, or the raw code if
// it's not in the map — display-only, never an error.
func CountryName(code string) string {
	if name, ok := countryNamesMap[code]; ok {
		return name
	}
	return code
}
