package careerquest

import "time"

// Calendar windows use UTC, include both endpoint dates, and never infer
// collection coverage from the first/last record alone.
func datasetReferenceTime(d Dataset) time.Time {
	if date, ok := historyDate(d.ReferenceDate); ok {
		return referenceDay(date)
	}
	return time.Now().UTC()
}

type EngagementRow struct {
	EmployeeID, Name                                                    string
	LastCompletion, LastParticipation                                   string
	Completions30, Completions90, NoShows90, Refusals90                 int
	ObservedFrom, ObservedThrough, Coverage, State, RecommendationState string
	InvalidDates, FutureDates, UnsupportedOutcomes, DuplicateRecords    int
	Records                                                             int
}

func engagementRows(d Dataset, reference time.Time) []EngagementRow {
	day := referenceDay(reference)
	end := day.AddDate(0, 0, 1)
	start30, start90 := day.AddDate(0, 0, -29), day.AddDate(0, 0, -89)
	from, fromOK := historyDate(d.HistoryStart)
	through, throughOK := historyDate(d.HistoryEnd)
	known := fromOK && throughOK && !from.After(through)
	full90 := known && !referenceDay(from).After(start90) && !referenceDay(through).Before(day)
	rows := make([]EngagementRow, len(d.Employees))
	indices := map[string]int{}
	for i, employee := range d.Employees {
		indices[employee.ID] = i
		rows[i] = EngagementRow{EmployeeID: employee.ID, Name: employee.Name, Coverage: "Collection coverage unknown"}
		if known {
			rows[i].Coverage = "Documented collection: " + from.Format("2006-01-02") + " to " + through.Format("2006-01-02")
		}
	}
	// Preserve occurrence semantics: distinct rows remain distinct without IDs;
	// conflicting identified records are all excluded, exact duplicates count once.
	first := map[string]Participation{}
	conflicts := map[string]bool{}
	for _, record := range d.History {
		if occurrenceID(record) == "" {
			continue
		}
		key := record.EmployeeID + ":" + occurrenceID(record)
		if previous, ok := first[key]; ok {
			if previous.ActivityID != record.ActivityID || previous.Status != record.Status || previous.Date != record.Date {
				conflicts[key] = true
			}
		} else {
			first[key] = record
		}
	}
	seen := map[string]bool{}
	for _, record := range d.History {
		i, ok := indices[record.EmployeeID]
		if !ok {
			continue
		}
		row := &rows[i]
		if occurrenceID(record) != "" {
			key := record.EmployeeID + ":" + occurrenceID(record)
			if conflicts[key] || seen[key] {
				row.DuplicateRecords++
				continue
			}
			seen[key] = true
		}
		date, valid := historyDate(record.Date)
		if !valid {
			row.InvalidDates++
			continue
		}
		if !date.Before(end) {
			row.FutureDates++
			continue
		}
		switch record.Status {
		case "completed", "registered", "no_show", "refused", "in_progress", "dropped", "declined", "overdue":
		default:
			row.UnsupportedOutcomes++
			continue
		}
		stamp := date.UTC().Format("2006-01-02")
		row.Records++
		if row.ObservedFrom == "" || stamp < row.ObservedFrom {
			row.ObservedFrom = stamp
		}
		if stamp > row.ObservedThrough {
			row.ObservedThrough = stamp
		}
		if stamp > row.LastParticipation {
			row.LastParticipation = stamp
		}
		if record.Status == "completed" {
			if stamp > row.LastCompletion {
				row.LastCompletion = stamp
			}
			if !date.Before(start30) {
				row.Completions30++
			}
			if !date.Before(start90) {
				row.Completions90++
			}
		}
		if !date.Before(start90) {
			if record.Status == "no_show" {
				row.NoShows90++
			}
			if record.Status == "refused" || record.Status == "declined" {
				row.Refusals90++
			}
		}
	}
	for i := range rows {
		row := &rows[i]
		switch {
		case row.Records == 0:
			row.State = "No participation history available"
		case row.Completions90 > 0:
			row.State = "Completion recorded in the last 90 days"
		case full90:
			row.State = "No completion recorded in the documented 90-day period"
		case row.ObservedFrom > start90.Format("2006-01-02"):
			row.State = "Recently observed; no recent completion recorded; coverage incomplete"
		default:
			row.State = "No recent completion recorded; collection coverage unknown"
		}
	}
	return rows
}

func filterEngagementRows(rows []EngagementRow, filter string) []EngagementRow {
	filtered := []EngagementRow{}
	for _, row := range rows {
		if filter == "no_recent_completion" && row.Completions90 != 0 {
			continue
		}
		if filter == "no_history" && row.Records != 0 {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}
