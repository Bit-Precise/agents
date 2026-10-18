package control

import (
	"errors"
	"io"
	"os"
	"strings"
)

// ReadSecret reads a trusted configured path, including Kubernetes projected
// Secret symlinks and root:fsGroup 0440 files. Client credential-file ownership
// rules deliberately remain stricter in the agentd SDK.
func ReadSecret(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0027 != 0 {
		return "", errors.New("server secret must be a regular file without world access or group write permission")
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 64<<10 {
		return "", errors.New("server secret exceeds 64 KiB")
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" || strings.ContainsAny(secret, "\r\n\x00") {
		return "", errors.New("server secret must contain one nonempty line")
	}
	return secret, nil
}
