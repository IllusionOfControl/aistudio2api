package waa

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"golang.org/x/text/language"
)

//go:embed timezones.json.gz
var timeZoneNamesGzip []byte

// timeZoneNames reads long timezone names exported from Firefox Intl, indexed by locale and IANA timezone, valued with January and July names
var timeZoneNames = sync.OnceValues(func() (map[string]map[string][]string, error) {
	data, err := gunzip(timeZoneNamesGzip)
	if err != nil {
		return nil, err
	}
	var names map[string]map[string][]string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, err
	}
	return names, nil
})

// timeZoneOf returns account timezone and Firefox display name formatter used for Date string timezone comments
func timeZoneOf(profile Profile) (*time.Location, func(time.Time) string, error) {
	zone := profile.TimeZone
	if zone == "" {
		zone = "UTC"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, nil, fmt.Errorf("load timezone %s: %w", zone, err)
	}
	names, err := timeZoneNames()
	if err != nil {
		return nil, nil, fmt.Errorf("read timezone display names: %w", err)
	}
	entries := names[localeKey(slices.Sorted(maps.Keys(names)), profile.Locale)][zone]
	return location, func(moment time.Time) string {
		if len(entries) == 0 {
			_, offset := moment.Zone()
			return gmtOffsetName(offset)
		}
		if len(entries) == 1 {
			return entries[0]
		}
		januaryDST := time.Date(moment.Year(), time.January, 15, 12, 0, 0, 0, location).IsDST()
		if moment.IsDST() != januaryDST {
			return entries[1]
		}
		return entries[0]
	}, nil
}

// localeKey selects the table closest to the locale in a name-sorted locale table.
func localeKey(keys []string, locale string) string {
	if locale == "" {
		return "en-US"
	}
	tag := baseLocale(strings.ReplaceAll(locale, "_", "-"))
	for _, key := range keys {
		if strings.EqualFold(key, tag) {
			return key
		}
	}
	parsed := language.Make(tag)
	base, _ := parsed.Base()
	switch base.String() {
	case "zh":
		script, _ := parsed.Script()
		region, _ := parsed.Region()
		switch {
		case script.String() != "Hant":
			return "zh-CN"
		case region.String() == "HK" || region.String() == "MO":
			return "zh-HK"
		}
		return "zh-TW"
	case "en":
		return "en-US"
	}
	for _, key := range keys {
		if strings.HasPrefix(key, base.String()+"-") {
			return key
		}
	}
	return "en-US"
}

// gmtOffsetName returns localized GMT format display name for unnamed timezones per ICU
func gmtOffsetName(offset int) string {
	if offset == 0 {
		return "GMT"
	}
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("GMT%s%02d:%02d", sign, offset/3600, offset%3600/60)
}
