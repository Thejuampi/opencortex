package knowledge

func NextVersion(current int) int {
	if current < 0 {
		return 1
	}
	return current + 1
}
