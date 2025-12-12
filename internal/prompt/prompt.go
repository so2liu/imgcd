package prompt

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// PromptSelection prompts the user to select from a list of options.
// Returns the selected option or error if invalid input.
func PromptSelection(prompt string, options []string) (string, error) {
	fmt.Printf("%s\n", prompt)
	for i, opt := range options {
		fmt.Printf("  [%d] %s\n", i+1, opt)
	}
	fmt.Print("Enter number: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	input = strings.TrimSpace(input)
	num, err := strconv.Atoi(input)
	if err != nil || num < 1 || num > len(options) {
		return "", fmt.Errorf("invalid selection: %s", input)
	}

	return options[num-1], nil
}
