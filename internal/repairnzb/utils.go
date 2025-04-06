package repairnzb

import (
	"math/rand"
	"time"
)

func generateRandomMessageID() string {
	return generateRandomString(32) + "@" + generateRandomString(8) + "." + generateRandomString(3)
}

func generateRandomString(size int) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	seededRand := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Create the random string
	result := make([]byte, size)
	for i := range result {
		result[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(result)
}
