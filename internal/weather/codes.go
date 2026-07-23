package weather

// WMOCode describes the icon/text for one Open-Meteo "weathercode" value.
// Open-Meteo uses the WMO 4677 weather interpretation codes - see
// https://open-meteo.com/en/docs#weathervariables for the full table.
type WMOCode struct {
	Description string
	Icon        string // short key rendered as an inline SVG on the kiosk side
}

var wmoCodes = map[int]WMOCode{
	0:  {"Clear sky", "sun"},
	1:  {"Mainly clear", "sun"},
	2:  {"Partly cloudy", "cloud-sun"},
	3:  {"Overcast", "cloud"},
	45: {"Fog", "fog"},
	48: {"Depositing rime fog", "fog"},
	51: {"Light drizzle", "rain"},
	53: {"Moderate drizzle", "rain"},
	55: {"Dense drizzle", "rain"},
	56: {"Light freezing drizzle", "rain"},
	57: {"Dense freezing drizzle", "rain"},
	61: {"Slight rain", "rain"},
	63: {"Moderate rain", "rain"},
	65: {"Heavy rain", "rain"},
	66: {"Light freezing rain", "rain"},
	67: {"Heavy freezing rain", "rain"},
	71: {"Slight snow fall", "snow"},
	73: {"Moderate snow fall", "snow"},
	75: {"Heavy snow fall", "snow"},
	77: {"Snow grains", "snow"},
	80: {"Slight rain showers", "rain"},
	81: {"Moderate rain showers", "rain"},
	82: {"Violent rain showers", "rain"},
	85: {"Slight snow showers", "snow"},
	86: {"Heavy snow showers", "snow"},
	95: {"Thunderstorm", "thunderstorm"},
	96: {"Thunderstorm with slight hail", "thunderstorm"},
	99: {"Thunderstorm with heavy hail", "thunderstorm"},
}

// DescribeCode returns the description/icon for a WMO weather code, falling
// back to a generic cloud icon for any code not in the table (Open-Meteo's
// code set is stable, but this avoids a blank/missing icon if it ever adds one).
func DescribeCode(code int) WMOCode {
	if c, ok := wmoCodes[code]; ok {
		return c
	}
	return WMOCode{Description: "Unknown", Icon: "cloud"}
}
