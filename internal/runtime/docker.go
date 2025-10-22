package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type DockerRuntime struct{}

func NewDockerRuntime() (*DockerRuntime, error) {
	// Check if docker is available
	cmd := exec.Command("docker", "version")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker not available: %w", err)
	}

	return &DockerRuntime{}, nil
}

func (d *DockerRuntime) Name() string {
	return "docker"
}

func (d *DockerRuntime) GetImage(ctx context.Context, ref string) (*ImageInfo, error) {
	// Try to inspect the image
	info, err := d.inspectImage(ctx, ref)
	if err == nil {
		return info, nil
	}

	// If image not found, try to pull it
	fmt.Printf("Image %s not found locally, pulling...\n", ref)
	if err := d.pullImage(ctx, ref, ""); err != nil {
		return nil, fmt.Errorf("failed to pull image: %w", err)
	}

	// Try to inspect again after pulling
	return d.inspectImage(ctx, ref)
}

func (d *DockerRuntime) GetImageWithPlatform(ctx context.Context, ref, platform string) (*ImageInfo, error) {
	// Try to inspect the image
	info, err := d.inspectImage(ctx, ref)
	if err == nil {
		// Image exists locally, check if it's the right platform
		// For now, we'll assume it's the right platform if it exists
		// TODO: Add platform verification
		return info, nil
	}

	// If image not found, try to pull it with platform specification
	fmt.Printf("Image %s not found locally, pulling for platform %s...\n", ref, platform)
	if err := d.pullImage(ctx, ref, platform); err != nil {
		return nil, fmt.Errorf("failed to pull image: %w", err)
	}

	// Try to inspect again after pulling
	return d.inspectImage(ctx, ref)
}

func (d *DockerRuntime) inspectImage(ctx context.Context, ref string) (*ImageInfo, error) {
	// Use docker inspect to get image information
	cmd := exec.CommandContext(ctx, "docker", "inspect", ref)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect image: %w", err)
	}

	var inspectData []dockerInspectOutput
	if err := json.Unmarshal(output, &inspectData); err != nil {
		return nil, fmt.Errorf("failed to parse inspect output: %w", err)
	}

	if len(inspectData) == 0 {
		return nil, ErrImageNotFound
	}

	imageData := inspectData[0]

	// Build layer info
	layers := make([]LayerInfo, len(imageData.RootFS.Layers))
	for i, layerID := range imageData.RootFS.Layers {
		layers[i] = LayerInfo{
			Digest:    layerID,
			Size:      0, // Docker inspect doesn't provide layer sizes easily
			MediaType: "application/vnd.docker.image.rootfs.diff.tar.gzip",
			Exists:    true,
		}
	}

	return &ImageInfo{
		Reference: ref,
		ID:        imageData.ID,
		Layers:    layers,
		RepoTags:  imageData.RepoTags,
	}, nil
}

func (d *DockerRuntime) pullImage(ctx context.Context, ref, platform string) error {
	args := []string{"pull"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	args = append(args, ref)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (d *DockerRuntime) SaveImage(ctx context.Context, ref, outputPath string) error {
	// Use docker save to export image
	cmd := exec.CommandContext(ctx, "docker", "save", "-o", outputPath, ref)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to save image: %w", err)
	}
	return nil
}

func (d *DockerRuntime) LoadImage(ctx context.Context, inputPath string) error {
	// Use docker load to import image
	f, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("failed to open image file: %w", err)
	}
	defer f.Close()

	cmd := exec.CommandContext(ctx, "docker", "load")
	cmd.Stdin = f
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to load image: %w\nOutput: %s", err, string(output))
	}

	return nil
}

func (d *DockerRuntime) LoadImageFromReader(ctx context.Context, r io.Reader) error {
	cmd := exec.CommandContext(ctx, "docker", "load")
	cmd.Stdin = r
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to load image: %w", err)
	}

	return nil
}

func (d *DockerRuntime) Close() error {
	return nil
}

// dockerInspectOutput represents the output of docker inspect
type dockerInspectOutput struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	RootFS   struct {
		Type   string   `json:"Type"`
		Layers []string `json:"Layers"`
	} `json:"RootFS"`
}
