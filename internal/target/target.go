package target

import (
	"errors"
	"strings"
)

type Target struct {
	Host string
	Path string
}

func Parse(value string) (Target, error) {
	separator := strings.IndexByte(value, ':')
	if separator <= 0 || separator == len(value)-1 {
		return Target{}, errors.New("remote target must have the form host:/path")
	}
	host := value[:separator]
	remotePath := value[separator+1:]
	if strings.HasPrefix(host, "-") || strings.ContainsAny(host, "\x00\r\n") || strings.IndexByte(remotePath, 0) >= 0 {
		return Target{}, errors.New("invalid remote target")
	}
	return Target{Host: host, Path: remotePath}, nil
}
