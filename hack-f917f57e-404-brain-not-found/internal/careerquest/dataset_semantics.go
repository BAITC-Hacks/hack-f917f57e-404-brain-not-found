package careerquest

func growthVersionForDataset(d Dataset) string {
	if d.Source == "halyk-v1" {
		return OfficialGrowthRuleVersion
	}
	return StandardGrowthRuleVersion
}

// Native record IDs and local completion request IDs occupy separate namespaces.
func occurrenceID(p Participation) string {
	if p.RecordID != "" {
		return "record:" + p.RecordID
	}
	if p.RequestID != "" {
		return "request:" + p.RequestID
	}
	return ""
}

func activityHours(a Activity) float64 {
	if a.DurationHours > 0 {
		return a.DurationHours
	}
	return float64(a.Hours)
}
