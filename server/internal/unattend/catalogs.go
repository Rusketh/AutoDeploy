package unattend

// Catalogs of common Windows configuration values, shown as dropdowns in
// the portal. These are intentionally curated — not exhaustive. Operators
// can still type any value the schema accepts, but the catalog covers
// > 95% of real deployments.

// Option is one row in a portal-shown catalog: machine-facing value plus
// a human label.
type Option struct {
	Value string
	Label string
}

// Locales are Windows locale codes — used for SystemLocale / UserLocale.
var Locales = []Option{
	{"en-US", "English (United States)"},
	{"en-GB", "English (United Kingdom)"},
	{"en-AU", "English (Australia)"},
	{"en-CA", "English (Canada)"},
	{"en-IE", "English (Ireland)"},
	{"en-NZ", "English (New Zealand)"},
	{"en-ZA", "English (South Africa)"},
	{"fr-FR", "French (France)"},
	{"fr-CA", "French (Canada)"},
	{"fr-BE", "French (Belgium)"},
	{"fr-CH", "French (Switzerland)"},
	{"de-DE", "German (Germany)"},
	{"de-AT", "German (Austria)"},
	{"de-CH", "German (Switzerland)"},
	{"es-ES", "Spanish (Spain)"},
	{"es-MX", "Spanish (Mexico)"},
	{"es-AR", "Spanish (Argentina)"},
	{"it-IT", "Italian (Italy)"},
	{"pt-BR", "Portuguese (Brazil)"},
	{"pt-PT", "Portuguese (Portugal)"},
	{"nl-NL", "Dutch (Netherlands)"},
	{"nl-BE", "Dutch (Belgium)"},
	{"sv-SE", "Swedish (Sweden)"},
	{"da-DK", "Danish (Denmark)"},
	{"nb-NO", "Norwegian (Bokmål)"},
	{"fi-FI", "Finnish (Finland)"},
	{"is-IS", "Icelandic"},
	{"pl-PL", "Polish (Poland)"},
	{"cs-CZ", "Czech (Czech Republic)"},
	{"sk-SK", "Slovak"},
	{"hu-HU", "Hungarian"},
	{"ro-RO", "Romanian"},
	{"el-GR", "Greek"},
	{"tr-TR", "Turkish"},
	{"ru-RU", "Russian"},
	{"uk-UA", "Ukrainian"},
	{"ja-JP", "Japanese"},
	{"ko-KR", "Korean"},
	{"zh-CN", "Chinese (Simplified, PRC)"},
	{"zh-TW", "Chinese (Traditional, Taiwan)"},
	{"zh-HK", "Chinese (Traditional, Hong Kong)"},
	{"ar-SA", "Arabic (Saudi Arabia)"},
	{"he-IL", "Hebrew (Israel)"},
	{"th-TH", "Thai (Thailand)"},
	{"vi-VN", "Vietnamese"},
	{"id-ID", "Indonesian"},
	{"ms-MY", "Malay (Malaysia)"},
	{"hi-IN", "Hindi (India)"},
}

// Keyboards are Windows input-locale identifiers — the "LCID:KLID" pairs
// that go in <InputLocale>.
var Keyboards = []Option{
	{"0409:00000409", "English (US) — US keyboard"},
	{"0809:00000809", "English (UK) — UK keyboard"},
	{"0c09:00000409", "English (Australia) — US keyboard"},
	{"1009:00001009", "English (Canada) — Canadian Multilingual"},
	{"1809:00001809", "English (Ireland) — UK keyboard"},
	{"040c:0000040c", "French (France) — French AZERTY"},
	{"0c0c:00001009", "French (Canada) — Canadian Multilingual"},
	{"080c:0000080c", "French (Belgium) — Belgian AZERTY"},
	{"100c:0000100c", "French (Switzerland) — Swiss French"},
	{"0407:00000407", "German (Germany) — German QWERTZ"},
	{"0c07:00000c07", "German (Austria) — Austrian QWERTZ"},
	{"0807:00000807", "German (Switzerland) — Swiss German"},
	{"040a:0000040a", "Spanish (Spain) — Spanish"},
	{"080a:0000080a", "Spanish (Latin America)"},
	{"0410:00000410", "Italian (Italy) — Italian"},
	{"0416:00000416", "Portuguese (Brazil) — Brazilian ABNT"},
	{"0816:00000816", "Portuguese (Portugal) — Portuguese"},
	{"0413:00020409", "Dutch (Netherlands) — US-International"},
	{"041d:0000041d", "Swedish (Sweden)"},
	{"0406:00000406", "Danish (Denmark)"},
	{"0414:00000414", "Norwegian (Bokmål)"},
	{"040b:0000040b", "Finnish (Finland)"},
	{"0415:00000415", "Polish — Polish (programmers)"},
	{"0405:00000405", "Czech"},
	{"041b:0000041b", "Slovak"},
	{"040e:0000040e", "Hungarian"},
	{"0418:00000418", "Romanian"},
	{"0408:00000408", "Greek"},
	{"041f:0000041f", "Turkish — Turkish Q"},
	{"0419:00000419", "Russian"},
	{"0422:00000422", "Ukrainian"},
	{"0411:e0010411", "Japanese — Microsoft IME"},
	{"0412:e0010412", "Korean — Microsoft IME"},
	{"0804:e00e0804", "Chinese (Simplified, PRC) — Microsoft Pinyin"},
	{"0404:e0080404", "Chinese (Traditional, Taiwan) — Microsoft New Phonetic"},
	{"0401:00000401", "Arabic (Saudi Arabia)"},
	{"040d:0002040d", "Hebrew — Standard"},
	{"041e:0000041e", "Thai — Kedmanee"},
	{"042a:0000042a", "Vietnamese — Vietnamese"},
	{"0439:00000439", "Hindi — Devanagari INSCRIPT"},
}

// TimeZones use Windows time-zone IDs — what Set-TimeZone consumes.
var TimeZones = []Option{
	{"GMT Standard Time", "(UTC+00:00) Dublin, Edinburgh, Lisbon, London"},
	{"Greenwich Standard Time", "(UTC+00:00) Monrovia, Reykjavik"},
	{"UTC", "(UTC) Coordinated Universal Time"},
	{"W. Europe Standard Time", "(UTC+01:00) Amsterdam, Berlin, Bern, Rome, Stockholm, Vienna"},
	{"Romance Standard Time", "(UTC+01:00) Brussels, Copenhagen, Madrid, Paris"},
	{"Central Europe Standard Time", "(UTC+01:00) Belgrade, Bratislava, Budapest, Ljubljana, Prague"},
	{"Central European Standard Time", "(UTC+01:00) Sarajevo, Skopje, Warsaw, Zagreb"},
	{"E. Europe Standard Time", "(UTC+02:00) Chisinau"},
	{"FLE Standard Time", "(UTC+02:00) Helsinki, Kyiv, Riga, Sofia, Tallinn, Vilnius"},
	{"GTB Standard Time", "(UTC+02:00) Athens, Bucharest"},
	{"Israel Standard Time", "(UTC+02:00) Jerusalem"},
	{"Egypt Standard Time", "(UTC+02:00) Cairo"},
	{"South Africa Standard Time", "(UTC+02:00) Harare, Pretoria"},
	{"Russian Standard Time", "(UTC+03:00) Moscow, St Petersburg"},
	{"Arabic Standard Time", "(UTC+03:00) Baghdad"},
	{"Arab Standard Time", "(UTC+03:00) Kuwait, Riyadh"},
	{"Turkey Standard Time", "(UTC+03:00) Istanbul"},
	{"Iran Standard Time", "(UTC+03:30) Tehran"},
	{"Arabian Standard Time", "(UTC+04:00) Abu Dhabi, Muscat"},
	{"Azerbaijan Standard Time", "(UTC+04:00) Baku"},
	{"Caucasus Standard Time", "(UTC+04:00) Yerevan"},
	{"Afghanistan Standard Time", "(UTC+04:30) Kabul"},
	{"Pakistan Standard Time", "(UTC+05:00) Islamabad, Karachi"},
	{"India Standard Time", "(UTC+05:30) Chennai, Kolkata, Mumbai, New Delhi"},
	{"Nepal Standard Time", "(UTC+05:45) Kathmandu"},
	{"Central Asia Standard Time", "(UTC+06:00) Astana"},
	{"Bangladesh Standard Time", "(UTC+06:00) Dhaka"},
	{"SE Asia Standard Time", "(UTC+07:00) Bangkok, Hanoi, Jakarta"},
	{"China Standard Time", "(UTC+08:00) Beijing, Chongqing, Hong Kong, Urumqi"},
	{"Singapore Standard Time", "(UTC+08:00) Kuala Lumpur, Singapore"},
	{"Taipei Standard Time", "(UTC+08:00) Taipei"},
	{"W. Australia Standard Time", "(UTC+08:00) Perth"},
	{"Korea Standard Time", "(UTC+09:00) Seoul"},
	{"Tokyo Standard Time", "(UTC+09:00) Osaka, Sapporo, Tokyo"},
	{"AUS Central Standard Time", "(UTC+09:30) Darwin"},
	{"Cen. Australia Standard Time", "(UTC+09:30) Adelaide"},
	{"AUS Eastern Standard Time", "(UTC+10:00) Canberra, Melbourne, Sydney"},
	{"E. Australia Standard Time", "(UTC+10:00) Brisbane"},
	{"Tasmania Standard Time", "(UTC+10:00) Hobart"},
	{"West Pacific Standard Time", "(UTC+10:00) Guam, Port Moresby"},
	{"Central Pacific Standard Time", "(UTC+11:00) Solomon Is., New Caledonia"},
	{"New Zealand Standard Time", "(UTC+12:00) Auckland, Wellington"},
	{"Fiji Standard Time", "(UTC+12:00) Fiji"},
	{"Hawaiian Standard Time", "(UTC-10:00) Hawaii"},
	{"Alaskan Standard Time", "(UTC-09:00) Alaska"},
	{"Pacific Standard Time", "(UTC-08:00) Pacific Time (US & Canada)"},
	{"Mountain Standard Time", "(UTC-07:00) Mountain Time (US & Canada)"},
	{"US Mountain Standard Time", "(UTC-07:00) Arizona"},
	{"Central Standard Time", "(UTC-06:00) Central Time (US & Canada)"},
	{"Central Standard Time (Mexico)", "(UTC-06:00) Guadalajara, Mexico City"},
	{"Eastern Standard Time", "(UTC-05:00) Eastern Time (US & Canada)"},
	{"Atlantic Standard Time", "(UTC-04:00) Atlantic Time (Canada)"},
	{"Newfoundland Standard Time", "(UTC-03:30) Newfoundland"},
	{"E. South America Standard Time", "(UTC-03:00) Brasilia"},
	{"Argentina Standard Time", "(UTC-03:00) Buenos Aires"},
	{"Pacific SA Standard Time", "(UTC-04:00) Santiago"},
}

// Editions per target OS. The string value is what goes in
// <ImageInstall><OSImage><InstallFrom><MetaData><Value>...</Value></MetaData>
// or in the Setup component's UI hint. AutoDeploy applies WIMs directly,
// so this is mostly an operator-facing label; the value also goes in the
// portal's display of "what edition was this unattend cut for".
//
// EditionsFor returns the right list for an os_type string.
func EditionsFor(osType string) []Option {
	switch osType {
	case "windows-11":
		return []Option{
			{"Windows 11 Pro", "Windows 11 Pro"},
			{"Windows 11 Pro for Workstations", "Windows 11 Pro for Workstations"},
			{"Windows 11 Enterprise", "Windows 11 Enterprise"},
			{"Windows 11 Education", "Windows 11 Education"},
			{"Windows 11 Pro Education", "Windows 11 Pro Education"},
			{"Windows 11 IoT Enterprise", "Windows 11 IoT Enterprise"},
			{"Windows 11 Home", "Windows 11 Home"},
		}
	case "windows-10":
		return []Option{
			{"Windows 10 Pro", "Windows 10 Pro"},
			{"Windows 10 Pro for Workstations", "Windows 10 Pro for Workstations"},
			{"Windows 10 Enterprise", "Windows 10 Enterprise"},
			{"Windows 10 Enterprise LTSC", "Windows 10 Enterprise LTSC"},
			{"Windows 10 Education", "Windows 10 Education"},
			{"Windows 10 Pro Education", "Windows 10 Pro Education"},
			{"Windows 10 IoT Enterprise", "Windows 10 IoT Enterprise"},
			{"Windows 10 Home", "Windows 10 Home"},
		}
	case "windows-server-2019":
		return []Option{
			{"Windows Server 2019 Standard", "Windows Server 2019 Standard"},
			{"Windows Server 2019 Datacenter", "Windows Server 2019 Datacenter"},
			{"Windows Server 2019 Standard (Desktop Experience)", "Standard (Desktop Experience)"},
			{"Windows Server 2019 Datacenter (Desktop Experience)", "Datacenter (Desktop Experience)"},
		}
	case "windows-server-2022":
		return []Option{
			{"Windows Server 2022 Standard", "Windows Server 2022 Standard"},
			{"Windows Server 2022 Datacenter", "Windows Server 2022 Datacenter"},
			{"Windows Server 2022 Standard (Desktop Experience)", "Standard (Desktop Experience)"},
			{"Windows Server 2022 Datacenter (Desktop Experience)", "Datacenter (Desktop Experience)"},
		}
	case "windows-server-2025":
		return []Option{
			{"Windows Server 2025 Standard", "Windows Server 2025 Standard"},
			{"Windows Server 2025 Datacenter", "Windows Server 2025 Datacenter"},
			{"Windows Server 2025 Standard (Desktop Experience)", "Standard (Desktop Experience)"},
			{"Windows Server 2025 Datacenter (Desktop Experience)", "Datacenter (Desktop Experience)"},
		}
	}
	// Unknown / mixed: present a few Win10/11 defaults.
	return []Option{
		{"Windows 11 Pro", "Windows 11 Pro"},
		{"Windows 11 Enterprise", "Windows 11 Enterprise"},
		{"Windows 10 Pro", "Windows 10 Pro"},
		{"Windows 10 Enterprise", "Windows 10 Enterprise"},
	}
}

// TargetOSes are the values the portal's TargetOS picker accepts. The
// generator branches on these to emit the correct XML (Windows 11 needs
// BypassNRO, optionally the hardware-requirements bypass; older Windows
// does not).
var TargetOSes = []Option{
	{"windows-11", "Windows 11"},
	{"windows-10", "Windows 10"},
	{"windows-server-2025", "Windows Server 2025"},
	{"windows-server-2022", "Windows Server 2022"},
	{"windows-server-2019", "Windows Server 2019"},
}

// IsWindows11Family reports whether the OS needs the Win11 OOBE network
// bypass.
func IsWindows11Family(osType string) bool {
	return osType == "windows-11"
}

// AccountGroups are the Windows local groups an operator-created account
// may be placed in. Limited to the two we actually emit into the unattend.
var AccountGroups = []Option{
	{"Administrators", "Administrators (full elevation)"},
	{"Users", "Users (standard non-admin account)"},
}

// TelemetryLevels are the Windows AllowTelemetry policy values the
// portal lets operators pick. "off" means do not emit any policy and
// leave the Windows default in place. (Level 0 / Security is Enterprise-
// only and rarely useful; omit it to keep the picker meaningful.)
var TelemetryLevels = []Option{
	{"off", "Leave system default (do not emit policy)"},
	{"1", "1 — Basic / Required diagnostic data"},
	{"2", "2 — Enhanced"},
	{"3", "3 — Full / Optional diagnostic data"},
}

// PowerSchemes are the active power schemes the generator can apply via
// powercfg /setactive. "" leaves the Windows default (Balanced).
var PowerSchemes = []Option{
	{"", "Balanced (Windows default)"},
	{"high", "High Performance"},
}
