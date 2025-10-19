package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type ContainerdRuntime struct {
	ctrPath string
}

func NewContainerdRuntime() (*ContainerdRuntime, error) {
	// Check if ctr (containerd CLI) is available
	ctrPath, err := exec.LookPath("ctr")
	if err != nil {
		return nil, fmt.Errorf("ctr (containerd CLI) not available: %w", err)
	}

	// Test if containerd is actually running
	cmd := exec.Command(ctrPath, "version")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("containerd not available: %w", err)
	}

	return &ContainerdRuntime{ctrPath: ctrPath}, nil
}

func (c *ContainerdRuntime) Name() string {
	return "containerd"
}

func (c *ContainerdRuntime) GetImage(ctx context.Context, ref string) (*ImageInfo, error) {
	// Try to check if image exists
	info, err := c.checkImage(ctx, ref)
	if err == nil {
		return info, nil
	}

	// If image not found, try to pull it
	fmt.Printf("Image %s not found locally, pulling...\n", ref)
	if err := c.pullImage(ctx, ref); err != nil {
		return nil, fmt.Errorf("failed to pull image: %w", err)
	}

	// Try to check again after pulling
	return c.checkImage(ctx, ref)
}

func (c *ContainerdRuntime) checkImage(ctx context.Context, ref string) (*ImageInfo, error) {
	// Use ctr to check if image exists
	cmd := exec.CommandContext(ctx, c.ctrPath, "image", "ls", fmt.Sprintf("name==%s", ref))
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list image: %w", err)
	}

	if len(output) == 0 {
		return nil, ErrImageNotFound
	}

	// For containerd, we'll use a simpler approach
	return &ImageInfo{
		Reference: ref,
		ID:        ref,
		Layers:    []LayerInfo{},
		RepoTags:  []string{ref},
	}, nil
}

func (c *ContainerdRuntime) pullImage(ctx context.Context, ref string) error {
	cmd := exec.CommandContext(ctx, c.ctrPath, "image", "pull", ref)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *ContainerdRuntime) SaveImage(ctx context.Context, ref, outputPath string) error {
	// Use ctr export to save image
	cmd := exec.CommandContext(ctx, c.ctrPath, "image", "export", outputPath, ref)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to export image: %w", err)
	}
	return nil
}

func (c *ContainerdRuntime) LoadImage(ctx context.Context, inputPath string) error {
	// Use ctr import to load image
	cmd := exec.CommandContext(ctx, c.ctrPath, "image", "import", inputPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to import image: %w\nOutput: %s", err, string(output))
	}
	return nil
}

func (c *ContainerdRuntime) LoadImageFromReader(ctx context.Context, r io.Reader) error {
	cmd := exec.CommandContext(ctx, c.ctrPath, "image", "import", "-")
	cmd.Stdin = r
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to import image: %w", err)
	}

	return nil
}

func (c *ContainerdRuntime) Close() error {
	return nil
}
