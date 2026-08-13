package render

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

func CountryName(code string) string {
	if name, ok := countryNamesMap[code]; ok {
		return name
	}
	return code
}
