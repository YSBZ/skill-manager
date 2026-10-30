package contentgithub

import (
	"sort"
	"time"

	"skillmanage/internal/content"
)

type starHistoryWeek struct {
	Week  int64 `json:"week"`
	Total int   `json:"total"`
	Days  []int `json:"days"`
}

func sevenDayGrowth(rows []starHistoryWeek, now time.Time) *content.Growth {
	if len(rows) == 0 {
		return nil
	}

	byDay := make(map[int64]int)
	anchors := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.Week <= 0 || len(row.Days) != 7 {
			return nil
		}
		anchors = append(anchors, row.Week)
		for i, count := range row.Days {
			if count < 0 {
				return nil
			}
			day := row.Week + int64(i)*86400
			if previous, ok := byDay[day]; ok && previous != count {
				return nil
			}
			byDay[day] = count
		}
	}

	sort.Slice(anchors, func(i, j int) bool { return anchors[i] < anchors[j] })
	for i := 1; i < len(anchors); i++ {
		if anchors[i]-anchors[i-1] != 7*86400 {
			return nil
		}
	}

	offset := anchors[0] % 86400
	if offset < 0 {
		offset += 86400
	}
	elapsedDays := (now.Unix() - offset) / 86400
	last := offset + (elapsedDays-1)*86400
	if last <= 0 {
		return nil
	}
	if _, ok := byDay[last]; !ok {
		return nil
	}

	first, sum := last-6*86400, 0
	for i := int64(0); i < 7; i++ {
		value, ok := byDay[first+i*86400]
		if !ok {
			return nil
		}
		sum += value
	}

	return &content.Growth{
		Metric:       "stars_added",
		Value:        sum,
		WindowStart:  time.Unix(first, 0).UTC().Format(time.RFC3339),
		WindowEnd:    time.Unix(last+86400, 0).UTC().Format(time.RFC3339),
		ObservedDays: 7,
		Complete:     true,
	}
}

func snapshotGrowth(rows []content.Snapshot, now time.Time) *content.Growth {
	valid := make([]content.Snapshot, 0, len(rows))
	for _, row := range rows {
		if row.RepositoryID > 0 && row.Stars >= 0 && !row.At.After(now) {
			valid = append(valid, row)
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].At.Before(valid[j].At) })
	if len(valid) < 2 {
		return nil
	}

	end := valid[len(valid)-1]
	startIndex := -1
	for i := len(valid) - 2; i >= 0; i-- {
		if valid[i].At.Before(end.At) && end.At.Sub(valid[i].At) >= 7*24*time.Hour {
			startIndex = i
			break
		}
	}
	if startIndex < 0 {
		for i := 0; i < len(valid)-1; i++ {
			if valid[i].At.Before(end.At) {
				startIndex = i
				break
			}
		}
	}
	if startIndex < 0 {
		return nil
	}

	start := valid[startIndex]
	span := end.At.Sub(start.At)
	return &content.Growth{
		Metric:       "stars_net",
		Value:        end.Stars - start.Stars,
		WindowStart:  start.At.UTC().Format(time.RFC3339),
		WindowEnd:    end.At.UTC().Format(time.RFC3339),
		ObservedDays: int(span / (24 * time.Hour)),
		Complete:     span == 7*24*time.Hour,
	}
}
