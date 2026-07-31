package taskalign

import "strings"

func MapDomainOrQueue(taskType, status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	taskType = strings.ToLower(strings.TrimSpace(taskType))
	switch taskType {
	case "subtitle":
		switch status {
		case "pending":
			return "waiting"
		case "running", "done", "failed":
			return status
		}
	case "preview":
		switch status {
		case "ready":
			return "done"
		case "waiting", "failed":
			return status
		}
	}
	// atrack, keyframe, encrypt, and queue statuses
	switch status {
	case "pending":
		return "waiting"
	case "ready":
		return "done"
	case "waiting", "running", "done", "failed", "cancelled":
		return status
	}
	return ""
}

func priority(s string) int {
	switch s {
	case "running":
		return 5
	case "waiting":
		return 4
	case "failed":
		return 3
	case "cancelled":
		return 2
	case "done":
		return 1
	default:
		return 0
	}
}

func Synthesize(queueStatus, domainStatus, taskType string) string {
	q := MapDomainOrQueue(taskType, queueStatus)
	d := MapDomainOrQueue(taskType, domainStatus)
	if (q == "failed" || q == "cancelled") && d == "waiting" {
		return q
	}
	if priority(q) >= priority(d) {
		if q != "" {
			return q
		}
		return d
	}
	return d
}
