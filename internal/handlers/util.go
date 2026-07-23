package handlers

import "strconv"

func parseInt(s string) (int, error) {
	return strconv.Atoi(s)
}
