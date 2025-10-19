package runtime

import "errors"

var (
	ErrNoRuntimeAvailable = errors.New("no container runtime (docker or containerd) available")
	ErrImageNotFound      = errors.New("image not found")
)
