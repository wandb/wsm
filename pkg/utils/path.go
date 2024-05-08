package utils

import (
	"fmt"
	"os"
	"regexp"
)

func PathFromDir(dir string, file string) (string, error) {
	pattern := fmt.Sprintf(`^%s-\d+\.\d+\.\d+\.[a-zA-Z0-9]+$`, file)
	regex := regexp.MustCompile(pattern)

	files, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	for _, file := range files {
		if !file.IsDir() && regex.MatchString(file.Name()) {
			return dir + "/" + file.Name(), nil
		}
	}

	return "", fmt.Errorf("file not found")
}
