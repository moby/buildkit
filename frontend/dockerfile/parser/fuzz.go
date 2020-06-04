package parser

import (
	"os"
)

func Fuzz(data []byte) int {
	f, err := os.Create("Dockerfile")
	if err != nil {
		return -1
	}
	defer f.Close()
	defer os.Remove("Dockerfile")
	_, err = f.Write(data)
	if err != nil {
		return -1
	}
	df, err := os.Open("Dockerfile")
	if err != nil {
		return 0
	}
	_, err = Parse(df)
	if err != nil {
		return 0
	}
	return 1
}
