package knowledge

import "strings"

func BuildFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	return strings.ReplaceAll(q, `"`, "")
}
