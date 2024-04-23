package utils

import (
	"fmt"
	"os"
	"regexp"
)

const (
	OperatorChartPattern = `^operator-\d+\.\d+\.\d+\.[a-zA-Z0-9]+$`
	WandbChartPattern    = `^operator-wandb-\d+\.\d+\.\d+\.[a-zA-Z0-9]+$`
)

func PathFromDir(dir string, pattern string) (string, error) {
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
