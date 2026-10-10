package aistudio

import (
	"fmt"
	"strings"
)

// harmCategories defines Gemini API safety category names and wire codes; the first four are official playground categories
var harmCategories = []struct {
	name string
	code int64
}{
	{"HARM_CATEGORY_HARASSMENT", 7},
	{"HARM_CATEGORY_HATE_SPEECH", 8},
	{"HARM_CATEGORY_SEXUALLY_EXPLICIT", 9},
	{"HARM_CATEGORY_DANGEROUS_CONTENT", 10},
	{"HARM_CATEGORY_CIVIC_INTEGRITY", 11},
}

// ignoredHarmCategories lists valid Gemini API categories that Gemini models reject or lack known wire codes for
var ignoredHarmCategories = map[string]bool{
	"HARM_CATEGORY_UNSPECIFIED": true, "HARM_CATEGORY_DEROGATORY": true, "HARM_CATEGORY_TOXICITY": true,
	"HARM_CATEGORY_VIOLENCE": true, "HARM_CATEGORY_SEXUAL": true, "HARM_CATEGORY_MEDICAL": true,
	"HARM_CATEGORY_DANGEROUS": true, "HARM_CATEGORY_IMAGE_HATE": true, "HARM_CATEGORY_IMAGE_DANGEROUS_CONTENT": true,
	"HARM_CATEGORY_IMAGE_HARASSMENT": true, "HARM_CATEGORY_IMAGE_SEXUALLY_EXPLICIT": true, "HARM_CATEGORY_JAILBREAK": true,
}

// harmThresholds maps Gemini API block threshold names to wire codes
var harmThresholds = map[string]int64{
	"BLOCK_LOW_AND_ABOVE": 1, "BLOCK_MEDIUM_AND_ABOVE": 2, "BLOCK_ONLY_HIGH": 3, "BLOCK_NONE": 4, "OFF": 5,
}

// resolveSafetySettings validates requested safety settings, returning thresholds by category code; non-image models default unspecified categories to OFF
func resolveSafetySettings(settings []SafetySetting, imageRoute bool) ([]SafetySetting, error) {
	thresholds := map[string]string{}
	if !imageRoute {
		for _, category := range harmCategories[:4] {
			thresholds[category.name] = "OFF"
		}
	}
	for _, setting := range settings {
		category := strings.ToUpper(strings.TrimSpace(setting.Category))
		threshold := strings.ToUpper(strings.TrimSpace(setting.Threshold))
		known := false
		for _, item := range harmCategories {
			known = known || item.name == category
		}
		if !known && !ignoredHarmCategories[category] {
			return nil, fmt.Errorf("safetySettings category %q is not supported; available categories: HARM_CATEGORY_HARASSMENT, HARM_CATEGORY_HATE_SPEECH, HARM_CATEGORY_SEXUALLY_EXPLICIT, HARM_CATEGORY_DANGEROUS_CONTENT, HARM_CATEGORY_CIVIC_INTEGRITY", setting.Category)
		}
		if threshold == "HARM_BLOCK_THRESHOLD_UNSPECIFIED" {
			continue
		}
		if _, ok := harmThresholds[threshold]; !ok {
			return nil, fmt.Errorf("safetySettings threshold %q is not supported", setting.Threshold)
		}
		if !known {
			continue
		}
		thresholds[category] = threshold
	}
	resolved := make([]SafetySetting, 0, len(thresholds))
	for _, category := range harmCategories {
		if threshold, ok := thresholds[category.name]; ok {
			resolved = append(resolved, SafetySetting{Category: category.name, Threshold: threshold})
		}
	}
	return resolved, nil
}

// encodeSafetySettings encodes safety settings in GenerateContent root field 3
func encodeSafetySettings(settings []SafetySetting) []any {
	if len(settings) == 0 {
		return nil
	}
	wire := make([]any, 0, len(settings))
	for _, setting := range settings {
		for _, category := range harmCategories {
			if category.name == setting.Category {
				wire = append(wire, []any{nil, nil, category.code, harmThresholds[setting.Threshold]})
			}
		}
	}
	return wire
}
