package service

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func readEnvironmentFiles(specs []string) ([]string, error) {
	var env []string
	for _, spec := range specs {
		for _, path := range strings.Fields(spec) {
			optional := strings.HasPrefix(path, "-")
			path = strings.TrimPrefix(path, "-")
			vars, err := readEnvironmentFile(path)
			if err != nil {
				if optional && os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("EnvironmentFile=%s: %w", path, err)
			}
			env = append(env, vars...)
		}
	}
	return env, nil
}

func readEnvironmentFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var env []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		env = append(env, key+"="+value)
	}
	return env, sc.Err()
}
